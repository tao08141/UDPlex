package main

import (
	"net"
	"testing"
	"time"
)

var testShaperAddr = &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9000}

func testShaper(t *testing.T, cfg ShaperConfig) (*pathShaper, *shaperStats, *Router) {
	t.Helper()
	cfg.Enabled = true
	s, err := newShaperSettings(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	stats := &shaperStats{}
	return newPathShaper(s, stats, time.Unix(0, 0)), stats, NewRouter(Config{BufferSize: 2048})
}

func testShaperPacket(r *Router, size int) *Packet {
	p := new(Packet)
	*p = r.GetPacket("t")
	p.SetLength(size)
	return p
}

// run advances time in steps, dequeuing and releasing what is sent; it
// returns bytes sent per size class (keyed by payload length).
func runShaper(p *pathShaper, start time.Time, dur, step time.Duration, each func(now time.Time)) map[int]int {
	sent := map[int]int{}
	var out []shaperItem
	for t := time.Duration(0); t <= dur; t += step {
		now := start.Add(t)
		if each != nil {
			each(now)
		}
		out = p.dequeue(now, out[:0], 64)
		for _, it := range out {
			sent[it.pkt.Length()] += it.wire
			it.pkt.Release(1)
		}
	}
	return sent
}

func TestShaperRate(t *testing.T) {
	p, stats, r := testShaper(t, ShaperConfig{Rate: 40, QueueLimit: 8 << 20})
	start := time.Unix(0, 0)
	for i := 0; i < 20000; i++ {
		p.enqueue(testShaperPacket(r, 1400), testShaperAddr, false, start)
	}
	p.codel.target = time.Hour // isolate pacing from AQM
	sent := runShaper(p, start, time.Second, 100*time.Microsecond, nil)
	want := 40e6 / 8
	if got := float64(sent[1400]); got < want*0.98 || got > want*1.02+p.s.burst {
		t.Fatalf("sent %.0f bytes in 1s, want about %.0f", got, want)
	}
	if stats.priorityPkts.Load() != 0 {
		t.Fatal("bulk packets counted as priority")
	}
	p.reset()
	if r.bufferRefCount != 0 {
		t.Fatalf("leaked %d buffers", r.bufferRefCount)
	}
}

func TestShaperPriorityJumpsQueue(t *testing.T) {
	p, _, r := testShaper(t, ShaperConfig{Rate: 10, QueueLimit: 8 << 20})
	start := time.Unix(0, 0)
	for i := 0; i < 500; i++ {
		p.enqueue(testShaperPacket(r, 1400), testShaperAddr, false, start)
	}
	// Drain the initial burst so the path is rate limited.
	runShaper(p, start, 0, time.Millisecond, nil)
	now := start.Add(time.Millisecond)
	p.enqueue(testShaperPacket(r, 100), testShaperAddr, false, now)
	var waited time.Duration
	for d := time.Duration(0); d < 50*time.Millisecond; d += 100 * time.Microsecond {
		out := p.dequeue(now.Add(d), nil, 64)
		found := false
		for _, it := range out {
			found = found || it.pkt.Length() == 100
			it.pkt.Release(1)
		}
		if found {
			waited = d
			break
		}
		if d+100*time.Microsecond >= 50*time.Millisecond {
			t.Fatal("small packet stuck behind bulk backlog")
		}
	}
	// One packet time at 10 Mbit/s is ~1.2ms; the bulk backlog is ~550ms.
	if waited > 3*time.Millisecond {
		t.Fatalf("small packet waited %v", waited)
	}
	p.reset()
}

func TestShaperPriorityShare(t *testing.T) {
	p, _, r := testShaper(t, ShaperConfig{Rate: 10, PriorityShare: 25, QueueLimit: 8 << 20})
	p.codel.target = time.Hour
	start := time.Unix(0, 0)
	// Small packets arrive at the full link rate, bulk keeps a backlog.
	sent := runShaper(p, start, time.Second, 100*time.Microsecond, func(now time.Time) {
		p.enqueue(testShaperPacket(r, 100), testShaperAddr, false, now)
		p.enqueue(testShaperPacket(r, 100), testShaperAddr, false, now)
		if p.bulk.len() < 100 {
			for i := 0; i < 50; i++ {
				p.enqueue(testShaperPacket(r, 1400), testShaperAddr, false, now)
			}
		}
	})
	total := float64(sent[100] + sent[1400])
	if share := float64(sent[1400]) / total; share < 0.7 {
		t.Fatalf("bulk got %.0f%% of the link under a small-packet flood", share*100)
	}
	p.reset()
}

func TestShaperQueueLimit(t *testing.T) {
	p, stats, r := testShaper(t, ShaperConfig{Rate: 10, QueueLimit: 100 * 1428})
	start := time.Unix(0, 0)
	for i := 0; i < 300; i++ {
		pk := testShaperPacket(r, 1400)
		pk.SetConnID(ConnIDFromUint64(uint64(i)))
		p.enqueue(pk, testShaperAddr, false, start)
	}
	if p.bulk.bytes > p.s.queueLimit || p.bulk.len() != 100 {
		t.Fatalf("queue holds %d bytes in %d packets, limit %d", p.bulk.bytes, p.bulk.len(), p.s.queueLimit)
	}
	if stats.overflowDrops.Load() != 200 {
		t.Fatalf("overflow drops %d, want 200", stats.overflowDrops.Load())
	}
	// The oldest packets were dropped, the newest kept.
	if got := p.bulk.peek().pkt.ConnID().ToUint64(); got != 200 {
		t.Fatalf("head is packet %d, want 200", got)
	}
	p.reset()
	if r.bufferRefCount != 0 || stats.queuedBytes.Load() != 0 {
		t.Fatalf("leaked %d buffers, %d queued bytes", r.bufferRefCount, stats.queuedBytes.Load())
	}
}

func TestShaperCodelDropsStandingQueue(t *testing.T) {
	p, stats, r := testShaper(t, ShaperConfig{Rate: 10, QueueLimit: 8 << 20})
	start := time.Unix(0, 0)
	// Offer twice the rate for two seconds.
	runShaper(p, start, 2*time.Second, time.Millisecond, func(now time.Time) {
		p.enqueue(testShaperPacket(r, 1400), testShaperAddr, false, now)
		p.enqueue(testShaperPacket(r, 1400), testShaperAddr, false, now)
	})
	if stats.codelDrops.Load() == 0 {
		t.Fatal("no CoDel drops under sustained overload")
	}
	p.reset()
}

func TestShaperControlBypassesRate(t *testing.T) {
	p, _, r := testShaper(t, ShaperConfig{Rate: 1, QueueLimit: 8 << 20})
	start := time.Unix(0, 0)
	for i := 0; i < 100; i++ {
		p.enqueue(testShaperPacket(r, 1400), testShaperAddr, false, start)
	}
	runShaper(p, start, 0, time.Millisecond, nil) // spend the bucket
	if w := p.wait(start); w <= 0 {
		t.Fatalf("wait %v with an empty bucket", w)
	}
	p.enqueue(testShaperPacket(r, 64), testShaperAddr, true, start)
	if w := p.wait(start); w != 0 {
		t.Fatalf("wait %v with a control packet queued", w)
	}
	out := p.dequeue(start, nil, 64)
	if len(out) != 1 || out[0].pkt.Length() != 64 {
		t.Fatalf("control packet not sent immediately: %d items", len(out))
	}
	out[0].pkt.Release(1)
	p.reset()
	if w := p.wait(start); w != -1 {
		t.Fatalf("wait %v on an empty shaper", w)
	}
}

func TestShaperWireOverhead(t *testing.T) {
	p, _, r := testShaper(t, ShaperConfig{Rate: 10, Overhead: 8})
	v6 := &net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 1}
	p.enqueue(testShaperPacket(r, 100), testShaperAddr, false, time.Unix(0, 0))
	p.enqueue(testShaperPacket(r, 100), v6, false, time.Unix(0, 0))
	if got := p.priority.bytes; got != (100+28+8)+(100+48+8) {
		t.Fatalf("wire bytes %d", got)
	}
	p.reset()
}

func TestNewShaperSettings(t *testing.T) {
	if s, err := newShaperSettings(nil); s != nil || err != nil {
		t.Fatal("nil config should disable the shaper")
	}
	if _, err := newShaperSettings(&ShaperConfig{Enabled: true}); err == nil {
		t.Fatal("missing rate should be an error")
	}
	s, err := newShaperSettings(&ShaperConfig{Enabled: true, Rate: 100})
	if err != nil {
		t.Fatal(err)
	}
	if s.prioritySize != 1000 || s.target != 5*time.Millisecond || s.queueLimit != 1250000 {
		t.Fatalf("unexpected defaults %+v", *s)
	}
	zero := 0
	s, _ = newShaperSettings(&ShaperConfig{Enabled: true, Rate: 1, PrioritySize: &zero})
	if s.prioritySize != 0 || s.queueLimit != shaperMinQueueLimit {
		t.Fatalf("unexpected settings %+v", *s)
	}
}
