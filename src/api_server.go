package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// APIConfig represents the configuration for the API server
type APIConfig struct {
	Enabled     bool   `json:"enabled"`
	Port        int    `json:"port"`
	Host        string `json:"host"`
	ServeUI     bool   `json:"serve_ui"`      // Whether to serve UI at root URL
	H5FilesPath string `json:"h5_files_path"` // Path to H5 files directory
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
	mux.HandleFunc("/api/filter/", a.handleGetFilterInfo)

	// Register H5 files handler if path is configured
	if a.config.H5FilesPath != "" {
		mux.HandleFunc("/h5/", a.handleH5Files)
	}

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

	componentList := make([]map[string]interface{}, 0)

	for _, serviceConfig := range a.router.config.Services {
		if serviceTag, ok := serviceConfig["tag"].(string); ok {
			if component := a.router.GetComponentByTag(serviceTag); component != nil {
				componentInfo := a.getComponentInfo(serviceTag)
				componentList = append(componentList, componentInfo)
			}
		}
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

	componentInfo := a.getComponentInfo(component.GetTag())

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(componentInfo)
	if err != nil {
		logger.Errorf("Error encoding JSON: %v", err)
		return
	}
}

// getComponentInfo retrieves comprehensive component information including detour
func (a *APIServer) getComponentInfo(tag string) map[string]interface{} {
	result := map[string]interface{}{
		"tag":    tag,
		"type":   "unknown",
		"detour": nil,
	}

	// Get the component configuration from router's configuration
	if a.router != nil {
		for _, serviceConfig := range a.router.config.Services {
			if serviceTag, ok := serviceConfig["tag"].(string); ok && serviceTag == tag {
				// Set component type
				if serviceType, ok := serviceConfig["type"].(string); ok {
					result["type"] = serviceType
				}

				// Set detour information
				if detour, ok := serviceConfig["detour"]; ok {
					result["detour"] = detour
				}

				// Add other relevant configuration based on component type
				if serviceType, ok := serviceConfig["type"].(string); ok {
					switch serviceType {
					case "listen":
						if listenAddr, ok := serviceConfig["listen_addr"].(string); ok {
							result["listen_addr"] = listenAddr
						}
						if timeout, ok := serviceConfig["timeout"]; ok {
							result["timeout"] = timeout
						}
						if replaceOldMapping, ok := serviceConfig["replace_old_mapping"]; ok {
							result["replace_old_mapping"] = replaceOldMapping
						}
					case "forward":
						if forwarders, ok := serviceConfig["forwarders"]; ok {
							result["forwarders"] = forwarders
						}
						if reconnectInterval, ok := serviceConfig["reconnect_interval"]; ok {
							result["reconnect_interval"] = reconnectInterval
						}
						if sendKeepalive, ok := serviceConfig["send_keepalive"]; ok {
							result["send_keepalive"] = sendKeepalive
						}
					case "load_balancer":
						if windowSize, ok := serviceConfig["window_size"]; ok {
							result["window_size"] = windowSize
						}
					case "filter":
						if useProtoDetectors, ok := serviceConfig["use_proto_detectors"]; ok {
							result["use_proto_detectors"] = useProtoDetectors
						}
						if detourMiss, ok := serviceConfig["detour_miss"]; ok {
							result["detour_miss"] = detourMiss
						}
					}
				}

				break
			}
		}
	}

	return result
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

	// Check if auth is configured
	hasAuth := listenComponent.authManager != nil

	for addrStr, mapping := range mappingsSnapshot {
		connection := map[string]interface{}{
			"address":       addrStr,
			"last_active":   mapping.lastActive.Format(time.RFC3339),
			"connection_id": fmt.Sprintf("%x", mapping.connID),
		}

		// Only include is_authenticated if auth is configured
		if hasAuth {
			connection["is_authenticated"] = mapping.authState != nil && mapping.authState.IsAuthenticated()
		}

		connections = append(connections, connection)
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

	// Check if auth is configured
	hasAuth := forwardComponent.authManager != nil

	for _, conn := range forwardComponent.forwardConnList {
		connection := map[string]interface{}{
			"remote_addr":      conn.remoteAddr,
			"is_connected":     atomic.LoadInt32(&conn.isConnected) == 1,
			"last_reconnect":   conn.lastReconnectAttempt.Format(time.RFC3339),
			"auth_retry_count": conn.authRetryCount,
			"heartbeat_miss":   conn.heartbeatMissCount,
			"last_heartbeat":   conn.lastHeartbeatSent.Format(time.RFC3339),
		}

		// Only include is_authenticated if auth is configured
		if hasAuth {
			connection["is_authenticated"] = conn.authState != nil && conn.authState.IsAuthenticated()
		}

		connections = append(connections, connection)
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

func (a *APIServer) handleGetFilterInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tag := r.URL.Path[len("/api/filter/"):]
	if tag == "" {
		http.Error(w, "Component tag is required", http.StatusBadRequest)
		return
	}

	component := a.router.GetComponentByTag(tag)
	if component == nil {
		http.Error(w, "Component not found", http.StatusNotFound)
		return
	}

	filterComponent, ok := component.(*FilterComponent)
	if !ok {
		http.Error(w, "Component is not a FilterComponent", http.StatusBadRequest)
		return
	}

	// Get filter configuration from router config
	result := map[string]interface{}{
		"tag":                 filterComponent.GetTag(),
		"type":                "filter",
		"use_proto_detectors": filterComponent.useProtoDetectors,
		"detour":              filterComponent.detour,
		"detour_miss":         filterComponent.detourMiss,
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(result)
	if err != nil {
		logger.Errorf("Error encoding JSON: %v", err)
		return
	}
}

// handleH5Files handles GET /h5/ and serves H5 files from the configured path
func (a *APIServer) handleH5Files(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract the file path from the URL
	filePath := r.URL.Path[len("/h5/"):]

	// If no file path is provided, try to serve index.html or index.htm
	if filePath == "" {
		// Try index.html first
		indexPath := filepath.Join(a.config.H5FilesPath, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			filePath = "index.html"
		} else {
			// Try index.htm if index.html doesn't exist
			indexPath = filepath.Join(a.config.H5FilesPath, "index.htm")
			if _, err := os.Stat(indexPath); err == nil {
				filePath = "index.htm"
			} else {
				http.Error(w, "No index file found", http.StatusNotFound)
				return
			}
		}
	}

	// Ensure the file path doesn't contain any directory traversal attempts
	if filepath.IsAbs(filePath) || filepath.Clean(filePath) != filePath {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	// Construct the full file path
	fullPath := filepath.Join(a.config.H5FilesPath, filePath)

	// Check if the file exists
	fileInfo, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "File not found", http.StatusNotFound)
		} else {
			logger.Errorf("Error accessing file: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	// Check if it's a directory
	if fileInfo.IsDir() {
		http.Error(w, "Cannot serve directories", http.StatusBadRequest)
		return
	}

	// Open and serve the file
	file, err := os.Open(fullPath)
	if err != nil {
		logger.Errorf("Error opening file: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Set appropriate content type based on file extension
	ext := filepath.Ext(filePath)
	contentType := a.getContentType(ext)
	w.Header().Set("Content-Type", contentType)

	// Copy the file content to the response
	http.ServeContent(w, r, fileInfo.Name(), fileInfo.ModTime(), file)
}

// getContentType returns the appropriate Content-Type based on file extension
func (a *APIServer) getContentType(ext string) string {
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".txt":
		return "text/plain"
	case ".pdf":
		return "application/pdf"
	case ".woff":
		return "application/font-woff"
	case ".woff2":
		return "application/font-woff2"
	case ".ttf":
		return "application/font-sfnt"
	case ".eot":
		return "application/vnd.ms-fontobject"
	case ".otf":
		return "application/font-sfnt"
	case ".xml":
		return "application/xml"
	case ".zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}
