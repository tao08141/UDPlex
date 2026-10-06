package main

import "fmt"

// TcpForwardComponent ends the TCP streams of tcp_listen components: it
// dials the target of each stream and sends the replies through detour.
type TcpForwardComponent struct {
	BaseComponent
	streams *tcpStreamEndpoint
}

func NewTcpForwardComponent(cfg ComponentConfig, router *Router) (*TcpForwardComponent, error) {
	if len(cfg.Detour) == 0 {
		return nil, fmt.Errorf("%s: detour is required for the return path", cfg.Tag)
	}
	return &TcpForwardComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router, 0),
		streams:       newTcpStreamEndpoint(cfg, router, false),
	}, nil
}

func (f *TcpForwardComponent) Start() error {
	if f.streams.target != "" {
		logger.Infof("%s: Forwarding TCP streams to %s", f.tag, f.streams.target)
	} else {
		logger.Infof("%s: Forwarding TCP streams to the targets requested by tcp_listen", f.tag)
	}
	return nil
}

func (f *TcpForwardComponent) Stop() error {
	close(f.stopCh)
	f.streams.stop()
	return nil
}

func (f *TcpForwardComponent) HandlePacket(packet *Packet) error {
	return f.streams.handlePacket(packet)
}

// ActiveStreams returns the number of open streams.
func (f *TcpForwardComponent) ActiveStreams() int64 {
	return f.streams.active.Load()
}
