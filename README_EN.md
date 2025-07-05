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

UDPlex supports various component types, each with specific functions and configuration parameters. For detailed documentation, please refer to:

- [Listen Component](docs/listen_en.md) - Listen on UDP ports and receive packets
- [Forward Component](docs/forward_en.md) - Forward packets to target servers
- [Filter Component](docs/filter_en.md) - Filter and categorize packets based on protocol characteristics
- [TCP Tunnel Listen Component](docs/tcp_tunnel_listen_en.md) - TCP tunnel listening end
- [TCP Tunnel Forward Component](docs/tcp_tunnel_forward_en.md) - TCP tunnel forwarding end
- [Load Balancer Component](docs/load_balancer_en.md) - Load balancing component

### Authentication Configuration

UDPlex supports packet authentication and encryption features. For detailed documentation, please refer to:

- [Authentication Protocol](docs/auth_protocol_en.md) - Detailed explanation of the authentication protocol

## Protocol Detector

UDPlex supports configuring protocol detectors to identify and categorize specific protocols in UDP packets. For detailed documentation, please refer to:

- [Protocol Detector](docs/protocol_detector_en.md) - Protocol detector configuration and usage instructions

## Development Roadmap
- [X] Support packet filtering and selective forwarding
- [X] Support authentication, encryption, deduplication, and other features
- [X] Support UDP Over TCP forwarding
- [X] Support more complex load balancing algorithms
- [ ] RESTful API interface

## Use Cases
- Game acceleration: Forward game traffic to multiple servers simultaneously, selecting the fastest response
- Network redundancy: Ensure important UDP data can be transmitted through multiple paths
- Traffic distribution: Replicate UDP traffic to multiple targets for processing


## Configuration Examples

The examples directory contains configuration examples for various usage scenarios:

- [**basic.json**](examples/basic.json) - Basic configuration example for UDP forwarding
- [**auth_client.json**](examples/auth_client.json) - UDP client configuration with authentication
- [**auth_server.json**](examples/auth_server.json) - UDP server configuration with authentication
- [**redundant_client.json**](examples/redundant_client.json) - UDP redundant client configuration, sending traffic to multiple servers simultaneously
- [**redundant_server.json**](examples/redundant_server.json) - UDP redundant server configuration, receiving client traffic and forwarding
- [**wg_bidirectional_client.json**](examples/wg_bidirectional_client.json) - WireGuard UDP bidirectional separated communication client configuration
- [**wg_bidirectional_server.json**](examples/wg_bidirectional_server.json) - WireGuard UDP bidirectional separated communication server configuration
- [**tcp_tunnel_server.json**](examples/tcp_tunnel_server.json) - TCP tunnel server configuration, listening for TCP connections and forwarding UDP traffic
- [**tcp_tunnel_client.json**](examples/tcp_tunnel_client.json) - TCP tunnel client configuration, connecting to a TCP tunnel server and forwarding UDP traffic
