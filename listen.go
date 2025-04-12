package main

import (
	"fmt"
	"maps"
	"net"
	"time"
)

// AddrMapping stores each mapped address and its last active timestamp
type AddrMapping struct {
	addr       net.Addr
	lastActive time.Time
}

// ListenComponent implements a UDP listener
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
}

// NewListenComponent creates a new listen component
func NewListenComponent(cfg ComponentConfig, router *Router) *ListenComponent {
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second // Default timeout
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

	// Start packet handling routine (which now also handles cleanup)
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

// handlePackets processes incoming UDP packets and handles cleanup
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

			// Set a shorter read deadline to ensure we check for cleanup regularly
			if err := l.conn.SetReadDeadline(time.Now().Add(shortDeadline)); err != nil {
				logger.Warnf("%s: Error setting read deadline: %v", l.tag, err)
			}

			buffer := l.router.GetBuffer()
			length, addr, err := l.conn.ReadFrom(buffer)

			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Just a read timeout - continue the loop to check if cleanup is needed
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

			packet := Packet{
				buffer:  buffer[:length],
				length:  length,
				srcAddr: addr,
				srcTag:  l.tag,
				count:   0,
				router:  l.router,
			}

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

	for _, mapping := range *l.mappingsRead {
		if err := l.router.SendPacket(l, packet, mapping.addr); err != nil {
			logger.Infof("%s: Failed to queue packet for sending: %v", l.tag, err)
		}
	}

	return nil
}
