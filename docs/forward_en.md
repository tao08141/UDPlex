# UDPlex Forward Component

## Overview
The Forward component is responsible for forwarding packets to one or more target servers and handling the returned response data. It supports parallel forwarding, automatic reconnection, and connection keep-alive features, making it a core forwarding component in the UDPlex system.

## Component Parameters

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

## Configuration Example

```yaml
type: forward
tag: game_server_forward
forwarders:
  - 192.168.1.100:9001
  - 192.168.1.101:9001
reconnect_interval: 5
connection_check_time: 30
send_keepalive: true
detour:
  - client_listen
auth:
  enabled: true
  secret: your-strong-password
  enable_encryption: true
  heartbeat_interval: 30
```

## How It Works

1. The Forward component attempts to connect to all configured target servers when started
2. When a packet is received, the component:
   - Forwards the packet in parallel to all connected target servers
   - Performs authentication processing if authentication is enabled
3. When response data is received from a target server, the component forwards the data to the components specified in `detour`
4. The component regularly checks connection status (as specified by `connection_check_time`)
5. If a connection is broken, the component attempts to reconnect after the time specified by `reconnect_interval`
6. When `send_keepalive` is set to true, the component periodically sends empty packets as heartbeats to keep the connection active

## Use Cases

- Forwarding client data to game servers
- Implementing network redundancy by sending the same data to multiple servers simultaneously
- As an exit point for VPNs or accelerators
- Any scenario requiring UDP packets to be forwarded to one or more target servers