#!/usr/bin/env bash
set -euo pipefail

# UDPlex access gateway manager
#
# Ordinary WireGuard and OpenVPN clients connect to the entry server, which
# relays their packets unchanged over two UDPlex lines to the exit server.
# The exit terminates the clients and forwards their traffic to the internet.
#
#   client --WireGuard/OpenVPN--> entry ==UDPlex lines==> exit (wg/openvpn) --> internet
#
# Commands:
#   install | uninstall | start | stop | restart | status | logs | update | show-keys
#   client add|del|list|show <name> (exit) | lang <zh|en> | set-threshold <bps>

# --------------------------------
# Global
# --------------------------------
BASE_DIR="${UDPLEX_GW_DIR:-/opt/udplex-gw}"
SETTINGS_FILE="${BASE_DIR}/settings.env"
CONFIG_FILE="${BASE_DIR}/config.yaml"
COMPOSE_FILE="${BASE_DIR}/docker-compose.yml"
KEYS_DIR="${BASE_DIR}/keys"
PKI_DIR="${BASE_DIR}/pki"
CLIENTS_DIR="${BASE_DIR}/clients"
SYSCTL_FILE="/etc/sysctl.d/99-udplex-gw.conf"

CONTAINER_NAME="udplex-gw"
CONTAINER_PKI_DIR="/app/pki"
UDPLEX_IMAGE="ghcr.io/tao08141/udplex:latest"
DOCKER_INSTALL_SCRIPT_URL="https://get.docker.com"

WG_ACCESS_IFACE="wg_access"
OVPN_ACCESS_IFACE="ovpn_access"
# Client packets plus the 16-byte line header still fit a 1500-byte path.
TUNNEL_MTU=1420

DOCKER_COMPOSE=""

# Settings, loaded from SETTINGS_FILE.
LANG_SEL="zh"
ROLE=""
SECRET=""
THRESHOLD="50000000"
LINE1_PROTO="udp"
LINE2_PROTO="udp"
HIGH_TRAFFIC_MODE="balance"
PREFERRED_LINE="1"
LINE1_ADDR=""
LINE2_ADDR=""
LISTEN1_PORT="9100"
LISTEN2_PORT="9101"
WG_ACCESS="no"
WG_ACCESS_PORT="51821"
WG_NET="10.8.0"
OVPN_ACCESS="no"
OVPN_PORT="1194"
OVPN_NET="10.9.0"
PUBLIC_HOST=""
CLIENT_DNS="1.1.1.1"
CLIENT_ROUTES="0.0.0.0/0"

SETTINGS_KEYS=(LANG_SEL ROLE SECRET THRESHOLD LINE1_PROTO LINE2_PROTO HIGH_TRAFFIC_MODE PREFERRED_LINE
  LINE1_ADDR LINE2_ADDR LISTEN1_PORT LISTEN2_PORT
  WG_ACCESS WG_ACCESS_PORT WG_NET OVPN_ACCESS OVPN_PORT OVPN_NET
  PUBLIC_HOST CLIENT_DNS CLIENT_ROUTES)

