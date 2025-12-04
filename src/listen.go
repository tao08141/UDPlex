package main

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"sync/atomic"
	"time"
)

// ListenConn represents a logical UDP "connection" from a remote address
// It encapsulates per-peer state like authentication and activity timestamps.
type ListenConn struct {
	addr              net.Addr
	lastActive        time.Time
	authState         *AuthState // Authentication state for this connection
	connID            ConnID     // Unique connection identifier
	lastHeartbeatSent time.Time  // Last heartbeat sent time
	sendQueue         chan *Packet
	closeCh           chan struct{}
	closed            atomic.Bool
}

// Address returns the remote address associated with this logical connection.
func (c *ListenConn) Address() net.Addr { return c.addr }

// ConnID returns the unique identifier for this logical connection.
func (c *ListenConn) ConnID() ConnID { return c.connID }

// IsAuthenticated reports whether this connection has passed authentication.
func (c *ListenConn) IsAuthenticated() bool {
	return c.authState != nil && c.authState.IsAuthenticated()
}

// Touch updates the last active timestamp to now.
func (c *ListenConn) Touch() { c.lastActive = time.Now() }

func (c *ListenConn) signalClose() {
	if c == nil || c.closeCh == nil {
		return
	}
	if c.closed.CompareAndSwap(false, true) {
		close(c.closeCh)
	}
}

// ListenComponent implements a UDP listener with authentication
type ListenComponent struct {
	BaseComponent

	listenAddr        string
	timeout           time.Duration
	replaceOldMapping bool
	detour            []string
	broadcastMode     bool
	conn              net.PacketConn
	mappings          map[string]*ListenConn
	mappingsAtomic    atomic.Value
	authManager       *AuthManager
	sendTimeout       time.Duration
	recvBufferSize    int // UDP socket receive buffer size
	sendBufferSize    int // UDP socket send buffer size
	connQueueSize     int
}

// NewListenComponent creates a new listen component
func NewListenComponent(cfg ComponentConfig, router *Router) *ListenComponent {
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second // Default timeout
	}

	// Initialize auth manager
	authManager, err := NewAuthManager(cfg.Auth, router)
	if err != nil {
		logger.Errorf("Failed to create auth manager: %v", err)
		return nil
	}

	broadcastMode := true
	if cfg.BroadcastMode != nil && !*cfg.BroadcastMode {
		broadcastMode = false
	}

	sendTimeout := time.Duration(cfg.SendTimeout) * time.Millisecond
	if sendTimeout == 0 {
		sendTimeout = 500 * time.Millisecond
	}
	queueSize := router.config.QueueSize
	if queueSize <= 0 {
		queueSize = 10240
	}
	component := &ListenComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router, sendTimeout),

		listenAddr:        cfg.ListenAddr,
		timeout:           timeout,
		replaceOldMapping: cfg.ReplaceOldMapping,
		detour:            cfg.Detour,
		mappings:          make(map[string]*ListenConn),
		authManager:       authManager,
		broadcastMode:     broadcastMode,
		sendTimeout:       sendTimeout,
		recvBufferSize:    cfg.RecvBufferSize,
		sendBufferSize:    cfg.SendBufferSize,
		connQueueSize:     queueSize,
	}

	// Initialize an atomic value with an empty map
	initialMap := make(map[string]*ListenConn)
	component.mappingsAtomic.Store(initialMap)

	return component
}

// Start initializes and starts the listener
func (l *ListenComponent) Start() error {
	// Create UDP address to listen on
	udpAddr, err := net.ResolveUDPAddr("udp", l.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	// Create UDP connection with specific options
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("failed to set up UDP listener: %w", err)
	}

	// Apply socket optimizations if configured
	if l.recvBufferSize > 0 {
		if err := conn.SetReadBuffer(l.recvBufferSize); err != nil {
			logger.Warnf("%s: Failed to set read buffer size to %d: %v", l.tag, l.recvBufferSize, err)
		} else {
			logger.Infof("%s: Set UDP read buffer size to %d bytes", l.tag, l.recvBufferSize)
		}
	}

	if l.sendBufferSize > 0 {
		if err := conn.SetWriteBuffer(l.sendBufferSize); err != nil {
			logger.Warnf("%s: Failed to set write buffer size to %d: %v", l.tag, l.sendBufferSize, err)
		} else {
			logger.Infof("%s: Set UDP write buffer size to %d bytes", l.tag, l.sendBufferSize)
		}
	}

	l.conn = conn
	logger.Infof("%s is listening on %s", l.tag, conn.LocalAddr())

	// Start packet handling routine
	go l.handlePackets()

	return nil
}

