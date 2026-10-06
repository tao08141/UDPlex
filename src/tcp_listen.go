package main

import (
	"fmt"
	"io"
	"net"
	"sync"
)

// TcpListenComponent accepts TCP connections. With detour it carries each
// connection as a stream through the pipeline to a tcp_forward, which dials
// target. With forwarders it dials them directly (e.g. a peer's address on a
// wg interface) and relays the bytes.
type TcpListenComponent struct {
	BaseComponent

	listenAddr string
	target     string
	forwarders []outboundForwarderSpec
	noDelay    bool
	listener   net.Listener
	streams    *tcpStreamEndpoint // nil in direct mode

	mu    sync.Mutex
	conns map[net.Conn]struct{} // direct mode connections, closed on Stop
}

func NewTcpListenComponent(cfg ComponentConfig, router *Router) (*TcpListenComponent, error) {
	if cfg.ListenAddr == "" {
		return nil, fmt.Errorf("%s: listen_addr is required", cfg.Tag)
	}
	l := &TcpListenComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router, 0),
		listenAddr:    cfg.ListenAddr,
		target:        cfg.Target,
		noDelay:       cfg.NoDelay == nil || *cfg.NoDelay,
		conns:         make(map[net.Conn]struct{}),
	}
	if len(cfg.Forwarders) > 0 {
		if len(cfg.Detour) > 0 {
			return nil, fmt.Errorf("%s: set either forwarders (direct) or detour (stream), not both", cfg.Tag)
		}
		for _, raw := range cfg.Forwarders {
			spec, err := parseOutboundForwarderSpec(raw, cfg.InterfaceName)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", cfg.Tag, err)
			}
			l.forwarders = append(l.forwarders, spec)
		}
		return l, nil
	}
	if len(cfg.Detour) == 0 {
		return nil, fmt.Errorf("%s: either forwarders or detour is required", cfg.Tag)
	}
	if cfg.Target == "" {
		return nil, fmt.Errorf("%s: target is required with detour", cfg.Tag)
	}
	l.streams = newTcpStreamEndpoint(cfg, router, true)
	return l, nil
}

func (l *TcpListenComponent) Start() error { return nil }

// PostStart listens once every component has started, so listen_addr can be
// an address of a wg interface created in its Start.
func (l *TcpListenComponent) PostStart() error {
	ln, err := net.Listen("tcp", l.listenAddr)
	if err != nil {
		return err
	}
	l.listener = ln
	logger.Infof("%s: Listening on tcp %s", l.tag, l.listenAddr)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-l.stopCh:
					return
				default:
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				logger.Warnf("%s: Accept error: %v", l.tag, err)
				return
			}
			logger.Debugf("%s: Accepted connection from %s", l.tag, conn.RemoteAddr())
			if l.streams != nil {
				l.streams.open(conn, l.target)
			} else {
				go l.relay(conn)
			}
		}
	}()
	return nil
}

// relay connects conn to the first forwarder that accepts and copies both ways.
func (l *TcpListenComponent) relay(conn net.Conn) {
	var remote net.Conn
	for _, spec := range l.forwarders {
		c, err := dialTCPWithInterfaceTimeout(spec.address, spec.interfaceName, tcpStreamOpenTimeout)
		if err == nil {
			remote = c
			break
		}
		logger.Infof("%s: Failed to connect to %s: %v", l.tag, formatOutboundRoute(spec.address, spec.interfaceName), err)
	}
	if remote == nil {
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetLinger(0)
		}
		_ = conn.Close()
		return
	}
	for _, c := range []net.Conn{conn, remote} {
		if tcpConn, ok := c.(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(l.noDelay)
		}
	}
	if !l.trackConns(conn, remote) {
		_ = conn.Close()
		_ = remote.Close()
		return
	}
	defer l.untrackConns(conn, remote)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		copyAndCloseWrite(remote, conn)
	}()
	copyAndCloseWrite(conn, remote)
	wg.Wait()
	_ = conn.Close()
	_ = remote.Close()
}

func copyAndCloseWrite(dst, src net.Conn) {
	if _, err := io.Copy(dst, src); err != nil {
		// Reset both sides instead of leaving the other direction hanging.
		_ = src.Close()
		_ = dst.Close()
		return
	}
	closeWrite(dst)
}

func (l *TcpListenComponent) trackConns(conns ...net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conns == nil {
		return false
	}
	for _, c := range conns {
		l.conns[c] = struct{}{}
	}
	return true
}

func (l *TcpListenComponent) untrackConns(conns ...net.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range conns {
		delete(l.conns, c)
	}
}

func (l *TcpListenComponent) Stop() error {
	close(l.stopCh)
	if l.listener != nil {
		_ = l.listener.Close()
	}
	if l.streams != nil {
		l.streams.stop()
	}
	l.mu.Lock()
	conns := l.conns
	l.conns = nil
	l.mu.Unlock()
	for c := range conns {
		_ = c.Close()
	}
	return nil
}

// HandlePacket receives the stream frames coming back from tcp_forward.
func (l *TcpListenComponent) HandlePacket(packet *Packet) error {
	if l.streams == nil {
		packet.Release(1)
		return nil
	}
	return l.streams.handlePacket(packet)
}

// ActiveStreams returns the number of open streams (or relayed connections).
func (l *TcpListenComponent) ActiveStreams() int64 {
	if l.streams != nil {
		return l.streams.active.Load()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return int64(len(l.conns) / 2)
}
