# UDPlex TCP Listen 组件

## 功能概述
TCP Listen 组件监听 TCP 端口，把收到的 TCP 连接转发出去，用于实现 `客户端 → A机 → B机 → 目标` 的 TCP 转发。它有两种模式：

- **流模式**（配置 `detour` 和 `target`）：每条 TCP 连接作为一条“流”切成帧，经过 UDPlex 管线（`tcp_tunnel_forward`、`load_balancer`、多条线路冗余等）送到对端的 [TCP Forward](tcp_forward_zh.md) 组件，由它拨号 `target`。帧带有序号，接收端去重、重排并确认，发送端重传未确认的帧，因此一条流可以分散在多条线路上，某条线路中断时数据会改从其他线路重传。
- **直连模式**（配置 `forwarders`）：接受连接后直接拨号目标并双向转发。配合 `wg` 组件使用：A 拨号 B 的 WireGuard 内网地址，WireGuard 隧道内由内核 TCP 负责可靠传输。

> 流模式只支持经 TCP 隧道（`tcp_tunnel_forward` / `tcp_tunnel_listen`）承载。经 UDP 线路（`forward` / `listen`）传 TCP 存在 TCP over UDP 的问题，这种场景请使用 wg + 直连模式。

## 组件参数

| 参数 | 说明 |
|------|------|
| `type` | `tcp_listen` |
| `tag` | 组件唯一标识 |
| `listen_addr` | 监听地址，如 `0.0.0.0:8080`。可以是 `wg` 网卡的地址（在所有组件启动后才开始监听） |
| `target` | 流模式：由对端 `tcp_forward` 拨号的目标地址，如 `10.0.0.5:80` |
| `detour` | 流模式：流帧的发送路径，如 `[line_a, line_b]` 或 `[load_balancer]` |
| `forwarders` | 直连模式：目标地址列表，按顺序尝试，前一个连不上时使用下一个。支持 `地址@网卡` 指定出口网卡 |
| `interface_name` | 直连模式：默认出口网卡 |
| `window_size` | 流模式：每条流的接收窗口（字节），默认 1048576。单条流吞吐上限约为 窗口 / RTT |
| `timeout` | 流模式：有未确认数据却在这么多秒内没有进展时复位连接，默认 60 |
| `no_delay` | 是否设置 TCP_NODELAY，默认 `true` |

`detour` 和 `forwarders` 只能二选一。

## 配置示例

流模式，两条 TCP 隧道线路轮流发送，一条中断时全部走另一条：

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
  detour: [tcp_in]          # 回程帧交给 tcp_listen
```

要冗余发送（每帧在两条线路上各发一份），把 `detour` 设为 `[line_a, line_b]`。完整示例见 `examples/tcp_forward_client.yaml` / `tcp_forward_server.yaml`。

直连模式（经 WireGuard）：

```yaml
- type: tcp_listen
  tag: tcp_in
  listen_addr: 0.0.0.0:8080
  forwarders: [10.66.0.1:8080]   # B 的 wg 地址
```

完整示例见 `examples/tcp_over_wg_client.yaml` / `tcp_over_wg_server.yaml`。

## 工作原理（流模式）

1. 接受连接后生成随机流 ID（同时作为数据包的 ConnID，隧道据此把同一条流固定在同一个连接池上，并把回程送回原线路），先发送带 `target` 的 OPEN 帧。
2. 从客户端读到的数据按 MSS（`buffer_size - 100`，默认 1400 字节）切成 DATA 帧，在对端窗口允许的范围内发送。
3. 未确认的帧超时重传（RTO 按 RTT 估算，200 ms–10 s），收到乱序确认时提前重传首个丢失的帧。重传可能走另一条线路。
4. 流帧在 TCP 隧道的发送队列中不会被 CoDel 或队列上限丢弃，积压量由窗口限制。
5. 客户端关闭写方向时发送 FIN，对端对目标执行半关闭；两个方向都结束后关闭连接。出错时发送 RST，并复位客户端连接。

## 注意

- 隧道中同时承载 UDP 业务时，对端的 `tcp_forward` 会忽略非流帧；也可以用 filter 加协议检测器按帧头 magic `UXTS`（`0x55585453`）分流。
- 回程依赖隧道按 ConnID 选路，`tcp_tunnel_forward` / `tcp_tunnel_listen` 建议设置 `broadcast_mode: false`。
