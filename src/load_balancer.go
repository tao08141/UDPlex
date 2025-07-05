package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CompiledExpression represents a pre-compiled expression for faster evaluation
type CompiledExpression struct {
	ast     ast.Expr
	varKeys []string // Variables found in the expression (e.g., "$seq", "$bps", "$pps")
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
	windowSize      uint32
	stats           *TrafficStats
	packetSeq       uint64                         // Atomic counter for packet sequence
	compiledRules   []CompiledExpression           // Pre-compiled expressions
	ruleTargets     [][]string                     // Corresponding targets for each rule (array of arrays)
	compileOnce     sync.Once                      // Ensure rules are compiled only once
	expressionCache map[string]*CompiledExpression // Cache for compiled expressions
	cacheMutex      sync.RWMutex                   // Mutex for cache access
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

// precompileRules compiles all detour rules into AST for faster evaluation
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

// compileExpression compiles an expression string into AST with variable detection
func (lb *LoadBalancerComponent) compileExpression(expr string) (*CompiledExpression, error) {
	// Check cache first
	lb.cacheMutex.RLock()
	if cached, exists := lb.expressionCache[expr]; exists {
		lb.cacheMutex.RUnlock()
		return cached, nil
	}
	lb.cacheMutex.RUnlock()

	// Find variables in the expression
	varKeys := lb.findVariables(expr)

	// Create a template expression with placeholders
	templateExpr := expr
	for _, varKey := range varKeys {
		templateExpr = strings.ReplaceAll(templateExpr, varKey, "0")
	}

	// Parse the template expression
	node, err := parser.ParseExpr(templateExpr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse expression: %w", err)
	}

	compiled := &CompiledExpression{
		ast:     node,
		varKeys: varKeys,
	}

	// Cache the compiled expression
	lb.cacheMutex.Lock()
	lb.expressionCache[expr] = compiled
	lb.cacheMutex.Unlock()

	return compiled, nil
}

// findVariables finds all variables in the expression
func (lb *LoadBalancerComponent) findVariables(expr string) []string {
	var vars []string
	variables := []string{"$seq", "$bps", "$pps"}

	for _, variable := range variables {
		if strings.Contains(expr, variable) {
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

	// Evaluate detour rules to find matching targets
	targets := lb.evaluateCompiledRules(seq, bps, pps)
	if len(targets) == 0 {
		return fmt.Errorf("%s: No matching rule found for packet", lb.tag)
	}

	// Route packet to all determined targets
	if err := lb.router.Route(packet, targets); err != nil {
		return fmt.Errorf("routing error: %w", err)
	}

	return nil
}

// updateStats updates traffic statistics with the current packet
func (lb *LoadBalancerComponent) updateStats(packet *Packet) {
	atomic.AddUint64(&lb.stats.currentBytes, uint64(len(packet.GetData())))
	atomic.AddUint64(&lb.stats.currentPackets, 1)
}

// getCurrentStats returns average BPS and PPS values across the window
func (lb *LoadBalancerComponent) getCurrentStats() (uint64, uint64) {
	var totalBytes, totalPackets uint64

	totalBytes += atomic.LoadUint64(&lb.stats.totalBytes) + atomic.LoadUint64(&lb.stats.currentBytes)
	totalPackets += atomic.LoadUint64(&lb.stats.totalPackets) + atomic.LoadUint64(&lb.stats.currentPackets)

	sampleCount := lb.stats.windowSize + 1
	bps := totalBytes / uint64(sampleCount)
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
}

// evaluateCompiledRules evaluates all pre-compiled detour rules and returns all matching targets
func (lb *LoadBalancerComponent) evaluateCompiledRules(seq, bps, pps uint64) []string {
	var allTargets []string

	for i, compiledRule := range lb.compiledRules {
		if lb.evaluateCompiledExpression(&compiledRule, seq, bps, pps) {
			allTargets = append(allTargets, lb.ruleTargets[i]...)
		}
	}

	return allTargets
}

// evaluateCompiledExpression evaluates a pre-compiled expression with given variables
func (lb *LoadBalancerComponent) evaluateCompiledExpression(compiled *CompiledExpression, seq, bps, pps uint64) bool {
	// Create variable map for substitution
	varMap := make(map[string]uint64)
	for _, varKey := range compiled.varKeys {
		switch varKey {
		case "$seq":
			varMap[varKey] = seq
		case "$bps":
			varMap[varKey] = bps
		case "$pps":
			varMap[varKey] = pps
		}
	}

	// Evaluate the pre-compiled AST with variable substitution
	result, err := lb.evaluateNodeWithVars(compiled.ast, varMap)
	if err != nil {
		logger.Errorf("%s: Error evaluating compiled expression: %v", lb.tag, err)
		return false
	}

	return result != 0
}

// evaluateRules evaluates all detour rules and returns all matching targets
func (lb *LoadBalancerComponent) evaluateRules(seq, bps, pps uint64) []string {
	var allTargets []string

	for _, rule := range lb.detour {
		if lb.evaluateExpression(rule.Rule, seq, bps, pps) {
			allTargets = append(allTargets, rule.Targets...)
		}
	}

	return allTargets
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

// evaluateNodeWithVars evaluates AST nodes with variable substitution
func (lb *LoadBalancerComponent) evaluateNodeWithVars(node ast.Expr, varMap map[string]uint64) (int64, error) {
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
		left, err := lb.evaluateNodeWithVars(n.X, varMap)
		if err != nil {
			return 0, err
		}

		right, err := lb.evaluateNodeWithVars(n.Y, varMap)
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
		operand, err := lb.evaluateNodeWithVars(n.X, varMap)
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
		return lb.evaluateNodeWithVars(n.X, varMap)

	default:
		return 0, fmt.Errorf("unsupported expression type: %T", node)
	}
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
