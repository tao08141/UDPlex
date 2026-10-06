# TCP 隧道：满载时的延迟

## 问题

TCP 隧道不会丢包，线路跑满时数据只会在队列里堆积。改进之前有三层队列，而且都是先进先出：

1. 每条隧道连接的发送队列（最多 `queue_size` 个包，约 14 MB）。
2. 内核的 TCP 发送缓冲（自动调整，可达数 MB）。
3. 光猫或运营商设备的缓冲，被隧道自身的 TCP 拥塞控制填满。

上传进行时发出的游戏包要排在所有这些数据后面。实测（WireGuard 经一条隧道连接，线路 40/200 Mbit/s，光猫缓冲 200 ms）：游戏延迟 **569 ms（p99 1.3 s），丢包 3%**。如果线路有 0.5% 的丢包，延迟超过 1 秒，传输还会中断。

## 现在的处理

- **压缩内核队列**：用 `TCP_NOTSENT_LOWAT` 把内核里未发送的数据限制在约 5 ms 的量，按连接的实测速率计算（Linux；macOS 上固定为 128 KB；Windows 不支持）。数据改为在 UDPlex 里排队，这样就可以区分优先级。
- **小包优先**：载荷不超过 `priority_size`（默认 1000 字节，包括游戏、语音、ACK）的包可以越过大流量先发，最多占 `priority_share` 的带宽。
- **大流量 CoDel**：大流量包排队超过 5 ms 并持续 100 ms 时，在进入隧道前丢弃一部分，让隧道内的 TCP 降速，而不是把队列塞满。
- **连接调度**：使用多条连接（`IP:端口:连接数`）时，数据保持在同一条连接上，等它出现积压才切换到最空闲的连接，而不是逐包轮换。

以上功能默认开启，无需配置。另外有两个选项用来处理第三层队列（光猫缓冲）：

- `pacing_rate`（Linux）：把每条连接的发送速率限制在线路带宽以下，作用和 UDP 的 [shaper](shaper_zh.md) 相同。
- `congestion: bbr`（Linux，需要内核有 `tcp_bbr` 模块）：BBR 本身就会让瓶颈处的队列保持很短。

## 配置

```yaml
  - type: tcp_tunnel_forward
    tag: tcp_line
    forwarders: [SERVER_IP:9001:4]
    pacing_rate: 38          # Mbit/s，上行带宽的 95% 左右
    queue:
      priority_conn: true    # 留一条连接专门传小包
    # congestion: bbr

  - type: tcp_tunnel_listen        # 服务端
    tag: tcp_server
    listen_addr: 0.0.0.0:9001
    pacing_rate: 190         # Mbit/s，客户端下行带宽的 95% 左右
    queue:
      priority_conn: true
```

| 参数 | 默认值 | 说明 |
|---|---|---|
| `pacing_rate` | 关闭 | Mbit/s，每条连接的发送速率上限（Linux） |
| `congestion` | 系统默认 | TCP 拥塞控制算法，如 `bbr`（Linux）。内核不支持时打印警告并使用系统默认 |
| `queue.enabled` | `true` | 设为 `false` 恢复为普通的先进先出队列 |
| `queue.priority_size` | `1000` | 字节，不超过该大小的包优先发送；`0` 关闭 |
| `queue.priority_share` | `50` | 有大流量排队时，保证给优先流量的字节百分比 |
| `queue.target_delay` | `5` | 毫秒，大流量队列的 CoDel 目标 |
| `queue.interval` | `100` | 毫秒，CoDel 判定窗口 |
| `queue.queue_limit` | 2 MB | 每条连接的字节上限，超出时丢弃最早的大流量包 |
| `queue.notsent_lowat` | 自动 | 内核可保留的未发送字节数。自动模式在 Linux 上按实测速率调整（16–512 KB），其他平台为 128 KB；`0` 表示使用内核默认 |
| `queue.priority_conn` | `false` | 有 2 条以上连接时，第一条只传小包，小包不会排在大流量或其重传之后 |

## 实测效果

WireGuard 经 TCP 隧道，线路 40/200 Mbit/s，RTT 20 ms，光猫缓冲 200 ms；在 TCP 传输进行时测量类游戏流（60 pps × 100 B，空闲延迟约 23 ms）：

| 配置 | 上传跑满 | 下载跑满 | 双向跑满 | 大流量 上/下 |
|---|---|---|---|---|
| 改进前，1 条连接 | 569 ms（p99 1257），丢包 2.8% | 126 ms | 592 ms，下载降到 16 Mbit/s | 35 / 176 |
| 改进后，1 条连接 | 192 ms | 149 ms | 151 ms | 35 / 175 |
| 改进后，1 条连接，`pacing_rate` 38/190 | **39 ms** | **27 ms** | **39 ms** | 35 / 176 |

不设 `pacing_rate` 或 BBR 时，光猫缓冲仍会被填满，所以在缓冲很深的线路上，大部分收益需要配合其中之一。

线路有 0.5% 随机丢包时：

| 配置 | 上传跑满 | 下载跑满 | 双向跑满 | 大流量 上/下 |
|---|---|---|---|---|
| 改进前，1 条连接 | 1114 ms（p99 2.5 s），丢包 14% | 23 ms（p99 209） | 671 ms，丢包 10% | 传输失败 |
| 改进后，1 条连接 | 80 ms | 98 ms | 101 ms | 10 / 10 |
| 改进前，4 条连接 | 27 ms（p99 91） | 26 ms（p99 137） | 30 ms（p99 117） | 26 / 25 |
| 改进后，4 条连接，`priority_conn` | 25 ms（p99 56） | 24 ms（p99 65） | 23 ms（p99 50） | **31 / 44** |

在这个丢包率和 RTT 下，单条 TCP 连接只能跑到约 10 Mbit/s，而且一个包丢失会阻塞后面的所有数据。有丢包的线路请使用多条连接并开启 `priority_conn: true`，或者改用 UDP 隧道。

单核、无瓶颈的环境下，1 条连接的极限吞吐和延迟没有变化；4 条连接时，由于不再逐包在连接间轮换，WireGuard 吞吐从 1260 提升到 1927 Mbit/s（内核 WireGuard），从 2247 提升到 3771 Mbit/s（wireguard-go）。

## 统计信息

`/api/tcp_tunnel_forward/<tag>` 和 `/api/tcp_tunnel_listen/<tag>` 返回 `queue` 字段（优先包数、CoDel 与溢出丢包、队列时延）；每条连接还会返回排队字节数，在 Linux 上另有内核数据：`rtt_ms`、`min_rtt_ms`、`cwnd`、`retrans`、`notsent_bytes`、`pacing_rate_mbps`。

负载均衡变量 `qdelay_<tag>` 也适用于 TCP 隧道组件，值为发送队列中最早的包已经等待的时间。
