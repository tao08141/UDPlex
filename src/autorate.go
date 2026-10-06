package main

import (
	"encoding/binary"
	"math"
	"sync"
	"time"
)

const (
	defaultAutorateThreshold     = 15 * time.Millisecond
	defaultAutorateProbeInterval = 50 * time.Millisecond
	// Probes slow down once a path has been idle for a while; a few samples
	// per second are enough to track the baseline delay.
	autorateIdleProbeInterval = time.Second
	autorateIdleAfter         = 2 * time.Second
	// Without samples for this long the controller has no information and
	// returns to the base rate.
	autorateStaleAfter = 2 * time.Second
	// After a decrease, wait before reacting again so the queue can drain.
	autorateDecreaseRefractory = 300 * time.Millisecond
	autorateIncreaseRefractory = 3 * time.Second
	autorateDecreaseFactor     = 0.9
	autorateIncreasePerTick    = 0.01 // per probe interval while the path is busy and clean
	autorateDecayPerTick       = 0.01 // per probe interval towards the base rate when the path is idle
	autorateHighLoad           = 0.75 // achieved/rate considered busy
	// The baseline follows lower samples immediately and higher ones very
	// slowly, so it tracks clock drift and route changes but not queues.
	autorateBaselineRise = 0.001
	probePayloadSize     = 8
	probeAckPayloadSize  = 16
)

type autorateSettings struct {
	base, min, max float64 // bytes per second
	threshold      time.Duration
	interval       time.Duration
}

// newAutorateSettings resolves the autorate part of a shaper config; nil if disabled.
func newAutorateSettings(cfg *ShaperConfig, base float64) *autorateSettings {
	if !cfg.Autorate {
		return nil
	}
	a := &autorateSettings{
		base:      base,
		min:       base / 5,
		max:       base,
		threshold: defaultAutorateThreshold,
		interval:  defaultAutorateProbeInterval,
	}
	if cfg.MinRate > 0 {
		a.min = math.Min(cfg.MinRate*1e6/8, base)
	}
	if cfg.MaxRate > 0 {
		a.max = math.Max(cfg.MaxRate*1e6/8, base)
	}
	if cfg.BloatThreshold > 0 {
		a.threshold = time.Duration(cfg.BloatThreshold * float64(time.Millisecond))
	}
	if cfg.ProbeInterval > 0 {
		a.interval = time.Duration(cfg.ProbeInterval) * time.Millisecond
	}
	return a
}

// autorate adjusts the shaping rate of one path from the one-way delay that
// the peer measures for our probes, in the spirit of cake-autorate: back off
// when the bottleneck queue grows, probe upwards while the path is busy and
// the queue stays empty, and drift back to the base rate otherwise.
//
// addSample is called by the receive goroutine; everything else by the send loop.
type autorate struct {
	s *autorateSettings

	mu          sync.Mutex
	baseline    float64 // relative one-way delay in ns; clock offset included
	hasBaseline bool
	delayed     uint8 // over-threshold flags of the last 4 samples, newest in bit 0
	fresh       int   // samples since the last tick
	lastSample  time.Time
	lastDelta   time.Duration
	lastRTT     time.Duration

	rate         float64
	lastTick     time.Time
	lastSent     uint64
	lastDecrease time.Time
	lastIncrease time.Time
	achieved     float64
}

func newAutorate(s *autorateSettings, now time.Time) *autorate {
	return &autorate{s: s, rate: s.base, lastTick: now}
}

// addSample records the peer's relative one-way delay for one probe.
func (a *autorate) addSample(owd int64, rtt time.Duration, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d := float64(owd)
	if !a.hasBaseline || d < a.baseline {
		a.baseline = d
		a.hasBaseline = true
	} else {
		a.baseline += (d - a.baseline) * autorateBaselineRise
	}
	a.lastDelta = time.Duration(d - a.baseline)
	a.lastRTT = rtt
	a.delayed <<= 1
	if a.lastDelta > a.s.threshold {
		a.delayed |= 1
	}
	a.fresh++
	a.lastSample = now
}

