package main

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type TcpTunnelListenComponent struct {
	BaseComponent

	listenAddr        string
	timeout           time.Duration
	replaceOldMapping bool
	detour            []string
	broadcastMode     bool
	noDelay           bool
	connections       atomic.Value
	authManager       *AuthManager
	sendTimeout       time.Duration
	listener          net.Listener
	recvBufferSize    int
	sendBufferSize    int
	writeBatchSize    int
	connIndex         sync.Map
}

func NewTcpTunnelListenComponent(cfg ComponentConfig, router *Router) *TcpTunnelListenComponent {
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second // Default timeout
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
	writeBatchSize := normalizeTcpTunnelWriteBatchSize(cfg.WriteBatchSize)

	component := &TcpTunnelListenComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router, sendTimeout),

		listenAddr:        cfg.ListenAddr,
		timeout:           timeout,
		replaceOldMapping: cfg.ReplaceOldMapping,
		detour:            cfg.Detour,
		authManager:       authManager,
		broadcastMode:     broadcastMode,
		noDelay:           noDelay,
		sendTimeout:       sendTimeout,
		recvBufferSize:    recvBufferSize,
		sendBufferSize:    sendBufferSize,
		writeBatchSize:    writeBatchSize,
	}

	emptyConnections := make(map[ForwardID]map[PoolID]*TcpTunnelConnPool)
	component.connections.Store(emptyConnections)

	return component
}

func (l *TcpTunnelListenComponent) GetDetour() []string {
	return l.detour
}

func (l *TcpTunnelListenComponent) Start() error {
	ln, err := net.Listen("tcp", l.listenAddr)
	if err != nil {
		logger.Errorf("%s: Failed to start listening on %s: %v", l.tag, l.listenAddr, err)
		return err
	}

	l.listener = ln

	if l.authManager == nil {
		logger.Warnf("%s: AuthManager is nil, cannot start component", l.tag)
		return fmt.Errorf("%s: AuthManager is nil", l.tag)
	}

	logger.Infof("%s: Listening on %s", l.tag, l.listenAddr)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-l.stopCh:
					return
				default:
				}
				continue
			}

			// 设置TCP缓冲区大小
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				if l.recvBufferSize > 0 {
					_ = tcpConn.SetReadBuffer(l.recvBufferSize)
				}
				if l.sendBufferSize > 0 {
					_ = tcpConn.SetWriteBuffer(l.sendBufferSize)
				}
			}

			logger.Infof("%s: Accepted connection from %s", l.tag, conn.RemoteAddr())
			NewTcpTunnelConn(conn, ForwardID{}, PoolID{}, l, l.router.config.QueueSize, l.writeBatchSize, TcpTunnelListenMode)

			if l.noDelay {
				if tcpConn, ok := conn.(*net.TCPConn); ok {
					err := tcpConn.SetNoDelay(true)
					if err != nil {
						logger.Warnf("%s: Failed to set TCP_NODELAY for connection %s: %v", l.tag, conn.RemoteAddr(), err)
					}
				}
			}

		}
	}()
	return nil
}

func (l *TcpTunnelListenComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	err := l.authManager.WrapData(packet)
	if err != nil {
		return err
	}

	if !l.broadcastMode && packet.ConnID() != (ConnID{}) {
		if pool := l.getPoolByID(packet.ConnID()); pool != nil {
			conn := pool.GetNextConn()
			if conn == nil {
				logger.Debugf("%s: No available TCP tunnel connection in selected pool for connection ID %x", l.tag, packet.ConnID())
				return nil
			}
			return conn.Write(packet)
		}

		logger.Debugf("%s: No TCP tunnel connection found for connection ID %x", l.tag, packet.ConnID())
		return nil
	}

	if l.broadcastMode {
		connections := l.connections.Load().(map[ForwardID]map[PoolID]*TcpTunnelConnPool)
		for _, pools := range connections {
			for _, pool := range pools {
				c := pool.GetNextConn()

				if c == nil {
					logger.Debugf("%s: No available connections in pool %s", l.tag, pool.remoteAddr)
					continue
				}

				if err := c.Write(packet); err != nil {
					remote := "<closed>"
					if c.conn != nil {
						remote = c.conn.RemoteAddr().String()
					}
					logger.Infof("%s: Failed to send packet to %s: %v", l.tag, remote, err)
					continue
				}
			}
		}
	}

	return nil
}

func (l *TcpTunnelListenComponent) Stop() error {
	close(l.stopCh)

	if l.listener != nil {
		err := l.listener.Close()
		if err != nil {
			logger.Errorf("%s: Error closing listener: %v", l.tag, err)
		}
		l.listener = nil
	}

	l.authManager.Stop()

	logger.Infof("%s: Stopped listening on %s", l.tag, l.listenAddr)
	return nil
}

func (l *TcpTunnelListenComponent) GetAuthManager() *AuthManager {
	return l.authManager
}

// IsAvailable checks if the component has any established connections
func (l *TcpTunnelListenComponent) IsAvailable() bool {
	// First check if the listener is active
	if l.listener == nil {
		return false
	}

	// Check if there are any established connections
	connections := l.connections.Load().(map[ForwardID]map[PoolID]*TcpTunnelConnPool)

	// Check if there are any forward IDs
	if len(connections) == 0 {
		return false
	}

	// Check if any of the forward IDs have pool IDs
	for _, pools := range connections {
		if len(pools) > 0 {
			// Check if any of the pools have connections
			for _, pool := range pools {
				if pool != nil && pool.ConnectionCount() > 0 {
					return true
				}
			}
		}
	}

	return false
}

