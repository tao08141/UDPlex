package main

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	TcpTunnelListenMode = iota
	TcpTunnelForwardMode
	tcpTunnelWriteBatchSize = 64
	tcpTunnelReadBufferSize = 64 * 1024
)

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type TcpTunnelConnPool struct {
	conns atomic.Pointer[[]*TcpTunnelConn]

	index      uint32
	remoteAddr string
	poolID     PoolID
	connCount  int
}

func NewTcpTunnelConnPool(addr string, poolID PoolID, count int) *TcpTunnelConnPool {
	pool := &TcpTunnelConnPool{
		remoteAddr: addr,
		poolID:     poolID,
		connCount:  count,
	}

	emptySlice := make([]*TcpTunnelConn, 0, count)
	pool.conns.Store(&emptySlice)

	return pool
}

func (p *TcpTunnelConnPool) ConnectionCount() int {
	connsPtr := p.conns.Load()
	return len(*connsPtr)
}

func (p *TcpTunnelConnPool) AddConnection(conn *TcpTunnelConn) {
	for {
		oldSlicePtr := p.conns.Load()
		oldSlice := *oldSlicePtr

		newSlice := make([]*TcpTunnelConn, len(oldSlice)+1)
		copy(newSlice, oldSlice)
		newSlice[len(oldSlice)] = conn

		if p.conns.CompareAndSwap(oldSlicePtr, &newSlice) {
			return
		}
	}
}

func (p *TcpTunnelConnPool) RemoveConnection(conn *TcpTunnelConn) {
	for {
		oldSlicePtr := p.conns.Load()
		oldSlice := *oldSlicePtr

		foundIndex := -1
		for i, c := range oldSlice {
			if c == conn {
				foundIndex = i
				break
			}
		}

		if foundIndex == -1 {
			logger.Warnf("Connection not found in pool %s for removal", p.remoteAddr)
			return
		}

		newSlice := make([]*TcpTunnelConn, len(oldSlice)-1)
		copy(newSlice, oldSlice[:foundIndex])
		copy(newSlice[foundIndex:], oldSlice[foundIndex+1:])

		if p.conns.CompareAndSwap(oldSlicePtr, &newSlice) {
			return
		}
	}
}

func (p *TcpTunnelConnPool) GetNextConn() *TcpTunnelConn {
	connsPtr := p.conns.Load()
	conns := *connsPtr

	if len(conns) == 0 {
		return nil
	}

	for i, n := 0, len(conns); i < n; i++ {
		index := atomic.AddUint32(&p.index, 1) % uint32(len(conns))

		conn := conns[index]

		if conn == nil || conn.conn == nil {
			logger.Warnf("TcpTunnelConnPool: Connection at index %d is nil or closed", index)
			continue
		}

		select {
		case <-conn.closed:
			continue
		default:
		}

		if !conn.authState.IsAuthenticated() {
			continue
		}

		return conn
	}

	return nil
}

type TcpTunnelConn struct {
	connID     ConnID
	forwardID  ForwardID
	poolID     PoolID
	conn       net.Conn
	t          *TcpTunnelComponent
	authState  *AuthState
	lastActive time.Time

	heartbeatMissCount int
	lastHeartbeatSent  time.Time

	writeQueue chan *Packet
	writePrio  chan *Packet
	writeWg    sync.WaitGroup
	closed     chan struct{}
	closeOnce  sync.Once
}

func NewTcpTunnelConn(conn net.Conn, forwardID ForwardID, poolID PoolID, t TcpTunnelComponent, queueSize int, mode int) *TcpTunnelConn {
	connID := ConnID{}
	if _, err := rand.Read(connID[:]); err != nil {
		connID = ConnID{}
	}

	c := &TcpTunnelConn{
		connID:     connID,
		forwardID:  forwardID,
		poolID:     poolID,
		conn:       conn,
		authState:  &AuthState{},
		lastActive: time.Now(),
		// Split write queue into priority (heartbeat/control) and normal (data).
		// Priority queue is small but always preferred by the writer.
		writePrio:  make(chan *Packet, max(4, queueSize/16)),
		writeQueue: make(chan *Packet, queueSize), // Buffered channel for normal packets
		closed:     make(chan struct{}),
		closeOnce:  sync.Once{},
		writeWg:    sync.WaitGroup{},
		t:          &t,
	}

	// Start to write goroutine
	c.writeWg.Add(1)
	go c.writeLoop()
	c.writeWg.Add(1)
	go c.readLoop(mode)

	return c
}

func (c *TcpTunnelConn) ConnID() ConnID {
	return c.connID
}

