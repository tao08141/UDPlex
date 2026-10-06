# UDPlex 接入网关

`udplex-gateway-manager.sh` 把两台服务器组成接入网关。手机和电脑用官方 WireGuard 或 OpenVPN 客户端连接**入口**。入口只把客户端的加密报文原样经两条 UDPlex 线路中继到**出口**。出口运行 WireGuard 和 OpenVPN 服务端，再把流量转发到互联网。

它与 [`udplex-wg-manager.sh`](udplex_wireguard_zh.md) 完全独立：使用自己的目录（`/opt/udplex-gw`）、容器（`udplex-gw`）和端口，两者可以在同一台服务器上共存。

## 拓扑

```text
WireGuard / OpenVPN 客户端
  -> 入口: listen 51821/udp（WireGuard）、1194/udp（OpenVPN）
  -> UDPlex 线路 #1 + 线路 #2（各自可选 UDP 或 TCP），报文全程保持加密
  -> 出口: wg_access / ovpn_access（内嵌服务端）
  -> NAT 为出口公网地址
  -> 互联网
```

- 入口不解密任何报文，不需要 TUN 网卡、NAT 或路由。客户端流量只由其自身协议加密一次。
- 入口为每个客户端地址分配独立的连接 ID，线路把它带到出口。出口据此区分客户端，同一客户端在两条线路上也是同一个连接。
- 低于带宽阈值时每个报文同时走两条线路，WireGuard 和 OpenVPN 用各自的重放保护丢弃第二份。高于阈值时按安装时的选择在两条线路间分流，或固定走一条。
- 客户端只能用 UDP；OpenVPN over TCP 无法以这种方式中继。

## 环境要求

- 两台 Linux 服务器，有 root 权限，安装 Docker（脚本会在缺失时自动安装）
- 出口：`/dev/net/tun`、`wireguard-tools` 和 `openssl`（脚本自动安装）
- 放行端口：入口放行 WireGuard（51821/udp）和 OpenVPN（1194/udp）端口；出口放行两个线路端口（默认 9100、9101）

## 安装

先装入口，出口需要填写入口显示的信息。

```bash
curl -fsSL -o udplex-gateway-manager.sh https://raw.githubusercontent.com/tao08141/UDPlex/master/udplex-gateway-manager.sh
sudo bash udplex-gateway-manager.sh install
```

入口：

1. 选择角色 `1`。
2. 共享密钥留空即自动生成。
3. 设置带宽阈值、每条线路的协议和高流量模式。
4. 输入出口的两个地址（`host:port`，例如 `exit.example.com:9100` 和 `:9101`）。
5. 选择客户端协议及其端口。
6. 记下最后显示的密钥和端口。

出口：

1. 选择角色 `2`，输入入口的密钥。
2. 选择相同的线路协议，输入两个线路端口。
3. 启用相同的客户端协议并填写入口的端口，选择客户端地址池（默认 `10.8.0.0/24` 和 `10.9.0.0/24`），输入入口的公网地址。客户端配置文件会指向这个地址。脚本会生成 OpenVPN CA、服务端证书和 `tls-crypt` 密钥。
4. 选择推送给客户端的 DNS，以及客户端经网关转发的路由（`0.0.0.0/0` 表示全部流量）。

两边启动：`sudo bash udplex-gateway-manager.sh start`。

## 客户端

在出口上执行：

```bash
sudo bash udplex-gateway-manager.sh client add alice
sudo bash udplex-gateway-manager.sh client show alice   # 显示 WireGuard 配置，有 qrencode 时显示二维码
sudo bash udplex-gateway-manager.sh client list
sudo bash udplex-gateway-manager.sh client del alice
```

`client add` 为每个启用的协议生成 `/opt/udplex-gw/clients/<name>/wg.conf` 和 `<name>.ovpn`，分别导入 WireGuard 客户端或 OpenVPN Connect。增删 WireGuard 客户端会重启出口容器。`client del` 同时吊销 OpenVPN 证书；CRL 在每次握手时读取，被吊销的客户端无法再连接。

## 命令

