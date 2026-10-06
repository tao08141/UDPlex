package main

import (
	"math"
	"time"
)

// codel is the CoDel drop law (RFC 8289). It decides, per dequeued packet,
// whether to drop it so that the standing delay of a queue stays near the
// target, while bursts shorter than one interval pass untouched.
type codel struct {
	target   time.Duration
	interval time.Duration

	firstAbove time.Time // deadline after which a sojourn above target is persistent
	dropNext   time.Time
	count      uint32
	lastCount  uint32
	dropping   bool
}

// shouldDrop reports whether the packet being dequeued at now, which waited
// sojourn in the queue, should be dropped.
func (c *codel) shouldDrop(now time.Time, sojourn time.Duration) bool {
	okToDrop := false
	if sojourn < c.target {
		c.firstAbove = time.Time{}
	} else if c.firstAbove.IsZero() {
		c.firstAbove = now.Add(c.interval)
	} else if !now.Before(c.firstAbove) {
		okToDrop = true
	}

	if c.dropping {
		if !okToDrop {
			c.dropping = false
			return false
		}
		if !now.Before(c.dropNext) {
			c.count++
			c.dropNext = c.controlLaw(c.dropNext)
			return true
		}
		return false
	}
	if !okToDrop {
		return false
	}

	c.dropping = true
	// Resume near the previous drop rate if we were dropping recently.
	if delta := c.count - c.lastCount; delta > 1 && now.Sub(c.dropNext) < 16*c.interval {
		c.count = delta
	} else {
		c.count = 1
	}
	c.lastCount = c.count
	c.dropNext = c.controlLaw(now)
	return true
}

func (c *codel) controlLaw(t time.Time) time.Time {
	return t.Add(time.Duration(float64(c.interval) / math.Sqrt(float64(c.count))))
}
