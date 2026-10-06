package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TCP forwarding integration tests (tcp_listen / tcp_forward).
//
// A target server runs behind machine B; a client connects to machine A's
// tcp_listen. Every connection starts with a mode byte:
//   'E' echo: the target echoes everything back.
//   'S' sink: the target counts the bytes and replies with the count at EOF.
// The integrity test streams pseudo-random data on several echo connections
// and checks every byte coming back while a line fails; the performance test
// measures bulk throughput on a sink connection and the round-trip time of
// small echo requests on the side.

const (
	tcpModeEcho = 'E'
	tcpModeSink = 'S'

	tcpTargetPort       = 5301
	tcpEntryPort        = 8080
	tcpIntegrityConns   = 4
	tcpChunkSize        = 16 << 10
	tcpPingSize         = 64
	tcpPingInterval     = 10 * time.Millisecond
	tcpOutageStart      = 1500 * time.Millisecond
	tcpOutageDuration   = 2 * time.Second
	tcpIdleDrainTimeout = 30 * time.Second

	tcpLineAClientIP = "172.31.1.1"
	tcpLineAServerIP = "172.31.1.2"
	tcpLineBClientIP = "172.31.2.1"
	tcpLineBServerIP = "172.31.2.2"
	tcpNetemDelay    = "10ms"
)

type TCPForwardMetrics struct {
	Conns         int      `json:"conns"`
	Chunks        int64    `json:"chunks"`
	ChunksBack    int64    `json:"chunks_back"`
	BytesSent     int64    `json:"bytes_sent"`
	BytesReceived int64    `json:"bytes_received"`
	Mismatches    int64    `json:"mismatches"`
	Pings         int64    `json:"pings"`
	Pongs         int64    `json:"pongs"`
	Mbps          float64  `json:"mbps"`
	AvgLatencyMs  float64  `json:"avg_latency_ms"`
	P50LatencyMs  float64  `json:"p50_latency_ms"`
	P95LatencyMs  float64  `json:"p95_latency_ms"`
	P99LatencyMs  float64  `json:"p99_latency_ms"`
	MinLatencyMs  float64  `json:"min_latency_ms"`
	MaxLatencyMs  float64  `json:"max_latency_ms"`
	Errors        []string `json:"errors,omitempty"`
}

func handleTCPHelperCommand() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "-tcp-target-server":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "missing listen address for -tcp-target-server")
			os.Exit(2)
		}
		if err := runTCPTargetServer(os.Args[2], func() { fmt.Println("TCP target server ready") }); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	case "-tcp-client":
		target, mode := "", "integrity"
		duration := TEST_DURATION
		for i := 2; i+1 < len(os.Args); i += 2 {
			switch os.Args[i] {
			case "-target":
				target = os.Args[i+1]
			case "-mode":
				mode = os.Args[i+1]
			case "-duration-ms":
				if ms, err := strconv.Atoi(os.Args[i+1]); err == nil && ms > 0 {
					duration = time.Duration(ms) * time.Millisecond
				}
			}
		}
		var metrics *TCPForwardMetrics
		if mode == "perf" {
			metrics = runTCPPerfClient(target, duration)
		} else {
			metrics = runTCPIntegrityClient(target, tcpIntegrityConns, duration)
		}
		if err := json.NewEncoder(os.Stdout).Encode(metrics); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	}
	return false
}

