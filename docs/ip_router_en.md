# ip_router component

Routes packets based on source IP/CIDR and GeoIP2 country code. First match wins; if none match, traffic is sent to detour_miss.

Key points
- Matches in order of rules
- Supported match formats:
  - Single IP: e.g. "192.168.1.10"
  - CIDR: e.g. "192.168.1.0/24" or any prefix like /21
  - Geo by country code: "geo:US", "geo:CN" (requires GeoIP2 database)
- GeoIP database: use a MaxMind MMDB file (e.g., GeoLite2-Country.mmdb). Provide its path via geoip_mmdb.
- When no rule matches or src IP cannot be determined, detour_miss is used.

Configuration
- type: ip_router
- tag: unique component tag
- rules: array of rules, each with fields
  - rule: match expression (IP/CIDR/geo:CC)
  - targets: detour component tags (array)
- detour_miss: default targets when no rules match
- geoip_mmdb: path to a local GeoIP2 database file (optional)
- geoip_url: http(s) URL to download GeoIP2 database. If provided (or if geoip_mmdb itself is an http/https URL), the file will be downloaded to a temp cache and loaded automatically.
- geoip_update_interval: background refresh interval for geoip_url. Accepts Go duration string (e.g., "24h", "6h30m") or a number of seconds. If omitted/zero, no periodic updates.

Example YAML
```yaml
buffer_size: 2000
queue_size: 8192
worker_count: 4

services:
  # UDP listener receiving client packets
  - type: listen
    tag: in
    listen_addr: 0.0.0.0:9000
    detour: [ ipr ]

  # IP router
  - type: ip_router
    tag: ipr
    # Either use a local file path or a URL. If both provided, geoip_url takes precedence.
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

  # Downstream forwards
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

Behavior
- The component inspects packet srcAddr set by the listen component to extract the source IP (supports UDPAddr and TCPAddr).
- It evaluates rules in order: IP match, CIDR containment, GeoIP country code (if DB loaded).
- On first match, it routes to the rule's targets. If no match or DB missing for geo, it falls back to detour_miss.

Notes
- IPv4 is supported; if your deployment carries IPv6, CIDR and single-IP rules with IPv6 literal should also parse (net.ParseIP/net.ParseCIDR). Ensure your listeners provide IPv6 addresses accordingly.
- For geo rules, provide a valid path accessible by the process. If geoip_mmdb is omitted or cannot be opened, geo rules will never match.
- Rule evaluation is stateless per packet; ordering matters.
- This component does not modify payload.
