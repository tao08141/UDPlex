# UDPlex Listen 组件

## 功能概述
Listen 组件负责监听指定的 UDP 端口，接收来自客户端的数据包，并将其转发到配置的目标组件。它是 UDPlex 系统中的入口点，处理所有入站连接和数据包。

## 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `listen`，表示监听组件 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `listen_addr` | 监听地址和端口，格式为"IP:端口"，如"0.0.0.0:9000" |
| `timeout` | 连接超时时间（秒），超过此时间无数据传输则清除映射 |
| `replace_old_mapping` | 是否替换旧映射，当为true时新映射会替换同地址的旧映射 |
| `detour` | 转发路径，指定接收数据的组件标识列表 |
| `auth` | 鉴权配置，详见鉴权部分 |

## 配置示例

```yaml
type: listen
tag: client_listen
listen_addr: 0.0.0.0:9000
timeout: 120
replace_old_mapping: true
detour:
  - forward_component
auth:
  enabled: true
  secret: your-strong-password
  enable_encryption: true
  heartbeat_interval: 30
```

## 工作原理

1. Listen 组件启动时会绑定到指定的 IP 地址和端口
2. 当接收到 UDP 数据包时，组件会：
   - 记录客户端的源地址和端口
   - 如果启用了鉴权，会进行鉴权验证
   - 将数据包转发到 `detour` 中指定的组件
3. 组件会维护一个客户端地址到内部连接 ID 的映射表
4. 如果在 `timeout` 指定的时间内没有收到某个客户端的数据，相应的映射会被清除
5. 当 `replace_old_mapping` 设置为 true 时，如果收到来自相同地址但不同端口的数据包，新的映射会替换旧的映射

## 使用场景

- 作为 UDP 转发服务的入口点
- 接收游戏客户端的连接请求
- 作为 VPN 或加速器的接入点
- 任何需要接收和处理 UDP 数据包的场景