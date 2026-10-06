package main

import (
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultTcpQueueLimit        = 2 << 20
	defaultTcpQueueNotsentLowat = 128 << 10
	// With an automatic notsent_lowat, the kernel holds about this much data
	// of unsent bytes at the measured delivery rate.
	tcpAutoLowatTime = 5 * time.Millisecond
	tcpAutoLowatMin  = 16 << 10
	tcpAutoLowatMax  = 512 << 10
	tcpAutoLowatTune = 200 * time.Millisecond
	// Priority traffic may run this far ahead of its share before bulk gets a turn.
	tcpQueueMaxCredit = 64 << 10
)

// TcpQueueConfig controls the send queue of a TCP tunnel connection. The
// kernel is kept from buffering more than notsent_lowat unsent bytes, so
// packets wait in this queue instead, where small packets go first and CoDel
// keeps the bulk queue short. The tunneled TCP flows then see drops instead
// of an ever growing delay.
type TcpQueueConfig struct {
	Enabled       *bool   `json:"enabled" yaml:"enabled"`               // default true
	PrioritySize  *int    `json:"priority_size" yaml:"priority_size"`   // default 1000, 0 disables
	PriorityShare int     `json:"priority_share" yaml:"priority_share"` // percent, default 50
	TargetDelay   float64 `json:"target_delay" yaml:"target_delay"`     // ms, default 5
	Interval      int     `json:"interval" yaml:"interval"`             // ms, default 100
	QueueLimit    int     `json:"queue_limit" yaml:"queue_limit"`       // bytes per connection, default 2MB
	NotsentLowat  *int    `json:"notsent_lowat" yaml:"notsent_lowat"`   // bytes; default follows the delivery rate on Linux (128KB elsewhere), 0 leaves the kernel default
	PriorityConn  bool    `json:"priority_conn" yaml:"priority_conn"`   // with 2+ connections, reserve the first for small packets
}

type tcpQueueSettings struct {
	prioritySize int
	share        float64 // priority share of the bytes sent while bulk is queued
	target       time.Duration
	interval     time.Duration
	codel        bool
	limit        int
	notsentLowat int
	autoLowat    bool // retune notsentLowat from the delivery rate
	priorityConn bool
	// Socket options of the connections, set from the component config.
	congestion string
	pacingRate uint64 // bytes per second
}

// newTcpTunnelSettings resolves the queue and socket settings of a TCP tunnel component.
func newTcpTunnelSettings(cfg ComponentConfig, router *Router) *tcpQueueSettings {
	s := newTcpQueueSettings(cfg.Queue, router.config.QueueSize, router.config.BufferSize)
	s.congestion = cfg.Congestion
	if cfg.PacingRate > 0 {
		s.pacingRate = uint64(cfg.PacingRate * 1e6 / 8)
	}
	return s
}

// tunedLowat returns the notsent_lowat for a connection delivering rate
// bytes per second, or 0 if it should stay as is.
func (s *tcpQueueSettings) tunedLowat(rate uint64, current int) int {
	if !s.autoLowat || rate == 0 {
		return 0
	}
	if s.pacingRate > 0 && s.pacingRate < rate {
		rate = s.pacingRate
	}
	want := min(max(int(float64(rate)*tcpAutoLowatTime.Seconds()), tcpAutoLowatMin), tcpAutoLowatMax)
	// Ignore small changes to avoid a setsockopt on every tick.
	if current > 0 && want*4 > current*3 && want*4 < current*5 {
		return 0
	}
	return want
}

// applySocketOptions sets the configured TCP options on a new connection.
func (s *tcpQueueSettings) applySocketOptions(conn net.Conn, tag string) {
	if s.notsentLowat > 0 {
		if err := setTCPNotsentLowat(conn, s.notsentLowat); err != nil {
			logger.Debugf("%s: Failed to set TCP_NOTSENT_LOWAT: %v", tag, err)
		}
	}
	if s.congestion != "" {
		if err := setTCPCongestion(conn, s.congestion); err != nil {
			logger.Warnf("%s: Failed to set congestion control %q, using the system default (is the tcp_%s kernel module loaded?): %v", tag, s.congestion, s.congestion, err)
		}
	}
	if s.pacingRate > 0 {
		if err := setTCPPacingRate(conn, s.pacingRate); err != nil {
			logger.Warnf("%s: Failed to set pacing rate: %v", tag, err)
		}
	}
}

