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
	connID     ConnID     // Unique connection identifier
}

// ListenComponent implements a UDP listener with authentication
type ListenComponent struct {
	BaseComponent

	listenAddr        string
	timeout           time.Duration
	replaceOldMapping bool
	detour            []string
	broadcastMode     bool

	conn net.PacketConn

	mappings     map[string]*AddrMapping
	mappingsRead *map[string]*AddrMapping
	stopCh       chan struct{}

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
	authManager, err := NewAuthManager(cfg.Auth, router)
	if err != nil {
		logger.Errorf("Failed to create auth manager: %v", err)
		return nil
	}

	broadcastMode := true
	if cfg.BroadcastMode != nil && !*cfg.BroadcastMode {
		broadcastMode = false
	}

	return &ListenComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router),

		listenAddr:        cfg.ListenAddr,
		timeout:           timeout,
		replaceOldMapping: cfg.ReplaceOldMapping,
		detour:            cfg.Detour,
		mappings:          make(map[string]*AddrMapping),
		mappingsRead:      &map[string]*AddrMapping{},
		stopCh:            make(chan struct{}),
		authManager:       authManager,
		broadcastMode:     broadcastMode,
	}
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
			l.RemoveConnData(mapping.connID)
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

func (l *ListenComponent) SendPacket(packet *Packet, metadata any) error {
	addr, ok := metadata.(net.Addr)
	if !ok {
		return fmt.Errorf("%s: Invalid address type", l.tag)
	}

	if addr == nil {
		return fmt.Errorf("%s: Address is nil", l.tag)
	}

	_, err := l.conn.WriteTo(packet.GetData(), addr)
	if err != nil {
		logger.Infof("%s: Failed to send packet: %v", l.tag, err)
		return err
	}

	return nil
}

// handleAuthMessage processes authentication messages
func (l *ListenComponent) handleAuthMessage(header *ProtocolHeader, buffer []byte, addr net.Addr) error {

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
				connID:     l.generateConnID(),
			}
			l.mappings[addrKey] = mapping
			l.syncMapping()
		}

		// Process challenge and send response
		data := buffer[HeaderSize : HeaderSize+header.Length]
		err, forwardID, poolID := l.authManager.ProcessAuthChallenge(data, mapping.authState)
		if err != nil {
			logger.Infof("%s: %s Authentication challenge failed: %v", l.tag, addr.String(), err)
			return nil
		}

		if l.replaceOldMapping {
			addrIP := addr.(*net.UDPAddr).IP.String()
			isSync := false

			for key, mapping := range l.mappings {
				if mapping.addr.(*net.UDPAddr).IP.String() == addrIP && key != addrKey {
					logger.Warnf("%s: Replacing old mapping: %s", l.tag, mapping.addr.String())
					delete(l.mappings, key)
					l.RemoveConnData(mapping.connID)
					isSync = true
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
		l.RemoveConnData(l.mappings[addrKey].connID)
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

				length, addr, err := l.conn.ReadFrom(packet.buffer[packet.offset:])

				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					return
				} else if err != nil {
					logger.Warnf("%s: Read error: %v", l.tag, err)
					return
				}

				packet.length = length

				// Handle authentication if enabled
				if l.authManager != nil {
					if length < HeaderSize {
						logger.Infof("%s: %s Packet too short for header: %d bytes", l.tag, addr.String(), length)
						return
					}

					header, err := l.authManager.UnwrapData(&packet)
					if err != nil {
						logger.Infof("%s: %s Failed to unwrap data: %v", l.tag, addr.String(), err)
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
					if _, exists := l.mappings[addrKey]; !exists {
						// If we should replace old connections with the same IP
						if l.replaceOldMapping {
							addrIP := addr.(*net.UDPAddr).IP.String()

							for key, mapping := range l.mappings {
								if mapping.addr.(*net.UDPAddr).IP.String() == addrIP {
									logger.Warnf("%s: Replacing old mapping: %s", l.tag, mapping.addr.String())
									delete(l.mappings, key)
									l.RemoveConnData(mapping.connID)
								}
							}
						}

						// Add the new mapping
						logger.Warnf("%s: New mapping: %s", l.tag, addr.String())
						connID := l.generateConnID()
						l.mappings[addrKey] = &AddrMapping{addr: addr, lastActive: time.Now(), connID: connID}
						l.syncMapping()
						packet.connID = connID
					} else {
						// Update the last active time for existing mapping
						l.mappings[addrKey].lastActive = time.Now()
						packet.connID = l.mappings[addr.String()].connID
					}
				}

				packet.srcAddr = addr

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
		l.authManager.WrapData(packet)
	}

	if l.broadcastMode {
		for _, mapping := range *l.mappingsRead {
			// Check authentication if required
			if l.authManager != nil && (mapping.authState == nil || !mapping.authState.IsAuthenticated()) {
				continue
			}

			if err := l.router.SendPacket(l, packet, mapping.addr); err != nil {
				logger.Infof("%s: Failed to queue packet for sending: %v", l.tag, err)
			}
		}
	} else {
		// Direct mode: send only to the specific connection ID
		if packet.connID == (ConnID{}) {
			logger.Infof("%s: Packet has no connection ID, dropping", l.tag)
			return nil
		}
		// Find the mapping that matches the connection ID
		for _, mapping := range *l.mappingsRead {
			if mapping.connID == packet.connID {
				// Check authentication if required
				if l.authManager != nil && (mapping.authState == nil || !mapping.authState.IsAuthenticated()) {
					logger.Debugf("%s: Connection not authenticated, dropping packet", l.tag)
					return nil
				}

				if err := l.router.SendPacket(l, packet, mapping.addr); err != nil {
					logger.Infof("%s: Failed to queue packet for sending: %v", l.tag, err)
				}
				return nil
			}
		}

		logger.Debugf("%s: No mapping found for connection ID: %s", l.tag, packet.connID)

	}

	return nil
}
