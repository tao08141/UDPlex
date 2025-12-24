package main

import (
	"crypto/rand"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type TcpTunnelForwardComponent struct {
	BaseComponent

	connectionCheckTime time.Duration
	detour              []string
	broadcastMode       bool
	forwardID           ForwardID
	authManager         *AuthManager
	pools               map[PoolID]*TcpTunnelConnPool
	noDelay             bool

	recvBufferSize int
	sendBufferSize int
}

func NewTcpTunnelForwardComponent(cfg ComponentConfig, router *Router) *TcpTunnelForwardComponent {
	connectionCheckTime := time.Duration(cfg.ConnectionCheckTime) * time.Second
	if connectionCheckTime == 0 {
		connectionCheckTime = 10 * time.Second
	}

	authManager, err := NewAuthManager(cfg.Auth, router)
	if err != nil {
		logger.Errorf("Failed to create auth manager: %v", err)
		return nil
	}

	broadcastMode := true
	if cfg.BroadcastMode != nil && !*cfg.BroadcastMode {
		broadcastMode = false
	}

	noDelay := true
	if cfg.NoDelay != nil && !*cfg.NoDelay {
		noDelay = false
	}

	sendTimeout := time.Duration(cfg.SendTimeout) * time.Millisecond
	if sendTimeout == 0 {
		sendTimeout = 500 * time.Millisecond
	}

	recvBufferSize := cfg.RecvBufferSize
	sendBufferSize := cfg.SendBufferSize

	forwardID := ForwardID{}
	_, err = rand.Read(forwardID[:])
	if err != nil {
		return nil
	}

	pools := make(map[PoolID]*TcpTunnelConnPool)

	for _, fwd := range cfg.Forwarders {
		addr, count, perr := parseForwarderAddress(fwd)
		if perr != nil {
			logger.Warnf("Invalid forwarder address '%s': %v", fwd, perr)
			continue
		}

		poolID := PoolID{}
		_, err = rand.Read(poolID[:])
		if err != nil {
			return nil
		}

		pools[poolID] = NewTcpTunnelConnPool(addr, poolID, count)
	}

	return &TcpTunnelForwardComponent{
		BaseComponent:       NewBaseComponent(cfg.Tag, router, sendTimeout),
		connectionCheckTime: connectionCheckTime,
		broadcastMode:       broadcastMode,
		authManager:         authManager,
		forwardID:           forwardID,
		pools:               pools,
		detour:              cfg.Detour,
		noDelay:             noDelay,
		recvBufferSize:      recvBufferSize,
		sendBufferSize:      sendBufferSize,
	}
}

// parseForwarderAddress parses a forwarder string in one of the following formats:
// - host:port
// - host:port:count
// - [ipv6]:port
// - [ipv6]:port:count
// It returns a dialable address (with IPv6 properly bracketed) and the connection count (default 4).
func parseForwarderAddress(s string) (string, int, error) {
	// Default connection count
	count := 4
	addrPart := s

	// Try to peel off an optional trailing ":count"
	if idx := strings.LastIndex(addrPart, ":"); idx != -1 {
		tail := addrPart[idx+1:]
		if n, err := strconv.Atoi(tail); err == nil {
			if n >= 1 {
				count = n
			}
			addrPart = addrPart[:idx]
		}
	}

	// Validate and normalize address using net.SplitHostPort / JoinHostPort
	host, port, err := net.SplitHostPort(addrPart)
	if err != nil {
		return "", 0, fmt.Errorf("invalid host:port '%s': %w", addrPart, err)
	}
	if port == "" {
		return "", 0, fmt.Errorf("missing port in '%s'", s)
	}
	addr := net.JoinHostPort(host, port)
	return addr, count, nil
}

func (f *TcpTunnelForwardComponent) Start() error {
	if len(f.pools) == 0 {
		err := fmt.Errorf("%s: No forwarders configured", f.tag)
		logger.Warn(err)
		return err
	}

	if f.authManager == nil {
		logger.Warnf("%s: AuthManager is nil, cannot start component", f.tag)
		return fmt.Errorf("%s: AuthManager is nil", f.tag)
	}

	logger.Infof("%s: Starting TCP tunnel forward component with ID %x", f.tag, f.forwardID)

	for poolID, pool := range f.pools {
		if pool == nil {
			logger.Warnf("%s: Pool for %x is nil, skipping", f.tag, poolID)
			continue
		}
		logger.Infof("%s: Forwarding to %s with %d connections", f.tag, pool.remoteAddr, pool.connCount)

		for range pool.connCount {
			ttc, err := f.setupConnection(pool.remoteAddr, pool.poolID)
			if err != nil {
				logger.Errorf("%s: Failed to setup connection to %s: %v", f.tag, pool.remoteAddr, err)
				continue
			}
			pool.AddConnection(ttc)

			logger.Infof("%s: Added new connection to %s", f.tag, pool.remoteAddr)
		}
	}

	go f.connectionChecker()

	return nil
}

func (f *TcpTunnelForwardComponent) Stop() error {
	close(f.stopCh)

	// Close all connections in all pools
	for poolID, pool := range f.pools {
		if pool == nil {
			logger.Warnf("%s: Pool for %x is nil, skipping", f.tag, poolID)
			continue
		}

		// Get current slice of connections atomically
		connsPtr := pool.conns.Load()
		conns := *connsPtr

		for i := range conns {
			conn := conns[i]
			if conn == nil {
				continue
			}

			if conn.conn != nil {
				logger.Infof("%s: Closing connection to %s", f.tag, conn.conn.RemoteAddr())
			} else {
				logger.Infof("%s: Closing connection (already nil) in pool %x", f.tag, poolID)
			}

		}
	}

	return nil
}

func (f *TcpTunnelForwardComponent) setupConnection(addr string, poolID PoolID) (*TcpTunnelConn, error) {
	dialer := net.Dialer{}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	if f.noDelay {
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			err := tcpConn.SetNoDelay(true)
			if err != nil {
				return nil, err
			}
		}
	}

	// 设置TCP缓冲区大小
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if f.recvBufferSize > 0 {
			_ = tcpConn.SetReadBuffer(f.recvBufferSize)
		}
		if f.sendBufferSize > 0 {
			_ = tcpConn.SetWriteBuffer(f.sendBufferSize)
		}
	}

	ttc := NewTcpTunnelConn(conn, f.forwardID, poolID, f, f.router.config.QueueSize, TcpTunnelForwardMode)

	packet := f.router.GetPacket(f.GetTag())
	defer packet.Release(1)

	length, err := f.authManager.CreateAuthChallenge(packet.BufAtOffset(), MsgTypeAuthChallenge, f.forwardID, poolID)
	if err != nil {
		logger.Warnf("%s: Failed to create auth challenge: %v", f.tag, err)
		ttc.Close()
		return nil, err
	}

	packet.SetLength(length)

	err = ttc.Write(&packet)
	if err != nil {
		logger.Warnf("%s: Failed to send auth challenge to %s: %v", f.tag, addr, err)
		ttc.Close()
		return nil, err
	}

	return ttc, nil
}

