package main

import (
	"net"
	"slices"
	"sync/atomic"
	"time"
)

const (
	TcpTunnelListenMode = iota
	TcpTunnelForwardMode
)

type TcpTunnelConnPool struct {
	conns      []*TcpTunnelConn
	index      uint32
	remoteAddr string
	poolID     PoolID
	connCount  int
}

func (p *TcpTunnelConnPool) AddConnection(conn *TcpTunnelConn) {
	p.conns = append(p.conns, conn)
}

func (p *TcpTunnelConnPool) RemoveConnection(conn *TcpTunnelConn) {
	for i, c := range p.conns {
		if c == conn {
			p.conns = slices.Delete(p.conns, i, i+1)
			return
		}
	}
}

func (p *TcpTunnelConnPool) GetNextConn() *TcpTunnelConn {
	if len(p.conns) == 0 {
		return nil
	}

	for i, n := 0, len(p.conns); i < n; i++ {
		index := atomic.AddUint32(&p.index, 1) % uint32(len(p.conns))

		conn := p.conns[index]
		if conn == nil || conn.conn == nil {
			logger.Warnf("TcpTunnelConnPool: Connection at index %d is nil or closed", index)
			continue
		}

		if !conn.authState.IsAuthenticated() {
			logger.Infof("TcpTunnelConnPool: Connection at index %d is not authenticated", index)
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
}

func TcpTunnelLoopRead(c *TcpTunnelConn, t TcpTunnelComponent, mode int) {

	c.authState.lastHeartbeat = time.Now()

	// Buffer to accumulate incoming data
	buffer := t.GetRouter().GetBuffer()
	defer t.GetRouter().PutBuffer(buffer)
	defer t.Disconnect(c)
	bufferUsed := 0

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

			n, err := c.conn.Read(buffer[bufferUsed:])
			if err != nil {
				logger.Infof("%s: %s Read error: %v", t.GetTag(), c.conn.RemoteAddr(), err)
				return
			}

			bufferUsed += n

			processedBytes := 0
			for processedBytes < bufferUsed {
				if bufferUsed-processedBytes < HeaderSize {
					break
				}

				// Parse header
				header, err := ParseHeader(buffer[processedBytes:])
				if err != nil {
					logger.Infof("%s: %s Invalid header: %v", t.GetTag(), c.conn.RemoteAddr(), err)
					return
				}

				totalMessageSize := HeaderSize + int(header.Length)

				// If we don't have the full message yet, wait for more data
				if bufferUsed-processedBytes < totalMessageSize {
					break
				}

				// We have a complete message, process it
				messageBuffer := buffer[processedBytes : processedBytes+totalMessageSize]

				if c.authState.IsAuthenticated() {
					if header.MsgType == MsgTypeData {
						packet := t.GetRouter().GetPacket(t.GetTag())
						packet.length = totalMessageSize
						copy(packet.buffer[packet.offset:], messageBuffer)

						_, err := t.GetAuthManager().UnwrapData(&packet)
						if err == nil {
							t.GetRouter().Route(&packet, t.GetDetour())
						} else {
							logger.Infof("%s: %s Failed to unwrap data: %v", t.GetTag(), c.conn.RemoteAddr(), err)
							packet.Release(1)
						}
					} else if header.MsgType == MsgTypeHeartbeat {
						c.authState.UpdateHeartbeat()

						if mode == TcpTunnelListenMode {
							responseBuffer := t.GetRouter().GetBuffer()
							length := CreateHeartbeat(responseBuffer)
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
						data := messageBuffer[HeaderSize:totalMessageSize]
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
				processedBytes += totalMessageSize

			}

			// Shift any remaining partial message to the beginning of the buffer
			if processedBytes > 0 {
				copy(buffer, buffer[processedBytes:bufferUsed])
				bufferUsed -= processedBytes
			}

			// Check if buffer is full but we couldn't process anything - likely a malformed packet
			if bufferUsed == len(buffer) && processedBytes == 0 {
				logger.Infof("%s: %s Buffer full but no complete message, likely malformed data", t.GetTag(), c.conn.RemoteAddr())
				return
			}
		}
	}
}