// bloated reports bufferbloat when at least 2 of the last 4 samples were delayed.
func (a *autorate) bloated() bool {
	n := 0
	for v := a.delayed & 0x0f; v != 0; v &= v - 1 {
		n++
	}
	return n >= 2
}

// tick updates the rate from the bytes the path has sent so far and returns it.
func (a *autorate) tick(now time.Time, sent uint64) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	dt := now.Sub(a.lastTick)
	if dt <= 0 {
		return a.rate
	}
	a.achieved = float64(sent-a.lastSent) / dt.Seconds()
	a.lastTick, a.lastSent = now, sent
	steps := float64(dt) / float64(a.s.interval)

	switch {
	case a.lastSample.IsZero() || now.Sub(a.lastSample) > autorateStaleAfter:
		a.rate = a.s.base
	case a.fresh > 0 && a.bloated():
		if now.Sub(a.lastDecrease) >= autorateDecreaseRefractory {
			a.rate = math.Max(a.s.min, math.Min(a.rate, a.achieved)*autorateDecreaseFactor)
			a.lastDecrease = now
			a.delayed = 0
		}
	case a.achieved >= a.rate*autorateHighLoad:
		if now.Sub(a.lastDecrease) >= autorateIncreaseRefractory {
			a.rate = math.Min(a.s.max, a.rate*math.Pow(1+autorateIncreasePerTick, steps))
			a.lastIncrease = now
		}
	default:
		f := math.Pow(1-autorateDecayPerTick, steps)
		if a.rate > a.s.base {
			a.rate = math.Max(a.s.base, a.rate*f)
		} else if a.rate < a.s.base {
			a.rate = math.Min(a.s.base, a.rate/f)
		}
	}
	a.fresh = 0
	return a.rate
}

// probeInterval is the time between probes, slower once the path has been idle.
func (a *autorate) probeInterval(now, lastActive time.Time) time.Duration {
	if now.Sub(lastActive) >= autorateIdleAfter {
		return autorateIdleProbeInterval
	}
	return a.s.interval
}

type autorateSnapshot struct {
	Rate     float64
	Achieved float64
	Delta    time.Duration
	RTT      time.Duration
}

func (a *autorate) snapshot() autorateSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return autorateSnapshot{Rate: a.rate, Achieved: a.achieved, Delta: a.lastDelta, RTT: a.lastRTT}
}

// Probe messages carry the sender's wall clock time. The receiver answers with
// a probe ack holding that time and its own receive time minus it: the one-way
// delay plus the clock offset between the two hosts, which cancels out against
// the baseline.

func createProbe(buffer []byte, now time.Time) int {
	WriteHeader(buffer, MsgTypeProbe, probePayloadSize)
	binary.BigEndian.PutUint64(buffer[HeaderSize:], uint64(now.UnixNano()))
	return HeaderSize + probePayloadSize
}

// createProbeAck answers the probe in msg (header included); 0 if msg is malformed.
func createProbeAck(buffer, msg []byte, now time.Time) int {
	if len(msg) < HeaderSize+probePayloadSize {
		return 0
	}
	sent := int64(binary.BigEndian.Uint64(msg[HeaderSize:]))
	WriteHeader(buffer, MsgTypeProbeAck, probeAckPayloadSize)
	binary.BigEndian.PutUint64(buffer[HeaderSize:], uint64(sent))
	binary.BigEndian.PutUint64(buffer[HeaderSize+8:], uint64(now.UnixNano()-sent))
	return HeaderSize + probeAckPayloadSize
}

// parseProbeAck returns the peer's relative one-way delay and the round trip time.
func parseProbeAck(msg []byte, now time.Time) (owd int64, rtt time.Duration, ok bool) {
	if len(msg) < HeaderSize+probeAckPayloadSize {
		return 0, 0, false
	}
	sent := int64(binary.BigEndian.Uint64(msg[HeaderSize:]))
	owd = int64(binary.BigEndian.Uint64(msg[HeaderSize+8:]))
	rtt = time.Duration(now.UnixNano() - sent)
	if rtt < 0 || rtt > time.Minute {
		return 0, 0, false
	}
	return owd, rtt, true
}
