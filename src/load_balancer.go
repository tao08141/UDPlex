package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TrafficSample represents a traffic sample for a specific interval
type TrafficSample struct {
	Bytes   uint64
	Packets uint64
}

// TrafficStats holds traffic statistics using sliding window
type TrafficStats struct {
	samples        []TrafficSample // Ring buffer for samples
	currentIndex   int             // Current position in ring buffer
	windowCount    int             // Number of samples in window
	currentBytes   uint64          // Bytes accumulated in current sample period
	currentPackets uint64          // Packets accumulated in current sample period
	mutex          sync.RWMutex
}

// LoadBalancerComponent implements intelligent packet distribution based on traffic and rules
type LoadBalancerComponent struct {
	BaseComponent
	detour         []LoadBalancerDetourRule
	sampleInterval time.Duration
	windowSize     time.Duration
	stats          *TrafficStats
	packetSeq      uint64
	mutex          sync.RWMutex
}

// NewLoadBalancerComponent creates a new load balancer component
func NewLoadBalancerComponent(cfg LoadBalancerComponentConfig, router *Router) (*LoadBalancerComponent, error) {
	// Parse sample interval
	sampleInterval, err := time.ParseDuration(cfg.SampleInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid sample_interval: %w", err)
	}

	// Parse window size
	windowSize, err := time.ParseDuration(cfg.WindowSize)
	if err != nil {
		return nil, fmt.Errorf("invalid window_size: %w", err)
	}

	// Calculate number of samples needed for the window
	windowCount := int(windowSize / sampleInterval)
	if windowCount < 1 {
		windowCount = 1
	}

	lb := &LoadBalancerComponent{
		BaseComponent:  NewBaseComponent(cfg.Tag, router, 0),
		detour:         cfg.Detour,
		sampleInterval: sampleInterval,
		windowSize:     windowSize,
		stats: &TrafficStats{
			samples:     make([]TrafficSample, windowCount),
			windowCount: windowCount,
		},
		packetSeq: 0,
	}

	return lb, nil
}

// GetTag returns the component's tag
func (lb *LoadBalancerComponent) GetTag() string {
	return lb.tag
}

// Start initializes the load balancer component
func (lb *LoadBalancerComponent) Start() error {
	logger.Infof("%s: Starting load balancer component", lb.tag)

	// Start statistics sampling goroutine
	go lb.statsSampler()

	return nil
}

// Stop stops the load balancer component
func (lb *LoadBalancerComponent) Stop() error {
	close(lb.GetStopChannel())
	return nil
}

func (lb *LoadBalancerComponent) SendPacket(packet *Packet, addr any) error {
	return nil
}

// HandlePacket processes and routes packets based on load balancing rules
func (lb *LoadBalancerComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	// Update traffic statistics
	lb.updateStats(packet)

	// Get current stats for rule evaluation
	bps, pps := lb.getCurrentStats()

	// Increment packet sequence
	lb.mutex.Lock()
	lb.packetSeq++
	seq := lb.packetSeq
	lb.mutex.Unlock()

	// Evaluate detour rules to find matching target
	target := lb.evaluateRules(seq, bps, pps)
	if target == "" {
		return fmt.Errorf("%s: No matching rule found for packet", lb.tag)
	}

	// Route packet to the determined target
	if err := lb.router.Route(packet, []string{target}); err != nil {
		return fmt.Errorf("routing error: %w", err)
	}

	return nil
}

// updateStats updates traffic statistics with the current packet
func (lb *LoadBalancerComponent) updateStats(packet *Packet) {
	lb.stats.mutex.Lock()
	defer lb.stats.mutex.Unlock()

	lb.stats.currentBytes += uint64(len(packet.GetData()))
	lb.stats.currentPackets++
}

// getCurrentStats returns average BPS and PPS values across the window
func (lb *LoadBalancerComponent) getCurrentStats() (uint64, uint64) {
	lb.stats.mutex.RLock()
	defer lb.stats.mutex.RUnlock()

	var totalBytes, totalPackets uint64
	sampleCount := 0

	// Sum up all samples in the window
	for i := 0; i < lb.stats.windowCount; i++ {
		sample := lb.stats.samples[i]
		if sample.Bytes > 0 || sample.Packets > 0 {
			totalBytes += sample.Bytes
			totalPackets += sample.Packets
			sampleCount++
		}
	}

	// Add current accumulating sample
	totalBytes += lb.stats.currentBytes
	totalPackets += lb.stats.currentPackets
	if lb.stats.currentBytes > 0 || lb.stats.currentPackets > 0 {
		sampleCount++
	}

	// Calculate average per sample interval
	if sampleCount == 0 {
		return 0, 0
	}

	avgBytesPerInterval := totalBytes / uint64(sampleCount)
	avgPacketsPerInterval := totalPackets / uint64(sampleCount)

	// Convert to per-second rates
	intervalSeconds := lb.sampleInterval.Seconds()
	bps := uint64(float64(avgBytesPerInterval) / intervalSeconds)
	pps := uint64(float64(avgPacketsPerInterval) / intervalSeconds)

	return bps, pps
}

