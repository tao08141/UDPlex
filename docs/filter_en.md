# UDPlex Filter Component

## Overview
The Filter component is responsible for filtering and categorizing packets based on protocol characteristics, forwarding different types of packets to different target components. It can identify specific protocol formats and determine the forwarding path of packets based on matching results.

## Component Parameters

| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `filter`, indicates a filtering component |
| `tag` | Unique component identifier, used for reference in detour |
| `use_proto_detectors` | List of protocol detectors to use, specifies the names of protocol detectors to apply |
| `detour` | Forwarding path object, keys are detector names, values are lists of target component identifiers after successful matching |
| `detour_miss` | Forwarding path when no protocol matches, specifies a list of component identifiers |

## Configuration Example

```json
{
    "type": "filter",
    "tag": "protocol_filter",
    "use_proto_detectors": ["wireguard", "openvpn", "game_protocol"],
    "detour": {
        "wireguard": ["wg_forward"],
        "openvpn": ["ovpn_forward"],
        "game_protocol": ["game_forward"]
    },
    "detour_miss": ["default_forward"]
}
```

## How It Works

1. After receiving a packet, the Filter component uses the configured protocol detectors to inspect the packet
2. The detection process proceeds in the order specified in `use_proto_detectors`
3. When a packet matches a protocol detector:
   - The component forwards the packet to the target components corresponding to the protocol detector name in `detour`
   - If a protocol detector corresponds to multiple target components, the packet is forwarded in parallel to all targets
4. If the packet does not match any protocol detector:
   - The component forwards the packet to the target components specified in `detour_miss`
   - If `detour_miss` is not configured, the packet is discarded

## Use Cases

- Identifying different types of game protocols and forwarding them to appropriate processing components
- Distinguishing between VPN traffic and regular traffic for different handling
- Filtering out packets that do not conform to specific protocol formats
- Implementing protocol-based traffic distribution and load balancing