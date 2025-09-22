package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

type TestConfig struct {
	Name        string
	ConfigFiles []string
	TestPort    int
	TargetPort  int
	Duration    time.Duration
}

type TestResult struct {
	ConfigName      string
	Sent            int64
	Received        int64
	ErrorPackets    int64
	LossRate        float64
	DataIntegrity   bool
	TotalDuration   time.Duration
	Throughput      float64 // packets per second
	BytesSent       int64   // total bytes sent (application payload size)
	BytesReceived   int64   // total bytes received (unique packets counted)
	Mbps            float64 // average megabits per second over the test duration
	TotalMBytes     float64 // total megabytes received
	PacketSizeBytes int     // size in bytes of each sent packet
	// latency metrics (milliseconds)
	AvgLatencyMs float64
	P50LatencyMs float64
	P95LatencyMs float64
	P99LatencyMs float64
	MinLatencyMs float64
	MaxLatencyMs float64
	Success      bool
	Error        string
}

type PacketData struct {
	ID        uint32
	Timestamp int64
	Checksum  [32]byte
	Payload   [1024]byte
}

// JSON structures for metrics persistence
type MetricsFile struct {
	Repo        string        `json:"repo"`
	Branch      string        `json:"branch"`
	SHA         string        `json:"sha"`
	RunID       string        `json:"run_id"`
	RunnerOS    string        `json:"runner_os"`
	Timestamp   time.Time     `json:"timestamp"`
	DurationSec float64       `json:"duration_sec"`
	Results     []MetricEntry `json:"results"`
}

