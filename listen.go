package main

import (
	"fmt"
	"maps"
	"net"
	"sync/atomic"
	"time"
)

// AddrMapping stores each mapped address and its last active timestamp
type AddrMapping struct {
	addr       net.Addr
	lastActive time.Time
	authState  *AuthState // Authentication state for this connection
}

// ListenComponent implements a UDP listener with authentication
type ListenComponent struct {
	tag string

	listenAddr        string
	timeout           time.Duration
	replaceOldMapping bool
	detour            []string

	conn         net.PacketConn
	router       *Router
	mappings     map[string]*AddrMapping
	mappingsRead *map[string]*AddrMapping
	stopCh       chan struct{}
	stopped      bool

	// Authentication
	authManager *AuthManager
}

// NewListenComponent creates a new listen component
func NewListenComponent(cfg ComponentConfig, router *Router) *ListenComponent {
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second // Default timeout
	}

	// Initialize auth manager
	authManager, err := NewAuthManager(cfg.Auth)
	if err != nil {
		logger.Errorf("Failed to create auth manager: %v", err)
		return nil
	}

	return &ListenComponent{
		tag:               cfg.Tag,
		listenAddr:        cfg.ListenAddr,
		timeout:           timeout,
		replaceOldMapping: cfg.ReplaceOldMapping,
		detour:            cfg.Detour,
		router:            router,
		mappings:          make(map[string]*AddrMapping),
		mappingsRead:      &map[string]*AddrMapping{},
		stopCh:            make(chan struct{}),
		authManager:       authManager,
	}
}

// GetTag returns the component's tag
func (l *ListenComponent) GetTag() string {
	return l.tag
}

// Start initializes and starts the listener
func (l *ListenComponent) Start() error {
	conn, err := net.ListenPacket("udp", l.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to set up packet listener: %w", err)
	}

	l.conn = conn
	logger.Infof("%s is listening on %s", l.tag, conn.LocalAddr())

	// Start packet handling routine
	go l.handlePackets()

	return nil
}

// Stop closes the listener
func (l *ListenComponent) Stop() error {
	if l.stopped {
		return nil
	}

	l.stopped = true
	close(l.stopCh)
	return l.conn.Close()
}

// performCleanup handles the cleaning of inactive mappings
func (l *ListenComponent) performCleanup() {
	now := time.Now()
	isSync := false

	// Remove inactive mappings
	for addrString, mapping := range l.mappings {
		if now.Sub(mapping.lastActive) > l.timeout {
			delete(l.mappings, addrString)
			isSync = true
			logger.Warnf("%s: Removed inactive mapping: %s", l.tag, addrString)
		}
	}

	if isSync {
		l.syncMapping()
	}
}

func (l *ListenComponent) syncMapping() error {
	mappingsTemp := make(map[string]*AddrMapping)
	maps.Copy(mappingsTemp, l.mappings)
	l.mappingsRead = &mappingsTemp
	return nil
}

func (l *ListenComponent) SendPacket(packet Packet, metadata any) error {
	addr, ok := metadata.(net.Addr)
	if !ok {
		return fmt.Errorf("%s: Invalid address type", l.tag)
	}

	if addr == nil {
		return fmt.Errorf("%s: Address is nil", l.tag)
	}

	_, err := l.conn.WriteTo(packet.buffer, addr)
	if err != nil {
		logger.Infof("%s: Failed to send packet: %v", l.tag, err)
		return err
	}

	return nil
}

// handleAuthMessage processes authentication messages
func (l *ListenComponent) handleAuthMessage(buffer []byte, length int, addr net.Addr) error {
	header, err := ParseHeader(buffer[:length])
	if err != nil {
		return err
	}

	addrKey := addr.String()

	switch header.MsgType {
	case MsgTypeAuthChallenge:
		// Get or create auth state
		mapping, exists := l.mappings[addrKey]
		if !exists {
			mapping = &AddrMapping{
				addr:       addr,
				lastActive: time.Now(),
				authState:  &AuthState{},
			}
			l.mappings[addrKey] = mapping
			l.syncMapping()
		}

		// Process challenge and send response
		data := buffer[HeaderSize : HeaderSize+header.Length]
		err := l.authManager.ProcessAuthChallenge(data, mapping.authState)
		if err != nil {
			// Authentication failed - silently drop packet
			return nil
		}

		// Create response
		responseBuffer := l.router.GetBuffer()
		l.router.PutBuffer(responseBuffer)
		responseLen, err := l.authManager.CreateAuthChallenge(responseBuffer, MsgTypeAuthResponse)
		if err != nil {
			logger.Warnf("%s: Failed to create auth challenge response: %v", l.tag, err)
		}

		// Send response
		_, err = l.conn.WriteTo(responseBuffer[:responseLen], addr)
		if err != nil {
			logger.Warnf("%s: Failed to send auth response: %v", l.tag, err)
		}

		atomic.StoreInt32(&mapping.authState.authenticated, 1)

		mapping.lastActive = time.Now()
		logger.Infof("%s: Authentication successful for %s", l.tag, addr.String())

	case MsgTypeHeartbeat:

		// Update mapping if exists
		if mapping, exists := l.mappings[addrKey]; exists {
			mapping.lastActive = time.Now()
			if mapping.authState != nil {
				mapping.authState.UpdateHeartbeat()

				// Echo heartbeat back
				responseBuffer := l.router.GetBuffer()
				responseLen := CreateHeartbeat(responseBuffer)
				l.conn.WriteTo(responseBuffer[:responseLen], addr)
				l.router.PutBuffer(responseBuffer)

			}
		}

	case MsgTypeDisconnect:
		// Remove mapping
		delete(l.mappings, addrKey)
		l.syncMapping()
		logger.Infof("%s: Client %s disconnected", l.tag, addr.String())
	}

	return nil
}

