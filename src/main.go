package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"

	"go.uber.org/zap"
)

var (
	version = "dev"
)

// stripJSONComments removes single-line (//) and multi-line (/* */) comments from JSON
func stripJSONComments(data []byte) ([]byte, error) {
	var result bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(data))

	inMultiLineComment := false

	for scanner.Scan() {
		line := scanner.Text()

		if inMultiLineComment {
			// Look for end of multi-line comment
			if idx := strings.Index(line, "*/"); idx != -1 {
				inMultiLineComment = false
				line = line[idx+2:]
			} else {
				continue
			}
		}

		// Process the line character by character
		var cleanLine strings.Builder
		inString := false
		escaped := false

		for i := 0; i < len(line); i++ {
			char := line[i]

			if escaped {
				cleanLine.WriteByte(char)
				escaped = false
				continue
			}

			if char == '\\' && inString {
				cleanLine.WriteByte(char)
				escaped = true
				continue
			}

			if char == '"' {
				inString = !inString
				cleanLine.WriteByte(char)
				continue
			}

			if !inString {
				// Check for single-line comment
				if i < len(line)-1 && line[i:i+2] == "//" {
					break
				}

				// Check for multi-line comment start
				if i < len(line)-1 && line[i:i+2] == "/*" {
					inMultiLineComment = true
					i++ // Skip the '*'
					continue
				}
			}

			cleanLine.WriteByte(char)
		}

		if cleanLine.Len() > 0 || !inMultiLineComment {
			result.WriteString(strings.TrimSpace(cleanLine.String()) + "\n")
		}
	}

	return result.Bytes(), scanner.Err()
}

func main() {
	defaultLogger, _ := zap.NewProduction()
	logger = defaultLogger.Sugar()
	logger.Warnf("UDPlex version: %s", version)

	configPath := flag.String("c", "config.json", "Path to configuration file")
	flag.Parse()

	// Load configuration
	configData, err := os.ReadFile(*configPath)
	if err != nil {
		logger.Fatalf("Failed to read config: %v", err)
	}

	// Strip comments from JSON
	cleanConfigData, err := stripJSONComments(configData)
	if err != nil {
		logger.Fatalf("Failed to process config comments: %v", err)
	}

	var config Config
	if err := json.Unmarshal(cleanConfigData, &config); err != nil {
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

		case "load_balancer":
			var cfg LoadBalancerComponentConfig
			if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
				logger.Warnf("Failed to unmarshal load_balancer config: %v", err)
				continue
			}
			var err error
			component, err = NewLoadBalancerComponent(cfg, router)
			if err != nil {
				logger.Warnf("Failed to create load_balancer component: %v", err)
				continue
			}

		case "tcp_tunnel_listen":
			var cfg ComponentConfig
			if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
				logger.Warnf("Failed to unmarshal tcp_tunnel_listen config: %v", err)
				continue
			}
			component = NewTcpTunnelListenComponent(cfg, router)
		case "tcp_tunnel_forward":
			var cfg ComponentConfig
			if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
				logger.Warnf("Failed to unmarshal tcp_tunnel_forward config: %v", err)
				continue
			}
			component = NewTcpTunnelForwardComponent(cfg, router)
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

	// Initialize and start API server
	apiConfig := config.API
	if apiConfig.Enabled {
		if apiConfig.Port == 0 {
			apiConfig.Port = 8080 // Default port
		}
		if apiConfig.Host == "" {
			apiConfig.Host = "0.0.0.0" // Default host
		}

		apiServer := NewAPIServer(apiConfig, router)
		if err := apiServer.Start(); err != nil {
			logger.Warnf("Failed to start API server: %v", err)
		} else {
			logger.Infof("API server started on %s:%d", apiConfig.Host, apiConfig.Port)
		}
	}

	select {}
}
