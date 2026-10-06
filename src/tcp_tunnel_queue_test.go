package main

import (
	"net"
	"testing"
	"time"
)

func testTcpQueue(t *testing.T, cfg *TcpQueueConfig) (*tcpTunnelQueue, *Router) {
	t.Helper()
	return newTcpTunnelQueue(newTcpQueueSettings(cfg, 16, 2048), nil), NewRouter(Config{BufferSize: 2048})
}

func TestTcpQueuePriorityAndControlFirst(t *testing.T) {
	q, r := testTcpQueue(t, nil)
	now := time.Unix(0, 0)
	for i := 0; i < 100; i++ {
		q.push(testShaperPacket(r, 1400), false, now)
	}
	q.push(testShaperPacket(r, 100), false, now)
	q.push(testShaperPacket(r, 8), true, now)
	out := q.pop(now, nil, 2)
	if len(out) != 2 || out[0].Length() != 8 || out[1].Length() != 100 {
		t.Fatalf("got lengths %d, %d; want control then priority", out[0].Length(), out[1].Length())
	}
	for _, p := range out {
		p.Release(1)
	}
	q.close()
	if r.bufferRefCount != 0 {
		t.Fatalf("leaked %d buffers", r.bufferRefCount)
	}
}

func TestTcpQueuePriorityShare(t *testing.T) {
	q, r := testTcpQueue(t, &TcpQueueConfig{PriorityShare: 25, QueueLimit: 64 << 20})
	q.s.codel = false
	now := time.Unix(0, 0)
	bytes := map[bool]int{}
	// Both classes always backlogged.
	for round := 0; round < 200; round++ {
		for q.priority.len() < 50 {
			q.push(testShaperPacket(r, 200), false, now)
		}
		for q.bulk.len() < 50 {
			q.push(testShaperPacket(r, 1400), false, now)
		}
		for _, p := range q.pop(now, nil, 16) {
			bytes[p.Length() <= 1000] += p.Length()
			p.Release(1)
		}
	}
	share := float64(bytes[true]) / float64(bytes[true]+bytes[false])
	if share < 0.2 || share > 0.3 {
		t.Fatalf("priority got %.0f%% of the bytes, want about 25%%", share*100)
	}
	q.close()
}

func TestTcpQueueCodelDropsStandingQueue(t *testing.T) {
	q, r := testTcpQueue(t, &TcpQueueConfig{QueueLimit: 64 << 20})
	start := time.Unix(0, 0)
	// The socket drains 1 packet per ms while 2 arrive: the queue keeps growing.
	// CoDel signals the senders; the byte limit bounds an unresponsive flood.
	sent := 0
	for ms := 0; ms < 2000; ms++ {
		now := start.Add(time.Duration(ms) * time.Millisecond)
		q.push(testShaperPacket(r, 1400), false, now)
		q.push(testShaperPacket(r, 1400), false, now)
		for _, p := range q.pop(now, nil, 1) {
			sent++
			p.Release(1)
		}
	}
	if q.stats.codelDrops.Load() == 0 {
		t.Fatal("no CoDel drops on a standing queue")
	}
	q.close()
	if r.bufferRefCount != 0 {
		t.Fatalf("leaked %d buffers", r.bufferRefCount)
	}
}

func TestTcpQueueLimitDropsOldestBulk(t *testing.T) {
	q, r := testTcpQueue(t, &TcpQueueConfig{QueueLimit: 10 * 1400})
	now := time.Unix(0, 0)
	for i := 0; i < 20; i++ {
		p := testShaperPacket(r, 1400)
		p.GetData()[0] = byte(i)
		q.push(p, false, now)
	}
	if q.stats.overflowDrops.Load() != 10 {
		t.Fatalf("overflow drops %d, want 10", q.stats.overflowDrops.Load())
	}
	out := q.pop(now, nil, 1)
	if out[0].GetData()[0] != 10 {
		t.Fatalf("head is packet %d, want the oldest kept (10)", out[0].GetData()[0])
	}
	out[0].Release(1)
	q.close()
	if q.push(testShaperPacket(r, 100), true, now) {
		t.Fatal("push after close accepted")
	}
	if r.bufferRefCount != 0 {
		t.Fatalf("leaked %d buffers", r.bufferRefCount)
	}
}