func (l *TcpTunnelListenComponent) Disconnect(c *TcpTunnelConn) {
	if c == nil {
		return
	}
	if c.conn != nil {
		logger.Infof("%s: Disconnecting %s", l.tag, c.conn.RemoteAddr())
	} else {
		logger.Infof("%s: Disconnecting connection (already closed)", l.tag)
	}

	currentConnections := l.connections.Load().(map[ForwardID]map[PoolID]*TcpTunnelConnPool)
	needsUpdate := false
	var emptiedPool *TcpTunnelConnPool

	if _, existsForward := currentConnections[c.forwardID]; existsForward {
		if _, existsPool := currentConnections[c.forwardID][c.poolID]; existsPool {
			needsUpdate = true
		}
	}

	if needsUpdate {
		newConnections := cloneConnections(currentConnections)

		if pool, exists := newConnections[c.forwardID][c.poolID]; exists {
			pool.RemoveConnection(c)

			if pool.ConnectionCount() == 0 {
				emptiedPool = pool
				delete(newConnections[c.forwardID], c.poolID)
				if len(newConnections[c.forwardID]) == 0 {
					delete(newConnections, c.forwardID)
				}
			}
		}

		l.connections.Store(newConnections)
	}

	if emptiedPool != nil {
		l.forgetPool(emptiedPool)
	}
	c.Close()
}

func (l *TcpTunnelListenComponent) HandleAuthenticatedConnection(c *TcpTunnelConn) error {
	packet := l.GetRouter().GetPacket(l.GetTag())
	defer packet.Release(1)

	responseLen, err := l.GetAuthManager().CreateAuthChallenge(packet.buffer[packet.offset:], MsgTypeAuthResponse, c.forwardID, c.poolID)
	if err != nil {
		logger.Warnf("%s: Failed to create auth challenge response: %v", l.GetTag(), err)
		return err
	}
	packet.length = responseLen

	if err := c.Write(&packet); err != nil {
		remote := "<closed>"
		if c.conn != nil {
			remote = c.conn.RemoteAddr().String()
		}
		logger.Infof("%s: %s Failed to send auth response: %v", l.GetTag(), remote, err)
		l.Disconnect(c)
		return fmt.Errorf("%s: Failed to send auth response: %v", l.GetTag(), err)
	}

	currentConnections := l.connections.Load().(map[ForwardID]map[PoolID]*TcpTunnelConnPool)

	newConnections := cloneConnections(currentConnections)

	if _, exists := newConnections[c.forwardID]; !exists {
		newConnections[c.forwardID] = make(map[PoolID]*TcpTunnelConnPool)
	}

	if _, exists := newConnections[c.forwardID][c.poolID]; !exists {
		remoteAddr := c.conn.RemoteAddr().String()
		newConnections[c.forwardID][c.poolID] = NewTcpTunnelConnPool(remoteAddr, c.poolID, 0)
	}

	newConnections[c.forwardID][c.poolID].AddConnection(c)

	l.connections.Store(newConnections)

	c.authState.SetAuthenticated(1)

	return nil
}

func (l *TcpTunnelListenComponent) RememberConnID(connID ConnID, c *TcpTunnelConn) {
	if connID == (ConnID{}) || c == nil {
		return
	}
	connections := l.connections.Load().(map[ForwardID]map[PoolID]*TcpTunnelConnPool)
	forwardPools, ok := connections[c.forwardID]
	if !ok {
		return
	}
	pool, ok := forwardPools[c.poolID]
	if !ok || pool == nil {
		return
	}
	l.connIndex.Store(connID, pool)
}

func (l *TcpTunnelListenComponent) getPoolByID(connID ConnID) *TcpTunnelConnPool {
	if connID == (ConnID{}) {
		return nil
	}
	value, ok := l.connIndex.Load(connID)
	if !ok {
		return nil
	}
	pool, ok := value.(*TcpTunnelConnPool)
	if !ok || pool == nil {
		if ok {
			l.connIndex.Delete(connID)
		}
		return nil
	}
	if pool.ConnectionCount() == 0 {
		l.connIndex.Delete(connID)
		return nil
	}
	return pool
}

func (l *TcpTunnelListenComponent) forgetPool(target *TcpTunnelConnPool) {
	if target == nil {
		return
	}
	l.connIndex.Range(func(key, value any) bool {
		pool, ok := value.(*TcpTunnelConnPool)
		if ok && pool == target {
			l.connIndex.Delete(key)
		}
		return true
	})
}

func cloneConnections(src map[ForwardID]map[PoolID]*TcpTunnelConnPool) map[ForwardID]map[PoolID]*TcpTunnelConnPool {
	dst := make(map[ForwardID]map[PoolID]*TcpTunnelConnPool, len(src))
	for fid, pools := range src {
		dst[fid] = make(map[PoolID]*TcpTunnelConnPool, len(pools))
		for pid, pool := range pools {
			dst[fid][pid] = pool
		}
	}
	return dst
}
