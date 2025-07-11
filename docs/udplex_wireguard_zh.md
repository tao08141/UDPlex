# UDPlex 游戏加速部署教程

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

## 准备工作

需要准备：
- 1台入口机器（靠近你的位置）
- 1台出口机器（靠近游戏服务器）
- 2条或以上转发线路（连接入口和出口机器）
- Docker 环境

## 步骤一：安装 Docker

### 在两台机器上安装 Docker

```bash
# 下载 Docker 安装脚本
curl -fsSL https://get.docker.com -o install-docker.sh

# 安装 Docker
sudo sh install-docker.sh
```


## 步骤二：配置 WireGuard

### 2.1 安装 WireGuard

**Ubuntu/Debian:**
```bash
sudo apt update
sudo apt install wireguard
```

**CentOS/RHEL:**
```bash
sudo yum install epel-release
sudo yum install wireguard-tools
```

### 2.2 生成密钥对

**在入口机器执行：**
```bash
# 生成私钥
wg genkey | tee client_private.key | wg pubkey > client_public.key

# 查看密钥
echo "Client Private Key:"
cat client_private.key
echo "Client Public Key:"
cat client_public.key
```

**在出口机器执行：**
```bash
# 生成私钥
wg genkey | tee server_private.key | wg pubkey > server_public.key

# 查看密钥
echo "Server Private Key:"
cat server_private.key
echo "Server Public Key:"
cat server_public.key
```

### 2.3 配置 WireGuard（关键：流量走 UDPlex）

**入口机器配置文件 (`/etc/wireguard/wg0.conf`)：**
```ini
[Interface]
PrivateKey = <入口机器的私钥>
Address = 10.0.0.1/24

[Peer]
PublicKey = <出口机器的公钥>
Endpoint = 127.0.0.1:7000  # 指向本地 UDPlex 客户端的出口端口，这里可以先不使用udplex，测试WireGuard连接是否正常
AllowedIPs = 10.0.0.2/32
PersistentKeepalive = 25
```

**出口机器配置文件 (`/etc/wireguard/wg0.conf`)：**
```ini
[Interface]
PrivateKey = <出口机器的私钥>
Address = 10.0.0.2/24
ListenPort = 51820

[Peer]
PublicKey = <入口机器的公钥>
AllowedIPs = 10.0.0.1/32
PersistentKeepalive = 25
```

## 步骤三：部署 UDPlex

### 3.1 创建项目目录

**在入口与入口机器分别执行：**
```bash
mkdir -p ~/udplex
cd ~/udplex
```

**创建 `~/udplex/docker-compose.yml`：**

````yaml

services:
  udplex:
    image: ghcr.io/tao08141/udplex:latest
    container_name: udplex
    restart: always
    volumes:
      - ./config.json:/app/config.json
    network_mode: host
    logging:
      options:
        max-size: "10m"
        max-file: "3"

````


### 3.2 配置入口机器（Client）

**创建 `~/udplex/config.json`：**

````json
{
    "buffer_size": 1500,
    "queue_size": 10240,
    "worker_count": 4,
    "logging": {
        "level": "info",
        "format": "console",
        "output_path": "stdout",
        "caller": true
    },
    "services": [
        {
            "type": "listen",
            "tag": "wg_input",
            "listen_addr": "0.0.0.0:7000",
            "timeout": 120,
            "replace_old_mapping": true,
            "detour": [
                "redundant_forward"
            ]
        },
        {
            "type": "forward",
            "tag": "redundant_forward",
            "forwarders": [
                "转发线路1的IP:9000",
                "转发线路2的IP:9000"
            ],
            "reconnect_interval": 5,
            "connection_check_time": 30,
            "send_keepalive": true,
            "detour": [
                "wg_input"
            ],
            "auth": {
                "secret": "your-super-secret-key-2024",
                "enabled": true,
                "enable_encryption": false, // wg已经自带了加密，没必要再加一次
                "heartbeat_interval": 30
            }
        }
    ]
}
````



