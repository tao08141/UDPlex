# UDPlex Load Balancer Component

## Feature Overview
- Real-time traffic statistics and monitoring
- Intelligent packet distribution based on traffic volume and load policies
- Support for multiple load balancing algorithms
- Dynamic adjustment of forwarding strategies
- Support for weight configuration

## Traffic Statistics Mechanism

The load balancer component uses a sliding window mechanism for traffic statistics:

1. **Sampling Mechanism**: Every 1s interval, the currently accumulated traffic data is stored as a sample
2. **Window Size**: The sliding window saves `window_size` samples
3. **Statistical Calculation**: `bps` and `pps` values are the average of all samples in the window
4. **Real-time Updates**: Packets are added to the current sample in real-time as they arrive, and averages are calculated during rule evaluation

**Example**:
- `window_size: 10` 
- The window saves 10 samples, each representing 1 second of traffic statistics
- `bps` and `pps` are the average bytes per second and packets per second over the past 10 seconds

## Configuration Parameters

### load_balancer Component Parameters
| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `load_balancer`, indicates a load balancing component |
| `tag` | Unique component identifier, used for reference in detour |
| `detour` | Forwarding path, specifies the component identifiers that receive data |
| `window_size` | Traffic statistics time window, e.g., 10, representing 10s |


### detour Configuration
| Parameter | Description |
|-----------|-------------|
| `rule` | Forwarding rule, see `Matching Rules` for details |
| `target` | Forwarding target tags, an array, can have multiple entries |


## Matching Rules

+ Built-in Variables

| Variable Name | Description |
|---------------|-------------|
| `seq` | Traffic packet sequence number |
| `bps` | Bytes in the statistics window |
| `pps` | Number of packets in the statistics window |
| `size` | Current packet size |

+ Supported Expressions

Matching rules support flexible expressions, and the final result of the expression is considered a match (condition is true) as long as it is not equal to zero. You can use various operators and condition combinations, such as:

- Comparison operations: `==`, `!=`, `<`, `<=`, `>`, `>=`
- Logical operations: `&&` (AND), `||` (OR), `!` (NOT)
- Modulo operation: `%` (modulo, used for implementing round-robin, load balancing)
- Other expressions: Can be combined with parentheses `()` to enhance expression complexity

### Traffic Balancing Examples

Using modulo operations to implement basic traffic balancing strategies, for example:

- Using `$seq % 2 == 0` and `$seq % 2 == 1` to implement alternating processing by odd and even numbers, achieving traffic balancing effects.
- Combining logical operations to build more complex distribution strategies, for example:

```
($seq % 2 == 0) || ($pps > 1000)
```

This means the condition is true when the packet sequence number is even, or when the number of packets in the statistics window exceeds 1000.

### Example Rule Format

```
# Only process traffic with odd sequence numbers or packet sizes exceeding 100 bytes
($seq % 2 == 1) || ($bps > 100)
```

```
# Route traffic based on current packet size: packets larger than 1000 bytes go to high-performance server
$size > 1000
```

**Notes**:
- The final result of the expression is considered true as long as it is not equal to 0.
- You can flexibly define traffic matching rules by combining various operators and built-in variables.
- Variables in the rules (such as `$seq`, `$bps`, `$pps`, `$size`) should be correctly declared and used in the configuration.

## Configuration Example

Below is a complete load balancing configuration example, showing how to distribute traffic among three servers:

```json
{
    "services": [
        {
            "type": "listen",
            "tag": "client_listen",
            "listen_addr": "0.0.0.0:5202",
            "timeout": 120,
            "detour": ["load_balancer"]
        },
        {
            "type": "load_balancer",
            "tag": "load_balancer",
            "sample_interval": "1s",
            "window_size": 10,
            "detour": [
                {
                    "rule": "$seq % 3 == 0",
                    "target": ["server1"]
                },
                {
                    "rule": "$seq % 3 == 1", 
                    "target": ["server2"]
                },
                {
                    "rule": "$seq % 3 == 2 || $pps > 1000",
                    "target": ["server3"]
                }
            ]
        },
        {
            "type": "forward",
            "tag": "server1",
            "forwarders": ["192.168.1.10:5201"],
            "detour": ["client_listen"]
        },
        {
            "type": "forward", 
            "tag": "server2",
            "forwarders": ["192.168.1.11:5201"],
            "detour": ["client_listen"]
        },
        {
            "type": "forward",
            "tag": "server3", 
            "forwarders": ["192.168.1.12:5201"],
            "detour": ["client_listen"]
        }
    ]
}
```

### Rule Explanation

The above configuration implements the following load balancing strategies:

1. **Round-Robin Distribution**: Based on modulo operations on packet sequence numbers, implementing basic round-robin load balancing
   - `$seq % 3 == 0`: Every 1st packet out of 3 goes to server1
   - `$seq % 3 == 1`: Every 2nd packet out of 3 goes to server2
   - `$seq % 3 == 2`: Every 3rd packet out of 3 goes to server3

2. **High Load Protection**: When PPS exceeds 1000, additional traffic is routed to server3
   - `$pps > 1000`: Condition is true when the number of packets in the statistics window exceeds 1000

3. **Statistics Window Configuration**:
   - `sample_interval: "1s"`: Traffic statistics are sampled once per second
   - `window_size: "10s"`: Statistics window is 10 seconds, statistics data is reset every 10 seconds
