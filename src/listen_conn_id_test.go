package main

import (
	"net"
	"sync"
	"testing"
	"time"
)

type connIDRecorder struct {
	BaseComponent
	mu  sync.Mutex
	got []ConnID
}

func (c *connIDRecorder) Start() error { return nil }
func (c *connIDRecorder) Stop() error  { return nil }
func (c *connIDRecorder) HandlePacket(p *Packet) error {
	c.mu.Lock()
	c.got = append(c.got, p.ConnID())
	c.mu.Unlock()
	p.Release(1)
	return nil
}

func (c *connIDRecorder) snapshot() []ConnID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ConnID(nil), c.got...)
}

func distinctConnIDs(ids []ConnID) int {
	seen := map[ConnID]struct{}{}
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	return len(seen)
}

// TestListenPreserveConnID authenticates to a listener like a forward line
// and sends data with two connection IDs. By default the listener replaces
// them with the line's own ID; with preserve_conn_id they arrive as sent.
func TestListenPreserveConnID(t *testing.T) {
	sent := []ConnID{ConnIDFromUint64(0x1111), ConnIDFromUint64(0x2222)}
	for _, preserve := range []bool{false, true} {
		r := NewRouter(Config{BufferSize: 2048, QueueSize: 1024})
		auth := &AuthConfig{Enabled: true, Secret: "conn-id-test"}
		addr := freeUDPAddr(t)
		sink := &connIDRecorder{BaseComponent: NewBaseComponent("sink", r, 0)}
		listen := NewListenComponent(ComponentConfig{
			Tag:            "line_in",
			ListenAddr:     addr,
			Detour:         []string{"sink"},
			Auth:           auth,
			PreserveConnID: preserve,
		}, r)
		for _, c := range []Component{sink, listen} {
			if err := r.Register(c); err != nil {
				t.Fatal(err)
			}
		}
		if err := listen.Start(); err != nil {
			t.Fatal(err)
		}

		client, err := NewAuthManager(auth, r)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("udp", addr)
		if err != nil {
			t.Fatal(err)
		}
		buf := r.GetBuffer()
		n, err := client.CreateAuthChallenge(buf, MsgTypeAuthChallenge, ForwardID{1}, PoolID{1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(buf[:n]); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("no auth response: %v", err)
		}
		r.PutBuffer(buf)

		for _, id := range sent {
			p := r.GetPacket("client")
			p.SetLength(64)
			p.SetConnID(id)
			if err := client.WrapData(&p); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write(p.GetData()); err != nil {
				t.Fatal(err)
			}
			p.Release(1)
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(sink.snapshot()) < len(sent) {
			if time.Now().After(deadline) {
				t.Fatalf("preserve=%v: received %v", preserve, sink.snapshot())
			}
			time.Sleep(10 * time.Millisecond)
		}
		got := sink.snapshot()
		conn.Close()
		client.Stop()
		listen.Stop()

		switch {
		case preserve && (got[0] != sent[0] || got[1] != sent[1]):
			t.Fatalf("preserve: got %v, want %v", got, sent)
		case !preserve && (distinctConnIDs(got) != 1 || got[0] == sent[0] || got[0] == sent[1]):
			t.Fatalf("default: got %v, want the line's own ID for both", got)
		}
	}
}
