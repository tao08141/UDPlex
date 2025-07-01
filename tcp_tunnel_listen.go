package main

import (
	"fmt"
	"net"
	"sync"
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

	connections      map[ForwardID]map[PoolID]*TcpTunnelConnPool
	connectionsMutex sync.RWMutex

	authManager *AuthManager
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

	return &TcpTunnelListenComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router),

		listenAddr:        cfg.ListenAddr,
		timeout:           timeout,
		replaceOldMapping: cfg.ReplaceOldMapping,
		detour:            cfg.Detour,
		authManager:       authManager,
		broadcastMode:     broadcastMode,
		connections:       make(map[ForwardID]map[PoolID]*TcpTunnelConnPool),
		noDelay:           noDelay,
	}
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
			c := &TcpTunnelConn{
				forwardID:  ForwardID{},
				poolID:     PoolID{},
				conn:       conn,
				authState:  &AuthState{},
				lastActive: time.Now(),
			}

			if l.noDelay {
				if tcpConn, ok := conn.(*net.TCPConn); ok {
					tcpConn.SetNoDelay(true)
					logger.Infof("%s: TCP_NODELAY enabled for %s", l.tag, conn.RemoteAddr())
				}
			}

			go TcpTunnelLoopRead(c, l, TcpTunnelListenMode)

		}
	}()
	return nil
}

func (l *TcpTunnelListenComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	l.authManager.WrapData(packet)

	// If broadcasting, send to all authenticated connections
	if l.broadcastMode {
		l.connectionsMutex.RLock()
		defer l.connectionsMutex.RUnlock()
		for _, pools := range l.connections {
			for _, pool := range pools {
				c := pool.GetNextConn()

				if c == nil {
					logger.Infof("%s: No available connections in pool %s", l.tag, pool.remoteAddr)
					continue
				}

				if err := l.router.SendPacket(l, packet, c.conn); err != nil {
					logger.Infof("%s: Failed to send packet to %s: %v", l.tag, c.conn.RemoteAddr(), err)
					continue
				}
			}
		}
	}

	return nil
}

func (l *TcpTunnelListenComponent) SendPacket(packet *Packet, metadata any) error {
	conn, ok := metadata.(net.Conn)
	if !ok {
		return fmt.Errorf("%s: Invalid connection type", l.tag)
	}

	if conn == nil {
		return fmt.Errorf("%s: Connection is nil", l.tag)
	}

	_, err := conn.Write(packet.GetData())
	if err != nil {
		logger.Infof("%s: Failed to send packet: %v", l.tag, err)
		return err
	}

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
	l.connectionsMutex.Lock()
	defer l.connectionsMutex.Unlock()
	if pool, exists := l.connections[c.forwardID][c.poolID]; exists {
		pool.RemoveConnection(c)
		if len(pool.conns) == 0 {
			delete(l.connections[c.forwardID], c.poolID)
			if len(l.connections[c.forwardID]) == 0 {
				delete(l.connections, c.forwardID)
			}
		}
	}
	if c != nil {
		c.conn.Close()
		c = nil
	}
}

func (l *TcpTunnelListenComponent) GetDetour() []string {
	return l.detour
}

func (l *TcpTunnelListenComponent) HandleAuthenticatedConnection(c *TcpTunnelConn) error {

	// Send auth response
	responseBuffer := l.GetRouter().GetBuffer()
	responseLen, err := l.GetAuthManager().CreateAuthChallenge(responseBuffer, MsgTypeAuthResponse, c.forwardID, c.poolID)
	if err != nil {
		logger.Warnf("%s: Failed to create auth challenge response: %v", l.GetTag(), err)
		l.GetRouter().PutBuffer(responseBuffer)
		return err
	}

	if _, err := c.conn.Write(responseBuffer[:responseLen]); err != nil {
		logger.Infof("%s: %s Failed to send auth response: %v", l.GetTag(), c.conn.RemoteAddr(), err)
		l.GetRouter().PutBuffer(responseBuffer)
		return fmt.Errorf("%s: Failed to send auth response: %v", l.GetTag(), err)
	}

	l.connectionsMutex.Lock()

	if _, exists := l.connections[c.forwardID]; !exists {
		l.connections[c.forwardID] = make(map[PoolID]*TcpTunnelConnPool)
	}

	if _, exists := l.connections[c.forwardID][c.poolID]; !exists {
		l.connections[c.forwardID][c.poolID] = &TcpTunnelConnPool{
			index: 0,
			conns: []*TcpTunnelConn{},
		}
	}

	l.connections[c.forwardID][c.poolID].AddConnection(c)
	l.connectionsMutex.Unlock()

	l.GetRouter().PutBuffer(responseBuffer)
	c.authState.authenticated = 1

	return nil
}
