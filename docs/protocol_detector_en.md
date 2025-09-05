# UDPlex Protocol Detector

## Overview
Protocol detectors are used to identify and categorize specific protocols in UDP packets by matching packet content against configured signature patterns. They can detect various protocols, such as game protocols, VPN protocols, etc., and work with the Filter component to implement protocol-based traffic distribution.

## Configuration Format

```yaml
protocol_detectors:
  name:
    signatures:
      - offset: 0
        bytes: matching_byte_sequence
        mask: applied_bit_mask
        hex: true
        length:
          min: 20
          max: 1500
        contains: contained_byte_sequence
        description: signature_description
    match_logic: AND
    description: protocol_detector_description
```

**Note**:
- `bytes` can be in hexadecimal or ASCII format, determined by the `hex` parameter
- `match_logic` can be "AND" (all must match) or "OR" (any can match)

## Protocol Signature Parameters

| Parameter | Description |
|-----------|-------------|
| `offset` | Byte offset to start matching in the packet |
| `bytes` | Byte sequence to match, can be in hexadecimal or ASCII format |
| `mask` | Bit mask applied to the match, represented in hexadecimal |
| `hex` | Specifies whether bytes is in hexadecimal format, true for hexadecimal, false for ASCII |
| `length` | Packet length limit, including min (minimum length) and max (maximum length) |
| `contains` | Byte sequence that must be contained in the packet, for further filtering |
| `description` | Signature description, explains the protocol feature this signature matches |

## Protocol Detector Parameters

| Parameter | Description |
|-----------|-------------|
| `signatures` | Array of protocol signatures, defines features for identifying specific protocols |
| `match_logic` | Logical relationship between multiple signatures, "AND" means all must match, "OR" means any can match |
| `description` | Protocol detector description |

## Configuration Example

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
        description: WireGuard handshake initialization packet
    match_logic: OR
    description: WireGuard VPN protocol detector
  game_protocol:
    signatures:
      - offset: 0
        bytes: "4741"
        hex: true
        description: Game protocol header identifier
      - contains: GAME_SERVER
        description: Game server identifier string
    match_logic: AND
    description: Specific game protocol detector
```

## How It Works

1. After receiving a packet, the protocol detector matches it against the configured signatures
2. For each signature, the detector:
   - Checks if the bytes at the specified offset in the packet match the `bytes` value
   - If a `mask` is specified, applies the bit mask before comparison
   - Checks if the packet length is within the range specified by `length`
   - Checks if the packet contains the byte sequence specified by `contains`
3. Determines the final match result based on `match_logic`:
   - If "AND", all signatures must match for a successful result
   - If "OR", any matching signature results in a successful result
4. The name of the successfully matched protocol is returned to the Filter component to determine the packet's forwarding path

## Use Cases

- Identifying packets from specific games for targeted optimization
- Distinguishing between different types of VPN protocol traffic
- Filtering out packets that do not conform to expected protocol formats
- Implementing protocol-based intelligent routing and load balancing