package main

import (
	"fmt"
	"math"
	"net"
	"sync/atomic"
	"time"
)

const (
	defaultShaperPrioritySize  = 1000
	defaultShaperPriorityShare = 50
	defaultShaperTargetDelay   = 5 * time.Millisecond
	defaultShaperInterval      = 100 * time.Millisecond
	shaperMinQueueLimit        = 256 << 10
	shaperMaxQueueLimit        = 4 << 20
	shaperMaxControlQueue      = 1024
	// Below this many queued bytes the bulk queue is never considered standing.
	shaperMinStandingBytes = 1500
)

// ShaperConfig limits the send rate of a path to just below the bottleneck
// link, so that the queue forms inside UDPlex instead of in a modem or ISP
// buffer. Small packets (games, voice, ACKs, handshakes) are sent first and the
// bulk queue is kept short with CoDel.
type ShaperConfig struct {
	Enabled       bool    `json:"enabled" yaml:"enabled"`
	Rate          float64 `json:"rate" yaml:"rate"`                     // Mbit/s on the wire, set to 90-95% of the link bandwidth
	Overhead      int     `json:"overhead" yaml:"overhead"`             // extra bytes per packet below IP (e.g. PPPoE 8)
	PrioritySize  *int    `json:"priority_size" yaml:"priority_size"`   // UDP payloads up to this size are prioritized, default 1000, 0 disables
	PriorityShare int     `json:"priority_share" yaml:"priority_share"` // percent of rate guaranteed to priority traffic while bulk is queued, default 50
	TargetDelay   float64 `json:"target_delay" yaml:"target_delay"`     // ms, CoDel target for the bulk queue, default 5
	Interval      int     `json:"interval" yaml:"interval"`             // ms, CoDel interval, default 100
	QueueLimit    int     `json:"queue_limit" yaml:"queue_limit"`       // bytes, default 100ms at rate (256KB-4MB)

	// Autorate follows a link whose bandwidth varies (LTE/5G, evening
	// congestion) by measuring the one-way delay to the peer. Requires auth
	// on both ends.
	Autorate       bool    `json:"autorate" yaml:"autorate"`
	MinRate        float64 `json:"min_rate" yaml:"min_rate"`               // Mbit/s, default rate/5
	MaxRate        float64 `json:"max_rate" yaml:"max_rate"`               // Mbit/s, default rate
	BloatThreshold float64 `json:"bloat_threshold" yaml:"bloat_threshold"` // ms of extra one-way delay that counts as bufferbloat, default 15
	ProbeInterval  int     `json:"probe_interval" yaml:"probe_interval"`   // ms between delay probes while busy, default 50
}

// shaperSettings is the resolved, immutable shaper configuration.
type shaperSettings struct {
	rate          float64 // bytes per second
	burst         float64 // bucket depth in bytes
	overhead      int
	prioritySize  int
	priorityRate  float64
	priorityBurst float64
	target        time.Duration
	interval      time.Duration
	queueLimit    int
	autorate      *autorateSettings
}

func newShaperSettings(cfg *ShaperConfig) (*shaperSettings, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}
	if cfg.Rate <= 0 {
		return nil, fmt.Errorf("shaper rate must be positive (Mbit/s)")
	}
	s := &shaperSettings{
		rate:         cfg.Rate * 1e6 / 8,
		overhead:     max(cfg.Overhead, 0),
		prioritySize: defaultShaperPrioritySize,
		target:       defaultShaperTargetDelay,
		interval:     defaultShaperInterval,
	}
	// Two milliseconds of tokens keeps timer wakeups near 1kHz without
	// letting bursts build a noticeable queue at the bottleneck.
	s.burst = math.Max(s.rate*0.002, 3000)
	if cfg.PrioritySize != nil {
		s.prioritySize = max(*cfg.PrioritySize, 0)
	}
	share := cfg.PriorityShare
	if share <= 0 || share > 100 {
		share = defaultShaperPriorityShare
	}
	s.priorityRate = s.rate * float64(share) / 100
	s.priorityBurst = math.Max(s.burst*float64(share)/100, 3000)
	if cfg.TargetDelay > 0 {
		s.target = time.Duration(cfg.TargetDelay * float64(time.Millisecond))
	}
	if cfg.Interval > 0 {
		s.interval = time.Duration(cfg.Interval) * time.Millisecond
	}
	s.queueLimit = cfg.QueueLimit
	if s.queueLimit <= 0 {
		s.queueLimit = min(max(int(s.rate*0.1), shaperMinQueueLimit), shaperMaxQueueLimit)
	}
	s.autorate = newAutorateSettings(cfg, s.rate)
	return s, nil
}