# --------------------------------
# I18N
# --------------------------------
T() {
  local key="$1"; shift || true
  local msg=""
  case "${LANG_SEL}:${key}" in
    zh:need_root) msg="请使用 root 权限运行此脚本，例如：sudo bash $0 ..." ;;
    en:need_root) msg="Please run this script as root (e.g., sudo bash $0 ...)" ;;
    zh:docker_installed) msg="Docker 已安装。" ;;
    en:docker_installed) msg="Docker is already installed." ;;
    zh:docker_installing) msg="正在安装 Docker..." ;;
    en:docker_installing) msg="Installing Docker..." ;;
    zh:compose_failed) msg="未找到 docker compose，自动安装失败，请手动安装后重试。" ;;
    en:compose_failed) msg="docker compose not found and could not be installed. Install it manually and retry." ;;
    zh:pkg_installing) msg="正在安装依赖：%s" ;;
    en:pkg_installing) msg="Installing dependencies: %s" ;;
    zh:pkg_manual) msg="请手动安装：%s" ;;
    en:pkg_manual) msg="Please install manually: %s" ;;
    zh:already_installed) msg="检测到已有安装（%s）。重新安装会保留密钥、证书和客户端，并重写配置。继续？(y/N): " ;;
    en:already_installed) msg="An installation exists in %s. Reinstalling keeps keys, certificates and clients and rewrites the config. Continue? (y/N): " ;;
    zh:select_role) msg="请选择角色：[1] 入口端（中继客户端报文，先安装）  [2] 出口端（终结客户端并转发到互联网）" ;;
    en:select_role) msg="Select role: [1] Entry (relays client packets, install it first)  [2] Exit (terminates clients, forwards to the internet)" ;;
    zh:invalid_choice) msg="无效选择。" ;;
    en:invalid_choice) msg="Invalid choice." ;;
    zh:prompt_threshold) msg="带宽阈值（bps，默认 %s）: " ;;
    en:prompt_threshold) msg="Bandwidth threshold (bps, default %s): " ;;
    zh:prompt_secret) msg="UDPlex 线路鉴权密钥（两端必须一致，留空自动生成）: " ;;
    en:prompt_secret) msg="UDPlex line auth secret (must match on both ends, empty to generate): " ;;
    zh:show_secret_title) msg="UDPlex 共享密钥（请复制到对端）:" ;;
    en:show_secret_title) msg="UDPlex shared secret (copy it to the peer):" ;;
    zh:show_entry_title) msg="出口端安装时需要以下信息：" ;;
    en:show_entry_title) msg="The exit install asks for the following:" ;;
    zh:prompt_line1_proto) msg="线路 1 外层协议 [1] UDP  [2] TCP（默认 1）: " ;;
    en:prompt_line1_proto) msg="Outer protocol for line #1 [1] UDP  [2] TCP (default 1): " ;;
    zh:prompt_line2_proto) msg="线路 2 外层协议 [1] UDP  [2] TCP（默认 1）: " ;;
    en:prompt_line2_proto) msg="Outer protocol for line #2 [1] UDP  [2] TCP (default 1): " ;;
    zh:prompt_high_traffic_mode) msg="大流量策略 [1] 两条线路均衡分流  [2] 全部走单条线路（默认 1）: " ;;
    en:prompt_high_traffic_mode) msg="High-traffic strategy [1] Balance across both lines  [2] Single line only (default 1): " ;;
    zh:prompt_preferred_line) msg="大流量固定走哪条线路 [1] 线路 1  [2] 线路 2（默认 1）: " ;;
    en:prompt_preferred_line) msg="Line for high traffic [1] Line #1  [2] Line #2 (default 1): " ;;
    zh:prompt_line1) msg="线路 1 目标（出口端 IP:端口，例如 1.2.3.4:9100）: " ;;
    en:prompt_line1) msg="Line #1 target (exit server IP:port, e.g. 1.2.3.4:9100): " ;;
    zh:prompt_line2) msg="线路 2 目标（出口端 IP:端口，例如 1.2.3.4:9101）: " ;;
    en:prompt_line2) msg="Line #2 target (exit server IP:port, e.g. 1.2.3.4:9101): " ;;
    zh:need_two_lines) msg="必须提供两条线路的目标地址。" ;;
    en:need_two_lines) msg="Both line targets are required." ;;
    zh:prompt_listen1) msg="线路 1 监听端口（默认 9100）: " ;;
    en:prompt_listen1) msg="Line #1 listen port (default 9100): " ;;
    zh:prompt_listen2) msg="线路 2 监听端口（默认 9101）: " ;;
    en:prompt_listen2) msg="Line #2 listen port (default 9101): " ;;
    zh:prompt_enable_wg) msg="启用 WireGuard 客户端接入？(Y/n，两端必须一致): " ;;
    en:prompt_enable_wg) msg="Accept WireGuard clients? (Y/n, must match on both ends): " ;;
    zh:prompt_wg_port) msg="入口端 WireGuard 接入端口（UDP，默认 51821）: " ;;
    en:prompt_wg_port) msg="WireGuard access port on the entry (UDP, default 51821): " ;;
    zh:prompt_wg_net) msg="WireGuard 客户端网段（/24，默认 10.8.0.0/24）: " ;;
    en:prompt_wg_net) msg="WireGuard client subnet (/24, default 10.8.0.0/24): " ;;
    zh:prompt_enable_ovpn) msg="启用 OpenVPN 客户端接入？(Y/n，两端必须一致): " ;;
    en:prompt_enable_ovpn) msg="Accept OpenVPN clients? (Y/n, must match on both ends): " ;;
    zh:prompt_ovpn_port) msg="入口端 OpenVPN 接入端口（UDP，默认 1194）: " ;;
    en:prompt_ovpn_port) msg="OpenVPN access port on the entry (UDP, default 1194): " ;;
    zh:prompt_ovpn_net) msg="OpenVPN 客户端网段（/24，默认 10.9.0.0/24）: " ;;
    en:prompt_ovpn_net) msg="OpenVPN client subnet (/24, default 10.9.0.0/24): " ;;
    zh:bad_net) msg="网段格式应为 a.b.c.0/24，且两个网段不能相同。" ;;
    en:bad_net) msg="Subnets must look like a.b.c.0/24 and differ from each other." ;;
    zh:need_access) msg="至少要启用一种客户端接入。" ;;
    en:need_access) msg="Enable at least one kind of client access." ;;
    zh:prompt_public_host) msg="客户端连接的入口端地址（入口公网 IP 或域名）: " ;;
    en:prompt_public_host) msg="Entry address clients connect to (public IP or domain of the entry): " ;;
    zh:need_public_host) msg="必须提供入口端地址。" ;;
    en:need_public_host) msg="The entry address is required." ;;
    zh:prompt_dns) msg="推送给客户端的 DNS（默认 1.1.1.1）: " ;;
    en:prompt_dns) msg="DNS server for clients (default 1.1.1.1): " ;;
    zh:prompt_routes) msg="客户端经隧道访问的网段，逗号分隔（默认 0.0.0.0/0 即全局）: " ;;
    en:prompt_routes) msg="Networks clients reach through the tunnel, comma separated (default 0.0.0.0/0, all traffic): " ;;
    zh:pki_created) msg="OpenVPN 证书已生成：%s" ;;
    en:pki_created) msg="OpenVPN PKI created in %s" ;;
    zh:install_done) msg="安装完成。现在可以执行：sudo bash $0 start" ;;
    en:install_done) msg="Installation finished. Now run: sudo bash $0 start" ;;
    zh:install_done_exit) msg="启动后用 sudo bash $0 client add <名称> 添加客户端。" ;;
    en:install_done_exit) msg="After starting, add clients with: sudo bash $0 client add <name>" ;;
    zh:open_ports) msg="请确认防火墙/安全组已放通：%s" ;;
    en:open_ports) msg="Make sure your firewall / security group allows: %s" ;;
    zh:no_install) msg="未找到安装，请先执行：sudo bash $0 install" ;;
    en:no_install) msg="No installation found. Run: sudo bash $0 install" ;;
    zh:started) msg="UDPlex 网关已启动。" ;;
    en:started) msg="UDPlex gateway started." ;;
    zh:iface_ready) msg="接口 %s 已就绪。" ;;
    en:iface_ready) msg="Interface %s is up." ;;
    zh:iface_fail) msg="接口 %s 未就绪，请执行 logs 查看日志。" ;;
    en:iface_fail) msg="Interface %s is not up. Check: sudo bash $0 logs" ;;
    zh:stopped) msg="UDPlex 网关已停止。" ;;
    en:stopped) msg="UDPlex gateway stopped." ;;
    zh:restarted) msg="UDPlex 网关已按新配置重启。" ;;
    en:restarted) msg="UDPlex gateway restarted with the new config." ;;
    zh:updated_image) msg="镜像已更新并重启。" ;;
    en:updated_image) msg="Image updated and restarted." ;;
    zh:exit_only) msg="客户端管理只能在出口端使用。" ;;
    en:exit_only) msg="Client management is only available on the exit." ;;
    zh:bad_name) msg="客户端名称只能包含字母、数字、点、下划线和横线（最多 32 个字符）。" ;;
    en:bad_name) msg="Client names may contain letters, digits, dot, underscore and dash (up to 32 characters)." ;;
    zh:client_exists) msg="客户端 %s 已存在。" ;;
    en:client_exists) msg="Client %s already exists." ;;
    zh:client_missing) msg="客户端 %s 不存在。" ;;
    en:client_missing) msg="Client %s does not exist." ;;
    zh:pool_full) msg="WireGuard 客户端网段已满。" ;;
    en:pool_full) msg="The WireGuard client subnet is full." ;;
    zh:client_added) msg="客户端 %s 已添加，配置文件位于 %s" ;;
    en:client_added) msg="Client %s added, its files are in %s" ;;
    zh:client_deleted) msg="客户端 %s 已删除。" ;;
    en:client_deleted) msg="Client %s deleted." ;;
    zh:client_reload) msg="正在重启网关以应用 WireGuard 客户端变更（所有连接会短暂中断）..." ;;
    en:client_reload) msg="Restarting the gateway to apply WireGuard client changes (all connections drop briefly)..." ;;
    zh:no_clients) msg="还没有客户端。" ;;
    en:no_clients) msg="No clients yet." ;;
    zh:qr_hint) msg="安装 qrencode 后可显示二维码，方便手机扫码导入。" ;;
    en:qr_hint) msg="Install qrencode to show a QR code for phone import." ;;
    zh:uninstall_confirm) msg="将停止网关并删除 %s（包含密钥、证书和所有客户端）。继续？(y/N): " ;;
    en:uninstall_confirm) msg="This stops the gateway and deletes %s (keys, certificates and all clients). Continue? (y/N): " ;;
    zh:prompt_backup) msg="删除前备份到 /root？(Y/n): " ;;
    en:prompt_backup) msg="Back it up to /root before deleting? (Y/n): " ;;
    zh:backed_up) msg="已备份到：%s" ;;
    en:backed_up) msg="Backed up to: %s" ;;
    zh:cancelled) msg="已取消。" ;;
    en:cancelled) msg="Cancelled." ;;
    zh:uninstalled) msg="卸载完成。" ;;
    en:uninstalled) msg="Uninstall finished." ;;
    zh:lang_set) msg="语言已切换为：%s" ;;
    en:lang_set) msg="Language switched to: %s" ;;
    zh:threshold_set) msg="带宽阈值已更新为 %s bps，执行 restart 生效。" ;;
    en:threshold_set) msg="Bandwidth threshold set to %s bps, run restart to apply." ;;
    zh:unknown_cmd) msg="未知命令：%s" ;;
    en:unknown_cmd) msg="Unknown command: %s" ;;
    *) msg="${key}" ;;
  esac
  # shellcheck disable=SC2059
  printf -- "$msg" "$@"
}

