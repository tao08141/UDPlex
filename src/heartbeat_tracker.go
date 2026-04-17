package main

import (
	"sync/atomic"
	"time"
)

const (
	heartbeatStatsWindow5m    = 5 * time.Minute
	heartbeatStatsWindow1h    = time.Hour
	heartbeatStatsWindow24h   = 24 * time.Hour
	heartbeatStatsBucketCount = int(heartbeatStatsWindow24h / time.Minute)
	heartbeatBucketCountBits  = 32
	heartbeatBucketCountMask  = uint64((1 << heartbeatBucketCountBits) - 1)
)

type heartbeatWindowStats struct {
	Sent     uint64
	Lost     uint64
	LossRate float64
}

type heartbeatStatsSnapshot struct {
	Last5m  heartbeatWindowStats
	Last1h  heartbeatWindowStats
	Last24h heartbeatWindowStats
}

type heartbeatAtomicBucket struct {
	value atomic.Uint64
}

type heartbeatStatsTracker struct {
	lastSentUnixNano atomic.Int64
	sentBuckets      [heartbeatStatsBucketCount]heartbeatAtomicBucket
	lostBuckets      [heartbeatStatsBucketCount]heartbeatAtomicBucket
}

func packHeartbeatBucket(minute uint64, count uint32) uint64 {
	return (minute << heartbeatBucketCountBits) | uint64(count)
}

func unpackHeartbeatBucket(value uint64) (minute uint64, count uint32) {
	return value >> heartbeatBucketCountBits, uint32(value & heartbeatBucketCountMask)
}

func newHeartbeatWindowStats(sent uint64, lost uint64) heartbeatWindowStats {
	stats := heartbeatWindowStats{Sent: sent, Lost: lost}
	if sent > 0 {
		stats.LossRate = float64(lost) * 100 / float64(sent)
	}
	return stats
}

func minuteIndex(minute uint64) int {
	return int(minute % uint64(heartbeatStatsBucketCount))
}

func heartbeatMinute(ts time.Time) uint64 {
	return uint64(ts.Unix() / 60)
}

func (h *heartbeatStatsTracker) incrementBucket(buckets *[heartbeatStatsBucketCount]heartbeatAtomicBucket, minute uint64) {
	bucket := &buckets[minuteIndex(minute)]
	for {
		current := bucket.value.Load()
		currentMinute, currentCount := unpackHeartbeatBucket(current)

		var next uint64
		if currentMinute == minute {
			if currentCount == ^uint32(0) {
				return
			}
			next = packHeartbeatBucket(minute, currentCount+1)
		} else {
			next = packHeartbeatBucket(minute, 1)
		}

		if bucket.value.CompareAndSwap(current, next) {
			return
		}
	}
}

func (h *heartbeatStatsTracker) countBucketsInRange(buckets *[heartbeatStatsBucketCount]heartbeatAtomicBucket, cutoffMinute uint64, nowMinute uint64) uint64 {
	var total uint64
	for index := range buckets {
		bucketMinute, bucketCount := unpackHeartbeatBucket(buckets[index].value.Load())
		if bucketMinute >= cutoffMinute && bucketMinute <= nowMinute {
			total += uint64(bucketCount)
		}
	}
	return total
}

func (h *heartbeatStatsTracker) noteHeartbeatSentAt(now time.Time) {
	unixNano := now.UnixNano()
	h.lastSentUnixNano.Store(unixNano)
	h.incrementBucket(&h.sentBuckets, heartbeatMinute(now))
}

func (h *heartbeatStatsTracker) noteHeartbeatLostAt(now time.Time) {
	h.incrementBucket(&h.lostBuckets, heartbeatMinute(now))
}

func (h *heartbeatStatsTracker) Snapshot() heartbeatStatsSnapshot {
	return h.snapshotAt(time.Now())
}

func (h *heartbeatStatsTracker) snapshotAt(now time.Time) heartbeatStatsSnapshot {
	nowMinute := heartbeatMinute(now)
	cutoff24h := heartbeatMinute(now.Add(-heartbeatStatsWindow24h))
	cutoff1h := heartbeatMinute(now.Add(-heartbeatStatsWindow1h))
	cutoff5m := heartbeatMinute(now.Add(-heartbeatStatsWindow5m))

	return heartbeatStatsSnapshot{
		Last5m: newHeartbeatWindowStats(
			h.countBucketsInRange(&h.sentBuckets, cutoff5m, nowMinute),
			h.countBucketsInRange(&h.lostBuckets, cutoff5m, nowMinute),
		),
		Last1h: newHeartbeatWindowStats(
			h.countBucketsInRange(&h.sentBuckets, cutoff1h, nowMinute),
			h.countBucketsInRange(&h.lostBuckets, cutoff1h, nowMinute),
		),
		Last24h: newHeartbeatWindowStats(
			h.countBucketsInRange(&h.sentBuckets, cutoff24h, nowMinute),
			h.countBucketsInRange(&h.lostBuckets, cutoff24h, nowMinute),
		),
	}
}

func (h *heartbeatStatsTracker) LastHeartbeatSent() time.Time {
	unixNano := h.lastSentUnixNano.Load()
	if unixNano <= 0 {
		return time.Time{}
	}
	return time.Unix(0, unixNano)
}

type heartbeatTracker struct {
	componentStats   *heartbeatStatsTracker
	lastSentUnixNano atomic.Int64
	sentCount        atomic.Uint64
	lostCount        atomic.Uint64
	consecutiveLoss  atomic.Int32
	awaitingAck      atomic.Int32
}

func (h *heartbeatTracker) SetHeartbeatStatsTracker(stats *heartbeatStatsTracker) {
	h.componentStats = stats
}

func (h *heartbeatTracker) NoteHeartbeatSent() {
	now := time.Now()
	h.lastSentUnixNano.Store(now.UnixNano())
	h.sentCount.Add(1)
	h.awaitingAck.Store(1)
	if h.componentStats != nil {
		h.componentStats.noteHeartbeatSentAt(now)
	}
}

func (h *heartbeatTracker) MarkHeartbeatResponse() {
	h.awaitingAck.Store(0)
	h.consecutiveLoss.Store(0)
}

func (h *heartbeatTracker) MarkPendingHeartbeatLost() int32 {
	if h.awaitingAck.CompareAndSwap(1, 0) {
		h.lostCount.Add(1)
		if h.componentStats != nil {
			h.componentStats.noteHeartbeatLostAt(time.Now())
		}
		return h.consecutiveLoss.Add(1)
	}
	return h.consecutiveLoss.Load()
}

func (h *heartbeatTracker) ResetHeartbeatState() {
	h.lastSentUnixNano.Store(0)
	h.consecutiveLoss.Store(0)
	h.awaitingAck.Store(0)
}

func (h *heartbeatTracker) ResetHeartbeatStats() {
	h.ResetHeartbeatState()
	h.sentCount.Store(0)
	h.lostCount.Store(0)
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