// runTCPTargetServer serves echo and sink connections until the listener fails.
func runTCPTargetServer(listenAddr string, ready func()) error {
	var ln net.Listener
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		ln, err = net.Listen("tcp", listenAddr)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("failed to bind TCP target server on %s: %w", listenAddr, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer ln.Close()
	if ready != nil {
		ready()
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go serveTCPTargetConn(conn)
	}
}

func serveTCPTargetConn(conn net.Conn) {
	defer conn.Close()
	var mode [1]byte
	if _, err := io.ReadFull(conn, mode[:]); err != nil {
		return
	}
	switch mode[0] {
	case tcpModeEcho:
		_, _ = io.Copy(conn, conn)
		closeTCPWrite(conn)
	case tcpModeSink:
		n, _ := io.Copy(io.Discard, conn)
		var reply [8]byte
		binary.BigEndian.PutUint64(reply[:], uint64(n))
		_, _ = conn.Write(reply[:])
		closeTCPWrite(conn)
	}
	_, _ = io.Copy(io.Discard, conn)
}

func closeTCPWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// dialTCPEntry dials the tcp_listen entry, retrying while it starts up.
func dialTCPEntry(target string, mode byte) (net.Conn, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err == nil {
			if _, err = conn.Write([]byte{mode}); err == nil {
				return conn, nil
			}
			conn.Close()
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func newTCPStreamGenerator(seed uint64) *mrand.ChaCha8 {
	var s [32]byte
	binary.BigEndian.PutUint64(s[:], seed)
	return mrand.NewChaCha8(s)
}

// runTCPIntegrityClient streams pseudo-random data on conns echo connections
// for duration and verifies every byte that comes back.
func runTCPIntegrityClient(target string, conns int, duration time.Duration) *TCPForwardMetrics {
	m := &TCPForwardMetrics{Conns: conns}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var chunks, chunksBack, sent, received, mismatches atomic.Int64
	addErr := func(err error) {
		mu.Lock()
		m.Errors = append(m.Errors, err.Error())
		mu.Unlock()
	}
	start := time.Now()
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			conn, err := dialTCPEntry(target, tcpModeEcho)
			if err != nil {
				addErr(fmt.Errorf("conn %d: dial: %w", seed, err))
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(duration + tcpIdleDrainTimeout))

			var connSent atomic.Int64
			writeDone := make(chan error, 1)
			go func() {
				gen := newTCPStreamGenerator(seed)
				buf := make([]byte, tcpChunkSize)
				for time.Since(start) < duration {
					_, _ = gen.Read(buf)
					n, err := conn.Write(buf)
					connSent.Add(int64(n))
					sent.Add(int64(n))
					if err != nil {
						writeDone <- err
						return
					}
					chunks.Add(1)
				}
				closeTCPWrite(conn)
				writeDone <- nil
			}()

			gen := newTCPStreamGenerator(seed)
			buf := make([]byte, tcpChunkSize)
			want := make([]byte, tcpChunkSize)
			var got, sinceChunk int64
			for {
				n, err := conn.Read(buf)
				if n > 0 {
					_, _ = gen.Read(want[:n])
					if string(buf[:n]) != string(want[:n]) {
						mismatches.Add(1)
						addErr(fmt.Errorf("conn %d: data mismatch at offset %d", seed, got))
						return
					}
					got += int64(n)
					received.Add(int64(n))
					sinceChunk += int64(n)
					for sinceChunk >= tcpChunkSize {
						sinceChunk -= tcpChunkSize
						chunksBack.Add(1)
					}
				}
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					addErr(fmt.Errorf("conn %d: read after %d bytes: %w", seed, got, err))
					return
				}
			}
			if err := <-writeDone; err != nil {
				addErr(fmt.Errorf("conn %d: write: %w", seed, err))
				return
			}
			if got != connSent.Load() {
				addErr(fmt.Errorf("conn %d: echoed %d of %d bytes", seed, got, connSent.Load()))
			}
		}(uint64(i + 1))
	}
	wg.Wait()
	elapsed := time.Since(start)
	m.Chunks, m.ChunksBack = chunks.Load(), chunksBack.Load()
	m.BytesSent, m.BytesReceived, m.Mismatches = sent.Load(), received.Load(), mismatches.Load()
	if elapsed > 0 {
		m.Mbps = float64(m.BytesReceived) * 8 / elapsed.Seconds() / 1e6
	}
	return m
}

