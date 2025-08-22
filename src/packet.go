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

// Encapsulation helpers below: prefer using these instead of directly accessing fields.

// Buffer returns the underlying buffer slice.
func (p *Packet) Buffer() []byte { return p.buffer }

// BufAtOffset returns a slice of the buffer starting at the current offset.
func (p *Packet) BufAtOffset() []byte { return p.buffer[p.offset:] }

// Offset returns the current data offset within the buffer.
func (p *Packet) Offset() int { return p.offset }

// SetOffset sets the data offset within the buffer.
func (p *Packet) SetOffset(off int) { p.offset = off }

// AddOffset adds delta to the current offset.
func (p *Packet) AddOffset(delta int) { p.offset += delta }

// Length returns the current data length.
func (p *Packet) Length() int { return p.length }

// SetLength sets the data length.
func (p *Packet) SetLength(n int) { p.length = n }

// AddLength adds delta to the current length.
func (p *Packet) AddLength(delta int) { p.length += delta }

// SrcAddr returns the source address.
func (p *Packet) SrcAddr() net.Addr { return p.srcAddr }

// SetSrcAddr sets the source address.
func (p *Packet) SetSrcAddr(addr net.Addr) { p.srcAddr = addr }

// SrcTag returns the source tag.
func (p *Packet) SrcTag() string { return p.srcTag }

// SetSrcTag sets the source tag.
func (p *Packet) SetSrcTag(tag string) { p.srcTag = tag }

// Proto returns the detected/assigned protocol string.
func (p *Packet) Proto() string { return p.proto }

// SetProto sets the protocol string.
func (p *Packet) SetProto(proto string) { p.proto = proto }

// Router returns the packet's router.
func (p *Packet) Router() *Router { return p.router }

// ConnID returns the unique connection identifier.
func (p *Packet) ConnID() ConnID { return p.connID }

// SetConnID sets the unique connection identifier.
func (p *Packet) SetConnID(id ConnID) { p.connID = id }