err() { echo "[ERROR] $(T "$@")" >&2; }
info() { echo "[INFO] $(T "$@")"; }
warn() { echo "[WARN] $(T "$@")"; }

# --------------------------------
# Settings
# --------------------------------
load_settings() {
  if [[ -f "${SETTINGS_FILE}" ]]; then
    # shellcheck disable=SC1090
    source "${SETTINGS_FILE}"
  fi
  case "${LANG_SEL}" in zh|en) ;; *) LANG_SEL="zh" ;; esac
}

save_settings() {
  mkdir -p "${BASE_DIR}"
  local key tmp="${SETTINGS_FILE}.tmp"
  : > "${tmp}"
  chmod 600 "${tmp}"
  for key in "${SETTINGS_KEYS[@]}"; do
    printf '%s=%q\n' "${key}" "${!key}" >> "${tmp}"
  done
  mv -f "${tmp}" "${SETTINGS_FILE}"
}

require_install() {
  if [[ ! -f "${SETTINGS_FILE}" || -z "${ROLE}" ]]; then
    err no_install
    exit 1
  fi
}

# --------------------------------
# Host helpers
# --------------------------------
need_root() {
  if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
    err need_root
    exit 1
  fi
}

detect_pkg_mgr() {
  if command -v apt-get >/dev/null 2>&1; then
    echo "apt"
  elif command -v dnf >/dev/null 2>&1; then
    echo "dnf"
  elif command -v yum >/dev/null 2>&1; then
    echo "yum"
  else
    echo ""
  fi
}

# install_packages installs the packages for the commands that are missing.
# Arguments are command:package pairs.
install_packages() {
  local pair cmd pkg missing=()
  for pair in "$@"; do
    cmd="${pair%%:*}"
    pkg="${pair#*:}"
    command -v "${cmd}" >/dev/null 2>&1 || missing+=("${pkg}")
  done
  [[ ${#missing[@]} -eq 0 ]] && return 0

  info pkg_installing "${missing[*]}"
  case "$(detect_pkg_mgr)" in
    apt) apt-get update -y >/dev/null && apt-get install -y "${missing[@]}" ;;
    dnf) dnf install -y epel-release >/dev/null 2>&1 || true; dnf install -y "${missing[@]}" ;;
    yum) yum install -y epel-release >/dev/null 2>&1 || true; yum install -y "${missing[@]}" ;;
    *) err pkg_manual "${missing[*]}"; exit 1 ;;
  esac
}

ensure_compose_cmd() {
  if docker compose version >/dev/null 2>&1; then
    DOCKER_COMPOSE="docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then
    DOCKER_COMPOSE="docker-compose"
  else
    DOCKER_COMPOSE=""
  fi
}

install_docker() {
  if command -v docker >/dev/null 2>&1; then
    info docker_installed
  else
    info docker_installing
    curl -fsSL "${DOCKER_INSTALL_SCRIPT_URL}" -o /tmp/install-docker.sh
    sh /tmp/install-docker.sh
    rm -f /tmp/install-docker.sh
    systemctl enable --now docker >/dev/null 2>&1 || true
  fi
  ensure_compose_cmd
  if [[ -z "${DOCKER_COMPOSE}" ]]; then
    case "$(detect_pkg_mgr)" in
      apt) apt-get update -y >/dev/null && apt-get install -y docker-compose-plugin || true ;;
      dnf) dnf install -y docker-compose-plugin || true ;;
      yum) yum install -y docker-compose-plugin || true ;;
    esac
    ensure_compose_cmd
    if [[ -z "${DOCKER_COMPOSE}" ]]; then
      err compose_failed
      exit 1
    fi
  fi
}

# prepare_host enables forwarding on the exit host: /proc/sys is read-only
# inside the container. Netfilter modules are loaded here for the same reason.
# The entry only relays UDP and needs neither.
prepare_host() {
  [[ "${ROLE}" == "exit" ]] || return 0
  printf 'net.ipv4.ip_forward = 1\n' > "${SYSCTL_FILE}"
  sysctl -q -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
  modprobe -a tun iptable_nat iptable_mangle xt_MASQUERADE xt_TCPMSS >/dev/null 2>&1 || true
}

random_secret() {
  openssl rand -base64 32 2>/dev/null || head -c 32 /dev/urandom | base64
}

detect_public_ip() {
  curl -fsS4 --max-time 5 https://api.ipify.org 2>/dev/null ||
    curl -fsS4 --max-time 5 https://ifconfig.me 2>/dev/null || true
}

# parse_net turns a.b.c.0/24 (or a.b.c) into a.b.c.
parse_net() {
  local value="${1%/24}"
  value="${value%.0}"
  if [[ "${value}" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]] &&
    ((BASH_REMATCH[1] <= 255 && BASH_REMATCH[2] <= 255 && BASH_REMATCH[3] <= 255)); then
    printf '%s' "${value}"
    return 0
  fi
  return 1
}