// runTCPPerfClient uploads to a sink for duration while timing small echo
// requests on a second connection.
func runTCPPerfClient(target string, duration time.Duration) *TCPForwardMetrics {
	m := &TCPForwardMetrics{Conns: 2}
	var mu sync.Mutex
	addErr := func(err error) {
		mu.Lock()
		m.Errors = append(m.Errors, err.Error())
		mu.Unlock()
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := dialTCPEntry(target, tcpModeSink)
		if err != nil {
			addErr(fmt.Errorf("sink: dial: %w", err))
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(duration + tcpIdleDrainTimeout))
		buf := make([]byte, tcpChunkSize)
		_, _ = newTCPStreamGenerator(99).Read(buf)
		start := time.Now()
		var sent int64
		for time.Since(start) < duration {
			n, err := conn.Write(buf)
			sent += int64(n)
			if err != nil {
				addErr(fmt.Errorf("sink: write: %w", err))
				return
			}
			m.Chunks++
		}
		closeTCPWrite(conn)
		var reply [8]byte
		if _, err := io.ReadFull(conn, reply[:]); err != nil {
			addErr(fmt.Errorf("sink: read count: %w", err))
			return
		}
		elapsed := time.Since(start)
		got := int64(binary.BigEndian.Uint64(reply[:]))
		m.BytesSent, m.BytesReceived = sent, got
		m.ChunksBack = got / tcpChunkSize
		if got != sent {
			addErr(fmt.Errorf("sink: received %d of %d bytes", got, sent))
		}
		m.Mbps = float64(got) * 8 / elapsed.Seconds() / 1e6
	}()

	var samples []int64
	conn, err := dialTCPEntry(target, tcpModeEcho)
	if err != nil {
		addErr(fmt.Errorf("ping: dial: %w", err))
	} else {
		_ = conn.SetDeadline(time.Now().Add(duration + tcpIdleDrainTimeout))
		req := make([]byte, tcpPingSize)
		resp := make([]byte, tcpPingSize)
		start := time.Now()
		for seq := uint64(0); time.Since(start) < duration; seq++ {
			binary.BigEndian.PutUint64(req, seq)
			sentAt := time.Now()
			m.Pings++
			if _, err := conn.Write(req); err != nil {
				addErr(fmt.Errorf("ping: write: %w", err))
				break
			}
			if _, err := io.ReadFull(conn, resp); err != nil {
				addErr(fmt.Errorf("ping: read: %w", err))
				break
			}
			if binary.BigEndian.Uint64(resp) != seq {
				m.Mismatches++
				addErr(fmt.Errorf("ping: got seq %d, want %d", binary.BigEndian.Uint64(resp), seq))
				break
			}
			m.Pongs++
			samples = append(samples, time.Since(sentAt).Nanoseconds())
			time.Sleep(tcpPingInterval)
		}
		conn.Close()
	}
	wg.Wait()

	if len(samples) > 0 {
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		var total int64
		for _, s := range samples {
			total += s
		}
		m.AvgLatencyMs = float64(total) / float64(len(samples)) / 1e6
		m.P50LatencyMs = percentileMs(samples, 0.50)
		m.P95LatencyMs = percentileMs(samples, 0.95)
		m.P99LatencyMs = percentileMs(samples, 0.99)
		m.MinLatencyMs = float64(samples[0]) / 1e6
		m.MaxLatencyMs = float64(samples[len(samples)-1]) / 1e6
	}
	return m
}

func populateResultFromTCPMetrics(result *TestResult, m *TCPForwardMetrics, duration time.Duration) {
	result.Sent = m.Chunks
	result.Received = m.ChunksBack
	result.ErrorPackets = m.Mismatches
	result.BytesSent = m.BytesSent
	result.BytesReceived = m.BytesReceived
	result.PacketSizeBytes = tcpChunkSize
	result.Mbps = m.Mbps
	result.TotalMBytes = float64(m.BytesReceived) / 1e6
	result.Throughput = float64(m.ChunksBack) / duration.Seconds()
	if m.BytesSent > 0 {
		result.LossRate = float64(m.BytesSent-m.BytesReceived) / float64(m.BytesSent)
	}
	result.AvgLatencyMs = m.AvgLatencyMs
	result.P50LatencyMs = m.P50LatencyMs
	result.P95LatencyMs = m.P95LatencyMs
	result.P99LatencyMs = m.P99LatencyMs
	result.MinLatencyMs = m.MinLatencyMs
	result.MaxLatencyMs = m.MaxLatencyMs

	switch {
	case len(m.Errors) > 0:
		result.Error = strings.Join(m.Errors, "; ")
	case m.BytesSent == 0:
		result.Error = "No bytes sent"
	case m.BytesReceived != m.BytesSent:
		result.Error = fmt.Sprintf("Received %d of %d bytes", m.BytesReceived, m.BytesSent)
	default:
		result.Success = true
	}
}