// Stop closes the listener
func (l *ListenComponent) Stop() error {
	close(l.GetStopChannel())
	return l.conn.Close()
}

// IsAvailable checks if the component has any established connections
func (l *ListenComponent) IsAvailable() bool {
	// First check if the listener is active
	if l.conn == nil {
		return false
	}

	// Check if there are any established connections
	mappings := l.mappingsAtomic.Load().(map[string]*ListenConn)
	return len(mappings) > 0
}

// performCleanup handles the cleaning of inactive mappings
func (l *ListenComponent) performCleanup() {
	now := time.Now()
	isSync := false

	// Remove inactive mappings
	for addrString, mapping := range l.mappings {
		if now.Sub(mapping.lastActive) > l.timeout {
			if l.removeMapping(addrString) {
				isSync = true
				logger.Warnf("%s: Removed inactive mapping: %s", l.tag, addrString)
			}
		}
	}

	if isSync {
		l.syncMapping()
	}
}

func (l *ListenComponent) syncMapping() {
	mappingsTemp := make(map[string]*ListenConn)
	maps.Copy(mappingsTemp, l.mappings)
	l.mappingsAtomic.Store(mappingsTemp)
}

func (l *ListenComponent) initSendQueue(mapping *ListenConn) {
	if mapping == nil || mapping.sendQueue != nil {
		return
	}
	mapping.sendQueue = make(chan *Packet, l.connQueueSize)
	mapping.closeCh = make(chan struct{})
	go l.listenConnSendLoop(mapping)
}

func (l *ListenComponent) listenConnSendLoop(mapping *ListenConn) {
	if mapping == nil || mapping.sendQueue == nil {
		return
	}
	for {
		select {
		case <-l.GetStopChannel():
			l.releasePendingPackets(mapping)
			return
		case <-mapping.closeCh:
			l.releasePendingPackets(mapping)
			return
		case pkt := <-mapping.sendQueue:
			if pkt == nil {
				continue
			}
			if l.sendTimeout > 0 {
				if err := l.conn.SetWriteDeadline(time.Now().Add(l.sendTimeout)); err != nil {
					logger.Infof("%s: Failed to set write deadline: %v", l.tag, err)
				}
			}
			if _, err := l.conn.WriteTo(pkt.GetData(), mapping.addr); err != nil {
				logger.Infof("%s: Failed to send packet: %v", l.tag, err)
			}
			pkt.Release(1)
		}
	}
}

func (l *ListenComponent) releasePendingPackets(mapping *ListenConn) {
	if mapping == nil || mapping.sendQueue == nil {
		return
	}
	for {
		select {
		case pkt := <-mapping.sendQueue:
			if pkt != nil {
				pkt.Release(1)
			}
		default:
			return
		}
	}
}

func (l *ListenComponent) enqueuePacket(mapping *ListenConn, packet *Packet) {
	if packet == nil {
		return
	}

	packet.AddRef(1)

	if mapping == nil || mapping.sendQueue == nil || mapping.closed.Load() {
		packet.Release(1)
		return
	}
	select {
	case mapping.sendQueue <- packet:
		return
	default:
		logger.Infof("%s: Send queue full for %s, dropping packet", l.tag, mapping.addr.String())
		packet.Release(1)
	}
}

func (l *ListenComponent) removeMapping(addrKey string) bool {
	mapping, exists := l.mappings[addrKey]
	if !exists {
		return false
	}
	mapping.signalClose()
	delete(l.mappings, addrKey)
	l.RemoveConnData(mapping.connID)
	return true
}

func (l *ListenComponent) SendPacket(packet *Packet, metadata any) error {
	addr, ok := metadata.(net.Addr)
	if !ok {
		return fmt.Errorf("%s: Invalid address type", l.tag)
	}

	if addr == nil {
		return fmt.Errorf("%s: Address is nil", l.tag)
	}

	if l.sendTimeout > 0 {
		if err := l.conn.SetWriteDeadline(time.Now().Add(l.sendTimeout)); err != nil {
			logger.Infof("%s: Failed to set write deadline: %v", l.tag, err)
		}
	}

	_, err := l.conn.WriteTo(packet.GetData(), addr)
	if err != nil {
		logger.Infof("%s: Failed to send packet: %v", l.tag, err)
		return err
	}

	return nil
}

