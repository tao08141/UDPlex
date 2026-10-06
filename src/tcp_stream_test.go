package main

import (
	"bytes"
	"crypto/rand"
	"io"
	mrand "math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTcpFrameRoundTrip(t *testing.T) {
	in := tcpFrame{typ: tcpFrameData, flags: tcpFlagFromClient | tcpFlagProbe, id: ConnIDFromUint64(42), seq: 1 << 40, ack: 7, wnd: 65536, payload: []byte("hello")}
	buf := make([]byte, 128)
	n := putTcpFrame(buf, &in)
	out, ok := parseTcpFrame(buf[:n])
	if !ok {
		t.Fatal("frame not parsed")
	}
	if out.typ != in.typ || out.flags != in.flags || out.id != in.id || out.seq != in.seq || out.ack != in.ack || out.wnd != in.wnd || !bytes.Equal(out.payload, in.payload) {
		t.Fatalf("got %+v, want %+v", out, in)
	}
	if _, ok := parseTcpFrame([]byte("not a stream frame at all, just some udp payload")); ok {
		t.Fatal("parsed a non-stream packet")
	}
}

// lossyPipe delivers packets to dest after a random delay, so they arrive
// reordered, and drops or duplicates some of them like a set of lines would.
type lossyPipe struct {
	BaseComponent
	dest   []string
	loss   float64
	dup    float64
	jitter time.Duration
	down   atomic.Bool // drop everything, like a failed line
	frames atomic.Int64
}

func newLossyPipe(tag string, r *Router, dest string, loss, dup float64, jitter time.Duration) *lossyPipe {
	return &lossyPipe{BaseComponent: NewBaseComponent(tag, r, 0), dest: []string{dest}, loss: loss, dup: dup, jitter: jitter}
}

func (p *lossyPipe) Start() error { return nil }
func (p *lossyPipe) Stop() error  { return nil }

func (p *lossyPipe) HandlePacket(packet *Packet) error {
	defer packet.Release(1)
	p.frames.Add(1)
	if p.down.Load() || mrand.Float64() < p.loss {
		return nil
	}
	copies := 1
	if mrand.Float64() < p.dup {
		copies = 2
	}
	data := append([]byte(nil), packet.GetData()...)
	for i := 0; i < copies; i++ {
		delay := time.Duration(0)
		if p.jitter > 0 {
			delay = time.Duration(mrand.Int64N(int64(p.jitter)))
		}
		time.AfterFunc(delay, func() {
			pkt := p.router.GetPacket(p.tag)
			pkt.SetLength(copy(pkt.BufAtOffset(), data))
			_ = p.router.Route(&pkt, p.dest)
			pkt.Release(1)
		})
	}
	return nil
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
				closeWrite(c)
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

type streamTestSetup struct {
	router     *Router
	listenAddr string
	ab, ba     *lossyPipe
	listen     *TcpListenComponent
	forward    *TcpForwardComponent
}

// newStreamTestSetup builds tcp_listen -> pipe -> tcp_forward -> pipe -> tcp_listen.
func newStreamTestSetup(t *testing.T, target string, loss, dup float64, jitter time.Duration, window int) *streamTestSetup {
	t.Helper()
	r := NewRouter(Config{BufferSize: 1500})
	s := &streamTestSetup{router: r, listenAddr: freeTCPAddr(t)}
	var err error
	s.listen, err = NewTcpListenComponent(ComponentConfig{Tag: "in", ListenAddr: s.listenAddr, Target: target, Detour: []string{"ab"}, Timeout: 10, WindowSize: window}, r)
	if err != nil {
		t.Fatal(err)
	}
	s.forward, err = NewTcpForwardComponent(ComponentConfig{Tag: "out", Detour: []string{"ba"}, Timeout: 10, WindowSize: window}, r)
	if err != nil {
		t.Fatal(err)
	}
	s.ab = newLossyPipe("ab", r, "out", loss, dup, jitter)
	s.ba = newLossyPipe("ba", r, "in", loss, dup, jitter)
	for _, c := range []Component{s.listen, s.forward, s.ab, s.ba} {
		if err := r.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.StartAll(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.StopAll)
	return s
}

// echoThrough sends size random bytes through addr and checks the echo.
func echoThrough(t *testing.T, addr string, size int, during func()) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	data := make([]byte, size)
	_, _ = rand.Read(data)
	writeErr := make(chan error, 1)
	go func() {
		_, err := conn.Write(data)
		closeWrite(conn)
		writeErr <- err
	}()
	if during != nil {
		go during()
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read after %d bytes: %v", len(got), err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("echo mismatch: got %d bytes, want %d", len(got), len(data))
	}
}

func waitStreamsClosed(t *testing.T, s *streamTestSetup) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.listen.ActiveStreams() != 0 || s.forward.ActiveStreams() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("streams still open: listen %d, forward %d", s.listen.ActiveStreams(), s.forward.ActiveStreams())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTcpStreamCleanPath(t *testing.T) {
	s := newStreamTestSetup(t, startEchoServer(t), 0, 0, 0, 0)
	echoThrough(t, s.listenAddr, 4<<20, nil)
	waitStreamsClosed(t, s)
}

func TestTcpStreamLossReorderDuplicate(t *testing.T) {
	s := newStreamTestSetup(t, startEchoServer(t), 0.02, 0.05, 3*time.Millisecond, 256<<10)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			echoThrough(t, s.listenAddr, 1<<20, nil)
		}()
	}
	wg.Wait()
	waitStreamsClosed(t, s)
}

