# UDPlex
[English](README.md) | [中文](README_ZH.md)

UDPlex is an efficient UDP packet bidirectional forwarding tool that supports forwarding UDP traffic to multiple target servers simultaneously, handling return traffic, and supporting authentication, encryption, and other features. It is suitable for gaming acceleration, network redundancy, and traffic distribution scenarios.

## Features

- Listen on specified UDP ports to receive packets
- Forward packets in parallel to multiple target servers
- Support bidirectional traffic forwarding
- Automatic reconnection for broken connections
- Configurable buffer sizes and timeout parameters
- Support authentication and encrypted transmission
- Support protocol detection and filtering
- Support Docker deployment

## Usage

1. Edit the config.json file to configure listening addresses and forwarding targets
2. Run the program:

```bash
# Use default configuration file
./UDPlex

# Or specify configuration file path
./UDPlex -c /path/to/config.json
```

## Docker Usage

### Using Docker Commands

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

> Note: For UDP forwarding applications, it is recommended to use host network mode (network_mode: host) for optimal performance. If precise port mapping control is needed, you can use port mapping mode.

## WireGuard One-click Deployment Tutorial

- UDPlex + WireGuard: One-click deployment guide (English): [docs/udplex_wireguard_en.md](docs/udplex_wireguard_en.md)

# UDPlex Parameter Details

## Global Configuration

| Parameter | Description |
|-----------|-------------|
| `buffer_size` | UDP packet buffer size (bytes), recommended to set to MTU size, usually 1500 |
| `queue_size` | Inter-component packet queue size, increase for high traffic scenarios |
| `worker_count` | Number of worker threads, affects concurrent processing capability |
| `services` | Component configuration array, defines all processing components in the system |
| `protocol_detectors` | Protocol detector configuration for identifying and filtering packets of specific protocols |

## Service Component Parameters

UDPlex supports multiple component types, each with specific functions and configuration parameters. For detailed documentation, please refer to:

- [Listen Component](docs/listen_en.md) - Listen on UDP ports and receive packets
- [Forward Component](docs/forward_en.md) - Forward packets to target servers
- [Filter Component](docs/filter_en.md) - Filter and classify packets based on protocol characteristics
- [TCP Tunnel Listen Component](docs/tcp_tunnel_listen_en.md) - TCP tunnel listening endpoint
- [TCP Tunnel Forward Component](docs/tcp_tunnel_forward_en.md) - TCP tunnel forwarding endpoint
- [Load Balancer Component](docs/load_balancer_en.md) - Load balancing component

### Authentication Configuration

UDPlex supports packet authentication and encryption features. For detailed documentation, please refer to:

- [Authentication Protocol](docs/auth_protocol_en.md) - Detailed authentication protocol description

## Protocol Detectors

UDPlex supports configuring protocol detectors to identify and classify specific protocols in UDP packets. For detailed documentation, please refer to:

- [Protocol Detectors](docs/protocol_detector_en.md) - Protocol detector configuration and usage instructions

## Development Roadmap
- [X] Support packet filtering and selective forwarding
- [X] Support authentication, encryption, deduplication, and other features
- [X] Support UDP over TCP forwarding
- [X] Support more complex load balancing algorithms
- [X] RESTful API interface

## RESTful API Interface
UDPlex provides RESTful API interfaces to query component status and connection information.

- [RESTful](docs/RESTful_en.md) - RESTful interface configuration and usage instructions

## Use Cases
- **Game Acceleration**: Simultaneously forwards game traffic through multiple paths, greatly reducing packet loss.
- **Bandwidth Aggregation**: Aggregates the bandwidth of multiple network connections to improve overall network speed.
- **Protocol-based Classification**: Uses protocol detectors to distribute different protocol traffic to different processing paths, such as prioritizing important heartbeat packets.
- **UDP over TCP Tunnel**: Transmits UDP traffic in network environments that do not support UDP.
- **Network Debugging**: Forwards UDP traffic to a specified server for debugging and analysis without affecting the original network structure.
- **Load Balancing**: Intelligently distributes packets based on traffic volume and load balancing strategies, supporting various algorithms. For example, when traffic is low, packets are forwarded to both servers to avoid packet loss; when traffic exceeds a certain threshold, packets are distributed in a round-robin manner to ensure bandwidth.

## Configuration Examples

The examples directory contains configuration examples for various use cases:

- [**basic.json**](examples/basic.json) - Basic UDP forwarding configuration example
- [**auth_client.json**](examples/auth_client.json) - Authenticated UDP client configuration
- [**auth_server.json**](examples/auth_server.json) - Authenticated UDP server configuration
- [**redundant_client.json**](examples/redundant_client.json) - UDP redundancy client configuration, sends traffic to multiple servers simultaneously
- [**redundant_server.json**](examples/redundant_server.json) - UDP redundancy server configuration, receives client traffic and forwards
- [**wg_bidirectional_client.json**](examples/wg_bidirectional_client.json) - WireGuard UDP upstream/downstream separation client configuration
- [**wg_bidirectional_server.json**](examples/wg_bidirectional_server.json) - WireGuard UDP upstream/downstream separation server configuration
- [**tcp_tunnel_server.json**](examples/tcp_tunnel_server.json) - TCP tunnel server configuration, listens for TCP connections and forwards UDP traffic
- [**tcp_tunnel_client.json**](examples/tcp_tunnel_client.json) - TCP tunnel client configuration, connects to TCP tunnel service and forwards UDP traffic
- [**load_balancer_bandwidth_threshold.json**](examples/load_balancer_bandwidth_threshold.json) - Bandwidth threshold-based load balancing configuration, forwards to two servers when traffic ≤ 100M, forwards to one server when > 100M
- [**load_balancer_equal_distribution.json**](examples/load_balancer_equal_distribution.json) - Equal load distribution configuration, distributes data to two servers in 1:1 ratio