package main

// Config represents the top-level configuration structure
type Config struct {
	BufferSize        int                           `json:"buffer_size"`
	QueueSize         int                           `json:"queue_size"`
	WorkerCount       int                           `json:"worker_count"`
	Services          []map[string]interface{}      `json:"services"`
	ProtocolDetectors map[string]ProtocolDefinition `json:"protocol_detectors"`
}

// ComponentConfig represents the common configuration for all components
type ComponentConfig struct {
	Type                string   `json:"type"`
	Tag                 string   `json:"tag"`
	ListenAddr          string   `json:"listen_addr"`
	BufferSize          int      `json:"buffer_size"`
	Timeout             int      `json:"timeout"`
	ReplaceOldMapping   bool     `json:"replace_old_mapping"`
	Forwarders          []string `json:"forwarders"`
	QueueSize           int      `json:"queue_size"`
	WorkerCount         int      `json:"worker_count"`
	ReconnectInterval   int      `json:"reconnect_interval"`
	ConnectionCheckTime int      `json:"connection_check_time"`
	Detour              []string `json:"detour"`
	SendKeepalive       *bool    `json:"send_keepalive"`
}
