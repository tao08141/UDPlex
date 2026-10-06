package main

import (
	"testing"
	"time"
)

type recordingComponent struct {
	BaseComponent
	got []uint64
}

func (c *recordingComponent) Start() error { return nil }
func (c *recordingComponent) Stop() error  { return nil }
func (c *recordingComponent) HandlePacket(p *Packet) error {
	c.got = append(c.got, p.BatchID())
	p.Release(1)
	return nil
}

func TestLoadBalancerBatchDecision(t *testing.T) {
	for _, batchDecision := range []bool{false, true} {
		r := NewRouter(Config{})
		a := &recordingComponent{BaseComponent: NewBaseComponent("a", r, 0)}
		b := &recordingComponent{BaseComponent: NewBaseComponent("b", r, 0)}
		lb, err := NewLoadBalancerComponent(LoadBalancerComponentConfig{
			Tag:           "lb",
			WindowSize:    4,
			BatchDecision: batchDecision,
			Detour: []LoadBalancerDetourRule{
				{Rule: "seq % 2 == 0", Targets: []string{"a"}},
				{Rule: "seq % 2 == 1", Targets: []string{"b"}},
			},
		}, r)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []Component{a, b, lb} {
			if err := r.Register(c); err != nil {
				t.Fatal(err)
			}
		}
		if err := lb.precompileRules(); err != nil {
			t.Fatal(err)
		}

		// Three batches of four packets, then four unbatched packets.
		for batch := 0; batch < 3; batch++ {
			id := r.NextBatchID()
			for i := 0; i < 4; i++ {
				p := r.GetPacket("src")
				p.SetLength(100)
				p.SetBatchID(id)
				if err := r.Route(&p, []string{"lb"}); err != nil {
					t.Fatal(err)
				}
				p.Release(1)
			}
		}
		for i := 0; i < 4; i++ {
			p := r.GetPacket("src")
			p.SetLength(100)
			if err := r.Route(&p, []string{"lb"}); err != nil {
				t.Fatal(err)
			}
			p.Release(1)
		}

		if !batchDecision {
			if len(a.got) != 8 || len(b.got) != 8 {
				t.Fatalf("per-packet: a=%d b=%d, want 8/8", len(a.got), len(b.got))
			}
			continue
		}
		// seq advances per batch: b gets batches 1 and 3, a gets batch 2, then
		// the unbatched packets alternate.
		if len(a.got) != 6 || len(b.got) != 10 {
			t.Fatalf("per-batch: a=%d b=%d, want 6/10", len(a.got), len(b.got))
		}
		for _, id := range a.got {
			if id != 0 && countOf(b.got, id) > 0 {
				t.Fatalf("batch %d split across paths", id)
			}
		}
	}
}

func countOf(list []uint64, v uint64) int {
	n := 0
	for _, x := range list {
		if x == v {
			n++
		}
	}
	return n
}

type queuedComponent struct {
	recordingComponent
	qdelay time.Duration
}

func (c *queuedComponent) QueueDelay() time.Duration { return c.qdelay }

func TestLoadBalancerQueueDelay(t *testing.T) {
	r := NewRouter(Config{})
	a := &queuedComponent{recordingComponent: recordingComponent{BaseComponent: NewBaseComponent("line_a", r, 0)}}
	b := &queuedComponent{recordingComponent: recordingComponent{BaseComponent: NewBaseComponent("line_b", r, 0)}}
	lb, err := NewLoadBalancerComponent(LoadBalancerComponentConfig{
		Tag:        "lb",
		WindowSize: 4,
		Detour: []LoadBalancerDetourRule{
			{Rule: "qdelay_line_a <= qdelay_line_b", Targets: []string{"line_a"}},
			{Rule: "qdelay_line_a > qdelay_line_b", Targets: []string{"line_b"}},
		},
	}, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Component{a, b, lb} {
		if err := r.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := lb.precompileRules(); err != nil {
		t.Fatal(err)
	}
	send := func() {
		p := r.GetPacket("src")
		p.SetLength(100)
		if err := r.Route(&p, []string{"lb"}); err != nil {
			t.Fatal(err)
		}
		p.Release(1)
	}
	send()
	a.qdelay = 30 * time.Millisecond
	b.qdelay = 2 * time.Millisecond
	send()
	if len(a.got) != 1 || len(b.got) != 1 {
		t.Fatalf("line_a got %d, line_b got %d packets; want 1 each", len(a.got), len(b.got))
	}
	if got := lb.checkTagQueueDelay("line_a"); got != 30 {
		t.Fatalf("qdelay_line_a = %v ms", got)
	}
	if got := lb.checkTagQueueDelay("missing"); got != 0 {
		t.Fatalf("qdelay of a missing tag = %v", got)
	}
}