// newTcpQueueSettings resolves the config. queueSize and bufferSize size the
// limit of a disabled queue like the packet channel it replaces.
func newTcpQueueSettings(cfg *TcpQueueConfig, queueSize, bufferSize int) *tcpQueueSettings {
	if cfg == nil {
		cfg = &TcpQueueConfig{}
	}
	if cfg.Enabled != nil && !*cfg.Enabled {
		return &tcpQueueSettings{limit: max(queueSize, 16) * max(bufferSize, 1500)}
	}
	s := &tcpQueueSettings{
		prioritySize: defaultShaperPrioritySize,
		share:        defaultShaperPriorityShare / 100.0,
		target:       defaultShaperTargetDelay,
		interval:     defaultShaperInterval,
		codel:        true,
		limit:        defaultTcpQueueLimit,
		notsentLowat: defaultTcpQueueNotsentLowat,
		autoLowat:    true,
	}
	if cfg.PrioritySize != nil {
		s.prioritySize = max(*cfg.PrioritySize, 0)
	}
	if cfg.PriorityShare > 0 && cfg.PriorityShare <= 100 {
		s.share = float64(cfg.PriorityShare) / 100
	}
	if cfg.TargetDelay > 0 {
		s.target = time.Duration(cfg.TargetDelay * float64(time.Millisecond))
	}
	if cfg.Interval > 0 {
		s.interval = time.Duration(cfg.Interval) * time.Millisecond
	}
	if cfg.QueueLimit > 0 {
		s.limit = cfg.QueueLimit
	}
	if cfg.NotsentLowat != nil {
		s.notsentLowat = max(*cfg.NotsentLowat, 0)
		s.autoLowat = false
	}
	s.priorityConn = cfg.PriorityConn
	return s
}

// tcpQueueStats aggregates the queues of a component for the API.
type tcpQueueStats struct {
	priorityPkts  atomic.Uint64
	codelDrops    atomic.Uint64
	overflowDrops atomic.Uint64
	queuedBytes   atomic.Int64
	maxDelay      atomic.Int64 // largest sojourn (ns) since the last snapshot
}

func (s *tcpQueueStats) observeDelay(d time.Duration) {
	for {
		cur := s.maxDelay.Load()
		if int64(d) <= cur || s.maxDelay.CompareAndSwap(cur, int64(d)) {
			return
		}
	}
}

// tcpTunnelQueue is the send queue of one TCP tunnel connection. Producers
// push from any goroutine; the write loop pops.
type tcpTunnelQueue struct {
	s     *tcpQueueSettings
	stats *tcpQueueStats

	mu       sync.Mutex
	control  shaperQueue
	priority shaperQueue
	bulk     shaperQueue
	// reliable holds large no-drop packets (TCP stream data). They are sent
	// in arrival order with bulk but never dropped: the streams' windows
	// bound how much of them can be queued.
	reliable      shaperQueue
	codel         codel
	priorityCodel codel
	// credit balances priority against bulk bytes while both are queued:
	// priority may go first while it is positive.
	credit float64
	closed bool
	queued atomic.Int64 // data bytes queued, read without the lock

	ready chan struct{} // signalled when packets are pushed
}

func newTcpTunnelQueue(s *tcpQueueSettings, stats *tcpQueueStats) *tcpTunnelQueue {
	if stats == nil {
		stats = &tcpQueueStats{}
	}
	return &tcpTunnelQueue{
		s:             s,
		stats:         stats,
		codel:         codel{target: s.target, interval: s.interval},
		priorityCodel: codel{target: s.target, interval: s.interval},
		credit:        tcpQueueMaxCredit,
		ready:         make(chan struct{}, 1),
	}
}