func (c *TcpTunnelConn) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		if c.conn != nil {
			_ = c.conn.Close()
		}
		// Close the queue and drain any pending packets to avoid refcount leaks.
		// Writers may still attempt to enqueue; Write() guards against panics.
		close(c.writePrio)
		for pkt := range c.writePrio {
			if pkt != nil {
				pkt.Release(1)
			}
		}
		close(c.writeQueue)
		for pkt := range c.writeQueue {
			if pkt != nil {
				pkt.Release(1)
			}
		}
	})
}

// Wait blocks until the connection's read/write goroutines exit.
func (c *TcpTunnelConn) Wait() {
	c.writeWg.Wait()
}

type TcpTunnelComponent interface {
	GetDetour() []string
	GetRouter() *Router
	GetTag() string
	GetStopChannel() chan struct{}
	GetAuthManager() *AuthManager
	HandleAuthenticatedConnection(c *TcpTunnelConn) error
	Disconnect(c *TcpTunnelConn)
	GetSendTimeout() time.Duration
}

type TcpTunnelConnIDTracker interface {
	RememberConnID(connID ConnID, c *TcpTunnelConn)
}

func (c *TcpTunnelConn) writeLoop() {
	defer c.writeWg.Done()

	remote := "<closed>"
	if c.conn != nil {
		remote = c.conn.RemoteAddr().String()
	}
	defer logger.Infof("Write loop for %s exiting", remote)

	releaseBatch := func(packets []*Packet) {
		for _, packet := range packets {
			if packet != nil {
				packet.Release(1)
			}
		}
	}

	sendPacketBatch := func(packets []*Packet) bool {
		if len(packets) == 0 {
			return true
		}
		if c.conn == nil {
			releaseBatch(packets)
			return true
		}

		if (*c.t).GetSendTimeout() > 0 {
			if err := c.conn.SetWriteDeadline(time.Now().Add((*c.t).GetSendTimeout())); err != nil {
				logger.Infof("Failed to set write deadline: %v", err)
				releaseBatch(packets)
				return false
			}
		}

		buffers := make(net.Buffers, 0, len(packets))
		for _, packet := range packets {
			if packet == nil {
				continue
			}
			buffers = append(buffers, packet.GetData())
		}

		for len(buffers) > 0 {
			_, err := buffers.WriteTo(c.conn)
			if err != nil {
				logger.Infof("Write error: %v", err)
				releaseBatch(packets)
				(*c.t).Disconnect(c)
				return false
			}
		}

		releaseBatch(packets)
		return true
	}

	drainBatch := func(first *Packet, preferPriority bool) []*Packet {
		batch := make([]*Packet, 0, tcpTunnelWriteBatchSize)
		if first != nil {
			batch = append(batch, first)
		}

		drain := func(ch <-chan *Packet) {
			for len(batch) < tcpTunnelWriteBatchSize {
				select {
				case packet, ok := <-ch:
					if !ok {
						return
					}
					batch = append(batch, packet)
				default:
					return
				}
			}
		}

		if preferPriority {
			drain(c.writePrio)
			drain(c.writeQueue)
		} else {
			drain(c.writeQueue)
			drain(c.writePrio)
		}

		return batch
	}

	for {
		select {
		case <-c.closed:
			logger.Infof("Write loop for %s closed", remote)
			return
		case <-(*c.t).GetStopChannel():
			logger.Infof("%s: Stopping connection handling for %s", (*c.t).GetTag(), remote)
			return
		case packet, ok := <-c.writePrio:
			if !ok {
				logger.Infof("Write queue for %s closed", remote)
				return
			}
			if !sendPacketBatch(drainBatch(packet, true)) {
				return
			}
		case packet, ok := <-c.writeQueue:
			if !ok {
				logger.Infof("Write queue for %s closed", remote)
				return
			}
			if !sendPacketBatch(drainBatch(packet, false)) {
				return
			}
		}
	}
}

func (c *TcpTunnelConn) Write(packet *Packet) error {

	if c.conn == nil {
		return net.ErrClosed
	}
	packet.AddRef(1)

	select {
	case <-c.closed:
		packet.Release(1)
		return net.ErrClosed
	default:
	}

	defer func() {
		if recover() != nil {
			packet.Release(1)
		}
	}()

	select {
	case c.writeQueue <- packet:
		return nil
	default:
		packet.Release(1)
		return fmt.Errorf("write queue full, dropping packet")
	}
}

func (c *TcpTunnelConn) WriteHighPriority(packet *Packet) error {
	if c.conn == nil {
		return net.ErrClosed
	}
	packet.AddRef(1)

	select {
	case <-c.closed:
		packet.Release(1)
		return net.ErrClosed
	default:
	}

	defer func() {
		if recover() != nil {
			packet.Release(1)
		}
	}()

	select {
	case c.writePrio <- packet:
		return nil
	default:
		// Priority queue is full. As a fallback, try normal queue.
		select {
		case c.writeQueue <- packet:
			return nil
		default:
			packet.Release(1)
			return fmt.Errorf("write priority queue full, dropping packet")
		}
	}
}

