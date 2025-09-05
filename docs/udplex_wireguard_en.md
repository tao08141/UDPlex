# UDPlex + WireGuard One-click Deployment Guide

## Why UDPlex accelerates games:

1. Multi-path redundant transmission: send over multiple paths simultaneously; if one path drops or delays, others still carry packets
2. Lower packet loss: redundancy ensures delivery as long as one path succeeds
3. Route/path optimization: avoid congested public routes via better paths
4. Real-time failover: automatically switch when a path has issues

## Topology

```
Game client -> WireGuard(Client) -> UDPlex Client -> Line 1 -> UDPlex Server -> WireGuard(Server) -> Game server
                                              -> Line 2 ->
```

## Requirements

- 1 Entry host (close to the player)
- 1 Exit host (close to the game server)
- At least 2 working network paths from entry to exit (ideally different ISPs/paths)
- Docker environment (the script will install Docker and compose if missing)
- sudo privileges and public internet access on both

## Quick install and initialization

Run on both Entry and Exit machines. The script will:
- Install Docker and docker compose if missing
- Install WireGuard if missing
- Generate WireGuard keys and show your local public key
- Interactively write configs to /opt/udplex and /etc/wireguard

1) Download the script

```bash
curl -fsSL -o udplex-wg-manager.sh https://raw.githubusercontent.com/tao08141/UDPlex/master/udplex-wg-manager.sh
chmod +x udplex-wg-manager.sh
```

2) Start installation (open one terminal on each side and proceed step-by-step)

```bash
sudo bash ./udplex-wg-manager.sh install
```

Install flow highlights:
- The script first prints your local WireGuard public key. Run the installer on both sides and exchange public keys.
- Choose language (English/中文)
- Set bandwidth threshold in bps (default 50,000,000 = 50 Mbps, used by smart split)
- Select role: 1=Entry (client), 2=Exit (server)
- Paste the peer WireGuard public key
- Fill ports when asked:
  - Entry: local UDPlex listen port (default 7000), forward line #1 and #2 targets (ExitIP:9000 / ExitIP:9001)
  - Exit: listen ports for line #1/#2 (defaults 9000/9001), WireGuard server port (default 51820)
- Default WireGuard addresses: Entry 10.0.0.1/24 ↔ Exit 10.0.0.2/24 (customize if needed)

Generated files:
- /opt/udplex/docker-compose.yml
- /opt/udplex/config.yaml (smart traffic rules included)
- /etc/wireguard/wg0.conf

## Start and enable on boot

Run on both sides:

```bash
sudo bash ./udplex-wg-manager.sh start
```

This will start the UDPlex container, bring up WireGuard, and enable wg0 on boot.

## Useful operations

```bash
sudo bash ./udplex-wg-manager.sh status     # Show UDPlex/WireGuard/ports
sudo bash ./udplex-wg-manager.sh logs       # Follow UDPlex logs
sudo bash ./udplex-wg-manager.sh stop       # Stop UDPlex and WireGuard
sudo bash ./udplex-wg-manager.sh pause      # Down wg0 (container stays up)
sudo bash ./udplex-wg-manager.sh resume     # Up wg0
sudo bash ./udplex-wg-manager.sh update     # Pull latest image and restart container
sudo bash ./udplex-wg-manager.sh reload     # Reload config (compose up -d)
sudo bash ./udplex-wg-manager.sh show-keys  # Show local WireGuard public key
sudo bash ./udplex-wg-manager.sh lang en    # Switch script language (zh/en)
```

## Smart split and threshold

The generated `config.yaml` includes a bandwidth-based + sequence-based split:
- When bps ≤ threshold (default 50 Mbps): send on both lines (redundant), reducing loss
- When bps > threshold: split by packet sequence parity to avoid waste

Update threshold online:

```bash
sudo bash ./udplex-wg-manager.sh set-threshold 80000000
sudo bash ./udplex-wg-manager.sh reload
```

## Firewall and ports

If you used defaults during install, open these UDP ports:
- Entry: 7000/udp (UDPlex listens; WireGuard peer connects to 127.0.0.1:7000)
- Exit: 9000/udp and 9001/udp (two relay lines), 51820/udp (WireGuard server)

## Validate connectivity

After start on both sides:

```bash
sudo bash ./udplex-wg-manager.sh status
sudo wg show
# From Entry side (if using default addresses):
ping 10.0.0.2
```

## Troubleshooting

- Container logs:
  ```bash
  sudo bash ./udplex-wg-manager.sh logs
  ```
- WireGuard state:
  ```bash
  sudo wg show
  ```
- Ports listening / firewall:
  ```bash
  ss -lunpt | grep -E ":(7000|9000|9001|51820)\b" || netstat -tulpn | grep -E ":(7000|9000|9001|51820)\b"
  ```
- Docker networking hiccups:
  ```bash
  sudo systemctl restart docker
  ```

## Security notes

- The installer auto-generates a strong UDPlex auth secret (must match on both ends). See /opt/udplex/secret if you need to copy it across.
- Run `update` periodically to get the latest image.
- Only open required UDP ports; restrict source IPs where possible.
- Review logs regularly.
