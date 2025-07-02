# UDPlex

[English](README_EN.md) | [中文](README.md)

UDPlex is an efficient bidirectional UDP packet forwarding tool that supports forwarding UDP traffic to multiple target servers simultaneously, handling return traffic, and providing authentication and encryption features. It is suitable for game acceleration, network redundancy, and traffic distribution scenarios.

## Features

- Listen on specified UDP ports to receive packets
- Forward packets in parallel to multiple target servers
- Support bidirectional traffic forwarding
- Automatically reconnect broken connections
- Configurable buffer size and timeout parameters
- Support for authentication and encrypted transmission
- Support for protocol detection and filtering
- Support for Docker deployment

## Usage

1. Edit the config.json file to configure listening address and forwarding targets
2. Run the program:

```bash
# Use the default configuration file
./UDPlex

# Or specify a configuration file path
./UDPlex -c /path/to/config.json
```

## Docker Usage

### Using Docker Command

```bash
# Pull the image
docker pull ghcr.io/tao08141/udplex:latest

# Run container (using host network mode)
docker run -d --name udplex --network host \
  -v $(pwd)/config.json:/app/config.json \
  ghcr.io/tao08141/udplex:latest

# Using port mapping mode (if not using host network mode)
docker run -d --name udplex \
  -v $(pwd)/config.json:/app/config.json \
  -p 9000:9000/udp \
  ghcr.io/tao08141/udplex:latest
```

### Using Docker Compose

1. Download the docker-compose.yml file:

```bash
mkdir udplex && cd udplex
# Download docker-compose.yml file
curl -o docker-compose.yml https://raw.githubusercontent.com/tao08141/UDPlex/refs/heads/master/docker-compose.yml
# Download configuration file
curl -o config.json https://raw.githubusercontent.com/tao08141/UDPlex/refs/heads/master/examples/basic.json
```

2. Start the service:

```bash
docker-compose up -d
```

3. View logs:

```bash
docker-compose logs -f
```

4. Stop the service:

```bash
docker-compose down
```

> Note: For UDP forwarding applications, it's recommended to use host network mode (network_mode: host) for best performance. If you need precise control over port mapping, you can use port mapping mode.

# UDPlex Parameter Guide

## Global Configuration

| Parameter | Description |
|-----------|-------------|
| `buffer_size` | UDP packet buffer size (bytes), recommended to set to MTU size, typically 1500 |
| `queue_size` | Size of packet queues between components, increase this value for high traffic scenarios |
| `worker_count` | Number of worker threads, affects concurrent processing capacity |
| `services` | Component configuration array, defines all processing components in the system |
| `protocol_detectors` | Protocol detector configuration, used to identify and filter specific protocol packets |

## Service Component Parameters

### listen Component Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `listen`, indicates a listening component |
| `tag` | Unique component identifier, used for reference in detour |
| `listen_addr` | Listening address and port, format "IP:port", e.g. "0.0.0.0:9000" |
| `timeout` | Connection timeout (seconds), after which mappings are cleared if no data is transmitted |
| `replace_old_mapping` | Whether to replace old mappings, when true new mappings replace old mappings with the same address |
| `detour` | Forwarding path, specifies the component identifiers that receive data |
| `auth` | Authentication configuration, see the authentication section |

### forward Component Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `forward`, indicates a forwarding component |
| `tag` | Unique component identifier, used for reference in detour |
| `forwarders` | List of forwarding target addresses, multiple targets can be configured for parallel forwarding |
| `reconnect_interval` | Reconnection interval (seconds), waiting time before attempting to reconnect after disconnection |
| `connection_check_time` | Connection check interval (seconds), time interval for regularly checking connection status |
| `send_keepalive` | Whether to send empty packets as heartbeats to keep the connection active |
| `detour` | Forwarding path, specifies the component identifiers that receive return data |
| `auth` | Authentication configuration, see the authentication section |
 
### filter Component Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `filter`, indicates a filtering component |
| `tag` | Unique component identifier, used for reference in detour |
| `use_proto_detectors` | List of protocol detectors to use, specifies the names of protocol detectors to apply |
| `detour` | Forwarding path object, keys are detector names, values are lists of target component identifiers after successful matching |
| `detour_miss` | Forwarding path when no protocol matches, specifies a list of component identifiers |

### tcp_tunnel_listen Component Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `tcp_tunnel_listen`, indicates a TCP tunnel listening end |
| `tag` | Unique component identifier, used for reference in detour |
| `listen_addr` | Listening address and port, format "IP:port", e.g. "0.0.0.0:9001" |
| `timeout` | Connection timeout (seconds), after which connections are closed if no data is transmitted |
| `no_delay` | Whether to enable TCP Nagle algorithm, true means disable Nagle algorithm to reduce latency |
| `detour` | Forwarding path, specifies the component identifiers that receive data |
| `auth` | Authentication configuration, see the authentication section |