func (f *TcpTunnelForwardComponent) GetDetour() []string {
	return f.detour
}

func (f *TcpTunnelForwardComponent) GetRouter() *Router {
	return f.router
}

func (f *TcpTunnelForwardComponent) GetAuthManager() *AuthManager {
	return f.authManager
}

// IsAvailable checks if the component has at least one valid connection
func (f *TcpTunnelForwardComponent) IsAvailable() bool {
	for _, pool := range f.pools {
		if pool == nil {
			continue
		}

		// Get current slice of connections atomically
		connsPtr := pool.conns.Load()
		if connsPtr == nil {
			continue
		}

		conns := *connsPtr
		for i := range conns {
			if conns[i] != nil && conns[i].conn != nil {
				return true
			}
		}
	}

	return false
}

func (f *TcpTunnelForwardComponent) sendHeartbeat(c *TcpTunnelConn) {
	if !c.authState.IsAuthenticated() {
		return
	}

	packet := f.router.GetPacket(f.GetTag())
	defer packet.Release(1)

	length := CreateHeartbeat(packet.BufAtOffset())
	c.lastHeartbeatSent = time.Now()
	packet.SetLength(length)

	err := c.WriteHighPriority(&packet)
	if err != nil {
		logger.Warnf("%s: Failed to send heartbeat: %v", f.tag, err)
		return
	}

	c.heartbeatMissCount++
	if c.heartbeatMissCount >= 5 {
		logger.Warnf("%s: Heartbeat missed %d times, disconnecting", f.tag, c.heartbeatMissCount)
		f.Disconnect(c)
		return
	}

}