func TestTcpStreamLineOutage(t *testing.T) {
	s := newStreamTestSetup(t, startEchoServer(t), 0, 0, time.Millisecond, 256<<10)
	start := time.Now()
	echoThrough(t, s.listenAddr, 4<<20, func() {
		for s.ab.frames.Load() < 300 {
			time.Sleep(time.Millisecond)
		}
		s.ab.down.Store(true)
		s.ba.down.Store(true)
		time.Sleep(500 * time.Millisecond)
		s.ab.down.Store(false)
		s.ba.down.Store(false)
	})
	if time.Since(start) < 500*time.Millisecond {
		t.Fatal("transfer finished before the outage ended")
	}
	waitStreamsClosed(t, s)
}

func TestTcpStreamWindowBoundsInFlight(t *testing.T) {
	// The target accepts but never reads: the client must stop sending
	// after about one window plus the kernel socket buffers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	hold := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			hold <- c
		}
	}()
	const window = 128 << 10
	s := newStreamTestSetup(t, ln.Addr().String(), 0, 0, 0, window)
	conn, err := net.Dial("tcp", s.listenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		_, _ = conn.Write(make([]byte, 64<<20))
	}()
	time.Sleep(time.Second)
	sent := s.ab.frames.Load()
	time.Sleep(500 * time.Millisecond)
	if more := s.ab.frames.Load() - sent; more > 20 {
		t.Fatalf("sender kept going with a full window: %d more frames", more)
	}
	if sent*int64(s.forward.streams.mss) > 32<<20 {
		t.Fatalf("sent %d frames, far more than the window allows", sent)
	}
	target := <-hold
	defer target.Close()
	// Draining the target opens the window again.
	go func() { _, _ = io.Copy(io.Discard, target) }()
	before := s.ab.frames.Load()
	time.Sleep(500 * time.Millisecond)
	if s.ab.frames.Load()-before < 100 {
		t.Fatal("sender did not resume after the window opened")
	}
}

