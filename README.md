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

### listen 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `listen`，表示监听组件 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `listen_addr` | 监听地址和端口，格式为"IP:端口"，如"0.0.0.0:9000" |
| `timeout` | 连接超时时间（秒），超过此时间无数据传输则清除映射 |
| `replace_old_mapping` | 是否替换旧映射，当为true时新映射会替换同地址的旧映射 |
| `detour` | 转发路径，指定接收数据的组件标识列表 |
| `auth` | 鉴权配置，详见鉴权部分 |

### forward 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `forward`，表示转发组件 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `forwarders` | 转发目标地址列表，可配置多个目标进行并行转发 |
| `reconnect_interval` | 重连间隔时间（秒），断开连接后尝试重连的等待时间 |
| `connection_check_time` | 连接检查间隔（秒），定期检查连接状态的时间间隔 |
| `send_keepalive` | 是否发送空数据包作为心跳包来保持连接活跃 |
| `detour` | 转发路径，指定接收返回数据的组件标识列表 |
| `auth` | 鉴权配置，详见鉴权部分 |
 
### filter 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `filter`，表示过滤组件 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `use_proto_detectors` | 使用的协议检测器列表，指定要应用的协议检测器名称 |
| `detour` | 转发路径对象，键为检测器名称，值为匹配成功后的目标组件标识列表 |
| `detour_miss` | 未匹配任何协议时的转发路径，指定组件标识列表 |

### tcp_tunnel_listen 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `tcp_tunnel_listen`，表示TCP隧道监听端 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `listen_addr` | 监听地址和端口，格式为"IP:端口"，如"0.0.0.0:9001" |
| `timeout` | 连接超时时间（秒），超过此时间无数据传输则断开连接 |
| `detour` | 转发路径，指定接收数据的组件标识列表 |
| `auth` | 鉴权配置，详见鉴权部分 |

### tcp_tunnel_forward 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `tcp_tunnel_forward`，表示TCP隧道转发端 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `forwarders` | 目标服务器地址列表，格式为"IP:端口[:连接数]"，如"1.2.3.4:9001:4" |
| `connection_check_time` | 连接检查间隔（秒），定期检查并重连断开的连接 |
| `detour` | 转发路径，指定接收返回数据的组件标识列表 |
| `auth` | 鉴权配置，详见鉴权部分 |


### auth 鉴权配置参数

| 参数                | 说明                                                                 |
|---------------------|----------------------------------------------------------------------|
| `enabled`           | 是否启用鉴权，布尔值，true 表示启用                                   |
| `secret`            | 鉴权密钥，字符串，客户端与服务端需保持一致                            |
| `enable_encryption` | 是否启用数据加密（AES-GCM），true 表示加密传输                        |
| `heartbeat_interval`| 心跳包发送间隔（秒），用于连接保活，默认30秒                          |

#### 说明

- `auth` 配置用于数据包鉴权与加密。
- 启用后，连接建立时会进行握手认证，认证通过后才允许数据转发。
- 若 `enable_encryption` 为 true，所有数据包内容将使用 AES-GCM 加密，提升安全性。
- `heartbeat_interval` 控制心跳包频率，防止长时间无数据导致连接断开。

#### 示例

```json
"auth": {
    "enabled": true,
    "secret": "your-strong-password",
    "enable_encryption": true,
    "heartbeat_interval": 30
}
```

> 注意：`secret` 必须在客户端和服务端保持一致，否则无法通过认证。


## 协议检测器配置

```json
"protocol_detectors": {
    "名称": {
        "signatures": [
            {
                "offset": 数据包偏移量,
                "bytes": "匹配字节序列",
                "mask": "应用的位掩码",
                "hex": true/false,  // bytes是否为十六进制格式
                "length": { "min": 最小长度, "max": 最大长度 },
                "contains": "包含的字节序列",
                "description": "特征描述"
            }
        ],
        "match_logic": "AND/OR",  // 多个签名的匹配逻辑
        "description": "协议检测器描述"
    }
}
```

### 协议签名参数

| 参数 | 说明 |
|------|------|
| `offset` | 在数据包中开始匹配的字节偏移量 |
| `bytes` | 要匹配的字节序列，可以是十六进制或ASCII格式 |
| `mask` | 应用于匹配的位掩码，用十六进制表示 |
| `hex` | 指定bytes是否为十六进制格式，true为十六进制，false为ASCII |
| `length` | 数据包长度限制，包含min（最小长度）和max（最大长度） |
| `contains` | 数据包中必须包含的字节序列，用于进一步过滤 |
| `description` | 签名描述，说明此签名匹配的协议特征 |

### 协议检测器参数

| 参数 | 说明 |
|------|------|
| `signatures` | 协议签名数组，定义识别特定协议的特征 |
| `match_logic` | 多个签名间的逻辑关系，"AND"表示全部匹配，"OR"表示任一匹配 |
| `description` | 协议检测器描述 |



## 开发计划
- [X] 支持包过滤和选择性转发
- [X] 支持鉴权、加密、去重等功能
- [ ] 支持更复杂的负载均衡算法
- [X] 支持UDP Over TCP的转发

## 使用场景
- 游戏加速：将游戏流量同时转发到多个服务器，选择最快的响应
- 网络冗余：确保重要的 UDP 数据能通过多条路径传输
- 流量分流：将 UDP 流量复制到多个目标进行处理

## 注意事项

- 确保监听端口在防火墙中已开放
- 转发目标服务器需要能够正确处理转发来的 UDP 数据包
- 对于高流量场景，请适当调整 `buffer_size` 和 `queue_size` 参数
- 仅支持 UDP 协议，不支持 TCP



## 配置示例

examples目录包含多种使用场景的配置示例：

- **basic.json** - UDP转发的基本配置示例
- **auth_client.json** - 带鉴权的UDP客户端配置
- **auth_server.json** - 带鉴权的UDP服务端配置
- **redundant_client_config.json** - UDP冗余客户端配置，将流量同时发送到多个服务器
- **redundant_server_config.json** - UDP冗余服务端配置，接收客户端流量并转发
- **wg_bidirectional_client_config.json** - WireGuard UDP上下行分离通信客户端配置
- **wg_bidirectional_server_config.json** - WireGuard UDP上下行分离通信服务端配置


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