func (f *TcpTunnelForwardComponent) connectionChecker() {
	connectionCheckTicker := time.NewTicker(f.connectionCheckTime)
	defer connectionCheckTicker.Stop()
	heartbeatIntervalTicker := time.NewTicker(f.authManager.heartbeatInterval)
	defer heartbeatIntervalTicker.Stop()

	for {
		select {
		case <-f.GetStopChannel():
			return
		case <-connectionCheckTicker.C:

			for poolID, pool := range f.pools {
				if pool == nil {
					logger.Warnf("%s: Pool for %x is nil, skipping", f.tag, poolID)
					continue
				}

				// Get current slice of connections atomically
				connsPtr := pool.conns.Load()
				conns := *connsPtr

				// Track connections that need to be removed
				var connectionsToRemove []*TcpTunnelConn

				for i := range conns {
					// Get reference to the connection
					if conns[i] == nil {
						logger.Warnf("%s: Connection in pool %s is nil, adding to removal list", f.tag, pool.remoteAddr)
						continue
					}

					if conns[i].conn == nil {
						logger.Infof("%s: Connection in pool %s is nil or closed, adding to removal list", f.tag, pool.remoteAddr)
						connectionsToRemove = append(connectionsToRemove, conns[i])
						continue
					}
				}

				for _, conn := range connectionsToRemove {
					pool.RemoveConnection(conn)
				}

				for i := pool.ConnectionCount(); i < pool.connCount; i++ {
					ttc, err := f.setupConnection(pool.remoteAddr, pool.poolID)
					if err != nil {
						logger.Errorf("%s: Failed to setup connection to %s: %v", f.tag, pool.remoteAddr, err)
						continue
					}
					pool.AddConnection(ttc)
					logger.Infof("%s: Added new connection to %s", f.tag, pool.remoteAddr)
				}
			}
		case <-heartbeatIntervalTicker.C:
			for poolID, pool := range f.pools {
				if pool == nil {
					logger.Warnf("%s: Pool for %x is nil, skipping", f.tag, poolID)
					continue
				}

				// Get current slice of connections atomically
				connsPtr := pool.conns.Load()
				conns := *connsPtr

				for i := range conns {
					conn := conns[i]
					if conn == nil || conn.conn == nil {
						logger.Warnf("%s: Connection in pool %s is nil or closed, skipping", f.tag, pool.remoteAddr)
						continue
					}

					go f.sendHeartbeat(conn)
				}
			}
		}
	}
}
func (l *TcpTunnelForwardComponent) HandleAuthenticatedConnection(c *TcpTunnelConn) error {
	c.authState.SetAuthenticated(1)
	return nil
}

func (f *TcpTunnelForwardComponent) Disconnect(c *TcpTunnelConn) {
	if c == nil {
		return
	}

	logger.Infof("%s: Disconnecting %s", f.tag, c.conn.RemoteAddr())

	defer c.Close()

	if _, exists := f.pools[c.poolID]; !exists {
		logger.Warnf("%s: Pool %x does not exist, cannot remove connection", f.tag, c.poolID)
		return
	}

	f.pools[c.poolID].RemoveConnection(c)
}

func (f *TcpTunnelForwardComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	err := f.authManager.WrapData(packet)
	if err != nil {
		return err
	}

	if f.broadcastMode {
		for _, pool := range f.pools {
			c := pool.GetNextConn()
			if c == nil {
				logger.Debugf("%s: No available connections in pool %s", f.tag, pool.remoteAddr)
				continue
			}

			if err := c.Write(packet); err != nil {
				//remote := "<closed>"
				//if c.conn != nil {
				//	remote = c.conn.RemoteAddr().String()
				//}
				//logger.Infof("%s: Failed to send packet to %s: %v", f.tag, remote, err)
				continue
			}
		}

	} else {

	}

	return nil
}
