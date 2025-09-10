# API服务器文档

## 概述

API服务器提供RESTful API接口，用于监控和管理路由器组件的状态。服务器支持多种组件类型，包括监听组件、转发组件、TCP隧道组件、负载均衡组件和过滤组件。

## 配置

在配置文件中添加以下配置：
```json
{
  "api": {
    "enabled": true,
    "port": 8080,
    "host": "0.0.0.0",
    "h5_files_path": "/path/to/h5/files"
  }
}
```

### 配置选项

- `enabled`: 是否启用API服务器
- `port`: API服务器监听的端口
- `host`: API服务器监听的主机地址
- `h5_files_path`: 包含H5文件的目录路径。配置后，API服务器将在"/h5/"端点提供该目录中的H5文件。

## API端点

### 1. 获取所有组件

**端点：** `GET /api/components`

**描述：** 获取所有已注册组件的列表，包含每个组件的基本信息和detour配置

**响应格式：**
```json
[
  {
    "tag": "client_listen",
    "type": "listen",
    "listen_addr": "0.0.0.0:5202",
    "timeout": 120,
    "replace_old_mapping": true,
    "detour": ["protocol_filter"]
  },
  {
    "tag": "protocol_filter",
    "type": "filter",
    "use_proto_detectors": ["wireguard", "openvpn"],
    "detour": {
      "wireguard": ["wg_forward"],
      "openvpn": ["ovpn_forward"]
    },
    "detour_miss": ["default_forward"]
  },
  {
    "tag": "load_balancer",
    "type": "load_balancer",
    "window_size": 10,
    "detour": [
      {
        "rule": "seq % 2 == 0",
        "targets": ["client_forward"]
      },
      {
        "rule": "seq % 2 == 1",
        "targets": ["client_forward"]
      }
    ]
  },
  {
    "tag": "client_forward",
    "type": "forward",
    "forwarders": ["127.0.0.1:5201"],
    "reconnect_interval": 5,
    "connection_check_time": 30,
    "send_keepalive": true,
    "detour": ["client_listen"]
  }
]
```

### 2. 获取指定组件信息

**端点：** `GET /api/components/{tag}`

**描述：** 根据标签获取特定组件的详细信息，包含完整的配置和detour信息

**参数：**
- `tag` (路径参数): 组件标签

**响应格式：**

**监听组件示例：**
```json
{
  "tag": "client_listen",
  "type": "listen",
  "listen_addr": "0.0.0.0:5202",
  "timeout": 120,
  "replace_old_mapping": true,
  "detour": ["protocol_filter"]
}
```

**过滤组件示例：**
```json
{
  "tag": "protocol_filter",
  "type": "filter",
  "use_proto_detectors": ["wireguard", "openvpn", "game_protocol"],
  "detour": {
    "wireguard": ["wg_forward"],
    "openvpn": ["ovpn_forward"],
    "game_protocol": ["game_forward"]
  },
  "detour_miss": ["default_forward"]
}
```

**负载均衡组件示例：**
```json
{
  "tag": "load_balancer",
  "type": "load_balancer",
  "window_size": 10,
  "detour": [
    {
      "rule": "seq % 2 == 0",
      "targets": ["client_forward"]
    },
    {
      "rule": "seq % 2 == 1",
      "targets": ["client_forward"]
    }
  ]
}
```

**转发组件示例：**
```json
{
  "tag": "client_forward",
  "type": "forward",
  "forwarders": ["127.0.0.1:5201"],
  "reconnect_interval": 5,
  "connection_check_time": 30,
  "send_keepalive": true,
  "detour": ["client_listen"]
}
```

### 3. 获取监听组件连接

**端点：** `GET /api/listen/{tag}`

**描述：** 获取指定监听组件的连接状态

**参数：**
- `tag` (路径参数): 组件标签

**响应格式：**
```json
{
  "tag": "listen_component",
  "listen_addr": "0.0.0.0:8080",
  "connections": [
    {
      "address": "192.168.1.100:12345",
      "last_active": "2023-12-01T10:30:00Z",
      "connection_id": "abc123",
      "is_authenticated": true
    }
  ],
  "count": 1
}
```

### 4. 获取转发组件连接

**端点：** `GET /api/forward/{tag}`

**描述：** 获取指定转发组件的连接状态

**参数：**
- `tag` (路径参数): 组件标签

**响应格式：**
```json
{
  "tag": "forward_component",
  "connections": [
    {
      "remote_addr": "192.168.1.200:8080",
      "is_connected": true,
      "last_reconnect": "2023-12-01T10:00:00Z",
      "auth_retry_count": 0,
      "heartbeat_miss": 0,
      "last_heartbeat": "2023-12-01T10:35:00Z",
      "is_authenticated": true
    }
  ],
  "count": 1
}
```

### 5. 获取TCP隧道监听连接

**端点：** `GET /api/tcp_tunnel_listen/{tag}`

**描述：** 获取指定TCP隧道监听组件的连接池状态

**参数：**
- `tag` (路径参数): 组件标签

**响应格式：**
```json
{
  "tag": "tcp_tunnel_listen",
  "listen_addr": "0.0.0.0:9090",
  "pools": [
    {
      "forward_id": "abc123",
      "pool_id": "def456",
      "remote_addr": "192.168.1.300:9090",
      "connections": [
        {
          "remote_addr": "192.168.1.300:54321",
          "is_authenticated": true,
          "last_active": "2023-12-01T10:40:00Z",
          "heartbeat_miss": 0,
          "last_heartbeat": "2023-12-01T10:40:30Z"
        }
      ],
      "conn_count": 1
    }
  ],
  "total_connections": 1
}
```

