package main

import (
	"fmt"
)

// FilterComponent implements protocol-based packet filtering
type FilterComponent struct {
	BaseComponent
	detour            map[string][]string // Maps protocol names to destination tags
	detourMiss        []string            // Default detour for undetected protocols
	protocolDetector  *ProtocolDetector
	useProtoDetectors []string // Protocol detector tags to use
}

// NewFilterComponent creates a new filter component
func NewFilterComponent(cfg FilterComponentConfig, router *Router, protoDetector *ProtocolDetector) *FilterComponent {

	return &FilterComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router, 0),

		detour:            cfg.Detour,
		detourMiss:        cfg.DetourMiss,
		protocolDetector:  protoDetector,
		useProtoDetectors: cfg.UseProtoDetectors,
	}
}

// GetTag returns the component's tag
func (f *FilterComponent) GetTag() string {
	return f.tag
}

// Start initializes the filter component
func (f *FilterComponent) Start() error {
	logger.Infof("%s: Starting filter component", f.tag)
	return nil
}

// Stop stops the filter component
func (f *FilterComponent) Stop() error {
	close(f.GetStopChannel())
	return nil
}

func (f *FilterComponent) SendPacket(packet *Packet, addr any) error {
	return nil
}

// HandlePacket processes and routes packets based on detected protocol
func (f *FilterComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	// Detect protocol
	proto := f.protocolDetector.DetectProtocol(packet.GetData(), f.useProtoDetectors)

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
