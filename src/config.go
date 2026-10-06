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
	UDPBatchSize      int                           `json:"udp_batch_size" yaml:"udp_batch_size"` // Datagrams per recvmmsg/sendmmsg on Linux, 1 disables batching (default 64)
	UDPOffload        *bool                         `json:"udp_offload" yaml:"udp_offload"`       // UDP GSO/GRO on Linux (default true)
}

// ComponentConfig represents the common configuration for all components
type ComponentConfig struct {
	Type                string          `json:"type" yaml:"type"`
	Tag                 string          `json:"tag" yaml:"tag"`
	ListenAddr          string          `json:"listen_addr" yaml:"listen_addr"`
	Timeout             int             `json:"timeout" yaml:"timeout"`
	ReplaceOldMapping   bool            `json:"replace_old_mapping" yaml:"replace_old_mapping"`
	Forwarders          []string        `json:"forwarders" yaml:"forwarders"`
	InterfaceName       string          `json:"interface_name" yaml:"interface_name"` // Default outbound interface, can be overridden per forwarder with addr@iface
	ReconnectInterval   int             `json:"reconnect_interval" yaml:"reconnect_interval"`
	ConnectionCheckTime int             `json:"connection_check_time" yaml:"connection_check_time"`
	Detour              []string        `json:"detour" yaml:"detour"`
	SendKeepalive       *bool           `json:"send_keepalive" yaml:"send_keepalive"`
	Auth                *AuthConfig     `json:"auth,omitempty" yaml:"auth,omitempty"`
	BroadcastMode       *bool           `json:"broadcast_mode" yaml:"broadcast_mode"`             // When false, only send to the specific connection ID
	PreserveConnID      bool            `json:"preserve_conn_id" yaml:"preserve_conn_id"`         // listen with auth: keep the connection ID the peer sent instead of the line's
	ConnectionPoolSize  int             `json:"connection_pool_size" yaml:"connection_pool_size"` // Number of connections in the pool
	NoDelay             *bool           `json:"no_delay" yaml:"no_delay"`
	SendTimeout         int             `json:"send_timeout" yaml:"send_timeout"`         // ms
	RecvBufferSize      int             `json:"recv_buffer_size" yaml:"recv_buffer_size"` // UDP socket receive buffer size in bytes
	SendBufferSize      int             `json:"send_buffer_size" yaml:"send_buffer_size"` // UDP socket send buffer size in bytes
	EnableWriteBatch    *bool           `json:"enable_write_batch" yaml:"enable_write_batch"`
	WriteBatchSize      int             `json:"write_batch_size" yaml:"write_batch_size"` // TCP tunnel writev batch size
	Shaper              *ShaperConfig   `json:"shaper,omitempty" yaml:"shaper,omitempty"` // Send rate shaping with small-packet priority (listen/forward)
	Queue               *TcpQueueConfig `json:"queue,omitempty" yaml:"queue,omitempty"`   // Send queue of TCP tunnel connections
	Congestion          string          `json:"congestion" yaml:"congestion"`             // TCP tunnel congestion control, e.g. bbr (Linux)
	PacingRate          float64         `json:"pacing_rate" yaml:"pacing_rate"`           // TCP tunnel send rate cap in Mbit/s (Linux)
	Target              string          `json:"target" yaml:"target"`                     // tcp_listen: address tcp_forward dials; tcp_forward: overrides it
	WindowSize          int             `json:"window_size" yaml:"window_size"`           // tcp_listen/tcp_forward: receive window per stream in bytes
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
	// BatchDecision evaluates the rules once per receive batch instead of once
	// per packet, so packets received together stay on the same path in order.
	BatchDecision bool `json:"batch_decision" yaml:"batch_decision"`
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
	// BindMode selects where WireGuard datagrams come from: "udplex" (default)
	// exchanges them with other components, "native" listens on listen_port
	// directly so ordinary WireGuard clients can connect.
	BindMode string `json:"bind_mode" yaml:"bind_mode"`
	TunNetConfig
}

// TunNetConfig is the kernel network setup of a component that owns a TUN
// interface: forwarding, policy routing and NAT. It is applied after every
// component has started, so it can reference interfaces of other components.
type TunNetConfig struct {
	IPForward    bool                `json:"ip_forward" yaml:"ip_forward"`       // Enable kernel forwarding and accept forwarded traffic on the interface
	MSSClamp     bool                `json:"mss_clamp" yaml:"mss_clamp"`         // Clamp TCP MSS to the path MTU on forwarded traffic of the interface
	PolicyRoutes []PolicyRouteConfig `json:"policy_routes" yaml:"policy_routes"` // Source based routing, e.g. client pool into another tunnel
	Masquerade   []MasqueradeConfig  `json:"masquerade" yaml:"masquerade"`       // Source NAT
}