// lineProxy stands in for a line between machine A and B; fail cuts it.
type lineProxy struct {
	ln     net.Listener
	target string
	down   atomic.Bool
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
}

func startLineProxy(target string) (*lineProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &lineProxy{ln: ln, target: target, conns: make(map[net.Conn]struct{})}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if p.down.Load() {
				c.Close()
				continue
			}
			go p.relay(c)
		}
	}()
	return p, nil
}

func (p *lineProxy) addr() string { return p.ln.Addr().String() }

func (p *lineProxy) relay(src net.Conn) {
	dst, err := net.Dial("tcp", p.target)
	if err != nil {
		src.Close()
		return
	}
	p.mu.Lock()
	p.conns[src], p.conns[dst] = struct{}{}, struct{}{}
	p.mu.Unlock()
	go func() { _, _ = io.Copy(dst, src); dst.Close(); src.Close() }()
	_, _ = io.Copy(src, dst)
	src.Close()
	dst.Close()
	p.mu.Lock()
	delete(p.conns, src)
	delete(p.conns, dst)
	p.mu.Unlock()
}

// fail resets every connection on the line and refuses new ones for d. It
// returns how many connections it reset.
func (p *lineProxy) fail(d time.Duration) int {
	p.down.Store(true)
	p.mu.Lock()
	reset := len(p.conns)
	for c := range p.conns {
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		c.Close()
	}
	p.mu.Unlock()
	time.Sleep(d)
	p.down.Store(false)
	return reset
}

func (p *lineProxy) Close() { p.ln.Close() }

func writeRenderedConfig(dir, name, examplePath string, replacer *strings.Replacer) (string, error) {
	content, err := renderWGConfigFromExample(examplePath, replacer)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, []byte(content), 0o644)
}

// runTCPForwardLocalIntegration runs the tcp_forward examples as two local
// processes; line A passes through a proxy that the integrity test cuts.
func runTCPForwardLocalIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	result := TestResult{ConfigName: fmt.Sprintf("%s %s", config.Name, label), TotalDuration: config.Duration}

	ports := make([]int, 3)
	for i := range ports {
		p, err := allocateLocalTCPPort()
		if err != nil {
			result.Error = fmt.Sprintf("failed to allocate port: %v", err)
			return result
		}
		ports[i] = p
	}
	targetAddr := fmt.Sprintf("127.0.0.1:%d", ports[0])
	entryAddr := fmt.Sprintf("127.0.0.1:%d", ports[1])
	tunnelAddr := fmt.Sprintf("127.0.0.1:%d", ports[2])

	targetLn := make(chan struct{})
	go func() { _ = runTCPTargetServer(targetAddr, func() { close(targetLn) }) }()
	select {
	case <-targetLn:
	case <-time.After(10 * time.Second):
		result.Error = "TCP target server did not start"
		return result
	}

	proxyA, err := startLineProxy(tunnelAddr)
	if err != nil {
		result.Error = fmt.Sprintf("failed to start line proxy: %v", err)
		return result
	}
	defer proxyA.Close()

	tempDir, err := os.MkdirTemp("", "udplex-tcp-integration-*")
	if err != nil {
		result.Error = fmt.Sprintf("failed to create temp dir: %v", err)
		return result
	}
	defer os.RemoveAll(tempDir)

	replacer := strings.NewReplacer(
		"SERVER_IP_A:9001", proxyA.addr(),
		"SERVER_IP_B:9001", tunnelAddr,
		"0.0.0.0:9001", tunnelAddr,
		"0.0.0.0:8080", entryAddr,
		"127.0.0.1:80 ", targetAddr+" ",
	)
	serverPath, err := writeRenderedConfig(tempDir, "tcp_server.yaml", filepath.Join(examplesDir, "tcp_forward_server.yaml"), replacer)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	clientPath, err := writeRenderedConfig(tempDir, "tcp_client.yaml", filepath.Join(examplesDir, "tcp_forward_client.yaml"), replacer)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	var processes []*exec.Cmd
	defer func() {
		for _, p := range processes {
			stopProcess(p)
		}
	}()
	for i, path := range []string{serverPath, clientPath} {
		target, err := newLocalProfileTarget(config.Name, filepath.Base(path), i)
		if err != nil {
			result.Error = fmt.Sprintf("failed to allocate profile target: %v", err)
			return result
		}
		p := startUDPlexProcess(projectRoot, path, target)
		if p == nil {
			result.Error = "failed to start UDPlex process"
			return result
		}
		processes = append(processes, p)
	}
	time.Sleep(1500 * time.Millisecond) // let both tunnel lines authenticate

	var metrics *TCPForwardMetrics
	if withSleep {
		outage := make(chan int, 1)
		go func() {
			time.Sleep(tcpOutageStart)
			outage <- proxyA.fail(tcpOutageDuration)
		}()
		metrics = runTCPIntegrityClient(entryAddr, tcpIntegrityConns, config.Duration)
		if reset := <-outage; reset == 0 {
			metrics.Errors = append(metrics.Errors, "line A carried no connections when it was cut")
		}
	} else {
		metrics = runTCPPerfClient(entryAddr, config.Duration)
	}
	populateResultFromTCPMetrics(&result, metrics, config.Duration)
	return result
}

