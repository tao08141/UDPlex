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
	connID  string // Unique connection identifier
}

func NewPacket(buffer []byte, length int, srcAddr net.Addr, srcTag string, router *Router, offset int) Packet {
	return Packet{
		buffer:  buffer,
		length:  length,
		srcAddr: srcAddr,
		srcTag:  srcTag,
		count:   1, // Initial reference count
		router:  router,
		offset:  offset,
		connID:  "", // Will be set by the component that establishes the connection
	}
}

func (p *Packet) GetData() []byte {
	return p.buffer[p.offset : p.offset+p.length]
}

// 替换buffer的地址
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
