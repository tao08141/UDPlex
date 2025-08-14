package config_test

import (
	"encoding/json"
	"testing"
)

// Copy of Config structs for testing
type Config struct {
	BufferSize        int                           `json:"buffer_size"`
	BufferOffset      int                           `json:"buffer_offset"`
	QueueSize         int                           `json:"queue_size"`
	WorkerCount       int                           `json:"worker_count"`
	Services          []map[string]any              `json:"services"`
	ProtocolDetectors map[string]ProtocolDefinition `json:"protocol_detectors"`
	Logging           LoggingConfig                 `json:"logging"`
	API               APIConfig                     `json:"api"`
}

type ComponentConfig struct {
	Type                string      `json:"type"`
	Tag                 string      `json:"tag"`
	ListenAddr          string      `json:"listen_addr"`
	Timeout             int         `json:"timeout"`
	ReplaceOldMapping   bool        `json:"replace_old_mapping"`
	Forwarders          []string    `json:"forwarders"`
	ReconnectInterval   int         `json:"reconnect_interval"`
	ConnectionCheckTime int         `json:"connection_check_time"`
	Detour              []string    `json:"detour"`
	SendKeepalive       *bool       `json:"send_keepalive"`
	Auth                *AuthConfig `json:"auth,omitempty"`
	BroadcastMode       *bool       `json:"broadcast_mode"`
	ConnectionPoolSize  int         `json:"connection_pool_size"`
	NoDelay             *bool       `json:"no_delay"`
	SendTimeout         int         `json:"send_timeout"`
	RecvBufferSize      int         `json:"recv_buffer_size"`
	SendBufferSize      int         `json:"send_buffer_size"`
}

type AuthConfig struct {
	Enabled           bool   `json:"enabled"`
	Secret            string `json:"secret"`
	EnableEncryption  bool   `json:"enable_encryption"`
	HeartbeatInterval int    `json:"heartbeat_interval"`
	AuthTimeout       int    `json:"auth_timeout"`
	DelayWindowSize   int    `json:"delay_window_size"`
}

type LoggingConfig struct {
	Level      string `json:"level"`
	Format     string `json:"format"`
	OutputPath string `json:"output_path"`
	Caller     bool   `json:"caller"`
}

type APIConfig struct {
	Enabled    bool   `json:"enabled"`
	ListenAddr string `json:"listen_addr"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

type ProtocolDefinition struct {
	Pattern string `json:"pattern"`
	Offset  int    `json:"offset"`
}

func TestConfigJSONUnmarshal(t *testing.T) {
	configJSON := `{
		"buffer_size": 4096,
		"buffer_offset": 0,
		"queue_size": 1000,
		"worker_count": 4,
		"services": [],
		"protocol_detectors": {},
		"logging": {
			"level": "info",
			"format": "console",
			"output_path": "stdout",
			"caller": false
		},
		"api": {
			"enabled": false,
			"listen_addr": "127.0.0.1:8080",
			"username": "",
			"password": ""
		}
	}`

	var config Config
	err := json.Unmarshal([]byte(configJSON), &config)
	if err != nil {
		t.Fatalf("Failed to unmarshal config: %v", err)
	}

	if config.BufferSize != 4096 {
		t.Errorf("BufferSize = %d, want 4096", config.BufferSize)
	}

	if config.WorkerCount != 4 {
		t.Errorf("WorkerCount = %d, want 4", config.WorkerCount)
	}

	if config.Logging.Level != "info" {
		t.Errorf("Logging.Level = %q, want 'info'", config.Logging.Level)
	}
}

func TestComponentConfigDefaults(t *testing.T) {
	configJSON := `{
		"type": "listen",
		"tag": "test_listener",
		"listen_addr": "0.0.0.0:8080"
	}`

	var config ComponentConfig
	err := json.Unmarshal([]byte(configJSON), &config)
	if err != nil {
		t.Fatalf("Failed to unmarshal component config: %v", err)
	}

	if config.Type != "listen" {
		t.Errorf("Type = %q, want 'listen'", config.Type)
	}

	if config.Tag != "test_listener" {
		t.Errorf("Tag = %q, want 'test_listener'", config.Tag)
	}

	if config.ListenAddr != "0.0.0.0:8080" {
		t.Errorf("ListenAddr = %q, want '0.0.0.0:8080'", config.ListenAddr)
	}
}

func TestAuthConfig(t *testing.T) {
	configJSON := `{
		"enabled": true,
		"secret": "test_secret",
		"enable_encryption": true,
		"heartbeat_interval": 30,
		"auth_timeout": 10,
		"delay_window_size": 50
	}`

	var config AuthConfig
	err := json.Unmarshal([]byte(configJSON), &config)
	if err != nil {
		t.Fatalf("Failed to unmarshal auth config: %v", err)
	}

	if !config.Enabled {
		t.Error("Auth should be enabled")
	}

	if config.Secret != "test_secret" {
		t.Errorf("Secret = %q, want 'test_secret'", config.Secret)
	}

	if !config.EnableEncryption {
		t.Error("Encryption should be enabled")
	}

	if config.HeartbeatInterval != 30 {
		t.Errorf("HeartbeatInterval = %d, want 30", config.HeartbeatInterval)
	}

	if config.DelayWindowSize != 50 {
		t.Errorf("DelayWindowSize = %d, want 50", config.DelayWindowSize)
	}
}

func TestLoggingConfig(t *testing.T) {
	tests := []struct {
		name   string
		json   string
		level  string
		format string
	}{
		{
			name:   "console format",
			json:   `{"level": "debug", "format": "console", "output_path": "stdout", "caller": true}`,
			level:  "debug",
			format: "console",
		},
		{
			name:   "json format",
			json:   `{"level": "error", "format": "json", "output_path": "/var/log/app.log", "caller": false}`,
			level:  "error",
			format: "json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var config LoggingConfig
			err := json.Unmarshal([]byte(tt.json), &config)
			if err != nil {
				t.Fatalf("Failed to unmarshal logging config: %v", err)
			}

			if config.Level != tt.level {
				t.Errorf("Level = %q, want %q", config.Level, tt.level)
			}

			if config.Format != tt.format {
				t.Errorf("Format = %q, want %q", config.Format, tt.format)
			}
		})
	}
}

func TestProtocolDefinition(t *testing.T) {
	configJSON := `{
		"pattern": "^HTTP",
		"offset": 0
	}`

	var proto ProtocolDefinition
	err := json.Unmarshal([]byte(configJSON), &proto)
	if err != nil {
		t.Fatalf("Failed to unmarshal protocol definition: %v", err)
	}

	if proto.Pattern != "^HTTP" {
		t.Errorf("Pattern = %q, want '^HTTP'", proto.Pattern)
	}

	if proto.Offset != 0 {
		t.Errorf("Offset = %d, want 0", proto.Offset)
	}
}