// pathRate is the token bucket of one path at its current rate.
type pathRate struct {
	rate          float64
	burst         float64
	priorityRate  float64
	priorityBurst float64
}

func (s *shaperSettings) pathRate(rate float64) pathRate {
	share := s.priorityRate / s.rate
	r := pathRate{rate: rate, burst: math.Max(rate*0.002, 3000), priorityRate: rate * share}
	r.priorityBurst = math.Max(r.burst*share, 3000)
	return r
}

// shaperStats aggregates all paths of a component for the API.
type shaperStats struct {
	sentBytes     atomic.Uint64
	priorityPkts  atomic.Uint64
	codelDrops    atomic.Uint64
	overflowDrops atomic.Uint64
	queuedBytes   atomic.Int64
	maxDelay      atomic.Int64 // largest bulk sojourn (ns) since the last snapshot
}

func (s *shaperStats) observeDelay(d time.Duration) {
	for {
		cur := s.maxDelay.Load()
		if int64(d) <= cur || s.maxDelay.CompareAndSwap(cur, int64(d)) {
			return
		}
	}
}

type shaperItem struct {
	pkt  *Packet
	addr net.Addr
	enq  time.Time
	wire int // bytes on the wire, including IP/UDP headers and overhead
}

// shaperQueue is a FIFO of items.
type shaperQueue struct {
	items []shaperItem
	head  int
	bytes int
}

func (q *shaperQueue) len() int { return len(q.items) - q.head }

func (q *shaperQueue) push(it shaperItem) {
	if q.head > 0 && q.head >= len(q.items)/2 {
		n := copy(q.items, q.items[q.head:])
		clear(q.items[n:])
		q.items = q.items[:n]
		q.head = 0
	}
	q.items = append(q.items, it)
	q.bytes += it.wire
}

func (q *shaperQueue) peek() *shaperItem { return &q.items[q.head] }

func (q *shaperQueue) pop() shaperItem {
	it := q.items[q.head]
	q.items[q.head] = shaperItem{}
	q.head++
	q.bytes -= it.wire
	if q.head == len(q.items) {
		q.items = q.items[:0]
		q.head = 0
	}
	return it
}

// pathShaper paces one path (a forward target or a listen client). It is used
// by a single send goroutine and is not safe for concurrent use.
type pathShaper struct {
	s              *shaperSettings
	stats          *shaperStats
	r              pathRate
	sent           uint64 // wire bytes sent by this path
	tokens         float64
	priorityTokens float64
	last           time.Time
	control        shaperQueue // auth and heartbeat messages, never delayed by the rate
	priority       shaperQueue
	bulk           shaperQueue
	codel          codel // bulk queue
	priorityCodel  codel // only matters when priority traffic exceeds its share
	lastActive     time.Time
}

func newPathShaper(s *shaperSettings, stats *shaperStats, now time.Time) *pathShaper {
	return &pathShaper{
		s:              s,
		stats:          stats,
		r:              s.pathRate(s.rate),
		tokens:         s.burst,
		priorityTokens: s.priorityBurst,
		last:           now,
		codel:          codel{target: s.target, interval: s.interval},
		priorityCodel:  codel{target: s.target, interval: s.interval},
		lastActive:     now,
	}
}

// udpWireOverhead is the IP and UDP header size for packets sent to addr.
func udpWireOverhead(addr net.Addr) int {
	if ua, ok := addr.(*net.UDPAddr); ok && ua.IP.To4() == nil && len(ua.IP) == net.IPv6len {
		return 48
	}
	return 28
}

// enqueue takes ownership of the packet reference.
func (p *pathShaper) enqueue(pkt *Packet, addr net.Addr, control bool, now time.Time) {
	size := pkt.Length()
	it := shaperItem{pkt: pkt, addr: addr, enq: now, wire: size + udpWireOverhead(addr) + p.s.overhead}
	if control {
		if p.control.len() >= shaperMaxControlQueue {
			p.drop(it, &p.stats.overflowDrops)
			return
		}
		p.control.push(it)
		p.stats.queuedBytes.Add(int64(it.wire))
		return
	}
	// Make room by dropping the oldest bulk packets: their delay is already
	// the largest, and the sender learns about congestion soonest.
	for p.priority.bytes+p.bulk.bytes+it.wire > p.s.queueLimit && p.bulk.len() > 0 {
		old := p.bulk.pop()
		p.stats.queuedBytes.Add(-int64(old.wire))
		p.drop(old, &p.stats.overflowDrops)
	}
	if p.priority.bytes+p.bulk.bytes+it.wire > p.s.queueLimit {
		p.drop(it, &p.stats.overflowDrops)
		return
	}
	p.lastActive = now
	if p.s.prioritySize > 0 && size <= p.s.prioritySize {
		p.priority.push(it)
	} else {
		p.bulk.push(it)
	}
	p.stats.queuedBytes.Add(int64(it.wire))
}

