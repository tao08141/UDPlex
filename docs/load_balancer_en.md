# UDPlex Load Balancer Component

## Feature Overview
- Real-time traffic statistics and monitoring
- Intelligent packet distribution based on traffic volume and load policies
- Support for multiple load balancing algorithms
- Dynamic adjustment of forwarding strategies
- Support for weight configuration
- Component availability checking for dynamic routing based on component status

## Traffic Statistics Mechanism

The load balancer component uses a sliding window mechanism for traffic statistics:

1. **Sampling Mechanism**: Every 1s interval, the currently accumulated traffic data is stored as a sample
2. **Window Size**: The sliding window saves `window_size` samples
3. **Statistical Calculation**: `bps` and `pps` values are the average of all samples in the window
4. **Real-time Updates**: Packets are added to the current sample in real-time as they arrive, and averages are calculated during rule evaluation

**Example**:
- `window_size: 10` 
- The window saves 10 samples, each representing 1 second of traffic statistics
- `bps` and `pps` are the average bits per second and packets per second over the past 10 seconds

## Configuration Parameters

### load_balancer Component Parameters
| Parameter | Description |
|-----------|-------------|
| `type` | Component type: `load_balancer`, indicates a load balancing component |
| `tag` | Unique component identifier, used for reference in detour |
| `detour` | Forwarding path, specifies the component identifiers that receive data |
| `window_size` | Traffic statistics time window, e.g., 10, representing 10s |


### detour Configuration
| Parameter | Description                                                  |
|-----------|--------------------------------------------------------------|
| `rule`    | Forwarding rule, see `Matching Rules` for details            |
| `targets` | Forwarding targets tags, an array, can have multiple entries |


## Matching Rules

### Expression Engine

Matching rules for the load balancer use the [expr-lang/expr](https://github.com/expr-lang/expr) engine for expression evaluation. This allows you to define flexible and complex forwarding policies using customizable logical expressions.

- **Built-in Variables**

| Variable Name | Description                              |
|---------------|------------------------------------------|
| `seq`         | Packet sequence number                   |
| `bps`         | Bits per second, averaged over window    |
| `pps`         | Packets per second, averaged over window |
| `size`        | Current packet size                      |
| `available_<tag>` | Component availability status, where tag is the component identifier |
| `delay_<tag>` | Average delay (milliseconds) of the current route for the corresponding component, where tag is the component identifier |

- **Supported Operators**

You can use all standard operators supported by expr, including but not limited to:

| Operator    | Description                  | Example                 |
|-------------|------------------------------|-------------------------|
| `==`        | Equal                        | `seq == 1`              |
| `!=`        | Not equal                    | `bps != 0`              |
| `< > <= >=` | Comparison               | `size > 1000`           |
| `&&`        | Logical AND                  | `seq > 100 && pps>10`   |
| `\|\|`      | Logical OR                   | `bps > 0 \|\| pps > 10` |
| `!`         | Logical NOT                  | `! (seq % 2 == 0)`      |
| `%`         | Modulo (remainder)           | `seq % 2 == 0`          |
| `()`        | Parentheses for grouping     | `(seq > 10) && ...`     |

#### Built-in Functions

In addition to operators, you can also use standard functions provided by expr if enabled in your runtime (check with your system whether additional functions are registered).

#### Rule Evaluation

If the final result of the expression is not equal to zero or false, the target is selected.

### Example
```
expr
# Forward if packet sequence number is odd or average bps > 100
(seq % 2 == 1) || (bps > 100)
```

```
expr
# Forward large packets to a high-performance server
size > 1000
```
- You may use any combination of logical operations, comparisons, and arithmetic supported by expr.
- Parentheses can be used to compose more complex selection logic.

#### Note
- Expressions are parsed and evaluated using expr, so all valid syntax from expr is supported, including chained comparisons and composite logical expressions.
- Built-in variables must exist in context to be referenced successfully.
- If a variable is missing or a function is not supported, there will be an evaluation error; please check your runtime environment.
- For full expr syntax, see the [expr official documentation](https://github.com/expr-lang/expr#language-definition).

## Availability Checking Feature

UDPlex supports component availability checking in the LoadBalancerComponent. This allows you to use component availability status in load balancing expressions to make routing decisions.

### Components Supporting Availability and Delay Checking


The following components support availability checking:

- TcpTunnelComponent
- TcpTunnelForwardComponent
- ListenComponent
- ForwardConn

For components that do not support availability checking, the availability variable will uniformly return "available" (true).

### Using Availability Variables in Expressions

In LoadBalancerComponent rule expressions, you can use variables in the format `available_<tag>` to check component availability, where `tag` is the component identifier.

For example:

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

In this example:
- If the `client_forward` component is available, packets will be routed to that component
- If `client_forward` is unavailable but `backup_listen` is available, packets will be routed to `backup_listen`
- If both are unavailable, packets will be routed to `fallback_forward`

### Implementation of Availability Checking

Each component that supports availability checking implements the `IsAvailable()` method, which returns a boolean value indicating whether the component is available:

- **TcpTunnelForwardComponent**: Returns true if at least one valid connection exists
- **TcpTunnelListenComponent**: Returns true if the listener is started and there are established connections
- **ListenComponent**: Returns true if the listener is started and there are established connections
- **ForwardConn**: Returns true if the connection is established
- **ForwardComponent**: Returns true if at least one ForwardConn is available

For components that do not support availability checking, the LoadBalancerComponent will default to returning true (available).

### Example Configuration

See the `examples/load_balancer_test.yaml` file to learn how to use the availability checking feature in your configuration.
