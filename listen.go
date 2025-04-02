package main

import (
	"fmt"
	"log"
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

	listenAddr      string
	timeout         time.Duration
	replaceOldConns bool
	detour          []string

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
		tag:             cfg.Tag,
		listenAddr:      cfg.ListenAddr,
		timeout:         timeout,
		replaceOldConns: cfg.ReplaceOldConns,
		detour:          cfg.Detour,
		router:          router,
		mappings:        make(map[string]*AddrMapping),
		mappingsRead:    &map[string]*AddrMapping{},
		stopCh:          make(chan struct{}),
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
	log.Printf("%s is listening on %s", l.tag, conn.LocalAddr())

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
			log.Printf("%s: Removed inactive mapping: %s", l.tag, addrString)
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
				log.Printf("%s: Error setting read deadline: %v", l.tag, err)
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
				log.Printf("%s: Read error: %v", l.tag, err)
				l.router.PutBuffer(buffer)
				continue
			}

			addrKey := addr.String()

			// Check if this is a new mapping
			if _, exists := l.mappings[addrKey]; !exists {
				// If we should replace old connections with the same IP
				if l.replaceOldConns {
					addrIP := addr.(*net.UDPAddr).IP.String()

					for key, mapping := range l.mappings {
						if mapping.addr.(*net.UDPAddr).IP.String() == addrIP {
							log.Printf("%s: Replacing old mapping: %s", l.tag, mapping.addr.String())
							delete(l.mappings, key)
						}
					}
				}

				// Add the new mapping
				log.Printf("%s: New mapping: %s", l.tag, addr.String())
				l.mappings[addrKey] = &AddrMapping{addr: addr, lastActive: time.Now()}
				l.syncMapping()
			} else {
				// Update the last active time for existing mapping
				l.mappings[addrKey].lastActive = time.Now()
			}

			packet := Packet{
				buffer:  buffer,
				length:  length,
				srcAddr: addr,
				srcTag:  l.tag,
			}

			// Forward the packet to detour components
			if err := l.router.Route(packet, l.detour); err != nil {
				log.Printf("%s: Error routing: %v", l.tag, err)
			}
		}
	}
}

// HandlePacket processes packets from other components
func (l *ListenComponent) HandlePacket(packet Packet) error {

	data := packet.buffer[:packet.length]
	mappings := *l.mappingsRead

	for _, mapping := range mappings {
		if _, err := l.conn.WriteTo(data, mapping.addr); err != nil {
			log.Printf("%s: Error writing to %s: %v", l.tag, mapping.addr, err)
		}
	}

	l.router.PutBuffer(packet.buffer)
	return nil
}
