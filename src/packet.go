package main

import (
	"net"
	"sync/atomic"
)

type Packet struct {
	buffer  []byte
	offset  int
	length  int
	srcAddr net.Addr
	srcTag  string
	count   int32
	proto   string
	router  *Router
	connID  ConnID // Unique connection identifier
}

func (p *Packet) GetData() []byte {
	return p.buffer[p.offset : p.offset+p.length]
}

// SetBuffer Replace buffer addr
func (p *Packet) SetBuffer(buffer []byte) {
	p.router.PutBuffer(p.buffer)
	p.buffer = buffer
	p.length = len(buffer)
}

func (p *Packet) AddRef(count int32) {
	atomic.AddInt32(&p.count, count)
}

func (p *Packet) Release(count int32) {
	if atomic.AddInt32(&p.count, -count) <= 0 {
		p.router.PutBuffer(p.buffer)
	}
}

func (p *Packet) Copy() Packet {
	newPacket := Packet{
		buffer:  p.router.GetBuffer(),
		offset:  p.offset,
		length:  p.length,
		srcAddr: p.srcAddr,
		srcTag:  p.srcTag,
		count:   1, // New packet starts with a reference count of 1
		router:  p.router,
		connID:  p.connID,
	}
	copy(newPacket.buffer, p.buffer)
	return newPacket
}
