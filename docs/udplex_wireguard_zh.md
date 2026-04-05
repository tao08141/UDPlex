# UDPlex + 内嵌 WireGuard 部署指南

## 这套模式是什么

UDPlex 现在可以直接内嵌 `wireguard-go`。在这种模式下：

- UDPlex 仍然负责多线路转发与负载均衡
- WireGuard 数据通过 UDPlex 的外层线路传输
- 系统上仍会自动创建一个 WireGuard 网卡
- 但运行时不再依赖系统 `wg-quick` 或 `/etc/wireguard/*.conf`

这适合：

- 游戏加速
- 双线或多线 UDP 冗余
- 公网抖动较大时的低丢包转发

## 拓扑

```text
应用
  -> 入口机 WireGuard 网卡
  -> UDPlex 内嵌 WireGuard
  -> UDPlex 外层线路 1 / 外层线路 2
  -> 出口机 UDPlex
  -> UDPlex 内嵌 WireGuard
  -> 出口机 WireGuard 网卡
  -> 目标应用 / 目标网络
```

默认生成规则中：

- 低带宽时，两条外层线同时发同一份数据
- 高带宽时，可以选择按包序号分流到两条线路，或固定走一条首选线路

两条外层线路的协议现在可以独立选择。
例如线路 1 走 UDP、线路 2 走 TCP，也可以反过来。

## 运行要求

- 1 台入口机，靠近玩家或客户端
- 1 台出口机，靠近目标或游戏服务器
- 入口到出口至少 2 条可用外层链路
- Linux
- `sudo`
- 公网访问能力

安装脚本会自动处理：

- Docker
- Docker Compose 插件
- `wireguard-tools`

注意：
现在安装 `wireguard-tools` 主要是为了生成和查看密钥。
实际数据面走的是 UDPlex 内嵌 WireGuard，不是系统 `wg-quick`。

## 快速开始

在入口和出口都执行：

```bash
curl -fsSL -o udplex-wg-manager.sh https://raw.githubusercontent.com/tao08141/UDPlex/master/udplex-wg-manager.sh
chmod +x udplex-wg-manager.sh
sudo bash ./udplex-wg-manager.sh install
```

## 脚本现在会写入哪些文件

- `/opt/udplex/config.yaml`
- `/opt/udplex/docker-compose.yml`
- `/opt/udplex/role`
- `/opt/udplex/secret`
- `/opt/udplex/threshold`
- `/opt/udplex/lang`
- `/opt/udplex/wireguard/wg_private.key`
- `/opt/udplex/wireguard/wg_public.key`

兼容说明：

- 旧版本脚本可能把密钥放在 `/etc/wireguard`
- 当前脚本会自动迁移到 `/opt/udplex/wireguard`

现在不会再生成系统级的 `wg0.conf`。

## 启动

```bash
sudo bash ./udplex-wg-manager.sh start
```

这会启动 UDPlex 容器，并拉起内嵌 WireGuard 网卡。

默认接口名是：

```text
wg_udplex
```

## 常用命令

```bash
sudo bash ./udplex-wg-manager.sh status
sudo bash ./udplex-wg-manager.sh logs
sudo bash ./udplex-wg-manager.sh stop
sudo bash ./udplex-wg-manager.sh pause
sudo bash ./udplex-wg-manager.sh resume
sudo bash ./udplex-wg-manager.sh update
sudo bash ./udplex-wg-manager.sh reload
sudo bash ./udplex-wg-manager.sh show-keys
sudo bash ./udplex-wg-manager.sh lang zh
sudo bash ./udplex-wg-manager.sh set-threshold 80000000
```

`pause` 和 `resume` 操作的是内嵌接口 `wg_udplex`。

## 阈值与冗余策略

生成的负载均衡规则有两种行为：

- 当带宽低于阈值：
  同时走两条外层线，做冗余
- 当带宽高于阈值：
  可以按包序号拆分到两条线路，或者固定走一条首选线路

在线修改阈值：

```bash
sudo bash ./udplex-wg-manager.sh set-threshold 80000000
sudo bash ./udplex-wg-manager.sh reload
```

## 端口说明

如果使用默认值：

- 出口线路 1：`9000/udp`
- 出口线路 2：`9001/udp`

脚本现在支持每条外层线路独立选择协议：

- 线路 1 可以是 UDP 或 TCP
- 线路 2 可以是 UDP 或 TCP

例如：

- `9000/udp` + `9001/udp`
- `9000/udp` + `9001/tcp`
- `9000/tcp` + `9001/udp`
- `9000/tcp` + `9001/tcp`

要点：

- manager 不再依赖系统 WireGuard 服务配置文件启动
- 内嵌 WireGuard 的运行配置都在 `config.yaml`
- 真正需要放通的，仍然是你配置的 UDPlex 外层线路端口

## 联通性验证

```bash
sudo bash ./udplex-wg-manager.sh status
ip addr show wg_udplex
```

如果使用默认内层地址，可以测试：

```bash
ping 10.0.0.2
```

## 故障排查

查看容器日志：

```bash
sudo bash ./udplex-wg-manager.sh logs
```

查看 WireGuard 状态：

```bash
ip addr show wg_udplex
```

查看外层线路端口：

```bash
ss -lunpt | grep -E ":(9000|9001)\b" || true
ss -lntp | grep -E ":(9000|9001)\b" || true
```

如果外层“能收到但回不去”，优先检查：

- 两条外层线是否都已完成认证并处于可用状态
- 防火墙是否放通了正确端口

## 卸载

```bash
sudo bash ./udplex-wg-manager.sh uninstall
```

如果你选择“不删除密钥”，脚本会先把密钥备份，再删除 `/opt/udplex`。

## 安全建议

- UDPlex 共享密钥两端必须一致
- 只开放必要的外层端口
- 按需轮换 WireGuard 密钥与共享密钥
- 定期更新镜像
