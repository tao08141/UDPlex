package main

import (
	"crypto/rand"
	"encoding/binary"
	"sync/atomic"
	"time"
)

type ConnID [8]byte
type PoolID [8]byte
type ForwardID [8]byte

func (c ConnID) ToUint64() uint64 {
	return binary.BigEndian.Uint64(c[:])
}

// ConnIDFromUint64 creates a ConnID from a uint64 value using big-endian encoding
func ConnIDFromUint64(val uint64) ConnID {
	var id ConnID
	binary.BigEndian.PutUint64(id[:], val)
	return id
}

func ForwardIDFromBytes(data []byte) ForwardID {
	var id ForwardID
	if len(data) < len(id) {
		copy(id[:], data)
	} else {
		copy(id[:], data[:len(id)])
	}
	return id
}

func PoolIDFromBytes(data []byte) PoolID {
	var id PoolID
	if len(data) < len(id) {
		copy(id[:], data)
	} else {
		copy(id[:], data[:len(id)])
	}
	return id
}

// AvailabilityChecker is the interface for components that support availability checking
type AvailabilityChecker interface {
	IsAvailable() bool
}

// Component is the interface that all network components must implement
type Component interface {
	Start() error
	Stop() error
	GetTag() string
	// HandlePacket processes packets coming from other components
	// srcTag is the tag of the component that sent the packet
	HandlePacket(packet *Packet) error
	SendPacket(packet *Packet, metadata any) error
	GetRouter() *Router
	GetStopChannel() chan struct{}
	SetSendQueueDelay(delay time.Duration)
	GetAverageSendQueueDelay() time.Duration
	GetSendTimeout() time.Duration
}

// BaseComponent provides common functionality for components
type BaseComponent struct {
	tag                 string
	router              *Router
	stopCh              chan struct{}
	sendTimeout         time.Duration
	sendQueueDelays     [10]time.Duration
	sendQueueDelayIndex uint32
}

// NewBaseComponent creates a base component with common functionality
func NewBaseComponent(tag string, router *Router, sendTimeout time.Duration) BaseComponent {
	return BaseComponent{
		tag:                 tag,
		router:              router,
		stopCh:              make(chan struct{}),
		sendTimeout:         sendTimeout,
		sendQueueDelays:     [10]time.Duration{},
		sendQueueDelayIndex: 0,
	}
}

func (bc *BaseComponent) SetSendQueueDelay(delay time.Duration) {
	index := atomic.AddUint32(&bc.sendQueueDelayIndex, 1) % uint32(len(bc.sendQueueDelays))
	bc.sendQueueDelays[index] = delay
}

func (bc *BaseComponent) GetAverageSendQueueDelay() time.Duration {
	var total time.Duration
	count := 0

	for _, delay := range bc.sendQueueDelays {
		if delay > 0 {
			total += delay
			count++
		}
	}

	if count == 0 {
		return 0
	}

	return total / time.Duration(count)
}

func (bc *BaseComponent) GetSendTimeout() time.Duration {
	return bc.sendTimeout
}

func (bc *BaseComponent) GetStopChannel() chan struct{} {
	return bc.stopCh
}

// GetTag returns the component's tag
func (bc *BaseComponent) GetTag() string {
	return bc.tag
}

// GetConnData retrieves connection-specific data for this component
func (bc *BaseComponent) GetConnData(connID ConnID) any {
	return bc.router.GetConnData(connID, bc.tag)
}

// SetConnData stores connection-specific data for this component
func (bc *BaseComponent) SetConnData(connID ConnID, data any) {
	bc.router.SetConnData(connID, bc.tag, data)
}

// RemoveConnData removes connection-specific data for this component
func (bc *BaseComponent) RemoveConnData(connID ConnID) {
	bc.router.RemoveConnData(connID, bc.tag)
}

func (l *BaseComponent) generateConnID() ConnID {
	connID := ConnID{}
	if _, err := rand.Read(connID[:]); err != nil {
		logger.Errorf("Failed to generate connection ID: %v", err)
		return ConnID{}
	}

	return connID
}

func (bc *BaseComponent) GetRouter() *Router {
	return bc.router
}
