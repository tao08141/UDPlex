package main

import (
	"testing"
	"time"
)

func testCodel() *codel {
	return &codel{target: 5 * time.Millisecond, interval: 100 * time.Millisecond}
}

// feed dequeues one packet every step with the given sojourn and returns the drop times.
func feed(c *codel, start time.Time, dur, step, sojourn time.Duration) []time.Duration {
	var drops []time.Duration
	for t := time.Duration(0); t < dur; t += step {
		if c.shouldDrop(start.Add(t), sojourn) {
			drops = append(drops, t)
		}
	}
	return drops
}

func TestCodelNoDropBelowTarget(t *testing.T) {
	if drops := feed(testCodel(), time.Unix(0, 0), 2*time.Second, 100*time.Microsecond, 4*time.Millisecond); len(drops) != 0 {
		t.Fatalf("dropped %d packets below target", len(drops))
	}
}

func TestCodelStandingQueue(t *testing.T) {
	drops := feed(testCodel(), time.Unix(0, 0), time.Second, 100*time.Microsecond, 20*time.Millisecond)
	if len(drops) < 3 {
		t.Fatalf("expected repeated drops for a standing queue, got %v", drops)
	}
	// Nothing is dropped during the first interval, which absorbs bursts.
	if drops[0] < 100*time.Millisecond {
		t.Fatalf("first drop at %v, before one interval", drops[0])
	}
	// The drop rate increases: gaps follow interval/sqrt(count).
	for i := 2; i < len(drops); i++ {
		if drops[i]-drops[i-1] > drops[i-1]-drops[i-2]+time.Millisecond {
			t.Fatalf("drop gaps grow: %v", drops)
		}
	}
}

func TestCodelShortBurstNotDropped(t *testing.T) {
	c := testCodel()
	start := time.Unix(0, 0)
	drops := feed(c, start, 50*time.Millisecond, 100*time.Microsecond, 30*time.Millisecond)
	drops = append(drops, feed(c, start.Add(50*time.Millisecond), time.Second, 100*time.Microsecond, time.Millisecond)...)
	if len(drops) != 0 {
		t.Fatalf("burst shorter than interval caused drops at %v", drops)
	}
}

func TestCodelStopsWhenQueueDrains(t *testing.T) {
	c := testCodel()
	start := time.Unix(0, 0)
	if len(feed(c, start, 300*time.Millisecond, 100*time.Microsecond, 20*time.Millisecond)) == 0 {
		t.Fatal("expected drops")
	}
	if drops := feed(c, start.Add(300*time.Millisecond), time.Second, 100*time.Microsecond, time.Millisecond); len(drops) != 0 {
		t.Fatalf("kept dropping after the queue drained: %v", drops)
	}
}