func TestTcpStreamDialFailureResetsClient(t *testing.T) {
	s := newStreamTestSetup(t, freeTCPAddr(t), 0, 0, 0, 0)
	conn, err := net.Dial("tcp", s.listenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Read(make([]byte, 1))
	if err == nil || isTimeoutError(err) {
		t.Fatalf("want the connection reset or closed, got %v", err)
	}
	waitStreamsClosed(t, s)
}

// TestTcpStreamOverRedundantTunnels carries a stream over two real TCP tunnel
// lines, each frame sent on both, and cuts one line mid-transfer.
func TestTcpStreamOverRedundantTunnels(t *testing.T) {
	testTcpStreamOverTunnels(t, false)
}

// TestTcpStreamOverBalancedTunnels alternates the frames between two lines,
// so cutting one loses frames that must be retransmitted on the other.
func TestTcpStreamOverBalancedTunnels(t *testing.T) {
	testTcpStreamOverTunnels(t, true)
}

func testTcpStreamOverTunnels(t *testing.T, balance bool) {
	target := startEchoServer(t)
	r := NewRouter(Config{BufferSize: 1500})
	auth := &AuthConfig{Enabled: true, Secret: "s", EnableEncryption: true, HeartbeatInterval: 1}
	off := false
	tunnelAddr := freeTCPAddr(t)
	listenAddr := freeTCPAddr(t)
	comps := []Component{}
	detour := []string{"line_a", "line_b"}
	if balance {
		detour = []string{"lb"}
		lb, err := NewLoadBalancerComponent(LoadBalancerComponentConfig{Tag: "lb", WindowSize: 10, Detour: []LoadBalancerDetourRule{
			{Rule: "available_line_a && seq % 2 == 0", Targets: []string{"line_a"}},
			{Rule: "!available_line_a || seq % 2 == 1", Targets: []string{"line_b"}},
		}}, r)
		if err != nil {
			t.Fatal(err)
		}
		comps = append(comps, lb)
	}
	in, err := NewTcpListenComponent(ComponentConfig{Tag: "in", ListenAddr: listenAddr, Target: target, Detour: detour}, r)
	if err != nil {
		t.Fatal(err)
	}
	out, err := NewTcpForwardComponent(ComponentConfig{Tag: "out", Detour: []string{"server"}}, r)
	if err != nil {
		t.Fatal(err)
	}
	server := NewTcpTunnelListenComponent(ComponentConfig{Tag: "server", ListenAddr: tunnelAddr, Detour: []string{"out"}, Auth: auth, BroadcastMode: &off}, r)
	lineA := NewTcpTunnelForwardComponent(ComponentConfig{Tag: "line_a", Forwarders: []string{tunnelAddr + ":2"}, Detour: []string{"in"}, Auth: auth, BroadcastMode: &off, ConnectionCheckTime: 1}, r)
	lineB := NewTcpTunnelForwardComponent(ComponentConfig{Tag: "line_b", Forwarders: []string{tunnelAddr + ":2"}, Detour: []string{"in"}, Auth: auth, BroadcastMode: &off, ConnectionCheckTime: 1}, r)
	comps = append(comps, in, out, server, lineA, lineB)
	for _, c := range comps {
		if err := r.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.StartAll(); err != nil {
		t.Fatal(err)
	}
	defer r.StopAll()
	deadline := time.Now().Add(5 * time.Second)
	for !lineA.IsAvailable() || !lineB.IsAvailable() {
		if time.Now().After(deadline) {
			t.Fatal("tunnel lines did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	echoThrough(t, listenAddr, 8<<20, func() {
		for out.ActiveStreams() == 0 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(10 * time.Millisecond)
		for _, pool := range lineA.pools {
			for _, c := range *pool.conns.Load() {
				c.Close()
			}
		}
	})
	deadline = time.Now().Add(10 * time.Second)
	for in.ActiveStreams() != 0 || out.ActiveStreams() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("streams still open: %d, %d", in.ActiveStreams(), out.ActiveStreams())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTcpListenDirectRelay(t *testing.T) {
	target := startEchoServer(t)
	r := NewRouter(Config{})
	addr := freeTCPAddr(t)
	l, err := NewTcpListenComponent(ComponentConfig{Tag: "direct", ListenAddr: addr, Forwarders: []string{freeTCPAddr(t), target}}, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.PostStart(); err != nil {
		t.Fatal(err)
	}
	defer l.Stop()
	echoThrough(t, addr, 1<<20, nil)
}