type MetricEntry struct {
	Name            string  `json:"name"`
	Sent            int64   `json:"sent"`
	Received        int64   `json:"received"`
	ErrorPackets    int64   `json:"error_packets"`
	LossRate        float64 `json:"loss_rate"`
	Throughput      float64 `json:"throughput_pps"`
	Mbps            float64 `json:"mbps"`
	TotalMBytes     float64 `json:"total_mbytes"`
	PacketSizeBytes int     `json:"packet_size_bytes"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
	P50LatencyMs    float64 `json:"p50_latency_ms"`
	P95LatencyMs    float64 `json:"p95_latency_ms"`
	P99LatencyMs    float64 `json:"p99_latency_ms"`
	MinLatencyMs    float64 `json:"min_latency_ms"`
	MaxLatencyMs    float64 `json:"max_latency_ms"`
	Success         bool    `json:"success"`
	Error           string  `json:"error,omitempty"`
}

// Bitset is a compact bitmap used to track seen packet IDs with minimal memory.
type Bitset struct {
	words []uint64
}

func (b *Bitset) ensure(bit uint32) {
	needed := int(bit/64) + 1
	if needed > len(b.words) {
		newWords := make([]uint64, needed)
		copy(newWords, b.words)
		b.words = newWords
	}
}

func (b *Bitset) Test(bit uint32) bool {
	idx := int(bit / 64)
	if idx >= len(b.words) {
		return false
	}
	return (b.words[idx] & (uint64(1) << (bit % 64))) != 0
}

func (b *Bitset) Set(bit uint32) {
	b.ensure(bit)
	idx := int(bit / 64)
	b.words[idx] |= uint64(1) << (bit % 64)
}

const (
	LISTEN_PORT     = 5201
	SEND_PORT       = 5202
	TEST_DURATION   = 10 * time.Second
	MAX_PACKET_LOSS = 0.05 // 5% acceptable packet loss
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-help" {
		fmt.Println("UDPlex Integration Test Tool")
		fmt.Println("Usage: go run udp_test.go")
		fmt.Println("This tool tests UDPlex configurations by sending UDP packets and measuring performance")
		return
	}

	projectRoot := getProjectRoot()
	examplesDir := filepath.Join(projectRoot, "examples")

	// Build UDPlex binary
	fmt.Println("Building UDPlex...")
	if err := buildUDPlex(projectRoot); err != nil {
		fmt.Printf("Failed to build UDPlex: %v\n", err)
		os.Exit(1)
	}

	// Define test configurations
	testConfigs := []TestConfig{
		{
			Name:        "Basic",
			ConfigFiles: []string{"basic.yaml"},
			TestPort:    LISTEN_PORT,
			TargetPort:  SEND_PORT,
			Duration:    TEST_DURATION,
		},
		{
			Name:        "Auth Client-Server",
			ConfigFiles: []string{"auth_server.yaml", "auth_client.yaml"},
			TestPort:    LISTEN_PORT,
			TargetPort:  SEND_PORT,
			Duration:    TEST_DURATION,
		},
		{
			Name:        "Filter",
			ConfigFiles: []string{"filter_test.yaml"},
			TestPort:    LISTEN_PORT,
			TargetPort:  SEND_PORT,
			Duration:    TEST_DURATION,
		},
		{
			Name:        "Load Balancer",
			ConfigFiles: []string{"load_balancer_test.yaml"},
			TestPort:    LISTEN_PORT,
			TargetPort:  SEND_PORT,
			Duration:    TEST_DURATION,
		},
		{
			Name:        "TCP Tunnel",
			ConfigFiles: []string{"tcp_tunnel_server.yaml", "tcp_tunnel_client.yaml"},
			TestPort:    LISTEN_PORT,
			TargetPort:  SEND_PORT,
			Duration:    TEST_DURATION,
		},
		{
			Name:        "IP Router",
			ConfigFiles: []string{"ip_router_test.yaml"},
			TestPort:    LISTEN_PORT,
			TargetPort:  SEND_PORT,
			Duration:    TEST_DURATION,
		},
	}

	var results []TestResult

	for _, config := range testConfigs {
		// Packet loss-focused test (with Sleep)
		fmt.Printf("\n=== %s - Packet Loss Test ===\n", config.Name)
		resultSleep := runTest(projectRoot, examplesDir, config, "Packet Loss Test", true)
		results = append(results, resultSleep)
		if resultSleep.Success {
			fmt.Printf("✓ %s - Packet Loss Test: PASSED\n", config.Name)
		} else {
			fmt.Printf("✗ %s - Packet Loss Test: FAILED - %s\n", config.Name, resultSleep.Error)
		}

		// Performance-focused test (no Sleep)
		fmt.Printf("\n=== %s - Performance Test ===\n", config.Name)
		resultNoSleep := runTest(projectRoot, examplesDir, config, "Performance Test", false)
		results = append(results, resultNoSleep)
		if resultNoSleep.Success {
			fmt.Printf("✓ %s - Performance Test: PASSED\n", config.Name)
		} else {
			fmt.Printf("✗ %s - Performance Test: FAILED - %s\n", config.Name, resultNoSleep.Error)
		}
	}

	// Print summary
	fmt.Println("\n=== Test Summary ===")
	passed := 0
	for _, result := range results {
		fmt.Printf("%-20s | Sent: %6d | Received: %6d | Err: %4d | Loss: %5.2f%% | Thr: %8.0f pps | Rate: %6.2f Mbits/s | Total: %6.2f MB | Lat(ms) avg/p50/p95/p99 min-max: %.2f/%.2f/%.2f/%.2f %.2f-%.2f | Status: %s\n",
			result.ConfigName,
			result.Sent,
			result.Received,
			result.ErrorPackets,
			result.LossRate*100,
			result.Throughput,
			result.Mbps,
			result.TotalMBytes,
			result.AvgLatencyMs, result.P50LatencyMs, result.P95LatencyMs, result.P99LatencyMs, result.MinLatencyMs, result.MaxLatencyMs,
			func() string {
				if result.Success {
					passed++
					return "PASS"
				}
				return "FAIL"
			}())
	}

	fmt.Printf("\nTotal: %d/%d tests passed\n", passed, len(results))

	// Always write JSON metrics before exiting
	if err := writeJSONMetrics(projectRoot, results, TEST_DURATION); err != nil {
		fmt.Printf("Failed to write metrics JSON: %v\n", err)
	}

	if passed != len(results) {
		os.Exit(1)
	}
}

func getProjectRoot() string {
	dir, _ := os.Getwd()
	for {
		// Look for the main project indicators: src directory and main go.mod
		if _, err := os.Stat(filepath.Join(dir, "src")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "../../" // fallback
}

func buildUDPlex(projectRoot string) error {
	srcDir := filepath.Join(projectRoot, "src")
	binaryName := "udplex_test"
	// Add .exe extension on Windows
	if filepath.Ext(os.Args[0]) == ".exe" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(projectRoot, binaryName)

	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = srcDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build failed: %v\nOutput: %s", err, output)
	}

	return nil
}

func runTest(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	result := TestResult{
		ConfigName:    fmt.Sprintf("%s %s", config.Name, label),
		TotalDuration: config.Duration,
	}

	// Start UDPlex processes
	var processes []*exec.Cmd

	for _, configFile := range config.ConfigFiles {
		configPath := filepath.Join(examplesDir, configFile)
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			result.Error = fmt.Sprintf("Config file not found: %s", configPath)
			return result
		}

		process := startUDPlexProcess(projectRoot, configPath)
		if process == nil {
			result.Error = "Failed to start UDPlex process"
			return result
		}
		processes = append(processes, process)
	}

	// Wait for processes to start
	time.Sleep(2 * time.Second)

	// Debug: check if port is actually listening
	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", config.TargetPort))
	if err != nil {
		fmt.Printf("Warning: Cannot connect to target port %d: %v\n", config.TargetPort, err)
	} else {
		conn.Close()
		fmt.Printf("Debug: Successfully connected to port %d\n", config.TargetPort)
	}

	// Ensure processes are cleaned up
	defer func() {
		for _, process := range processes {
			if process != nil && process.Process != nil {
				process.Process.Kill()
				process.Wait()
			}
		}
	}()

	// Run UDP test
	sent, received, errorPackets, bytesSent, bytesReceived, lat := runUDPTest(config.TestPort, config.TargetPort, config.Duration, withSleep)

	result.Sent = sent
	result.Received = received
	result.ErrorPackets = errorPackets
	result.TotalDuration = config.Duration
	result.BytesSent = bytesSent
	result.BytesReceived = bytesReceived

	if sent > 0 {
		result.LossRate = float64(sent-received) / float64(sent)
		result.Throughput = float64(received) / config.Duration.Seconds()
	}
	if bytesReceived > 0 {
		seconds := config.Duration.Seconds()
		if seconds > 0 {
			result.Mbps = (float64(bytesReceived) * 8.0) / seconds / 1e6
		}
		result.TotalMBytes = float64(bytesReceived) / 1e6
	}

	// Record per-packet size used by sender
	result.PacketSizeBytes = int(unsafe.Sizeof(PacketData{}))

	// Fill latency metrics (convert ns -> ms)
	result.AvgLatencyMs = lat.AvgNs / 1e6
	result.P50LatencyMs = lat.P50Ns / 1e6
	result.P95LatencyMs = lat.P95Ns / 1e6
	result.P99LatencyMs = lat.P99Ns / 1e6
	result.MinLatencyMs = float64(lat.MinNs) / 1e6
	result.MaxLatencyMs = float64(lat.MaxNs) / 1e6

	// Determine if test passed
	if sent == 0 {
		result.Error = "No packets sent"
	} else if withSleep {
		// In loss-check mode, enforce integrity and loss thresholds
		if errorPackets > 0 {
			result.Error = fmt.Sprintf("Error packets detected: %d", errorPackets)
		} else if result.LossRate > MAX_PACKET_LOSS {
			result.Error = fmt.Sprintf("High packet loss: %.2f%%", result.LossRate*100)
		} else if received == 0 {
			result.Error = "No packets received"
		} else {
			result.Success = true
		}
	} else {
		// In performance mode, don't enforce loss rate, but integrity must hold and some packets must be received
		if errorPackets > 0 {
			result.Error = fmt.Sprintf("Error packets detected (perf): %d", errorPackets)
		} else if received == 0 {
			result.Error = "No packets received (perf)"
		} else {
			result.Success = true
		}
	}

	return result
}

func startUDPlexProcess(projectRoot, configPath string) *exec.Cmd {
	binaryName := "udplex_test"
	// Add .exe extension on Windows
	if filepath.Ext(os.Args[0]) == ".exe" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(projectRoot, binaryName)

	cmd := exec.Command(binaryPath, "-c", configPath)

	// Capture stdout/stderr to detect healthy start and print on failure
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Printf("Failed to get stdout pipe: %v\n", err)
		return nil
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		fmt.Printf("Failed to get stderr pipe: %v\n", err)
		return nil
	}

	var logBuf bytes.Buffer
	logMu := &sync.Mutex{}
	readyCh := make(chan struct{}, 1)
	failCh := make(chan string, 1)

	// Start the process
	if err := cmd.Start(); err != nil {
		fmt.Printf("Failed to start UDPlex: %v\n", err)
		return nil
	}

	// Helper to read a stream and look for signals
	scanStream := func(rdr *bufio.Scanner, stream string) {
		for rdr.Scan() {
			line := rdr.Text()
			// Store logs
			logMu.Lock()
			logBuf.WriteString(line)
			logBuf.WriteByte('\n')
			logMu.Unlock()

			// Detect healthy start
			if strings.Contains(line, "UDPlex started and ready") {
				select {
				case readyCh <- struct{}{}:
				default:
				}
			}
			// Detect obvious failures
			if strings.Contains(line, "FATAL") || strings.Contains(line, "Failed ") || strings.Contains(line, "Failed:") {
				select {
				case failCh <- fmt.Sprintf("%s: %s", stream, line):
				default:
				}
			}
		}
	}

	go scanStream(bufio.NewScanner(stdoutPipe), "stdout")
	go scanStream(bufio.NewScanner(stderrPipe), "stderr")

	// Wait briefly for healthy signal or failure; otherwise timeout
	timeout := 5 * time.Second
	select {
	case <-readyCh:
		// Healthy start
		return cmd
	case msg := <-failCh:
		// Early failure detected
		logMu.Lock()
		logs := logBuf.String()
		logMu.Unlock()
		fmt.Printf("UDPlex failed to start for config %s: %s\n--- Logs ---\n%s\n------------\n", configPath, msg, logs)
		// Ensure process is not left running
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil
	case <-time.After(timeout):
		// No healthy signal within timeout; consider it a failure
		logMu.Lock()
		logs := logBuf.String()
		logMu.Unlock()
		fmt.Printf("UDPlex did not signal readiness within %v for config %s\n--- Logs ---\n%s\n------------\n", timeout, configPath, logs)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil
	}
}

// LatencySummary conveys aggregate latency stats in nanoseconds.
type LatencySummary struct {
	AvgNs float64
	P50Ns float64
	P95Ns float64
	P99Ns float64
	MinNs int64
	MaxNs int64
}

func runUDPTest(listenPort, sendPort int, duration time.Duration, withSleep bool) (sent, received, errorPackets int64, bytesSent, bytesReceived int64, lat LatencySummary) {
	var sentCount, receivedCount int64
	var bytesSentCount, bytesReceivedCount int64
	var errorCount int64
	var wg sync.WaitGroup

	// Bitmap to deduplicate received packet IDs (optional but keeps counts accurate)
	var seenIDs Bitset

	// latency aggregation (receiver goroutine only mutates these)
	var latCount int64
	var latSumNs int64
	var latMinNs int64 = 0
	var latMaxNs int64 = 0
	const sampleCap = 20000
	latSample := make([]int64, 0, sampleCap)

	// Start receiver
	wg.Add(1)
	go func() {
		defer wg.Done()

		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", listenPort))
		if err != nil {
			fmt.Printf("Failed to resolve listen address: %v\n", err)
			return
		}

		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			fmt.Printf("Failed to listen on port %d: %v\n", listenPort, err)
			return
		}
		defer conn.Close()

		conn.SetReadDeadline(time.Now().Add(duration + 5*time.Second))

		buffer := make([]byte, 2048)
		for {
			n, err := conn.Read(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					break
				}
				continue
			}

			if n == int(unsafe.Sizeof(PacketData{})) {
				var packet PacketData
				copy((*[unsafe.Sizeof(PacketData{})]byte)(unsafe.Pointer(&packet))[:], buffer[:n])
				
				// Verify data integrity by recomputing checksum from received content
				if calculateChecksum(packet.ID, packet.Timestamp, packet.Payload[:]) != packet.Checksum {
					atomic.AddInt64(&errorCount, 1)
				}
				// Deduplicate by packet ID to avoid counting duplicates
				if packet.ID > 0 { // IDs start from 1 in our sender
					idx := packet.ID - 1
					if !seenIDs.Test(idx) {
						// compute latency once for the first time we see this packet
						nowNs := time.Now().UnixNano()
						if packet.Timestamp > 0 {
							d := nowNs - packet.Timestamp
							if d < 0 {
								d = 0
							}
							// aggregate
							if latCount == 0 {
								latMinNs, latMaxNs = d, d
							} else {
								if d < latMinNs {
									latMinNs = d
								}
								if d > latMaxNs {
									latMaxNs = d
								}
							}
							latSumNs += d
							latCount++
							if len(latSample) < sampleCap {
								latSample = append(latSample, d)
							}
						}
						seenIDs.Set(idx)
						atomic.AddInt64(&receivedCount, 1)
						atomic.AddInt64(&bytesReceivedCount, int64(n))
					}
				} else {
					// Fallback: count ID==0 packets without dedup to avoid underflow
					// still compute latency if possible
					nowNs := time.Now().UnixNano()
					if packet.Timestamp > 0 {
						d := nowNs - packet.Timestamp
						if d < 0 {
							d = 0
						}
						if latCount == 0 {
							latMinNs, latMaxNs = d, d
						} else {
							if d < latMinNs {
								latMinNs = d
							}
							if d > latMaxNs {
								latMaxNs = d
							}
						}
						latSumNs += d
						latCount++
						if len(latSample) < sampleCap {
							latSample = append(latSample, d)
						}
					}
					atomic.AddInt64(&receivedCount, 1)
					atomic.AddInt64(&bytesReceivedCount, int64(n))
				}
			}
		}
	}()

	// Start sender
	wg.Add(1)
	go func() {
		defer wg.Done()

		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", sendPort))
		if err != nil {
			fmt.Printf("Failed to resolve target address: %v\n", err)
			return
		}

		conn, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			fmt.Printf("Failed to dial target port %d: %v\n", sendPort, err)
			return
		}
		defer conn.Close()

		startTime := time.Now()
		var packetID uint32

		for time.Since(startTime) < duration {
			packetID++

			var packet PacketData
			packet.ID = packetID
			packet.Timestamp = time.Now().UnixNano()

			// Fill payload with random data
			rand.Read(packet.Payload[:])

			// Calculate checksum
			packet.Checksum = calculateChecksum(packet.ID, packet.Timestamp, packet.Payload[:])

			// Send packet
			data := (*[unsafe.Sizeof(PacketData{})]byte)(unsafe.Pointer(&packet))[:]
			n, err := conn.Write(data)
			if err == nil {
				atomic.AddInt64(&sentCount, 1)
				atomic.AddInt64(&bytesSentCount, int64(n))
			}

			if withSleep {
				time.Sleep(1 * time.Microsecond)
			}
		}
	}()

	wg.Wait()

	sent = atomic.LoadInt64(&sentCount)
	received = atomic.LoadInt64(&receivedCount)
	errorPackets = atomic.LoadInt64(&errorCount)

	// finalize latency summary
	if latCount > 0 {
		lat.AvgNs = float64(latSumNs) / float64(latCount)
		lat.MinNs = latMinNs
		lat.MaxNs = latMaxNs
		if len(latSample) > 0 {
			sort.Slice(latSample, func(i, j int) bool { return latSample[i] < latSample[j] })
			// helper to percentile from sample
			pct := func(p float64) float64 {
				if len(latSample) == 0 {
					return 0
				}
				// nearest-rank method
				rank := int(p*float64(len(latSample)-1) + 0.5)
				if rank < 0 {
					rank = 0
				}
				if rank >= len(latSample) {
					rank = len(latSample) - 1
				}
				return float64(latSample[rank])
			}
			lat.P50Ns = pct(0.50)
			lat.P95Ns = pct(0.95)
			lat.P99Ns = pct(0.99)
		}
	}

	// load counters for return
	sent = atomic.LoadInt64(&sentCount)
	received = atomic.LoadInt64(&receivedCount)
	errorPackets = atomic.LoadInt64(&errorCount)
	bytesSent = atomic.LoadInt64(&bytesSentCount)
	bytesReceived = atomic.LoadInt64(&bytesReceivedCount)

	return sent, received, errorPackets, bytesSent, bytesReceived, lat
}

func calculateChecksum(id uint32, timestamp int64, payload []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte{byte(id), byte(id >> 8), byte(id >> 16), byte(id >> 24)})
	h.Write([]byte{byte(timestamp), byte(timestamp >> 8), byte(timestamp >> 16), byte(timestamp >> 24),
		byte(timestamp >> 32), byte(timestamp >> 40), byte(timestamp >> 48), byte(timestamp >> 56)})
	h.Write(payload)

	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

// writeJSONMetrics writes metrics to metrics/latest.json and metrics/<timestamp>.json under project root.
func writeJSONMetrics(projectRoot string, results []TestResult, testDuration time.Duration) error {
	metricsDir := filepath.Join(projectRoot, "metrics")
	if err := os.MkdirAll(metricsDir, 0o755); err != nil {
		return err
	}

	// collect environment metadata (works in GitHub Actions, falls back locally)
	repo := getenvDefault("GITHUB_REPOSITORY", "local")
	branch := getenvDefault("GITHUB_REF_NAME", currentBranch())
	sha := getenvDefault("GITHUB_SHA", currentSHA())
	runID := getenvDefault("GITHUB_RUN_ID", "0")
	runnerOS := getenvDefault("RUNNER_OS", runtimeGOOS())

	mf := MetricsFile{
		Repo:        repo,
		Branch:      branch,
		SHA:         sha,
		RunID:       runID,
		RunnerOS:    runnerOS,
		Timestamp:   time.Now().UTC(),
		DurationSec: testDuration.Seconds(),
		Results:     make([]MetricEntry, 0, len(results)),
	}

	for _, r := range results {
		mf.Results = append(mf.Results, MetricEntry{
			Name:            r.ConfigName,
			Sent:            r.Sent,
			Received:        r.Received,
			ErrorPackets:    r.ErrorPackets,
			LossRate:        r.LossRate,
			Throughput:      r.Throughput,
			Mbps:            r.Mbps,
			TotalMBytes:     r.TotalMBytes,
			PacketSizeBytes: r.PacketSizeBytes,
			AvgLatencyMs:    r.AvgLatencyMs,
			P50LatencyMs:    r.P50LatencyMs,
			P95LatencyMs:    r.P95LatencyMs,
			P99LatencyMs:    r.P99LatencyMs,
			MinLatencyMs:    r.MinLatencyMs,
			MaxLatencyMs:    r.MaxLatencyMs,
			Success:         r.Success,
			Error:           r.Error,
		})
	}

	data, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}

	ts := mf.Timestamp.Format("20060102T150405Z")
	latestPath := filepath.Join(metricsDir, "latest.json")
	versionedPath := filepath.Join(metricsDir, fmt.Sprintf("%s.json", ts))

	if err := os.WriteFile(latestPath, data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(versionedPath, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("Metrics written to %s and %s\n", latestPath, versionedPath)
	return nil
}

func getenvDefault(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

// lightweight helpers; safe fallbacks when not in git
func currentBranch() string { return os.Getenv("GIT_BRANCH") }
func currentSHA() string    { return os.Getenv("GIT_SHA") }
func runtimeGOOS() string   { return strings.ToLower(os.Getenv("RUNNER_OS")) }