type tcpMultilineEnv struct {
	clientNS, serverNS string
	lineAIf, lineBIf   string
	cleanup            func()
}

// setupTCPMultilineNamespaces connects two namespaces with two veth lines,
// each with a netem delay when available.
func setupTCPMultilineNamespaces() (*tcpMultilineEnv, error) {
	suffix := time.Now().UnixNano() % 100000
	env := &tcpMultilineEnv{
		clientNS: fmt.Sprintf("udtcpc-%d", suffix),
		serverNS: fmt.Sprintf("udtcps-%d", suffix),
		lineAIf:  fmt.Sprintf("tla%d", suffix),
		lineBIf:  fmt.Sprintf("tlb%d", suffix),
	}
	env.cleanup = func() {
		_ = runCommand("ip", "netns", "del", env.clientNS)
		_ = runCommand("ip", "netns", "del", env.serverNS)
	}
	env.cleanup()

	commands := [][]string{
		{"ip", "netns", "add", env.clientNS},
		{"ip", "netns", "add", env.serverNS},
		{"ip", "-n", env.clientNS, "link", "set", "lo", "up"},
		{"ip", "-n", env.serverNS, "link", "set", "lo", "up"},
	}
	for _, line := range []struct{ ifName, clientIP, serverIP string }{
		{env.lineAIf, tcpLineAClientIP, tcpLineAServerIP},
		{env.lineBIf, tcpLineBClientIP, tcpLineBServerIP},
	} {
		peer := line.ifName + "s"
		commands = append(commands,
			[]string{"ip", "link", "add", line.ifName, "type", "veth", "peer", "name", peer},
			[]string{"ip", "link", "set", line.ifName, "netns", env.clientNS},
			[]string{"ip", "link", "set", peer, "netns", env.serverNS},
			[]string{"ip", "-n", env.clientNS, "addr", "add", line.clientIP + "/30", "dev", line.ifName},
			[]string{"ip", "-n", env.serverNS, "addr", "add", line.serverIP + "/30", "dev", peer},
			[]string{"ip", "-n", env.clientNS, "link", "set", line.ifName, "up"},
			[]string{"ip", "-n", env.serverNS, "link", "set", peer, "up"},
		)
	}
	for _, cmd := range commands {
		if err := runCommand(cmd[0], cmd[1:]...); err != nil {
			env.cleanup()
			return nil, err
		}
	}
	for _, ifName := range []string{env.lineAIf, env.lineBIf} {
		if err := runCommand("ip", "netns", "exec", env.clientNS, "tc", "qdisc", "add", "dev", ifName, "root", "netem", "delay", tcpNetemDelay); err != nil {
			fmt.Printf("Note: netem unavailable, lines run without delay: %v\n", err)
			break
		}
	}
	return env, nil
}