type PolicyRouteConfig struct {
	From     []string `json:"from" yaml:"from"`         // Source prefixes looked up in Table
	Table    int      `json:"table" yaml:"table"`       // Routing table id
	Priority int      `json:"priority" yaml:"priority"` // ip rule priority, 0 lets the kernel choose
	Dev      string   `json:"dev" yaml:"dev"`           // Interface the table routes to, defaults to the component interface
	Routes   []string `json:"routes" yaml:"routes"`     // Destinations sent to Dev, defaults to the default route of each From family
}

type MasqueradeConfig struct {
	Source       string `json:"source" yaml:"source"`               // Source prefix to masquerade
	OutInterface string `json:"out_interface" yaml:"out_interface"` // Egress interface, "auto" for the default route interface, empty for any
}

type OpenVPNUserConfig struct {
	Username string `json:"username" yaml:"username"`
	Password string `json:"password" yaml:"password"`
}

type OpenVPNComponentConfig struct {
	Type string `json:"type" yaml:"type"`
	Tag  string `json:"tag" yaml:"tag"`
	// BindMode selects where OpenVPN datagrams come from: "native" (default)
	// listens on listen_addr, "udplex" exchanges them with other components.
	BindMode            string   `json:"bind_mode" yaml:"bind_mode"`
	Proto               string   `json:"proto" yaml:"proto"`             // udp (default) or tcp, tcp needs native bind mode
	ListenAddr          string   `json:"listen_addr" yaml:"listen_addr"` // native bind mode
	Detour              []string `json:"detour" yaml:"detour"`           // udplex bind mode, path of replies without reuse_incoming_detour
	ReuseIncomingDetour *bool    `json:"reuse_incoming_detour" yaml:"reuse_incoming_detour"`
	SendTimeout         int      `json:"send_timeout" yaml:"send_timeout"`
	InterfaceName       string   `json:"interface_name" yaml:"interface_name"`
	MTU                 int      `json:"mtu" yaml:"mtu"`
	Addresses           []string `json:"addresses" yaml:"addresses"` // Server address inside the client pool, e.g. 10.9.0.1/24
	Routes              []string `json:"routes" yaml:"routes"`       // Extra routes through the interface
	Topology            string   `json:"topology" yaml:"topology"`   // subnet (default), net30 or p2p
	SetupInterface      *bool    `json:"setup_interface" yaml:"setup_interface"`
	MaxClients          int      `json:"max_clients" yaml:"max_clients"`

	// Certificates and keys take PEM content or a file path.
	CA                      string              `json:"ca" yaml:"ca"`     // Verifies client certificates
	Cert                    string              `json:"cert" yaml:"cert"` // Server certificate
	Key                     string              `json:"key" yaml:"key"`   // Server private key
	TLSCrypt                string              `json:"tls_crypt" yaml:"tls_crypt"`
	TLSCryptV2              string              `json:"tls_crypt_v2" yaml:"tls_crypt_v2"`
	TLSAuth                 string              `json:"tls_auth" yaml:"tls_auth"`
	KeyDirection            *int                `json:"key_direction" yaml:"key_direction"`                         // tls_auth direction, omitted for bidirectional
	VerifyClientCertificate string              `json:"verify_client_certificate" yaml:"verify_client_certificate"` // require (default with ca), optional or none
	CRLVerify               string              `json:"crl_verify" yaml:"crl_verify"`                               // CRL file of revoked client certificates, read on every handshake
	Users                   []OpenVPNUserConfig `json:"users" yaml:"users"`                                         // auth-user-pass accounts
	DuplicateCN             bool                `json:"duplicate_cn" yaml:"duplicate_cn"`

	DataCiphers         []string `json:"data_ciphers" yaml:"data_ciphers"`
	DataCiphersFallback string   `json:"data_ciphers_fallback" yaml:"data_ciphers_fallback"`
	Auth                string   `json:"auth" yaml:"auth"`

	PushRoutes        []string `json:"push_routes" yaml:"push_routes"`
	PushDNS           []string `json:"push_dns" yaml:"push_dns"`
	RedirectGateway   bool     `json:"redirect_gateway" yaml:"redirect_gateway"`
	KeepaliveInterval int      `json:"keepalive_interval" yaml:"keepalive_interval"` // seconds, default 10
	KeepaliveTimeout  int      `json:"keepalive_timeout" yaml:"keepalive_timeout"`   // seconds, default 60

	TunNetConfig
}