### tcp_tunnel_forward Component Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `tcp_tunnel_forward`, indicates a TCP tunnel forwarding end |
| `tag` | Unique component identifier, used for reference in detour |
| `forwarders` | List of target server addresses, format "IP:port[:connection_count]", e.g. "1.2.3.4:9001:4". Using multiple connections may cause UDP packet reordering, which might lead to unknown issues in some scenarios |
| `connection_check_time` | Connection check interval (seconds), regularly checks and reconnects broken connections |
| `no_delay` | Whether to enable TCP Nagle algorithm, true means disable Nagle algorithm to reduce latency |
| `detour` | Forwarding path, specifies the component identifiers that receive return data |
| `auth` | Authentication configuration, see the authentication section |

### auth Authentication Configuration Parameters

| Parameter | Description |
|-----------|-------------|
| `enabled` | Whether to enable authentication, boolean value, true means enabled |
| `secret` | Authentication key, string, must be consistent between client and server |
| `enable_encryption` | Whether to enable data encryption (AES-GCM), true means encrypted transmission |
| `heartbeat_interval` | Heartbeat packet sending interval (seconds), used to keep connections alive, default 30 seconds |

#### Description

- The `auth` configuration is used for packet authentication and encryption.
- When enabled, handshake authentication is performed when establishing a connection, and data forwarding is only allowed after successful authentication.
- If `enable_encryption` is true, all packet contents will be encrypted using AES-GCM, enhancing security.
- `heartbeat_interval` controls the frequency of heartbeat packets, preventing connections from being disconnected due to long periods of inactivity.

#### Example

```json
"auth": {
    "enabled": true,
    "secret": "your-strong-password",
    "enable_encryption": true,
    "heartbeat_interval": 30
}
```

> Note: The `secret` must be consistent between client and server, otherwise authentication will fail.

## Protocol Detector Configuration

```json
"protocol_detectors": {
    "name": {
        "signatures": [
            {
                "offset": packet_offset,
                "bytes": "matching_byte_sequence",
                "mask": "applied_bit_mask",
                "hex": true/false,  // whether bytes is in hexadecimal format
                "length": { "min": minimum_length, "max": maximum_length },
                "contains": "contained_byte_sequence",
                "description": "signature_description"
            }
        ],
        "match_logic": "AND/OR",  // matching logic for multiple signatures
        "description": "protocol_detector_description"
    }
}
```

### Protocol Signature Parameters

| Parameter | Description |
|-----------|-------------|
| `offset` | Byte offset to start matching in the packet |
| `bytes` | Byte sequence to match, can be in hexadecimal or ASCII format |
| `mask` | Bit mask applied to the match, represented in hexadecimal |
| `hex` | Specifies whether bytes is in hexadecimal format, true for hexadecimal, false for ASCII |
| `length` | Packet length limit, including min (minimum length) and max (maximum length) |
| `contains` | Byte sequence that must be contained in the packet, for further filtering |
| `description` | Signature description, explains the protocol feature this signature matches |

### Protocol Detector Parameters

| Parameter | Description |
|-----------|-------------|
| `signatures` | Array of protocol signatures, defines features for identifying specific protocols |
| `match_logic` | Logical relationship between multiple signatures, "AND" means all must match, "OR" means any can match |
| `description` | Protocol detector description |

## Development Roadmap
- [X] Support packet filtering and selective forwarding
- [X] Support authentication, encryption, deduplication, and other features
- [X] Support UDP Over TCP forwarding
- [ ] Support more complex load balancing algorithms

## Use Cases
- Game acceleration: Forward game traffic to multiple servers simultaneously, selecting the fastest response
- Network redundancy: Ensure important UDP data can be transmitted through multiple paths
- Traffic distribution: Replicate UDP traffic to multiple targets for processing


## Configuration Examples

The examples directory contains configuration examples for various usage scenarios:

- **basic.json** - Basic configuration example for UDP forwarding
- **auth_client.json** - UDP client configuration with authentication
- **auth_server.json** - UDP server configuration with authentication
- **redundant_client_config.json** - UDP redundant client configuration, sending traffic to multiple servers simultaneously
- **redundant_server_config.json** - UDP redundant server configuration, receiving client traffic and forwarding
- **wg_bidirectional_client_config.json** - WireGuard UDP bidirectional separated communication client configuration
- **wg_bidirectional_server_config.json** - WireGuard UDP bidirectional separated communication server configuration
- **tcp_tunnel_server.json** - TCP tunnel server configuration, listening for TCP connections and forwarding UDP traffic
- **tcp_tunnel_client.json** - TCP tunnel client configuration, connecting to a TCP tunnel server and forwarding UDP traffic