// statsSampler periodically samples statistics based on sample interval
func (lb *LoadBalancerComponent) statsSampler() {
	ticker := time.NewTicker(lb.sampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			lb.sampleStats()
		case <-lb.GetStopChannel():
			return
		}
	}
}

// sampleStats moves current accumulated stats to the sample window
func (lb *LoadBalancerComponent) sampleStats() {
	lb.stats.mutex.Lock()
	defer lb.stats.mutex.Unlock()

	// Store current sample in ring buffer
	lb.stats.samples[lb.stats.currentIndex] = TrafficSample{
		Bytes:   lb.stats.currentBytes,
		Packets: lb.stats.currentPackets,
	}

	// Move to next position in ring buffer
	lb.stats.currentIndex = (lb.stats.currentIndex + 1) % lb.stats.windowCount

	// Reset current accumulators
	lb.stats.currentBytes = 0
	lb.stats.currentPackets = 0
}

// evaluateRules evaluates all detour rules and returns the target for the first matching rule
func (lb *LoadBalancerComponent) evaluateRules(seq, bps, pps uint64) string {
	for _, rule := range lb.detour {
		if lb.evaluateExpression(rule.Rule, seq, bps, pps) {
			return rule.Target
		}
	}
	return ""
}

// evaluateExpression evaluates a rule expression with the given variables
func (lb *LoadBalancerComponent) evaluateExpression(expr string, seq, bps, pps uint64) bool {
	// Replace variables in expression
	expr = strings.ReplaceAll(expr, "$seq", fmt.Sprintf("%d", seq))
	expr = strings.ReplaceAll(expr, "$bps", fmt.Sprintf("%d", bps))
	expr = strings.ReplaceAll(expr, "$pps", fmt.Sprintf("%d", pps))

	// Parse and evaluate the expression
	result, err := lb.parseAndEvaluate(expr)
	if err != nil {
		logger.Errorf("%s: Error evaluating expression '%s': %v", lb.tag, expr, err)
		return false
	}

	// Non-zero result means condition is met
	return result != 0
}

// parseAndEvaluate parses and evaluates a mathematical/logical expression
func (lb *LoadBalancerComponent) parseAndEvaluate(expr string) (int64, error) {
	// Parse the expression into an AST
	node, err := parser.ParseExpr(expr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse expression: %w", err)
	}

	// Evaluate the AST
	return lb.evaluateNode(node)
}

// evaluateNode recursively evaluates AST nodes
func (lb *LoadBalancerComponent) evaluateNode(node ast.Expr) (int64, error) {
	switch n := node.(type) {
	case *ast.BasicLit:
		// Handle literal values (numbers)
		if n.Kind == token.INT {
			val, err := strconv.ParseInt(n.Value, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid integer: %s", n.Value)
			}
			return val, nil
		}
		return 0, fmt.Errorf("unsupported literal type: %s", n.Kind)

	case *ast.BinaryExpr:
		// Handle binary operations
		left, err := lb.evaluateNode(n.X)
		if err != nil {
			return 0, err
		}

		right, err := lb.evaluateNode(n.Y)
		if err != nil {
			return 0, err
		}

		switch n.Op {
		case token.ADD:
			return left + right, nil
		case token.SUB:
			return left - right, nil
		case token.MUL:
			return left * right, nil
		case token.QUO:
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			return left / right, nil
		case token.REM:
			if right == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			return left % right, nil
		case token.EQL:
			if left == right {
				return 1, nil
			}
			return 0, nil
		case token.NEQ:
			if left != right {
				return 1, nil
			}
			return 0, nil
		case token.LSS:
			if left < right {
				return 1, nil
			}
			return 0, nil
		case token.LEQ:
			if left <= right {
				return 1, nil
			}
			return 0, nil
		case token.GTR:
			if left > right {
				return 1, nil
			}
			return 0, nil
		case token.GEQ:
			if left >= right {
				return 1, nil
			}
			return 0, nil
		case token.LAND:
			if left != 0 && right != 0 {
				return 1, nil
			}
			return 0, nil
		case token.LOR:
			if left != 0 || right != 0 {
				return 1, nil
			}
			return 0, nil
		default:
			return 0, fmt.Errorf("unsupported binary operator: %s", n.Op)
		}

	case *ast.UnaryExpr:
		// Handle unary operations
		operand, err := lb.evaluateNode(n.X)
		if err != nil {
			return 0, err
		}

		switch n.Op {
		case token.NOT:
			if operand == 0 {
				return 1, nil
			}
			return 0, nil
		case token.SUB:
			return -operand, nil
		case token.ADD:
			return operand, nil
		default:
			return 0, fmt.Errorf("unsupported unary operator: %s", n.Op)
		}

	case *ast.ParenExpr:
		// Handle parentheses
		return lb.evaluateNode(n.X)

	default:
		return 0, fmt.Errorf("unsupported expression type: %T", node)
	}
}
