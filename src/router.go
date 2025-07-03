package main

import (
	"fmt"
	"sync"
	"time"
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
		routeTasks: make(chan routeTask, config.QueueSize),
		sendTasks:  make(chan sendTask, config.QueueSize),
	}

	return r
}

type sendTask struct {
	component Component
	packet    *Packet
	metadata  any
}

type routeTask struct {
	packet   *Packet
	destTags []string
}

// Router manages all components and routes packets between them
type Router struct {
	components     map[string]Component
	bufferPool     sync.Pool
	config         Config
	routeTasks     chan routeTask
	sendTasks      chan sendTask
	wg             sync.WaitGroup
	connPool       map[ConnID]map[string]any // connPoll[connId][tag] = any
	concurrencyChs map[string]chan struct{}
}

func (r *Router) GetConnData(connID ConnID, tag string) any {
	if _, exists := r.connPool[connID]; !exists {
		r.connPool[connID] = make(map[string]any)
	}
	return r.connPool[connID][tag]
}

func (r *Router) SetConnData(connID ConnID, tag string, data any) {
	if _, exists := r.connPool[connID]; !exists {
		r.connPool[connID] = make(map[string]any)
	}
	r.connPool[connID][tag] = data
}

func (r *Router) RemoveConnData(connID ConnID, tag string) {
	if _, exists := r.connPool[connID]; exists {
		delete(r.connPool[connID], tag)
		if len(r.connPool[connID]) == 0 {
			delete(r.connPool, connID) // Remove empty connection data
		}
	}
}

func (r *Router) processSendTask(task sendTask) {
	tag := task.component.GetTag()
	sendStartTime := time.Now()
	if err := task.component.SendPacket(task.packet, task.metadata); err != nil {
		logger.Warnf("Error sending packet via %s: %v", tag, err)
	}

	sendDuration := time.Since(sendStartTime)
	task.component.SetSendQueueDelay(sendDuration)
	task.packet.Release(1) // Release packet reference
}

// startWorkers initializes the worker goroutines for packet routing
func (r *Router) startWorkers() {
	r.concurrencyChs = make(map[string]chan struct{})
	capacity := max(int(float64(r.config.WorkerCount)/float64(len(r.components))*1.3), 1)
	for tag := range r.components {
		r.concurrencyChs[tag] = make(chan struct{}, capacity)
	}

	logger.Infof("Starting %d router workers with concurrency capacity %d", r.config.WorkerCount, capacity)

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
					r.processRouteTaskConcurrent(task)

				case task, ok := <-r.sendTasks:
					if !ok {
						logger.Warnf("Router worker %d: send tasks channel closed", workerID)
						return
					}
					tag := task.component.GetTag()
					if ch, found := r.concurrencyChs[tag]; found {
						select {
						case ch <- struct{}{}:
							r.processSendTask(task) // Process the send task
							<-ch
						default:
							// If the token is not obtained, the average delay of the last 10 sending queues is determined. If it is higher than sendTimeout, the task is directly discarded.
							avgDelay := task.component.GetAverageSendQueueDelay()
							if avgDelay > task.component.GetSendTimeout()/2 {
								logger.Infof("Send queue for %s is full, dropping packet due to high average delay: %v", tag, avgDelay)
								task.packet.Release(1) // Release packet reference
							} else {
								r.processSendTask(task)
							}
						}
					} else {
						logger.Warnf("Error sending packet via %s: concurrency channel not found", tag)
						task.packet.Release(1)
					}
				}
			}
		}(i)
	}
}

// processRouteTask handles the actual routing of packets
func (r *Router) processRouteTaskConcurrent(task routeTask) {
	packet := task.packet
	defer packet.Release(1)

	for i, tag := range task.destTags {
		if tag == packet.srcTag {
			continue
		}
		c, exists := r.GetComponent(tag)
		if !exists {
			logger.Warnf("Warning: trying to route to non-existing component: %s", tag)
			continue
		}

		if i < len(task.destTags)-1 {
			// Create a copy for all but the last destination
			newPacket := packet.Copy()
			if err := c.HandlePacket(&newPacket); err != nil {
				logger.Warnf("Error routing to %s: %v", tag, err)
			}
		} else {
			// Use original packet for the last destination or if only one destination
			packet.AddRef(1)
			if err := c.HandlePacket(packet); err != nil {
				logger.Warnf("Error routing to %s: %v", tag, err)
			}
		}
	}
}

// SendPacket adds a packet to the send queue
func (r *Router) SendPacket(component Component, packet *Packet, metadata any) error {
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
func (r *Router) Route(packet *Packet, destTags []string) error {
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

	r.startWorkers()

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
	close(r.sendTasks)

	r.wg.Wait()
	logger.Infof("All router workers stopped")
}