// handlePackets processes incoming UDP packets
func (l *ListenComponent) handlePackets() {
	cleanupInterval := l.timeout / 2
	lastCleanupTime := time.Now()
	shortDeadline := min(time.Second*5, cleanupInterval)

	for {
		select {
		case <-l.stopCh:
			return
		default:
			// Check if it's time to do cleanup
			now := time.Now()
			if now.Sub(lastCleanupTime) >= cleanupInterval {
				l.performCleanup()
				lastCleanupTime = now
			}

			// Set read deadline
			if err := l.conn.SetReadDeadline(time.Now().Add(shortDeadline)); err != nil {
				logger.Warnf("%s: Error setting read deadline: %v", l.tag, err)
			}

			buffer := l.router.GetBuffer()
			length, addr, err := l.conn.ReadFrom(buffer)

			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				l.router.PutBuffer(buffer)
				continue
			} else if err != nil {
				if l.stopped {
					return
				}
				logger.Warnf("%s: Read error: %v", l.tag, err)
				l.router.PutBuffer(buffer)
				continue
			}

			// Handle authentication if enabled
			if l.authManager != nil {
				if length < HeaderSize {
					l.router.PutBuffer(buffer)
					continue
				}

				header, err := ParseHeader(buffer[:length])
				if err != nil {
					l.router.PutBuffer(buffer)
					continue
				}

				// Handle auth messages
				if header.MsgType != MsgTypeData {
					l.handleAuthMessage(buffer, length, addr)
					l.router.PutBuffer(buffer)
					continue
				}

				logger.Debugf("%s: Received data from %s", l.tag, addr.String())

				// For data messages, check authentication
				addrKey := addr.String()
				mapping, exists := l.mappings[addrKey]
				if !exists || mapping.authState == nil || !mapping.authState.IsAuthenticated() {
					// Not authenticated - silently drop
					l.router.PutBuffer(buffer)
					continue
				}

				// Unwrap data
				unwrappedData, err := l.authManager.UnwrapData(buffer[:length], l.router.GetBuffer())
				if err != nil {
					logger.Warnf("%s: Failed to unwrap data: %v", l.tag, err)
					l.router.PutBuffer(buffer)
					continue
				}

				// Create new buffer with unwrapped data
				l.router.PutBuffer(buffer)
				buffer = unwrappedData
				length = len(unwrappedData)

				mapping.lastActive = time.Now()
			}

			// Handle address mapping for non-auth mode
			addrKey := addr.String()
			if l.authManager == nil {
				// Check if this is a new mapping
				if _, exists := l.mappings[addrKey]; !exists {
					// If we should replace old connections with the same IP
					if l.replaceOldMapping {
						addrIP := addr.(*net.UDPAddr).IP.String()

						for key, mapping := range l.mappings {
							if mapping.addr.(*net.UDPAddr).IP.String() == addrIP {
								logger.Warnf("%s: Replacing old mapping: %s", l.tag, mapping.addr.String())
								delete(l.mappings, key)
							}
						}
					}

					// Add the new mapping
					logger.Warnf("%s: New mapping: %s", l.tag, addr.String())
					l.mappings[addrKey] = &AddrMapping{addr: addr, lastActive: time.Now()}
					l.syncMapping()
				} else {
					// Update the last active time for existing mapping
					l.mappings[addrKey].lastActive = time.Now()
				}
			}

			packet := NewPacket(buffer[:length], length, addr, l.tag, l.router)

			defer packet.Release(1)

			// Forward the packet to detour components
			if err := l.router.Route(packet, l.detour); err != nil {
				logger.Infof("%s: Error routing: %v", l.tag, err)
			}
		}
	}
}

// HandlePacket processes packets from other components
func (l *ListenComponent) HandlePacket(packet Packet) error {
	defer packet.Release(1)

	var packetToSend Packet

	if l.authManager != nil {

		buffer := l.router.GetBuffer()
		length, err := l.authManager.WrapData(buffer, packet.buffer)
		if err != nil {
			l.router.PutBuffer(buffer)
			logger.Infof("%s: Failed to wrap packet: %v", l.tag, err)
			return err
		}

		wrappedPacket := NewPacket(buffer[:length], length, packet.srcAddr, l.tag, l.router)
		packetToSend = wrappedPacket
		defer packetToSend.Release(1)

	} else {
		packetToSend = packet
	}

	for _, mapping := range *l.mappingsRead {
		// Check authentication if required
		if l.authManager != nil && (mapping.authState == nil || !mapping.authState.IsAuthenticated()) {
			continue
		}

		if err := l.router.SendPacket(l, packetToSend, mapping.addr); err != nil {
			logger.Infof("%s: Failed to queue packet for sending: %v", l.tag, err)
		}
	}

	return nil
}
