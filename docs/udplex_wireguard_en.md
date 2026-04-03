# UDPlex + Embedded WireGuard Deployment Guide

## What This Mode Does

UDPlex can embed `wireguard-go` directly inside the UDPlex process. In this mode:

- UDPlex still provides multi-line forwarding and load balancing
- WireGuard traffic is carried through UDPlex lines instead of relying on system `wg-quick`
- A WireGuard interface is still created automatically for the host
- Return traffic follows the incoming outer line correctly through `reuse_incoming_detour: true`

This is intended for scenarios such as:

- game acceleration
- multi-line UDP redundancy
- low-loss forwarding across unstable public networks

## Topology

```text
Application
  -> WireGuard interface on Entry
  -> Embedded WireGuard in UDPlex
  -> UDPlex outer line #1 / outer line #2
  -> UDPlex on Exit
  -> Embedded WireGuard on Exit
  -> WireGuard interface on Exit
  -> Application / target network
```

At low bandwidth, the generated config sends the same packet on both outer lines.
At higher bandwidth, it can either split packets by sequence across both lines or keep all traffic on a single preferred line.

Each outer line can be configured independently as UDP or TCP.
For example, line #1 can use UDP while line #2 uses TCP.

## Requirements

- 1 Entry host close to the player/client
- 1 Exit host close to the target/game server
- At least 2 working outer network paths from Entry to Exit
- Linux
- `sudo`
- public internet access

The install script will install:

- Docker
- Docker Compose plugin if needed
- `wireguard-tools` if needed

Note:
`wireguard-tools` is only used for key generation and inspection convenience.
The runtime data path uses UDPlex with embedded WireGuard, not system `wg-quick`.

## Quick Start

Run on both Entry and Exit:

```bash
curl -fsSL -o udplex-wg-manager.sh https://raw.githubusercontent.com/tao08141/UDPlex/master/udplex-wg-manager.sh
chmod +x udplex-wg-manager.sh
sudo bash ./udplex-wg-manager.sh install
```

During install, the script will:

- show the local WireGuard public key
- ask for language
- ask for the UDPlex shared secret, or generate one automatically
- ask for role: Entry or Exit
- ask for the peer public key
- ask the outer protocol for line #1 and line #2 independently
- ask how high-bandwidth traffic should be handled
- generate `/opt/udplex/config.yaml`
- generate `/opt/udplex/docker-compose.yml`

## Files Written By The Script

- `/opt/udplex/config.yaml`
- `/opt/udplex/docker-compose.yml`
- `/opt/udplex/role`
- `/opt/udplex/secret`
- `/opt/udplex/threshold`
- `/opt/udplex/lang`
- `/opt/udplex/wireguard/wg_private.key`
- `/opt/udplex/wireguard/wg_public.key`

Legacy behavior:

- older versions stored keys in `/etc/wireguard`
- the current script migrates legacy keys automatically to `/opt/udplex/wireguard`

No system `wg0.conf` is generated anymore.

## Start

```bash
sudo bash ./udplex-wg-manager.sh start
```

This starts the UDPlex container and brings up the embedded WireGuard interface.

The generated interface name is:

```text
wg_udplex
```

## Common Commands

```bash
sudo bash ./udplex-wg-manager.sh status
sudo bash ./udplex-wg-manager.sh logs
sudo bash ./udplex-wg-manager.sh stop
sudo bash ./udplex-wg-manager.sh pause
sudo bash ./udplex-wg-manager.sh resume
sudo bash ./udplex-wg-manager.sh update
sudo bash ./udplex-wg-manager.sh reload
sudo bash ./udplex-wg-manager.sh show-keys
sudo bash ./udplex-wg-manager.sh lang en
sudo bash ./udplex-wg-manager.sh set-threshold 80000000
```

`pause` and `resume` operate on the embedded interface `wg_udplex`.

## Threshold And Redundancy

The generated config uses a load balancer with two behaviors:

- when bandwidth is below the threshold:
  send on both lines for redundancy
- when bandwidth is above the threshold:
  either split by packet sequence across the two lines, or stay on one preferred line

Update threshold:

```bash
sudo bash ./udplex-wg-manager.sh set-threshold 80000000
sudo bash ./udplex-wg-manager.sh reload
```

## Ports

If you keep the defaults:

- Exit line #1: `9000/udp`
- Exit line #2: `9001/udp`

The manager now lets you choose the transport for each outer line independently:

- line #1 can be UDP or TCP
- line #2 can be UDP or TCP

Examples:

- `9000/udp` + `9001/udp`
- `9000/udp` + `9001/tcp`
- `9000/tcp` + `9001/udp`
- `9000/tcp` + `9001/tcp`

Important:

- the manager no longer relies on a system WireGuard service port prompt for runtime startup
- embedded WireGuard is configured inside `config.yaml`
- outer forwarding still depends on the UDPlex line ports you configure

## Verification

```bash
sudo bash ./udplex-wg-manager.sh status
sudo wg show
ip addr show wg_udplex
```

If you use the default inner addresses:

```bash
ping 10.0.0.2
```

## Troubleshooting

Container logs:

```bash
sudo bash ./udplex-wg-manager.sh logs
```

WireGuard state:

```bash
sudo wg show
ip addr show wg_udplex
```

Outer line ports:

```bash
ss -lunpt | grep -E ":(9000|9001)\b" || true
ss -lntp | grep -E ":(9000|9001)\b" || true
```

If replies do not come back correctly on the outer lines, check:

- `reuse_incoming_detour: true`
- `broadcast_mode: false` on outer `listen`
- whether both outer lines are actually authenticated and available
- whether the correct ports are open in the firewall

## Uninstall

```bash
sudo bash ./udplex-wg-manager.sh uninstall
```

If you choose not to delete keys, the script backs them up before removing `/opt/udplex`.

## Security Notes

- the UDPlex auth secret must match on both sides
- only open the required outer ports
- rotate keys and secrets when needed
- update the container image periodically
