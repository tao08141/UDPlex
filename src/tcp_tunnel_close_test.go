package main

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	// Ensure global logger is available in tests; tcp tunnel goroutines log.
	if logger == nil {
		_ = initTestLogger()
	}
}

func initTestLogger() error {
	initLogger(LoggingConfig{Level: "error", Format: "console", OutputPath: "stdout"})
	return nil
}

type testTcpTunnelComponent struct {
	tag         string
	stop        chan struct{}
	r           *Router
	a           *AuthManager
	sendTimeout time.Duration
	disconnects atomic.Int32
}

func (t *testTcpTunnelComponent) GetDetour() []string           { return nil }
func (t *testTcpTunnelComponent) GetRouter() *Router            { return t.r }
func (t *testTcpTunnelComponent) GetTag() string                { return t.tag }
func (t *testTcpTunnelComponent) GetStopChannel() chan struct{} { return t.stop }
func (t *testTcpTunnelComponent) GetAuthManager() *AuthManager  { return t.a }
func (t *testTcpTunnelComponent) Disconnect(c *TcpTunnelConn)   { t.disconnects.Add(1) }
func (t *testTcpTunnelComponent) GetSendTimeout() time.Duration { return t.sendTimeout }
func (t *testTcpTunnelComponent) HandleAuthenticatedConnection(c *TcpTunnelConn) error {
	return nil
}

func TestTcpTunnelConn_CloseDoesNotDeadlock(t *testing.T) {
	// net.Pipe gives us an in-memory full-duplex net.Conn pair.
	c1, c2 := net.Pipe()
	defer c2.Close()

	// Minimal router/auth setup: we only need enough so readLoop can run.
	router := NewRouter(Config{BufferSize: 64 * 1024, BufferOffset: 64, QueueSize: 16})
	auth, err := NewAuthManager(&AuthConfig{Enabled: true, Secret: "test", AuthTimeout: 1, DelayWindowSize: 1}, router)
	if err != nil {
		t.Fatalf("NewAuthManager failed: %v", err)
	}

	comp := &testTcpTunnelComponent{tag: "test", stop: make(chan struct{}), r: router, a: auth}

	conn := NewTcpTunnelConn(c1, ForwardID{}, PoolID{}, comp, 16, true, 64, TcpTunnelListenMode)

	// Close immediately; prior implementation could deadlock because readLoop deferred Close()
	// and Close waited for the readLoop goroutine.
	done := make(chan struct{})
	go func() {
		conn.Close()
		conn.Wait()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatalf("Close/Wait timed out (possible goroutine deadlock)")
	}
}

type blockingTestConn struct {
	writeStarted chan struct{}
	releaseWrite chan struct{}
	closed       chan struct{}
	closeOnce    sync.Once
	writeCalls   atomic.Int32
}

func newBlockingTestConn() *blockingTestConn {
	return &blockingTestConn{
		writeStarted: make(chan struct{}, 8),
		releaseWrite: make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

func (c *blockingTestConn) Read(_ []byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *blockingTestConn) Write(b []byte) (int, error) {
	c.writeCalls.Add(1)
	select {
	case c.writeStarted <- struct{}{}:
	default:
	}

	select {
	case <-c.releaseWrite:
		return len(b), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *blockingTestConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		close(c.releaseWrite)
	})
	return nil
}

func (c *blockingTestConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *blockingTestConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *blockingTestConn) SetDeadline(time.Time) error      { return nil }
func (c *blockingTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *blockingTestConn) SetWriteDeadline(time.Time) error { return nil }

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }

type timeoutTestError struct{}

func (timeoutTestError) Error() string   { return "i/o timeout" }
func (timeoutTestError) Timeout() bool   { return true }
func (timeoutTestError) Temporary() bool { return true }

type scriptedWriteResult struct {
	n   int
	err error
}

type scriptedWriteConn struct {
	results      []scriptedWriteResult
	writeStarted chan struct{}
	closed       chan struct{}
	closeOnce    sync.Once
	mu           sync.Mutex
}

func newScriptedWriteConn(results ...scriptedWriteResult) *scriptedWriteConn {
	return &scriptedWriteConn{
		results:      append([]scriptedWriteResult(nil), results...),
		writeStarted: make(chan struct{}, len(results)+4),
		closed:       make(chan struct{}),
	}
}

func (c *scriptedWriteConn) Read(_ []byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *scriptedWriteConn) Write(b []byte) (int, error) {
	select {
	case c.writeStarted <- struct{}{}:
	default:
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.results) == 0 {
		return len(b), nil
	}

	result := c.results[0]
	c.results = c.results[1:]
	if result.n > len(b) {
		result.n = len(b)
	}
	return result.n, result.err
}

func (c *scriptedWriteConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
	})
	return nil
}

func (c *scriptedWriteConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *scriptedWriteConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *scriptedWriteConn) SetDeadline(time.Time) error      { return nil }
func (c *scriptedWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptedWriteConn) SetWriteDeadline(time.Time) error { return nil }

func waitForWriteStarts(t *testing.T, ch <-chan struct{}, count int) {
	t.Helper()

	for i := 0; i < count; i++ {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for write start %d/%d", i+1, count)
		}
	}
}

func waitForCondition(t *testing.T, predicate func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("condition not met before timeout")
}

func TestTcpTunnelConn_WriteBatchToggle(t *testing.T) {
	testCases := []struct {
		name             string
		enableWriteBatch bool
		wantQueued       int
	}{
		{name: "batch enabled drains queued packets", enableWriteBatch: true, wantQueued: 0},
		{name: "batch disabled leaves next packet queued", enableWriteBatch: false, wantQueued: 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter(Config{BufferSize: 64 * 1024, BufferOffset: 64, QueueSize: 16})
			auth, err := NewAuthManager(&AuthConfig{Enabled: true, Secret: "test", AuthTimeout: 1, DelayWindowSize: 1}, router)
			if err != nil {
				t.Fatalf("NewAuthManager failed: %v", err)
			}

			comp := &testTcpTunnelComponent{tag: "test", stop: make(chan struct{}), r: router, a: auth}
			mockConn := newBlockingTestConn()
			conn := NewTcpTunnelConn(mockConn, ForwardID{}, PoolID{}, comp, 16, tc.enableWriteBatch, 64, TcpTunnelListenMode)

			pkt1 := router.GetPacket("test")
			pkt1.SetLength(copy(pkt1.BufAtOffset(), []byte("one")))
			defer pkt1.Release(1)

			pkt2 := router.GetPacket("test")
			pkt2.SetLength(copy(pkt2.BufAtOffset(), []byte("two")))
			defer pkt2.Release(1)

			if err := conn.Write(&pkt1); err != nil {
				t.Fatalf("first write failed: %v", err)
			}
			if err := conn.Write(&pkt2); err != nil {
				t.Fatalf("second write failed: %v", err)
			}

			select {
			case <-mockConn.writeStarted:
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out waiting for writer to start")
			}

			if got := len(conn.writeQueue); got != tc.wantQueued {
				t.Fatalf("unexpected queued packet count: got %d want %d", got, tc.wantQueued)
			}

			conn.Close()
			conn.Wait()
		})
	}
}

func TestTcpTunnelConn_WriteTimeoutWithProgressKeepsConnection(t *testing.T) {
	router := NewRouter(Config{BufferSize: 64 * 1024, BufferOffset: 64, QueueSize: 16})
	auth, err := NewAuthManager(&AuthConfig{Enabled: true, Secret: "test", AuthTimeout: 1, DelayWindowSize: 1}, router)
	if err != nil {
		t.Fatalf("NewAuthManager failed: %v", err)
	}

	comp := &testTcpTunnelComponent{tag: "test", stop: make(chan struct{}), r: router, a: auth, sendTimeout: 5 * time.Millisecond}
	mockConn := newScriptedWriteConn(
		scriptedWriteResult{n: 1, err: timeoutTestError{}},
		scriptedWriteResult{n: 2, err: nil},
		scriptedWriteResult{n: 3, err: nil},
	)
	conn := NewTcpTunnelConn(mockConn, ForwardID{}, PoolID{}, comp, 16, true, 64, TcpTunnelListenMode)

	pkt1 := router.GetPacket("test")
	pkt1.SetLength(copy(pkt1.BufAtOffset(), []byte("one")))
	defer pkt1.Release(1)

	pkt2 := router.GetPacket("test")
	pkt2.SetLength(copy(pkt2.BufAtOffset(), []byte("two")))
	defer pkt2.Release(1)

	if err := conn.Write(&pkt1); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	if err := conn.Write(&pkt2); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	waitForWriteStarts(t, mockConn.writeStarted, 3)
	waitForCondition(t, func() bool {
		return comp.disconnects.Load() == 0 && len(conn.writeQueue) == 0
	})

	select {
	case <-conn.closed:
		t.Fatalf("connection should stay open while writes keep making progress")
	default:
	}

	conn.Close()
	conn.Wait()
}

func TestTcpTunnelConn_WriteTimeoutWithoutProgressDisconnects(t *testing.T) {
	router := NewRouter(Config{BufferSize: 64 * 1024, BufferOffset: 64, QueueSize: 16})
	auth, err := NewAuthManager(&AuthConfig{Enabled: true, Secret: "test", AuthTimeout: 1, DelayWindowSize: 1}, router)
	if err != nil {
		t.Fatalf("NewAuthManager failed: %v", err)
	}

	comp := &testTcpTunnelComponent{tag: "test", stop: make(chan struct{}), r: router, a: auth, sendTimeout: 5 * time.Millisecond}
	mockConn := newScriptedWriteConn(
		scriptedWriteResult{n: 0, err: timeoutTestError{}},
	)
	conn := NewTcpTunnelConn(mockConn, ForwardID{}, PoolID{}, comp, 16, false, 64, TcpTunnelListenMode)

	pkt := router.GetPacket("test")
	pkt.SetLength(copy(pkt.BufAtOffset(), []byte("one")))
	defer pkt.Release(1)

	if err := conn.Write(&pkt); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	waitForWriteStarts(t, mockConn.writeStarted, 1)
	waitForCondition(t, func() bool {
		return comp.disconnects.Load() > 0
	})

	conn.Close()
	conn.Wait()
}