func startTCPTargetServerInNamespace(netns, listenAddr string) *exec.Cmd {
	executable, err := os.Executable()
	if err != nil {
		fmt.Printf("Failed to resolve test executable: %v\n", err)
		return nil
	}
	cmd := exec.Command("ip", "netns", "exec", netns, executable, "-tcp-target-server", listenAddr)
	return startManagedProcess(cmd, fmt.Sprintf("tcp-target-server [%s]", netns), "TCP target server ready", 10*time.Second)
}

func runTCPClientInNamespace(netns, target, mode string, duration time.Duration) (*TCPForwardMetrics, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("ip", "netns", "exec", netns, executable, "-tcp-client",
		"-target", target, "-mode", mode, "-duration-ms", strconv.Itoa(int(duration/time.Millisecond)))
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(output)))
	}
	var metrics TCPForwardMetrics
	if err := json.Unmarshal(output, &metrics); err != nil {
		return nil, fmt.Errorf("failed to parse TCP client metrics: %w, output=%s", err, strings.TrimSpace(string(output)))
	}
	return &metrics, nil
}

// runTCPForwardMultilineIntegration carries TCP streams over two separate
// network lines and takes line A down mid-transfer in the integrity test.
func runTCPForwardMultilineIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	result := TestResult{ConfigName: fmt.Sprintf("%s %s", config.Name, label), TotalDuration: config.Duration}

	env, err := setupTCPMultilineNamespaces()
	if err != nil {
		result.Error = fmt.Sprintf("failed to prepare network namespaces: %v", err)
		return result
	}
	defer env.cleanup()

	tempDir, err := os.MkdirTemp("", "udplex-tcp-integration-*")
	if err != nil {
		result.Error = fmt.Sprintf("failed to create temp dir: %v", err)
		return result
	}
	defer os.RemoveAll(tempDir)

	targetAddr := fmt.Sprintf("127.0.0.1:%d", tcpTargetPort)
	replacer := strings.NewReplacer(
		"SERVER_IP_A", tcpLineAServerIP,
		"SERVER_IP_B", tcpLineBServerIP,
		"127.0.0.1:80 ", targetAddr+" ",
	)
	serverPath, err := writeRenderedConfig(tempDir, "tcp_server.yaml", filepath.Join(examplesDir, "tcp_forward_server.yaml"), replacer)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	clientPath, err := writeRenderedConfig(tempDir, "tcp_client.yaml", filepath.Join(examplesDir, "tcp_forward_client.yaml"), replacer)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	target := startTCPTargetServerInNamespace(env.serverNS, targetAddr)
	if target == nil {
		result.Error = "failed to start TCP target server"
		return result
	}
	defer stopProcess(target)
	server := startUDPlexProcessInNamespace(projectRoot, serverPath, env.serverNS, newNamespaceProfileTarget("server", env.serverNS))
	if server == nil {
		result.Error = "failed to start server UDPlex process"
		return result
	}
	defer stopProcess(server)
	client := startUDPlexProcessInNamespace(projectRoot, clientPath, env.clientNS, newNamespaceProfileTarget("client", env.clientNS))
	if client == nil {
		result.Error = "failed to start client UDPlex process"
		return result
	}
	defer stopProcess(client)
	time.Sleep(1500 * time.Millisecond)

	entry := fmt.Sprintf("127.0.0.1:%d", tcpEntryPort)
	mode := "perf"
	var outage chan error
	if withSleep {
		mode = "integrity"
		outage = make(chan error, 1)
		go func() {
			time.Sleep(tcpOutageStart)
			aBefore := interfaceTxBytes(env.clientNS, env.lineAIf)
			_ = runCommand("ip", "-n", env.clientNS, "link", "set", env.lineAIf, "down")
			bStart := interfaceTxBytes(env.clientNS, env.lineBIf)
			time.Sleep(tcpOutageDuration)
			bDuring := interfaceTxBytes(env.clientNS, env.lineBIf) - bStart
			_ = runCommand("ip", "-n", env.clientNS, "link", "set", env.lineAIf, "up")
			switch {
			case aBefore < 1<<20:
				outage <- fmt.Errorf("line A sent only %d bytes before it went down", aBefore)
			case bDuring < 1<<20:
				outage <- fmt.Errorf("line B sent only %d bytes while line A was down", bDuring)
			default:
				outage <- nil
			}
		}()
	}
	metrics, err := runTCPClientInNamespace(env.clientNS, entry, mode, config.Duration)
	if err != nil {
		result.Error = fmt.Sprintf("TCP client failed: %v", err)
		return result
	}
	if outage != nil {
		if err := <-outage; err != nil {
			metrics.Errors = append(metrics.Errors, err.Error())
		}
	}
	populateResultFromTCPMetrics(&result, metrics, config.Duration)
	return result
}

