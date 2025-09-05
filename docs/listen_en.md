# UDPlex Listen Component

## Overview
The Listen component is responsible for listening on a specified UDP port, receiving packets from clients, and forwarding them to configured target components. It serves as the entry point in the UDPlex system, handling all inbound connections and packets.

## Component Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `listen`, indicates a listening component |
| `tag` | Unique component identifier, used for reference in detour |
| `listen_addr` | Listening address and port, format "IP:port", e.g. "0.0.0.0:9000" |
| `timeout` | Connection timeout (seconds), after which mappings are cleared if no data is transmitted |
| `replace_old_mapping` | Whether to replace old mappings, when true new mappings replace old mappings with the same address |
| `detour` | Forwarding path, specifies the component identifiers that receive data |
| `auth` | Authentication configuration, see the authentication section |

## Configuration Example

```yaml
type: listen
tag: client_listen
listen_addr: 0.0.0.0:9000
timeout: 120
replace_old_mapping: true
detour:
  - forward_component
auth:
  enabled: true
  secret: your-strong-password
  enable_encryption: true
  heartbeat_interval: 30
```

## How It Works

1. The Listen component binds to the specified IP address and port when started
2. When a UDP packet is received, the component:
   - Records the client's source address and port
   - Performs authentication verification if authentication is enabled
   - Forwards the packet to the components specified in `detour`
3. The component maintains a mapping table from client addresses to internal connection IDs
4. If no data is received from a client within the time specified by `timeout`, the corresponding mapping is cleared
5. When `replace_old_mapping` is set to true, if a packet is received from the same address but a different port, the new mapping replaces the old one

## Use Cases

- As an entry point for UDP forwarding services
- Receiving connection requests from game clients
- As an access point for VPNs or accelerators
- Any scenario requiring the reception and processing of UDP packets