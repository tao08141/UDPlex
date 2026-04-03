package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/crypto/curve25519"
)

type TestConfig struct {
	Name        string
	ConfigFiles []string
	TestPort    int
	TargetPort  int
	Duration    time.Duration
	Runner      func(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult
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

type IntegrationProfileTarget struct {
	Name      string
	Namespace string
	PprofAddr string
}

type ProfileArtifactIndex struct {
	GeneratedAt time.Time                  `json:"generated_at"`
	Entries     []ProfileArtifactIndexItem `json:"entries"`
}

type ProfileArtifactIndexItem struct {
	ConfigName       string   `json:"config_name"`
	ConfigNormalized string   `json:"config_normalized"`
	TargetName       string   `json:"target_name"`
	TargetNormalized string   `json:"target_normalized"`
	Namespace        string   `json:"namespace,omitempty"`
	PprofAddr        string   `json:"pprof_addr,omitempty"`
	BaseName         string   `json:"base_name"`
	RawProfile       string   `json:"raw_profile"`
	TopReport        string   `json:"top_report"`
	SVGReport        string   `json:"svg_report,omitempty"`
	TopHotspots      []string `json:"top_hotspots,omitempty"`
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

type WGEchoMetrics struct {
	Sent            int64   `json:"sent"`
	Received        int64   `json:"received"`
	ErrorPackets    int64   `json:"error_packets"`
	BytesSent       int64   `json:"bytes_sent"`
	BytesReceived   int64   `json:"bytes_received"`
	PacketSizeBytes int     `json:"packet_size_bytes"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
	P50LatencyMs    float64 `json:"p50_latency_ms"`
	P95LatencyMs    float64 `json:"p95_latency_ms"`
	P99LatencyMs    float64 `json:"p99_latency_ms"`
	MinLatencyMs    float64 `json:"min_latency_ms"`
	MaxLatencyMs    float64 `json:"max_latency_ms"`
}

type IntegrationSelection struct {
	Suite         string
	SelectedTests map[string]struct{}
	BuildTags     string
	ProfileDir    string
	ProfileTests  map[string]struct{}
	ProfileSecs   int
}

var integrationSelection IntegrationSelection

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

	wgOuterClientIP   = "172.31.0.1"
	wgOuterServerIP   = "172.31.0.2"
	wgForwardPortA    = 5900
	wgForwardPortB    = 5901
	wgTCPTunnelPort   = 5910
	wgInnerServerAddr = "10.66.0.1:5201"
)

const (
	wgModeForward   = "forward"
	wgModeTCPTunnel = "tcp_tunnel"
)

const (
	wgExampleClientPrivateKey = "787bec983d533e31e64a71c5c079006860cd9e2c6a93b6da69ab4465e8d58179"
	wgExampleClientPublicKey  = "edfbbb2364ea4d1f8c85034c3358172f6154055e4c9d0bbf86825b03d10ed25c"
	wgExampleServerPrivateKey = "28e0153a3f10376886d3a1de6153cc1c6026ca301ac854104861eafa41b9a067"
	wgExampleServerPublicKey  = "4e126dd08dc1e79c7417e71198861dd57694d68f34cbb6a8b3777697e9c3654c"
)

func main() {
	if handled := handleWGHelperCommand(); handled {
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "-help" {
		fmt.Println("UDPlex Integration Test Tool")
		fmt.Println("Usage: go run udp_integration.go [-suite all|wg] [-tests test1,test2,...]")
		fmt.Println("Default suite is 'all', which runs the full integration matrix.")
		fmt.Println("Use '-suite wg' to run only WireGuard integration tests.")
		fmt.Println("Use '-tests' to run a subset, for example:")
		fmt.Println("  go run udp_integration.go -tests wg_forward,wg_tcp_tunnel")
		fmt.Println("  go run udp_integration.go -tests basic,tcp_tunnel")
		fmt.Println("Optional profiling flags:")
		fmt.Println("  go run udp_integration.go -build-tags dev -profile-dir ./profiles -profile-tests tcp_tunnel,wg_tcp_tunnel -profile-seconds 10")
		return
	}

	projectRoot := getProjectRoot()
	examplesDir := filepath.Join(projectRoot, "examples")
	selection := parseIntegrationSelection()
	integrationSelection = selection

	// Build UDPlex binary
	fmt.Println("Building UDPlex...")
	if err := buildUDPlex(projectRoot, selection.BuildTags); err != nil {
		fmt.Printf("Failed to build UDPlex: %v\n", err)
		os.Exit(1)
	}

	// Define non-WG baseline configurations
	regularConfigs := []TestConfig{
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

	var testConfigs []TestConfig
	if selection.Suite == "all" {
		testConfigs = append(testConfigs, regularConfigs...)
	}

	if ok, reason := supportsWGIntegration(); ok {
		testConfigs = append(testConfigs,
			TestConfig{
				Name:     "WireGuard Forward",
				Duration: TEST_DURATION,
				Runner:   runWireGuardForwardIntegration,
			},
			TestConfig{
				Name:     "WireGuard TCP Tunnel",
				Duration: TEST_DURATION,
				Runner:   runWireGuardTCPTunnelIntegration,
			},
		)
	} else {
		fmt.Printf("Skipping WireGuard integration tests: %s\n", reason)
	}

	testConfigs = filterIntegrationConfigs(testConfigs, selection.SelectedTests)

	if len(testConfigs) == 0 {
		fmt.Printf("No integration tests selected for suite %q\n", selection.Suite)
		return
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

		// Performance-focused test (with adaptive sleep)
		fmt.Printf("\n=== %s - Performance Test ===\n", config.Name)
		resultAdaptive := runTest(projectRoot, examplesDir, config, "Performance Test", false)
		results = append(results, resultAdaptive)
		if resultAdaptive.Success {
			fmt.Printf("✓ %s - Performance Test: PASSED\n", config.Name)
		} else {
			fmt.Printf("✗ %s - Performance Test: FAILED - %s\n", config.Name, resultAdaptive.Error)
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

func buildUDPlex(projectRoot string, buildTags string) error {
	srcDir := filepath.Join(projectRoot, "src")
	binaryName := "udplex_test"
	// Add .exe extension on Windows
	if filepath.Ext(os.Args[0]) == ".exe" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(projectRoot, binaryName)

	args := []string{"build"}
	if strings.TrimSpace(buildTags) != "" {
		args = append(args, "-tags", strings.TrimSpace(buildTags))
	}
	args = append(args, "-o", binaryPath, ".")

	cmd := exec.Command("go", args...)
	cmd.Dir = srcDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build failed: %v\nOutput: %s", err, output)
	}

	return nil
}

func parseIntegrationSelection() IntegrationSelection {
	selection := IntegrationSelection{
		Suite:         "all",
		SelectedTests: make(map[string]struct{}),
		ProfileTests:  make(map[string]struct{}),
		ProfileSecs:   int(TEST_DURATION / time.Second),
	}

	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "-suite":
			if i+1 < len(os.Args) {
				candidate := strings.ToLower(strings.TrimSpace(os.Args[i+1]))
				if candidate == "all" || candidate == "wg" {
					selection.Suite = candidate
				}
				i++
			}
		case "-tests":
			if i+1 < len(os.Args) {
				for _, raw := range strings.Split(os.Args[i+1], ",") {
					name := normalizeIntegrationTestName(raw)
					if name != "" {
						selection.SelectedTests[name] = struct{}{}
					}
				}
				i++
			}
		case "-build-tags":
			if i+1 < len(os.Args) {
				selection.BuildTags = strings.TrimSpace(os.Args[i+1])
				i++
			}
		case "-profile-dir":
			if i+1 < len(os.Args) {
				selection.ProfileDir = strings.TrimSpace(os.Args[i+1])
				i++
			}
		case "-profile-tests":
			if i+1 < len(os.Args) {
				for _, raw := range strings.Split(os.Args[i+1], ",") {
					name := normalizeIntegrationTestName(raw)
					if name != "" {
						selection.ProfileTests[name] = struct{}{}
					}
				}
				i++
			}
		case "-profile-seconds":
			if i+1 < len(os.Args) {
				if secs, err := strconv.Atoi(strings.TrimSpace(os.Args[i+1])); err == nil && secs > 0 {
					selection.ProfileSecs = secs
				}
				i++
			}
		case "-all":
			selection.Suite = "all"
		case "-wg":
			selection.Suite = "wg"
		}
	}

	return selection
}

func filterIntegrationConfigs(configs []TestConfig, selected map[string]struct{}) []TestConfig {
	if len(selected) == 0 {
		return configs
	}

	filtered := make([]TestConfig, 0, len(configs))
	for _, config := range configs {
		if integrationConfigSelected(config.Name, selected) {
			filtered = append(filtered, config)
		}
	}
	return filtered
}

func integrationConfigSelected(name string, selected map[string]struct{}) bool {
	if len(selected) == 0 {
		return true
	}

	normalized := normalizeIntegrationTestName(name)
	if _, ok := selected[normalized]; ok {
		return true
	}
	if strings.HasPrefix(normalized, "wireguard_") {
		if _, ok := selected["wg"]; ok {
			return true
		}
		if _, ok := selected["wireguard"]; ok {
			return true
		}
	}
	return false
}

func normalizeIntegrationTestName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		"-", "_",
		" ", "_",
		"/", "_",
	)
	value = replacer.Replace(value)
	value = strings.Trim(value, "_")

	switch value {
	case "auth", "auth_client_server":
		return "auth_client_server"
	case "load_balancer", "loadbalancer":
		return "load_balancer"
	case "tcp_tunnel", "tcptunnel":
		return "tcp_tunnel"
	case "ip_router", "iprouter":
		return "ip_router"
	case "wg_forward", "wireguard_forward":
		return "wireguard_forward"
	case "wg_tcp_tunnel", "wireguard_tcp_tunnel", "wg_tcptunnel":
		return "wireguard_tcp_tunnel"
	}

	return value
}

func runTest(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	if config.Runner != nil {
		return config.Runner(projectRoot, examplesDir, config, label, withSleep)
	}

	result := TestResult{
		ConfigName:    fmt.Sprintf("%s %s", config.Name, label),
		TotalDuration: config.Duration,
	}

	// Start UDPlex processes
	var processes []*exec.Cmd
	var profileTargets []IntegrationProfileTarget

	for index, configFile := range config.ConfigFiles {
		configPath := filepath.Join(examplesDir, configFile)
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			result.Error = fmt.Sprintf("Config file not found: %s", configPath)
			return result
		}

		profileTarget, err := newLocalProfileTarget(config.Name, configFile, index)
		if err != nil {
			result.Error = fmt.Sprintf("Failed to allocate profile target: %v", err)
			return result
		}

		process := startUDPlexProcess(projectRoot, configPath, profileTarget)
		if process == nil {
			result.Error = "Failed to start UDPlex process"
			return result
		}
		processes = append(processes, process)
		profileTargets = append(profileTargets, profileTarget)
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

	profileWait, err := maybeStartProfiles(projectRoot, config.Name, withSleep, profileTargets)
	if err != nil {
		result.Error = fmt.Sprintf("Failed to start profiles: %v", err)
		return result
	}

	// Run UDP test
	sent, received, errorPackets, bytesSent, bytesReceived, lat := runUDPTest(config.TestPort, config.TargetPort, config.Duration, withSleep)
	if profileWait != nil {
		if waitErr := profileWait(); waitErr != nil {
			result.Error = fmt.Sprintf("Profile capture failed: %v", waitErr)
			return result
		}
	}

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

func startUDPlexProcess(projectRoot, configPath string, profileTarget IntegrationProfileTarget) *exec.Cmd {
	binaryName := "udplex_test"
	// Add .exe extension on Windows
	if filepath.Ext(os.Args[0]) == ".exe" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(projectRoot, binaryName)

	cmd := exec.Command(binaryPath, "-c", configPath)
	cmd.Env = append(os.Environ(), formatPprofEnv(profileTarget)...)
	return startManagedProcess(cmd, configPath, "UDPlex started and ready", 10*time.Second)
}

func startUDPlexProcessInNamespace(projectRoot, configPath, netns string, profileTarget IntegrationProfileTarget) *exec.Cmd {
	binaryName := "udplex_test"
	if filepath.Ext(os.Args[0]) == ".exe" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(projectRoot, binaryName)

	cmd := exec.Command("ip", "netns", "exec", netns, binaryPath, "-c", configPath)
	cmd.Env = append(os.Environ(), formatPprofEnv(profileTarget)...)
	return startManagedProcess(cmd, fmt.Sprintf("%s [%s]", configPath, netns), "UDPlex started and ready", 12*time.Second)
}

func startManagedProcess(cmd *exec.Cmd, label, readyNeedle string, timeout time.Duration) *exec.Cmd {
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
		fmt.Printf("Failed to start process %s: %v\n", label, err)
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
			if readyNeedle != "" && strings.Contains(line, readyNeedle) {
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
	select {
	case <-readyCh:
		// Healthy start
		return cmd
	case msg := <-failCh:
		// Early failure detected
		logMu.Lock()
		logs := logBuf.String()
		logMu.Unlock()
		fmt.Printf("Process failed to start for %s: %s\n--- Logs ---\n%s\n------------\n", label, msg, logs)
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
		fmt.Printf("Process did not signal readiness within %v for %s\n--- Logs ---\n%s\n------------\n", timeout, label, logs)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil
	}
}

func handleWGHelperCommand() bool {
	if len(os.Args) < 2 {
		return false
	}

	switch os.Args[1] {
	case "-wg-echo-server":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "missing listen address for -wg-echo-server")
			os.Exit(2)
		}
		if err := runWGEchoServer(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	case "-wg-echo-client":
		target := ""
		duration := TEST_DURATION
		withSleep := false
		for i := 2; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "-target":
				i++
				if i < len(os.Args) {
					target = os.Args[i]
				}
			case "-duration-ms":
				i++
				if i < len(os.Args) {
					ms, err := strconv.Atoi(os.Args[i])
					if err == nil && ms > 0 {
						duration = time.Duration(ms) * time.Millisecond
					}
				}
			case "-with-sleep":
				i++
				if i < len(os.Args) {
					withSleep = os.Args[i] == "1" || strings.EqualFold(os.Args[i], "true")
				}
			}
		}
		if target == "" {
			fmt.Fprintln(os.Stderr, "missing -target for -wg-echo-client")
			os.Exit(2)
		}
		metrics, err := runWGEchoClient(target, duration, withSleep)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := json.NewEncoder(os.Stdout).Encode(metrics); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	default:
		return false
	}
}

func supportsWGIntegration() (bool, string) {
	if runtime.GOOS != "linux" {
		return false, fmt.Sprintf("requires Linux, current GOOS=%s", runtime.GOOS)
	}
	if !isRootUser() {
		return false, "requires root privileges for ip netns and /dev/net/tun"
	}
	if _, err := exec.LookPath("ip"); err != nil {
		return false, "requires iproute2 (`ip` command)"
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return false, "requires /dev/net/tun"
	}
	return true, ""
}

func isRootUser() bool {
	cmd := exec.Command("id", "-u")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "0"
}

func runWireGuardForwardIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	return runWireGuardIntegration(projectRoot, config, label, withSleep, wgModeForward)
}

func runWireGuardTCPTunnelIntegration(projectRoot, examplesDir string, config TestConfig, label string, withSleep bool) TestResult {
	return runWireGuardIntegration(projectRoot, config, label, withSleep, wgModeTCPTunnel)
}

func runWireGuardIntegration(projectRoot string, config TestConfig, label string, withSleep bool, mode string) TestResult {
	result := TestResult{
		ConfigName:    fmt.Sprintf("%s %s", config.Name, label),
		TotalDuration: config.Duration,
	}

	env, err := setupWGNamespaces()
	if err != nil {
		result.Error = fmt.Sprintf("failed to prepare network namespaces: %v", err)
		return result
	}
	defer env.cleanup()

	tempDir, err := os.MkdirTemp("", "udplex-wg-integration-*")
	if err != nil {
		result.Error = fmt.Sprintf("failed to create temp dir: %v", err)
		return result
	}
	defer os.RemoveAll(tempDir)

	clientConfigPath, serverConfigPath, err := writeWGConfigs(projectRoot, tempDir, mode)
	if err != nil {
		result.Error = fmt.Sprintf("failed to write WireGuard configs: %v", err)
		return result
	}

	serverProfileTarget := newNamespaceProfileTarget("server", env.serverNS)
	clientProfileTarget := newNamespaceProfileTarget("client", env.clientNS)

	serverProcess := startUDPlexProcessInNamespace(projectRoot, serverConfigPath, env.serverNS, serverProfileTarget)
	if serverProcess == nil {
		result.Error = "failed to start WireGuard server UDPlex process"
		return result
	}
	defer stopProcess(serverProcess)

	clientProcess := startUDPlexProcessInNamespace(projectRoot, clientConfigPath, env.clientNS, clientProfileTarget)
	if clientProcess == nil {
		result.Error = "failed to start WireGuard client UDPlex process"
		return result
	}
	defer stopProcess(clientProcess)

	echoServer := startWGEchoServerInNamespace(env.serverNS, wgInnerServerAddr)
	if echoServer == nil {
		result.Error = "failed to start WireGuard echo server"
		return result
	}
	defer stopProcess(echoServer)

	time.Sleep(1500 * time.Millisecond)

	profileWait, err := maybeStartProfiles(projectRoot, config.Name, withSleep, []IntegrationProfileTarget{
		clientProfileTarget,
		serverProfileTarget,
	})
	if err != nil {
		result.Error = fmt.Sprintf("failed to start profiles: %v", err)
		return result
	}

	metrics, err := runWGEchoClientInNamespace(env.clientNS, wgInnerServerAddr, config.Duration, withSleep)
	if profileWait != nil {
		if waitErr := profileWait(); waitErr != nil && err == nil {
			err = fmt.Errorf("profile capture failed: %w", waitErr)
		}
	}
	if err != nil {
		result.Error = fmt.Sprintf("WireGuard echo client failed: %v", err)
		return result
	}

	populateResultFromWGEchoMetrics(&result, metrics, config.Duration, withSleep)
	return result
}

func maybeStartProfiles(projectRoot, configName string, withSleep bool, targets []IntegrationProfileTarget) (func() error, error) {
	if withSleep || integrationSelection.ProfileDir == "" || len(targets) == 0 {
		return nil, nil
	}
	if len(integrationSelection.ProfileTests) > 0 && !integrationConfigSelected(configName, integrationSelection.ProfileTests) {
		return nil, nil
	}

	profileSecs := integrationSelection.ProfileSecs
	if profileSecs <= 0 {
		profileSecs = int(TEST_DURATION / time.Second)
	}

	profileDir := integrationSelection.ProfileDir
	if !filepath.IsAbs(profileDir) {
		profileDir = filepath.Join(projectRoot, profileDir)
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, err
	}

	type profileTask struct {
		name       string
		cmd        *exec.Cmd
		raw        string
		target     IntegrationProfileTarget
		artifactID string
	}

	tasks := make([]profileTask, 0, len(targets))
	for _, target := range targets {
		artifactID := profileArtifactBaseName(configName, target.Name)
		rawPath := filepath.Join(profileDir, artifactID+".prof")
		profileURL := fmt.Sprintf("http://%s/debug/pprof/profile?seconds=%d", target.PprofAddr, profileSecs)

		var cmd *exec.Cmd
		if target.Namespace != "" {
			cmd = exec.Command("ip", "netns", "exec", target.Namespace, "curl", "-sS", profileURL, "-o", rawPath)
		} else {
			cmd = exec.Command("curl", "-sS", profileURL, "-o", rawPath)
		}

		tasks = append(tasks, profileTask{
			name:       target.Name,
			cmd:        cmd,
			raw:        rawPath,
			target:     target,
			artifactID: artifactID,
		})
	}

	for i := range tasks {
		if err := tasks[i].cmd.Start(); err != nil {
			return nil, fmt.Errorf("start %s profile capture: %w", tasks[i].name, err)
		}
	}

	return func() error {
		binaryPath := filepath.Join(projectRoot, "udplex_test")
		indexEntries := make([]ProfileArtifactIndexItem, 0, len(tasks))
		for _, task := range tasks {
			if err := task.cmd.Wait(); err != nil {
				return fmt.Errorf("%s profile capture failed: %w", task.name, err)
			}
			if err := writePprofArtifacts(binaryPath, task.raw); err != nil {
				return fmt.Errorf("%s profile post-processing failed: %w", task.name, err)
			}
			indexEntries = append(indexEntries, buildProfileArtifactIndexItem(configName, task))
		}
		if err := upsertProfileArtifactIndex(profileDir, indexEntries); err != nil {
			return fmt.Errorf("write profile index: %w", err)
		}
		if err := writeProfileSummary(profileDir); err != nil {
			return fmt.Errorf("write profile summary: %w", err)
		}
		return nil
	}, nil
}

func writePprofArtifacts(binaryPath, rawProfilePath string) error {
	topCmd := exec.Command("go", "tool", "pprof", "-top", "-nodecount=30", binaryPath, rawProfilePath)
	topOutput, err := topCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("generate top report: %v: %s", err, strings.TrimSpace(string(topOutput)))
	}
	if err := os.WriteFile(strings.TrimSuffix(rawProfilePath, ".prof")+".top.txt", topOutput, 0o644); err != nil {
		return err
	}

	svgCmd := exec.Command("go", "tool", "pprof", "-svg", binaryPath, rawProfilePath)
	svgOutput, err := svgCmd.CombinedOutput()
	if err == nil {
		if writeErr := os.WriteFile(strings.TrimSuffix(rawProfilePath, ".prof")+".svg", svgOutput, 0o644); writeErr != nil {
			return writeErr
		}
	}

	return nil
}

func newLocalProfileTarget(configName, configFile string, index int) (IntegrationProfileTarget, error) {
	port, err := allocateLocalTCPPort()
	if err != nil {
		return IntegrationProfileTarget{}, err
	}

	baseName := strings.TrimSuffix(filepath.Base(configFile), filepath.Ext(configFile))
	if baseName == "" {
		baseName = fmt.Sprintf("process_%d", index+1)
	}

	return IntegrationProfileTarget{
		Name:      baseName,
		PprofAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, nil
}

func newNamespaceProfileTarget(name, namespace string) IntegrationProfileTarget {
	return IntegrationProfileTarget{
		Name:      name,
		Namespace: namespace,
		PprofAddr: "127.0.0.1:6060",
	}
}

func formatPprofEnv(target IntegrationProfileTarget) []string {
	if strings.TrimSpace(target.PprofAddr) == "" {
		return nil
	}
	return []string{fmt.Sprintf("UDPLEX_PPROF_ADDR=%s", target.PprofAddr)}
}

func allocateLocalTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", listener.Addr())
	}
	return tcpAddr.Port, nil
}

func normalizeProfileTargetName(name string) string {
	name = normalizeIntegrationTestName(name)
	if name == "" {
		return "process"
	}
	return name
}

func profileArtifactBaseName(configName, targetName string) string {
	configPart := normalizeIntegrationTestName(configName)
	targetPart := normalizeProfileTargetName(targetName)

	if configPart == "" {
		return targetPart
	}
	if targetPart == "" {
		return configPart
	}
	if targetPart == configPart || strings.HasPrefix(targetPart, configPart+"_") {
		return targetPart
	}
	return configPart + "_" + targetPart
}

func buildProfileArtifactIndexItem(configName string, task struct {
	name       string
	cmd        *exec.Cmd
	raw        string
	target     IntegrationProfileTarget
	artifactID string
}) ProfileArtifactIndexItem {
	topReport := strings.TrimSuffix(task.raw, ".prof") + ".top.txt"
	svgReport := strings.TrimSuffix(task.raw, ".prof") + ".svg"
	item := ProfileArtifactIndexItem{
		ConfigName:       configName,
		ConfigNormalized: normalizeIntegrationTestName(configName),
		TargetName:       task.target.Name,
		TargetNormalized: normalizeProfileTargetName(task.target.Name),
		Namespace:        task.target.Namespace,
		PprofAddr:        task.target.PprofAddr,
		BaseName:         task.artifactID,
		RawProfile:       task.raw,
		TopReport:        topReport,
		TopHotspots:      extractTopHotspots(topReport, 5),
	}
	if _, err := os.Stat(svgReport); err == nil {
		item.SVGReport = svgReport
	}
	return item
}

func extractTopHotspots(topReportPath string, limit int) []string {
	content, err := os.ReadFile(topReportPath)
	if err != nil {
		return nil
	}

	lines := strings.Split(string(content), "\n")
	hotspots := make([]string, 0, limit)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		if !strings.HasSuffix(fields[1], "%") || !strings.HasSuffix(fields[4], "%") {
			continue
		}
		symbol := fields[len(fields)-1]
		if symbol == "cum%" || symbol == "Type:" || symbol == "Time:" || symbol == "Duration:" {
			continue
		}
		if strings.HasSuffix(symbol, "%") {
			continue
		}
		if _, err := strconv.ParseFloat(strings.TrimSuffix(fields[1], "%"), 64); err != nil {
			continue
		}
		hotspots = append(hotspots, fmt.Sprintf("%s (%s flat)", symbol, fields[1]))
		if len(hotspots) >= limit {
			break
		}
	}
	return hotspots
}

func upsertProfileArtifactIndex(profileDir string, newEntries []ProfileArtifactIndexItem) error {
	indexPath := filepath.Join(profileDir, "profile_index.json")
	index := ProfileArtifactIndex{}

	if content, err := os.ReadFile(indexPath); err == nil {
		if err := json.Unmarshal(content, &index); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	existingByBase := make(map[string]ProfileArtifactIndexItem, len(index.Entries)+len(newEntries))
	for _, entry := range index.Entries {
		existingByBase[entry.BaseName] = entry
	}
	for _, entry := range newEntries {
		existingByBase[entry.BaseName] = entry
	}

	index.Entries = index.Entries[:0]
	for _, entry := range existingByBase {
		index.Entries = append(index.Entries, entry)
	}
	sort.Slice(index.Entries, func(i, j int) bool {
		if index.Entries[i].ConfigNormalized == index.Entries[j].ConfigNormalized {
			return index.Entries[i].TargetNormalized < index.Entries[j].TargetNormalized
		}
		return index.Entries[i].ConfigNormalized < index.Entries[j].ConfigNormalized
	})
	index.GeneratedAt = time.Now().UTC()

	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(indexPath, data, 0o644)
}

func writeProfileSummary(profileDir string) error {
	indexPath := filepath.Join(profileDir, "profile_index.json")
	content, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}

	index := ProfileArtifactIndex{}
	if err := json.Unmarshal(content, &index); err != nil {
		return err
	}

	var builder strings.Builder
	builder.WriteString("# Profile Summary\n\n")
	builder.WriteString(fmt.Sprintf("Generated: %s\n\n", index.GeneratedAt.Format(time.RFC3339)))
	builder.WriteString("| Test | Target | Namespace | Pprof Addr | Top Hotspots | Raw | Top |\n")
	builder.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")

	for _, entry := range index.Entries {
		namespace := entry.Namespace
		if namespace == "" {
			namespace = "-"
		}
		pprofAddr := entry.PprofAddr
		if pprofAddr == "" {
			pprofAddr = "-"
		}
		hotspots := "-"
		if len(entry.TopHotspots) > 0 {
			hotspots = strings.Join(entry.TopHotspots, "<br>")
		}

		builder.WriteString(fmt.Sprintf(
			"| %s | %s | %s | %s | %s | `%s` | `%s` |\n",
			entry.ConfigNormalized,
			entry.TargetNormalized,
			namespace,
			pprofAddr,
			hotspots,
			filepath.Base(entry.RawProfile),
			filepath.Base(entry.TopReport),
		))
	}

	return os.WriteFile(filepath.Join(profileDir, "profile_summary.md"), []byte(builder.String()), 0o644)
}

type wgNamespaceEnv struct {
	clientNS string
	serverNS string
	cleanup  func()
}

func setupWGNamespaces() (*wgNamespaceEnv, error) {
	suffix := time.Now().UnixNano() % 100000
	clientNS := fmt.Sprintf("udwgc-%d", suffix)
	serverNS := fmt.Sprintf("udwgs-%d", suffix)
	clientIf := fmt.Sprintf("vethc%d", suffix)
	serverIf := fmt.Sprintf("veths%d", suffix)

	cleanup := func() {
		_ = runCommand("ip", "netns", "del", clientNS)
		_ = runCommand("ip", "netns", "del", serverNS)
	}

	cleanup()

	commands := [][]string{
		{"ip", "netns", "add", clientNS},
		{"ip", "netns", "add", serverNS},
		{"ip", "link", "add", clientIf, "type", "veth", "peer", "name", serverIf},
		{"ip", "link", "set", clientIf, "netns", clientNS},
		{"ip", "link", "set", serverIf, "netns", serverNS},
		{"ip", "-n", clientNS, "link", "set", "lo", "up"},
		{"ip", "-n", serverNS, "link", "set", "lo", "up"},
		{"ip", "-n", clientNS, "addr", "add", wgOuterClientIP + "/30", "dev", clientIf},
		{"ip", "-n", serverNS, "addr", "add", wgOuterServerIP + "/30", "dev", serverIf},
		{"ip", "-n", clientNS, "link", "set", clientIf, "up"},
		{"ip", "-n", serverNS, "link", "set", serverIf, "up"},
	}

	for _, cmd := range commands {
		if err := runCommand(cmd[0], cmd[1:]...); err != nil {
			cleanup()
			return nil, err
		}
	}

	return &wgNamespaceEnv{
		clientNS: clientNS,
		serverNS: serverNS,
		cleanup:  cleanup,
	}, nil
}

func writeWGConfigs(projectRoot, tempDir, mode string) (string, string, error) {
	clientPath := filepath.Join(tempDir, "wg_client.yaml")
	serverPath := filepath.Join(tempDir, "wg_server.yaml")

	var clientExample string
	var serverExample string
	switch mode {
	case wgModeForward:
		clientExample = "wg_component_forward_client.yaml"
		serverExample = "wg_component_forward_server.yaml"
	case wgModeTCPTunnel:
		clientExample = "wg_component_tcp_tunnel_client.yaml"
		serverExample = "wg_component_tcp_tunnel_server.yaml"
	default:
		return "", "", fmt.Errorf("unknown WireGuard integration mode: %s", mode)
	}

	examplesDir := filepath.Join(projectRoot, "examples")
	clientPriv, clientPub, err := generateWGKeyPair()
	if err != nil {
		return "", "", fmt.Errorf("generate client WireGuard key pair: %w", err)
	}
	serverPriv, serverPub, err := generateWGKeyPair()
	if err != nil {
		return "", "", fmt.Errorf("generate server WireGuard key pair: %w", err)
	}
	replacements := strings.NewReplacer(
		"SERVER_IP", wgOuterServerIP,
		"udplex-server", wgOuterServerIP,
		wgExampleClientPrivateKey, clientPriv,
		wgExampleClientPublicKey, clientPub,
		wgExampleServerPrivateKey, serverPriv,
		wgExampleServerPublicKey, serverPub,
	)

	clientConfig, err := renderWGConfigFromExample(filepath.Join(examplesDir, clientExample), replacements)
	if err != nil {
		return "", "", err
	}
	serverConfig, err := renderWGConfigFromExample(filepath.Join(examplesDir, serverExample), replacements)
	if err != nil {
		return "", "", err
	}

	if err := os.WriteFile(clientPath, []byte(clientConfig), 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(serverPath, []byte(serverConfig), 0o644); err != nil {
		return "", "", err
	}
	return clientPath, serverPath, nil
}

func generateWGKeyPair() (string, string, error) {
	private := make([]byte, 32)
	if _, err := rand.Read(private); err != nil {
		return "", "", err
	}
	private[0] &= 248
	private[31] &= 127
	private[31] |= 64

	public, err := curve25519Public(private)
	if err != nil {
		return "", "", err
	}

	return hex.EncodeToString(private), hex.EncodeToString(public), nil
}

func curve25519Public(private []byte) ([]byte, error) {
	if len(private) != 32 {
		return nil, fmt.Errorf("invalid WireGuard private key length: %d", len(private))
	}
	pub, err := curve25519.X25519(private, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	return pub, nil
}

func renderWGConfigFromExample(path string, replacer *strings.Replacer) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return replacer.Replace(string(content)), nil
}

func startWGEchoServerInNamespace(netns, listenAddr string) *exec.Cmd {
	executable, err := os.Executable()
	if err != nil {
		fmt.Printf("Failed to resolve test executable: %v\n", err)
		return nil
	}
	cmd := exec.Command("ip", "netns", "exec", netns, executable, "-wg-echo-server", listenAddr)
	return startManagedProcess(cmd, fmt.Sprintf("wg-echo-server [%s]", netns), "WG echo server ready", 10*time.Second)
}

func runWGEchoClientInNamespace(netns, target string, duration time.Duration, withSleep bool) (*WGEchoMetrics, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	sleepFlag := "0"
	if withSleep {
		sleepFlag = "1"
	}
	cmd := exec.Command(
		"ip", "netns", "exec", netns,
		executable, "-wg-echo-client",
		"-target", target,
		"-duration-ms", strconv.Itoa(int(duration/time.Millisecond)),
		"-with-sleep", sleepFlag,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(output)))
	}

	var metrics WGEchoMetrics
	if err := json.Unmarshal(output, &metrics); err != nil {
		return nil, fmt.Errorf("failed to parse echo client metrics: %w, output=%s", err, strings.TrimSpace(string(output)))
	}
	return &metrics, nil
}

func populateResultFromWGEchoMetrics(result *TestResult, metrics *WGEchoMetrics, duration time.Duration, withSleep bool) {
	result.Sent = metrics.Sent
	result.Received = metrics.Received
	result.ErrorPackets = metrics.ErrorPackets
	result.BytesSent = metrics.BytesSent
	result.BytesReceived = metrics.BytesReceived
	result.PacketSizeBytes = metrics.PacketSizeBytes
	result.AvgLatencyMs = metrics.AvgLatencyMs
	result.P50LatencyMs = metrics.P50LatencyMs
	result.P95LatencyMs = metrics.P95LatencyMs
	result.P99LatencyMs = metrics.P99LatencyMs
	result.MinLatencyMs = metrics.MinLatencyMs
	result.MaxLatencyMs = metrics.MaxLatencyMs

	if result.Sent > 0 {
		result.LossRate = float64(result.Sent-result.Received) / float64(result.Sent)
		result.Throughput = float64(result.Received) / duration.Seconds()
	}
	if result.BytesReceived > 0 {
		result.Mbps = (float64(result.BytesReceived) * 8.0) / duration.Seconds() / 1e6
		result.TotalMBytes = float64(result.BytesReceived) / 1e6
	}

	if result.Sent == 0 {
		result.Error = "No packets sent"
		return
	}
	if result.ErrorPackets > 0 {
		result.Error = fmt.Sprintf("Error packets detected: %d", result.ErrorPackets)
		return
	}
	if result.Received == 0 {
		result.Error = "No packets received"
		return
	}
	if withSleep && result.LossRate > MAX_PACKET_LOSS {
		result.Error = fmt.Sprintf("High packet loss: %.2f%%", result.LossRate*100)
		return
	}
	result.Success = true
}

func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runWGEchoServer(listenAddr string) error {
	deadline := time.Now().Add(10 * time.Second)
	var conn *net.UDPConn

	for {
		udpAddr, err := net.ResolveUDPAddr("udp", listenAddr)
		if err == nil {
			conn, err = net.ListenUDP("udp", udpAddr)
			if err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("failed to bind echo server on %s", listenAddr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer conn.Close()

	fmt.Println("WG echo server ready")

	buffer := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return err
		}
		if _, err := conn.WriteToUDP(buffer[:n], addr); err != nil {
			return err
		}
	}
}

func runWGEchoClient(target string, duration time.Duration, withSleep bool) (*WGEchoMetrics, error) {
	var sentCount, receivedCount int64
	var bytesSentCount, bytesReceivedCount int64
	var errorCount int64
	var wg sync.WaitGroup
	var seenIDs Bitset

	var latCount int64
	var latSumNs int64
	var latMinNs int64
	var latMaxNs int64
	const sampleCap = 20000
	latSample := make([]int64, 0, sampleCap)

	targetAddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, targetAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.SetWriteBuffer(2 * 1024 * 1024); err != nil {
		fmt.Printf("Warning: Failed to set write buffer: %v\n", err)
	}
	if err := conn.SetReadBuffer(2 * 1024 * 1024); err != nil {
		fmt.Printf("Warning: Failed to set read buffer: %v\n", err)
	}

	conn.SetReadDeadline(time.Now().Add(duration + 5*time.Second))

	wg.Add(1)
	go func() {
		defer wg.Done()
		buffer := make([]byte, 2048)
		for {
			n, err := conn.Read(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					return
				}
				continue
			}
			if n != int(unsafe.Sizeof(PacketData{})) {
				continue
			}

			var packet PacketData
			copy((*[unsafe.Sizeof(PacketData{})]byte)(unsafe.Pointer(&packet))[:], buffer[:n])
			if calculateChecksum(packet.ID, packet.Timestamp, packet.Payload[:]) != packet.Checksum {
				atomic.AddInt64(&errorCount, 1)
				continue
			}

			idx := packet.ID - 1
			if packet.ID > 0 && !seenIDs.Test(idx) {
				nowNs := time.Now().UnixNano()
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
				seenIDs.Set(idx)
				atomic.AddInt64(&receivedCount, 1)
				atomic.AddInt64(&bytesReceivedCount, int64(n))
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		startTime := time.Now()
		var packetID uint32
		for time.Since(startTime) < duration {
			packetID++

			var packet PacketData
			packet.ID = packetID
			packet.Timestamp = time.Now().UnixNano()
			rand.Read(packet.Payload[:])
			packet.Checksum = calculateChecksum(packet.ID, packet.Timestamp, packet.Payload[:])

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

	metrics := &WGEchoMetrics{
		Sent:            atomic.LoadInt64(&sentCount),
		Received:        atomic.LoadInt64(&receivedCount),
		ErrorPackets:    atomic.LoadInt64(&errorCount),
		BytesSent:       atomic.LoadInt64(&bytesSentCount),
		BytesReceived:   atomic.LoadInt64(&bytesReceivedCount),
		PacketSizeBytes: int(unsafe.Sizeof(PacketData{})),
	}

	if latCount > 0 {
		sort.Slice(latSample, func(i, j int) bool { return latSample[i] < latSample[j] })
		metrics.AvgLatencyMs = (float64(latSumNs) / float64(latCount)) / 1e6
		metrics.MinLatencyMs = float64(latMinNs) / 1e6
		metrics.MaxLatencyMs = float64(latMaxNs) / 1e6
		metrics.P50LatencyMs = percentileMs(latSample, 0.50)
		metrics.P95LatencyMs = percentileMs(latSample, 0.95)
		metrics.P99LatencyMs = percentileMs(latSample, 0.99)
	}

	return metrics, nil
}

func percentileMs(samples []int64, percentile float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	rank := int(percentile*float64(len(samples)-1) + 0.5)
	if rank < 0 {
		rank = 0
	}
	if rank >= len(samples) {
		rank = len(samples) - 1
	}
	return float64(samples[rank]) / 1e6
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

	// Adaptive rate limiting
	var consecutiveErrors int64

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

		// Set socket buffer sizes for better performance
		if err := conn.SetReadBuffer(2 * 1024 * 1024); err != nil {
			fmt.Printf("Warning: Failed to set read buffer: %v\n", err)
		}
		if err := conn.SetWriteBuffer(2 * 1024 * 1024); err != nil {
			fmt.Printf("Warning: Failed to set write buffer: %v\n", err)
		}

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

		// Set socket buffer sizes for better performance
		if err := conn.SetWriteBuffer(2 * 1024 * 1024); err != nil {
			fmt.Printf("Warning: Failed to set write buffer: %v\n", err)
		}
		if err := conn.SetReadBuffer(2 * 1024 * 1024); err != nil {
			fmt.Printf("Warning: Failed to set read buffer: %v\n", err)
		}

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
				// Reset error tracking on success
				atomic.StoreInt64(&consecutiveErrors, 0)
			} else {
				// Track consecutive errors for adaptive backoff
				errors := atomic.AddInt64(&consecutiveErrors, 1)
				if errors > 10 {
					// Adaptive backoff when seeing too many errors
					time.Sleep(time.Duration(errors) * 10 * time.Microsecond)
				}
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
