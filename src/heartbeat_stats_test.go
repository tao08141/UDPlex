package main

import "testing"

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
