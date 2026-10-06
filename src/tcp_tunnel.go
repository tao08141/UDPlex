package main

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	TcpTunnelListenMode = iota
	TcpTunnelForwardMode
	defaultTcpTunnelWriteBatchSize = 64
	tcpTunnelReadBufferSize        = 64 * 1024
	// A pool keeps sending on one connection until its backlog exceeds the
	// least loaded connection by this much, so packets are reordered only at
	// these switches instead of on every packet.
	tcpTunnelSwitchBacklog = 16 << 10
)

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type TcpTunnelConnPool struct {
	conns atomic.Pointer[[]*TcpTunnelConn]

	index         uint32
	remoteAddr    string
	targetSpec    string
	interfaceName string
	poolID        PoolID
	connCount     int
	connecting    atomic.Int32
	current       atomic.Pointer[TcpTunnelConn]
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

func (p *TcpTunnelConnPool) RouteLabel() string {
	if p == nil {
		return ""
	}
	return formatOutboundRoute(p.remoteAddr, p.interfaceName)
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

// usable reports whether conn can carry data.
func (c *TcpTunnelConn) usable() bool {
	if c == nil || c.conn == nil || !c.authState.IsAuthenticated() {
		return false
	}
	select {
	case <-c.closed:
		return false
	default:
		return true
	}
}

// PickConn chooses the connection for a data packet of the given size. With
// priorityConn and at least two connections, the first one carries only
// packets up to prioritySize, so they never wait behind bulk data or its
// retransmissions.
func (p *TcpTunnelConnPool) PickConn(size, prioritySize int, priorityConn bool) *TcpTunnelConn {
	conns := *p.conns.Load()
	var reserved *TcpTunnelConn
	if priorityConn && prioritySize > 0 && len(conns) >= 2 {
		if size <= prioritySize && conns[0].usable() {
			return conns[0]
		}
		reserved, conns = conns[0], conns[1:]
	}
	cur := p.current.Load()
	var best *TcpTunnelConn
	var bestBacklog int64
	curOK := false
	for _, c := range conns {
		if !c.usable() {
			continue
		}
		b := c.queue.backlog()
		if best == nil || b < bestBacklog {
			best, bestBacklog = c, b
		}
		if c == cur {
			curOK = true
		}
	}
	if best == nil {
		if reserved.usable() {
			return reserved
		}
		return nil
	}
	if curOK && cur.queue.backlog() <= bestBacklog+tcpTunnelSwitchBacklog {
		return cur
	}
	p.current.Store(best)
	return best
}

type TcpTunnelConn struct {
	connID     ConnID
	forwardID  ForwardID
	poolID     PoolID
	conn       net.Conn
	t          *TcpTunnelComponent
	authState  *AuthState
	lastActive time.Time

	heartbeatTracker

	queue            *tcpTunnelQueue
	enableWriteBatch bool
	writeBatchSize   int
	writeWg          sync.WaitGroup
	closed           chan struct{}
	closeOnce        sync.Once
}

func normalizeTcpTunnelWriteBatchSize(size int) int {
	if size <= 0 {
		return defaultTcpTunnelWriteBatchSize
	}
	return size
}

func NewTcpTunnelConn(conn net.Conn, forwardID ForwardID, poolID PoolID, t TcpTunnelComponent, qs *tcpQueueSettings, qstats *tcpQueueStats, enableWriteBatch bool, writeBatchSize int, mode int) *TcpTunnelConn {
	connID := ConnID{}
	if _, err := rand.Read(connID[:]); err != nil {
		connID = ConnID{}
	}

	writeBatchSize = normalizeTcpTunnelWriteBatchSize(writeBatchSize)

	c := &TcpTunnelConn{
		connID:           connID,
		forwardID:        forwardID,
		poolID:           poolID,
		conn:             conn,
		authState:        &AuthState{},
		lastActive:       time.Now(),
		queue:            newTcpTunnelQueue(qs, qstats),
		enableWriteBatch: enableWriteBatch,
		writeBatchSize:   writeBatchSize,
		closed:           make(chan struct{}),
		closeOnce:        sync.Once{},
		writeWg:          sync.WaitGroup{},
		t:                &t,
	}
	c.SetHeartbeatStatsTracker(t.GetHeartbeatStatsTracker())
	qs.applySocketOptions(conn, t.GetTag())

	// Start to write goroutine
	c.writeWg.Add(1)
	go c.writeLoop()
	c.writeWg.Add(1)
	go c.readLoop(mode)

	return c
}

// queueDelay returns the largest send queue delay over the pool's connections.
func (p *TcpTunnelConnPool) queueDelay(now time.Time) time.Duration {
	var d time.Duration
	for _, c := range *p.conns.Load() {
		if c != nil && c.queue != nil {
			if q := c.queue.delay(now); q > d {
				d = q
			}
		}
	}
	return d
}

func (c *TcpTunnelConn) ConnID() ConnID {
	return c.connID
}

func (c *TcpTunnelConn) Close() {
	c.closeOnce.Do(func() {
		c.MarkPendingHeartbeatLost()
		close(c.closed)
		if c.conn != nil {
			_ = c.conn.Close()
		}
		// Release pending packets; later writes are dropped by the closed queue.
		c.queue.close()
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
	GetHeartbeatStatsTracker() *heartbeatStatsTracker
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

	var lastWriteDeadlineTimeout time.Duration
	var lastWriteDeadlineUpdate time.Time

	refreshWriteDeadline := func(timeout time.Duration) error {
		if timeout <= 0 {
			return nil
		}

		now := time.Now()
		refreshInterval := timeout / 4
		if refreshInterval <= 0 {
			refreshInterval = timeout
		}

		if timeout == lastWriteDeadlineTimeout && !lastWriteDeadlineUpdate.IsZero() && now.Sub(lastWriteDeadlineUpdate) < refreshInterval {
			return nil
		}

		if err := c.conn.SetWriteDeadline(now.Add(timeout)); err != nil {
			return err
		}

		lastWriteDeadlineTimeout = timeout
		lastWriteDeadlineUpdate = now
		return nil
	}

	batch := make([]*Packet, 0, c.writeBatchSize)
	buffers := make(net.Buffers, 0, c.writeBatchSize)
	currentPacketIndex := 0
	currentPacketOffset := 0

	releaseBatch := func() {
		for i := range batch {
			if batch[i] != nil {
				batch[i].Release(1)
				batch[i] = nil
			}
		}
		batch = batch[:0]
		buffers = buffers[:0]
		currentPacketIndex = 0
		currentPacketOffset = 0
	}

	advanceBatchWrite := func(written int64) bool {
		for written > 0 && currentPacketIndex < len(batch) {
			packet := batch[currentPacketIndex]
			if packet == nil {
				currentPacketIndex++
				currentPacketOffset = 0
				continue
			}

			packetLen := len(packet.GetData()) - currentPacketOffset
			if packetLen <= 0 {
				packet.Release(1)
				batch[currentPacketIndex] = nil
				currentPacketIndex++
				currentPacketOffset = 0
				continue
			}

			if written < int64(packetLen) {
				currentPacketOffset += int(written)
				return true
			}

			written -= int64(packetLen)
			packet.Release(1)
			batch[currentPacketIndex] = nil
			currentPacketIndex++
			currentPacketOffset = 0
		}

		return false
	}

	sendPacketBatch := func() bool {
		if len(batch) == 0 {
			return true
		}
		if c.conn == nil {
			releaseBatch()
			return true
		}

		if sendTimeout := (*c.t).GetSendTimeout(); sendTimeout > 0 {
			if err := refreshWriteDeadline(sendTimeout); err != nil {
				logger.Infof("Failed to set write deadline: %v", err)
				releaseBatch()
				return false
			}
		}

		buffers = buffers[:0]
		for _, packet := range batch {
			if packet == nil {
				continue
			}
			buffers = append(buffers, packet.GetData())
		}
		currentPacketIndex = 0
		currentPacketOffset = 0

		for len(buffers) > 0 {
			n, err := buffers.WriteTo(c.conn)
			progressMade := n > 0
			advanceBatchWrite(n)
			if err != nil {
				if isTimeoutError(err) {
					if progressMade {
						logger.Infof("Write timeout after partial progress, retrying remaining bytes: %v", err)
						lastWriteDeadlineUpdate = time.Time{}
						if sendTimeout := (*c.t).GetSendTimeout(); sendTimeout > 0 {
							if deadlineErr := refreshWriteDeadline(sendTimeout); deadlineErr != nil {
								logger.Infof("Failed to refresh write deadline after timeout: %v", deadlineErr)
								releaseBatch()
								(*c.t).Disconnect(c)
								return false
							}
						}
						continue
					}

					logger.Infof("Write timeout without progress: %v", err)
					releaseBatch()
					(*c.t).Disconnect(c)
					return false
				}

				logger.Infof("Write error: %v", err)
				releaseBatch()
				(*c.t).Disconnect(c)
				return false
			}
		}

		releaseBatch()
		return true
	}

	limit := 1
	if c.enableWriteBatch {
		limit = c.writeBatchSize
	}
	lowat := c.queue.s.notsentLowat
	var lastTune time.Time

	for {
		now := time.Now()
		batch = c.queue.pop(now, batch[:0], limit)
		if len(batch) > 0 {
			if !sendPacketBatch() {
				return
			}
			// Keep about tcpAutoLowatTime of unsent data in the kernel.
			if c.queue.s.autoLowat && now.Sub(lastTune) >= tcpAutoLowatTune {
				lastTune = now
				if info, ok := getTCPInfo(c.conn); ok {
					if want := c.queue.s.tunedLowat(info.DeliveryRate, lowat); want > 0 && setTCPNotsentLowat(c.conn, want) == nil {
						lowat = want
					}
				}
			}
			continue
		}
		select {
		case <-c.closed:
			logger.Infof("Write loop for %s closed", remote)
			return
		case <-(*c.t).GetStopChannel():
			logger.Infof("%s: Stopping connection handling for %s", (*c.t).GetTag(), remote)
			return
		case <-c.queue.ready:
		}
	}
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	return errors.Is(err, os.ErrDeadlineExceeded)
}

func (c *TcpTunnelConn) Write(packet *Packet) error {
	return c.enqueue(packet, false)
}

// WriteHighPriority queues a control message ahead of all data.
func (c *TcpTunnelConn) WriteHighPriority(packet *Packet) error {
	return c.enqueue(packet, true)
}

func (c *TcpTunnelConn) enqueue(packet *Packet, control bool) error {
	if c.conn == nil {
		return net.ErrClosed
	}
	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	packet.AddRef(1)
	if !c.queue.push(packet, control, time.Now()) {
		return fmt.Errorf("write queue full, dropping packet")
	}
	return nil
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
							c.MarkPendingHeartbeatLost()
							// Echo heartbeat back
							packet := router.GetPacket(tag)
							length := CreateHeartbeat(packet.BufAtOffset())
							packet.SetLength(length)
							err := c.WriteHighPriority(&packet)
							if err != nil {
								logger.Infof("%s: %s Failed to write heartbeat packet: %v", tag, remoteAddr, err)
							}
							c.NoteHeartbeatSent()
							packet.Release(1)

						case TcpTunnelForwardMode:
							c.MarkHeartbeatResponse()

							// If this is the second heartbeat (response to our response), measure delay
							if lastHeartbeatSent := c.LastHeartbeatSent(); !lastHeartbeatSent.IsZero() {
								delay := time.Since(lastHeartbeatSent)
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
						c.MarkHeartbeatResponse()
						if lastHeartbeatSent := c.LastHeartbeatSent(); !lastHeartbeatSent.IsZero() {
							delay := time.Since(lastHeartbeatSent)
							if authManager != nil {
								authManager.RecordDelayMeasurement(delay)
							}
							c.ClearLastHeartbeatSent()
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
