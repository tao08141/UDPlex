// // filepath: c:\Users\ghost\Desktop\dev\UDPlex\tcp_tunnel_forward_pool.go
package main

import (
	"crypto/rand"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TcpTunnelForwardComponent struct {
	BaseComponent

	checkTime     time.Duration
	timeout       time.Duration
	detour        []string
	broadcastMode bool
	forwardID     ForwardID
	authManager   *AuthManager
	pools         map[string]*TcpTunnelConnPool

	connectionsMutex sync.RWMutex
}

func NewTcpTunnelForwardComponent(cfg ComponentConfig, router *Router) *TcpTunnelForwardComponent {
	checkTime := time.Duration(cfg.ConnectionCheckTime) * time.Second
	if checkTime == 0 {
		checkTime = 10 * time.Second
	}

	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
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

	forwardID := ForwardID{}
	rand.Read(forwardID[:])

	pools := make(map[string]*TcpTunnelConnPool)

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

		pools[addr] = &TcpTunnelConnPool{
			conns:      []*TcpTunnelConn{},
			index:      0,
			remoteAddr: addr,
			poolID:     PoolID{},
			connCount:  count,
		}

	}

	return &TcpTunnelForwardComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router),
		checkTime:     checkTime,
		broadcastMode: broadcastMode,
		timeout:       timeout,
		authManager:   authManager,
		forwardID:     forwardID,
		pools:         pools,
		detour:        cfg.Detour,
	}
}

func (f *TcpTunnelForwardComponent) Start() error {
	if len(f.pools) == 0 {
		logger.Warnf("%s: No forwarders configured, skipping start", f.tag)
	}

	logger.Infof("%s: Starting TCP tunnel forward component with ID %x", f.tag, f.forwardID)

	// 打印转发地址与数量
	for addr, pool := range f.pools {
		if pool == nil {
			logger.Warnf("%s: Pool for %s is nil, skipping", f.tag, addr)
			continue
		}
		logger.Infof("%s: Forwarding to %s with %d connections", f.tag, addr, pool.connCount)
		rand.Read(pool.poolID[:])

		for i := 0; i < pool.connCount; i++ {
			ttc, err := f.setupConnection(addr, pool.poolID)
			if err != nil {
				logger.Errorf("%s: Failed to setup connection to %s: %v", f.tag, addr, err)
				continue
			}
			pool.AddConnection(ttc)

			logger.Infof("%s: Added new connection to %s", f.tag, addr)
		}
	}

	go f.runChecker()

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

	ttc := &TcpTunnelConn{
		conn:        conn,
		authState:   &AuthState{},
		lastActive:  time.Now(),
		isConnected: 1,
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

func (f *TcpTunnelForwardComponent) runChecker() {
	ticker := time.NewTicker(f.checkTime)
	defer ticker.Stop()
	for {
		select {
		case <-f.GetStopChannel():
			return
		case <-ticker.C:

			for addr, pool := range f.pools {
				if pool == nil {
					logger.Warnf("%s: Pool for %s is nil, skipping", f.tag, addr)
					continue
				}

				if len(pool.conns) < pool.connCount {
					for i := len(pool.conns); i < pool.connCount; i++ {
						ttc, err := f.setupConnection(addr, pool.poolID)
						if err != nil {
							logger.Errorf("%s: Failed to setup connection to %s: %v", f.tag, addr, err)
							continue
						}
						pool.AddConnection(ttc)

						logger.Infof("%s: Added new connection to %s", f.tag, addr)
					}
				}
			}
		}
	}
}

func (l *TcpTunnelForwardComponent) HandleAuthenticatedConnection(c *TcpTunnelConn) error {
	c.authState.authenticated = 1
	return nil
}

func (l *TcpTunnelForwardComponent) Disconnect(c *TcpTunnelConn) {
	if c == nil {
		return
	}

	logger.Infof("%s: Disconnecting %s", l.tag, c.conn.RemoteAddr())

	c.isConnected = 0
	c.conn.Close()
	c = nil

	for _, pool := range l.pools {
		pool.RemoveConnection(c)
	}
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
		f.connectionsMutex.RLock()
		defer f.connectionsMutex.RUnlock()

		for _, pool := range f.pools {
			c := pool.GetNextConn()
			if c == nil {
				logger.Warnf("%s: No available connections in pool %s", f.tag, pool.remoteAddr)
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
