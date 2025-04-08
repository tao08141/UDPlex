UDPlex 是一个高效的 UDP 数据包双向转发工具，支持将 UDP 流量同时转发到多个目标服务器，并能处理返回流量。

## 功能特点

- 监听指定 UDP 端口接收数据包
- 将数据包并行转发到多个目标服务器
- 支持双向流量转发
- 自动重连断开的连接
- 可配置的缓冲区大小和超时参数
- 简单的 JSON 配置文件

## 安装

```bash
git clone https://github.com/tao08141/UDPlex.git
cd UDPlex
go build
```

## 使用方法

1. 编辑 config.json 文件配置监听地址和转发目标
2. 运行程序：

```bash
# 使用默认配置文件
./UDPlex

# 或指定配置文件路径
./UDPlex -c /path/to/config.json
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
- 仅支持 UDP 协议，不支持 TCP
- 没有处理包重复的问题，可能会导致数据包重复发送到目标服务器，请根据实际需求进行处理
- 没有鉴权机制，`listen`组件会回复所有建立了连接的地址，可能会导致数据包泄露，请根据实际需求进行处理


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

- **basic.json** - UDP转发的基本配置示例
- **redundant_client_config.json** - UDP冗余客户端配置，将流量同时发送到多个服务器
- **redundant_server_config.json** - UDP冗余服务端配置，接收客户端流量并转发
- **bidirectional_client_config.json** - UDP上下行分离通信客户端配置
- **bidirectional_server_config.json** - UDP上下行分离通信服务端配置

## 性能测试

+ 测试命令`iperf3.exe -c 127.0.0.1  -p 5202 -u  -b 100000M`

+ 基准测试，iperf3性能测试，没有中间件。
```bash
-----------------------------------------------------------
Server listening on 5201 (test #1)
-----------------------------------------------------------
Accepted connection from 127.0.0.1, port 58706
[  5] local 127.0.0.1 port 5201 connected to 127.0.0.1 port 63982
[ ID] Interval           Transfer     Bitrate         Jitter    Lost/Total Datagrams
[  5]   0.00-1.01   sec  5.94 GBytes  50.5 Gbits/sec  0.005 ms  805/98222 (0.82%)
[  5]   1.01-2.00   sec  5.66 GBytes  49.1 Gbits/sec  0.004 ms  778/93535 (0.83%)
[  5]   2.00-3.01   sec  5.74 GBytes  49.1 Gbits/sec  0.005 ms  769/94898 (0.81%)
[  5]   3.01-4.00   sec  5.74 GBytes  49.6 Gbits/sec  0.002 ms  531/94593 (0.56%)
[  5]   4.00-5.01   sec  5.77 GBytes  49.2 Gbits/sec  0.007 ms  644/95227 (0.68%)
[  5]   5.01-6.01   sec  5.88 GBytes  50.4 Gbits/sec  0.003 ms  930/97358 (0.96%)
[  5]   6.01-7.01   sec  5.69 GBytes  48.8 Gbits/sec  0.005 ms  721/93989 (0.77%)
[  5]   7.01-8.01   sec  5.63 GBytes  48.6 Gbits/sec  0.006 ms  647/92910 (0.7%)
[  5]   8.01-9.01   sec  5.80 GBytes  49.7 Gbits/sec  0.002 ms  848/95871 (0.88%)
[  5]   9.01-10.01  sec  5.73 GBytes  49.1 Gbits/sec  0.010 ms  799/94792 (0.84%)
[  5]  10.01-10.01  sec   128 KBytes  3.44 Gbits/sec  0.013 ms  0/2 (0%)
- - - - - - - - - - - - - - - - - - - - - - - - -
[ ID] Interval           Transfer     Bitrate         Jitter    Lost/Total Datagrams
[  5]   0.00-10.01  sec  57.6 GBytes  49.4 Gbits/sec  0.013 ms  7472/951397 (0.79%)  receiver
```

+ 使用了转发配置文件 `basic.json` 中的示例配置，iperf3性能测试，使用了中间件。
```bash
-----------------------------------------------------------
Server listening on 5201 (test #2)
-----------------------------------------------------------
Accepted connection from 127.0.0.1, port 58929
[  5] local 127.0.0.1 port 5201 connected to 127.0.0.1 port 57174
[ ID] Interval           Transfer     Bitrate         Jitter    Lost/Total Datagrams
[  5]   0.00-1.01   sec  3.55 GBytes  30.3 Gbits/sec  0.008 ms  40878/99061 (41%)
[  5]   1.01-2.02   sec  3.66 GBytes  31.1 Gbits/sec  0.008 ms  73155/133095 (55%)
[  5]   2.02-3.01   sec  3.56 GBytes  30.8 Gbits/sec  0.010 ms  66587/124925 (53%)
[  5]   3.01-4.00   sec  3.55 GBytes  30.6 Gbits/sec  0.008 ms  71581/129761 (55%)
[  5]   4.00-5.01   sec  3.65 GBytes  31.0 Gbits/sec  0.011 ms  70070/129940 (54%)
[  5]   5.01-6.01   sec  3.52 GBytes  30.4 Gbits/sec  0.010 ms  67885/125564 (54%)
[  5]   6.01-7.00   sec  3.59 GBytes  31.0 Gbits/sec  0.007 ms  65266/124137 (53%)
[  5]   7.00-8.01   sec  3.58 GBytes  30.6 Gbits/sec  0.007 ms  69799/128461 (54%)
[  5]   8.01-9.01   sec  3.59 GBytes  30.7 Gbits/sec  0.007 ms  56935/115861 (49%)
[  5]   9.01-10.01  sec  3.37 GBytes  29.2 Gbits/sec  0.008 ms  37222/92417 (40%)
[  5]  10.01-10.01  sec  1.37 MBytes  26.0 Gbits/sec  0.009 ms  10/32 (31%)
- - - - - - - - - - - - - - - - - - - - - - - - -
[ ID] Interval           Transfer     Bitrate         Jitter    Lost/Total Datagrams
[  5]   0.00-10.01  sec  35.6 GBytes  30.6 Gbits/sec  0.009 ms  619388/1203254 (51%)  receiver
```

