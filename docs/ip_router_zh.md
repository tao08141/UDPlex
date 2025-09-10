# ip_router 组件

基于来源 IP/CIDR 与 GeoIP2 国家码进行路由。按规则顺序匹配，命中即跳转；都不命中则走 detour_miss。

要点
- 规则按顺序匹配，先匹配先生效
- 支持的匹配格式：
  - 单个 IP：例如 "192.168.1.10"
  - CIDR：例如 "192.168.1.0/24"，也支持任意前缀（如 /21）
  - 按国家码："geo:US"、"geo:CN"（需要 GeoIP2 数据库）
- GeoIP 数据库：使用 MaxMind 的 MMDB 文件（如 GeoLite2-Country.mmdb），通过 geoip_mmdb 指定路径
- 当无法获取源 IP 或无任何规则命中时，流量转发到 detour_miss

配置项
- type：ip_router
- tag：组件标签
- rules：规则数组，每条包含
  - rule：匹配表达式（IP/CIDR/geo:CC）
  - targets：目标组件标签数组
- detour_miss：未命中规则时的默认目标
- geoip_mmdb：本地 GeoIP2 MMDB 文件路径（可选）
- geoip_url：GeoIP2 数据库 http(s) 下载地址；若提供（或 geoip_mmdb 本身是 http/https URL），将自动下载到临时缓存并加载
- geoip_update_interval：从 geoip_url 后台定时刷新间隔；支持 Go 时长字符串（如 "24h"、"6h30m"）或纯秒数字。为 0/空则不自动更新

示例 YAML
```yaml
buffer_size: 2000
queue_size: 8192
worker_count: 4

services:
  # UDP 监听入口
  - type: listen
    tag: in
    listen_addr: 0.0.0.0:9000
    detour: [ ipr ]

  # 基于 IP/Geo 路由
  - type: ip_router
    tag: ipr
    # 可本地路径或 URL；若两者皆填，则以 geoip_url 优先
    geoip_url: https://example.com/GeoLite2-Country.mmdb
    geoip_update_interval: 24h
    detour_miss: [ default_forward ]
    rules:
      - rule: "192.168.1.100"
        targets: [ special_forward ]
      - rule: "192.168.1.0/21"
        targets: [ lan_forward ]
      - rule: "geo:CN"
        targets: [ china_forward ]

  # 下游转发
  - type: forward
    tag: special_forward
    forwarders: [ 203.0.113.10:9100 ]

  - type: forward
    tag: lan_forward
    forwarders: [ 10.0.0.10:9100 ]

  - type: forward
    tag: china_forward
    forwarders: [ 198.51.100.20:9100 ]

  - type: forward
    tag: default_forward
    forwarders: [ 192.0.2.30:9100 ]
```

工作原理
- 组件从 listen 组件设置的 packet.srcAddr 提取源 IP（支持 UDPAddr/TCPAddr）。
- 依序检查每条规则：单 IP 相等、CIDR 包含、GeoIP 国家码（需已加载 DB）。
- 首次命中即路由到对应 targets；若全部未命中（或缺少 GeoIP DB 导致 geo 规则无法命中），则走 detour_miss。

注意事项
- 默认支持 IPv4；若环境包含 IPv6，单 IP 与 CIDR 规则也可解析（依赖 net.ParseIP/net.ParseCIDR）。需确保监听组件能提供对应 IPv6 地址。
- 使用 geo 规则时，需要提供可访问的 MMDB 文件路径。未提供或打开失败时，所有 geo 规则都不会命中。
- 规则匹配是无状态、逐包进行的；顺序很重要。
- 组件不会修改负载数据。
