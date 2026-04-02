# UDPlex `wg` Component

The `wg` component embeds `wireguard-go` directly into UDPlex. It creates a WireGuard interface in-process, keeps encryption and TUN handling inside the current binary, and sends WireGuard packets through normal UDPlex detours instead of a kernel UDP socket or an external `wireguard-go` process.

## What It Solves

- Removes the extra user-space to kernel-space WireGuard forwarding hop used by external `wg` setups.
- Lets WireGuard traffic keep using UDPlex `forward` or `tcp_tunnel` components.
- Learns the incoming UDPlex return path and reuses it for WireGuard replies.
- Supports creating the WireGuard NIC automatically on Linux.

## Configuration

```yaml
- type: wg
  tag: wg_client
  interface_name: wg0
  mtu: 1420
  addresses:
    - 10.6.0.2/24
  private_key: YOUR_PRIVATE_KEY_HEX
  listen_port: 51820
  detour: [line_a, line_b]
  route_allowed_ips: false
  peers:
    - public_key: REMOTE_PUBLIC_KEY_HEX
      endpoint: udplex-server:51820
      allowed_ips:
        - 10.6.0.1/32
      persistent_keepalive: 25
```

## Fields

| Field | Description |
|---|---|
| `type` | Must be `wg` |
| `tag` | Component tag |
| `interface_name` | Interface name to create, such as `wg0` |
| `mtu` | Interface MTU, default `1420` |
| `addresses` | Interface addresses to assign on Linux, such as `10.6.0.2/24` |
| `private_key` | WireGuard private key in hex |
| `listen_port` | Logical WireGuard listen port |
| `detour` | Default UDPlex path for initial outbound WireGuard packets |
| `routes` | Extra Linux routes to add through the interface |
| `route_allowed_ips` | When `true`, also add every peer `allowed_ips` as Linux routes |
| `setup_interface` | When `true`, automatically configures MTU, addresses, link state, and routes on Linux |
| `reuse_incoming_detour` | When `true`, replies reuse the source component of the received packet |
| `peers` | Standard WireGuard peer list |

## Peer Fields

| Field | Description |
|---|---|
| `public_key` | Peer public key in hex |
| `preshared_key` | Optional preshared key in hex |
| `endpoint` | Initial endpoint string. This can be a normal `host:port` or a logical label like `udplex-server:51820` |
| `allowed_ips` | WireGuard allowed IP list |
| `persistent_keepalive` | Persistent keepalive interval in seconds |

## Detour Behavior

- Client side: the `wg` component uses its own `detour` for the initial handshake and all traffic before a peer learns a return path.
- Server side: once a packet arrives from a `listen` or `tcp_tunnel_listen` component, the `wg` component remembers that source path and sends replies back through the same component.

## Notes

- Automatic address/route setup is currently implemented for Linux only.
- For `forward` mode, enable UDPlex auth if you want per-session `ConnID` preservation across multiple clients.
- For `tcp_tunnel` mode, set `broadcast_mode: false` to make the tunnel route by `ConnID`.

## Examples

- `examples/wg_component_forward_client.yaml`
- `examples/wg_component_forward_server.yaml`
- `examples/wg_component_tcp_tunnel_client.yaml`
- `examples/wg_component_tcp_tunnel_server.yaml`