// push takes ownership of one packet reference; false if it was dropped.
func (q *tcpTunnelQueue) push(pkt *Packet, control bool, now time.Time) bool {
	it := shaperItem{pkt: pkt, enq: now, wire: pkt.Length()}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		pkt.Release(1)
		return false
	}
	switch {
	case control:
		q.control.push(it)
	case pkt.NoDrop():
		if q.s.prioritySize > 0 && it.wire <= q.s.prioritySize {
			q.priority.push(it)
		} else {
			q.reliable.push(it)
		}
	default:
		// Make room by dropping the oldest bulk packets.
		for q.priority.bytes+q.bulk.bytes+q.reliable.bytes+it.wire > q.s.limit && q.bulk.len() > 0 {
			old := q.bulk.pop()
			q.account(-int64(old.wire))
			q.stats.overflowDrops.Add(1)
			old.pkt.Release(1)
		}
		if q.priority.bytes+q.bulk.bytes+q.reliable.bytes+it.wire > q.s.limit {
			q.mu.Unlock()
			q.stats.overflowDrops.Add(1)
			pkt.Release(1)
			return false
		}
		if q.s.prioritySize > 0 && it.wire <= q.s.prioritySize {
			q.priority.push(it)
		} else {
			q.bulk.push(it)
		}
	}
	q.account(int64(it.wire))
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
	return true
}

// pop appends up to limit packets to out, control first, then priority up to
// its share while bulk is waiting.
func (q *tcpTunnelQueue) pop(now time.Time, out []*Packet, limit int) []*Packet {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(out) < limit {
		bulkLen := q.bulk.len() + q.reliable.len()
		if bulkLen == 0 {
			q.credit = tcpQueueMaxCredit
		}
		var it shaperItem
		switch {
		case q.control.len() > 0:
			it = q.control.pop()
		case q.priority.len() > 0 && (q.credit > 0 || bulkLen == 0):
			it = q.priority.pop()
			sojourn := now.Sub(it.enq)
			q.stats.observeDelay(sojourn)
			// Priority packets also wait while the socket is busy; only drop
			// them when priority traffic is over its share.
			if q.s.codel && !it.pkt.NoDrop() && q.credit <= 0 && bulkLen > 0 && q.priority.bytes >= shaperMinStandingBytes && q.priorityCodel.shouldDrop(now, sojourn) {
				q.drop(it)
				continue
			}
			q.credit -= float64(it.wire)
			q.stats.priorityPkts.Add(1)
		case q.reliable.len() > 0 && (q.bulk.len() == 0 || q.reliable.peek().enq.Before(q.bulk.peek().enq)):
			it = q.reliable.pop()
			q.stats.observeDelay(now.Sub(it.enq))
			q.credit = math.Min(q.credit+float64(it.wire)*q.s.share/(1-q.s.share+1e-9), tcpQueueMaxCredit)
		case q.bulk.len() > 0:
			it = q.bulk.pop()
			sojourn := now.Sub(it.enq)
			q.stats.observeDelay(sojourn)
			if q.bulk.bytes+q.reliable.bytes < shaperMinStandingBytes {
				sojourn = 0
			}
			if q.s.codel && q.codel.shouldDrop(now, sojourn) {
				q.drop(it)
				continue
			}
			// Each bulk byte earns share/(1-share) bytes of priority credit.
			q.credit = math.Min(q.credit+float64(it.wire)*q.s.share/(1-q.s.share+1e-9), tcpQueueMaxCredit)
		default:
			return out
		}
		q.account(-int64(it.wire))
		out = append(out, it.pkt)
	}
	return out
}

func (q *tcpTunnelQueue) account(delta int64) {
	q.stats.queuedBytes.Add(delta)
	q.queued.Add(delta)
}

// backlog returns the bytes waiting to be written.
func (q *tcpTunnelQueue) backlog() int64 { return q.queued.Load() }

func (q *tcpTunnelQueue) drop(it shaperItem) {
	q.account(-int64(it.wire))
	q.stats.codelDrops.Add(1)
	it.pkt.Release(1)
}

// delay is how long the oldest queued data packet has been waiting.
func (q *tcpTunnelQueue) delay(now time.Time) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	var d time.Duration
	for _, sq := range []*shaperQueue{&q.priority, &q.bulk, &q.reliable} {
		if sq.len() > 0 && now.Sub(sq.peek().enq) > d {
			d = now.Sub(sq.peek().enq)
		}
	}
	return d
}

func (q *tcpTunnelQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.control.len() + q.priority.len() + q.bulk.len() + q.reliable.len()
}

// close releases every queued packet; later pushes are dropped.
func (q *tcpTunnelQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	for _, sq := range []*shaperQueue{&q.control, &q.priority, &q.bulk, &q.reliable} {
		for sq.len() > 0 {
			it := sq.pop()
			q.account(-int64(it.wire))
			it.pkt.Release(1)
		}
	}
}
