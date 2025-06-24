package main

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
func (bc *BaseComponent) GetConnData(connID string) any {
	return bc.router.GetConnData(connID, bc.tag)
}

// SetConnData stores connection-specific data for this component
func (bc *BaseComponent) SetConnData(connID string, data any) {
	bc.router.SetConnData(connID, bc.tag, data)
}

// RemoveConnData removes connection-specific data for this component
func (bc *BaseComponent) RemoveConnData(connID string) {
	bc.router.RemoveConnData(connID, bc.tag)
}

func (l *BaseComponent) generateConnID() string {
	return ""
}
