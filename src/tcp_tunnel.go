package main

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	TcpTunnelListenMode = iota
	TcpTunnelForwardMode
)

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
		if conn.conn == nil {
			logger.Warnf("TcpTunnelConnPool: Connection at index %d is nil or closed", index)
			continue
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
		writeQueue: make(chan *Packet, queueSize), // Buffered channel for write queue
		closed:     make(chan struct{}),
		closeOnce:  sync.Once{},
		writeWg:    sync.WaitGroup{},
		t:          &t,
	}

	// Start write goroutine
	c.writeWg.Add(1)
	go c.writeLoop()
	c.writeWg.Add(1)
	go c.readLoop(mode)

	return c
}

func (c *TcpTunnelConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		close(c.writeQueue)
		c.writeWg.Wait() // Wait for write goroutine to finish
		if c.conn != nil {
			c.conn.Close()
		}
	})
	return nil
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
	defer c.Close()

	for {
		select {
		case <-c.closed:
			logger.Infof("Write loop for %s closed", c.conn.RemoteAddr())
			return
		case <-(*c.t).GetStopChannel():
			logger.Infof("%s: Stopping connection handling for %s", (*c.t).GetTag(), c.conn.RemoteAddr())
			return
		case packet := <-c.writeQueue:
			if c.conn == nil {
				packet.Release(1)
				return
			}

			// Ensure all data is written
			totalWritten := 0
			for totalWritten < packet.length {
				if (*c.t).GetSendTimeout() > 0 {
					if err := c.conn.SetWriteDeadline(time.Now().Add((*c.t).GetSendTimeout())); err != nil {
						logger.Infof("Failed to set write deadline: %v", err)
						packet.Release(1)
						return
					}
				}

				n, err := c.conn.Write(packet.GetData()[totalWritten:])
				if err != nil {
					logger.Infof("Write error: %v", err)
					packet.Release(1)
					return
				}
				totalWritten += n
			}

			packet.Release(1)
		}
	}
}

func (c *TcpTunnelConn) Write(packet *Packet) error {

	if c.conn == nil {
		return net.ErrClosed
	}

	select {
	case c.writeQueue <- packet:
		return nil
	default:
		// Queue is full, drop the packet or handle error
		return net.ErrClosed
	}
}

func (c *TcpTunnelConn) readLoop(mode int) {
	defer c.writeWg.Done()

	// Buffer to accumulate incoming data
	buffer := (*c.t).GetRouter().GetBuffer()

	defer func() {
		if buffer != nil {
			(*c.t).GetRouter().PutBuffer(buffer)
		}
	}()

	defer c.Close()
	defer (*c.t).Disconnect(c)
	bufferUsed := 0
	bufferOffset := (*c.t).GetRouter().config.BufferOffset

	expectedTotalSize := 0

	for {
		select {
		case <-c.closed:
			logger.Infof("Read loop for %s closed", c.conn.RemoteAddr())
			return
		case <-(*c.t).GetStopChannel():
			logger.Infof("%s: Stopping connection handling for %s", (*c.t).GetTag(), c.conn.RemoteAddr())
			return
		default:
			if c.authState.IsAuthenticated() {
				c.conn.SetReadDeadline(time.Now().Add((*c.t).GetAuthManager().dataTimeout))
			} else {
				c.conn.SetReadDeadline(time.Now().Add((*c.t).GetAuthManager().authTimeout))
			}

			// Calculate read size based on whether we have partial message
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
				// If we don't have an expected size yet, we need to parse header
				if bufferUsed-processedBytes < HeaderSize {
					expectedTotalSize = 0
					break // Not enough for header
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
					if header.MsgType == MsgTypeData {
						var packet Packet

						if bufferUsed-processedBytes == expectedTotalSize {
							packet = (*c.t).GetRouter().GetPacketWithBuffer((*c.t).GetTag(), buffer, bufferOffset+processedBytes)
							packet.length = expectedTotalSize
							buffer = (*c.t).GetRouter().GetBuffer()
						} else {
							packet = (*c.t).GetRouter().GetPacket((*c.t).GetTag())
							packet.length = expectedTotalSize
							copy(packet.buffer[packet.offset:], messageBuffer)
						}

						_, err := (*c.t).GetAuthManager().UnwrapData(&packet)
						if err == nil {
							(*c.t).GetRouter().Route(&packet, (*c.t).GetDetour())
						} else {
							if err.Error() != "duplicate packet detected" {
								logger.Infof("%s: %s Failed to unwrap data: %v", (*c.t).GetTag(), c.conn.RemoteAddr(), err)
							}
						}

						packet.Release(1)
					} else if header.MsgType == MsgTypeHeartbeat {
						if mode == TcpTunnelListenMode {
							packet := (*c.t).GetRouter().GetPacket((*c.t).GetTag())
							length := CreateHeartbeat(packet.buffer[packet.offset:])
							packet.length = length
							c.Write(&packet)
						} else if mode == TcpTunnelForwardMode {
							c.heartbeatMissCount = 0
						}
					} else if header.MsgType == MsgTypeDisconnect {
						logger.Infof("%s: %s Client requested disconnect", (*c.t).GetTag(), c.conn.RemoteAddr())
						return
					}
				} else {
					// Handle authentication
					if (mode == TcpTunnelListenMode && header.MsgType == MsgTypeAuthChallenge) || (mode == TcpTunnelForwardMode && header.MsgType == MsgTypeAuthResponse) {
						data := messageBuffer[HeaderSize:expectedTotalSize]
						forwardID, poolID, err := (*c.t).GetAuthManager().ProcessAuthChallenge(data, c.authState)
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
				expectedTotalSize = 0 // Reset for next message
			}

			// Shift any remaining partial message to the beginning of the buffer
			if processedBytes > 0 {
				if processedBytes < bufferUsed {
					copy(buffer[bufferOffset:], buffer[bufferOffset+processedBytes:bufferOffset+bufferUsed])
				}
				bufferUsed -= processedBytes
			}

			// Check if buffer is full but we couldn't process anything - likely a malformed packet
			if bufferUsed == len(buffer[bufferOffset:]) && processedBytes == 0 {
				logger.Infof("%s: %s Buffer full but no complete message, likely malformed data", (*c.t).GetTag(), c.conn.RemoteAddr())
				return
			}
		}
	}
}
