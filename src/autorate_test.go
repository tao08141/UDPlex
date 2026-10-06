package main

import (
	"math"
	"testing"
	"time"
)

func testAutorate(t *testing.T, cfg ShaperConfig) *autorate {
	t.Helper()
	cfg.Enabled, cfg.Autorate = true, true
	s, err := newShaperSettings(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	return newAutorate(s.autorate, time.Unix(0, 0))
}

func TestProbeRoundTrip(t *testing.T) {
	sent := time.Unix(1000, 0)
	probe := make([]byte, 64)
	n := createProbe(probe, sent)
	// The peer's clock is 3s ahead and the probe takes 10ms to arrive.
	ack := make([]byte, 64)
	m := createProbeAck(ack, probe[:n], sent.Add(3*time.Second+10*time.Millisecond))
	if m == 0 {
		t.Fatal("probe rejected")
	}
	if createProbeAck(ack, probe[:n-1], sent) != 0 {
		t.Fatal("short probe accepted")
	}
	owd, rtt, ok := parseProbeAck(ack[:m], sent.Add(25*time.Millisecond))
	if !ok || owd != int64(3*time.Second+10*time.Millisecond) || rtt != 25*time.Millisecond {
		t.Fatalf("owd %v rtt %v ok %v", time.Duration(owd), rtt, ok)
	}
	if _, _, ok := parseProbeAck(ack[:m-1], sent); ok {
		t.Fatal("short ack accepted")
	}
}

// feedAutorate sends one probe sample per interval for dur, with the one-way delay
// from owd and the path sending at achieved bytes per second.
func feedAutorate(a *autorate, start time.Time, dur time.Duration, achieved float64, owd func(time.Time) time.Duration) (time.Time, float64) {
	now := start
	sent := a.lastSent
	rate := a.rate
	for end := start.Add(dur); now.Before(end); {
		now = now.Add(a.s.interval)
		a.addSample(int64(5*time.Hour+owd(now)), 20*time.Millisecond, now) // 5h clock offset
		sent += uint64(math.Min(achieved, rate) * a.s.interval.Seconds())
		rate = a.tick(now, sent)
	}
	return now, rate
}

func constDelay(d time.Duration) func(time.Time) time.Duration {
	return func(time.Time) time.Duration { return d }
}

func TestAutorateBacksOffOnBloat(t *testing.T) {
	a := testAutorate(t, ShaperConfig{Rate: 100})
	base := a.s.base
	now, rate := feedAutorate(a, time.Unix(0, 0), 2*time.Second, 1e9, constDelay(10*time.Millisecond))
	if rate != base {
		t.Fatalf("clean busy link at max: rate %.0f, want %.0f", rate, base)
	}
	// The bottleneck queue grows by 40ms.
	_, rate = feedAutorate(a, now, 200*time.Millisecond, 1e9, constDelay(50*time.Millisecond))
	if rate > base*0.91 || rate < base*0.5 {
		t.Fatalf("rate after one bloat episode %.0f, want about %.0f", rate, base*0.9)
	}
	// Persistent bloat keeps cutting, refractory permitting, down to min.
	_, rate = feedAutorate(a, now, 10*time.Second, 1e9, constDelay(50*time.Millisecond))
	if rate != a.s.min {
		t.Fatalf("persistent bloat: rate %.0f, want min %.0f", rate, a.s.min)
	}
}

func TestAutorateFollowsCapacityDrop(t *testing.T) {
	// The link drops from 100 to 50 Mbit/s: a crude bottleneck model where
	// the queue grows by the excess and drains at capacity.
	a := testAutorate(t, ShaperConfig{Rate: 100})
	capacity := 50e6 / 8
	var queue float64 // bytes
	now := time.Unix(0, 0)
	sent := uint64(0)
	for i := 0; i < 400; i++ { // 20s
		now = now.Add(a.s.interval)
		queue = math.Max(0, queue+(a.rate-capacity)*a.s.interval.Seconds())
		a.addSample(int64(10*time.Millisecond+time.Duration(queue/capacity*float64(time.Second))), 0, now)
		sent += uint64(a.rate * a.s.interval.Seconds())
		a.tick(now, sent)
	}
	if a.rate > capacity*1.05 || a.rate < capacity*0.6 {
		t.Fatalf("rate %.1f Mbit/s, want just below 50", a.rate*8/1e6)
	}
	if qd := queue / capacity; qd > 0.03 {
		t.Fatalf("bottleneck queue %.0f ms at the end", qd*1000)
	}
}

func TestAutorateRecovers(t *testing.T) {
	a := testAutorate(t, ShaperConfig{Rate: 100, MaxRate: 200})
	now, _ := feedAutorate(a, time.Unix(0, 0), time.Second, 1e9, constDelay(50*time.Millisecond))
	now, rate := feedAutorate(a, now, time.Second, 1e9, constDelay(time.Second))
	if rate >= a.s.base {
		t.Fatalf("no back off: %.0f", rate)
	}
	// Busy and clean again: climbs back, above base up to max.
	_, rate = feedAutorate(a, now, 30*time.Second, 1e9, constDelay(0))
	if rate != a.s.max {
		t.Fatalf("busy clean link: rate %.0f, want max %.0f", rate, a.s.max)
	}
	// Idle: decays back to base.
	_, rate = feedAutorate(a, now.Add(30*time.Second), 30*time.Second, 0, constDelay(0))
	if rate != a.s.base {
		t.Fatalf("idle link: rate %.0f, want base %.0f", rate, a.s.base)
	}
}

func TestAutorateStaleUsesBase(t *testing.T) {
	a := testAutorate(t, ShaperConfig{Rate: 100})
	now, _ := feedAutorate(a, time.Unix(0, 0), time.Second, 1e9, constDelay(0))
	now, rate := feedAutorate(a, now, time.Second, 1e9, constDelay(time.Second))
	if rate >= a.s.base {
		t.Fatal("no back off")
	}
	// Probes stop (an old peer, or the path went down).
	if rate = a.tick(now.Add(3*time.Second), a.lastSent); rate != a.s.base {
		t.Fatalf("without samples: rate %.0f, want base", rate)
	}
}

func TestNewAutorateSettings(t *testing.T) {
	cfg := ShaperConfig{Enabled: true, Rate: 100}
	s, _ := newShaperSettings(&cfg)
	if s.autorate != nil {
		t.Fatal("autorate enabled by default")
	}
	cfg.Autorate = true
	s, _ = newShaperSettings(&cfg)
	a := s.autorate
	if a.base != 12.5e6 || a.min != 2.5e6 || a.max != 12.5e6 || a.threshold != 15*time.Millisecond || a.interval != 50*time.Millisecond {
		t.Fatalf("defaults %+v", *a)
	}
	cfg.MinRate, cfg.MaxRate, cfg.BloatThreshold, cfg.ProbeInterval = 500, 50, 8, 20
	s, _ = newShaperSettings(&cfg)
	a = s.autorate
	if a.min != a.base || a.max != a.base || a.threshold != 8*time.Millisecond || a.interval != 20*time.Millisecond {
		t.Fatalf("clamped %+v", *a)
	}
}

func TestShaperSetRate(t *testing.T) {
	p, _, r := testShaper(t, ShaperConfig{Rate: 40, QueueLimit: 8 << 20})
	p.codel.target = time.Hour
	start := time.Unix(0, 0)
	for i := 0; i < 20000; i++ {
		p.enqueue(testShaperPacket(r, 1400), testShaperAddr, false, start)
	}
	p.setRate(start, 20e6/8)
	sent := runShaper(p, start, time.Second, 100*time.Microsecond, nil)
	if got, want := float64(sent[1400]), 20e6/8; got < want*0.98 || got > want*1.02+p.r.burst {
		t.Fatalf("sent %.0f bytes in 1s at 20 Mbit/s, want about %.0f", got, want)
	}
	want := time.Duration(float64(p.bulk.bytes) / (20e6 / 8) * float64(time.Second))
	if d := p.queueDelay(); d != want || d < time.Second {
		t.Fatalf("queue delay %v, want %v (the backlog at 20 Mbit/s)", d, want)
	}
	p.reset()
}

func TestContainsIdent(t *testing.T) {
	for _, c := range []struct {
		expr, name string
		want       bool
	}{
		{"delay_a < 10", "delay_a", true},
		{"qdelay_a < 10", "delay_a", false},
		{"delay_ab < 10", "delay_a", false},
		{"x && delay_a", "delay_a", true},
		{"(delay_a+1)", "delay_a", true},
		{"qdelay_a < 5 && delay_a < 10", "delay_a", true},
	} {
		if got := containsIdent(c.expr, c.name); got != c.want {
			t.Errorf("containsIdent(%q, %q) = %v", c.expr, c.name, got)
		}
	}
}
