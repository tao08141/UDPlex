package main

import (
	"fmt"
	"log"
	"net"
	"sync"
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

	conn          net.PacketConn
	router        *Router
	mappings      map[string]AddrMapping
	activeClients map[string]struct{}
	mu            sync.RWMutex
	stopCh        chan struct{}
	stopped       bool
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
		mappings:        make(map[string]AddrMapping),
		activeClients:   make(map[string]struct{}),
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

	// Start the cleanup goroutine
	go l.cleanupRoutine()

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

// cleanupRoutine periodically cleans up inactive mappings
func (l *ListenComponent) cleanupRoutine() {
	ticker := time.NewTicker(l.timeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			now := time.Now()

			l.mu.Lock()
			// Process activity markers and update timestamps
			for addrString := range l.activeClients {
				if mapping, exists := l.mappings[addrString]; exists {
					mapping.lastActive = now
					l.mappings[addrString] = mapping
				}
			}

			// Clear activity tracking for next cycle
			l.activeClients = make(map[string]struct{})

			// Remove inactive mappings
			for addrString, mapping := range l.mappings {
				if now.Sub(mapping.lastActive) > l.timeout {
					delete(l.mappings, addrString)
					log.Printf("%s: Removed inactive mapping: %s", l.tag, addrString)
				}
			}
			l.mu.Unlock()
		}
	}
}

// handlePackets processes incoming UDP packets
func (l *ListenComponent) handlePackets() {

	for {
		select {
		case <-l.stopCh:
			return
		default:
			buffer := l.router.GetBuffer()
			length, addr, err := l.conn.ReadFrom(buffer)
			if err != nil {
				if l.stopped {
					return
				}
				log.Printf("%s: Read error: %v", l.tag, err)
				continue
			}

			if length == 0 {
				continue
			}

			addrKey := addr.String()

			// Check if this is a new mapping
			l.mu.RLock()
			_, exists := l.mappings[addrKey]
			l.mu.RUnlock()

			if !exists {
				l.mu.Lock()

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
				if _, ok := l.mappings[addrKey]; !ok {
					log.Printf("%s: New mapping: %s", l.tag, addr.String())
					l.mappings[addrKey] = AddrMapping{addr: addr, lastActive: time.Now()}
				}
				l.mu.Unlock()
			}

			l.activeClients[addrKey] = struct{}{}

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
	// When receiving from another component, broadcast to all known mappings
	l.mu.RLock()
	defer l.mu.RUnlock()

	data := packet.buffer[:packet.length]

	for _, mapping := range l.mappings {
		if _, err := l.conn.WriteTo(data, mapping.addr); err != nil {
			log.Printf("%s: Error writing to %s: %v", l.tag, mapping.addr, err)
			// We don't remove mappings here to avoid map changes during iteration
		}
	}

	l.router.PutBuffer(packet.buffer)
	return nil
}