# endpoint_host formats a host for host:port, bracketing IPv6 addresses.
endpoint_host() {
  if [[ "$1" == *:* ]]; then
    printf '[%s]' "$1"
  else
    printf '%s' "$1"
  fi
}

ask() {
  local __var="$1" __prompt="$2" __default="${3:-}" __answer=""
  read -rp "${__prompt}" __answer || true
  printf -v "${__var}" '%s' "${__answer:-${__default}}"
}

ask_yes() {
  local answer=""
  read -rp "$1" answer || true
  answer="${answer:-${2:-y}}"
  [[ "${answer,,}" == y || "${answer,,}" == yes ]]
}

select_proto() {
  local answer=""
  while true; do
    read -rp "$(T "$1")" answer || true
    case "${answer,,}" in
      ""|1|u|udp) printf 'udp'; return 0 ;;
      2|t|tcp) printf 'tcp'; return 0 ;;
      *) warn invalid_choice >&2 ;;
    esac
  done
}

# --------------------------------
# Keys and PKI
# --------------------------------
gen_wg_keypair() {
  local name="$1"
  if [[ ! -f "${KEYS_DIR}/${name}.key" ]]; then
    mkdir -p "${KEYS_DIR}"
    (umask 077 && wg genkey > "${KEYS_DIR}/${name}.key")
    wg pubkey < "${KEYS_DIR}/${name}.key" > "${KEYS_DIR}/${name}.pub"
  fi
}

write_openssl_cnf() {
  cat > "${PKI_DIR}/openssl.cnf" <<EOF
[ ca ]
default_ca = gateway_ca

[ gateway_ca ]
dir              = ${PKI_DIR}
database         = \$dir/index.txt
new_certs_dir    = \$dir/issued
certificate      = \$dir/ca.crt
private_key      = \$dir/ca.key
serial           = \$dir/serial
crlnumber        = \$dir/crlnumber
default_md       = sha256
default_days     = 3650
default_crl_days = 3650
policy           = policy_any
unique_subject   = no
copy_extensions  = none

[ policy_any ]
commonName = supplied

[ server_ext ]
basicConstraints = CA:FALSE
keyUsage         = critical, digitalSignature, keyAgreement
extendedKeyUsage = serverAuth

[ client_ext ]
basicConstraints = CA:FALSE
keyUsage         = critical, digitalSignature, keyAgreement
extendedKeyUsage = clientAuth
EOF
}

# pki_issue <name> <server_ext|client_ext> <out_dir> issues a P-256 certificate.
pki_issue() {
  local name="$1" ext="$2" out="$3"
  (umask 077 && openssl ecparam -name prime256v1 -genkey -noout -out "${out}/${name}.key")
  openssl req -new -key "${out}/${name}.key" -subj "/CN=${name}" -out "${out}/${name}.csr" 2>/dev/null
  openssl ca -batch -notext -config "${PKI_DIR}/openssl.cnf" -extensions "${ext}" \
    -in "${out}/${name}.csr" -out "${out}/${name}.crt" >/dev/null 2>&1
  rm -f "${out}/${name}.csr"
}

pki_gencrl() {
  openssl ca -gencrl -config "${PKI_DIR}/openssl.cnf" -out "${PKI_DIR}/crl.pem" >/dev/null 2>&1
  chmod 644 "${PKI_DIR}/crl.pem"
}

pki_init() {
  [[ -f "${PKI_DIR}/ca.crt" ]] && return 0
  mkdir -p "${PKI_DIR}/issued"
  chmod 700 "${PKI_DIR}"
  : > "${PKI_DIR}/index.txt"
  echo 1000 > "${PKI_DIR}/serial"
  echo 1000 > "${PKI_DIR}/crlnumber"
  write_openssl_cnf

  (umask 077 && openssl ecparam -name prime256v1 -genkey -noout -out "${PKI_DIR}/ca.key")
  openssl req -x509 -new -sha256 -days 3650 -key "${PKI_DIR}/ca.key" -subj "/CN=UDPlex Gateway CA" \
    -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -out "${PKI_DIR}/ca.crt" 2>/dev/null
  pki_issue server server_ext "${PKI_DIR}"
  {
    echo "-----BEGIN OpenVPN Static key V1-----"
    openssl rand -hex 256 | fold -w 32
    echo "-----END OpenVPN Static key V1-----"
  } > "${PKI_DIR}/tc.key"
  chmod 600 "${PKI_DIR}/tc.key"
  pki_gencrl
  info pki_created "${PKI_DIR}"
}

# --------------------------------
# Config rendering
# --------------------------------
render_load_balancer_rules() {
  local tag1="line1" tag2="line2"
  cat <<YAML
      - rule: "bps <= ${THRESHOLD} || !available_${tag1} || !available_${tag2}"
        targets: [${tag1}, ${tag2}]
YAML
  if [[ "${HIGH_TRAFFIC_MODE}" == "single" ]]; then
    local primary="${tag1}"
    [[ "${PREFERRED_LINE}" == "2" ]] && primary="${tag2}"
    cat <<YAML
      - rule: "(bps > ${THRESHOLD}) && available_${primary}"
        targets: [${primary}]
YAML
  else
    cat <<YAML
      - rule: "(bps > ${THRESHOLD}) && (seq % 2 == 0) && available_${tag1} && available_${tag2}"
        targets: [${tag1}]
      - rule: "(bps > ${THRESHOLD}) && (seq % 2 == 1) && available_${tag2} && available_${tag1}"
        targets: [${tag2}]
YAML
  fi
}

render_line_auth() {
  cat <<YAML
    auth:
      enabled: true
      secret: "${SECRET}"
      enable_encryption: false
      heartbeat_interval: 10
YAML
}

# Lines are checked every 5 seconds, so they re-authenticate quickly after
# the exit restarts (client add/del restarts it).
render_entry_line() {
  local tag="$1" target="$2" proto="$3"
  if [[ "${proto}" == "tcp" ]]; then
    cat <<YAML
  - type: tcp_tunnel_forward
    tag: ${tag}
    forwarders: [${target}:4]
    reconnect_interval: 2
    connection_check_time: 5
    no_delay: true
    detour: [$(client_detour)]
YAML
  else
    cat <<YAML
  - type: forward
    tag: ${tag}
    forwarders: [${target}]
    reconnect_interval: 2
    connection_check_time: 5
    detour: [$(client_detour)]
YAML
  fi
  render_line_auth
}

