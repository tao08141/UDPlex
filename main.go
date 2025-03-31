package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "net"
    "os"
    "sync"

)

// ComponentConfig represents the common configuration for all components
type ComponentConfig struct {
    Type              string   `json:"type"`
    Tag               string   `json:"tag"`
    ListenAddr        string   `json:"listen_addr"`
    BufferSize        int      `json:"buffer_size"`
    Timeout           int      `json:"timeout"`
    ReplaceOldConns   bool     `json:"replace_old_conns"`
    Forwarders        []string `json:"forwarders"`
    QueueSize         int      `json:"queue_size"`
    ReconnectInterval int      `json:"reconnect_interval"`
    ConnectionCheckTime int     `json:"connection_check_time"`
    Detour            []string `json:"detour"`
}

// Component is the interface that all network components must implement
type Component interface {
    Start() error
    Stop() error
    GetTag() string
    // HandlePacket processes packets coming from other components
    // srcTag is the tag of the component that sent the packet
    HandlePacket(data []byte, srcAddr net.Addr, srcTag string) error
}

// Router manages all components and routes packets between them
type Router struct {
    components map[string]Component
    mu         sync.RWMutex
}

// NewRouter creates a new router
func NewRouter() *Router {
    return &Router{
        components: make(map[string]Component),
    }
}

// Register adds a component to the router
func (r *Router) Register(c Component) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    
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
    r.mu.RLock()
    defer r.mu.RUnlock()
    c, exists := r.components[tag]
    return c, exists
}

// Route sends a packet to components specified by their tags
func (r *Router) Route(data []byte, srcAddr net.Addr, srcTag string, destTags []string) error {
    for _, tag := range destTags {
        if tag == srcTag {
            continue // Don't route back to source
        }
        
        c, exists := r.GetComponent(tag)
        if !exists {
            log.Printf("Warning: trying to route to non-existing component: %s", tag)
            continue
        }
        
        if err := c.HandlePacket(data, srcAddr, srcTag); err != nil {
            log.Printf("Error routing to %s: %v", tag, err)
        }
    }
    return nil
}

// StartAll starts all registered components
func (r *Router) StartAll() error {
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    for tag, component := range r.components {
        log.Printf("Starting component: %s", tag)
        if err := component.Start(); err != nil {
            return fmt.Errorf("failed to start component %s: %w", tag, err)
        }
    }
    return nil
}

// StopAll stops all registered components
func (r *Router) StopAll() {
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    for tag, component := range r.components {
        log.Printf("Stopping component: %s", tag)
        if err := component.Stop(); err != nil {
            log.Printf("Error stopping component %s: %v", tag, err)
        }
    }
}

func main() {
    log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
    
    configPath := flag.String("c", "config.json", "Path to configuration file")
    flag.Parse()
    
    // Load configuration
    configData, err := os.ReadFile(*configPath)
    if err != nil {
        log.Fatalf("Failed to read config: %v", err)
    }
    
    var configs []ComponentConfig
    if err := json.Unmarshal(configData, &configs); err != nil {
        log.Fatalf("Failed to parse config: %v", err)
    }
    
    // Initialize router
    router := NewRouter()
    
    // Create components based on config
    for _, cfg := range configs {
        var component Component
        
        switch cfg.Type {
        case "listen":
            component = NewListenComponent(cfg, router)
        case "forward":
            component = NewForwardComponent(cfg, router)
        default:
            log.Printf("Unknown component type: %s", cfg.Type)
            continue
        }
        
        if err := router.Register(component); err != nil {
            log.Printf("Failed to register component %s: %v", cfg.Tag, err)
        }
    }
    
    // Start all components
    if err := router.StartAll(); err != nil {
        log.Fatalf("Failed to start components: %v", err)
    }
    
    // Wait indefinitely
    select {}
}