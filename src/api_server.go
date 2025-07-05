package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// APIConfig represents the configuration for the API server
type APIConfig struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Host    string `json:"host"`
}

// APIServer represents the RESTful API server
type APIServer struct {
	config  APIConfig
	router  *Router
	server  *http.Server
	running atomic.Bool
}

// NewAPIServer creates a new API server
func NewAPIServer(config APIConfig, router *Router) *APIServer {
	return &APIServer{
		config: config,
		router: router,
	}
}

// Start starts the API server
func (a *APIServer) Start() error {
	if !a.config.Enabled {
		logger.Info("API server is disabled")
		return nil
	}

	if a.running.Load() {
		return fmt.Errorf("API server is already running")
	}

	mux := http.NewServeMux()

	// Register endpoints
	mux.HandleFunc("/api/components", a.handleGetComponents)
	mux.HandleFunc("/api/components/", a.handleGetComponentByTag)
	mux.HandleFunc("/api/listen/", a.handleGetListenConnections)
	mux.HandleFunc("/api/forward/", a.handleGetForwardConnections)
	mux.HandleFunc("/api/tcp_tunnel_listen/", a.handleGetTcpTunnelListenConnections)
	mux.HandleFunc("/api/tcp_tunnel_forward/", a.handleGetTcpTunnelForwardConnections)
	mux.HandleFunc("/api/load_balancer/", a.handleGetLoadBalancerTraffic)

	addr := fmt.Sprintf("%s:%d", a.config.Host, a.config.Port)
	a.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	logger.Infof("Starting API server on %s", addr)
	a.running.Store(true)

	go func() {
		if err := a.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Errorf("API server error: %v", err)
		}
	}()

	return nil
}

// Stop stops the API server
func (a *APIServer) Stop() error {
	if !a.running.Load() {
		return nil
	}

	logger.Info("Stopping API server")
	a.running.Store(false)
	return a.server.Close()
}

