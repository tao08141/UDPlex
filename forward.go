package main

import (
	"crypto/rand"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// ForwardConn represents a connection to a forwarder with authentication
type ForwardConn struct {
	conn                 *net.UDPConn
	isConnected          int32 // Atomic flag: 0=disconnected, 1=connected
	remoteAddr           string
	udpAddr              *net.UDPAddr
	lastReconnectAttempt time.Time

	// Authentication state
	authState          *AuthState
	authRetryCount     int
	heartbeatMissCount int
	lastHeartbeatSent  time.Time
	poolID             PoolID
}

// ForwardComponent implements a UDP forwarder with authentication
type ForwardComponent struct {
	BaseComponent

	forwarders          []string
	reconnectInterval   time.Duration
	connectionCheckTime time.Duration
	detour              []string
	sendKeepalive       bool

	forwardConns    map[string]*ForwardConn
	forwardConnList []*ForwardConn
	forwardID       ForwardID

	// Authentication
	authManager *AuthManager
}

// NewForwardComponent creates a new forward component
func NewForwardComponent(cfg ComponentConfig, router *Router) *ForwardComponent {
	reconnectInterval := time.Duration(cfg.ReconnectInterval) * time.Second
	if reconnectInterval == 0 {
		reconnectInterval = 5 * time.Second
	}

	connectionCheckTime := time.Duration(cfg.ConnectionCheckTime) * time.Second
	if connectionCheckTime == 0 {
		connectionCheckTime = 30 * time.Second
	}

	sendKeepalive := true
	if cfg.SendKeepalive != nil {
		sendKeepalive = *cfg.SendKeepalive
	}

	// Initialize auth manager
	authManager, err := NewAuthManager(cfg.Auth, router)
	if err != nil {
		logger.Errorf("Failed to create auth manager: %v", err)
		return nil
	}

	forwardID := ForwardID{}

	rand.Read(forwardID[:])

	return &ForwardComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router),

		forwarders:          cfg.Forwarders,
		reconnectInterval:   reconnectInterval,
		connectionCheckTime: connectionCheckTime,
		detour:              cfg.Detour,
		sendKeepalive:       sendKeepalive,
		forwardConns:        make(map[string]*ForwardConn),
		authManager:         authManager,
		forwardID:           forwardID,
	}
}

// GetTag returns the component's tag
func (f *ForwardComponent) GetTag() string {
	return f.tag
}

// Start initializes and starts the forwarder
func (f *ForwardComponent) Start() error {
	// Initialize connections to all forwarders
	for _, addr := range f.forwarders {
		conn, err := f.setupForwarder(addr)
		if err != nil {
			logger.Errorf("%s: Failed to initialize forwarder %s: %v", f.tag, addr, err)
			continue
		}
		f.forwardConns[addr] = conn
		f.forwardConnList = append(f.forwardConnList, conn)
	}

	logger.Infof("%s: Forwarding to %v", f.tag, f.forwarders)

	// Start connection checker routine
	go f.connectionChecker()

	return nil
}

// Stop closes all forwarder connections
func (f *ForwardComponent) Stop() error {
	close(f.GetStopChannel())

	for _, conn := range f.forwardConnList {
		if conn.conn != nil {
			conn.conn.Close()
		}
	}

	return nil
}

// connectionChecker periodically checks connections and handles authentication
func (f *ForwardComponent) connectionChecker() {
	ticker := time.NewTicker(f.connectionCheckTime)
	defer ticker.Stop()

	for {
		select {
		case <-f.GetStopChannel():
			return
		case <-ticker.C:
			for _, conn := range f.forwardConnList {
				if atomic.LoadInt32(&conn.isConnected) == 0 {
					go f.tryReconnect(conn)
				} else {
					f.handleConnectionMaintenance(conn)
				}
			}
		}
	}
}

// handleConnectionMaintenance handles heartbeat and authentication
func (f *ForwardComponent) handleConnectionMaintenance(conn *ForwardConn) {
	now := time.Now()

	if f.authManager != nil {

		if !conn.authState.IsAuthenticated() {
			// Need authentication
			go f.sendAuthChallenge(conn)
		} else {
			// Authenticated - send heartbeat if needed
			if now.Sub(conn.lastHeartbeatSent) >= f.authManager.heartbeatInterval {
				go f.sendHeartbeat(conn)
			}
		}
	} else if f.sendKeepalive {
		// No auth - just send keepalive
		conn.conn.Write([]byte{})
	}
}

