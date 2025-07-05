# UDPlex TCP Tunnel Listen Component

## Overview
The TCP Tunnel Listen component is responsible for listening for TCP connections, receiving UDP packets transmitted through a TCP tunnel, and forwarding them to configured target components. It allows UDP packets to be transmitted over TCP connections, suitable for network environments where UDP packets cannot be transmitted directly.

## Component Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `tcp_tunnel_listen`, indicates a TCP tunnel listening end |
| `tag` | Unique component identifier, used for reference in detour |
| `listen_addr` | Listening address and port, format "IP:port", e.g. "0.0.0.0:9001" |
| `timeout` | Connection timeout (seconds), after which connections are closed if no data is transmitted |
| `no_delay` | Whether to enable TCP Nagle algorithm, true means disable Nagle algorithm to reduce latency |
| `detour` | Forwarding path, specifies the component identifiers that receive data |
| `auth` | Authentication configuration, see the authentication section |

## Configuration Example

```json
{
    "type": "tcp_tunnel_listen",
    "tag": "tcp_tunnel_server",
    "listen_addr": "0.0.0.0:9001",
    "timeout": 300,
    "no_delay": true,
    "detour": ["forward_component"],
    "auth": {
        "enabled": true,
        "secret": "your-strong-password",
        "enable_encryption": true,
        "heartbeat_interval": 30
    }
}
```

## How It Works

1. The TCP Tunnel Listen component binds to the specified IP address and port when started, listening for TCP connections
2. When a TCP connection request is received, the component:
   - Establishes a TCP connection
   - Performs authentication verification if authentication is enabled
   - Begins receiving UDP packets transmitted over the TCP connection
3. Received UDP packets are decapsulated and then forwarded to the components specified in `detour`
4. If no data is received within the time specified by `timeout`, the TCP connection is closed
5. When `no_delay` is set to true, the TCP Nagle algorithm is disabled, reducing data transmission latency

## Use Cases

- Transmitting UDP packets in network environments where UDP is blocked by firewalls
- Implementing reliable transmission of UDP packets through TCP tunnels
- Using UDP-based applications in networks that only allow TCP connections
- As an access point for VPNs or game accelerators, transmitting UDP game data through TCP tunnels