Double UDP 是一个高效的 UDP 数据包双向转发工具，支持将 UDP 流量同时转发到多个目标服务器，并能处理返回流量。

## 功能特点

- 监听指定 UDP 端口接收数据包
- 将数据包并行转发到多个目标服务器
- 支持双向流量转发
- 自动重连断开的连接
- 可配置的缓冲区大小和超时参数
- 简单的 JSON 配置文件

## 安装

```bash
git clone https://github.com/yourusername/double_udp.git
cd double_udp
go build
```

## 使用方法

1. 编辑 config.json 文件配置监听地址和转发目标
2. 运行程序：

```bash
# 使用默认配置文件
./double_udp

# 或指定配置文件路径
./double_udp -c /path/to/config.json
```

## 配置说明

配置文件使用 JSON 格式，包含多个组件配置：

### 监听组件 (listen)

```json
{
    "type": "listen",
    "tag": "client_listen",
    "listen_addr": "0.0.0.0:6000",
    "timeout": 120,
    "replace_old_conns": true,
    "detour": [
        "client_forward"
    ]
}
```

### 转发组件 (forward)

```json
{
    "type": "forward",
    "tag": "client_forward",
    "forwarders": [
        "a.com:1111",
        "b.com:2222"
    ],
    "queue_size": 1024,
    "reconnect_interval": 5,
    "connection_check_time": 30,
    "detour": [
        "client_listen"
    ]
}
```

## 参数详解

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `listen` 或 `forward` |
| `tag` | 组件唯一标识 |
| `listen_addr` | 监听地址和端口 (仅 listen 组件) |
| `timeout` | 连接超时时间 (秒) (仅 listen 组件) |
| `replace_old_conns` | 是否替换旧连接 (仅 listen 组件)  |
| `forwarders` | 转发目标地址列表 (仅 forward 组件) |
| `queue_size` | 队列大小 (仅 forward 组件) |
| `reconnect_interval` | 重连间隔时间 (秒)  (仅 forward 组件) |
| `connection_check_time` | 连接检查间隔 (秒)  (仅 forward 组件) |
| `detour` | 转发路径，指定接收返回数据的组件 |


## 使用场景

- 游戏加速：将游戏流量同时转发到多个服务器，选择最快的响应
- 网络冗余：确保重要的 UDP 数据能通过多条路径传输
- 流量分流：将 UDP 流量复制到多个目标进行处理

## 注意事项

- 确保监听端口在防火墙中已开放
- 转发目标服务器需要能够正确处理转发来的 UDP 数据包
- 对于高流量场景，请适当调整 `buffer_size` 和 `queue_size` 参数


## 目录结构

### 核心文件

- **main.go** - 程序入口点，包含配置解析、组件路由系统和主函数
- **packe.go** - 定义数据包结构体(Packet)，用于在组件间传递UDP数据
- **listen.go** - 实现监听组件(ListenComponent)，负责监听UDP端口接收数据包
- **forward.go** - 实现转发组件(ForwardComponent)，负责将数据包转发到目标服务器
- **config.json** - 主配置文件，定义系统监听地址和转发目标
- **go.mod** - Go模块定义文件

### 配置示例

examples目录包含多种使用场景的配置示例：

- **redundant_client_config.json** - 冗余客户端配置，将流量同时发送到多个服务器
- **redundant_server_config.json** - 冗余服务端配置，接收客户端流量并转发
- **bidirectional_client_config.json** - 上下行分离通信客户端配置
- **bidirectional_server_config.json** - 上下行分离通信服务端配置
