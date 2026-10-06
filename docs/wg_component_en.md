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
| `bind_mode` | `udplex` (default): WireGuard packets are exchanged with other UDPlex components. `native`: listen on `listen_port` directly so ordinary WireGuard clients can connect |
| `peers` | Standard WireGuard peer list |

The kernel network fields `ip_forward`, `mss_clamp`, `policy_routes` and `masquerade` are described in [Kernel Network Setup](#kernel-network-setup).

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

## Accepting External Clients (`bind_mode: native`)

With `bind_mode: native` the component listens on `listen_port` like a normal WireGuard server, so phones and PCs running the official WireGuard apps connect to it directly. Combined with the kernel network fields below, their traffic can be sent into another tunnel, for example an embedded `wg` component carried over UDPlex lines:

```yaml
- type: wg
  tag: wg_access
  bind_mode: native
  interface_name: wg_access
  listen_port: 51821
  addresses: [10.8.0.1/24]
  private_key: ACCESS_PRIVATE_KEY
  ip_forward: true
  mss_clamp: true
  policy_routes:
    - {from: [10.8.0.0/24], table: 7100, priority: 7100, dev: wg_gw}
  masquerade:
    - {source: 10.8.0.0/24, out_interface: wg_gw}
  peers:
    - public_key: CLIENT_PUBLIC_KEY
      allowed_ips: [10.8.0.2/32]
```

`detour` is not used in native mode, and packets routed to the component by other components are dropped.

When the inner tunnel carries traffic of external clients to the internet, the entry side peer needs `allowed_ips: [0.0.0.0/0]`, otherwise WireGuard drops replies from internet addresses. Keep `route_allowed_ips: false` so the host routing table is not changed.

## Kernel Network Setup

These fields are shared by the `wg` and `openvpn` components. They are applied after every component has started, so `dev` and `out_interface` may name interfaces of other components. Everything is removed again when UDPlex stops (SIGINT/SIGTERM). Linux only.

| Field | Description |
|---|---|
| `ip_forward` | Enable kernel forwarding and accept forwarded traffic of this interface in the `FORWARD` chain (needed when Docker sets the chain policy to `DROP`) |
| `mss_clamp` | Clamp the TCP MSS of forwarded connections of this interface to the path MTU |
| `policy_routes` | Source based routing, see below |
| `masquerade` | Source NAT, see below |

`policy_routes` entries:

| Field | Description |
|---|---|
| `from` | Source prefixes looked up in `table` (`ip rule add from ... lookup ...`) |
| `table` | Routing table id |
| `priority` | `ip rule` priority, optional |
| `dev` | Interface the table routes to, defaults to this component's interface |
| `routes` | Destinations sent to `dev`, defaults to the default route of each `from` family |

Traffic between addresses of a `from` prefix (for example two clients of one pool) falls back to the main table.

`masquerade` entries:

| Field | Description |
|---|---|
| `source` | Source prefix to masquerade |
| `out_interface` | Egress interface. `auto` uses the interface of the default route, empty masquerades towards any destination outside `source` |

Notes:

- In Docker, `/proc/sys` is read-only: enable forwarding on the host (`sysctl -w net.ipv4.ip_forward=1`). UDPlex only checks that it is on.
- iptables rules are written with the backend (legacy or nft) that already holds the host's rules, so they work next to Docker's rules. Set `UDPLEX_IPTABLES_BACKEND=legacy` or `nft` to choose it explicitly.

## Notes

- Automatic address/route setup is currently implemented for Linux only.
- For `forward` mode, enable UDPlex auth if you want per-session `ConnID` preservation across multiple clients.
- For `tcp_tunnel` mode, set `broadcast_mode: false` to make the tunnel route by `ConnID`.

## Examples

- `examples/wg_component_forward_client.yaml`
- `examples/wg_component_forward_server.yaml`
- `examples/wg_component_tcp_tunnel_client.yaml`
- `examples/wg_component_tcp_tunnel_server.yaml`
