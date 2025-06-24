package main

import (
	"crypto/rand"
	"encoding/binary"
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

// Component is the interface that all network components must implement
type Component interface {
	Start() error
	Stop() error
	GetTag() string
	// HandlePacket processes packets coming from other components
	// srcTag is the tag of the component that sent the packet
	HandlePacket(packet *Packet) error
	SendPacket(packet *Packet, metadata any) error
}

// BaseComponent provides common functionality for components
type BaseComponent struct {
	tag    string
	router *Router
}

// NewBaseComponent creates a base component with common functionality
func NewBaseComponent(tag string, router *Router) BaseComponent {
	return BaseComponent{
		tag:    tag,
		router: router,
	}
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
