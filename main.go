package main

import (
	"encoding/json"
	"flag"
	"os"

	"go.uber.org/zap"
)

// Component is the interface that all network components must implement
type Component interface {
	Start() error
	Stop() error
	GetTag() string
	// HandlePacket processes packets coming from other components
	// srcTag is the tag of the component that sent the packet
	HandlePacket(packet Packet) error
	SendPacket(packet Packet, metadata any) error
}

type routeTask struct {
	packet   Packet
	destTags []string
}

type sendTask struct {
	component Component
	packet    Packet
	metadata  any
}

func main() {
	defaultLogger, _ := zap.NewProduction()
	logger = defaultLogger.Sugar()

	configPath := flag.String("c", "config.json", "Path to configuration file")
	flag.Parse()

	// Load configuration
	configData, err := os.ReadFile(*configPath)
	if err != nil {
		logger.Fatalf("Failed to read config: %v", err)
	}

	var config Config
	if err := json.Unmarshal(configData, &config); err != nil {
		logger.Fatalf("Failed to parse config: %v", err)
	}

	// Initialize the real logger with config
	initLogger(config.Logging)
	logger.Info("Logger initialized")

	// Initialize router with buffer pool
	router := NewRouter(config)

	// Create protocol detector
	protocolDetector := NewProtocolDetector(config.ProtocolDetectors)

	// Create components based on config
	for _, cfgMap := range config.Services {
		// Get component type
		typeVal, ok := cfgMap["type"].(string)
		if !ok {
			logger.Warnf("Component missing type field, skipping")
			continue
		}

		// Convert generic config to specific config based on type
		cfgBytes, err := json.Marshal(cfgMap)
		if err != nil {
			logger.Warnf("Failed to marshal component config: %v", err)
			continue
		}

		var component Component

		switch typeVal {
		case "listen":
			var cfg ComponentConfig
			if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
				logger.Warnf("Failed to unmarshal listen config: %v", err)
				continue
			}
			component = NewListenComponent(cfg, router)

		case "forward":
			var cfg ComponentConfig
			if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
				logger.Warnf("Failed to unmarshal forward config: %v", err)
				continue
			}
			component = NewForwardComponent(cfg, router)

		case "filter":
			var cfg FilterComponentConfig
			if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
				logger.Warnf("Failed to unmarshal filter config: %v", err)
				continue
			}
			component = NewFilterComponent(cfg, router, protocolDetector)

		default:
			logger.Warnf("Unknown component type: %s", typeVal)
			continue
		}

		if err := router.Register(component); err != nil {
			logger.Warnf("Failed to register component: %v", err)
		}
	}

	// Start all components
	if err := router.StartAll(); err != nil {
		logger.Fatalf("Failed to start components: %v", err)
	}

	logger.Info("UDPlex started and ready")

	select {}
}