// sendAuthChallenge sends authentication challenge to server
func (f *ForwardComponent) sendAuthChallenge(conn *ForwardConn) {
	if atomic.LoadInt32(&conn.isConnected) == 0 {
		return
	}

	conn.authRetryCount++

	if conn.authRetryCount > 5 {
		// Too many auth failures - mark as disconnected
		logger.Warnf("%s: Too many auth failures for %s", f.tag, conn.remoteAddr)
		atomic.StoreInt32(&conn.isConnected, 0)
		return
	}

	buffer := f.router.GetBuffer()
	defer f.router.PutBuffer(buffer)

	length, err := f.authManager.CreateAuthChallenge(buffer, MsgTypeAuthChallenge, f.forwardID, conn.poolID)
	if err != nil {
		logger.Warnf("%s: Failed to create auth challenge: %v", f.tag, err)
		return
	}

	_, err = conn.conn.Write(buffer[:length])
	if err != nil {
		logger.Warnf("%s: Failed to send auth challenge: %v", f.tag, err)
		atomic.StoreInt32(&conn.isConnected, 0)
	}
}

// sendHeartbeat sends heartbeat to server
func (f *ForwardComponent) sendHeartbeat(conn *ForwardConn) {
	if atomic.LoadInt32(&conn.isConnected) == 0 || !conn.authState.IsAuthenticated() {
		return
	}

	buffer := f.router.GetBuffer()
	defer f.router.PutBuffer(buffer)

	length := CreateHeartbeat(buffer)
	conn.lastHeartbeatSent = time.Now()

	_, err := conn.conn.Write(buffer[:length])
	if err != nil {
		logger.Warnf("%s: Failed to send heartbeat: %v", f.tag, err)
		atomic.StoreInt32(&conn.isConnected, 0)
		return
	}

	if conn.heartbeatMissCount >= 2 {
		f.sendAuthChallenge(conn)
	}

	// Increment miss count - will be reset when response received
	conn.heartbeatMissCount++
	if conn.heartbeatMissCount >= 5 {
		logger.Warnf("%s: Heartbeat timeout for %s", f.tag, conn.remoteAddr)
		atomic.StoreInt32(&conn.isConnected, 0)
		atomic.StoreInt32(&conn.authState.authenticated, 0)
		conn.heartbeatMissCount = 0
	}
}

// setupForwarder creates a new connection to a forwarder
func (f *ForwardComponent) setupForwarder(remoteAddr string) (*ForwardConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve address %v: %w", remoteAddr, err)
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, err
	}

	forwardConn := &ForwardConn{
		conn:                 conn,
		isConnected:          1,
		remoteAddr:           remoteAddr,
		udpAddr:              udpAddr,
		lastReconnectAttempt: time.Now(),
		authState:            &AuthState{},
	}

	// Start goroutine to handle receiving packets
	go f.readFromForwarder(forwardConn)

	// Start authentication if required
	if f.authManager != nil {
		go f.sendAuthChallenge(forwardConn)
	} else {
		atomic.StoreInt32(&forwardConn.authState.authenticated, 1)
	}

	return forwardConn, nil
}

// tryReconnect attempts to reconnect to a forwarder
func (f *ForwardComponent) tryReconnect(conn *ForwardConn) {
	if time.Since(conn.lastReconnectAttempt) < f.reconnectInterval {
		return
	}

	conn.lastReconnectAttempt = time.Now()

	if conn.conn != nil {
		conn.conn.Close()
		conn.conn = nil
	}

	logger.Infof("%s: Attempting to reconnect to %s", f.tag, conn.remoteAddr)

	newConn, err := net.DialUDP("udp", nil, conn.udpAddr)
	if err != nil {
		logger.Infof("%s: Reconnection to %s failed: %v", f.tag, conn.remoteAddr, err)
		return
	}

	conn.conn = newConn
	atomic.StoreInt32(&conn.isConnected, 1)
	conn.authRetryCount = 0
	conn.heartbeatMissCount = 0
	logger.Infof("%s: Successfully reconnected to %s", f.tag, conn.remoteAddr)

	go f.readFromForwarder(conn)

	// Start authentication if required
	if f.authManager != nil {
		go f.sendAuthChallenge(conn)
	} else {
		atomic.StoreInt32(&conn.authState.authenticated, 1)
	}
}

