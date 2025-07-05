# API Server Documentation

## Overview

The API server provides a RESTful API interface for monitoring and managing the status of router components. The server supports multiple component types, including listen components, forward components, TCP tunnel components, load balancer components, and filter components.

## Configuration

Add the following configuration to your config file:
```json
{
  "api": {
    "enabled": true,
    "port": 8080,
    "host": "0.0.0.0",
    "h5_files_path": "/path/to/h5/files"
  }
}
```

### Configuration Options

- `enabled`: Whether to enable the API server
- `port`: Port on which the API server listens
- `host`: Host address for the API server
- `h5_files_path`: Directory path containing H5 files. When configured, the API server will serve files from this directory at the "/h5/" endpoint.

## API Endpoints

### 1. Get All Components

**Endpoint:** `GET /api/components`

**Description:** Retrieve a list of all registered components, including basic information and detour configuration for each component.

**Response Example:**
```json
[
  {
    "tag": "client_listen",
    "type": "listen",
    "listen_addr": "0.0.0.0:5202",
    "timeout": 120,
    "replace_old_mapping": true,
    "detour": ["protocol_filter"]
  },
  {
    "tag": "protocol_filter",
    "type": "filter",
    "use_proto_detectors": ["wireguard", "openvpn"],
    "detour": {
      "wireguard": ["wg_forward"],
      "openvpn": ["ovpn_forward"]
    },
    "detour_miss": ["default_forward"]
  },
  {
    "tag": "load_balancer",
    "type": "load_balancer",
    "window_size": 10,
    "detour": [
      {
        "rule": "seq % 2 == 0",
        "targets": ["client_forward"]
      },
      {
        "rule": "seq % 2 == 1",
        "targets": ["client_forward"]
      }
    ]
  },
  {
    "tag": "client_forward",
    "type": "forward",
    "forwarders": ["127.0.0.1:5201"],
    "reconnect_interval": 5,
    "connection_check_time": 30,
    "send_keepalive": true,
    "detour": ["client_listen"]
  }
]
```

### 2. Get Component by Tag

**Endpoint:** `GET /api/components/{tag}`

**Description:** Retrieve detailed information for a specific component by tag, including full configuration and detour info.

**Parameters:**
- `tag` (path parameter): Component tag

**Response Examples:**

**Listen Component:**
```json
{
  "tag": "client_listen",
  "type": "listen",
  "listen_addr": "0.0.0.0:5202",
  "timeout": 120,
  "replace_old_mapping": true,
  "detour": ["protocol_filter"]
}
```

**Filter Component:**
```json
{
  "tag": "protocol_filter",
  "type": "filter",
  "use_proto_detectors": ["wireguard", "openvpn", "game_protocol"],
  "detour": {
    "wireguard": ["wg_forward"],
    "openvpn": ["ovpn_forward"],
    "game_protocol": ["game_forward"]
  },
  "detour_miss": ["default_forward"]
}
```

**Load Balancer Component:**
```json
{
  "tag": "load_balancer",
  "type": "load_balancer",
  "window_size": 10,
  "detour": [
    {
      "rule": "seq % 2 == 0",
      "targets": ["client_forward"]
    },
    {
      "rule": "seq % 2 == 1",
      "targets": ["client_forward"]
    }
  ]
}
```

**Forward Component:**
```json
{
  "tag": "client_forward",
  "type": "forward",
  "forwarders": ["127.0.0.1:5201"],
  "reconnect_interval": 5,
  "connection_check_time": 30,
  "send_keepalive": true,
  "detour": ["client_listen"]
}
```

### 3. Get Listen Component Connections

**Endpoint:** `GET /api/listen/{tag}`

**Description:** Get the connection status of a specified listen component.

**Parameters:**
- `tag` (path parameter): Component tag

**Response Example:**
```json
{
  "tag": "listen_component",
  "listen_addr": "0.0.0.0:8080",
  "connections": [
    {
      "address": "192.168.1.100:12345",
      "last_active": "2023-12-01T10:30:00Z",
      "connection_id": "abc123",
      "is_authenticated": true
    }
  ],
  "count": 1
}
```

### 4. Get Forward Component Connections

**Endpoint:** `GET /api/forward/{tag}`

**Description:** Get the connection status of a specified forward component.

**Parameters:**
- `tag` (path parameter): Component tag

**Response Example:**
```json
{
  "tag": "forward_component",
  "connections": [
    {
      "remote_addr": "192.168.1.200:8080",
      "is_connected": true,
      "last_reconnect": "2023-12-01T10:00:00Z",
      "auth_retry_count": 0,
      "heartbeat_miss": 0,
      "last_heartbeat": "2023-12-01T10:35:00Z",
      "is_authenticated": true
    }
  ],
  "count": 1
}
```

