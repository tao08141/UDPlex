# UDPlex TCP Forward 组件

## 功能概述
TCP Forward 组件是 [TCP Listen](tcp_listen_zh.md) 流模式的对端（B 机）。它从管线（通常是 `tcp_tunnel_listen`）接收流帧，为每条流拨号目标，把目标返回的数据作为流帧经 `detour` 送回。帧的去重、重排、确认、重传和流控与 `tcp_listen` 相同。

## 组件参数

| 参数 | 说明 |
|------|------|
| `type` | `tcp_forward` |
| `tag` | 组件唯一标识 |
| `detour` | 回程帧的发送路径，通常是接收流帧的 `tcp_tunnel_listen`（多条线路时可以列出多个，或使用 `load_balancer`） |
| `target` | 可选，固定的目标地址。设置后忽略 `tcp_listen` 发来的 `target` |
| `interface_name` | 可选，拨号目标时使用的出口网卡 |
| `window_size` | 每条流的接收窗口（字节），默认 1048576 |
| `timeout` | 有未确认数据却在这么多秒内没有进展时复位，默认 60 |
| `no_delay` | 是否设置 TCP_NODELAY，默认 `true` |

## 配置示例

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

## 工作原理

1. 收到新流的帧时创建流；收到 OPEN 帧后拨号目标（超时 10 秒），连接失败时向 A 发送 RST。
2. OPEN 可能走了较慢的线路而晚于数据到达，期间数据先缓存；10 秒内仍未收到 OPEN 则复位该流。
3. 两个方向都结束后，流再保留 30 秒，用于回应迟到的重传帧。

## 安全提示

未设置 `target` 时，B 会拨号 A 请求的任意地址。请务必在隧道上开启 `auth`（建议同时开启加密），或者设置 `target` 限定目标。
