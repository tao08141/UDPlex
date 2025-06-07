package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"

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

// Router manages all components and routes packets between them
type Router struct {
	components map[string]Component
	bufferPool sync.Pool
	config     Config
	routeTasks chan routeTask
	sendTasks  chan sendTask
	wg         sync.WaitGroup
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

// NewRouter creates a new router
func NewRouter(config Config) *Router {
	// Set default worker count if not specified
	if config.WorkerCount <= 0 {
		config.WorkerCount = 4 // Default to 4 workers
	}

	if config.BufferSize <= 0 {
		config.BufferSize = 1500 // Default buffer size
	}

	if config.QueueSize <= 0 {
		config.QueueSize = 10240 // Default queue size
	}

	r := &Router{
		config:     config,
		components: make(map[string]Component),
		bufferPool: sync.Pool{
			New: func() any {
				buf := make([]byte, config.BufferSize+128)
				return &buf // Return pointer to slice
			},
		},
		routeTasks: make(chan routeTask, config.QueueSize),
		sendTasks:  make(chan sendTask, config.QueueSize), // Initialize send queue
	}

	// Start the worker pools
	r.startWorkers()

	return r
}

// startWorkers initializes the worker goroutines for packet routing
func (r *Router) startWorkers() {
	for i := range r.config.WorkerCount {
		r.wg.Add(1)
		go func(workerID int) {
			defer r.wg.Done()
			logger.Infof("Starting router worker %d", workerID)

			for {
				select {
				case task, ok := <-r.routeTasks:
					if !ok {
						logger.Warnf("Router worker %d: route tasks channel closed", workerID)
						return
					}
					r.processRouteTask(task)
				case task, ok := <-r.sendTasks:
					if !ok {
						logger.Warnf("Router worker %d: send tasks channel closed", workerID)
						return
					}
					if err := task.component.SendPacket(task.packet, task.metadata); err != nil {
						logger.Warnf("Error sending packet via %s: %v", task.component.GetTag(), err)
					}
					task.packet.Release(1)
				}
			}
		}(i)
	}
}

// processRouteTask handles the actual routing of packets
func (r *Router) processRouteTask(task routeTask) {
	packet := task.packet
	defer packet.Release(1) // Release our reference when done

	for _, tag := range task.destTags {
		if tag == packet.srcTag {
			continue // Don't route back to source
		}

		c, exists := r.GetComponent(tag)
		if !exists {
			logger.Warnf("Warning: trying to route to non-existing component: %s", tag)
			continue
		}

		packet.AddRef(1)
		if err := c.HandlePacket(packet); err != nil {
			logger.Warnf("Error routing to %s: %v", tag, err)
		}
	}
}

// SendPacket adds a packet to the send queue
func (r *Router) SendPacket(component Component, packet Packet, metadata any) error {
	packet.AddRef(1) // Add reference for the worker

	select {
	case r.sendTasks <- sendTask{component: component, packet: packet, metadata: metadata}:
		// Task successfully queued
	default:
		packet.Release(1) // Release reference if queue is full
		return fmt.Errorf("send queue is full, packet dropped")
	}
	return nil
}

// GetBuffer retrieves a buffer from the pool
func (r *Router) GetBuffer() []byte {
	return *(r.bufferPool.Get().(*[]byte))
}

// PutBuffer returns a buffer to the pool
func (r *Router) PutBuffer(buf []byte) {
	buf = buf[:r.config.BufferSize+128]
	r.bufferPool.Put(&buf)
}

// Register adds a component to the router
func (r *Router) Register(c Component) error {
	tag := c.GetTag()
	if tag == "" {
		return fmt.Errorf("component has empty tag")
	}

	if _, exists := r.components[tag]; exists {
		return fmt.Errorf("component with tag %s already registered", tag)
	}

	r.components[tag] = c
	return nil
}

// GetComponent returns a component by its tag
func (r *Router) GetComponent(tag string) (Component, bool) {
	c, exists := r.components[tag]
	return c, exists
}

// Route asynchronously sends a packet to components specified by their tags
func (r *Router) Route(packet Packet, destTags []string) error {
	packet.AddRef(1) // Add reference for the worker

	select {
	case r.routeTasks <- routeTask{packet: packet, destTags: destTags}:
		// Task successfully queued
	default:
		packet.Release(1) // Release reference if queue is full
		return fmt.Errorf("routing queue is full, packet dropped")
	}
	return nil
}

// StartAll starts all registered components
func (r *Router) StartAll() error {
	for tag, component := range r.components {
		logger.Infof("Starting component: %s", tag)
		if err := component.Start(); err != nil {
			return fmt.Errorf("failed to start component %s: %w", tag, err)
		}
	}
	return nil
}

// StopAll stops all registered components and worker pool
func (r *Router) StopAll() {
	// Stop components
	for tag, component := range r.components {
		logger.Infof("Stopping component: %s", tag)
		if err := component.Stop(); err != nil {
			logger.Warnf("Error stopping component %s: %v", tag, err)
		}
	}

	// Close task channel and wait for workers to complete
	close(r.routeTasks)
	r.wg.Wait()
	logger.Infof("All router workers stopped")
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