# The exit's line listeners keep the connection ID the entry gave each
# client, so the exit tells clients apart and sees a client as the same
# connection on both lines.
render_exit_line() {
  local tag="$1" port="$2" proto="$3"
  if [[ "${proto}" == "tcp" ]]; then
    cat <<YAML
  - type: tcp_tunnel_listen
    tag: ${tag}
    listen_addr: 0.0.0.0:${port}
    timeout: 120
    no_delay: true
    detour: [$(client_detour)]
YAML
  else
    cat <<YAML
  - type: listen
    tag: ${tag}
    listen_addr: 0.0.0.0:${port}
    timeout: 120
    preserve_conn_id: true
    detour: [$(client_detour)]
YAML
  fi
  render_line_auth
}

# client_detour is where packets arriving from the lines go: the client
# component of the enabled protocol, or the filter telling both apart.
client_detour() {
  if [[ "${WG_ACCESS}" == "yes" && "${OVPN_ACCESS}" == "yes" ]]; then
    printf 'client_filter'
  elif [[ "${WG_ACCESS}" == "yes" ]]; then
    printf '%s' "$(client_tag wg)"
  else
    printf '%s' "$(client_tag ovpn)"
  fi
}

# client_tag names the component facing the clients of a protocol: the relay
# listener on the entry, the server on the exit.
client_tag() {
  case "${ROLE}:$1" in
    entry:wg) printf 'wg_in' ;;
    entry:ovpn) printf 'ovpn_in' ;;
    exit:wg) printf '%s' "${WG_ACCESS_IFACE}" ;;
    exit:ovpn) printf '%s' "${OVPN_ACCESS_IFACE}" ;;
  esac
}

render_client_filter() {
  [[ "${WG_ACCESS}" == "yes" && "${OVPN_ACCESS}" == "yes" ]] || return 0
  cat <<YAML
  - type: filter
    tag: client_filter
    use_proto_detectors: [wireguard]
    detour:
      wireguard: [$(client_tag wg)]
    detour_miss: [$(client_tag ovpn)]
YAML
}

# A WireGuard message starts with its type (1-4) and three zero bytes; the
# first byte of an OpenVPN packet carries the opcode in its upper five bits,
# so it is at least 0x08.
render_protocol_detectors() {
  [[ "${WG_ACCESS}" == "yes" && "${OVPN_ACCESS}" == "yes" ]] || return 0
  cat <<YAML
protocol_detectors:
  wireguard:
    signatures:
      - offset: 0
        bytes: "00000000"
        mask: "F8FFFFFF"
        hex: true
        length: {min: 32}
    description: WireGuard message
YAML
}

render_entry_relay() {
  local proto port
  for proto in wg ovpn; do
    if [[ "${proto}" == "wg" ]]; then
      [[ "${WG_ACCESS}" == "yes" ]] || continue
      port="${WG_ACCESS_PORT}"
    else
      [[ "${OVPN_ACCESS}" == "yes" ]] || continue
      port="${OVPN_PORT}"
    fi
    cat <<YAML
  - type: listen
    tag: $(client_tag "${proto}")
    listen_addr: 0.0.0.0:${port}
    timeout: 180
    broadcast_mode: false
    detour: [load_balancer]
YAML
  done
}