// handleAuthMessage processes authentication messages
func (l *ListenComponent) handleAuthMessage(header *ProtocolHeader, buffer []byte, addr net.Addr) {

	addrKey := addr.String()

	switch header.MsgType {
	case MsgTypeAuthChallenge:
		// Get or create an auth state
		mapping, exists := l.mappings[addrKey]
		if !exists {
			mapping = &ListenConn{
				addr:       addr,
				lastActive: time.Now(),
				authState:  &AuthState{},
				connID:     l.generateConnID(),
			}
			l.initSendQueue(mapping)
			l.mappings[addrKey] = mapping
			l.syncMapping()
		}

		// Process challenge and send response
		data := buffer[HeaderSize : HeaderSize+header.Length]
		forwardID, poolID, err := l.authManager.ProcessAuthChallenge(data)
		if err != nil {
			logger.Infof("%s: %s Authentication challenge failed: %v", l.tag, addr.String(), err)
			return
		}

		if l.replaceOldMapping {
			addrIP := addr.(*net.UDPAddr).IP.String()
			isSync := false

			for key, mapping := range l.mappings {
				if mapping.addr.(*net.UDPAddr).IP.String() == addrIP && key != addrKey {
					logger.Warnf("%s: Replacing old mapping: %s", l.tag, mapping.addr.String())
					if l.removeMapping(key) {
						isSync = true
					}
				}
			}

			if isSync {
				l.syncMapping()
			}
		}

		// Create response
		responseBuffer := l.router.GetBuffer()
		l.router.PutBuffer(responseBuffer)
		responseLen, err := l.authManager.CreateAuthChallenge(responseBuffer, MsgTypeAuthResponse, forwardID, poolID)
		if err != nil {
			logger.Warnf("%s: Failed to create auth challenge response: %v", l.tag, err)
		}

		if l.sendTimeout > 0 {
			if err := l.conn.SetWriteDeadline(time.Now().Add(l.sendTimeout)); err != nil {
				logger.Infof("%s: Failed to set write deadline: %v", l.tag, err)
			}
		}

		// Send response
		_, err = l.conn.WriteTo(responseBuffer[:responseLen], addr)
		if err != nil {
			logger.Warnf("%s: Failed to send auth response: %v", l.tag, err)
		}

		mapping.authState.SetAuthenticated(1)

		mapping.lastActive = time.Now()
		logger.Infof("%s: Authentication successful for %s", l.tag, addr.String())

	case MsgTypeHeartbeat:

		// Update mapping if exists
		if mapping, exists := l.mappings[addrKey]; exists {
			mapping.lastActive = time.Now()
			if mapping.authState != nil {
				// Echo's heartbeat back
				responseBuffer := l.router.GetBuffer()
				responseLen := CreateHeartbeat(responseBuffer)

				if l.sendTimeout > 0 {
					if err := l.conn.SetWriteDeadline(time.Now().Add(l.sendTimeout)); err != nil {
						logger.Infof("%s: Failed to set write deadline: %v", l.tag, err)
					}
				}

				_, err := l.conn.WriteTo(responseBuffer[:responseLen], addr)
				if err != nil {
					logger.Infof("%s: Failed to send heartbeat response: %v", l.tag, err)
				}
				l.router.PutBuffer(responseBuffer)
				mapping.lastHeartbeatSent = time.Now()
			}
		}

	case MsgTypeHeartbeatAck:
		// Update mapping if exists
		if mapping, exists := l.mappings[addrKey]; exists {
			mapping.lastActive = time.Now()
			if mapping.authState != nil {
				// If this is a response to our heartbeat, measure delay
				if !mapping.lastHeartbeatSent.IsZero() {
					delay := time.Since(mapping.lastHeartbeatSent)
					if l.authManager != nil {
						l.authManager.RecordDelayMeasurement(delay)
					}
				}
				mapping.lastHeartbeatSent = time.Time{} // Reset last heartbeat sent
			}
		}

	case MsgTypeDisconnect:
		if l.removeMapping(addrKey) {
			l.syncMapping()
			logger.Infof("%s: Client %s disconnected", l.tag, addr.String())
		}
	}

}

