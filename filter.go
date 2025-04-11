package main

import (
	"fmt"
	"log"
)

// FilterComponent implements protocol-based packet filtering
type FilterComponent struct {
	tag               string
	detour            map[string][]string // Maps protocol names to destination tags
	detourMiss        []string            // Default detour for undetected protocols
	protocolDetector  *ProtocolDetector
	router            *Router
	useProtoDetectors []string // Protocol detector tags to use
	stopCh            chan struct{}
	stopped           bool
}

// NewFilterComponent creates a new filter component
func NewFilterComponent(cfg FilterComponentConfig, router *Router, protoDetector *ProtocolDetector) *FilterComponent {

	return &FilterComponent{
		tag:               cfg.Tag,
		detour:            cfg.Detour,
		detourMiss:        cfg.DetourMiss,
		protocolDetector:  protoDetector,
		router:            router,
		useProtoDetectors: cfg.UseProtoDetectors,
		stopped:           false,
		stopCh:            make(chan struct{}),
	}
}

// GetTag returns the component's tag
func (f *FilterComponent) GetTag() string {
	return f.tag
}

// Start initializes the filter component
func (f *FilterComponent) Start() error {
	log.Printf("%s: Starting filter component", f.tag)
	return nil
}

// Stop stops the filter component
func (f *FilterComponent) Stop() error {
	if f.stopped {
		return nil
	}

	f.stopped = true
	close(f.stopCh)
	return nil
}

// HandlePacket processes and routes packets based on detected protocol
func (f *FilterComponent) HandlePacket(packet Packet) error {
	defer packet.Release(1)

	// Detect protocol
	proto := f.protocolDetector.DetectProtocol(packet.buffer, packet.length, f.useProtoDetectors)

	// Store detected protocol in packet
	packet.proto = proto

	if proto != "" {
		// Route packet to destination(s)
		if detour, ok := f.detour[proto]; ok {
			if err := f.router.Route(packet, detour); err != nil {
				return fmt.Errorf("routing error: %w", err)
			}
		} else {
			return fmt.Errorf("%s: No detour found for protocol %s", f.tag, proto)
		}

	} else {
		// If no protocol detected, route to default detour
		if err := f.router.Route(packet, f.detourMiss); err != nil {
			return fmt.Errorf("routing error: %w", err)
		}
	}

	return nil
}
