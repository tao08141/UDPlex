# UDPlex 接入网关

`udplex-gateway-manager.sh` 把两台服务器组成接入网关。手机和电脑用官方 WireGuard 或 OpenVPN 客户端连接**入口**，流量经 UDPlex 内部隧道（在两条 UDPlex 线路上运行的内嵌 WireGuard）到达**出口**，再从出口访问互联网。

它与 [`udplex-wg-manager.sh`](udplex_wireguard_zh.md) 完全独立：使用自己的目录（`/opt/udplex-gw`）、容器（`udplex-gw`）、网卡和端口，两者可以在同一台服务器上共存。

## 拓扑

```text
WireGuard / OpenVPN 客户端
  -> 入口: wg_access (UDP 51821) / ovpn_access (1194)
  -> NAT 为内部隧道地址，策略路由进入 wg_gw
  -> UDPlex 线路 #1 + 线路 #2（各自可选 UDP 或 TCP）
  -> 出口: wg_gw
  -> NAT 为出口公网地址
  -> 互联网
```

入口把客户端地址 NAT 成自己的内部隧道地址，所以出口不需要回程到客户端地址池的路由。只有来自客户端地址池的流量会被策略路由，入口自身的流量不受影响。

## 环境要求

- 两台 Linux 服务器，有 root 权限，安装 Docker（脚本会在缺失时自动安装）
- 两边都有 `/dev/net/tun`
- 放行端口：出口放行两个线路端口（默认 9100、9101）；入口放行 WireGuard（51821/udp）和 OpenVPN（1194）端口
- 启用 OpenVPN 时入口需要 `openssl`

## 安装

在两台服务器上同时下载并运行脚本，因为双方都要输入对方的公钥：

```bash
curl -fsSL -o udplex-gateway-manager.sh https://raw.githubusercontent.com/tao08141/UDPlex/master/udplex-gateway-manager.sh
sudo bash udplex-gateway-manager.sh install
```

1. 选择角色：`1` 入口，`2` 出口。
2. 输入共享密钥。第一台留空即自动生成，再把它填到第二台。
3. 把两边显示的内部隧道公钥互相填入。
4. 设置带宽阈值、每条线路的协议和高流量模式，与 `udplex-wg-manager.sh` 相同。
5. 出口输入两个线路端口；入口输入出口的两个地址（`host:port`）。
6. 入口选择接入协议：
   - WireGuard：端口和客户端地址池（默认 `10.8.0.0/24`）
   - OpenVPN：UDP 或 TCP、端口和客户端地址池（默认 `10.9.0.0/24`）。脚本会生成 CA、服务端证书和 `tls-crypt` 密钥。
   - 写入客户端配置的公网地址、推送给客户端的 DNS，以及客户端经网关转发的路由（`0.0.0.0/0` 表示全部流量）。
7. 两边启动：`sudo bash udplex-gateway-manager.sh start`。

## 客户端

在入口上执行：

```bash
sudo bash udplex-gateway-manager.sh client add alice
sudo bash udplex-gateway-manager.sh client show alice   # 显示 WireGuard 配置，有 qrencode 时显示二维码
sudo bash udplex-gateway-manager.sh client list
sudo bash udplex-gateway-manager.sh client del alice
```

`client add` 为每个启用的协议生成 `/opt/udplex-gw/clients/<name>/wg.conf` 和 `<name>.ovpn`，分别导入 WireGuard 客户端或 OpenVPN Connect。增删 WireGuard 客户端会重启容器。`client del` 同时吊销 OpenVPN 证书；CRL 在每次握手时读取，被吊销的客户端无法再连接。

## 命令

| 命令 | 说明 |
|---|---|
| `install` | 把本机配置为入口或出口 |
| `start` / `stop` / `restart` | 控制容器；`restart` 使配置变更生效 |
| `status` | 容器、网卡、策略路由和客户端 |
| `logs` | 查看容器日志 |
| `update` | 拉取最新镜像并重启 |
| `show-keys` | 显示共享密钥和内部隧道公钥 |
| `client add/del/list/show` | 管理客户端（仅入口） |
| `set-threshold <bps>` | 修改带宽阈值，之后执行 `restart` |
| `lang <zh\|en>` | 切换脚本语言 |
| `uninstall` | 停止网关并删除 `/opt/udplex-gw`（可先备份） |

## 文件

| 路径 | 内容 |
|---|---|
| `settings.env` | `install` 时的选项 |
| `config.yaml` | 生成的 UDPlex 配置，`client` 和 `set-threshold` 会重新生成，请勿手动修改 |
| `docker-compose.yml` | 容器定义（host 网络、`NET_ADMIN`、`/dev/net/tun`） |
| `keys/` | 内部隧道和 WireGuard 接入密钥 |
| `pki/` | OpenVPN CA、服务端证书、`tc.key`、`crl.pem` |
| `clients/<name>/` | 客户端密钥、`wg.conf`、`<name>.ovpn` |

## 工作原理

生成的配置使用以下组件：

- 内部隧道：两边各一个 `wg` 组件 `wg_gw`，经 `forward`/`listen` 或 `tcp_tunnel_*` 线路和 `load_balancer` 传输，与 `udplex-wg-manager.sh` 相同。入口上它的 peer 设置 `allowed_ips: 0.0.0.0/0`，以接收任意目的地址的回程流量。
- WireGuard 接入：`bind_mode: native` 的 `wg` 组件（见 [WireGuard 组件](wg_component_zh.md)）。
- OpenVPN 接入：[`openvpn` 组件](openvpn_component_zh.md)。
- 两个接入组件都设置 `ip_forward`、`mss_clamp`、从客户端地址池到 `wg_gw` 的策略路由（表 7100）以及从 `wg_gw` 出去的 masquerade。出口把内部隧道 masquerade 到默认出口网卡。

UDPlex 启动时添加这些路由、规则和 iptables 条目，停止时删除。容器内 `/proc/sys` 只读，所以脚本会在宿主机上开启 `net.ipv4.ip_forward`（`/etc/sysctl.d/99-udplex-gw.conf`）。

## 排错

- `status` 显示网卡和 `ip rule`；确认两边都有 `wg_gw`，并且入口能 `ping 10.0.0.2`。
- 客户端能连上但无法上网：检查出口防火墙是否允许转发、默认出口网卡是否正确。如果 UDPlex 选错了 iptables 后端（启动日志中可见），在 `docker-compose.yml` 中设置 `UDPLEX_IPTABLES_BACKEND=legacy` 或 `nft`。
- 大流量下载卡住：所有网卡 MTU 为 1420 且已做 MSS clamp；如果线路额外开销更大，请在客户端配置中调低 MTU。

## 集成测试

`tests/integration` 在网络命名空间中运行网关示例配置，使用内核 WireGuard 客户端和 `openvpn` 客户端（需要 root、`wireguard-tools`、`openvpn`、`iptables`）：

```bash
cd tests/integration
./run_in_docker.sh -tests gateway            # 客户端 -> 入口 -> 出口 -> 目标，并检查 SIGTERM 后的清理
./run_in_docker.sh -tests gateway_internet   # 两种客户端经出口访问真实互联网
```

`gateway_internet` 只有在显式指定时才运行，因为它需要联网，并会在宿主机上添加一条 NAT 规则（结束后删除）。
