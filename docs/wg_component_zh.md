# UDPlex `wg` 组件

`wg` 组件会把 `wireguard-go` 直接嵌入 UDPlex 进程内部。启动后会在本机创建一个 WireGuard 网卡，并把 WireGuard 的握手与数据包交给 UDPlex 现有的 `forward` 或 `tcp_tunnel` 链路转发，不再依赖外部 `wg-quick` 或额外的用户态转发进程。

## 作用

- 避免“内核 WireGuard -> 用户态转发程序”之间的额外交互开销。
- 让 WireGuard 仍然走 UDPlex 现有的转发和隧道能力。
- 自动记住 WireGuard 包从哪个 UDPlex 组件进来，并优先沿原路径回包。
- 在 Linux 上自动创建并配置 WireGuard 网卡。

## 基本配置

```yaml
- type: wg
  tag: wg_client
  interface_name: wg0
  mtu: 1420
  addresses:
    - 10.6.0.2/24
  private_key: YOUR_PRIVATE_KEY_HEX
  listen_port: 51820
  detour: [line_a, line_b]
  peers:
    - public_key: REMOTE_PUBLIC_KEY_HEX
      endpoint: udplex-server:51820
      allowed_ips:
        - 10.6.0.1/32
      persistent_keepalive: 25
```

## 主要字段

| 字段 | 说明 |
|---|---|
| `interface_name` | 创建的网卡名，例如 `wg0` |
| `addresses` | Linux 上自动配置到网卡的地址，例如 `10.6.0.2/24` |
| `private_key` | WireGuard 私钥，十六进制 |
| `listen_port` | WireGuard 逻辑监听端口 |
| `detour` | 初始出站 WireGuard 报文要走的 UDPlex 路径 |
| `routes` | 额外写入到 Linux 路由表的路由 |
| `route_allowed_ips` | 为 `true` 时，把每个 peer 的 `allowed_ips` 也写成系统路由 |
| `setup_interface` | 为 `true` 时，Linux 上自动设置 MTU、地址、链路状态和路由 |
| `reuse_incoming_detour` | 为 `true` 时，回包优先沿收到该包的源组件返回 |

## 使用建议

- `forward` 模式下，建议打开 UDPlex `auth`，这样 `ConnID` 能跨线路保留下来，多客户端时回程更稳定。
- `tcp_tunnel` 模式下，建议把 `broadcast_mode` 设为 `false`，让回包按 `ConnID` 精准写回对应隧道连接。
- 服务端 peer 可以不写 `endpoint`，由第一次入站握手自动学习。

## 示例

- `examples/wg_component_forward_client.yaml`
- `examples/wg_component_forward_server.yaml`
- `examples/wg_component_tcp_tunnel_client.yaml`
- `examples/wg_component_tcp_tunnel_server.yaml`
