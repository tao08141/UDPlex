# UDPlex Filter 组件

## 功能概述
Filter 组件负责根据协议特征对数据包进行过滤和分类，将不同类型的数据包转发到不同的目标组件。它可以识别特定的协议格式，并根据匹配结果决定数据包的转发路径。

## 组件参数

| 参数 | 说明 |
|------|------|
| `type` | 组件类型: `filter`，表示过滤组件 |
| `tag` | 组件唯一标识，用于在detour中引用 |
| `use_proto_detectors` | 使用的协议检测器列表，指定要应用的协议检测器名称 |
| `detour` | 转发路径对象，键为检测器名称，值为匹配成功后的目标组件标识列表 |
| `detour_miss` | 未匹配任何协议时的转发路径，指定组件标识列表 |

## 配置示例

```yaml
type: filter
tag: protocol_filter
use_proto_detectors: [wireguard, openvpn, game_protocol]
detour:
  wireguard: [wg_forward]
  openvpn: [ovpn_forward]
  game_protocol: [game_forward]
detour_miss: [default_forward]
```

## 工作原理

1. Filter 组件接收数据包后，会使用配置的协议检测器对数据包进行检测
2. 检测过程会按照 `use_proto_detectors` 中指定的顺序依次进行
3. 当数据包匹配某个协议检测器时：
   - 组件会将数据包转发到 `detour` 中对应协议检测器名称的目标组件
   - 如果一个协议检测器对应多个目标组件，数据包会被并行转发到所有目标
4. 如果数据包未匹配任何协议检测器：
   - 组件会将数据包转发到 `detour_miss` 中指定的目标组件
   - 如果未配置 `detour_miss`，则数据包会被丢弃

## 使用场景

- 识别不同类型的游戏协议，并转发到相应的处理组件
- 区分 VPN 流量和普通流量，进行不同的处理
- 过滤掉不符合特定协议格式的数据包
- 实现基于协议的流量分流和负载均衡