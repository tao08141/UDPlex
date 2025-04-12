package main

import (
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"
)

// ForwardConn represents a connection to a forwarder
type ForwardConn struct {
	conn                 *net.UDPConn
	isConnected          int32 // Atomic flag: 0=disconnected, 1=connected
	remoteAddr           string
	udpAddr              *net.UDPAddr
	lastReconnectAttempt time.Time
}

// ForwardComponent implements a UDP forwarder
type ForwardComponent struct {
	tag                 string
	forwarders          []string
	reconnectInterval   time.Duration
	connectionCheckTime time.Duration
	detour              []string
	sendKeepalive       bool

	router          *Router
	forwardConns    map[string]*ForwardConn
	forwardConnList []*ForwardConn
	stopCh          chan struct{}
	stopped         bool
}

// NewForwardComponent creates a new forward component
func NewForwardComponent(cfg ComponentConfig, router *Router) *ForwardComponent {

	reconnectInterval := time.Duration(cfg.ReconnectInterval) * time.Second
	if reconnectInterval == 0 {
		reconnectInterval = 5 * time.Second // Default reconnect interval
	}

	connectionCheckTime := time.Duration(cfg.ConnectionCheckTime) * time.Second
	if connectionCheckTime == 0 {
		connectionCheckTime = 30 * time.Second // Default connection check interval
	}

	sendKeepalive := true
	if cfg.SendKeepalive != nil {
		sendKeepalive = *cfg.SendKeepalive
	}

	return &ForwardComponent{
		tag:                 cfg.Tag,
		forwarders:          cfg.Forwarders,
		reconnectInterval:   reconnectInterval,
		connectionCheckTime: connectionCheckTime,
		detour:              cfg.Detour,
		sendKeepalive:       sendKeepalive,
		router:              router,
		forwardConns:        make(map[string]*ForwardConn),
		stopCh:              make(chan struct{}),
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
			log.Printf("%s: Failed to initialize forwarder %s: %v", f.tag, addr, err)
			continue
		}
		f.forwardConns[addr] = conn
		f.forwardConnList = append(f.forwardConnList, conn)
	}

	// Start connection checker routine
	go f.connectionChecker()

	return nil
}

// Stop closes all forwarder connections
func (f *ForwardComponent) Stop() error {
	if f.stopped {
		return nil
	}

	f.stopped = true
	close(f.stopCh)

	for _, conn := range f.forwardConnList {
		if conn.conn != nil {
			conn.conn.Close()
		}
	}

	return nil
}

// connectionChecker periodically checks and reconnects if needed
func (f *ForwardComponent) connectionChecker() {
	ticker := time.NewTicker(f.connectionCheckTime)
	defer ticker.Stop()

	for {
		select {
		case <-f.stopCh:
			return
		case <-ticker.C:
			for _, conn := range f.forwardConnList {
				if atomic.LoadInt32(&conn.isConnected) == 0 {
					go f.tryReconnect(conn)
				} else if f.sendKeepalive {
					conn.conn.Write([]byte{})
				}
			}
		}
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
	}

	// Start goroutine to handle receiving packets
	go f.readFromForwarder(forwardConn)

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

	log.Printf("%s: Attempting to reconnect to %s", f.tag, conn.remoteAddr)

	newConn, err := net.DialUDP("udp", nil, conn.udpAddr)
	if err != nil {
		log.Printf("%s: Reconnection to %s failed: %v", f.tag, conn.remoteAddr, err)
		return
	}

	conn.conn = newConn
	atomic.StoreInt32(&conn.isConnected, 1)
	log.Printf("%s: Successfully reconnected to %s", f.tag, conn.remoteAddr)

	go f.readFromForwarder(conn)
}

// readFromForwarder handles receiving packets from a forwarder
func (f *ForwardComponent) readFromForwarder(conn *ForwardConn) {

	for atomic.LoadInt32(&conn.isConnected) == 1 {
		select {
		case <-f.stopCh:
			return
		default:
			conn.conn.SetReadDeadline(time.Now().Add(f.connectionCheckTime))
			buffer := f.router.GetBuffer()
			length, err := conn.conn.Read(buffer)

			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}

				log.Printf("%s: Error reading from %s: %v", f.tag, conn.remoteAddr, err)
				atomic.StoreInt32(&conn.isConnected, 0)
				return
			}

			packet := Packet{
				buffer:  buffer[:length],
				length:  length,
				srcAddr: conn.udpAddr,
				srcTag:  f.tag,
				count:   0,
				router:  f.router,
			}

			// Forward to detour components
			if err := f.router.Route(packet, f.detour); err != nil {
				log.Printf("%s: Error routing: %v", f.tag, err)
			}
		}

	}
}

func (f *ForwardComponent) SendPacket(packet Packet, metadata any) error {
	conn, ok := metadata.(*ForwardConn)
	if !ok || conn == nil {
		return fmt.Errorf("invalid connection type")
	}

	if atomic.LoadInt32(&conn.isConnected) == 0 || conn.conn == nil {
		return nil // Connection isn't available, just skip
	}

	_, err := conn.conn.Write(packet.buffer)
	if err != nil {
		log.Printf("%s: Error writing to %s: %v", f.tag, conn.remoteAddr, err)
		atomic.StoreInt32(&conn.isConnected, 0)
		return err
	}

	return nil
}

// HandlePacket processes packets from other components
func (f *ForwardComponent) HandlePacket(packet Packet) error {
	defer packet.Release(1)

	for _, conn := range f.forwardConnList {
		if atomic.LoadInt32(&conn.isConnected) == 1 {
			// Use the send queue instead of direct writing
			if err := f.router.SendPacket(f, packet, conn); err != nil {
				log.Printf("%s: Failed to queue packet for sending: %v", f.tag, err)
			}
		}
	}

	return nil
}
