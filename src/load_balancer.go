package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// CompiledExpression represents a pre-compiled expression for faster evaluation
type CompiledExpression struct {
	program      *vm.Program
	varKeys      []string // Variables found in the expression (e.g., "seq", "bps", "pps")
	canCache     bool     // Whether this expression can be cached (no seq/size dependency)
	cachedResult uint64   // Cached evaluation result (0=false, 1=true) - atomic
}

// TrafficSample represents a traffic sample for a specific interval
type TrafficSample struct {
	Bytes   uint64
	Packets uint64
}

// TrafficStats holds traffic statistics using sliding window
type TrafficStats struct {
	samples        []TrafficSample // Ring buffer for samples
	currentIndex   uint32          // Current position in ring buffer
	windowSize     uint32          // Number of samples in window
	currentBytes   uint64          // Bytes accumulated in current sample period (atomic)
	currentPackets uint64          // Packets accumulated in current sample period (atomic)
	totalBytes     uint64          // totalBytes represents the cumulative count of bytes processed across all samples in the traffic statistics.
	totalPackets   uint64          // totalPackets represents the cumulative count of packets processed across all samples in the traffic statistics.
}

// LoadBalancerComponent implements intelligent packet distribution based on traffic and rules
type LoadBalancerComponent struct {
	BaseComponent
	detour          []LoadBalancerDetourRule
	stats           *TrafficStats
	packetSeq       uint64                         // Atomic counter for packet sequence
	compiledRules   []CompiledExpression           // Pre-compiled expressions
	ruleTargets     [][]string                     // Corresponding targets for each rule (array of arrays)
	expressionCache map[string]*CompiledExpression // Cache for compiled expressions
}

