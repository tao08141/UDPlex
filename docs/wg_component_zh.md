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
| `bind_mode` | `udplex`（默认）：WireGuard 报文与其他 UDPlex 组件交换。`native`：直接监听 `listen_port`，普通 WireGuard 客户端可以直接连入 |

内核网络字段 `ip_forward`、`mss_clamp`、`policy_routes`、`masquerade` 见 [内核网络配置](#内核网络配置)。

## 接入外部客户端（`bind_mode: native`）

设置 `bind_mode: native` 后，组件像普通 WireGuard 服务端一样监听 `listen_port`，手机、电脑上的官方 WireGuard 客户端可以直接连入。配合下面的内核网络字段，可以把它们的流量送进另一条隧道，例如经 UDPlex 线路传输的内嵌 `wg` 组件：

```yaml
- type: wg
  tag: wg_access
  bind_mode: native
  interface_name: wg_access
  listen_port: 51821
  addresses: [10.8.0.1/24]
  private_key: ACCESS_PRIVATE_KEY
  ip_forward: true
  mss_clamp: true
  policy_routes:
    - {from: [10.8.0.0/24], table: 7100, priority: 7100, dev: wg_gw}
  masquerade:
    - {source: 10.8.0.0/24, out_interface: wg_gw}
  peers:
    - public_key: CLIENT_PUBLIC_KEY
      allowed_ips: [10.8.0.2/32]
```

native 模式不使用 `detour`，其他组件路由过来的报文会被丢弃。

内层隧道替外部客户端访问互联网时，入口端的 peer 需要 `allowed_ips: [0.0.0.0/0]`，否则 WireGuard 会丢弃来自互联网地址的回包。保持 `route_allowed_ips: false`，这样不会改动主机路由表。

## 内核网络配置

这些字段由 `wg` 和 `openvpn` 组件共用。它们在所有组件启动之后才生效，因此 `dev` 和 `out_interface` 可以引用其他组件的网卡。UDPlex 停止时（SIGINT/SIGTERM）会全部撤销。仅支持 Linux。

| 字段 | 说明 |
|---|---|
| `ip_forward` | 开启内核转发，并在 `FORWARD` 链放行本网卡的转发流量（Docker 会把该链默认策略设为 `DROP`） |
| `mss_clamp` | 把本网卡转发的 TCP 连接的 MSS 钳制到路径 MTU |
| `policy_routes` | 基于源地址的路由，见下表 |
| `masquerade` | 源地址 NAT，见下表 |

`policy_routes` 每一项：

| 字段 | 说明 |
|---|---|
| `from` | 走 `table` 查路由的源网段（`ip rule add from ... lookup ...`） |
| `table` | 路由表编号 |
| `priority` | `ip rule` 优先级，可选 |
| `dev` | 路由表指向的网卡，默认是本组件的网卡 |
| `routes` | 发往 `dev` 的目标网段，默认是 `from` 对应地址族的默认路由 |

同一个 `from` 网段内部的流量（例如同一地址池里的两个客户端）会回落到主路由表。

`masquerade` 每一项：

| 字段 | 说明 |
|---|---|
| `source` | 要做 NAT 的源网段 |
| `out_interface` | 出口网卡。`auto` 表示默认路由所在网卡，留空表示对 `source` 以外的所有目标做 NAT |

说明：

- Docker 中 `/proc/sys` 是只读的：请在宿主机开启转发（`sysctl -w net.ipv4.ip_forward=1`），UDPlex 只检查它是否已开启。
- iptables 规则会写入宿主机现有规则所在的后端（legacy 或 nft），从而与 Docker 的规则共存。可以用 `UDPLEX_IPTABLES_BACKEND=legacy` 或 `nft` 显式指定。

## 使用建议

- `forward` 模式下，建议打开 UDPlex `auth`，这样 `ConnID` 能跨线路保留下来，多客户端时回程更稳定。
- `tcp_tunnel` 模式下，建议把 `broadcast_mode` 设为 `false`，让回包按 `ConnID` 精准写回对应隧道连接。
- 服务端 peer 可以不写 `endpoint`，由第一次入站握手自动学习。

## 示例

- `examples/wg_component_forward_client.yaml`
- `examples/wg_component_forward_server.yaml`
- `examples/wg_component_tcp_tunnel_client.yaml`
- `examples/wg_component_tcp_tunnel_server.yaml`
