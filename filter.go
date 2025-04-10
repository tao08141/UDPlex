package main

import (
	"fmt"
	"log"
)

// FilterComponent implements protocol-based packet filtering
type FilterComponent struct {
	tag               string
	detour            map[string][]string // Maps protocol names to destination tags
	defaultDetour     []string            // Default route if no protocol match
	protocolDetector  *ProtocolDetector
	router            *Router
	useProtoDetectors []string // Protocol detector tags to use
	stopCh            chan struct{}
	stopped           bool
}

// FilterComponentConfig represents the configuration for a filter component
type FilterComponentConfig struct {
	Type              string              `json:"type"`
	Tag               string              `json:"tag"`
	Detour            map[string][]string `json:"detour"`
	DefaultAction     string              `json:"default_action"`
	DefaultDetour     []string            `json:"default_detour"`
	UseProtoDetectors []string            `json:"use_proto_detectors"`
}

// NewFilterComponent creates a new filter component
func NewFilterComponent(cfg FilterComponentConfig, router *Router, protoDetector *ProtocolDetector) *FilterComponent {
	var defaultDetour []string

	if cfg.DefaultAction == "drop" {
		defaultDetour = nil
	} else if len(cfg.DefaultDetour) > 0 {
		defaultDetour = cfg.DefaultDetour
	} else {
		// If default_detour not specified, all protocols without specific routes will be dropped
		defaultDetour = nil
	}

	return &FilterComponent{
		tag:               cfg.Tag,
		detour:            cfg.Detour,
		defaultDetour:     defaultDetour,
		protocolDetector:  protoDetector,
		useProtoDetectors: cfg.UseProtoDetectors,
		router:            router,
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
	// Detect protocol
	proto := f.protocolDetector.DetectProtocol(packet.buffer[:packet.length], packet.length, f.useProtoDetectors)

	// Store detected protocol in packet
	packet.proto = proto

	var destTags []string

	if proto != "" {
		log.Printf("%s: Detected protocol: %s", f.tag, proto)

		// Route packet to destination(s)
		if err := f.router.Route(packet, destTags); err != nil {
			log.Printf("%s: Error routing: %v", f.tag, err)
			packet.Release(1)
			return fmt.Errorf("routing error: %w", err)
		}
	}

	packet.Release(1)

	return nil
}
