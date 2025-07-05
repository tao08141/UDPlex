# API服务器文档

## 概述

API服务器提供RESTful API接口，用于监控和管理路由器组件的状态。服务器支持多种组件类型，包括监听组件、转发组件、TCP隧道组件和负载均衡组件。

## 配置

在配置文件中添加以下配置：
```json
{
  "api": {
    "enabled": true,
    "port": 8080,
    "host": "0.0.0.0"
  }
}
```

## API端点

### 1. 获取所有组件

**端点：** `GET /api/components`

**描述：** 获取所有已注册组件的列表

**响应格式：**
```
json
[
  {
    "tag": "component1",
    "type": "listen"
  },
  {
    "tag": "component2",
    "type": "forward"
  }
]
```
### 2. 获取指定组件信息

**端点：** `GET /api/components/{tag}`

**描述：** 根据标签获取特定组件的信息

**参数：**
- `tag` (路径参数): 组件标签

**响应格式：**
```
json
{
  "tag": "component1",
  "type": "listen"
}
```
### 3. 获取监听组件连接

**端点：** `GET /api/listen/{tag}`

**描述：** 获取指定监听组件的连接状态

**参数：**
- `tag` (路径参数): 组件标签

**响应格式：**
```
json
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
```
json
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
```
json
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
```
json
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
```
json
{
  "tag": "load_balancer",
  "bytes_per_sec": 1024000,
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
## 错误处理

API服务器使用标准HTTP状态码：

- `200 OK`: 成功
- `400 Bad Request`: 请求参数错误
- `404 Not Found`: 组件未找到
- `405 Method Not Allowed`: 不支持的HTTP方法
- `500 Internal Server Error`: 服务器内部错误
