package main

import (
	"sync/atomic"
	"time"
)

type heartbeatTracker struct {
	lastSentUnixNano atomic.Int64
	sentCount        atomic.Uint64
	lostCount        atomic.Uint64
	consecutiveLoss  atomic.Int32
	awaitingAck      atomic.Int32
}

func (h *heartbeatTracker) NoteHeartbeatSent() {
	h.lastSentUnixNano.Store(time.Now().UnixNano())
	h.sentCount.Add(1)
	h.awaitingAck.Store(1)
}

func (h *heartbeatTracker) MarkHeartbeatResponse() {
	h.awaitingAck.Store(0)
	h.consecutiveLoss.Store(0)
}

func (h *heartbeatTracker) MarkPendingHeartbeatLost() int32 {
	if h.awaitingAck.CompareAndSwap(1, 0) {
		h.lostCount.Add(1)
		return h.consecutiveLoss.Add(1)
	}
	return h.consecutiveLoss.Load()
}

func (h *heartbeatTracker) ResetHeartbeatStats() {
	h.lastSentUnixNano.Store(0)
	h.sentCount.Store(0)
	h.lostCount.Store(0)
	h.consecutiveLoss.Store(0)
	h.awaitingAck.Store(0)
}

func (h *heartbeatTracker) HeartbeatStats() (sent uint64, lost uint64, lossRate float64) {
	sent = h.sentCount.Load()
	lost = h.lostCount.Load()
	if sent > 0 {
		lossRate = float64(lost) * 100 / float64(sent)
	}
	return
}

func (h *heartbeatTracker) LastHeartbeatSent() time.Time {
	unixNano := h.lastSentUnixNano.Load()
	if unixNano <= 0 {
		return time.Time{}
	}
	return time.Unix(0, unixNano)
}

func (h *heartbeatTracker) ClearLastHeartbeatSent() {
	h.lastSentUnixNano.Store(0)
}
