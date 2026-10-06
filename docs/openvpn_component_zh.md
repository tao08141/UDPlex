# UDPlex `openvpn` 组件

`openvpn` 组件把 OpenVPN 服务端（[sing-openvpn](https://github.com/SagerNet/sing-openvpn)，即 sing-box 使用的实现）嵌入 UDPlex 进程。它会创建自己的 TUN 网卡，直接接入普通 OpenVPN 客户端（OpenVPN 2.x、OpenVPN Connect 等），不需要运行 `openvpn` 进程。配合[内核网络字段](wg_component_zh.md#内核网络配置)，客户端的流量可以被送进另一条隧道，例如经 UDPlex 线路传输的内嵌 `wg` 组件。

## 配置

```yaml
- type: openvpn
  tag: ovpn_access
  listen_addr: 0.0.0.0:1194
  proto: udp
  interface_name: ovpn_access
  mtu: 1420
  addresses: [10.9.0.1/24]
  ca: /etc/udplex/pki/ca.crt
  cert: /etc/udplex/pki/server.crt
  key: /etc/udplex/pki/server.key
  tls_crypt: /etc/udplex/pki/tc.key
  crl_verify: /etc/udplex/pki/crl.pem
  push_dns: [1.1.1.1]
  redirect_gateway: true
  ip_forward: true
  mss_clamp: true
  policy_routes:
    - {from: [10.9.0.0/24], table: 7100, priority: 7101, dev: wg_gw}
  masquerade:
    - {source: 10.9.0.0/24, out_interface: wg_gw}
```

## 字段

| 字段 | 说明 |
|---|---|
| `bind_mode` | `native`（默认）：监听 `listen_addr`。`udplex`：与其他组件交换 OpenVPN 报文（见下文） |
| `listen_addr` | native 模式的监听地址 |
| `proto` | `udp`（默认）或 `tcp`。`tcp` 需要 native 模式 |
| `detour` | udplex 模式：不复用来路时的回包路径 |
| `reuse_incoming_detour` | udplex 模式：回包走客户端报文进来的组件，默认 `true` |
| `interface_name` | TUN 网卡名，默认使用 tag |
| `mtu` | 网卡 MTU，默认 `1500`。客户端流量要转进 WireGuard 隧道时建议 `1420` |
| `addresses` | 服务端地址及客户端地址池，例如 `10.9.0.1/24`，客户端分配其余地址。IPv4、IPv6 各最多一个 |
| `routes` | 经该网卡的额外路由 |
| `topology` | `subnet`（默认）、`net30` 或 `p2p` |
| `setup_interface` | 在 Linux 上自动配置 MTU、地址、链路状态和路由，默认 `true` |
| `max_clients` | 最大客户端数，0 表示不限 |
| `ca` | 签发客户端证书的 CA |
| `cert`、`key` | 服务端证书和私钥 |
| `tls_crypt`、`tls_crypt_v2`、`tls_auth` | 控制通道保护，最多选一个。`key_direction`（0/1）用于 `tls_auth` |
| `crl_verify` | 吊销客户端证书的 CRL 文件，每次握手都会重新读取 |
| `verify_client_certificate` | `require`（设置了 `ca` 时的默认值）、`optional` 或 `none` |
| `users` | `auth-user-pass` 账号：`[{username, password}]`。不设 `ca` 时客户端只用密码登录 |
| `duplicate_cn` | 允许多个客户端使用同一张证书 |
| `data_ciphers`、`data_ciphers_fallback`、`auth` | 数据通道加密协商 |
| `push_routes` | 推送给客户端的路由 |
| `push_dns` | 推送给客户端的 DNS |
| `redirect_gateway` | 推送 `redirect-gateway def1`（客户端全部流量走隧道） |
| `keepalive_interval`、`keepalive_timeout` | 等同 `keepalive 10 60`，即默认值 |

证书和密钥既可以填 PEM 内容，也可以填文件路径。内核网络字段 `ip_forward`、`mss_clamp`、`policy_routes`、`masquerade` 与 [`wg` 组件](wg_component_zh.md#内核网络配置)相同。

## 客户端配置

```
client
dev tun
proto udp
remote YOUR_SERVER 1194
nobind
remote-cert-tls server
tun-mtu 1420
<ca>...</ca>
<cert>...</cert>
<key>...</key>
<tls-crypt>...</tls-crypt>
```

## `udplex` 模式

`udplex` 模式下服务端从其他组件接收报文，因此 OpenVPN 客户端可以经 UDPlex `listen` 组件或多条线路连入。服务端按报文的连接 ID 区分客户端：请使用 `listen` 组件（每个客户端地址一个 ID）或开启了 `auth` 的线路。

```yaml
- type: listen
  tag: ovpn_in
  listen_addr: 0.0.0.0:1194
  timeout: 120
  broadcast_mode: false
  detour: [ovpn]

- type: openvpn
  tag: ovpn
  bind_mode: udplex
  addresses: [10.9.0.1/24]
  ca: ca.crt
  cert: server.crt
  key: server.key
```

## 说明

- 只支持 TLS 模式（不支持 static key 模式）。
- 网卡配置和内核网络字段仅支持 Linux。
- [接入网关脚本](udplex_gateway_zh.md)会自动生成证书、服务端配置和客户端配置文件。
