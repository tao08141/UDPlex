package main

import (
	"fmt"
	"net"
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

			logger.Infof("%s: Accepted connection from %s", l.tag, conn.RemoteAddr())
			NewTcpTunnelConn(conn, ForwardID{}, PoolID{}, l, l.router.config.QueueSize, TcpTunnelListenMode)

			if l.noDelay {
				if tcpConn, ok := conn.(*net.TCPConn); ok {
					tcpConn.SetNoDelay(true)
				}
			}

		}
	}()
	return nil
}

func (l *TcpTunnelListenComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	l.authManager.WrapData(packet)

	if l.broadcastMode {
		connections := l.connections.Load().(map[ForwardID]map[PoolID]*TcpTunnelConnPool)
		for _, pools := range connections {
			for _, pool := range pools {
				c := pool.GetNextConn()

				if c == nil {
					logger.Debugf("%s: No available connections in pool %s", l.tag, pool.remoteAddr)
					continue
				}

				packet.AddRef(1)
				if err := c.Write(packet); err != nil {
					packet.Release(1)
					logger.Infof("%s: Failed to send packet to %s: %v", l.tag, c.conn.RemoteAddr(), err)
					continue
				}
			}
		}
	}

	return nil
}

func (l *TcpTunnelListenComponent) SendPacket(packet *Packet, metadata any) error {

	return nil
}

func (l *TcpTunnelListenComponent) Stop() error {
	close(l.stopCh)

	logger.Infof("%s: Stopped listening on %s", l.tag, l.listenAddr)
	return nil
}

func (l *TcpTunnelListenComponent) GetAuthManager() *AuthManager {
	return l.authManager
}

func (l *TcpTunnelListenComponent) Disconnect(c *TcpTunnelConn) {
	logger.Infof("%s: Disconnecting %s", l.tag, c.conn.RemoteAddr())

	currentConnections := l.connections.Load().(map[ForwardID]map[PoolID]*TcpTunnelConnPool)
	needsUpdate := false

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
				delete(newConnections[c.forwardID], c.poolID)
				if len(newConnections[c.forwardID]) == 0 {
					delete(newConnections, c.forwardID)
				}
			}
		}

		l.connections.Store(newConnections)
	}

	if c != nil {
		c.Close()
		c = nil
	}
}

func (l *TcpTunnelListenComponent) HandleAuthenticatedConnection(c *TcpTunnelConn) error {
	packet := l.GetRouter().GetPacket(l.GetTag())

	responseLen, err := l.GetAuthManager().CreateAuthChallenge(packet.buffer[packet.offset:], MsgTypeAuthResponse, c.forwardID, c.poolID)
	if err != nil {
		logger.Warnf("%s: Failed to create auth challenge response: %v", l.GetTag(), err)
		packet.Release(1)
		return err
	}
	packet.length = responseLen

	if err := c.Write(&packet); err != nil {
		logger.Infof("%s: %s Failed to send auth response: %v", l.GetTag(), c.conn.RemoteAddr(), err)
		packet.Release(1)
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

	atomic.StoreInt32(&c.authState.authenticated, 1)

	return nil
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
