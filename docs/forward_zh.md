# UDPlex Forward 组件

## 功能概述
Forward 组件负责将数据包转发到一个或多个目标服务器，并处理返回的响应数据。它支持并行转发、自动重连和连接保活等功能，是 UDPlex 系统中的核心转发组件。

## 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `forward`，表示转发组件 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `forwarders` | 转发目标地址列表，可配置多个目标进行并行转发 |
| `reconnect_interval` | 重连间隔时间（秒），断开连接后尝试重连的等待时间 |
| `connection_check_time` | 连接检查间隔（秒），定期检查连接状态的时间间隔 |
| `send_keepalive` | 是否发送空数据包作为心跳包来保持连接活跃 |
| `detour` | 转发路径，指定接收返回数据的组件标识列表 |
| `auth` | 鉴权配置，详见鉴权部分 |

## 配置示例

```json
{
    "type": "forward",
    "tag": "game_server_forward",
    "forwarders": ["192.168.1.100:9001", "192.168.1.101:9001"],
    "reconnect_interval": 5,
    "connection_check_time": 30,
    "send_keepalive": true,
    "detour": ["client_listen"],
    "auth": {
        "enabled": true,
        "secret": "your-strong-password",
        "enable_encryption": true,
        "heartbeat_interval": 30
    }
}
```

## 工作原理

1. Forward 组件启动时会尝试连接到所有配置的目标服务器
2. 当接收到数据包时，组件会：
   - 将数据包并行转发到所有已连接的目标服务器
   - 如果启用了鉴权，会进行鉴权处理
3. 当从目标服务器接收到响应数据时，组件会将数据转发到 `detour` 中指定的组件
4. 组件会定期（由 `connection_check_time` 指定）检查连接状态
5. 如果连接断开，组件会在 `reconnect_interval` 指定的时间后尝试重新连接
6. 当 `send_keepalive` 设置为 true 时，组件会定期发送空数据包作为心跳包，保持连接活跃

## 使用场景

- 将客户端数据转发到游戏服务器
- 实现网络冗余，同时向多个服务器发送相同数据
- 作为 VPN 或加速器的出口点
- 任何需要将 UDP 数据包转发到一个或多个目标服务器的场景