func (p *pathShaper) drop(it shaperItem, counter *atomic.Uint64) {
	counter.Add(1)
	it.pkt.Release(1)
}

func (p *pathShaper) refill(now time.Time) {
	dt := now.Sub(p.last).Seconds()
	if dt <= 0 {
		return
	}
	p.last = now
	p.tokens = math.Min(p.tokens+p.r.rate*dt, p.r.burst)
	p.priorityTokens = math.Min(p.priorityTokens+p.r.priorityRate*dt, p.r.priorityBurst)
}

// setRate changes the rate of the path, in bytes per second.
func (p *pathShaper) setRate(now time.Time, rate float64) {
	if rate == p.r.rate {
		return
	}
	p.refill(now)
	p.r = p.s.pathRate(rate)
	p.tokens = math.Min(p.tokens, p.r.burst)
	p.priorityTokens = math.Min(p.priorityTokens, p.r.priorityBurst)
}

// queueDelay estimates how long a packet queued now would wait.
func (p *pathShaper) queueDelay() time.Duration {
	return time.Duration(float64(p.priority.bytes+p.bulk.bytes) / p.r.rate * float64(time.Second))
}

// dequeue appends the packets that may be sent now to out, up to limit items.
func (p *pathShaper) dequeue(now time.Time, out []shaperItem, limit int) []shaperItem {
	p.refill(now)
	for len(out) < limit {
		if p.control.len() > 0 {
			it := p.control.pop()
			p.tokens -= float64(it.wire)
			out = p.take(out, it)
			continue
		}
		if p.tokens <= 0 || p.priority.len()+p.bulk.len() == 0 {
			break
		}
		// Priority traffic goes first up to its share; beyond that, queued bulk
		// gets the rest so small-packet floods cannot starve it.
		if p.priority.len() > 0 && (p.priorityTokens > 0 || p.bulk.len() == 0) {
			it := p.priority.pop()
			if p.priority.bytes >= shaperMinStandingBytes && p.priorityCodel.shouldDrop(now, now.Sub(it.enq)) {
				p.stats.queuedBytes.Add(-int64(it.wire))
				p.drop(it, &p.stats.codelDrops)
				continue
			}
			p.tokens -= float64(it.wire)
			p.priorityTokens = math.Max(p.priorityTokens-float64(it.wire), -p.r.priorityBurst)
			p.stats.priorityPkts.Add(1)
			out = p.take(out, it)
			continue
		}
		it := p.bulk.pop()
		sojourn := now.Sub(it.enq)
		p.stats.observeDelay(sojourn)
		if p.bulk.bytes < shaperMinStandingBytes {
			sojourn = 0
		}
		if p.codel.shouldDrop(now, sojourn) {
			p.stats.queuedBytes.Add(-int64(it.wire))
			p.drop(it, &p.stats.codelDrops)
			continue
		}
		p.tokens -= float64(it.wire)
		out = p.take(out, it)
	}
	return out
}

func (p *pathShaper) take(out []shaperItem, it shaperItem) []shaperItem {
	p.stats.queuedBytes.Add(-int64(it.wire))
	p.stats.sentBytes.Add(uint64(it.wire))
	p.sent += uint64(it.wire)
	return append(out, it)
}

// wait returns how long until packets can be sent, or -1 if nothing is queued.
// It waits for half a bucket of tokens so a busy path wakes about once per
// millisecond instead of once per packet.
func (p *pathShaper) wait(now time.Time) time.Duration {
	if p.control.len() > 0 {
		return 0
	}
	if p.priority.len()+p.bulk.len() == 0 {
		return -1
	}
	p.refill(now)
	if p.tokens > 0 {
		return 0
	}
	need := -p.tokens + p.r.burst/2
	return time.Duration(need / p.r.rate * float64(time.Second))
}

// idle reports whether the path has nothing queued and has carried no data
// (control messages aside) for d.
func (p *pathShaper) idle(now time.Time, d time.Duration) bool {
	return p.control.len()+p.priority.len()+p.bulk.len() == 0 && now.Sub(p.lastActive) >= d
}

// reset releases every queued packet.
func (p *pathShaper) reset() {
	for _, q := range []*shaperQueue{&p.control, &p.priority, &p.bulk} {
		for q.len() > 0 {
			it := q.pop()
			p.stats.queuedBytes.Add(-int64(it.wire))
			it.pkt.Release(1)
		}
	}
}
