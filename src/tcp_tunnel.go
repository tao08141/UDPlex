package main

import (
	"net"
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
	authState  *AuthState
	lastActive time.Time

	heartbeatMissCount int
	lastHeartbeatSent  time.Time
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

func TcpTunnelLoopRead(c *TcpTunnelConn, t TcpTunnelComponent, mode int) {
	c.authState.lastHeartbeat = time.Now()

	// Buffer to accumulate incoming data
	buffer := t.GetRouter().GetBuffer()

	defer func() {
		if buffer != nil {
			t.GetRouter().PutBuffer(buffer)
		}
	}()

	defer t.Disconnect(c)
	bufferUsed := 0
	bufferOffset := t.GetRouter().config.BufferOffset

	expectedTotalSize := 0

	for {
		select {
		case <-t.GetStopChannel():
			logger.Infof("%s: Stopping connection handling for %s", t.GetTag(), c.conn.RemoteAddr())
			return
		default:
			if c.authState.IsAuthenticated() {
				c.conn.SetReadDeadline(time.Now().Add(t.GetAuthManager().dataTimeout))
			} else {
				c.conn.SetReadDeadline(time.Now().Add(t.GetAuthManager().authTimeout))
			}

			// Calculate read size based on whether we have partial message
			readSize := len(buffer) - (bufferOffset + bufferUsed)

			if readSize <= 0 {
				logger.Infof("%s: %s Buffer full, processing messages", t.GetTag(), c.conn.RemoteAddr())
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
				logger.Infof("%s: %s Read error: %v", t.GetTag(), c.conn.RemoteAddr(), err)
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
					logger.Infof("%s: %s Invalid header: %v", t.GetTag(), c.conn.RemoteAddr(), err)
					return
				}

				if c.authState.IsAuthenticated() {
					if header.Length > uint32(t.GetRouter().config.BufferSize) {
						logger.Infof("%s: %s Message size %d exceeds maximum allowed size %d", t.GetTag(), c.conn.RemoteAddr(), header.Length, t.GetRouter().config.BufferSize)
						return
					}
				} else {
					if header.Length != HandshakeSize {
						logger.Infof("%s: %s Received unexpected message size %d before authentication", t.GetTag(), c.conn.RemoteAddr(), header.Length)
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
							packet = t.GetRouter().GetPacketWithBuffer(t.GetTag(), buffer, bufferOffset+processedBytes)
							packet.length = expectedTotalSize
							buffer = t.GetRouter().GetBuffer()
						} else {
							packet = t.GetRouter().GetPacket(t.GetTag())
							packet.length = expectedTotalSize
							copy(packet.buffer[packet.offset:], messageBuffer)
						}

						_, err := t.GetAuthManager().UnwrapData(&packet)
						if err == nil {
							t.GetRouter().Route(&packet, t.GetDetour())
						} else {
							if err.Error() != "duplicate packet detected" {
								logger.Infof("%s: %s Failed to unwrap data: %v", t.GetTag(), c.conn.RemoteAddr(), err)
							}
						}

						packet.Release(1)
					} else if header.MsgType == MsgTypeHeartbeat {
						c.authState.UpdateHeartbeat()

						if mode == TcpTunnelListenMode {
							responseBuffer := t.GetRouter().GetBuffer()
							length := CreateHeartbeat(responseBuffer)
							if t.GetSendTimeout() > 0 {
								if err := c.conn.SetWriteDeadline(time.Now().Add(t.GetSendTimeout())); err != nil {
									logger.Infof("%s: Failed to set write deadline: %v", t.GetTag(), err)
								}
							}
							c.conn.Write(responseBuffer[:length])
							t.GetRouter().PutBuffer(responseBuffer)
						} else if mode == TcpTunnelForwardMode {
							c.heartbeatMissCount = 0
						}
					} else if header.MsgType == MsgTypeDisconnect {
						logger.Infof("%s: %s Client requested disconnect", t.GetTag(), c.conn.RemoteAddr())
						return
					}
				} else {
					// Handle authentication
					if (mode == TcpTunnelListenMode && header.MsgType == MsgTypeAuthChallenge) || (mode == TcpTunnelForwardMode && header.MsgType == MsgTypeAuthResponse) {
						data := messageBuffer[HeaderSize:expectedTotalSize]
						forwardID, poolID, err := t.GetAuthManager().ProcessAuthChallenge(data, c.authState)
						if err != nil {
							logger.Infof("%s: %s Failed to process auth challenge: %v", t.GetTag(), c.conn.RemoteAddr(), err)
							return
						}

						c.forwardID = forwardID
						c.poolID = poolID

						err = t.HandleAuthenticatedConnection(c)

						if err != nil {
							logger.Infof("%s: %s Failed to handle authenticated connection: %v", t.GetTag(), c.conn.RemoteAddr(), err)
							return
						}

						logger.Infof("%s: %s Authentication successful, forwardID: %x, poolID: %x", t.GetTag(), c.conn.RemoteAddr(), c.forwardID, c.poolID)

					} else {
						logger.Infof("%s: %s Received unexpected message type %d before authentication", t.GetTag(), c.conn.RemoteAddr(), header.MsgType)
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
				logger.Infof("%s: %s Buffer full but no complete message, likely malformed data", t.GetTag(), c.conn.RemoteAddr())
				return
			}
		}
	}
}