// handleGetComponents handles GET /api/components
func (a *APIServer) handleGetComponents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	components := a.router.GetComponents()
	componentList := make([]map[string]string, 0, len(components))

	for _, component := range components {
		componentType := a.getComponentTypeFromConfig(component.GetTag())
		componentList = append(componentList, map[string]string{
			"tag":  component.GetTag(),
			"type": componentType,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(componentList)
	if err != nil {
		logger.Errorf("Error encoding JSON: %v", err)
		return
	}
}

// handleGetComponentByTag handles GET /api/components/{tag}
func (a *APIServer) handleGetComponentByTag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tag := r.URL.Path[len("/api/components/"):]
	if tag == "" {
		http.Error(w, "Component tag is required", http.StatusBadRequest)
		return
	}

	component := a.router.GetComponentByTag(tag)
	if component == nil {
		http.Error(w, "Component not found", http.StatusNotFound)
		return
	}

	componentType := a.getComponentTypeFromConfig(component.GetTag())

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(map[string]string{
		"tag":  component.GetTag(),
		"type": componentType,
	})
	if err != nil {
		logger.Errorf("Error encoding JSON: %v", err)
		return
	}
}

// getComponentTypeFromConfig retrieves the component type from router config
func (a *APIServer) getComponentTypeFromConfig(tag string) string {
	// Get the component type from router's configuration
	if a.router != nil {
		for _, serviceConfig := range a.router.config.Services {
			if serviceTag, ok := serviceConfig["tag"].(string); ok && serviceTag == tag {
				if serviceType, ok := serviceConfig["type"].(string); ok {
					return serviceType
				}
			}
		}
	}

	// Fallback to "unknown" if type cannot be determined from config
	return "unknown"
}

// handleGetListenConnections handles GET /api/listen/{tag}
func (a *APIServer) handleGetListenConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tag := r.URL.Path[len("/api/listen/"):]
	if tag == "" {
		http.Error(w, "Component tag is required", http.StatusBadRequest)
		return
	}

	component := a.router.GetComponentByTag(tag)
	if component == nil {
		http.Error(w, "Component not found", http.StatusNotFound)
		return
	}

	listenComponent, ok := component.(*ListenComponent)
	if !ok {
		http.Error(w, "Component is not a ListenComponent", http.StatusBadRequest)
		return
	}

	// Get mappings from the component
	mappingsSnapshot := listenComponent.mappingsAtomic.Load().(map[string]*AddrMapping)
	connections := make([]map[string]interface{}, 0, len(mappingsSnapshot))

	for addrStr, mapping := range mappingsSnapshot {
		connections = append(connections, map[string]interface{}{
			"address":          addrStr,
			"last_active":      mapping.lastActive.Format(time.RFC3339),
			"connection_id":    fmt.Sprintf("%x", mapping.connID),
			"is_authenticated": mapping.authState != nil && mapping.authState.IsAuthenticated(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(map[string]interface{}{
		"tag":         listenComponent.GetTag(),
		"listen_addr": listenComponent.listenAddr,
		"connections": connections,
		"count":       len(connections),
	})
	if err != nil {
		logger.Errorf("Error encoding JSON: %v", err)
		return
	}
}

// handleGetForwardConnections handles GET /api/forward/{tag}
func (a *APIServer) handleGetForwardConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tag := r.URL.Path[len("/api/forward/"):]
	if tag == "" {
		http.Error(w, "Component tag is required", http.StatusBadRequest)
		return
	}

	component := a.router.GetComponentByTag(tag)
	if component == nil {
		http.Error(w, "Component not found", http.StatusNotFound)
		return
	}

	forwardComponent, ok := component.(*ForwardComponent)
	if !ok {
		http.Error(w, "Component is not a ForwardComponent", http.StatusBadRequest)
		return
	}

	connections := make([]map[string]interface{}, 0, len(forwardComponent.forwardConnList))

	for _, conn := range forwardComponent.forwardConnList {
		connections = append(connections, map[string]interface{}{
			"remote_addr":      conn.remoteAddr,
			"is_connected":     atomic.LoadInt32(&conn.isConnected) == 1,
			"is_authenticated": conn.authState != nil && conn.authState.IsAuthenticated(),
			"last_reconnect":   conn.lastReconnectAttempt.Format(time.RFC3339),
			"auth_retry_count": conn.authRetryCount,
			"heartbeat_miss":   conn.heartbeatMissCount,
			"last_heartbeat":   conn.lastHeartbeatSent.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(map[string]interface{}{
		"tag":         forwardComponent.GetTag(),
		"connections": connections,
		"count":       len(connections),
	})
	if err != nil {
		logger.Errorf("Error encoding JSON: %v", err)
		return
	}
}

// handleGetTcpTunnelListenConnections handles GET /api/tcp_tunnel_listen/{tag}
func (a *APIServer) handleGetTcpTunnelListenConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tag := r.URL.Path[len("/api/tcp_tunnel_listen/"):]
	if tag == "" {
		http.Error(w, "Component tag is required", http.StatusBadRequest)
		return
	}

	component := a.router.GetComponentByTag(tag)
	if component == nil {
		http.Error(w, "Component not found", http.StatusNotFound)
		return
	}

	tcpTunnelListenComponent, ok := component.(*TcpTunnelListenComponent)
	if !ok {
		http.Error(w, "Component is not a TcpTunnelListenComponent", http.StatusBadRequest)
		return
	}

	connectionsMap := tcpTunnelListenComponent.connections.Load().(map[ForwardID]map[PoolID]*TcpTunnelConnPool)
	result := make(map[string]interface{})
	result["tag"] = tcpTunnelListenComponent.GetTag()
	result["listen_addr"] = tcpTunnelListenComponent.listenAddr

	pools := make([]map[string]interface{}, 0)
	totalConnections := 0

	for forwardID, poolMap := range connectionsMap {
		for poolID, pool := range poolMap {
			connsPtr := pool.conns.Load()
			conns := *connsPtr

			connections := make([]map[string]interface{}, 0, len(conns))
			for _, conn := range conns {
				if conn != nil {
					connections = append(connections, map[string]interface{}{
						"remote_addr":      conn.conn.RemoteAddr().String(),
						"is_authenticated": conn.authState != nil && conn.authState.IsAuthenticated(),
						"last_active":      conn.lastActive.Format(time.RFC3339),
						"heartbeat_miss":   conn.heartbeatMissCount,
						"last_heartbeat":   conn.lastHeartbeatSent.Format(time.RFC3339),
					})
				}
			}

			totalConnections += len(connections)

			pools = append(pools, map[string]interface{}{
				"forward_id":  fmt.Sprintf("%x", forwardID),
				"pool_id":     fmt.Sprintf("%x", poolID),
				"remote_addr": pool.remoteAddr,
				"connections": connections,
				"conn_count":  len(connections),
			})
		}
	}

	result["pools"] = pools
	result["total_connections"] = totalConnections

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(result)
	if err != nil {
		logger.Errorf("Error encoding JSON: %v", err)
		return
	}
}

// handleGetTcpTunnelForwardConnections handles GET /api/tcp_tunnel_forward/{tag}
func (a *APIServer) handleGetTcpTunnelForwardConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tag := r.URL.Path[len("/api/tcp_tunnel_forward/"):]
	if tag == "" {
		http.Error(w, "Component tag is required", http.StatusBadRequest)
		return
	}

	component := a.router.GetComponentByTag(tag)
	if component == nil {
		http.Error(w, "Component not found", http.StatusNotFound)
		return
	}

	tcpTunnelForwardComponent, ok := component.(*TcpTunnelForwardComponent)
	if !ok {
		http.Error(w, "Component is not a TcpTunnelForwardComponent", http.StatusBadRequest)
		return
	}

	result := make(map[string]interface{})
	result["tag"] = tcpTunnelForwardComponent.GetTag()
	result["forward_id"] = fmt.Sprintf("%x", tcpTunnelForwardComponent.forwardID)

	pools := make([]map[string]interface{}, 0, len(tcpTunnelForwardComponent.pools))
	totalConnections := 0

	for poolID, pool := range tcpTunnelForwardComponent.pools {
		connsPtr := pool.conns.Load()
		conns := *connsPtr

		connections := make([]map[string]interface{}, 0, len(conns))
		for _, conn := range conns {
			if conn != nil {
				connections = append(connections, map[string]interface{}{
					"remote_addr":      conn.conn.RemoteAddr().String(),
					"is_authenticated": conn.authState != nil && conn.authState.IsAuthenticated(),
					"last_active":      conn.lastActive.Format(time.RFC3339),
					"heartbeat_miss":   conn.heartbeatMissCount,
					"last_heartbeat":   conn.lastHeartbeatSent.Format(time.RFC3339),
				})
			}
		}

		totalConnections += len(connections)

		pools = append(pools, map[string]interface{}{
			"pool_id":      fmt.Sprintf("%x", poolID),
			"remote_addr":  pool.remoteAddr,
			"connections":  connections,
			"conn_count":   len(connections),
			"target_count": pool.connCount,
		})
	}

	result["pools"] = pools
	result["total_connections"] = totalConnections

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(result)
	if err != nil {
		logger.Errorf("Error encoding JSON: %v", err)
		return
	}
}

// handleGetLoadBalancerTraffic handles GET /api/load_balancer/{tag}
func (a *APIServer) handleGetLoadBalancerTraffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tag := r.URL.Path[len("/api/load_balancer/"):]
	if tag == "" {
		http.Error(w, "Component tag is required", http.StatusBadRequest)
		return
	}

	component := a.router.GetComponentByTag(tag)
	if component == nil {
		http.Error(w, "Component not found", http.StatusNotFound)
		return
	}

	loadBalancerComponent, ok := component.(*LoadBalancerComponent)
	if !ok {
		http.Error(w, "Component is not a LoadBalancerComponent", http.StatusBadRequest)
		return
	}

	bps, pps := loadBalancerComponent.getCurrentStats()

	samples := make([]map[string]interface{}, 0, loadBalancerComponent.stats.windowSize)
	for i := uint32(0); i < loadBalancerComponent.stats.windowSize; i++ {
		sample := loadBalancerComponent.stats.samples[i]
		samples = append(samples, map[string]interface{}{
			"bytes":   sample.Bytes,
			"packets": sample.Packets,
		})
	}

	result := map[string]interface{}{
		"tag":             loadBalancerComponent.GetTag(),
		"bytes_per_sec":   bps,
		"packets_per_sec": pps,
		"total_bytes":     atomic.LoadUint64(&loadBalancerComponent.stats.totalBytes) + atomic.LoadUint64(&loadBalancerComponent.stats.currentBytes),
		"total_packets":   atomic.LoadUint64(&loadBalancerComponent.stats.totalPackets) + atomic.LoadUint64(&loadBalancerComponent.stats.currentPackets),
		"current_bytes":   atomic.LoadUint64(&loadBalancerComponent.stats.currentBytes),
		"current_packets": atomic.LoadUint64(&loadBalancerComponent.stats.currentPackets),
		"samples":         samples,
		"window_size":     loadBalancerComponent.stats.windowSize,
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(result)
	if err != nil {
		logger.Errorf("Error encoding JSON: %v", err)
		return
	}
}
