# UDPlex TCP Listen Component

## Overview
The TCP Listen component accepts TCP connections and forwards them, for TCP forwarding along `client → machine A → machine B → target`. It has two modes:

- **Stream mode** (`detour` and `target`): each TCP connection becomes a stream of frames that travels through the UDPlex pipeline (`tcp_tunnel_forward`, `load_balancer`, redundant lines...) to a [TCP Forward](tcp_forward_en.md) component on the other side, which dials `target`. Frames are sequenced: the receiver drops duplicates, reorders and acknowledges, and the sender retransmits what is not acknowledged. A stream can therefore be spread over several lines, and when a line fails its data is retransmitted over the others.
- **Direct mode** (`forwarders`): dials the target directly and relays the bytes. Use it with the `wg` component: machine A dials machine B's WireGuard address, and the kernel TCP inside the WireGuard tunnel takes care of reliability.

> Stream mode is supported over TCP tunnels (`tcp_tunnel_forward` / `tcp_tunnel_listen`) only. Carrying TCP over UDP lines (`forward` / `listen`) suffers from TCP-over-UDP problems; use wg with direct mode instead.

## Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | `tcp_listen` |
| `tag` | Unique component identifier |
| `listen_addr` | Listen address, e.g. `0.0.0.0:8080`. May be an address of a `wg` interface (listening starts after all components have started) |
| `target` | Stream mode: target dialed by the remote `tcp_forward`, e.g. `10.0.0.5:80` |
| `detour` | Stream mode: where stream frames are sent, e.g. `[line_a, line_b]` or `[load_balancer]` |
| `forwarders` | Direct mode: target addresses, tried in order. `address@interface` selects the outbound interface |
| `interface_name` | Direct mode: default outbound interface |
| `window_size` | Stream mode: receive window per stream in bytes, default 1048576. A stream reaches at most about window / RTT |
| `timeout` | Stream mode: reset the connection when unacknowledged data makes no progress for this many seconds, default 60 |
| `no_delay` | Set TCP_NODELAY, default `true` |

Set either `detour` or `forwarders`, not both.

## Examples

Stream mode, alternating between two TCP tunnel lines and moving to the remaining one when a line fails:

```yaml
- type: tcp_listen
  tag: tcp_in
  listen_addr: 0.0.0.0:8080
  target: 127.0.0.1:80
  detour: [lb]

- type: load_balancer
  tag: lb
  window_size: 10
  detour:
    - rule: available_line_a && (seq % 2 == 0 || !available_line_b)
      targets: [line_a]
    - rule: available_line_b && (seq % 2 == 1 || !available_line_a)
      targets: [line_b]

- type: tcp_tunnel_forward
  tag: line_a
  forwarders: [SERVER_IP_A:9001:2]
  broadcast_mode: false
  detour: [tcp_in]          # return frames go to tcp_listen
```

For redundancy (every frame on both lines), set `detour: [line_a, line_b]`. See `examples/tcp_forward_client.yaml` / `tcp_forward_server.yaml`.

Direct mode over WireGuard:

```yaml
- type: tcp_listen
  tag: tcp_in
  listen_addr: 0.0.0.0:8080
  forwarders: [10.66.0.1:8080]   # machine B's wg address
```

See `examples/tcp_over_wg_client.yaml` / `tcp_over_wg_server.yaml`.

## How stream mode works

1. An accepted connection gets a random stream ID, which is also the packets' ConnID: tunnels keep a stream on one connection pool and send replies back on the line it arrived on. An OPEN frame carries `target`.
2. Data read from the client is cut into DATA frames of up to `buffer_size - 100` bytes (1400 by default) and sent while the peer's window has room.
3. Unacknowledged frames are retransmitted on timeout (RTO from the measured RTT, 200 ms–10 s); out-of-order acknowledgements retransmit the first missing frame early. A retransmission may take another line.
4. Stream frames are never dropped by CoDel or the queue limit of a TCP tunnel's send queue; the windows bound how much of them can queue.
5. When the client closes its write side, a FIN half-closes the target connection; the connection is closed once both directions are done. Errors send an RST and reset the client connection.

## Notes

- When a tunnel also carries UDP traffic, `tcp_forward` ignores packets that are not stream frames; a filter with a protocol detector can also match the frame magic `UXTS` (`0x55585453`).
- Replies are routed by ConnID, so set `broadcast_mode: false` on `tcp_tunnel_forward` / `tcp_tunnel_listen`.
