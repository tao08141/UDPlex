# Shaper (Rate Limiting with Small-Packet Priority)

## Why

When a download, upload or game update saturates your line, packets queue up in the modem, router or ISP equipment ("bufferbloat"). Those buffers are often hundreds of milliseconds deep, so every game packet waits behind the bulk traffic.

The shaper fixes this the same way SQM (cake / fq_codel) does on routers. UDPlex sends slightly below the bottleneck bandwidth, so the queue forms inside UDPlex instead of in the modem. Inside UDPlex:

- **Small packets go first**: game traffic, voice, TCP ACKs and handshakes skip the bulk queue.
- **The bulk queue is kept short with CoDel**, so the TCP flows inside the tunnel slow down instead of filling buffers.

Because both ends of the tunnel run UDPlex, the download direction can be shaped where it is sent (on the server), which works much better than shaping incoming traffic on a home router.

## Measured effect

Test setup: WireGuard over UDPlex on a 40 Mbit/s up / 200 Mbit/s down line with 20 ms RTT and a 200 ms modem buffer. The probe is a 60 pps × 100 B game-like stream through the tunnel, measured while a bulk TCP transfer runs.

| | Upload busy | Download busy | Both busy | Bulk throughput up / down |
|---|---|---|---|---|
| No shaper | ~195 ms | ~187 ms | ~205 ms | 36 / 180 Mbit/s |
| Shaper at 95% of the line | **~21 ms** | **~21 ms** | **~21 ms** | 35 / 173 Mbit/s |

The idle latency is ~21–24 ms, so with the shaper the game sees almost no extra delay under full load.

## Configuration

Add `shaper` to the component that sends over the bottleneck:

- **Client**, on each `forward` line: set `rate` to the upload bandwidth of that line.
- **Server**, on each `listen`: set `rate` to the client's download bandwidth. Each client address is shaped separately.

```yaml
# client
  - type: forward
    tag: line_a
    forwarders: [SERVER_IP:5900]
    shaper:
      enabled: true
      rate: 38          # Mbit/s, 90-95% of the measured upload bandwidth

# server
  - type: listen
    tag: line_a
    listen_addr: 0.0.0.0:5900
    shaper:
      enabled: true
      rate: 190         # Mbit/s, 90-95% of the client's download bandwidth
```

| Parameter | Default | Description |
|---|---|---|
| `enabled` | `false` | Enable the shaper |
| `rate` | (required) | Send rate in Mbit/s, counting IP/UDP headers. Use 90–95% of the real bandwidth: if it is above the bottleneck, the queue moves back into the modem and there is no benefit |
| `overhead` | `0` | Extra bytes per packet below IP, e.g. 8 for PPPoE |
| `priority_size` | `1000` | UDP payloads up to this size (bytes) are prioritized. Bulk traffic in a tunnel uses full-size packets (~1400+ bytes), so 1000 separates them reliably. `0` disables prioritization |
| `priority_share` | `50` | Percent of `rate` guaranteed to priority traffic while bulk traffic is waiting. Beyond it, bulk traffic gets the rest, so a flood of small packets cannot starve it |
| `target_delay` | `5` | ms, CoDel target for the bulk queue |
| `interval` | `100` | ms, CoDel interval. Roughly the typical RTT of the traffic |
| `queue_limit` | 100 ms at `rate` (256 KB – 4 MB) | Maximum queued bytes per path. When full, the oldest bulk packets are dropped |

## Autorate: lines whose bandwidth changes

A fixed `rate` only helps while the line really delivers it. On LTE/5G, Wi-Fi bridges, or lines that are congested in the evening, the bandwidth drops below `rate` and the queue moves back into the modem. With `autorate: true` the shaper follows the line:

- Every `probe_interval` it sends a small probe to the peer. The peer measures the one-way delay and reports it back, so each direction is measured separately: the client tunes the upload and the server tunes the download of that client. The clocks of the two hosts do not need to be synchronized.
- When the one-way delay rises more than `bloat_threshold` above its baseline, the queue is growing in the line, and the rate drops to 90% of what is actually sent.
- While the line is busy and the delay stays low, the rate creeps back up to `max_rate`. When the line is idle, it returns to `rate`.

```yaml
    shaper:
      enabled: true
      rate: 38          # Mbit/s, normal bandwidth (starting point)
      autorate: true
      min_rate: 8       # never go below this
      max_rate: 38      # never go above this; set higher on lines that are sometimes faster
```

| Parameter | Default | Description |
|---|---|---|
| `autorate` | `false` | Adjust the rate automatically |
| `min_rate` | `rate`/5 | Mbit/s, lower bound |
| `max_rate` | `rate` | Mbit/s, upper bound |
| `bloat_threshold` | `15` | ms of extra one-way delay that counts as a growing queue. Lower keeps latency tighter but costs more bandwidth |
| `probe_interval` | `50` | ms between probes while the path is busy (one probe per second when idle) |

Measured with an upload or download running when the line bandwidth halves mid-transfer (40 → 20 Mbit/s up, 200 → 100 Mbit/s down; idle latency ~22 ms):

| | Game latency after the drop (p50 / p99) | Bulk throughput after the drop |
|---|---|---|
| Fixed rate, upload | 181 / 233 ms | 18.6 Mbit/s |
| Autorate, upload | **20.6 / 37 ms** | 16.5 Mbit/s |
| Fixed rate, download | 75 / 196 ms | 90.5 Mbit/s |
| Autorate, download | **20.7 / 36 ms** | 84.7 Mbit/s |

When the bandwidth comes back, the rate climbs back within several seconds.

Autorate requires `auth` on both ends, and both ends must run a version that supports it. An older peer does not answer the probes, so the shaper stays at `rate`.

## Notes

- Authentication and heartbeat messages bypass the rate limit and are never delayed.
- Shaping is per path. In a load-balancer or redundant setup, set the shaper on every line with that line's own bandwidth. The load balancer can then pick the less busy line with `qdelay_<tag>` (see [Load Balancer](load_balancer_en.md#switching-lines-by-queue-delay)).
- The shaper only covers UDP `listen`/`forward`. `tcp_tunnel_*` components have their own queue management and a `pacing_rate` option, see [TCP Tunnel Tuning](tcp_tunnel_tuning_en.md).
- CPU cost is small: on a single core, peak TCP-over-WireGuard throughput with the shaper enabled (but not limiting) was 1–10% lower, and latency was unchanged. When disabled (the default) there is no cost.
- Statistics (queue delay, drops, priority packets, and the current rate and one-way delay of each path with autorate) are shown under `shaper` in the `/api/listen/<tag>` and `/api/forward/<tag>` responses.
