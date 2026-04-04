package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

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

	if config.BufferOffset <= 0 {
		config.BufferOffset = 64 // Default buffer offer size
	}

	r := &Router{
		config:     config,
		components: make(map[string]Component),
		bufferPool: sync.Pool{
			New: func() any {
				buf := make([]byte, config.BufferSize+config.BufferOffset)
				return &buf // Return pointer to slice
			},
		},
	}
	initialConnPool := make(connDataSnapshot)
	r.connPool.Store(&initialConnPool)

	return r
}

type connDataSnapshot map[ConnID]map[string]any

// Router manages all components and routes packets between them
type Router struct {
	components     map[string]Component
	bufferPool     sync.Pool
	bufferRefCount int32
	config         Config
	connPool       atomic.Pointer[connDataSnapshot]
}

func (r *Router) GetConnData(connID ConnID, tag string) any {
	current := r.connPool.Load()
	if current == nil {
		return nil
	}
	connData, exists := (*current)[connID]
	if !exists {
		return nil
	}
	return connData[tag]
}

func (r *Router) SetConnData(connID ConnID, tag string, data any) {
	for {
		currentPtr := r.connPool.Load()
		current := *currentPtr
		next := cloneConnDataSnapshot(current)
		connData := cloneConnDataEntry(next[connID])
		connData[tag] = data
		next[connID] = connData
		if r.connPool.CompareAndSwap(currentPtr, &next) {
			return
		}
	}
}

func (r *Router) RemoveConnData(connID ConnID, tag string) {
	for {
		currentPtr := r.connPool.Load()
		current := *currentPtr
		connData, exists := current[connID]
		if !exists {
			return
		}

		next := cloneConnDataSnapshot(current)
		nextConnData := cloneConnDataEntry(connData)
		delete(nextConnData, tag)
		if len(nextConnData) == 0 {
			delete(next, connID)
		} else {
			next[connID] = nextConnData
		}

		if r.connPool.CompareAndSwap(currentPtr, &next) {
			return
		}
	}
}

func cloneConnDataSnapshot(current connDataSnapshot) connDataSnapshot {
	next := make(connDataSnapshot, len(current))
	for connID, connData := range current {
		next[connID] = connData
	}
	return next
}

func cloneConnDataEntry(current map[string]any) map[string]any {
	if len(current) == 0 {
		return make(map[string]any)
	}
	next := make(map[string]any, len(current))
	for tag, data := range current {
		next[tag] = data
	}
	return next
}

// routePacket handles the actual routing of packets in the caller goroutine.
func (r *Router) routePacket(packet *Packet, destTags []string) {
	if len(destTags) == 1 {
		tag := destTags[0]
		if tag == packet.srcTag {
			return
		}
		c, exists := r.components[tag]
		if !exists {
			logger.Warnf("Warning: trying to route to non-existing component: %s", tag)
			return
		}
		packet.AddRef(1)
		if err := c.HandlePacket(packet); err != nil {
			logger.Warnf("Error routing to %s: %v", tag, err)
		}
		return
	}

	// Single-pass: collect valid targets, then dispatch.
	// Use a small stack-allocated array to avoid heap allocation for common cases.
	type dest struct {
		component Component
		tag       string
	}
	var stackBuf [8]dest
	targets := stackBuf[:0]

	for _, tag := range destTags {
		if tag == packet.srcTag {
			continue
		}
		c, exists := r.components[tag]
		if !exists {
			logger.Warnf("Warning: trying to route to non-existing component: %s", tag)
			continue
		}
		targets = append(targets, dest{component: c, tag: tag})
	}

	if len(targets) == 0 {
		return
	}

	// Send copies to all but the last target; reuse original for the last.
	last := len(targets) - 1
	for i := 0; i < last; i++ {
		newPacket := packet.Copy()
		if err := targets[i].component.HandlePacket(&newPacket); err != nil {
			logger.Warnf("Error routing to %s: %v", targets[i].tag, err)
		}
	}
	packet.AddRef(1)
	if err := targets[last].component.HandlePacket(packet); err != nil {
		logger.Warnf("Error routing to %s: %v", targets[last].tag, err)
	}
}

// GetBuffer retrieves a buffer from the pool
func (r *Router) GetBuffer() []byte {
	r.incrementBufferRef()
	return *(r.bufferPool.Get().(*[]byte))
}

func (r *Router) GetPacket(srcTag string) Packet {
	return Packet{
		buffer:  r.GetBuffer(),
		offset:  r.config.BufferOffset,
		length:  0,
		srcAddr: nil,
		srcTag:  srcTag,
		count:   1, // Initial reference count
		router:  r,
		proto:   "",
	}
}

func (r *Router) GetPacketWithBuffer(srcTag string, buf []byte, offset int) Packet {
	return Packet{
		buffer:  buf,
		offset:  offset,
		length:  0,
		srcAddr: nil,
		srcTag:  srcTag,
		count:   1, // Initial reference count
		router:  r,
		proto:   "",
	}
}

// PutBuffer returns a buffer to the pool
func (r *Router) PutBuffer(buf []byte) {
	buf = buf[:r.config.BufferSize+r.config.BufferOffset]
	r.bufferPool.Put(&buf)

	r.decrementBufferRef()
	r.logBufferRef()
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

// Route sends a packet to components specified by their tags in the caller goroutine.
func (r *Router) Route(packet *Packet, destTags []string) error {
	r.routePacket(packet, destTags)
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

	for tag, component := range r.components {
		postStarter, ok := component.(PostStarter)
		if !ok {
			continue
		}
		logger.Infof("Post-starting component: %s", tag)
		if err := postStarter.PostStart(); err != nil {
			return fmt.Errorf("failed to post-start component %s: %w", tag, err)
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

	logger.Infof("All router components stopped")
}

// GetComponentByTag returns a component by its tag or nil if not found
func (r *Router) GetComponentByTag(tag string) Component {
	c, exists := r.components[tag]
	if !exists {
		return nil
	}
	return c
}

// GetComponents returns a slice of all registered components
func (r *Router) GetComponents() []Component {
	components := make([]Component, 0, len(r.components))
	for _, c := range r.components {
		components = append(components, c)
	}
	return components
}