render_wg_access() {
  local dir name peers=""
  for dir in "${CLIENTS_DIR}"/*/; do
    [[ -f "${dir}wg.pub" ]] || continue
    name="$(basename "${dir}")"
    peers+="      - public_key: $(cat "${dir}wg.pub") # ${name}
        allowed_ips: [$(cat "${dir}wg.ip")/32]
"
  done
  cat <<YAML
  - type: wg
    tag: ${WG_ACCESS_IFACE}
    bind_mode: udplex
    interface_name: ${WG_ACCESS_IFACE}
    mtu: ${TUNNEL_MTU}
    addresses: [${WG_NET}.1/24]
    private_key: $(cat "${KEYS_DIR}/access.key")
    detour: [load_balancer]
    reuse_incoming_detour: false
    ip_forward: true
    mss_clamp: true
    masquerade:
      - {source: ${WG_NET}.0/24, out_interface: auto}
YAML
  if [[ -n "${peers}" ]]; then
    printf '    peers:\n%s' "${peers}"
  else
    echo "    peers: []"
  fi
}

render_ovpn_access() {
  local routes="" route
  cat <<YAML
  - type: openvpn
    tag: ${OVPN_ACCESS_IFACE}
    bind_mode: udplex
    interface_name: ${OVPN_ACCESS_IFACE}
    mtu: ${TUNNEL_MTU}
    addresses: [${OVPN_NET}.1/24]
    detour: [load_balancer]
    reuse_incoming_detour: false
    ca: ${CONTAINER_PKI_DIR}/ca.crt
    cert: ${CONTAINER_PKI_DIR}/server.crt
    key: ${CONTAINER_PKI_DIR}/server.key
    tls_crypt: ${CONTAINER_PKI_DIR}/tc.key
    crl_verify: ${CONTAINER_PKI_DIR}/crl.pem
    push_dns: [${CLIENT_DNS}]
YAML
  if [[ "${CLIENT_ROUTES}" == "0.0.0.0/0" ]]; then
    echo "    redirect_gateway: true"
  else
    for route in ${CLIENT_ROUTES//,/ }; do
      routes+="${routes:+, }${route}"
    done
    echo "    push_routes: [${routes}]"
  fi
  cat <<YAML
    ip_forward: true
    mss_clamp: true
    masquerade:
      - {source: ${OVPN_NET}.0/24, out_interface: auto}
YAML
}

render_config() {
  {
    cat <<YAML
# Generated by udplex-gateway-manager.sh, changes are overwritten.
buffer_size: 1500
queue_size: 10240
worker_count: 4
logging:
  level: info
  format: console
  output_path: stdout
  caller: false
services:
YAML
    if [[ "${ROLE}" == "entry" ]]; then
      render_entry_relay
      render_entry_line line1 "${LINE1_ADDR}" "${LINE1_PROTO}"
      render_entry_line line2 "${LINE2_ADDR}" "${LINE2_PROTO}"
    else
      render_exit_line line1 "${LISTEN1_PORT}" "${LINE1_PROTO}"
      render_exit_line line2 "${LISTEN2_PORT}" "${LINE2_PROTO}"
    fi
    # Every packet goes over both lines below the threshold; the client
    # protocols drop the duplicate copies.
    cat <<YAML
  - type: load_balancer
    tag: load_balancer
    window_size: 3
    batch_decision: true
    detour:
YAML
    render_load_balancer_rules
    render_client_filter
    if [[ "${ROLE}" == "exit" ]]; then
      [[ "${OVPN_ACCESS}" == "yes" ]] && render_ovpn_access
      [[ "${WG_ACCESS}" == "yes" ]] && render_wg_access
    fi
    render_protocol_detectors
  } > "${CONFIG_FILE}.tmp"

  chmod 600 "${CONFIG_FILE}.tmp"
  mv -f "${CONFIG_FILE}.tmp" "${CONFIG_FILE}"
}

write_compose_file() {
  # Only the exit runs the client servers: TUN interfaces, NAT and the PKI.
  local exit_only=""
  if [[ "${ROLE}" == "exit" ]]; then
    exit_only="
      - ./pki:${CONTAINER_PKI_DIR}:ro
    devices:
      - /dev/net/tun:/dev/net/tun
    cap_add:
      - NET_ADMIN"
  fi
  cat > "${COMPOSE_FILE}" <<YAML
services:
  udplex:
    image: ${UDPLEX_IMAGE}
    container_name: ${CONTAINER_NAME}
    restart: always
    command: ["/app/UDPlex", "-c", "/app/config.yaml"]
    volumes:
      - ./config.yaml:/app/config.yaml:ro${exit_only}
    network_mode: host
    logging:
      options:
        max-size: "10m"
        max-file: "3"
YAML
}

# --------------------------------
# Install
# --------------------------------
show_peer_info() {
  echo
  echo "========================================"
  T show_secret_title; echo
  printf '%s\n' "${SECRET}"
  if [[ "${ROLE}" == "entry" ]]; then
    echo
    T show_entry_title; echo
    [[ "${WG_ACCESS}" == "yes" ]] && echo "  WireGuard: ${WG_ACCESS_PORT}/udp"
    [[ "${OVPN_ACCESS}" == "yes" ]] && echo "  OpenVPN:   ${OVPN_PORT}/udp"
  fi
  echo "========================================"
  echo
}

install_flow() {
  need_root
  load_settings
  if [[ -f "${SETTINGS_FILE}" ]] && ! ask_yes "$(T already_installed "${BASE_DIR}")" n; then
    T cancelled; echo
    exit 0
  fi

  echo "Language / 语言: [1] English  [2] 中文"
  local lang_choice=""
  read -rp "> " lang_choice || true
  case "${lang_choice}" in 1) LANG_SEL="en" ;; 2) LANG_SEL="zh" ;; esac

  T select_role; echo
  local role_choice=""
  read -rp "> " role_choice || true
  case "${role_choice}" in
    1) ROLE="entry" ;;
    2) ROLE="exit" ;;
    *) err invalid_choice; exit 1 ;;
  esac

  install_docker
  if [[ "${ROLE}" == "exit" ]]; then
    install_packages wg:wireguard-tools openssl:openssl curl:curl
  else
    install_packages curl:curl
  fi
  mkdir -p "${BASE_DIR}" "${KEYS_DIR}" "${CLIENTS_DIR}"
  chmod 700 "${BASE_DIR}" "${KEYS_DIR}" "${CLIENTS_DIR}"

  local secret_input=""
  read -rp "$(T prompt_secret)" secret_input || true
  if [[ -n "${secret_input}" ]]; then
    SECRET="${secret_input}"
  elif [[ -z "${SECRET}" ]]; then
    SECRET="$(random_secret)"
  fi

  local threshold_input=""
  read -rp "$(T prompt_threshold "${THRESHOLD}")" threshold_input || true
  [[ "${threshold_input}" =~ ^[0-9]+$ ]] && THRESHOLD="${threshold_input}"

  LINE1_PROTO="$(select_proto prompt_line1_proto)"
  LINE2_PROTO="$(select_proto prompt_line2_proto)"
  local answer=""
  while true; do
    read -rp "$(T prompt_high_traffic_mode)" answer || true
    case "${answer,,}" in
      ""|1|b|balance) HIGH_TRAFFIC_MODE="balance"; break ;;
      2|s|single) HIGH_TRAFFIC_MODE="single"; break ;;
      *) warn invalid_choice ;;
    esac
  done
  if [[ "${HIGH_TRAFFIC_MODE}" == "single" ]]; then
    while true; do
      read -rp "$(T prompt_preferred_line)" answer || true
      answer="${answer:-1}"
      [[ "${answer}" == 1 || "${answer}" == 2 ]] && { PREFERRED_LINE="${answer}"; break; }
      warn invalid_choice
    done
  fi

  local ports=()
  if [[ "${ROLE}" == "entry" ]]; then
    ask LINE1_ADDR "$(T prompt_line1)" "${LINE1_ADDR}"
    ask LINE2_ADDR "$(T prompt_line2)" "${LINE2_ADDR}"
    if [[ -z "${LINE1_ADDR}" || -z "${LINE2_ADDR}" ]]; then
      err need_two_lines
      exit 1
    fi
    install_access_ports
    [[ "${WG_ACCESS}" == "yes" ]] && ports+=("${WG_ACCESS_PORT}/udp")
    [[ "${OVPN_ACCESS}" == "yes" ]] && ports+=("${OVPN_PORT}/udp")
  else
    ask LISTEN1_PORT "$(T prompt_listen1)" "9100"
    ask LISTEN2_PORT "$(T prompt_listen2)" "9101"
    ports+=("${LISTEN1_PORT}/${LINE1_PROTO}" "${LISTEN2_PORT}/${LINE2_PROTO}")
    install_exit_access
  fi

  save_settings
  write_compose_file
  render_config

  echo
  info install_done
  [[ "${ROLE}" == "exit" ]] && info install_done_exit
  info open_ports "${ports[*]}"
  show_peer_info
}

# install_access_ports asks which client protocols are relayed and on which
# entry ports; the exit asks the same so its client files match.
install_access_ports() {
  WG_ACCESS="no"
  OVPN_ACCESS="no"
  if ask_yes "$(T prompt_enable_wg)" y; then
    WG_ACCESS="yes"
    ask WG_ACCESS_PORT "$(T prompt_wg_port)" "51821"
  fi
  if ask_yes "$(T prompt_enable_ovpn)" y; then
    OVPN_ACCESS="yes"
    ask OVPN_PORT "$(T prompt_ovpn_port)" "1194"
  fi
  if [[ "${WG_ACCESS}" != "yes" && "${OVPN_ACCESS}" != "yes" ]]; then
    err need_access
    exit 1
  fi
}

install_exit_access() {
  local net=""
  install_access_ports
  if [[ "${WG_ACCESS}" == "yes" ]]; then
    gen_wg_keypair access
    while true; do
      ask net "$(T prompt_wg_net)" "10.8.0.0/24"
      if WG_NET="$(parse_net "${net}")"; then break; fi
      warn bad_net
    done
  fi
  if [[ "${OVPN_ACCESS}" == "yes" ]]; then
    while true; do
      ask net "$(T prompt_ovpn_net)" "10.9.0.0/24"
      if OVPN_NET="$(parse_net "${net}")" && [[ "${WG_ACCESS}" != "yes" || "${OVPN_NET}" != "${WG_NET}" ]]; then break; fi
      warn bad_net
    done
    pki_init
  fi

  ask PUBLIC_HOST "$(T prompt_public_host)" "${PUBLIC_HOST}"
  if [[ -z "${PUBLIC_HOST}" ]]; then
    err need_public_host
    exit 1
  fi
  ask CLIENT_DNS "$(T prompt_dns)" "1.1.1.1"
  ask CLIENT_ROUTES "$(T prompt_routes)" "0.0.0.0/0"
  CLIENT_ROUTES="${CLIENT_ROUTES// /}"
}

# --------------------------------
# Clients
# --------------------------------
require_exit() {
  require_install
  if [[ "${ROLE}" != "exit" ]]; then
    err exit_only
    exit 1
  fi
}

validate_client_name() {
  if [[ ! "${1:-}" =~ ^[A-Za-z0-9_.-]{1,32}$ ]]; then
    err bad_name
    exit 1
  fi
}

next_wg_ip() {
  local i used=" "
  for f in "${CLIENTS_DIR}"/*/wg.ip; do
    [[ -f "${f}" ]] && used+="$(cat "${f}") "
  done
  for i in $(seq 2 254); do
    if [[ "${used}" != *" ${WG_NET}.${i} "* ]]; then
      printf '%s' "${WG_NET}.${i}"
      return 0
    fi
  done
  return 1
}

