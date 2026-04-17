package main

import (
	"math"
	"testing"
	"time"
)

func TestForwardConnHeartbeatStats(t *testing.T) {
	conn := &ForwardConn{}

	conn.NoteHeartbeatSent()
	conn.MarkPendingHeartbeatLost()
	conn.NoteHeartbeatSent()
	conn.MarkHeartbeatResponse()

	sent, lost, rate := conn.HeartbeatStats()
	if sent != 2 {
		t.Fatalf("unexpected sent count: %d", sent)
	}
	if lost != 1 {
		t.Fatalf("unexpected lost count: %d", lost)
	}
	if rate != 50 {
		t.Fatalf("unexpected loss rate: %v", rate)
	}
}

func TestListenConnHeartbeatStats(t *testing.T) {
	conn := &ListenConn{}

	conn.NoteHeartbeatSent()
	conn.MarkPendingHeartbeatLost()
	conn.NoteHeartbeatSent()
	conn.MarkHeartbeatResponse()

	sent, lost, rate := conn.HeartbeatStats()
	if sent != 2 || lost != 1 || rate != 50 {
		t.Fatalf("unexpected heartbeat stats: sent=%d lost=%d rate=%v", sent, lost, rate)
	}
}

func TestTcpTunnelConnHeartbeatStats(t *testing.T) {
	conn := &TcpTunnelConn{}

	conn.NoteHeartbeatSent()
	conn.MarkPendingHeartbeatLost()
	conn.NoteHeartbeatSent()
	conn.MarkHeartbeatResponse()

	sent, lost, rate := conn.HeartbeatStats()
	if sent != 2 || lost != 1 || rate != 50 {
		t.Fatalf("unexpected heartbeat stats: sent=%d lost=%d rate=%v", sent, lost, rate)
	}
}

func TestHeartbeatStatsTrackerWindows(t *testing.T) {
	var tracker heartbeatStatsTracker
	now := time.Date(2026, time.April, 17, 12, 0, 0, 0, time.UTC)

	tracker.noteHeartbeatSentAt(now.Add(-25 * time.Hour))
	tracker.noteHeartbeatLostAt(now.Add(-25 * time.Hour))
	tracker.noteHeartbeatSentAt(now.Add(-2 * time.Hour))
	tracker.noteHeartbeatLostAt(now.Add(-2 * time.Hour))
	tracker.noteHeartbeatSentAt(now.Add(-30 * time.Minute))
	tracker.noteHeartbeatLostAt(now.Add(-30 * time.Minute))
	tracker.noteHeartbeatSentAt(now.Add(-4 * time.Minute))
	tracker.noteHeartbeatSentAt(now.Add(-2 * time.Minute))
	tracker.noteHeartbeatLostAt(now.Add(-2 * time.Minute))

	snapshot := tracker.snapshotAt(now)

	if snapshot.Last5m.Sent != 2 || snapshot.Last5m.Lost != 1 {
		t.Fatalf("unexpected 5m stats: %+v", snapshot.Last5m)
	}
	if math.Abs(snapshot.Last5m.LossRate-50) > 0.0001 {
		t.Fatalf("unexpected 5m loss rate: %v", snapshot.Last5m.LossRate)
	}

	if snapshot.Last1h.Sent != 3 || snapshot.Last1h.Lost != 2 {
		t.Fatalf("unexpected 1h stats: %+v", snapshot.Last1h)
	}
	if math.Abs(snapshot.Last1h.LossRate-66.6666666667) > 0.0001 {
		t.Fatalf("unexpected 1h loss rate: %v", snapshot.Last1h.LossRate)
	}

	if snapshot.Last24h.Sent != 4 || snapshot.Last24h.Lost != 3 {
		t.Fatalf("unexpected 24h stats: %+v", snapshot.Last24h)
	}
	if math.Abs(snapshot.Last24h.LossRate-75) > 0.0001 {
		t.Fatalf("unexpected 24h loss rate: %v", snapshot.Last24h.LossRate)
	}
}

func TestHeartbeatTrackerAggregatesAtComponentLevel(t *testing.T) {
	var componentTracker heartbeatStatsTracker
	var first heartbeatTracker
	first.SetHeartbeatStatsTracker(&componentTracker)

	first.NoteHeartbeatSent()
	first.MarkPendingHeartbeatLost()
	first.ResetHeartbeatState()

	var second heartbeatTracker
	second.SetHeartbeatStatsTracker(&componentTracker)
	second.NoteHeartbeatSent()
	second.MarkHeartbeatResponse()

	snapshot := componentTracker.Snapshot()
	if snapshot.Last24h.Sent != 2 || snapshot.Last24h.Lost != 1 {
		t.Fatalf("unexpected component-level stats: %+v", snapshot.Last24h)
	}
	if math.Abs(snapshot.Last24h.LossRate-50) > 0.0001 {
		t.Fatalf("unexpected component-level loss rate: %v", snapshot.Last24h.LossRate)
	}
}

func TestHeartbeatStatsTrackerReusesBucketWithoutLeakingOldCounts(t *testing.T) {
	var tracker heartbeatStatsTracker
	now := time.Date(2026, time.April, 17, 12, 0, 0, 0, time.UTC)

	tracker.noteHeartbeatSentAt(now.Add(-25 * time.Hour))
	tracker.noteHeartbeatLostAt(now.Add(-25 * time.Hour))
	tracker.noteHeartbeatSentAt(now.Add(-time.Hour))
	tracker.noteHeartbeatLostAt(now.Add(-time.Hour))

	snapshot := tracker.snapshotAt(now)
	if snapshot.Last24h.Sent != 1 || snapshot.Last24h.Lost != 1 {
		t.Fatalf("unexpected recycled bucket stats: %+v", snapshot.Last24h)
	}
}