func (c *TcpTunnelConn) readLoop(mode int) {
	defer c.writeWg.Done()

	remote := "<closed>"
	if c.conn != nil {
		remote = c.conn.RemoteAddr().String()
	}
	component := *c.t
	router := component.GetRouter()
	tag := component.GetTag()
	stopCh := component.GetStopChannel()
	authManager := component.GetAuthManager()
	detour := component.GetDetour()
	remoteAddr := c.conn.RemoteAddr()
	readerSize := tcpTunnelReadBufferSize
	if minReaderSize := router.config.BufferSize + router.config.BufferOffset; readerSize < minReaderSize {
		readerSize = minReaderSize
	}
	reader := bufio.NewReaderSize(c.conn, readerSize)

	// Buffer to accumulate incoming data
	buffer := router.GetBuffer()

	defer func() {
		if buffer != nil {
			router.PutBuffer(buffer)
		}
	}()

	defer logger.Infof("%s: Read loop for %s exiting", tag, remote)

	defer c.Close()
	defer component.Disconnect(c)
	bufferUsed := 0
	bufferOffset := router.config.BufferOffset
	maxPayloadSize := router.config.BufferSize
	var lastDeadlineTimeout time.Duration
	var lastDeadlineUpdate time.Time
	var lastTrackedConnID ConnID
	lastTrackedConnIDSet := false

	refreshReadDeadline := func(timeout time.Duration) error {
		if timeout <= 0 {
			return nil
		}

		now := time.Now()
		refreshInterval := timeout / 4
		if refreshInterval <= 0 {
			refreshInterval = timeout
		}

		if timeout == lastDeadlineTimeout && !lastDeadlineUpdate.IsZero() && now.Sub(lastDeadlineUpdate) < refreshInterval {
			return nil
		}

		if err := c.conn.SetReadDeadline(now.Add(timeout)); err != nil {
			return err
		}

		lastDeadlineTimeout = timeout
		lastDeadlineUpdate = now
		return nil
	}

	expectedTotalSize := 0

	for {
		select {
		case <-c.closed:
			logger.Infof("Read loop for %s closed", remote)
			return
		case <-stopCh:
			logger.Infof("%s: Stopping connection handling for %s", tag, remote)
			return
		default:
			timeout := time.Duration(0)
			if c.authState.IsAuthenticated() {
				timeout = authManager.dataTimeout
			} else {
				timeout = authManager.authTimeout
			}

			if err := refreshReadDeadline(timeout); err != nil {
				logger.Infof("%s: %s Failed to set read deadline: %v", tag, remoteAddr, err)
				return
			}

			// Calculate read size based on whether we have a partial message
			readSize := len(buffer) - (bufferOffset + bufferUsed)

			if readSize <= 0 {
				logger.Infof("%s: %s Buffer full, processing messages", tag, remoteAddr)
				return
			}

			// If we have a partial message with known size, only read what's needed
			if expectedTotalSize > 0 {
				bytesNeeded := expectedTotalSize - bufferUsed
				if bytesNeeded < readSize {
					readSize = bytesNeeded
				}
			}

			n, err := reader.Read(buffer[bufferOffset+bufferUsed : bufferOffset+bufferUsed+readSize])
			if err != nil {
				logger.Infof("%s: %s Read error: %v", tag, remoteAddr, err)
				return
			}

			bufferUsed += n

			processedBytes := 0
			for processedBytes < bufferUsed {
				// If we don't have an expected size yet, we need to parse the header
				if bufferUsed-processedBytes < HeaderSize {
					expectedTotalSize = 0
					break // Not enough for a header
				}

				headerStart := bufferOffset + processedBytes
				headerLength := int(binary.BigEndian.Uint32(buffer[headerStart+4 : headerStart+8]))
				msgType := buffer[headerStart+1]

				if c.authState.IsAuthenticated() {
					if headerLength > maxPayloadSize {
						logger.Infof("%s: %s Message size %d exceeds maximum allowed size %d", tag, remoteAddr, headerLength, maxPayloadSize)
						return
					}
				} else {
					if headerLength != HandshakeSize {
						logger.Infof("%s: %s Received unexpected message size %d before authentication", tag, remoteAddr, headerLength)
						return
					}
				}

				expectedTotalSize = HeaderSize + headerLength

				// If we don't have the full message yet, wait for more data
				if bufferUsed-processedBytes < expectedTotalSize {
					break
				}

				// We have a complete message, process it
				messageBuffer := buffer[bufferOffset+processedBytes : bufferOffset+processedBytes+expectedTotalSize]

				if c.authState.IsAuthenticated() {
					switch msgType {
					case MsgTypeData:
						var packet Packet

						if bufferUsed-processedBytes == expectedTotalSize {
							packet = router.GetPacketWithBuffer(tag, buffer, bufferOffset+processedBytes)
							packet.SetLength(expectedTotalSize)
							buffer = router.GetBuffer()
						} else {
							packet = router.GetPacket(tag)
							packet.SetLength(expectedTotalSize)
							copy(packet.BufAtOffset(), messageBuffer)
						}

						_, err := authManager.UnwrapData(&packet)
						if err == nil {
							if packet.ConnID() == (ConnID{}) {
								packet.SetConnID(c.connID)
							}
							if tracker, ok := (*c.t).(TcpTunnelConnIDTracker); ok && packet.ConnID() != (ConnID{}) {
								if !lastTrackedConnIDSet || packet.ConnID() != lastTrackedConnID {
									tracker.RememberConnID(packet.ConnID(), c)
									lastTrackedConnID = packet.ConnID()
									lastTrackedConnIDSet = true
								}
							}
							// Set source address for downstream components (TCP remote)
							packet.SetSrcAddr(remoteAddr)
							err := router.Route(&packet, detour)
							if err != nil {
								logger.Infof("%s: %s Failed to route packet: %v", tag, remoteAddr, err)
							}
						} else {
							if err.Error() != "duplicate packet detected" {
								logger.Infof("%s: %s Failed to unwrap data: %v", tag, remoteAddr, err)
							}
						}

						packet.Release(1)
					case MsgTypeHeartbeat:
						switch mode {
						case TcpTunnelListenMode:
							// Echo heartbeat back
							packet := router.GetPacket(tag)
							length := CreateHeartbeat(packet.BufAtOffset())
							packet.SetLength(length)
							err := c.WriteHighPriority(&packet)
							if err != nil {
								logger.Infof("%s: %s Failed to write heartbeat packet: %v", tag, remoteAddr, err)
							}
							c.lastHeartbeatSent = time.Now()
							packet.Release(1)

						case TcpTunnelForwardMode:
							c.heartbeatMissCount = 0

							// If this is the second heartbeat (response to our response), measure delay
							if !c.lastHeartbeatSent.IsZero() {
								delay := time.Since(c.lastHeartbeatSent)
								if authManager != nil {
									authManager.RecordDelayMeasurement(delay)
								}
							}

							packet := router.GetPacket(tag)
							length := CreateHeartbeatAck(packet.BufAtOffset())
							packet.SetLength(length)
							err := c.WriteHighPriority(&packet)
							if err != nil {
								logger.Infof("%s: %s Failed to write heartbeat packet: %v", tag, remoteAddr, err)
							}
							packet.Release(1)
						}
					case MsgTypeHeartbeatAck:
						if !c.lastHeartbeatSent.IsZero() {
							delay := time.Since(c.lastHeartbeatSent)
							if authManager != nil {
								authManager.RecordDelayMeasurement(delay)
							}
							c.lastHeartbeatSent = time.Time{} // Reset after processing
						}
					case MsgTypeDisconnect:
						logger.Infof("%s: %s Client requested disconnect", tag, remoteAddr)
						return
					}
				} else {
					// Handle authentication
					if (mode == TcpTunnelListenMode && msgType == MsgTypeAuthChallenge) || (mode == TcpTunnelForwardMode && msgType == MsgTypeAuthResponse) {
						data := messageBuffer[HeaderSize:expectedTotalSize]
						forwardID, poolID, err := authManager.ProcessAuthChallenge(data)
						if err != nil {
							logger.Infof("%s: %s Failed to process auth challenge: %v", tag, remoteAddr, err)
							return
						}

						c.forwardID = forwardID
						c.poolID = poolID

						err = component.HandleAuthenticatedConnection(c)

						if err != nil {
							logger.Infof("%s: %s Failed to handle authenticated connection: %v", tag, remoteAddr, err)
							return
						}

						logger.Infof("%s: %s Authentication successful, forwardID: %x, poolID: %x", tag, remoteAddr, c.forwardID, c.poolID)

					} else {
						logger.Infof("%s: %s Received unexpected message type %d before authentication", tag, remoteAddr, msgType)
						return
					}
				}

				processedBytes += expectedTotalSize
				expectedTotalSize = 0 // Reset for the next message
			}

			// Shift any remaining partial message to the beginning of the buffer
			if processedBytes > 0 {
				if processedBytes < bufferUsed {
					copy(buffer[bufferOffset:], buffer[bufferOffset+processedBytes:bufferOffset+bufferUsed])
				}
				bufferUsed -= processedBytes
			}

			// Check if the buffer is full, but we couldn't process anything - likely a malformed packet
			if bufferUsed == len(buffer[bufferOffset:]) && processedBytes == 0 {
				logger.Infof("%s: %s Buffer full but no complete message, likely malformed data", tag, remoteAddr)
				return
			}
		}
	}
}
