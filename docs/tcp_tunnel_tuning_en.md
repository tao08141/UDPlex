# TCP Tunnel: Latency Under Load

## The problem

A TCP tunnel never drops packets: when the line is full, data piles up in queues. Before this change there were three of them, all first-in first-out:

1. The send queue of each tunnel connection (up to `queue_size` packets, ~14 MB).
2. The kernel's TCP send buffer (auto-tuned, several MB).
3. The modem or ISP buffer, which the tunnel's own TCP congestion control fills.

A game packet sent while an upload runs waited behind all of it. Measured with WireGuard through one tunnel connection on a 40/200 Mbit/s line with a 200 ms modem buffer, game latency was **569 ms (p99 1.3 s) with 3% loss**. With 0.5% packet loss on the line it was over a second, and the transfer stalled.

## What UDPlex does now

- **Short kernel queue**: `TCP_NOTSENT_LOWAT` keeps the unsent data in the kernel to about 5 ms at the connection's measured rate (Linux; a fixed 128 KB on macOS; not available on Windows). Packets wait in UDPlex instead, where they can be prioritized.
- **Small packets first**: payloads up to `priority_size` (default 1000 bytes: games, voice, ACKs) skip the bulk data, up to `priority_share` of the bandwidth.
- **CoDel on bulk data**: when bulk packets wait longer than 5 ms for 100 ms, some are dropped before they enter the tunnel. The TCP flows inside the tunnel then slow down instead of filling the queue.
- **Connection scheduling**: with several connections (`IP:port:count`), packets stay on one connection and move to the least loaded one only when it backs up, instead of alternating per packet.

These are on by default and need no configuration. Two options deal with the third queue, the modem buffer:

- `pacing_rate` (Linux): cap the send rate of each connection below the line bandwidth, like the [shaper](shaper_en.md) does for UDP.
- `congestion: bbr` (Linux, needs the `tcp_bbr` kernel module): BBR keeps the bottleneck queue short on its own.

## Configuration

```yaml
  - type: tcp_tunnel_forward
    tag: tcp_line
    forwarders: [SERVER_IP:9001:4]
    pacing_rate: 38          # Mbit/s, ~95% of the upload bandwidth
    queue:
      priority_conn: true    # reserve one connection for small packets
    # congestion: bbr

  - type: tcp_tunnel_listen        # server
    tag: tcp_server
    listen_addr: 0.0.0.0:9001
    pacing_rate: 190         # Mbit/s, ~95% of the client's download bandwidth
    queue:
      priority_conn: true
```

| Parameter | Default | Description |
|---|---|---|
| `pacing_rate` | off | Mbit/s, send rate cap per connection (Linux) |
| `congestion` | system default | TCP congestion control, e.g. `bbr` (Linux). Falls back with a warning if the kernel lacks it |
| `queue.enabled` | `true` | `false` restores a plain first-in first-out queue |
| `queue.priority_size` | `1000` | Bytes; smaller packets are prioritized. `0` disables |
| `queue.priority_share` | `50` | Percent of the bytes guaranteed to priority traffic while bulk data waits |
| `queue.target_delay` | `5` | ms, CoDel target for bulk data |
| `queue.interval` | `100` | ms, CoDel interval |
| `queue.queue_limit` | 2 MB | Bytes per connection; the oldest bulk packets are dropped beyond it |
| `queue.notsent_lowat` | automatic | Bytes of unsent data the kernel may hold. Automatic follows the delivery rate on Linux (16–512 KB) and is 128 KB elsewhere; `0` leaves the kernel default |
| `queue.priority_conn` | `false` | With 2+ connections, the first carries only small packets, so they never wait behind bulk data or its retransmissions |

## Measured effect

WireGuard through the tunnel, 40/200 Mbit/s line with a 20 ms RTT and a 200 ms modem buffer; game-like stream (60 pps × 100 B, idle latency ~23 ms) measured while TCP transfers run:

| Setup | Upload busy | Download busy | Both busy | Bulk up / down |
|---|---|---|---|---|
| Before, 1 connection | 569 ms (p99 1257), 2.8% lost | 126 ms | 592 ms, download 16 Mbit/s | 35 / 176 |
| Now, 1 connection | 192 ms | 149 ms | 151 ms | 35 / 175 |
| Now, 1 connection, `pacing_rate` 38/190 | **39 ms** | **27 ms** | **39 ms** | 35 / 176 |

Without `pacing_rate` or BBR the modem buffer still fills, so most of the gain on a bloated line needs one of them.

With 0.5% random packet loss on the line:

| Setup | Upload busy | Download busy | Both busy | Bulk up / down |
|---|---|---|---|---|
| Before, 1 connection | 1114 ms (p99 2.5 s), 14% lost | 23 ms (p99 209) | 671 ms, 10% lost | transfer failed |
| Now, 1 connection | 80 ms | 98 ms | 101 ms | 10 / 10 |
| Before, 4 connections | 27 ms (p99 91) | 26 ms (p99 137) | 30 ms (p99 117) | 26 / 25 |
| Now, 4 connections, `priority_conn` | 25 ms (p99 56) | 24 ms (p99 65) | 23 ms (p99 50) | **31 / 44** |

A single TCP connection is limited to about 10 Mbit/s at this loss rate and RTT, and a lost packet holds back everything behind it. On lossy lines use several connections with `priority_conn: true`, or the UDP tunnel.

On a single CPU core without a bottleneck, peak throughput and latency are unchanged with one connection. With four connections, packets no longer alternate between connections, so WireGuard throughput rose from 1260 to 1927 Mbit/s (kernel WireGuard) and from 2247 to 3771 Mbit/s (wireguard-go).

## Statistics

`/api/tcp_tunnel_forward/<tag>` and `/api/tcp_tunnel_listen/<tag>` report a `queue` section (priority packets, CoDel and overflow drops, queue delay) and, per connection, the queued bytes and on Linux the kernel's view: `rtt_ms`, `min_rtt_ms`, `cwnd`, `retrans`, `notsent_bytes`, `pacing_rate_mbps`.

The load balancer variable `qdelay_<tag>` works for TCP tunnel components: it is the time the oldest packet has been waiting in a send queue.