### 5. Get TCP Tunnel Listen Connections

**Endpoint:** `GET /api/tcp_tunnel_listen/{tag}`

**Description:** Get the connection pool status of a specified TCP tunnel listen component.

**Parameters:**
- `tag` (path parameter): Component tag

**Response Example:**
```json
{
  "tag": "tcp_tunnel_listen",
  "listen_addr": "0.0.0.0:9090",
  "pools": [
    {
      "forward_id": "abc123",
      "pool_id": "def456",
      "remote_addr": "192.168.1.300:9090",
      "connections": [
        {
          "remote_addr": "192.168.1.300:54321",
          "is_authenticated": true,
          "last_active": "2023-12-01T10:40:00Z",
          "heartbeat_miss": 0,
          "last_heartbeat": "2023-12-01T10:40:30Z"
        }
      ],
      "conn_count": 1
    }
  ],
  "total_connections": 1
}
```

### 6. Get TCP Tunnel Forward Connections

**Endpoint:** `GET /api/tcp_tunnel_forward/{tag}`

**Description:** Get the connection pool status of a specified TCP tunnel forward component.

**Parameters:**
- `tag` (path parameter): Component tag

**Response Example:**
```json
{
  "tag": "tcp_tunnel_forward",
  "forward_id": "abc123",
  "pools": [
    {
      "pool_id": "def456",
      "remote_addr": "192.168.1.400:9090",
      "connections": [
        {
          "remote_addr": "192.168.1.400:54321",
          "is_authenticated": true,
          "last_active": "2023-12-01T10:45:00Z",
          "heartbeat_miss": 0,
          "last_heartbeat": "2023-12-01T10:45:30Z"
        }
      ],
      "conn_count": 1,
      "target_count": 5
    }
  ],
  "total_connections": 1
}
```

### 7. Get Load Balancer Traffic Statistics

**Endpoint:** `GET /api/load_balancer/{tag}`

**Description:** Get traffic statistics for a specified load balancer component.

**Parameters:**
- `tag` (path parameter): Component tag

**Response Example:**
```json
{
  "tag": "load_balancer",
  "bytes_per_sec": 1024000,
  "packets_per_sec": 100,
  "total_bytes": 10240000,
  "total_packets": 1000,
  "current_bytes": 1024,
  "current_packets": 10,
  "samples": [
    {
      "bytes": 1024,
      "packets": 10
    }
  ],
  "window_size": 60
}
```

### 8. Get Filter Component Info

**Endpoint:** `GET /api/filter/{tag}`

**Description:** Get the configuration information of a specified filter component.

**Parameters:**
- `tag` (path parameter): Component tag

**Response Example:**
```json
{
  "tag": "protocol_filter",
  "type": "filter",
  "use_proto_detectors": ["wireguard", "openvpn", "game_protocol"],
  "detour": {
    "wireguard": ["wg_forward"],
    "openvpn": ["ovpn_forward"],
    "game_protocol": ["game_forward"]
  },
  "detour_miss": ["default_forward"]
}
```

### 9. Serve H5 Files

**Endpoint:** `GET /h5/{file_path}`

**Description:** When `h5_files_path` is configured, this endpoint serves H5 files from the specified directory.

**Parameters:**
- `file_path` (path parameter): Path to the H5 file relative to the configured `h5_files_path` directory

**Response:** The requested H5 file content, with appropriate content type.

## Detour Field Explanation

### Detour Field Types

**Simple string array (for listen and forward components):**
```json
"detour": ["target_component"]
```

**Protocol mapping object (for filter components):**
```json
"detour": {
  "protocol_name": ["target_component1", "target_component2"],
  "another_protocol": ["target_component3"]
}
```

**Load balancing rule array (for load_balancer components):**
```json
"detour": [
  {
    "rule": "seq % 2 == 0",
    "targets": ["component1"]
  },
  {
    "rule": "seq % 2 == 1",
    "targets": ["component2"]
  }
]
```

### Component Type Specific Fields

- **listen component**: `listen_addr`, `timeout`, `replace_old_mapping`
- **forward component**: `forwarders`, `reconnect_interval`, `connection_check_time`, `send_keepalive`
- **load_balancer component**: `window_size`
- **filter component**: `use_proto_detectors`, `detour_miss`
- **tcp_tunnel_listen component**: `listen_addr`, `pools`
- **tcp_tunnel_forward component**: `pools`, `target_count`

## Error Handling

The API server uses standard HTTP status codes:

- `200 OK`: Success
- `400 Bad Request`: Invalid request parameters
- `404 Not Found`: Component not found
- `405 Method Not Allowed`: Unsupported HTTP method
- `500 Internal Server Error`: