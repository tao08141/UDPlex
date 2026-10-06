# UDPlex TCP Forward Component

## Overview
The TCP Forward component is the far end (machine B) of [TCP Listen](tcp_listen_en.md) in stream mode. It receives stream frames from the pipeline (usually a `tcp_tunnel_listen`), dials the target of each stream and sends the target's replies back through `detour` as stream frames. Deduplication, reordering, acknowledgements, retransmission and flow control work as in `tcp_listen`.

## Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | `tcp_forward` |
| `tag` | Unique component identifier |
| `detour` | Where return frames are sent, usually the `tcp_tunnel_listen` that receives the streams (list several, or use a `load_balancer`, with multiple lines) |
| `target` | Optional fixed target. When set, the `target` sent by `tcp_listen` is ignored |
| `interface_name` | Optional outbound interface for dialing the target |
| `window_size` | Receive window per stream in bytes, default 1048576 |
| `timeout` | Reset when unacknowledged data makes no progress for this many seconds, default 60 |
| `no_delay` | Set TCP_NODELAY, default `true` |

## Example

```yaml
- type: tcp_tunnel_listen
  tag: server_listen
  listen_addr: 0.0.0.0:9001
  broadcast_mode: false
  detour: [tcp_out]
  auth:
    enabled: true
    secret: your-secret-key-here
    enable_encryption: true

- type: tcp_forward
  tag: tcp_out
  detour: [server_listen]
  # target: 127.0.0.1:80
```

## How it works

1. A frame of an unknown stream creates it; the OPEN frame makes it dial the target (10 s timeout). A failed dial sends an RST back to machine A.
2. The OPEN may arrive after data that took a faster line; that data is buffered meanwhile. Without an OPEN within 10 seconds the stream is reset.
3. After both directions are done, the stream is kept for 30 seconds to answer late retransmissions.

## Security

Without `target`, machine B dials whatever address machine A requests. Always enable `auth` on the tunnel (ideally with encryption), or set `target` to pin the destination.
