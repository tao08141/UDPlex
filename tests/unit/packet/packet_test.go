package packet_test

import (
	"net"
	"testing"
)

// Mock router for testing
type MockRouter struct {
	buffers [][]byte
}

func (r *MockRouter) GetBuffer() []byte {
	if len(r.buffers) > 0 {
		buf := r.buffers[0]
		r.buffers = r.buffers[1:]
		return buf
	}
	return make([]byte, 1024)
}

func (r *MockRouter) PutBuffer(buf []byte) {
	r.buffers = append(r.buffers, buf)
}

// Copy of Packet struct for testing
type Packet struct {
	buffer  []byte
	offset  int
	length  int
	srcAddr net.Addr
	srcTag  string
	count   int32
	proto   string
	router  *MockRouter
	connID  string
}

func (p *Packet) GetData() []byte {
	return p.buffer[p.offset : p.offset+p.length]
}

func (p *Packet) SetBuffer(buffer []byte) {
	p.router.PutBuffer(p.buffer)
	p.buffer = buffer
	p.length = len(buffer)
}

func (p *Packet) AddRef(count int32) {
	p.count += count
}

func (p *Packet) Release(count int32) {
	p.count -= count
	if p.count <= 0 {
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
		count:   1,
		router:  p.router,
		connID:  p.connID,
	}
	copy(newPacket.buffer[p.offset:p.offset+p.length], p.buffer[p.offset:p.offset+p.length])
	return newPacket
}

func TestPacketGetData(t *testing.T) {
	router := &MockRouter{}
	buffer := make([]byte, 100)
	testData := []byte("hello world")
	copy(buffer[10:], testData)
	
	packet := Packet{
		buffer: buffer,
		offset: 10,
		length: len(testData),
		router: router,
	}
	
	data := packet.GetData()
	if string(data) != string(testData) {
		t.Errorf("GetData() = %q, want %q", string(data), string(testData))
	}
}

func TestPacketSetBuffer(t *testing.T) {
	router := &MockRouter{}
	oldBuffer := make([]byte, 100)
	newBuffer := []byte("new buffer data")
	
	packet := Packet{
		buffer: oldBuffer,
		offset: 0,
		length: 10,
		router: router,
	}
	
	packet.SetBuffer(newBuffer)
	
	if string(packet.buffer) != string(newBuffer) {
		t.Errorf("SetBuffer() buffer = %q, want %q", string(packet.buffer), string(newBuffer))
	}
	
	if packet.length != len(newBuffer) {
		t.Errorf("SetBuffer() length = %d, want %d", packet.length, len(newBuffer))
	}
	
	// Check that old buffer was returned to router
	if len(router.buffers) != 1 {
		t.Errorf("SetBuffer() should return old buffer to router, got %d buffers", len(router.buffers))
	}
}

func TestPacketRefCounting(t *testing.T) {
	router := &MockRouter{}
	buffer := make([]byte, 100)
	
	packet := Packet{
		buffer: buffer,
		count:  1,
		router: router,
	}
	
	// Add reference
	packet.AddRef(2)
	if packet.count != 3 {
		t.Errorf("AddRef(2) count = %d, want 3", packet.count)
	}
	
	// Release one reference
	packet.Release(1)
	if packet.count != 2 {
		t.Errorf("Release(1) count = %d, want 2", packet.count)
	}
	
	// Buffer should not be returned to router yet
	if len(router.buffers) != 0 {
		t.Errorf("Buffer should not be returned yet, got %d buffers", len(router.buffers))
	}
	
	// Release remaining references
	packet.Release(2)
	if packet.count != 0 {
		t.Errorf("Release(2) count = %d, want 0", packet.count)
	}
	
	// Buffer should now be returned to router
	if len(router.buffers) != 1 {
		t.Errorf("Buffer should be returned to router, got %d buffers", len(router.buffers))
	}
}

func TestPacketCopy(t *testing.T) {
	router := &MockRouter{}
	buffer := make([]byte, 100)
	testData := []byte("test data")
	copy(buffer[5:], testData)
	
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:8080")
	
	original := Packet{
		buffer:  buffer,
		offset:  5,
		length:  len(testData),
		srcAddr: addr,
		srcTag:  "test",
		count:   1,
		proto:   "udp",
		router:  router,
		connID:  "conn1",
	}
	
	copied := original.Copy()
	
	// Check that copy has correct values
	if copied.offset != original.offset {
		t.Errorf("Copy() offset = %d, want %d", copied.offset, original.offset)
	}
	
	if copied.length != original.length {
		t.Errorf("Copy() length = %d, want %d", copied.length, original.length)
	}
	
	if copied.srcTag != original.srcTag {
		t.Errorf("Copy() srcTag = %q, want %q", copied.srcTag, original.srcTag)
	}
	
	if copied.count != 1 {
		t.Errorf("Copy() count = %d, want 1", copied.count)
	}
	
	// Check that data was copied correctly
	if string(copied.GetData()) != string(original.GetData()) {
		t.Errorf("Copy() data = %q, want %q", string(copied.GetData()), string(original.GetData()))
	}
	
	// Modify original data and ensure copy is not affected
	original.buffer[5] = 'X'
	if string(copied.GetData()) == string(original.GetData()) {
		t.Error("Copy() should create independent buffer")
	}
}