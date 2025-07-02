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

	forwardID := ForwardID{}
	rand.Read(forwardID[:])

	pools := make(map[PoolID]*TcpTunnelConnPool)

	for _, fwd := range cfg.Forwarders {

		addrParts := strings.Split(fwd, ":")
		if len(addrParts) < 2 {
			logger.Warnf("Invalid forwarder address: %s", fwd)
			continue
		}
		addr := strings.Join(addrParts[:2], ":")
		count := 4
		if len(addrParts) == 3 {
			var err error
			count, err = strconv.Atoi(addrParts[2])
			if err != nil || count < 1 {
				logger.Warnf("Invalid connection count for %s, using default %d", addr, count)
			}
		}

		poolID := PoolID{}
		rand.Read(poolID[:])

		pools[poolID] = NewTcpTunnelConnPool(addr, poolID, count)
	}

	return &TcpTunnelForwardComponent{
		BaseComponent:       NewBaseComponent(cfg.Tag, router),
		connectionCheckTime: connectionCheckTime,
		broadcastMode:       broadcastMode,
		authManager:         authManager,
		forwardID:           forwardID,
		pools:               pools,
		detour:              cfg.Detour,
		noDelay:             noDelay,
	}
}

func (f *TcpTunnelForwardComponent) Start() error {
	if len(f.pools) == 0 {
		logger.Warnf("%s: No forwarders configured, skipping start", f.tag)
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
			tcpConn.SetNoDelay(true)
			logger.Infof("%s: TCP_NODELAY enabled for connection to %s", f.tag, addr)
		}
	}

	ttc := &TcpTunnelConn{
		conn:       conn,
		authState:  &AuthState{},
		poolID:     poolID,
		forwardID:  f.forwardID,
		lastActive: time.Now(),
	}

	buffer := f.router.GetBuffer()
	defer f.router.PutBuffer(buffer)

	length, err := f.authManager.CreateAuthChallenge(buffer, MsgTypeAuthChallenge, f.forwardID, poolID)
	if err != nil {
		logger.Warnf("%s: Failed to create auth challenge: %v", f.tag, err)
		conn.Close()
		return nil, err
	}

	_, err = conn.Write(buffer[:length])

	if err != nil {
		logger.Warnf("%s: Failed to send auth challenge to %s: %v", f.tag, addr, err)
		conn.Close()
		return nil, err
	}

	go TcpTunnelLoopRead(ttc, f, TcpTunnelForwardMode)

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

func (f *TcpTunnelForwardComponent) sendHeartbeat(conn *TcpTunnelConn) {
	if !conn.authState.IsAuthenticated() {
		return
	}

	buffer := f.router.GetBuffer()
	defer f.router.PutBuffer(buffer)

	length := CreateHeartbeat(buffer)
	conn.lastHeartbeatSent = time.Now()

	_, err := conn.conn.Write(buffer[:length])
	if err != nil {
		logger.Warnf("%s: Failed to send heartbeat: %v", f.tag, err)
		conn.conn.Close()
		return
	}

	conn.heartbeatMissCount++
	if conn.heartbeatMissCount >= 5 {
		logger.Warnf("%s: Heartbeat missed %d times, disconnecting", f.tag, conn.heartbeatMissCount)
		conn.conn.Close()
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
					if conn == nil || (*conn).conn == nil {
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
	c.authState.authenticated = 1
	return nil
}

func (f *TcpTunnelForwardComponent) Disconnect(c *TcpTunnelConn) {
	if c == nil {
		return
	}

	logger.Infof("%s: Disconnecting %s", f.tag, c.conn.RemoteAddr())

	defer c.conn.Close()

	if _, exists := f.pools[c.poolID]; !exists {
		logger.Warnf("%s: Pool %x does not exist, cannot remove connection", f.tag, c.poolID)
		return
	}

	f.pools[c.poolID].RemoveConnection(c)
}

func (f *TcpTunnelForwardComponent) SendPacket(packet *Packet, metadata any) error {

	conn, ok := metadata.(net.Conn)
	if !ok {
		return fmt.Errorf("%s: Invalid connection type", f.tag)
	}

	if conn == nil {
		return fmt.Errorf("%s: Connection is nil", f.tag)
	}

	_, err := conn.Write(packet.GetData())
	if err != nil {
		logger.Infof("%s: Failed to send packet: %v", f.tag, err)
		return err
	}
	return err
}

func (f *TcpTunnelForwardComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	f.authManager.WrapData(packet)

	if f.broadcastMode {

		for _, pool := range f.pools {
			c := pool.GetNextConn()
			if c == nil {
				logger.Debugf("%s: No available connections in pool %s", f.tag, pool.remoteAddr)
				continue
			}

			if err := f.router.SendPacket(f, packet, c.conn); err != nil {
				logger.Infof("%s: Failed to send packet to %s: %v", f.tag, c.conn.RemoteAddr(), err)
				continue
			}
		}

	} else {

	}

	return nil
}
