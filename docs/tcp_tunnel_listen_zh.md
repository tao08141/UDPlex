# UDPlex TCP Tunnel Listen 组件

## 功能概述
TCP Tunnel Listen 组件负责监听 TCP 连接，接收通过 TCP 隧道传输的 UDP 数据包，并将其转发到配置的目标组件。它允许 UDP 数据包通过 TCP 连接传输，适用于 UDP 数据包无法直接传输的网络环境。

## 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `tcp_tunnel_listen`，表示TCP隧道监听端 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `listen_addr` | 监听地址和端口，格式为"IP:端口"，如"0.0.0.0:9001" |
| `timeout` | 连接超时时间（秒），超过此时间无数据传输则断开连接 |
| `no_delay` | 是否启用TCP Nagle算法，true表示禁用Nagle算法以减少延迟 |
| `detour` | 转发路径，指定接收数据的组件标识列表 |
| `auth` | 鉴权配置，详见鉴权部分 |

## 配置示例

```json
{
    "type": "tcp_tunnel_listen",
    "tag": "tcp_tunnel_server",
    "listen_addr": "0.0.0.0:9001",
    "timeout": 300,
    "no_delay": true,
    "detour": ["forward_component"],
    "auth": {
        "enabled": true,
        "secret": "your-strong-password",
        "enable_encryption": true,
        "heartbeat_interval": 30
    }
}
```

## 工作原理

1. TCP Tunnel Listen 组件启动时会绑定到指定的 IP 地址和端口，监听 TCP 连接
2. 当接收到 TCP 连接请求时，组件会：
   - 建立 TCP 连接
   - 如果启用了鉴权，会进行鉴权验证
   - 开始接收通过 TCP 连接传输的 UDP 数据包
3. 接收到的 UDP 数据包会被解封装，然后转发到 `detour` 中指定的组件
4. 如果在 `timeout` 指定的时间内没有收到数据，TCP 连接会被断开
5. 当 `no_delay` 设置为 true 时，会禁用 TCP 的 Nagle 算法，减少数据传输的延迟

## 使用场景

- 在 UDP 被防火墙阻止的网络环境中传输 UDP 数据包
- 通过 TCP 隧道实现 UDP 数据包的可靠传输
- 在只允许 TCP 连接的网络中使用基于 UDP 的应用程序
- 作为 VPN 或游戏加速器的接入点，通过 TCP 隧道传输 UDP 游戏数据