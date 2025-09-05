# UDPlex + WireGuard 一键部署教程

## 为什么 UDPlex 能加速游戏？

UDPlex 通过以下机制实现游戏加速：
1. **多路径冗余传输**：同时使用多条网络路径传输数据包，当某条路径出现丢包或延迟时，其他路径仍可正常工作
2. **降低丢包率**：通过冗余传输，即使某条线路丢包，只要有一条线路成功传输，数据包就能到达
3. **路径优化**：选择更优质的网络路径，避开拥堵的公网路由
4. **实时故障切换**：当某条线路出现问题时，自动使用其他正常线路

## 架构说明

```
游戏客户端 -> WireGuard(Client) -> UDPlex Client -> 转发线路1 -> UDPlex Server -> WireGuard(Server) -> 游戏服务器
                                                  -> 转发线路2 ->
```

## 准备条件

- 入口机器 1 台（靠近玩家）
- 出口机器 1 台（靠近游戏服）
- 至少 2 条可达出口的线路（建议不同运营商/网络）
- Docker 环境

## 一键安装与初始化

在「入口」与「出口」两台机器上各执行以下步骤。脚本会自动：
- 安装 Docker 与 docker compose（如缺失）
- 安装 WireGuard（如缺失）
- 生成 WireGuard 密钥并展示本机公钥
- 交互式配置 UDPlex 与 WireGuard（写入 /opt/udplex 与 /etc/wireguard）

1) 下载脚本

```bash
curl -fsSL -o udplex-wg-manager.sh https://raw.githubusercontent.com/tao08141/UDPlex/master/udplex-wg-manager.sh
chmod +x udplex-wg-manager.sh
```

2) 同步进行安装（两端各开一个终端）

```bash
sudo bash ./udplex-wg-manager.sh install
```

安装流程要点：
- 一开始脚本会显示本机 WireGuard 公钥；请在两端都执行到这一步，互相复制对端公钥备用
- 选择语言（中文/英文）
- 设置带宽阈值 bps（默认 50,000,000，即 50Mbps，用于“智能分流”）
- 选择角色：1=入口(client)，2=出口(server)
- 粘贴对端的 WireGuard 公钥
- 根据提示填写端口：
    - 入口：本地 UDPlex 监听端口（默认 7000），转发线路1/2的出口目标（形如 出口IP:9000 / 出口IP:9001）
    - 出口：两条线路监听端口（默认 9000、9001），WireGuard 服务端口（默认 51820）
- WireGuard 地址段默认：入口 10.0.0.1/24 ↔ 出口 10.0.0.2/24（可按需修改）

完成后，关键文件：
- /opt/udplex/docker-compose.yml
- /opt/udplex/config.yaml（已内置智能分流规则）
- /etc/wireguard/wg0.conf

## 启动与开机自启

在两端分别执行：

```bash
sudo bash ./udplex-wg-manager.sh start
```

说明：
- 会启动/拉起 UDPlex 容器
- 启动 WireGuard，并设置 wg0 开机自启

## 常用管理命令

```bash
sudo bash ./udplex-wg-manager.sh status     # 查看 UDPlex/WireGuard/端口状态
sudo bash ./udplex-wg-manager.sh logs       # 跟随 UDPlex 日志
sudo bash ./udplex-wg-manager.sh stop       # 停止 UDPlex 与 WireGuard
sudo bash ./udplex-wg-manager.sh pause      # 暂停 wg0（容器保留运行）
sudo bash ./udplex-wg-manager.sh resume     # 恢复 wg0
sudo bash ./udplex-wg-manager.sh update     # 拉取最新镜像并重启容器
sudo bash ./udplex-wg-manager.sh reload     # 仅重载配置（compose up -d）
sudo bash ./udplex-wg-manager.sh show-keys  # 显示本机 WireGuard 公钥
sudo bash ./udplex-wg-manager.sh lang en    # 切换脚本语言（zh/en）
```

## 智能分流与阈值

脚本内置的 `config.yaml` 已包含基于带宽与序号（seq）的分流：
- 当 bps ≤ 阈值（默认 50Mbps）时：两条线路冗余发送，降低丢包
- 当 bps > 阈值时：按照数据包序号奇偶拆分到不同线路，避免带宽浪费

在线更新阈值：

```bash
sudo bash ./udplex-wg-manager.sh set-threshold 80000000
sudo bash ./udplex-wg-manager.sh reload
```

## 防火墙与端口

默认端口（如在安装时未改）：
- 入口：7000/udp（本地 UDPlex 监听，WireGuard 出口会连 127.0.0.1:7000）
- 出口：9000/udp、9001/udp（两条线路监听）、51820/udp（WireGuard 服务端口）

请在云厂商/系统防火墙中放通这些 UDP 端口。

## 验证连接

在两端启动后：

```bash
sudo bash ./udplex-wg-manager.sh status
sudo wg show
# 从入口 ping 出口（若按默认地址）：
ping 10.0.0.2
```

## 故障排查

- 查看容器日志：
    ```bash
    sudo bash ./udplex-wg-manager.sh logs
    ```
- 查看 WireGuard 状态：
    ```bash
    sudo wg show
    ```
- 端口占用/放通：
    ```bash
    ss -lunpt | grep -E ":(7000|9000|9001|51820)\b" || netstat -tulpn | grep -E ":(7000|9000|9001|51820)\b"
    ```
- Docker 网络异常：
    ```bash
    sudo systemctl restart docker
    ```

## 安全建议

- 安装时为 UDPlex 鉴权自动生成强随机密钥（两端需一致），可在 /opt/udplex/secret 查看与同步
- 定期执行 `update` 获取最新镜像
- 仅放通必要 UDP 端口，限制来源 IP
- 定期审查日志