### 3.3 配置出口机器（Server）

**创建 `~/udplex/config.json`：**

````json
{
    "buffer_size": 1500,
    "queue_size": 10240,
    "worker_count": 4,
    "logging": {
        "level": "info",
        "format": "console",
        "output_path": "stdout",
        "caller": true
    },
    "services": [
        {
            "type": "listen",
            "tag": "server_listen",
            "listen_addr": "0.0.0.0:9000",
            "timeout": 120,
            "replace_old_mapping": false,
            "detour": [
                "wg_forward"
            ],
            "auth": {
                "secret": "your-super-secret-key-2024",
                "enabled": true,
                "enable_encryption": false,
                "heartbeat_interval": 30
            }
        },
        {
            "type": "forward",
            "tag": "wg_forward",
            "forwarders": [
                "127.0.0.1:51820" // 出口 WireGuard 服务端口
            ],
            "reconnect_interval": 5,
            "connection_check_time": 30,
            "send_keepalive": false,
            "detour": [
                "server_listen"
            ]
        }
    ]
}
````

## 步骤四：启动服务

### 4.1 启动 UDPlex

**在出口机器先启动：**
```bash
cd ~/udplex
docker-compose up -d
```

**在入口机器启动：**
```bash
cd ~/udplex
docker-compose up -d
```

### 4.2 启动 WireGuard

**在两台机器上执行：**
```bash
# 启动 WireGuard
sudo wg-quick up wg0

# 设置开机自启
sudo systemctl enable wg-quick@wg0
```

### 4.3 验证连接

```bash
# 检查 UDPlex 容器状态
docker-compose ps

# 查看 UDPlex 日志
docker-compose logs -f

# 检查 WireGuard 状态
sudo wg show

# 测试 WireGuard 连接（从入口机器 ping 出口机器）
ping 10.0.0.2
```


## 步骤五：监控和管理

### 查看 UDPlex 日志
```bash
# 入口机器
cd ~/udplex && docker-compose logs -f

# 出口机器
cd ~/udplex && docker-compose logs -f
```

### 重启服务
```bash
# 重启 UDPlex
docker-compose restart

# 重启 WireGuard
sudo wg-quick down wg0
sudo wg-quick up wg0
```

### 停止服务
```bash
# 停止 UDPlex
docker-compose down

# 停止 WireGuard
sudo wg-quick down wg0
```

## 进阶教程：智能流量控制(当前配置不是必须的，但是可以避免流量浪费)

需要将两条线路分开配置，使用不同的转发标签

### 进阶配置文件（入口机器）

````json
{
    "buffer_size": 1500,
    "queue_size": 10240,
    "worker_count": 4,
    "logging": {
        "level": "info",
        "format": "console",
        "output_path": "stdout",
        "caller": true
    },
    "services": [
        {
            "type": "listen",
            "tag": "wg_input",
            "listen_addr": "0.0.0.0:7000",
            "timeout": 120,
            "replace_old_mapping": true,
            "detour": [
                "load_balancer" // 这里使用负载均衡器来控制流量的发送
            ]
        },
        {
            "type": "forward",
            "tag": "redundant_forward1",
            "forwarders": [
                "转发线路1的IP:9000",
            ],
            "reconnect_interval": 5,
            "connection_check_time": 30,
            "send_keepalive": true,
            "detour": [
                "wg_input"
            ],
            "auth": {
                "secret": "your-super-secret-key-2024",
                "enabled": true,
                "enable_encryption": false,
                "heartbeat_interval": 30
            }
        },
        {
            "type": "forward",
            "tag": "redundant_forward2",
            "forwarders": [
                "转发线路2的IP:9001",
            ],
            "reconnect_interval": 5,
            "connection_check_time": 30,
            "send_keepalive": true,
            "detour": [
                "wg_input"
            ],
            "auth": {
                "secret": "your-super-secret-key-2024",
                "enabled": true,
                "enable_encryption": false,
                "heartbeat_interval": 30
            }
        },
        {
            "type": "load_balancer",
            "tag": "load_balancer",
            "window_size": 3,
            "detour": [
               {
                    "rule": "bps <= 50000000",
                    "targets": ["redundant_forward1", "redundant_forward2"]
                },
                {
                    "rule": "bps > 50000000",
                    "targets": ["server_listen_up"]
                }
            ]
        }
    ]
}
````