write_wg_client_conf() {
  local dir="$1" ip="$2" allowed="${CLIENT_ROUTES//,/, }"
  cat > "${dir}/wg.conf" <<EOF
[Interface]
PrivateKey = $(cat "${dir}/wg.key")
Address = ${ip}/32
DNS = ${CLIENT_DNS}
MTU = ${TUNNEL_MTU}

[Peer]
PublicKey = $(cat "${KEYS_DIR}/access.pub")
Endpoint = $(endpoint_host "${PUBLIC_HOST}"):${WG_ACCESS_PORT}
AllowedIPs = ${allowed}
PersistentKeepalive = 25
EOF
  chmod 600 "${dir}/wg.conf"
}

write_ovpn_client_conf() {
  local dir="$1" name="$2"
  {
    cat <<EOF
client
dev tun
proto udp
remote ${PUBLIC_HOST} ${OVPN_PORT}
resolv-retry infinite
nobind
persist-key
persist-tun
remote-cert-tls server
tun-mtu ${TUNNEL_MTU}
verb 3
EOF
    echo "<ca>"; cat "${PKI_DIR}/ca.crt"; echo "</ca>"
    echo "<cert>"; cat "${dir}/${name}.crt"; echo "</cert>"
    echo "<key>"; cat "${dir}/${name}.key"; echo "</key>"
    echo "<tls-crypt>"; cat "${PKI_DIR}/tc.key"; echo "</tls-crypt>"
  } > "${dir}/${name}.ovpn"
  chmod 600 "${dir}/${name}.ovpn"
}

container_running() {
  command -v docker >/dev/null 2>&1 &&
    [[ "$(docker inspect -f '{{.State.Running}}' "${CONTAINER_NAME}" 2>/dev/null)" == "true" ]]
}

apply_client_change() {
  render_config
  if [[ "${WG_ACCESS}" == "yes" ]] && container_running; then
    info client_reload
    restart_services
  fi
}

client_add() {
  local name="${1:-}"
  require_exit
  validate_client_name "${name}"
  local dir="${CLIENTS_DIR}/${name}"
  if [[ -d "${dir}" ]]; then
    err client_exists "${name}"
    exit 1
  fi
  mkdir -p "${dir}"
  chmod 700 "${dir}"

  if [[ "${WG_ACCESS}" == "yes" ]]; then
    local ip
    if ! ip="$(next_wg_ip)"; then
      rm -rf "${dir}"
      err pool_full
      exit 1
    fi
    (umask 077 && wg genkey > "${dir}/wg.key")
    wg pubkey < "${dir}/wg.key" > "${dir}/wg.pub"
    printf '%s' "${ip}" > "${dir}/wg.ip"
    write_wg_client_conf "${dir}" "${ip}"
  fi
  if [[ "${OVPN_ACCESS}" == "yes" ]]; then
    pki_issue "${name}" client_ext "${dir}"
    write_ovpn_client_conf "${dir}" "${name}"
  fi

  info client_added "${name}" "${dir}"
  client_show "${name}"
  apply_client_change
}

client_del() {
  local name="${1:-}"
  require_exit
  validate_client_name "${name}"
  local dir="${CLIENTS_DIR}/${name}"
  if [[ ! -d "${dir}" ]]; then
    err client_missing "${name}"
    exit 1
  fi
  if [[ -f "${dir}/${name}.crt" ]]; then
    # Revoked certificates fail the next handshake, the CRL is read on each one.
    openssl ca -config "${PKI_DIR}/openssl.cnf" -revoke "${dir}/${name}.crt" >/dev/null 2>&1 || true
    pki_gencrl
  fi
  rm -rf "${dir}"
  info client_deleted "${name}"
  apply_client_change
}