// runWireGuardTCPListenIntegration relays TCP with tcp_listen (direct mode)
// to the server's wg address; the wg packets use two UDP lines.
func runWireGuardTCPListenIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	result := TestResult{ConfigName: fmt.Sprintf("%s %s", config.Name, label), TotalDuration: config.Duration}

	env, err := setupWGNamespaces()
	if err != nil {
		result.Error = fmt.Sprintf("failed to prepare network namespaces: %v", err)
		return result
	}
	defer env.cleanup()

	tempDir, err := os.MkdirTemp("", "udplex-wg-tcp-integration-*")
	if err != nil {
		result.Error = fmt.Sprintf("failed to create temp dir: %v", err)
		return result
	}
	defer os.RemoveAll(tempDir)

	clientPriv, clientPub, err := generateWGKeyPair()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	serverPriv, serverPub, err := generateWGKeyPair()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	targetAddr := fmt.Sprintf("127.0.0.1:%d", tcpTargetPort)
	replacer := strings.NewReplacer(
		"SERVER_IP_A", wgOuterServerIP,
		"SERVER_IP_B", wgOuterServerIP,
		"udplex-server", wgOuterServerIP,
		"CLIENT_PRIVATE_KEY_HEX", clientPriv,
		"CLIENT_PUBLIC_KEY_HEX", clientPub,
		"SERVER_PRIVATE_KEY_HEX", serverPriv,
		"SERVER_PUBLIC_KEY_HEX", serverPub,
		"127.0.0.1:80]", targetAddr+"]",
	)
	serverPath, err := writeRenderedConfig(tempDir, "server.yaml", filepath.Join(examplesDir, "tcp_over_wg_server.yaml"), replacer)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	clientPath, err := writeRenderedConfig(tempDir, "client.yaml", filepath.Join(examplesDir, "tcp_over_wg_client.yaml"), replacer)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	target := startTCPTargetServerInNamespace(env.serverNS, targetAddr)
	if target == nil {
		result.Error = "failed to start TCP target server"
		return result
	}
	defer stopProcess(target)
	server := startUDPlexProcessInNamespace(projectRoot, serverPath, env.serverNS, newNamespaceProfileTarget("server", env.serverNS))
	if server == nil {
		result.Error = "failed to start WireGuard server UDPlex process"
		return result
	}
	defer stopProcess(server)
	client := startUDPlexProcessInNamespace(projectRoot, clientPath, env.clientNS, newNamespaceProfileTarget("client", env.clientNS))
	if client == nil {
		result.Error = "failed to start WireGuard client UDPlex process"
		return result
	}
	defer stopProcess(client)
	time.Sleep(1500 * time.Millisecond)

	mode := "perf"
	if withSleep {
		mode = "integrity"
	}
	metrics, err := runTCPClientInNamespace(env.clientNS, fmt.Sprintf("127.0.0.1:%d", tcpEntryPort), mode, config.Duration)
	if err != nil {
		result.Error = fmt.Sprintf("TCP client failed: %v", err)
		return result
	}
	populateResultFromTCPMetrics(&result, metrics, config.Duration)
	return result
}

func interfaceTxBytes(netns, ifName string) int64 {
	out, err := exec.Command("ip", "netns", "exec", netns, "cat", "/sys/class/net/"+ifName+"/statistics/tx_bytes").Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}
