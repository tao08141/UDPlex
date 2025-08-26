package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	ConfigName    string
	Sent          int64
	Received      int64
	ErrorPackets  int64
	LossRate      float64
	DataIntegrity bool
	TotalDuration time.Duration
	Throughput    float64 // packets per second
	Success       bool
	Error         string
}

type PacketData struct {
	ID        uint32
	Timestamp int64
	Checksum  [32]byte
	Payload   [1024]byte
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
			ConfigFiles: []string{"basic.json"},
			TestPort:    LISTEN_PORT,
			TargetPort:  SEND_PORT,
			Duration:    TEST_DURATION,
		},
		{
			Name:        "Auth Client-Server",
			ConfigFiles: []string{"auth_server.json", "auth_client.json"},
			TestPort:    LISTEN_PORT,
			TargetPort:  SEND_PORT,
			Duration:    TEST_DURATION,
		},
		{
			Name:        "Load Balancer",
			ConfigFiles: []string{"load_balancer_test.json"},
			TestPort:    LISTEN_PORT,
			TargetPort:  SEND_PORT,
			Duration:    TEST_DURATION,
		},
		{
			Name:        "TCP Tunnel",
			ConfigFiles: []string{"tcp_tunnel_server.json", "tcp_tunnel_client.json"},
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
		fmt.Printf("%-20s | Sent: %6d | Received: %6d | Err: %4d | Loss: %5.2f%% | Throughput: %8.0f pps | Status: %s\n",
			result.ConfigName,
			result.Sent,
			result.Received,
			result.ErrorPackets,
			result.LossRate*100,
			result.Throughput,
			func() string {
				if result.Success {
					passed++
					return "PASS"
				}
				return "FAIL"
			}())
	}

	fmt.Printf("\nTotal: %d/%d tests passed\n", passed, len(results))

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
	sent, received, errorPackets := runUDPTest(config.TestPort, config.TargetPort, config.Duration, withSleep)

	result.Sent = sent
	result.Received = received
	result.ErrorPackets = errorPackets
	result.TotalDuration = config.Duration

	if sent > 0 {
		result.LossRate = float64(sent-received) / float64(sent)
		result.Throughput = float64(received) / config.Duration.Seconds()
	}

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

func runUDPTest(listenPort, sendPort int, duration time.Duration, withSleep bool) (sent, received, errorPackets int64) {
	var sentCount, receivedCount int64
	var errorCount int64
	var wg sync.WaitGroup

	// Bitmap to deduplicate received packet IDs (optional but keeps counts accurate)
	var seenIDs Bitset

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
						seenIDs.Set(idx)
						atomic.AddInt64(&receivedCount, 1)
					}
				} else {
					// Fallback: count ID==0 packets without dedup to avoid underflow
					atomic.AddInt64(&receivedCount, 1)
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
			_, err := conn.Write(data)
			if err == nil {
				atomic.AddInt64(&sentCount, 1)
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

	return sent, received, errorPackets
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