### 6. 获取TCP隧道转发连接

**端点：** `GET /api/tcp_tunnel_forward/{tag}`

**描述：** 获取指定TCP隧道转发组件的连接池状态

**参数：**
- `tag` (路径参数): 组件标签

**响应格式：**
```json
{
  "tag": "tcp_tunnel_forward",
  "forward_id": "abc123",
  "pools": [
    {
      "pool_id": "def456",
      "remote_addr": "192.168.1.400:9090",
      "connections": [
        {
          "remote_addr": "192.168.1.400:54321",
          "is_authenticated": true,
          "last_active": "2023-12-01T10:45:00Z",
          "heartbeat_miss": 0,
          "last_heartbeat": "2023-12-01T10:45:30Z"
        }
      ],
      "conn_count": 1,
      "target_count": 5
    }
  ],
  "total_connections": 1
}
```

### 7. 获取负载均衡器流量统计

**端点：** `GET /api/load_balancer/{tag}`

**描述：** 获取指定负载均衡器组件的流量统计信息

**参数：**
- `tag` (路径参数): 组件标签

**响应格式：**
```json
{
  "tag": "load_balancer",
  "bits_per_sec": 8192000,
  "packets_per_sec": 100,
  "total_bytes": 10240000,
  "total_packets": 1000,
  "current_bytes": 1024,
  "current_packets": 10,
  "samples": [
    {
      "bytes": 1024,
      "packets": 10
    }
  ],
  "window_size": 60
}
```
> **说明：** `bits_per_sec` 字段为比特每秒（bit/s），原 `bytes_per_sec` 字段已废弃。

### 8. 获取过滤器组件信息

**端点：** `GET /api/filter/{tag}`

**描述：** 获取指定过滤器组件的配置信息

**参数：**
- `tag` (路径参数): 组件标签

**响应格式：**
```json
{
  "tag": "protocol_filter",
  "type": "filter",
  "use_proto_detectors": ["wireguard", "openvpn", "game_protocol"],
  "detour": {
    "wireguard": ["wg_forward"],
    "openvpn": ["ovpn_forward"],
    "game_protocol": ["game_forward"]
  },
  "detour_miss": ["default_forward"]
}
```

### 9. 提供H5文件

**端点：** `GET /h5/{file_path}`

**描述：** 当配置了 `h5_files_path` 时，此端点提供指定目录中的H5文件。

**参数：**
- `file_path` (路径参数): 相对于配置的 `h5_files_path` 目录的H5文件路径

**响应：** 请求的H5文件内容，带有适当的内容类型。

## 组件detour配置说明

### detour字段类型

**简单字符串数组（适用于listen和forward组件）：**
```json
"detour": ["target_component"]
```

**协议映射对象（适用于filter组件）：**
```json
"detour": {
  "protocol_name": ["target_component1", "target_component2"],
  "another_protocol": ["target_component3"]
}
```

**负载均衡规则数组（适用于load_balancer组件）：**
```json
"detour": [
  {
    "rule": "seq % 2 == 0",
    "targets": ["component1"]
  },
  {
    "rule": "seq % 2 == 1", 
    "targets": ["component2"]
  }
]
```

### 组件类型特定字段

- **listen组件**: `listen_addr`, `timeout`, `replace_old_mapping`
- **forward组件**: `forwarders`, `reconnect_interval`, `connection_check_time`, `send_keepalive`
- **load_balancer组件**: `window_size`
- **filter组件**: `use_proto_detectors`, `detour_miss`
- **tcp_tunnel_listen组件**: `listen_addr`, `pools`
- **tcp_tunnel_forward组件**: `pools`, `target_count`

## 错误处理

API服务器使用标准HTTP状态码：

- `200 OK`: 成功
- `400 Bad Request`: 请求参数错误
- `404 Not Found`: 组件未找到
- `405 Method Not Allowed`: 不支持的HTTP方法
- `500 Internal Server Error`: 服务器内部错误

### 6. 获取 IP Router 信息

端点：GET /api/ip_router/{tag}

描述：获取指定 ip_router 组件的运行时信息，包括规则、detour_miss 以及 GeoIP 加载状态。

参数：
- tag（路径参数）：组件标签

响应示例：
```json
{
  "tag": "ipr",
  "type": "ip_router",
  "rules": [
    { "match": "192.168.1.100", "targets": ["special_forward"] },
    { "match": "192.168.1.0/21", "targets": ["lan_forward"] },
    { "match": "geo:CN", "targets": ["china_forward"] }
  ],
  "detour_miss": ["default_forward"],
  "geoip": {
    "db_loaded": true,
    "geoip_url": "https://example.com/GeoLite2-Country.mmdb",
    "geoip_path": "C:/Users/.../udplex_geoip_ipr.mmdb",
    "update_interval_sec": 86400
  }
}
```

### 7. 触发 IP Router 动作

端点：POST /api/ip_router_action/{tag}?action=geoip_update

描述：当配置了 geoip_url 时，手动触发 GeoIP 数据库下载并热更新指定的 ip_router 组件。

参数：
- tag（路径参数）：组件标签
- action（查询参数）：当前支持 "geoip_update"

响应：
- 200 OK，并返回 "ok" 表示成功
- 400/500：返回错误信息

