package main

// Config represents the top-level configuration structure
type Config struct {
	BufferSize        int                           `json:"buffer_size" yaml:"buffer_size"`
	BufferOffset      int                           `json:"buffer_offset" yaml:"buffer_offset"`
	QueueSize         int                           `json:"queue_size" yaml:"queue_size"`
	WorkerCount       int                           `json:"worker_count" yaml:"worker_count"`
	Services          []map[string]any              `json:"services" yaml:"services"`
	ProtocolDetectors map[string]ProtocolDefinition `json:"protocol_detectors" yaml:"protocol_detectors"`
	Logging           LoggingConfig                 `json:"logging" yaml:"logging"`
	API               APIConfig                     `json:"api" yaml:"api"`
}

// ComponentConfig represents the common configuration for all components
type ComponentConfig struct {
	Type                string      `json:"type" yaml:"type"`
	Tag                 string      `json:"tag" yaml:"tag"`
	ListenAddr          string      `json:"listen_addr" yaml:"listen_addr"`
	Timeout             int         `json:"timeout" yaml:"timeout"`
	ReplaceOldMapping   bool        `json:"replace_old_mapping" yaml:"replace_old_mapping"`
	Forwarders          []string    `json:"forwarders" yaml:"forwarders"`
	InterfaceName       string      `json:"interface_name" yaml:"interface_name"` // Default outbound interface, can be overridden per forwarder with addr@iface
	ReconnectInterval   int         `json:"reconnect_interval" yaml:"reconnect_interval"`
	ConnectionCheckTime int         `json:"connection_check_time" yaml:"connection_check_time"`
	Detour              []string    `json:"detour" yaml:"detour"`
	SendKeepalive       *bool       `json:"send_keepalive" yaml:"send_keepalive"`
	Auth                *AuthConfig `json:"auth,omitempty" yaml:"auth,omitempty"`
	BroadcastMode       *bool       `json:"broadcast_mode" yaml:"broadcast_mode"`             // When false, only send to the specific connection ID
	ConnectionPoolSize  int         `json:"connection_pool_size" yaml:"connection_pool_size"` // Number of connections in the pool
	NoDelay             *bool       `json:"no_delay" yaml:"no_delay"`
	SendTimeout         int         `json:"send_timeout" yaml:"send_timeout"`         // ms
	RecvBufferSize      int         `json:"recv_buffer_size" yaml:"recv_buffer_size"` // UDP socket receive buffer size in bytes
	SendBufferSize      int         `json:"send_buffer_size" yaml:"send_buffer_size"` // UDP socket send buffer size in bytes
	EnableWriteBatch    *bool       `json:"enable_write_batch" yaml:"enable_write_batch"`
	WriteBatchSize      int         `json:"write_batch_size" yaml:"write_batch_size"` // TCP tunnel writev batch size
}

// AuthConfig represents authentication and encryption settings
type AuthConfig struct {
	Enabled           bool   `json:"enabled" yaml:"enabled"`
	Secret            string `json:"secret" yaml:"secret"`
	EnableEncryption  bool   `json:"enable_encryption" yaml:"enable_encryption"`
	HeartbeatInterval int    `json:"heartbeat_interval" yaml:"heartbeat_interval"` // seconds
	AuthTimeout       int    `json:"auth_timeout" yaml:"auth_timeout"`             // seconds
	DelayWindowSize   int    `json:"delay_window_size" yaml:"delay_window_size"`   // number of delay measurements to record for averaging
}

// FilterComponentConfig represents the configuration for a filter component
type FilterComponentConfig struct {
	Type              string              `json:"type" yaml:"type"`
	Tag               string              `json:"tag" yaml:"tag"`
	Detour            map[string][]string `json:"detour" yaml:"detour"`
	DetourMiss        []string            `json:"detour_miss" yaml:"detour_miss"`
	UseProtoDetectors []string            `json:"use_proto_detectors" yaml:"use_proto_detectors"`
}

// LoggingConfig holds all logging-related configuration
type LoggingConfig struct {
	Level      string `json:"level" yaml:"level"`             // debug, info, warn, error, dpanic, panic, fatal
	Format     string `json:"format" yaml:"format"`           // json or console
	OutputPath string `json:"output_path" yaml:"output_path"` // file path or "stdout"
	Caller     bool   `json:"caller" yaml:"caller"`           // include caller information
}

// LoadBalancerDetourRule represents a single detour rule for load balancer
type LoadBalancerDetourRule struct {
	Rule    string   `json:"rule" yaml:"rule"`       // Expression rule for matching
	Targets []string `json:"targets" yaml:"targets"` // Target component tags (array)
}

// LoadBalancerComponentConfig represents the configuration for a load balancer component
type LoadBalancerComponentConfig struct {
	Type        string                   `json:"type" yaml:"type"`
	Tag         string                   `json:"tag" yaml:"tag"`
	Detour      []LoadBalancerDetourRule `json:"detour" yaml:"detour"`
	Miss        []string                 `json:"miss" yaml:"miss"`
	WindowSize  uint32                   `json:"window_size" yaml:"window_size"`
	EnableCache bool                     `json:"enable_cache" yaml:"enable_cache"`
}

type WireGuardPeerConfig struct {
	PublicKey           string   `json:"public_key" yaml:"public_key"`
	PresharedKey        string   `json:"preshared_key" yaml:"preshared_key"`
	Endpoint            string   `json:"endpoint" yaml:"endpoint"`
	AllowedIPs          []string `json:"allowed_ips" yaml:"allowed_ips"`
	PersistentKeepalive int      `json:"persistent_keepalive" yaml:"persistent_keepalive"`
}

type WireGuardComponentConfig struct {
	Type                string                `json:"type" yaml:"type"`
	Tag                 string                `json:"tag" yaml:"tag"`
	InterfaceName       string                `json:"interface_name" yaml:"interface_name"`
	Detour              []string              `json:"detour" yaml:"detour"`
	PrivateKey          string                `json:"private_key" yaml:"private_key"`
	ListenPort          int                   `json:"listen_port" yaml:"listen_port"`
	Addresses           []string              `json:"addresses" yaml:"addresses"`
	Routes              []string              `json:"routes" yaml:"routes"`
	RouteAllowedIPs     *bool                 `json:"route_allowed_ips" yaml:"route_allowed_ips"`
	MTU                 int                   `json:"mtu" yaml:"mtu"`
	SendTimeout         int                   `json:"send_timeout" yaml:"send_timeout"`
	Peers               []WireGuardPeerConfig `json:"peers" yaml:"peers"`
	SetupInterface      *bool                 `json:"setup_interface" yaml:"setup_interface"`
	ReuseIncomingDetour *bool                 `json:"reuse_incoming_detour" yaml:"reuse_incoming_detour"`
}
