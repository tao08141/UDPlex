# UDPlex 负载均衡组件

## 功能概述
- 实时流量统计和监控
- 根据流量大小和负载策略智能分发数据包
- 支持多种负载均衡算法
- 动态调整转发策略
- 支持权重配置
- 支持组件可用性检查，根据组件状态动态调整路由

## 流量统计机制

负载均衡组件使用滑动窗口机制进行流量统计：

1. **采样机制**：每隔 1s 时间间隔，将当前累积的流量数据作为一个样本存储
2. **窗口大小**：滑动窗口保存 `window_size` 个样本
3. **统计计算**：`bps` 和 `pps` 值为窗口内所有样本的平均值
4. **实时更新**：数据包到达时实时累加到当前样本，规则评估时计算平均值

**示例**：
- `window_size: 10` 
- 窗口保存10个样本，每个样本代表1秒的流量统计
- `bps` 和 `pps` 为过去10秒的平均每秒比特数和包数

## 配置参数

### load_balancer 组件参数
| 参数 | 说明                             |
|------|--------------------------------|
| `type` | 组件类型: `load_balancer`，表示负载均衡组件 |
| `tag` | 组件唯一标识，用于在detour中引用            |
| `detour` | 转发路径，指定接收数据的组件标识列表             |
| `window_size` | 流量统计时间窗口，如10，代表10s             |
| `miss`        | 可选，当所有规则都不匹配时使用的目标组件tag列表 |
| `batch_decision` | 可选，默认 `false`。每个接收批次只计算一次规则，而不是每个包计算一次，详见[批量决策](#批量决策) |


### detour配置
| 参数        | 说明              |
|-----------|-----------------|
| `rule`    | 转发规则，详情参考`匹配规则` |
| `targets` | 转发目标tag，数组，可多个  |


## 匹配规则

### 表达式引擎

负载均衡的转发规则使用 [expr-lang/expr](https://github.com/expr-lang/expr) 作为表达式计算引擎，你可以通过灵活强大的表达式自定义流量分发策略。

- **内置变量**

| 变量名  | 说明                    |
|---------|-------------------------|
| `seq`   | 流量包序列号            |
| `bps`   | 窗口内平均每秒比特数     |
| `pps`   | 窗口内平均每秒包数       |
| `size`  | 当前包大小              |
| `available_<tag>` | 组件可用性状态，其中tag为组件标识 |
| `delay_<tag>` | 对应组件当前线路的“平均延迟”（毫秒），其中tag为组件标识 |
| `qdelay_<tag>` | 组件 [shaper](shaper_zh.md) 当前的排队时延（毫秒），即此刻发出的包在 UDPlex 内要等多久。未启用 shaper 时为 0 |

- **支持的运算符**

你可以使用 expr 标准支持的全部运算符，包括但不限于：

| 运算符    | 说明             | 示例                      |
|--------|----------------|---------------------------|
| `==`   | 等于             | `seq == 1`                |
| `!=`   | 不等于            | `bps != 0`                |
| `< > <= >=` | 大小比较           | `size > 1000`             |
| `&&`   | 逻辑与            | `seq > 100 && pps>10`     |
| `\|\|` | 逻辑或            | `bps > 0 \|\| pps > 10`     |
| `!`    | 逻辑非            | `! (seq % 2 == 0)`        |
| `%`    | 求余数(模运算)       | `seq % 2 == 0`            |
| `()`   | 括号，可用于复合逻辑分组   | `(seq > 10) && ...`       |

#### 内置函数支持

除上述操作符外，expr 支持的标准函数（如 `len()` 等）在本系统允许时同样可用，具体取决于运行环境是否开放或注册。

#### 规则成立条件

表达式的最终计算值只要不是“0”或`false`，即视为匹配成立，当前 detour 目标会被选中。

### 示例
```
expr
# 包序列号为奇数或bps统计大于100时转发
(seq % 2 == 1) || (bps > 100)
```

```
expr
# 把大包转到高性能服务器
size > 1000
```
- 运算符和逻辑可任意组合，括号用于实现更复杂的自定义逻辑。
- 可以用任何 expr 支持的算术、逻辑运算，以及表达式嵌套。

#### 注意事项

- 所有表达式均基于 [expr](https://github.com/expr-lang/expr#language-definition) 语法；
- 变量必须有效，否则计算会报错；
- 如引用未声明变量或禁用函数，将抛出表达式错误，建议在调试或配置时先做校验。
- 若需更复杂的语法，请查阅 [expr 官方文档](https://github.com/expr-lang/expr#language-definition)。

## 可用性检查功能

UDPlex 支持在 LoadBalancerComponent 中进行组件可用性检查。这允许在负载均衡表达式中使用组件的可用性状态来做出路由决策。

### 支持可用性、延迟检查的组件

以下组件支持可用性检查：

- TcpTunnelComponent
- TcpTunnelForwardComponent
- ListenComponent
- ForwardConn

对于不支持可用性检查的组件，可用性变量将统一返回"可用"（true）。

### 在表达式中使用可用性变量

在 LoadBalancerComponent 的规则表达式中，可以使用 `available_<tag>` 格式的变量来检查组件的可用性，其中 `tag` 是组件的标签。

例如：

```yaml
type: load_balancer
tag: load_balancer
detour:
  - rule: available_client_forward
    targets:
      - client_forward
  - rule: available_backup_listen
    targets:
      - backup_listen
  - rule: '!available_client_forward && !available_backup_listen'
    targets:
      - fallback_forward
```

在这个例子中：
- 如果 `client_forward` 组件可用，数据包将被路由到该组件
- 如果 `client_forward` 不可用但 `backup_listen` 可用，数据包将被路由到 `backup_listen`
- 如果两者都不可用，数据包将被路由到 `fallback_forward`

### 可用性检查的实现

每个支持可用性检查的组件都实现了 `IsAvailable()` 方法，该方法返回一个布尔值，指示组件是否可用：

- **TcpTunnelForwardComponent**: 如果至少有一个有效的连接，则返回 true
- **TcpTunnelListenComponent**: 如果监听器已启动且有已建立的连接，则返回 true
- **ListenComponent**: 如果监听器已启动且有已建立的连接，则返回 true
- **ForwardConn**: 如果连接已建立，则返回 true
- **ForwardComponent**: 如果至少有一个可用的 ForwardConn，则返回 true

对于不支持可用性检查的组件，LoadBalancerComponent 将默认返回 true（可用）。

### 示例配置

查看 `examples/load_balancer_test.yaml` 文件，了解如何在配置中使用可用性检查功能。

## 按排队时延切换线路

线路启用 [shaper](shaper_zh.md) 后，`qdelay_<tag>` 反映每条线路此刻的繁忙程度：现在发出的包要在该线路的 shaper 队列里等多久。它和 `delay_<tag>`（心跳往返的平均值，几秒更新一次）不同，能在一毫秒内反映变化。

把流量发往更空闲的线路，并留一点余量避免来回切换：

```yaml
type: load_balancer
tag: load_balancer
batch_decision: true
detour:
  - rule: "qdelay_line_a <= qdelay_line_b + 2 && available_line_a"
    targets: [line_a]
  - rule: "qdelay_line_a > qdelay_line_b + 2 || !available_line_a"
    targets: [line_b]
```

启用 shaper 后，空闲线路的排队时延为 0，所以流量会一直走 `line_a`，直到它跑满，才溢出到 `line_b`。

实测（WireGuard 经两条线路，每条限速上行 20 Mbit/s、下行 100 Mbit/s）：使用上面的规则，上行跑到 36 Mbit/s、下行 182 Mbit/s，单条线路为 18 和 91；类游戏流的延迟全程保持在空闲水平（约 20.5 ms）。

## Miss 机制

负载均衡器支持 `miss` 配置，用于定义当所有 `detour` 规则都不匹配当前数据包时的回退目标。

### 配置示例

```yaml
type: load_balancer
tag: lb
detour:
  - rule: seq > 100
    targets: [target1]
miss: [target2]
```

在此示例中：
- 如果数据包序列号 > 100，则转发到 `target1`。
- 否则（如果规则不匹配），转发到 `target2`。
- 如果未配置 `miss` 且没有规则匹配，数据包将被通过（或丢弃，具体取决于现有逻辑，通常在 LB 上下文中意味着不进行特定的 LB 转发）。

## 批量决策

设置 `batch_decision: true` 后，同一批收到的数据包（`listen`/`forward` 套接字的一次 `recvmmsg`/GRO 读取，或 `wg` 组件的一次批量发送）都沿用该批第一个包的决策，`seq` 也改为每批递增一次，而不是每个包递增一次。

这主要影响 `seq % 2 == 0` 这类在高带宽时把流量分到两条线路的规则。逐包轮换会把每个突发拆散到两条线路上，接收端看到大量乱序，导致隧道内 TCP 的 GRO 失效并产生虚假重传。按批决策时，一个突发完整、有序地走同一条线路，下一个突发再走另一条线路，整体仍然分摊到两条线路。

游戏等低速率流量通常一批只有一个包，行为与之前完全一致。发往多个目标的规则（冗余模式）不受影响，每个包仍会发送到全部目标。