client_list() {
  require_exit
  local dir name found=0
  printf '%-20s %-16s %s\n' "NAME" "WIREGUARD" "OPENVPN"
  for dir in "${CLIENTS_DIR}"/*/; do
    [[ -d "${dir}" ]] || continue
    name="$(basename "${dir}")"
    printf '%-20s %-16s %s\n' "${name}" "$(cat "${dir}wg.ip" 2>/dev/null || echo -)" \
      "$([[ -f "${dir}${name}.ovpn" ]] && echo yes || echo -)"
    found=1
  done
  [[ ${found} -eq 1 ]] || { T no_clients; echo; }
}

client_show() {
  local name="${1:-}"
  require_exit
  validate_client_name "${name}"
  local dir="${CLIENTS_DIR}/${name}"
  if [[ ! -d "${dir}" ]]; then
    err client_missing "${name}"
    exit 1
  fi
  if [[ -f "${dir}/wg.conf" ]]; then
    echo "===== WireGuard: ${dir}/wg.conf ====="
    cat "${dir}/wg.conf"
    if command -v qrencode >/dev/null 2>&1; then
      qrencode -t ansiutf8 < "${dir}/wg.conf"
    else
      info qr_hint
    fi
  fi
  if [[ -f "${dir}/${name}.ovpn" ]]; then
    echo "===== OpenVPN: ${dir}/${name}.ovpn ====="
  fi
}

# --------------------------------
# Manage
# --------------------------------
compose() {
  ensure_compose_cmd
  if [[ -z "${DOCKER_COMPOSE}" ]]; then
    err compose_failed
    exit 1
  fi
  ${DOCKER_COMPOSE} -f "${COMPOSE_FILE}" "$@"
}

# wait_client_ifaces waits for the exit's client interfaces.
wait_client_ifaces() {
  [[ "${ROLE}" == "exit" ]] || return 0
  local iface _
  for iface in "${WG_ACCESS_IFACE}" "${OVPN_ACCESS_IFACE}"; do
    [[ "${iface}" == "${WG_ACCESS_IFACE}" && "${WG_ACCESS}" != "yes" ]] && continue
    [[ "${iface}" == "${OVPN_ACCESS_IFACE}" && "${OVPN_ACCESS}" != "yes" ]] && continue
    for _ in $(seq 1 15); do
      ip link show "${iface}" >/dev/null 2>&1 && break
      sleep 1
    done
    if ip link show "${iface}" >/dev/null 2>&1; then
      info iface_ready "${iface}"
    else
      warn iface_fail "${iface}"
    fi
  done
}

start_services() {
  need_root
  require_install
  prepare_host
  compose up -d
  info started
  wait_client_ifaces
}

stop_services() {
  need_root
  require_install
  compose down || true
  info stopped
}

restart_services() {
  need_root
  require_install
  prepare_host
  # Recreate so a changed config.yaml or compose file is picked up.
  compose up -d --force-recreate
  info restarted
  wait_client_ifaces
}

update_image() {
  need_root
  require_install
  compose pull
  compose up -d
  info updated_image
}

show_status() {
  require_install
  echo "=== Container ==="
  compose ps || true
  if [[ "${ROLE}" == "exit" ]]; then
    echo
    echo "=== Interfaces ==="
    local iface
    for iface in "${WG_ACCESS_IFACE}" "${OVPN_ACCESS_IFACE}"; do
      ip -brief addr show "${iface}" 2>/dev/null || true
    done
    echo
    echo "=== Clients ==="
    client_list
  fi
  echo
  echo "=== Settings ==="
  echo "Role: ${ROLE}"
  echo "Lines: ${LINE1_PROTO}/${LINE2_PROTO}, high traffic ${HIGH_TRAFFIC_MODE}, threshold ${THRESHOLD} bps"
  if [[ "${ROLE}" == "entry" ]]; then
    echo "Lines to: ${LINE1_ADDR}, ${LINE2_ADDR}"
    [[ "${WG_ACCESS}" == "yes" ]] && echo "WireGuard relay: ${WG_ACCESS_PORT}/udp"
    [[ "${OVPN_ACCESS}" == "yes" ]] && echo "OpenVPN relay: ${OVPN_PORT}/udp"
  else
    echo "Listen ports: ${LISTEN1_PORT}/${LINE1_PROTO}, ${LISTEN2_PORT}/${LINE2_PROTO}"
    [[ "${WG_ACCESS}" == "yes" ]] && echo "WireGuard: clients ${WG_NET}.0/24 via ${PUBLIC_HOST}:${WG_ACCESS_PORT}/udp"
    [[ "${OVPN_ACCESS}" == "yes" ]] && echo "OpenVPN: clients ${OVPN_NET}.0/24 via ${PUBLIC_HOST}:${OVPN_PORT}/udp"
    echo "Client routes: ${CLIENT_ROUTES}, DNS ${CLIENT_DNS}"
  fi
  echo "Directory: ${BASE_DIR}"
}

show_keys() {
  require_install
  show_peer_info
}

uninstall_flow() {
  need_root
  if ! ask_yes "$(T uninstall_confirm "${BASE_DIR}")" n; then
    T cancelled; echo
    exit 0
  fi
  if [[ -f "${COMPOSE_FILE}" ]]; then
    compose down || true
  fi
  if ask_yes "$(T prompt_backup)" y; then
    local backup
    backup="/root/udplex-gw-backup-$(date +%Y%m%d%H%M%S).tar.gz"
    tar -czf "${backup}" -C "$(dirname "${BASE_DIR}")" "$(basename "${BASE_DIR}")"
    chmod 600 "${backup}"
    info backed_up "${backup}"
  fi
  rm -rf "${BASE_DIR}"
  rm -f "${SYSCTL_FILE}"
  info uninstalled
}

usage() {
  cat <<EOF
Usage: sudo bash $0 <command>

Commands:
  install                 Configure this server as the entry or the exit (interactive, entry first)
  start | stop | restart  Control the gateway container (restart applies config changes)
  status                  Show container, interfaces, routing and clients
  logs                    Follow container logs
  update                  Pull the latest image and restart
  show-keys               Print the shared line secret (and the entry's access ports)
  client add <name>       Exit: create a client (WireGuard config and/or OpenVPN profile)
  client del <name>       Exit: delete a client and revoke its certificate
  client list             Exit: list clients
  client show <name>      Exit: print a client's WireGuard config (QR code with qrencode)
  set-threshold <bps>     Change the bandwidth threshold, then run restart
  lang <zh|en>            Switch the script language
  uninstall               Stop the gateway and remove ${BASE_DIR}

Files live in ${BASE_DIR}; client files are in ${CLIENTS_DIR}/<name>/.
EOF
}

main() {
  load_settings
  local cmd="${1:-}"
  shift || true
  case "${cmd}" in
    install) install_flow ;;
    uninstall) uninstall_flow ;;
    start) start_services ;;
    stop) stop_services ;;
    restart|reload) restart_services ;;
    status) show_status ;;
    logs) require_install; compose logs -f ;;
    update) update_image ;;
    show-keys) show_keys ;;
    client)
      need_root
      local sub="${1:-}"
      shift || true
      case "${sub}" in
        add) client_add "$@" ;;
        del|delete|rm) client_del "$@" ;;
        list|ls) client_list ;;
        show) client_show "$@" ;;
        *) usage; exit 1 ;;
      esac
      ;;
    set-threshold)
      need_root
      require_install
      if [[ ! "${1:-}" =~ ^[0-9]+$ ]]; then
        usage
        exit 1
      fi
      THRESHOLD="$1"
      save_settings
      render_config
      info threshold_set "${THRESHOLD}"
      ;;
    lang)
      case "${1:-}" in zh|en) LANG_SEL="$1" ;; *) usage; exit 1 ;; esac
      [[ -f "${SETTINGS_FILE}" ]] && save_settings
      info lang_set "${LANG_SEL}"
      ;;
    ""|-h|--help|help) usage ;;
    *)
      err unknown_cmd "${cmd}"
      usage
      exit 1
      ;;
  esac
}

# Sourcing the script (for tests) only defines the functions.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