### 进阶配置文件（出口机器）

````json
{
    "buffer_size": 1500,
    "queue_size": 10240,
    "worker_count": 4,
    "logging": {
        "level": "info",
        "format": "console",
        "output_path": "stdout",
        "caller": true
    },
    "services": [
        {
            "type": "listen",
            "tag": "server_listen1", // 监听线路1
            "listen_addr": "0.0.0.0:9000",
            "timeout": 120,
            "replace_old_mapping": false,
            "detour": [
                "wg_forward"
            ],
            "auth": {
                "secret": "your-super-secret-key-2024",
                "enabled": true,
                "enable_encryption": false,
                "heartbeat_interval": 30
            }
        },
        {
            "type": "listen",
            "tag": "server_listen2", // 监听线路2
            "listen_addr": "0.0.0.0:9001",
            "timeout": 120,
            "replace_old_mapping": false,
            "detour": [
                "wg_forward"
            ],
            "auth": {
                "secret": "your-super-secret-key-2024",
                "enabled": true,
                "enable_encryption": false,
                "heartbeat_interval": 30
            }
        },
        {
            "type": "forward",
            "tag": "wg_forward",
            "forwarders": [
                "127.0.0.1:51820" // 出口 WireGuard 服务端口
            ],
            "reconnect_interval": 5,
            "connection_check_time": 30,
            "send_keepalive": false,
            "detour": [
                "load_balancer" // 使用负载均衡器来控制流量的发送
            ]
        },
        {
            "type": "load_balancer",
            "tag": "load_balancer",
            "window_size": 3,
            "detour": [
               {
                    "rule": "bps <= 50000000",
                    "targets": ["redundant_forward1", "redundant_forward2"]
                },
                {
                    "rule": "bps > 50000000",
                    "targets": ["server_listen_up"]
                }
            ]
        }
    ]
}
````


这个配置实现了：
- 当带宽 ≤ 50Mbps 时，同时使用两条线路（冗余传输，降低丢包）
- 当带宽 > 50Mbps 时，只使用线路1（避免带宽浪费）

## 故障排除

### 常见问题

1. **UDPlex 容器无法启动**
```bash
# 检查配置文件语法
docker-compose config

# 查看详细错误信息
docker-compose logs
```

2. **WireGuard 连接失败**
```bash
# 检查 iptables 规则
sudo iptables -t nat -L -n

# 检查路由表
ip route show table all
```

3. **端口被占用**
```bash
# 检查端口占用
sudo netstat -tulpn | grep :7000
sudo netstat -tulpn | grep :9000
```

4. **Docker 网络问题**
```bash
# 重启 Docker 网络
sudo systemctl restart docker
```

## 安全建议

1. **使用强密钥**：将配置文件中的 `your-super-secret-key-2024` 替换为更复杂的随机字符串
2. **启用鉴权**：确保 `auth` 配置中的 `enabled` 设置为 `true`
3. **定期更新**：定期更新 UDPlex 镜像到最新版本
4. **监控日志**：定期检查日志中的异常连接
5. **网络隔离**：使用防火墙限制不必要的端口访问

```bash
# 更新 UDPlex 镜像
docker-compose pull
docker-compose up -d
```

现在你的 WireGuard 流量将通过 UDPlex 进行多路径冗余传输，享受更稳定的网络连接！
