# UDPlex `openvpn` Component

The `openvpn` component embeds an OpenVPN server ([sing-openvpn](https://github.com/SagerNet/sing-openvpn), the implementation used by sing-box) in the UDPlex process. It creates its own TUN interface and accepts ordinary OpenVPN clients (OpenVPN 2.x, OpenVPN Connect and others) without running an `openvpn` process. Together with the [kernel network fields](wg_component_en.md#kernel-network-setup) the traffic of the clients can be sent through another tunnel, for example an embedded `wg` component carried over UDPlex lines.

## Configuration

```yaml
- type: openvpn
  tag: ovpn_access
  listen_addr: 0.0.0.0:1194
  proto: udp
  interface_name: ovpn_access
  mtu: 1420
  addresses: [10.9.0.1/24]
  ca: /etc/udplex/pki/ca.crt
  cert: /etc/udplex/pki/server.crt
  key: /etc/udplex/pki/server.key
  tls_crypt: /etc/udplex/pki/tc.key
  crl_verify: /etc/udplex/pki/crl.pem
  push_dns: [1.1.1.1]
  redirect_gateway: true
  ip_forward: true
  mss_clamp: true
  policy_routes:
    - {from: [10.9.0.0/24], table: 7100, priority: 7101, dev: wg_gw}
  masquerade:
    - {source: 10.9.0.0/24, out_interface: wg_gw}
```

## Fields

| Field | Description |
|---|---|
| `bind_mode` | `native` (default): listen on `listen_addr`. `udplex`: exchange OpenVPN datagrams with other components (see below) |
| `listen_addr` | Listen address in native bind mode |
| `proto` | `udp` (default) or `tcp`. `tcp` needs native bind mode |
| `detour` | udplex bind mode: path of replies when the incoming path is not reused |
| `reuse_incoming_detour` | udplex bind mode: reply through the component a client's packets came from, default `true` |
| `interface_name` | TUN interface name, defaults to the tag |
| `mtu` | Interface MTU, default `1500`. Use `1420` when clients are forwarded into a WireGuard tunnel |
| `addresses` | Server address with the client pool, e.g. `10.9.0.1/24`; clients get the other addresses. At most one IPv4 and one IPv6 pool |
| `routes` | Extra routes through the interface |
| `topology` | `subnet` (default), `net30` or `p2p` |
| `setup_interface` | Configure MTU, addresses, link state and routes on Linux, default `true` |
| `max_clients` | Maximum number of clients, 0 for no limit |
| `ca` | CA that signs client certificates |
| `cert`, `key` | Server certificate and private key |
| `tls_crypt`, `tls_crypt_v2`, `tls_auth` | Control channel protection, at most one. `key_direction` (0/1) applies to `tls_auth` |
| `crl_verify` | CRL file of revoked client certificates, read on every handshake |
| `verify_client_certificate` | `require` (default when `ca` is set), `optional` or `none` |
| `users` | `auth-user-pass` accounts: `[{username, password}]`. Without `ca`, users log in with a password only |
| `duplicate_cn` | Allow several clients with the same certificate |
| `data_ciphers`, `data_ciphers_fallback`, `auth` | Data channel cipher negotiation |
| `push_routes` | Routes pushed to clients |
| `push_dns` | DNS servers pushed to clients |
| `redirect_gateway` | Push `redirect-gateway def1` (all client traffic through the tunnel) |
| `keepalive_interval`, `keepalive_timeout` | Like `keepalive 10 60`, the defaults |

Certificates and keys take PEM content or a file path. The kernel network fields `ip_forward`, `mss_clamp`, `policy_routes` and `masquerade` work as in the [`wg` component](wg_component_en.md#kernel-network-setup).

## Client Profile

```
client
dev tun
proto udp
remote YOUR_SERVER 1194
nobind
remote-cert-tls server
tun-mtu 1420
<ca>...</ca>
<cert>...</cert>
<key>...</key>
<tls-crypt>...</tls-crypt>
```

## `udplex` Bind Mode

In `udplex` bind mode the server takes its datagrams from other components, so OpenVPN clients can reach it through UDPlex `listen` components or multiple lines. Clients are told apart by the connection id of their packets: use a `listen` component (one id per client address) or lines with `auth` enabled.

```yaml
- type: listen
  tag: ovpn_in
  listen_addr: 0.0.0.0:1194
  timeout: 120
  broadcast_mode: false
  detour: [ovpn]

- type: openvpn
  tag: ovpn
  bind_mode: udplex
  addresses: [10.9.0.1/24]
  ca: ca.crt
  cert: server.crt
  key: server.key
```

## Notes

- Only TLS mode is supported (no static key mode).
- Interface setup and the kernel network fields are Linux only.
- The [access gateway script](udplex_gateway_en.md) sets up the certificates, the server and client profiles.