// NewLoadBalancerComponent creates a new load balancer component
func NewLoadBalancerComponent(cfg LoadBalancerComponentConfig, router *Router) (*LoadBalancerComponent, error) {
	lb := &LoadBalancerComponent{
		BaseComponent: NewBaseComponent(cfg.Tag, router, 0),
		detour:        cfg.Detour,
		stats: &TrafficStats{
			samples:    make([]TrafficSample, cfg.WindowSize),
			windowSize: cfg.WindowSize,
		},
		packetSeq:       0,
		expressionCache: make(map[string]*CompiledExpression),
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

	// Pre-compile all rules for better performance
	if err := lb.precompileRules(); err != nil {
		return fmt.Errorf("failed to precompile rules: %w", err)
	}

	// Start statistics sampling goroutine
	go lb.statsSampler()

	return nil
}

// precompileRules compiles all detour rules into expr programs for faster evaluation
func (lb *LoadBalancerComponent) precompileRules() error {
	lb.compiledRules = make([]CompiledExpression, len(lb.detour))
	lb.ruleTargets = make([][]string, len(lb.detour))

	for i, rule := range lb.detour {
		compiled, err := lb.compileExpression(rule.Rule)
		if err != nil {
			return fmt.Errorf("failed to compile rule %d (%s): %w", i, rule.Rule, err)
		}
		lb.compiledRules[i] = *compiled
		lb.ruleTargets[i] = rule.Targets
	}

	return nil
}

// compileExpression compiles an expression string into an expr.Program with variable detection
func (lb *LoadBalancerComponent) compileExpression(exprStr string) (*CompiledExpression, error) {
	// Check cache first
	if cached, exists := lb.expressionCache[exprStr]; exists {
		return cached, nil
	}

	// Find variables in the expression
	varKeys := lb.findVariables(exprStr)

	program, err := expr.Compile(exprStr, expr.Env(map[string]any{
		"seq":  uint64(0),
		"bps":  uint64(0),
		"pps":  uint64(0),
		"size": uint64(0),
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to compile expression: %w", err)
	}

	// Determine if this expression can be cached (no seq/size dependency)
	canCache := true
	for _, varKey := range varKeys {
		if varKey == "seq" || varKey == "size" {
			canCache = false
			break
		}
	}

	compiled := &CompiledExpression{
		program:      program,
		varKeys:      varKeys,
		canCache:     canCache,
		cachedResult: 0,
	}

	// Cache the compiled expression
	lb.expressionCache[exprStr] = compiled

	return compiled, nil
}

// findVariables finds all variable names used in the expression
func (lb *LoadBalancerComponent) findVariables(exprStr string) []string {
	var vars []string
	variables := []string{"seq", "bps", "pps", "size"}
	for _, variable := range variables {
		if strings.Contains(exprStr, variable) {
			vars = append(vars, variable)
		}
	}
	return vars
}

// Stop stops the load balancer component
func (lb *LoadBalancerComponent) Stop() error {
	close(lb.GetStopChannel())
	return nil
}

func (lb *LoadBalancerComponent) SendPacket(_ *Packet, _ any) error {
	return nil
}

// HandlePacket processes and routes packets based on load balancing rules
func (lb *LoadBalancerComponent) HandlePacket(packet *Packet) error {
	defer packet.Release(1)

	// Update traffic statistics
	lb.updateStats(packet)

	// Get current stats for rule evaluation
	bps, pps := lb.getCurrentStats()

	// Increment packet sequence atomically
	seq := atomic.AddUint64(&lb.packetSeq, 1)

	// Get current packet size
	size := uint64(packet.length)

	// Evaluate detour rules to find matching targets
	targets := lb.evaluateCompiledRules(seq, bps, pps, size)
	if len(targets) == 0 {
		return nil
	}

	// Route packet to all determined targets
	if err := lb.router.Route(packet, targets); err != nil {
		return fmt.Errorf("routing error: %w", err)
	}

	return nil
}

// updateStats updates traffic statistics with the current packet
func (lb *LoadBalancerComponent) updateStats(packet *Packet) {
	atomic.AddUint64(&lb.stats.currentBytes, uint64(packet.length))
	atomic.AddUint64(&lb.stats.currentPackets, 1)
}

// getCurrentStats returns average bps (bits per second) and pps values across the window
func (lb *LoadBalancerComponent) getCurrentStats() (uint64, uint64) {
	var totalBytes, totalPackets uint64

	totalBytes += atomic.LoadUint64(&lb.stats.totalBytes) + atomic.LoadUint64(&lb.stats.currentBytes)
	totalPackets += atomic.LoadUint64(&lb.stats.totalPackets) + atomic.LoadUint64(&lb.stats.currentPackets)

	sampleCount := lb.stats.windowSize + 1
	bps := totalBytes * 8 / uint64(sampleCount) // Convert bytes to bits per second
	pps := totalPackets / uint64(sampleCount)

	return bps, pps
}

// statsSampler periodically samples statistics based on sample interval
func (lb *LoadBalancerComponent) statsSampler() {
	ticker := time.NewTicker(time.Second) // Sample every second
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
	// Atomically swap current counters to get snapshot and reset
	currentBytes := atomic.SwapUint64(&lb.stats.currentBytes, 0)
	currentPackets := atomic.SwapUint64(&lb.stats.currentPackets, 0)

	// Get current index atomically and increment
	currentIndex := atomic.AddUint32(&lb.stats.currentIndex, 1) % lb.stats.windowSize

	atomic.AddUint64(&lb.stats.totalBytes, currentBytes)
	atomic.AddUint64(&lb.stats.totalPackets, currentPackets)

	atomic.AddUint64(&lb.stats.totalBytes, -lb.stats.samples[currentIndex].Bytes)
	atomic.AddUint64(&lb.stats.totalPackets, -lb.stats.samples[currentIndex].Packets)

	// Store sample in ring buffer
	lb.stats.samples[currentIndex] = TrafficSample{
		Bytes:   currentBytes,
		Packets: currentPackets,
	}

	// Update cached expression results for rules that don't depend on seq/size
	lb.updateCachedResults()
}

// updateCachedResults updates cached results for expressions that only depend on bps/pps
func (lb *LoadBalancerComponent) updateCachedResults() {
	bps, pps := lb.getCurrentStats()
	// bps is bits per second now

	// Update each cacheable rule
	for i := range lb.compiledRules {
		if lb.compiledRules[i].canCache {
			result := lb.evaluateExpressionDirect(&lb.compiledRules[i], 0, bps, pps, 0)

			// Store result as 0 (false) or 1 (true)
			var resultValue uint64
			if result {
				resultValue = 1
			}

			// Atomically update cached result and version
			atomic.StoreUint64(&lb.compiledRules[i].cachedResult, resultValue)
		}
	}
}

// evaluateCompiledRules evaluates all pre-compiled detour rules and returns all matching targets
func (lb *LoadBalancerComponent) evaluateCompiledRules(seq, bps, pps, size uint64) []string {
	var allTargets []string

	for i, compiledRule := range lb.compiledRules {
		var result bool

		if compiledRule.canCache {
			// Use cached result
			cachedResult := atomic.LoadUint64(&compiledRule.cachedResult)
			result = cachedResult == 1
		} else {
			// Direct evaluation for expressions that depend on seq/size
			result = lb.evaluateExpressionDirect(&compiledRule, seq, bps, pps, size)
		}

		if result {
			allTargets = append(allTargets, lb.ruleTargets[i]...)
		}
	}

	return allTargets
}

// evaluateExpressionDirect performs direct expression evaluation without caching
func (lb *LoadBalancerComponent) evaluateExpressionDirect(compiled *CompiledExpression, seq, bps, pps, size uint64) bool {
	// Create environment with variables for expr evaluation
	env := map[string]any{}

	// Add variables to environment without $ prefix
	for _, varKey := range compiled.varKeys {
		switch varKey {
		case "seq":
			env[varKey] = seq
		case "bps":
			env[varKey] = bps
		case "pps":
			env[varKey] = pps
		case "size":
			env[varKey] = size
		}
	}

	// Evaluate the expression
	result, err := expr.Run(compiled.program, env)
	if err != nil {
		logger.Errorf("%s: Error evaluating compiled expression: %v", lb.tag, err)
		return false
	}

	// Convert result to boolean
	switch v := result.(type) {
	case bool:
		return v
	case int, int64, float64, uint64:
		return v != 0
	default:
		logger.Errorf("%s: Unexpected result type from expression: %T", lb.tag, result)
		return false
	}
}