func TestTcpQueueDisabledIsFifo(t *testing.T) {
	q, r := testTcpQueue(t, &TcpQueueConfig{Enabled: new(bool)})
	if q.s.notsentLowat != 0 || q.s.codel || q.s.prioritySize != 0 {
		t.Fatalf("disabled queue settings %+v", *q.s)
	}
	now := time.Unix(0, 0)
	q.push(testShaperPacket(r, 1400), false, now)
	q.push(testShaperPacket(r, 100), false, now)
	out := q.pop(now.Add(time.Hour), nil, 2)
	if len(out) != 2 || out[0].Length() != 1400 {
		t.Fatal("disabled queue is not FIFO")
	}
	for _, p := range out {
		p.Release(1)
	}
}

func testPoolConn(r *Router, s *tcpQueueSettings) *TcpTunnelConn {
	c1, _ := net.Pipe()
	c := &TcpTunnelConn{conn: c1, authState: &AuthState{}, closed: make(chan struct{}), queue: newTcpTunnelQueue(s, nil)}
	c.authState.SetAuthenticated(1)
	return c
}

func TestTcpPoolPickConn(t *testing.T) {
	r := NewRouter(Config{BufferSize: 2048})
	s := newTcpQueueSettings(nil, 16, 2048)
	pool := NewTcpTunnelConnPool("x", PoolID{}, 3)
	a, b, c := testPoolConn(r, s), testPoolConn(r, s), testPoolConn(r, s)
	for _, x := range []*TcpTunnelConn{a, b, c} {
		pool.AddConnection(x)
	}
	now := time.Unix(0, 0)
	// Sticks to one connection while backlogs are small.
	first := pool.PickConn(1400, 1000, false)
	for i := 0; i < 10; i++ {
		if got := pool.PickConn(1400, 1000, false); got != first {
			t.Fatal("switched connection without backlog")
		}
		first.queue.push(testShaperPacket(r, 1400), false, now)
	}
	// Moves to the least loaded connection once the current one backs up.
	for first.queue.backlog() <= tcpTunnelSwitchBacklog {
		first.queue.push(testShaperPacket(r, 1400), false, now)
	}
	next := pool.PickConn(1400, 1000, false)
	if next == first || next.queue.backlog() != 0 {
		t.Fatal("did not move off the backed up connection")
	}
	// Priority connection: small packets on the first, bulk on the others.
	conns := *pool.conns.Load()
	if got := pool.PickConn(100, 1000, true); got != conns[0] {
		t.Fatal("small packet not on the priority connection")
	}
	for i := 0; i < 20; i++ {
		if got := pool.PickConn(1400, 1000, true); got == conns[0] {
			t.Fatal("bulk packet on the priority connection")
		}
	}
	// Unusable connections are skipped.
	b.authState.SetAuthenticated(0)
	close(c.closed)
	for i := 0; i < 5; i++ {
		if got := pool.PickConn(1400, 1000, false); got != a {
			t.Fatal("picked an unusable connection")
		}
	}
	for _, x := range []*TcpTunnelConn{a, b, c} {
		x.queue.close()
	}
	if r.bufferRefCount != 0 {
		t.Fatalf("leaked %d buffers", r.bufferRefCount)
	}
}

func TestTcpTunedLowat(t *testing.T) {
	s := newTcpQueueSettings(nil, 16, 2048)
	for _, c := range []struct {
		rate    uint64
		current int
		want    int
	}{
		{0, 128 << 10, 0},                      // no measurement yet
		{38e6 / 8, 128 << 10, 23750},           // 5ms at 38 Mbit/s
		{1e6 / 8, 128 << 10, tcpAutoLowatMin},  // floor
		{10e9 / 8, 128 << 10, tcpAutoLowatMax}, // ceiling
		{200e6 / 8, 120 << 10, 0},              // within 25%: keep
	} {
		if got := s.tunedLowat(c.rate, c.current); got != c.want {
			t.Errorf("tunedLowat(%d, %d) = %d, want %d", c.rate, c.current, got, c.want)
		}
	}
	s.pacingRate = 20e6 / 8
	if got := s.tunedLowat(200e6/8, 128<<10); got != 16<<10 {
		t.Errorf("pacing caps the rate: got %d", got)
	}
	fixed := newTcpQueueSettings(&TcpQueueConfig{NotsentLowat: new(int)}, 16, 2048)
	if fixed.autoLowat || fixed.tunedLowat(200e6/8, 0) != 0 {
		t.Error("explicit notsent_lowat is retuned")
	}
}
