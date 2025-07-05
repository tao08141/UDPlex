# UDPlex TCP Tunnel Forward Component

## Overview
The TCP Tunnel Forward component is responsible for establishing connections to TCP tunnel servers, encapsulating UDP packets for transmission over TCP connections, and receiving UDP packets returned from the TCP tunnel. It serves as the client part of the TCP tunnel, allowing UDP packets to be transmitted over TCP connections, suitable for network environments where UDP packets cannot be transmitted directly.

## Component Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `tcp_tunnel_forward`, indicates a TCP tunnel forwarding end |
| `tag` | Unique component identifier, used for reference in detour |
| `forwarders` | List of target server addresses, format "IP:port[:connection_count]", e.g. "1.2.3.4:9001:4". Using multiple connections may cause UDP packet reordering, which might lead to unknown issues in some scenarios |
| `connection_check_time` | Connection check interval (seconds), regularly checks and reconnects broken connections |
| `no_delay` | Whether to enable TCP Nagle algorithm, true means disable Nagle algorithm to reduce latency |
| `detour` | Forwarding path, specifies the component identifiers that receive return data |
| `auth` | Authentication configuration, see the authentication section |

## Configuration Example

```json
{
    "type": "tcp_tunnel_forward",
    "tag": "tcp_tunnel_client",
    "forwarders": ["203.0.113.1:9001:2", "203.0.113.2:9001:2"],
    "connection_check_time": 30,
    "no_delay": true,
    "detour": ["listen_component"],
    "auth": {
        "enabled": true,
        "secret": "your-strong-password",
        "enable_encryption": true,
        "heartbeat_interval": 30
    }
}
```

## How It Works

1. The TCP Tunnel Forward component attempts to connect to all configured TCP tunnel servers when started
2. For each server address, the component establishes the specified number of TCP connections (indicated by the number after the address)
3. When a UDP packet is received, the component:
   - Encapsulates the UDP packet within a TCP connection
   - Performs authentication processing if authentication is enabled
   - Sends the encapsulated packet over the TCP connection
4. When a packet is received from the TCP tunnel, the component decapsulates the packet and forwards it to the components specified in `detour`
5. The component regularly checks connection status (as specified by `connection_check_time`) and attempts to reconnect broken connections
6. When `no_delay` is set to true, the TCP Nagle algorithm is disabled, reducing data transmission latency

## Use Cases

- Transmitting UDP packets in network environments where UDP is blocked by firewalls
- Implementing reliable transmission of UDP packets through TCP tunnels
- Using UDP-based applications in networks that only allow TCP connections
- As an exit point for VPNs or game accelerators, transmitting UDP game data through TCP tunnels