package main

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	TcpTunnelListenMode = iota
	TcpTunnelForwardMode
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
	c := &TcpTunnelConn{
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

func (c *TcpTunnelConn) writeLoop() {
	defer c.writeWg.Done()

	remote := "<closed>"
	if c.conn != nil {
		remote = c.conn.RemoteAddr().String()
	}
	defer logger.Infof("Write loop for %s exiting", remote)

	sendPacket := func(packet *Packet) bool {
		if packet == nil {
			return true
		}
		if c.conn == nil {
			packet.Release(1)
			return true
		}

		totalWritten := 0
		for totalWritten < packet.length {
			if (*c.t).GetSendTimeout() > 0 {
				if err := c.conn.SetWriteDeadline(time.Now().Add((*c.t).GetSendTimeout())); err != nil {
					logger.Infof("Failed to set write deadline: %v", err)
					packet.Release(1)
					return false
				}
			}

			n, err := c.conn.Write(packet.GetData()[totalWritten:])
			if err != nil {
				logger.Infof("Write error: %v", err)
				packet.Release(1)
				(*c.t).Disconnect(c)
				return false
			}
			totalWritten += n
		}
		packet.Release(1)
		return true
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
			if !sendPacket(packet) {
				return
			}
		case packet, ok := <-c.writeQueue:
			if !ok {
				logger.Infof("Write queue for %s closed", remote)
				return
			}
			if !sendPacket(packet) {
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

	// Buffer to accumulate incoming data
	buffer := (*c.t).GetRouter().GetBuffer()

	defer func() {
		if buffer != nil {
			(*c.t).GetRouter().PutBuffer(buffer)
		}
	}()

	defer logger.Infof("%s: Read loop for %s exiting", (*c.t).GetTag(), remote)

	defer c.Close()
	defer (*c.t).Disconnect(c)
	bufferUsed := 0
	bufferOffset := (*c.t).GetRouter().config.BufferOffset

	expectedTotalSize := 0

	for {
		select {
		case <-c.closed:
			logger.Infof("Read loop for %s closed", remote)
			return
		case <-(*c.t).GetStopChannel():
			logger.Infof("%s: Stopping connection handling for %s", (*c.t).GetTag(), remote)
			return
		default:
			var err error
			if c.authState.IsAuthenticated() {
				err = c.conn.SetReadDeadline(time.Now().Add((*c.t).GetAuthManager().dataTimeout))
			} else {
				err = c.conn.SetReadDeadline(time.Now().Add((*c.t).GetAuthManager().authTimeout))
			}
			if err != nil {
				logger.Infof("%s: %s Failed to set read deadline: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
				return
			}

			// Calculate read size based on whether we have a partial message
			readSize := len(buffer) - (bufferOffset + bufferUsed)

			if readSize <= 0 {
				logger.Infof("%s: %s Buffer full, processing messages", (*c.t).GetTag(), c.conn.RemoteAddr())
				return
			}

			// If we have a partial message with known size, only read what's needed
			if expectedTotalSize > 0 {
				bytesNeeded := expectedTotalSize - bufferUsed
				if bytesNeeded < readSize {
					readSize = bytesNeeded
				}
			}

			n, err := c.conn.Read(buffer[bufferOffset+bufferUsed : bufferOffset+bufferUsed+readSize])
			if err != nil {
				logger.Infof("%s: %s Read error: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
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

				// Parse header
				header, err := ParseHeader(buffer[bufferOffset+processedBytes:])
				if err != nil {
					logger.Infof("%s: %s Invalid header: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
					return
				}

				if c.authState.IsAuthenticated() {
					if header.Length > uint32((*c.t).GetRouter().config.BufferSize) {
						logger.Infof("%s: %s Message size %d exceeds maximum allowed size %d", (*c.t).GetTag(), c.conn.RemoteAddr(), header.Length, (*c.t).GetRouter().config.BufferSize)
						return
					}
				} else {
					if header.Length != HandshakeSize {
						logger.Infof("%s: %s Received unexpected message size %d before authentication", (*c.t).GetTag(), c.conn.RemoteAddr(), header.Length)
						return
					}
				}

				expectedTotalSize = HeaderSize + int(header.Length)

				// If we don't have the full message yet, wait for more data
				if bufferUsed-processedBytes < expectedTotalSize {
					break
				}

				// We have a complete message, process it
				messageBuffer := buffer[bufferOffset+processedBytes : bufferOffset+processedBytes+expectedTotalSize]

				if c.authState.IsAuthenticated() {
					switch header.MsgType {
					case MsgTypeData:
						var packet Packet

						if bufferUsed-processedBytes == expectedTotalSize {
							packet = (*c.t).GetRouter().GetPacketWithBuffer((*c.t).GetTag(), buffer, bufferOffset+processedBytes)
							packet.SetLength(expectedTotalSize)
							buffer = (*c.t).GetRouter().GetBuffer()
						} else {
							packet = (*c.t).GetRouter().GetPacket((*c.t).GetTag())
							packet.SetLength(expectedTotalSize)
							copy(packet.BufAtOffset(), messageBuffer)
						}

						_, err := (*c.t).GetAuthManager().UnwrapData(&packet)
						if err == nil {
							// Set source address for downstream components (TCP remote)
							packet.SetSrcAddr(c.conn.RemoteAddr())
							err := (*c.t).GetRouter().Route(&packet, (*c.t).GetDetour())
							if err != nil {
								logger.Infof("%s: %s Failed to route packet: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
							}
						} else {
							if err.Error() != "duplicate packet detected" {
								logger.Infof("%s: %s Failed to unwrap data: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
							}
						}

						packet.Release(1)
					case MsgTypeHeartbeat:
						switch mode {
						case TcpTunnelListenMode:
							// Echo heartbeat back
							packet := (*c.t).GetRouter().GetPacket((*c.t).GetTag())
							length := CreateHeartbeat(packet.BufAtOffset())
							packet.SetLength(length)
							err := c.WriteHighPriority(&packet)
							if err != nil {
								logger.Infof("%s: %s Failed to write heartbeat packet: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
							}
							c.lastHeartbeatSent = time.Now()
							packet.Release(1)

						case TcpTunnelForwardMode:
							c.heartbeatMissCount = 0

							// If this is the second heartbeat (response to our response), measure delay
							if !c.lastHeartbeatSent.IsZero() {
								delay := time.Since(c.lastHeartbeatSent)
								if (*c.t).GetAuthManager() != nil {
									(*c.t).GetAuthManager().RecordDelayMeasurement(delay)
								}
							}

							packet := (*c.t).GetRouter().GetPacket((*c.t).GetTag())
							length := CreateHeartbeatAck(packet.BufAtOffset())
							packet.SetLength(length)
							err := c.WriteHighPriority(&packet)
							if err != nil {
								logger.Infof("%s: %s Failed to write heartbeat packet: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
							}
							packet.Release(1)
						}
					case MsgTypeHeartbeatAck:
						if !c.lastHeartbeatSent.IsZero() {
							delay := time.Since(c.lastHeartbeatSent)
							if (*c.t).GetAuthManager() != nil {
								(*c.t).GetAuthManager().RecordDelayMeasurement(delay)
							}
							c.lastHeartbeatSent = time.Time{} // Reset after processing
						}
					case MsgTypeDisconnect:
						logger.Infof("%s: %s Client requested disconnect", (*c.t).GetTag(), c.conn.RemoteAddr())
						return
					}
				} else {
					// Handle authentication
					if (mode == TcpTunnelListenMode && header.MsgType == MsgTypeAuthChallenge) || (mode == TcpTunnelForwardMode && header.MsgType == MsgTypeAuthResponse) {
						data := messageBuffer[HeaderSize:expectedTotalSize]
						forwardID, poolID, err := (*c.t).GetAuthManager().ProcessAuthChallenge(data)
						if err != nil {
							logger.Infof("%s: %s Failed to process auth challenge: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
							return
						}

						c.forwardID = forwardID
						c.poolID = poolID

						err = (*c.t).HandleAuthenticatedConnection(c)

						if err != nil {
							logger.Infof("%s: %s Failed to handle authenticated connection: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
							return
						}

						logger.Infof("%s: %s Authentication successful, forwardID: %x, poolID: %x", (*c.t).GetTag(), c.conn.RemoteAddr(), c.forwardID, c.poolID)

					} else {
						logger.Infof("%s: %s Received unexpected message type %d before authentication", (*c.t).GetTag(), c.conn.RemoteAddr(), header.MsgType)
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
				logger.Infof("%s: %s Buffer full but no complete message, likely malformed data", (*c.t).GetTag(), c.conn.RemoteAddr())
				return
			}
		}
	}
}
