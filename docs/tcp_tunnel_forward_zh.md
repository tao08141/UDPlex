# UDPlex TCP Tunnel Forward 组件

## 功能概述
TCP Tunnel Forward 组件负责建立到 TCP 隧道服务器的连接，将 UDP 数据包封装在 TCP 连接中传输，并接收从 TCP 隧道返回的 UDP 数据包。它是 TCP 隧道的客户端部分，允许 UDP 数据包通过 TCP 连接传输，适用于 UDP 数据包无法直接传输的网络环境。

## 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `tcp_tunnel_forward`，表示TCP隧道转发端 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `forwarders` | 目标服务器地址列表，格式为"IP:端口[:连接数]"，如"1.2.3.4:9001:4"，使用多连接时会造成UDP乱序，部分场景下可能会导致一些未知的问题 |
| `connection_check_time` | 连接检查间隔（秒），定期检查并重连断开的连接 |
| `no_delay` | 是否启用TCP Nagle算法，true表示禁用Nagle算法以减少延迟 |
| `detour` | 转发路径，指定接收返回数据的组件标识列表 |
| `auth` | 鉴权配置，详见鉴权部分 |

## 配置示例

```yaml
type: tcp_tunnel_forward
tag: tcp_tunnel_client
forwarders:
  - 203.0.113.1:9001:2
  - 203.0.113.2:9001:2
connection_check_time: 30
no_delay: true
detour:
  - listen_component
auth:
  enabled: true
  secret: your-strong-password
  enable_encryption: true
  heartbeat_interval: 30
```

## 工作原理

1. TCP Tunnel Forward 组件启动时会尝试连接到所有配置的 TCP 隧道服务器
2. 对于每个服务器地址，组件会建立指定数量的 TCP 连接（由地址后的数字指定）
3. 当接收到 UDP 数据包时，组件会：
   - 将 UDP 数据包封装在 TCP 连接中
   - 如果启用了鉴权，会进行鉴权处理
   - 通过 TCP 连接发送封装后的数据包
4. 当从 TCP 隧道接收到数据包时，组件会将数据包解封装，然后转发到 `detour` 中指定的组件
5. 组件会定期（由 `connection_check_time` 指定）检查连接状态，并尝试重连断开的连接
6. 当 `no_delay` 设置为 true 时，会禁用 TCP 的 Nagle 算法，减少数据传输的延迟

## 使用场景

- 在 UDP 被防火墙阻止的网络环境中传输 UDP 数据包
- 通过 TCP 隧道实现 UDP 数据包的可靠传输
- 在只允许 TCP 连接的网络中使用基于 UDP 的应用程序
- 作为 VPN 或游戏加速器的出口点，通过 TCP 隧道传输 UDP 游戏数据