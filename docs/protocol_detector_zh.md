# UDPlex 协议检测器

## 功能概述
协议检测器用于识别和分类 UDP 数据包中的特定协议，通过配置的特征签名来匹配数据包内容。它可以检测各种协议，如游戏协议、VPN 协议等，并与 Filter 组件配合使用，实现基于协议的流量分流。

## 配置格式

```yaml
protocol_detectors:
  名称:
    signatures:
      - offset: 0
        bytes: 匹配字节序列
        mask: 应用的位掩码
        hex: true
        length:
          min: 20
          max: 1500
        contains: 包含的字节序列
        description: 特征描述
    match_logic: AND
    description: 协议检测器描述
```

**注意**：
- `bytes` 可以是十六进制或ASCII格式，由 `hex` 参数决定
- `match_logic` 可以是 "AND"（全部匹配）或 "OR"（任一匹配）

## 协议签名参数

| 参数 | 说明 |
|------|------|
| `offset` | 在数据包中开始匹配的字节偏移量 |
| `bytes` | 要匹配的字节序列，可以是十六进制或ASCII格式 |
| `mask` | 应用于匹配的位掩码，用十六进制表示 |
| `hex` | 指定bytes是否为十六进制格式，true为十六进制，false为ASCII |
| `length` | 数据包长度限制，包含min（最小长度）和max（最大长度） |
| `contains` | 数据包中必须包含的字节序列，用于进一步过滤 |
| `description` | 签名描述，说明此签名匹配的协议特征 |

## 协议检测器参数

| 参数 | 说明 |
|------|------|
| `signatures` | 协议签名数组，定义识别特定协议的特征 |
| `match_logic` | 多个签名间的逻辑关系，"AND"表示全部匹配，"OR"表示任一匹配 |
| `description` | 协议检测器描述 |

## 配置示例

```yaml
protocol_detectors:
  wireguard:
    signatures:
      - offset: 0
        bytes: "0400000000"
        hex: true
        length:
          min: 20
          max: 1500
        description: WireGuard 握手初始化包
    match_logic: OR
    description: WireGuard VPN 协议检测器
  game_protocol:
    signatures:
      - offset: 0
        bytes: "4741"
        hex: true
        description: 游戏协议头部标识
      - contains: GAME_SERVER
        description: 游戏服务器标识字符串
    match_logic: AND
    description: 特定游戏协议检测器
```

## 工作原理

1. 协议检测器接收数据包后，会根据配置的签名进行匹配
2. 对于每个签名，检测器会：
   - 检查数据包在指定偏移量处的字节是否与 `bytes` 匹配
   - 如果指定了 `mask`，会应用位掩码后再进行比较
   - 检查数据包长度是否在 `length` 指定的范围内
   - 检查数据包是否包含 `contains` 指定的字节序列
3. 根据 `match_logic` 确定最终匹配结果：
   - 如果是 "AND"，则所有签名都必须匹配才算成功
   - 如果是 "OR"，则任一签名匹配即算成功
4. 匹配成功的协议名称会被返回给 Filter 组件，用于决定数据包的转发路径

## 使用场景

- 识别特定游戏的数据包，进行针对性优化
- 区分不同类型的 VPN 协议流量
- 过滤掉不符合预期协议格式的数据包
- 实现基于协议的智能路由和负载均衡