// readFromForwarder handles receiving packets from a forwarder
func (f *ForwardComponent) readFromForwarder(conn *ForwardConn) {
	for atomic.LoadInt32(&conn.isConnected) == 1 {
		select {
		case <-f.GetStopChannel():
			return
		default:
			conn.conn.SetReadDeadline(time.Now().Add(f.connectionCheckTime))

			func() {
				packet := f.router.GetPacket(f.tag)
				defer packet.Release(1)

				length, err := conn.conn.Read(packet.buffer[packet.offset:])

				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						return
					}

					logger.Warnf("%s: Error reading from %s: %v", f.tag, conn.remoteAddr, err)
					atomic.StoreInt32(&conn.isConnected, 0)
					return
				}

				packet.length = length

				// Handle authentication if enabled
				if f.authManager != nil {
					if length < HeaderSize {
						return
					}

					header, err := f.authManager.UnwrapData(&packet)
					if err != nil {
						return
					}

					// Handle auth messages
					if header.MsgType != MsgTypeData {
						f.handleAuthMessage(header, packet.GetData(), conn)
						return
					}

					// For data messages, check authentication
					if !conn.authState.IsAuthenticated() {
						return
					}

				}

				// Forward to detour components
				if err := f.router.Route(&packet, f.detour); err != nil {
					logger.Infof("%s: Error routing: %v", f.tag, err)
				}
			}()
		}
	}
}

// handleAuthMessage processes authentication messages
func (f *ForwardComponent) handleAuthMessage(header *ProtocolHeader, buffer []byte, conn *ForwardConn) {

	switch header.MsgType {
	case MsgTypeAuthResponse:

		data := buffer[HeaderSize : HeaderSize+header.Length]
		_, _, err := f.authManager.ProcessAuthChallenge(data, conn.authState)
		if err != nil {
			logger.Warnf("%s: Auth response verification failed: %v", f.tag, err)
			atomic.StoreInt32(&conn.authState.authenticated, 0)
			return
		}

		// Authentication successful
		atomic.StoreInt32(&conn.authState.authenticated, 1)
		conn.authRetryCount = 0
		conn.authState.lastAuth = time.Now()

		logger.Infof("%s: Authentication successful for %s", f.tag, conn.remoteAddr)

	case MsgTypeHeartbeat:
		// Reset heartbeat miss count
		conn.heartbeatMissCount = 0
		if conn.authState != nil {
			conn.authState.UpdateHeartbeat()
		}
	}
}

func (f *ForwardComponent) SendPacket(packet *Packet, metadata any) error {
	conn, ok := metadata.(*ForwardConn)
	if !ok || conn == nil {
		return fmt.Errorf("invalid connection type")
	}

	if atomic.LoadInt32(&conn.isConnected) == 0 || conn.conn == nil {
		return nil // Connection isn't available, just skip
	}

	_, err := conn.conn.Write(packet.GetData())
	if err != nil {
		logger.Infof("%s: Error writing to %s: %v", f.tag, conn.remoteAddr, err)
		atomic.StoreInt32(&conn.isConnected, 0)
		return err
	}

	return nil
}

// HandlePacket processes packets from other components
func (f *ForwardComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	if f.authManager != nil {
		err := f.authManager.WrapData(packet)
		if err != nil {
			logger.Infof("%s: Failed to wrap packet: %v", f.tag, err)
			return err
		}

	}

	for _, conn := range f.forwardConnList {
		if atomic.LoadInt32(&conn.isConnected) == 1 {
			// Check authentication if required
			if f.authManager != nil && !conn.authState.IsAuthenticated() {
				continue
			}

			if err := f.router.SendPacket(f, packet, conn); err != nil {
				logger.Infof("%s: Failed to queue packet for sending: %v", f.tag, err)
			}
		}
	}

	return nil
}