| 命令 | 说明 |
|---|---|
| `install` | 把本机配置为入口或出口 |
| `start` / `stop` / `restart` | 控制容器；`restart` 使配置变更生效 |
| `status` | 容器、网卡和客户端 |
| `logs` | 查看容器日志 |
| `update` | 拉取最新镜像并重启 |
| `show-keys` | 显示共享密钥（入口还显示客户端端口） |
| `client add/del/list/show` | 管理客户端（仅出口） |
| `set-threshold <bps>` | 修改带宽阈值，之后执行 `restart`；两边应设置相同的值 |
| `lang <zh\|en>` | 切换脚本语言 |
| `uninstall` | 停止网关并删除 `/opt/udplex-gw`（可先备份） |

## 文件

| 路径 | 内容 |
|---|---|
| `settings.env` | `install` 时的选项 |
| `config.yaml` | 生成的 UDPlex 配置，`client` 和 `set-threshold` 会重新生成，请勿手动修改 |
| `docker-compose.yml` | 容器定义（host 网络；出口另有 `NET_ADMIN` 和 `/dev/net/tun`） |
| `keys/` | 出口：WireGuard 服务端密钥 |
| `pki/` | 出口：OpenVPN CA、服务端证书、`tc.key`、`crl.pem` |
| `clients/<name>/` | 出口：客户端密钥、`wg.conf`、`<name>.ovpn` |

## 工作原理

参见 [examples/gateway_entry.yaml](../examples/gateway_entry.yaml) 和 [examples/gateway_exit.yaml](../examples/gateway_exit.yaml)。

- 入口：每个协议一个 `broadcast_mode: false` 的 `listen`，为每个客户端分配连接 ID，并把报文交给 `load_balancer` 经两条线路发送。回包经 `filter` 分发：WireGuard 报文交给 WireGuard 的 `listen`，其余交给 OpenVPN 的 `listen`。
- 出口：线路 `listen` 设置了 [`preserve_conn_id`](listen_zh.md)，保留入口分配的 ID。`filter` 按协议分流，由 `bind_mode: udplex` 的 `wg` 和 [`openvpn`](openvpn_component_zh.md) 组件服务客户端。回包经 `load_balancer` 走两条线路。
- 出口组件会设置 `ip_forward`、`mss_clamp`，并把客户端地址池 masquerade 到默认出口网卡。UDPlex 启动时添加这些规则，停止时删除。容器内 `/proc/sys` 只读，所以脚本会在出口宿主机上开启 `net.ipv4.ip_forward`（`/etc/sysctl.d/99-udplex-gw.conf`）。

## 排错

- 在出口执行 `status`，可以看到 `wg_access` 和 `ovpn_access`。
- 客户端握手失败：检查两边的密钥、线路协议和端口，以及入口的客户端端口是否放行。
- 客户端能连上但无法上网：检查出口防火墙是否允许转发、默认出口网卡是否正确。如果 UDPlex 选错了 iptables 后端（启动日志中可见），在 `docker-compose.yml` 中设置 `UDPLEX_IPTABLES_BACKEND=legacy` 或 `nft`。
- 大流量下载卡住：客户端 MTU 为 1420 且已做 MSS clamp；如果到入口的路径 MTU 小于 1500，请在客户端配置中调低 MTU。

## 集成测试

`tests/integration` 在网络命名空间中运行网关示例配置，使用内核 WireGuard 客户端和 `openvpn` 客户端（需要 root、`wireguard-tools`、`openvpn`、`iptables`）：

```bash
cd tests/integration
./run_in_docker.sh -tests gateway            # 客户端 -> 入口 -> 出口 -> 目标
./run_in_docker.sh -tests gateway_internet   # 两种客户端经出口访问真实互联网
```

`gateway` 会向出口后面的目标发送 TCP 数据并逐字节校验；在每个报文都走两条线路的情况下，从两个客户端同时发送带编号的 UDP 报文，检查每个报文恰好返回一次且只回到发送者；还会检查 SIGTERM 后出口的 iptables 规则被清除。`gateway_internet` 只有在显式指定时才运行，因为它需要联网，并会在宿主机上添加一条 NAT 规则（结束后删除）。
