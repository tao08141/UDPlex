package main

import (
	"net"
	"sync/atomic"
)

type Packet struct {
	buffer  []byte
	length  int
	srcAddr net.Addr
	srcTag  string
	count   int32
	proto   string
	router  *Router
}

func (p *Packet) AddRef(count int32) {
	atomic.AddInt32(&p.count, count)
}

func (p *Packet) Release(count int32) {
	if atomic.AddInt32(&p.count, -count) <= 0 {
		p.router.PutBuffer(p.buffer)
	}
}
