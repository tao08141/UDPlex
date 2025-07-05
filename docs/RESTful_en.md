# API Server Documentation

## Overview

The API server provides RESTful API endpoints for monitoring and managing router component states. The server supports various component types including Listen, Forward, TCP Tunnel, and Load Balancer components.

## Configuration

Add the following configuration to the configuration file:
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
- `port`: The port on which the API server listens
- `host`: The host address on which the API server listens
- `h5_files_path`: Path to a directory containing H5 files to be served. When configured, the API server will serve H5 files from this directory at the "/h5/" endpoint.

## API Endpoints

### 1. Get All Components

**Endpoint:** `GET /api/components`

**Description:** Retrieve a list of all registered components

**Response Format:**
```
json
[
  {
    "tag": "component1",
    "type": "listen"
  },
  {
    "tag": "component2",
    "type": "forward"
  }
]
```
### 2. Get Component by Tag

**Endpoint:** `GET /api/components/{tag}`

**Description:** Retrieve information about a specific component by its tag

**Parameters:**
- `tag` (path parameter): Component tag

**Response Format:**
```
json
{
  "tag": "component1",
  "type": "listen"
}
```
### 3. Get Listen Component Connections

**Endpoint:** `GET /api/listen/{tag}`

**Description:** Retrieve connection status for a specified listen component

**Parameters:**
- `tag` (path parameter): Component tag

**Response Format:**
```
json
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

**Description:** Retrieve connection status for a specified forward component

**Parameters:**
- `tag` (path parameter): Component tag

**Response Format:**
```
json
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

**Description:** Retrieve connection pool status for a specified TCP tunnel listen component

**Parameters:**
- `tag` (path parameter): Component tag

**Response Format:**
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
```


### 6. Get TCP Tunnel Forward Connections

**Endpoint:** `GET /api/tcp_tunnel_forward/{tag}`

**Description:** Retrieve connection pool status for a specified TCP tunnel forward component

**Parameters:**
- `tag` (path parameter): Component tag

**Response Format:**
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

**Description:** Retrieve traffic statistics for a specified load balancer component

**Parameters:**
- `tag` (path parameter): Component tag

**Response Format:**
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


### 8. Serve H5 Files

**Endpoint:** `GET /h5/{file_path}`

**Description:** When `h5_files_path` is configured, this endpoint serves H5 files from the specified directory.

**Parameters:**
- `file_path` (path parameter): Path to the H5 file relative to the configured `h5_files_path` directory

**Response:** The requested H5 file content with appropriate content type.

## Error Handling

The API server uses standard HTTP status codes:

- `200 OK`: Success
- `400 Bad Request`: Invalid request parameters
- `404 Not Found`: Component not found
- `405 Method Not Allowed`: Unsupported HTTP method
- `500 Internal Server Error`: Internal server error
