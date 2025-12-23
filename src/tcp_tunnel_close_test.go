package main

import (
	"net"
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
	tag  string
	stop chan struct{}
	r    *Router
	a    *AuthManager
}

func (t *testTcpTunnelComponent) GetDetour() []string           { return nil }
func (t *testTcpTunnelComponent) GetRouter() *Router            { return t.r }
func (t *testTcpTunnelComponent) GetTag() string                { return t.tag }
func (t *testTcpTunnelComponent) GetStopChannel() chan struct{} { return t.stop }
func (t *testTcpTunnelComponent) GetAuthManager() *AuthManager  { return t.a }
func (t *testTcpTunnelComponent) Disconnect(c *TcpTunnelConn)   {}
func (t *testTcpTunnelComponent) GetSendTimeout() time.Duration { return 0 }
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

	conn := NewTcpTunnelConn(c1, ForwardID{}, PoolID{}, comp, 16, TcpTunnelListenMode)

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