// handlePackets processes incoming UDP packets
func (l *ListenComponent) handlePackets() {
	cleanupInterval := l.timeout / 2
	lastCleanupTime := time.Now()
	shortDeadline := min(time.Second*1, cleanupInterval)

	for {
		select {
		case <-l.GetStopChannel():
			return
		default:
			func() {
				now := time.Now()
				if now.Sub(lastCleanupTime) >= cleanupInterval {
					l.performCleanup()
					lastCleanupTime = now
				}

				// Set read deadline
				if err := l.conn.SetReadDeadline(time.Now().Add(shortDeadline)); err != nil {
					logger.Warnf("%s: Error setting read deadline: %v", l.tag, err)
				}

				packet := l.router.GetPacket(l.tag)
				defer packet.Release(1)

				length, addr, err := l.conn.ReadFrom(packet.BufAtOffset())

				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					return
				} else if err != nil {
					logger.Warnf("%s: Read error: %v", l.tag, err)
					return
				}

				packet.SetLength(length)

				// Handle authentication if enabled
				if l.authManager != nil {
					if length < HeaderSize {
						logger.Infof("%s: %s Packet too short for header: %d bytes", l.tag, addr.String(), length)
						return
					}

					header, err := l.authManager.UnwrapData(&packet)
					if err != nil {
						if err.Error() != "duplicate packet detected" {
							logger.Infof("%s: %s Failed to unwrap data: %v", l.tag, addr.String(), err)
						}
						return
					}

					// Handle auth messages
					if header.MsgType != MsgTypeData {
						l.handleAuthMessage(header, packet.GetData(), addr)
						return
					}

					// For data messages, check authentication
					addrKey := addr.String()
					mapping, exists := l.mappings[addrKey]
					if !exists || mapping.authState == nil || !mapping.authState.IsAuthenticated() {
						// Not authenticated - silently drop
						return
					}

					mapping.lastActive = time.Now()
				}

				// Handle address mapping for non-auth mode
				if l.authManager == nil {
					addrKey := addr.String()
					// Check if this is a new mapping
					mapping, exists := l.mappings[addrKey]
					if !exists {
						if l.replaceOldMapping {
							addrIP := addr.(*net.UDPAddr).IP.String()
							removed := false
							for key, existing := range l.mappings {
								if existing.addr.(*net.UDPAddr).IP.String() == addrIP {
									logger.Warnf("%s: Replacing old mapping: %s", l.tag, existing.addr.String())
									if l.removeMapping(key) {
										removed = true
									}
								}
							}
							if removed {
								l.syncMapping()
							}
						}

						logger.Warnf("%s: New mapping: %s", l.tag, addr.String())
						connID := l.generateConnID()
						mapping = &ListenConn{addr: addr, lastActive: time.Now(), connID: connID}
						l.initSendQueue(mapping)
						l.mappings[addrKey] = mapping
						l.syncMapping()
						packet.SetConnID(connID)
					} else {
						mapping.lastActive = time.Now()
						packet.SetConnID(mapping.connID)
					}
				}

				packet.SetSrcAddr(addr)

				// Forward the packet to detour components
				if err := l.router.Route(&packet, l.detour); err != nil {
					logger.Infof("%s: Error routing: %v", l.tag, err)
				}
			}()

		}
	}
}

// HandlePacket processes packets from other components
func (l *ListenComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	if l.authManager != nil {
		err := l.authManager.WrapData(packet)
		if err != nil {
			return err
		}
	}

	mappingsSnapshot := l.mappingsAtomic.Load().(map[string]*ListenConn)

	if l.broadcastMode {
		for _, mapping := range mappingsSnapshot {
			if l.authManager != nil && (mapping.authState == nil || !mapping.authState.IsAuthenticated()) {
				continue
			}

			l.enqueuePacket(mapping, packet)
		}

	} else {
		if packet.ConnID() == (ConnID{}) {
			logger.Infof("%s: Packet has no connection ID, dropping", l.tag)
			return nil
		}
		for _, mapping := range mappingsSnapshot {
			if mapping.connID == packet.ConnID() {
				// Check authentication if required
				if l.authManager != nil && (mapping.authState == nil || !mapping.authState.IsAuthenticated()) {
					logger.Debugf("%s: Connection not authenticated, dropping packet", l.tag)
					return nil
				}

				l.enqueuePacket(mapping, packet)
				return nil
			}
		}

		logger.Debugf("%s: No mapping found for connection ID: %s", l.tag, packet.ConnID())
	}

	return nil
}
