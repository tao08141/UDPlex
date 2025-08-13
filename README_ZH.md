# UDPlex
[English](README.md) | [中文](README_ZH.md)

UDPlex 是一个高效的 UDP 数据包双向转发工具，支持将 UDP 流量同时转发到多个目标服务器，并能处理返回流量，并支持鉴权、加密等功能。它适用于游戏加速、网络冗余和流量分流等场景。

## 功能特点

- 监听指定 UDP 端口接收数据包
- 将数据包并行转发到多个目标服务器
- 支持双向流量转发
- 自动重连断开的连接
- 可配置的缓冲区大小和超时参数
- 支持鉴权和加密传输
- 支持协议检测和过滤
- 支持 Docker 部署


## 使用方法

1. 编辑 config.json 文件配置监听地址和转发目标
2. 运行程序：

```bash
# 使用默认配置文件
./UDPlex

# 或指定配置文件路径
./UDPlex -c /path/to/config.json
```

## Docker 使用方法

### 使用 Docker 命令

```bash
# 拉取镜像
docker pull ghcr.io/tao08141/udplex:latest

# 运行容器 (使用主机网络模式)
docker run -d --name udplex --network host \
  -v $(pwd)/config.json:/app/config.json \
  ghcr.io/tao08141/udplex:latest

# 使用端口映射模式 (如果不使用主机网络模式)
docker run -d --name udplex \
  -v $(pwd)/config.json:/app/config.json \
  -p 9000:9000/udp \
  ghcr.io/tao08141/udplex:latest
```

### 使用 Docker Compose

1. 下载 docker-compose.yml 文件:

```bash
mkdir udplex && cd udplex
# 下载 docker-compose.yml 文件
curl -o docker-compose.yml https://raw.githubusercontent.com/tao08141/UDPlex/refs/heads/master/docker-compose.yml
# 下载配置文件
curl -o config.json https://raw.githubusercontent.com/tao08141/UDPlex/refs/heads/master/examples/basic.json
```

2. 启动服务:

```bash
docker-compose up -d
```

3. 查看日志:

```bash
docker-compose logs -f
```

4. 停止服务:

```bash
docker-compose down
```

> 注意：对于 UDP 转发应用，建议使用主机网络模式 (network_mode: host) 以获得最佳性能。如果需要精确控制端口映射，可以使用端口映射模式。

## WireGuard 一键部署教程

- UDPlex + WireGuard 一键部署指南（中文）: [docs/udplex_wireguard_zh.md](docs/udplex_wireguard_zh.md)

# UDPlex 参数详解

## 全局配置

| 参数 | 说明 |
|------|------|
| `buffer_size` | UDP数据包缓冲区大小（字节），建议设置为MTU大小，通常为1500 |
| `queue_size` | 组件间数据包队列大小，高流量场景建议增大此值 |
| `worker_count` | 工作线程数量，影响并发处理能力 |
| `services` | 组件配置数组，定义系统中所有的处理组件 |
| `protocol_detectors` | 协议检测器配置，用于识别和过滤特定协议的数据包 |

## 服务组件参数

UDPlex 支持多种组件类型，每种组件都有特定的功能和配置参数。详细文档请参考：

- [Listen 组件](docs/listen_zh.md) - 监听 UDP 端口并接收数据包
- [Forward 组件](docs/forward_zh.md) - 将数据包转发到目标服务器
- [Filter 组件](docs/filter_zh.md) - 根据协议特征过滤和分类数据包
- [TCP Tunnel Listen 组件](docs/tcp_tunnel_listen_zh.md) - TCP 隧道监听端
- [TCP Tunnel Forward 组件](docs/tcp_tunnel_forward_zh.md) - TCP 隧道转发端
- [Load Balancer 组件](docs/load_balancer_zh.md) - 负载均衡组件


### 鉴权配置

UDPlex 支持数据包鉴权与加密功能，详细文档请参考：

- [鉴权协议](docs/auth_protocol_zh.md) - 鉴权协议详细说明


## 协议检测器

UDPlex 支持通过配置协议检测器来识别和分类 UDP 数据包中的特定协议，详细文档请参考：

- [协议检测器](docs/protocol_detector_zh.md) - 协议检测器配置和使用说明


## 开发计划
- [X] 支持包过滤和选择性转发
- [X] 支持鉴权、加密、去重等功能
- [X] 支持UDP Over TCP的转发
- [X] 支持更复杂的负载均衡算法
- [X] RESTful API 接口

## RESTful API 接口
UDPlex 提供了 RESTful API 接口，可以查询组件状态和连接信息。

- [RESTful](docs/RESTful_zh.md) - RESTful接口配置和使用说明

## 使用场景
- **游戏加速**：将游戏流量同时转发到多个路径，极大程度减少了丢包
- **带宽叠加**：将多个网络连接的带宽进行叠加，提升整体网络速度
- **按照协议分类**：通过协议检测器将不同协议的流量分发到不同的处理路径，比如较为重要的心跳包
- **UDP over TCP隧道**：在不支持UDP的网络环境中传输UDP流量
- **网络调试**：在不影响原有网络结构的情况下，将UDP流量转发到指定服务器进行调试和分析
- **负载均衡**：根据流量大小和负载策略智能分发数据包，支持多种负载均衡算法，如流量较小时向两个服务器转发避免丢包，大于一定阈值时向两个服务器轮询发送保证宽带速度。


## 配置示例

examples目录包含多种使用场景的配置示例：

- [**basic.json**](examples/basic.json) - UDP转发的基本配置示例
- [**auth_client.json**](examples/auth_client.json) - 带鉴权的UDP客户端配置
- [**auth_server.json**](examples/auth_server.json) - 带鉴权的UDP服务端配置
- [**redundant_client.json**](examples/redundant_client.json) - UDP冗余客户端配置，将流量同时发送到多个服务器
- [**redundant_server.json**](examples/redundant_server.json) - UDP冗余服务端配置，接收客户端流量并转发
- [**wg_bidirectional_client.json**](examples/wg_bidirectional_client.json) - WireGuard UDP上下行分离通信客户端配置
- [**wg_bidirectional_server.json**](examples/wg_bidirectional_server.json) - WireGuard UDP上下行分离通信服务端配置
- [**tcp_tunnel_server.json**](examples/tcp_tunnel_server.json) - TCP隧道服务端配置，监听TCP连接并转发UDP流量
- [**tcp_tunnel_client.json**](examples/tcp_tunnel_client.json) - TCP隧道客户端配置，连接TCP隧道服务并转发UDP流量
- [**load_balancer_bandwidth_threshold.json**](examples/load_balancer_bandwidth_threshold.json) - 基于带宽阈值的负载均衡配置，当流量小于等于100M时向两个服务器转发，大于100M时只向一个服务器转发
- [**load_balancer_equal_distribution.json**](examples/load_balancer_equal_distribution.json) - 均衡负载配置，以1:1的比例向两个服务器分发数据
