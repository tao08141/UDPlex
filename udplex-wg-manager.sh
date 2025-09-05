#!/usr/bin/env bash
set -euo pipefail

# UDPlex + WireGuard 管理脚本
# Manage UDPlex + WireGuard
# and advanced smart traffic control.
#
# Commands:
#   install | uninstall | start | stop | pause | resume | status | logs | show-keys | update | reload | lang <zh|en> | set-threshold <number>
#
# Examples:
#   sudo bash udplex-wg-manager.sh install
#   sudo bash udplex-wg-manager.sh lang en
#   sudo bash udplex-wg-manager.sh set-threshold 80000000
#   sudo bash udplex-wg-manager.sh start

# --------------------------------
# Global
# --------------------------------
BASE_DIR="/opt/udplex"
COMPOSE_FILE="${BASE_DIR}/docker-compose.yml"
CONFIG_FILE="${BASE_DIR}/config.yaml"
ROLE_FILE="${BASE_DIR}/role"
SECRET_FILE="${BASE_DIR}/secret"
LANG_FILE="${BASE_DIR}/lang"
THRESHOLD_FILE="${BASE_DIR}/threshold"

WG_DIR="/etc/wireguard"
WG_CONF="${WG_DIR}/wg0.conf"
WG_PRIV="${WG_DIR}/wg_private.key"
WG_PUB="${WG_DIR}/wg_public.key"

DOCKER_INSTALL_SCRIPT_URL="https://get.docker.com"
UDPlex_IMAGE="ghcr.io/tao08141/udplex:latest"

DOCKER_COMPOSE=""

# --------------------------------
# I18N
# --------------------------------
get_lang() {
  if [[ -f "${LANG_FILE}" ]]; then
    cat "${LANG_FILE}"
  else
    echo "zh"
  fi
}

set_lang_file() {
  local L="${1:-zh}"
  if [[ "$L" != "zh" && "$L" != "en" ]]; then
    L="zh"
  fi
  mkdir -p "${BASE_DIR}"
  echo -n "$L" > "${LANG_FILE}"
}

LANG_SEL="$(get_lang)"

T() {
  # t KEY [arg1 arg2 ...]
  local key="$1"; shift || true
  local L="${LANG_SEL}"
  local msg=""
  case "${key}|${L}" in
    "need_root|zh") msg="请使用 root 权限运行本脚本（例如：sudo bash $0 ...）";;
    "need_root|en") msg="Please run this script as root (e.g., sudo bash $0 ...)";;

    "docker_installed|zh") msg="Docker 已安装";;
    "docker_installed|en") msg="Docker is already installed.";;
    "docker_installing|zh") msg="开始安装 Docker...";;
    "docker_installing|en") msg="Installing Docker...";;
    "compose_missing|zh") msg="未检测到 docker compose，尝试安装 docker compose 插件（如失败请手动安装）。";;
    "compose_missing|en") msg="Docker compose not found. Trying to install docker compose plugin (install manually if it fails).";;
    "compose_failed|zh") msg="无法自动安装 docker compose，请手动安装后重试。";;
    "compose_failed|en") msg="Failed to auto-install docker compose. Please install it manually and retry.";;
    "wg_installed|zh") msg="WireGuard 已安装";;
    "wg_installed|en") msg="WireGuard is already installed.";;
    "wg_installing|zh") msg="开始安装 WireGuard...";;
    "wg_installing|en") msg="Installing WireGuard...";;

    "gen_keys|zh") msg="生成 WireGuard 密钥...";;
    "gen_keys|en") msg="Generating WireGuard keys...";;
    "keys_exist|zh") msg="已存在 WireGuard 密钥，跳过生成。";;
    "keys_exist|en") msg="WireGuard keys exist. Skipping generation.";;
    "show_pubkey_title|zh") msg="本机 WireGuard 公钥（请复制给对端机器）：";;
    "show_pubkey_title|en") msg="Local WireGuard public key (share it with the peer):";;

    "select_role|zh") msg="请选择角色：[1] 入口机器(client)  [2] 出口机器(server)";;
    "select_role|en") msg="Select role: [1] Entry (client)  [2] Exit (server)";;
    "invalid_choice|zh") msg="无效选择";;
    "invalid_choice|en") msg="Invalid choice.";;

    "secret_found|zh") msg="已检测到已存在的共享密钥，将沿用。";;
    "secret_found|en") msg="Existing shared secret found. Reusing it.";;
    "prompt_secret|zh") msg="设置 UDPlex 鉴权密钥（两端必须一致，留空使用随机生成）: ";;
    "prompt_secret|en") msg="Set UDPlex auth secret (must match on both ends, empty to auto-generate): ";;

    "prompt_peer_pub|zh") msg="请粘贴对端 WireGuard 公钥（可在对端执行本脚本 install 时打印的公钥）：";;
    "prompt_peer_pub|en") msg="Paste the peer WireGuard public key (shown by running install on the peer):";;
    "bad_pubkey|zh") msg="公钥看起来不正确，请重新粘贴。";;
    "bad_pubkey|en") msg="Invalid-looking public key. Please paste again.";;

    "compose_written|zh") msg="已生成 docker-compose.yml";;
    "compose_written|en") msg="docker-compose.yml generated.";;

    "prompt_client_wg_port|zh") msg="客户端本地 UDPlex 监听端口（用于 WireGuard 出口，默认 7000）: ";;
    "prompt_client_wg_port|en") msg="Client local UDPlex listen port (for WireGuard egress, default 7000): ";;
    "prompt_line1|zh") msg="转发线路1的目标地址（出口服务器IP:端口，默认 9000 端口，例如 1.2.3.4:9000）: ";;
    "prompt_line1|en") msg="Forward line #1 target (exit server IP:port, default 9000, e.g. 1.2.3.4:9000): ";;
    "prompt_line2|zh") msg="转发线路2的目标地址（出口服务器IP:端口，默认 9001 端口，例如 1.2.3.4:9001）: ";;
    "prompt_line2|en") msg="Forward line #2 target (exit server IP:port, default 9001, e.g. 1.2.3.4:9001): ";;
    "need_two_lines|zh") msg="必须提供两条线路的目标地址。";;
    "need_two_lines|en") msg="Both forward line targets are required.";;
    "client_cfg_written|zh") msg="已生成客户端进阶配置 config.yaml";;
    "client_cfg_written|en") msg="Advanced client config.yaml generated.";;

    "prompt_server_p1|zh") msg="服务端线路1监听端口（默认 9000）: ";;
    "prompt_server_p1|en") msg="Server listen port for line #1 (default 9000): ";;
    "prompt_server_p2|zh") msg="服务端线路2监听端口（默认 9001）: ";;
    "prompt_server_p2|en") msg="Server listen port for line #2 (default 9001): ";;
    "prompt_server_wg|zh") msg="WireGuard 服务端端口（默认 51820）: ";;
    "prompt_server_wg|en") msg="WireGuard server port (default 51820): ";;
    "server_cfg_written|zh") msg="已生成服务端进阶配置 config.yaml";;
    "server_cfg_written|en") msg="Advanced server config.yaml generated.";;

    "prompt_client_addr|zh") msg="WireGuard 本机地址（默认 10.0.0.1/24）: ";;
    "prompt_client_addr|en") msg="WireGuard local address (default 10.0.0.1/24): ";;
    "prompt_client_peer|zh") msg="WireGuard 对端地址（默认 10.0.0.2）: ";;
    "prompt_client_peer|en") msg="WireGuard peer address (default 10.0.0.2): ";;
    "wg_client_written|zh") msg="已生成 WireGuard 客户端配置 /etc/wireguard/wg0.conf";;
    "wg_client_written|en") msg="WireGuard client config /etc/wireguard/wg0.conf generated.";;

    "prompt_server_addr|zh") msg="WireGuard 本机地址（默认 10.0.0.2/24）: ";;
    "prompt_server_addr|en") msg="WireGuard local address (default 10.0.0.2/24): ";;
    "prompt_server_peer|zh") msg="WireGuard 对端地址（默认 10.0.0.1）: ";;
    "prompt_server_peer|en") msg="WireGuard peer address (default 10.0.0.1): ";;
    "wg_server_written|zh") msg="已生成 WireGuard 服务端配置 /etc/wireguard/wg0.conf";;
    "wg_server_written|en") msg="WireGuard server config /etc/wireguard/wg0.conf generated.";;

    "prompt_threshold|zh") msg="设置带宽阈值（bps，默认 50000000）：";;
    "prompt_threshold|en") msg="Set bandwidth threshold (bps, default 50000000): ";;
    "threshold_saved|zh") msg="带宽阈值已设置为 %s bps";;
    "threshold_saved|en") msg="Bandwidth threshold set to %s bps";;

    "install_done|zh") msg="安装准备完成。现在可执行：sudo bash $0 start";;
    "install_done|en") msg="Installation prepared. You can now run: sudo bash $0 start";;

    "start_udplex|zh") msg="UDPlex 容器已启动。";;
    "start_udplex|en") msg="UDPlex container started.";;
    "start_wg_ok|zh") msg="WireGuard 已启动。";;
    "start_wg_ok|en") msg="WireGuard started.";;
    "start_wg_fail|zh") msg="WireGuard 启动失败，请检查配置与公钥是否正确。";;
    "start_wg_fail|en") msg="WireGuard failed to start. Check config and public keys.";;
    "wg_enable_boot|zh") msg="已设置 WireGuard 开机自启。";;
    "wg_enable_boot|en") msg="WireGuard enabled on boot.";;

    "stopped_all|zh") msg="已停止 UDPlex 与 WireGuard。";;
    "stopped_all|en") msg="UDPlex and WireGuard stopped.";;
    "paused_wg|zh") msg="已暂停（关闭）WireGuard 接口 wg0。UDPlex 容器保持运行。";;
    "paused_wg|en") msg="WireGuard interface wg0 paused (down). UDPlex container remains running.";;
    "wg_not_running|zh") msg="WireGuard wg0 未运行。";;
    "wg_not_running|en") msg="WireGuard wg0 is not running.";;
    "resumed_wg|zh") msg="已恢复 WireGuard 接口 wg0。";;
    "resumed_wg|en") msg="WireGuard interface wg0 resumed (up).";;
    "wg_start_failed|zh") msg="WireGuard 启动失败，请检查配置。";;
    "wg_start_failed|en") msg="WireGuard start failed. Please check the config.";;

    "no_config|zh") msg="未找到配置文件，请先运行：sudo bash $0 install";;
    "no_config|en") msg="Config not found. Run: sudo bash $0 install first.";;
    "no_compose|zh") msg="未找到 docker-compose.yml";;
    "no_compose|en") msg="docker-compose.yml not found.";;
    "logs_follow|zh") msg="正在跟随 UDPlex 容器日志...";;
    "logs_follow|en") msg="Following UDPlex container logs...";;

    "updated_image|zh") msg="UDPlex 镜像已更新并重启。";;
    "updated_image|en") msg="UDPlex image updated and restarted.";;

    "show_local_pub|zh") msg="本机 WireGuard 公钥：";;
    "show_local_pub|en") msg="Local WireGuard public key:";;
    "no_local_pub|zh") msg="未找到本机公钥，请先执行 install 生成密钥。";;
    "no_local_pub|en") msg="No local public key found. Run install to generate keys first.";;

    "uninstall_confirm|zh") msg="将执行卸载操作：\n- 停止 UDPlex 容器与 WireGuard\n- 删除 %s 下的文件\n- 保留 /etc/wireguard 配置与密钥（可选择删除）\n确认卸载？(y/N): ";;
    "uninstall_confirm|en") msg="This will uninstall:\n- Stop UDPlex and WireGuard\n- Remove files under %s\n- Keep /etc/wireguard configs and keys (optional removal)\nProceed? (y/N): ";;
    "uninstall_cancel|zh") msg="已取消。";;
    "uninstall_cancel|en") msg="Cancelled.";;
    "removed_base|zh") msg="已删除 %s";;
    "removed_base|en") msg="%s removed.";;
    "prompt_del_wg|zh") msg="是否同时删除 WireGuard 配置与密钥（/etc/wireguard/wg0.conf, wg_private.key, wg_public.key）？(y/N): ";;
    "prompt_del_wg|en") msg="Also delete WireGuard config and keys (/etc/wireguard/wg0.conf, wg_private.key, wg_public.key)? (y/N): ";;
    "deleted_wg|zh") msg="已删除 WireGuard 配置与密钥。";;
    "deleted_wg|en") msg="WireGuard config and keys deleted.";;
    "kept_wg|zh") msg="已保留 WireGuard 配置与密钥。";;
    "kept_wg|en") msg="Kept WireGuard config and keys.";;
    "uninstall_done|zh") msg="卸载完成。";;
    "uninstall_done|en") msg="Uninstall completed.";;

    "lang_set|zh") msg="语言已切换为：%s";;
    "lang_set|en") msg="Language switched to: %s";;

    "threshold_updated|zh") msg="已更新带宽阈值至 %s bps";;
    "threshold_updated|en") msg="Bandwidth threshold updated to %s bps";;
    "threshold_patch_fail|zh") msg="配置文件不包含新规则结构（seq 分流），请先重新 install 以重建配置。";;
    "threshold_patch_fail|en") msg="Config does not contain new rule structure (seq split). Please re-run install to rebuild config.";;

    "reload_done|zh") msg="配置已重新加载（容器重建/重启）。";;
    "reload_done|en") msg="Configuration reloaded (container recreated/restarted).";;

    "unknown_cmd|zh") msg="未知命令：%s";;
    "unknown_cmd|en") msg="Unknown command: %s";;

    *) msg="${key}";;
  esac
  # shellcheck disable=SC2059
  printf -- "$msg" "$@"
}

err() { echo "[ERROR] $(T "$@")" >&2; }
info() { echo "[INFO] $(T "$@")"; }
warn() { echo "[WARN] $(T "$@")"; }

# --------------------------------
# Helpers
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

ensure_dirs() {
  mkdir -p "${BASE_DIR}"
  mkdir -p "${WG_DIR}"
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
    systemctl enable docker >/dev/null 2>&1 || true
    systemctl start docker >/dev/null 2>&1 || true
  fi
  ensure_compose_cmd
  if [[ -z "$DOCKER_COMPOSE" ]]; then
    warn compose_missing
    local pm
    pm=$(detect_pkg_mgr)
    if [[ "$pm" == "apt" ]]; then
      apt-get update -y
      apt-get install -y docker-compose-plugin || true
    elif [[ "$pm" == "dnf" ]]; then
      dnf install -y docker-compose-plugin || true
    elif [[ "$pm" == "yum" ]]; then
      yum install -y docker-compose-plugin || true
    fi
    ensure_compose_cmd
    if [[ -z "$DOCKER_COMPOSE" ]]; then
      err compose_failed
      exit 1
    fi
  fi
}

install_wireguard() {
  if command -v wg >/dev/null 2>&1 && command -v wg-quick >/dev/null 2>&1; then
    info wg_installed
  else
    info wg_installing
    local pm
    pm=$(detect_pkg_mgr)
    case "$pm" in
      apt)
        apt-get update -y
        apt-get install -y wireguard
        ;;
      dnf)
        dnf install -y epel-release || true
        dnf install -y wireguard-tools
        ;;
      yum)
        yum install -y epel-release || true
        yum install -y wireguard-tools
        ;;
      *)
        echo "Please install WireGuard (wireguard or wireguard-tools) manually."
        exit 1
        ;;
    esac
  fi
}

random_secret() {
  openssl rand -base64 32 2>/dev/null || head -c 32 /dev/urandom | base64
}

generate_wg_keys() {
  if [[ -f "$WG_PRIV" && -f "$WG_PUB" ]]; then
    info keys_exist
  else
    info gen_keys
    umask 077
    wg genkey | tee "${WG_PRIV}" | wg pubkey > "${WG_PUB}"
    chmod 600 "${WG_PRIV}" "${WG_PUB}"
  fi
}

show_local_pubkey() {
  if [[ -f "$WG_PUB" ]]; then
    echo
    echo "========================================"
    T show_pubkey_title; echo
    cat "$WG_PUB"
    echo
    echo "========================================"
    echo
  fi
}

validate_pubkey() {
  local key="${1:-}"
  [[ -n "$key" ]] || return 1
  # WireGuard base64 pubkey length ~44 chars
  if [[ ${#key} -lt 40 || ${#key} -gt 60 ]]; then
    return 1
  fi
  return 0
}

# --------------------------------
# Write files
# --------------------------------
write_compose_file() {
  cat > "${COMPOSE_FILE}" <<'YAML'
services:
  udplex:
    image: ghcr.io/tao08141/udplex:latest
    container_name: udplex
    restart: always
    command: ["/app/UDPlex", "-c", "/app/config.yaml"]
    volumes:
      - ./config.yaml:/app/config.yaml
    network_mode: host
    logging:
      options:
        max-size: "10m"
        max-file: "3"
YAML
  info compose_written
}

write_client_config() {
  local LINE1_ADDR="${1}"
  local LINE2_ADDR="${2}"
  local WG_INPUT_PORT="${3}"
  local SECRET="${4}"
  local THRESH="${5}"

  cat > "${CONFIG_FILE}" <<YAML
buffer_size: 1500
queue_size: 10240
worker_count: 4
logging:
  level: info
  format: console
  output_path: stdout
  caller: true
services:
  - type: listen
    tag: wg_input
    listen_addr: 127.0.0.1:${WG_INPUT_PORT}
    timeout: 120
    replace_old_mapping: true
    detour: [load_balancer]
  - type: forward
    tag: redundant_forward1
    forwarders: [${LINE1_ADDR}]
    reconnect_interval: 5
    connection_check_time: 30
    send_keepalive: true
    detour: [wg_input]
    auth:
      secret: ${SECRET}
      enabled: true
      enable_encryption: false
      heartbeat_interval: 30
  - type: forward
    tag: redundant_forward2
    forwarders: [${LINE2_ADDR}]
    reconnect_interval: 5
    connection_check_time: 30
    send_keepalive: true
    detour: [wg_input]
    auth:
      secret: ${SECRET}
      enabled: true
      enable_encryption: false
      heartbeat_interval: 30
  - type: load_balancer
    tag: load_balancer
    window_size: 3
    detour:
      - rule: "bps <= ${THRESH}"
        targets: [redundant_forward1, redundant_forward2]
      - rule: "(bps > ${THRESH}) && (seq % 2 == 0)"
        targets: [redundant_forward1]
      - rule: "(bps > ${THRESH}) && (seq % 2 == 1)"
        targets: [redundant_forward2]
YAML
  info client_cfg_written
}

write_server_config() {
  local LISTEN1_PORT="${1}"
  local LISTEN2_PORT="${2}"
  local WG_PORT="${3}"
  local SECRET="${4}"
  local THRESH="${5}"

  cat > "${CONFIG_FILE}" <<YAML
buffer_size: 1500
queue_size: 10240
worker_count: 4
logging:
  level: info
  format: console
  output_path: stdout
  caller: true
services:
  - type: listen
    tag: server_listen1
    listen_addr: 0.0.0.0:${LISTEN1_PORT}
    timeout: 120
    replace_old_mapping: false
    detour: [wg_forward]
    auth:
      secret: ${SECRET}
      enabled: true
      enable_encryption: false
      heartbeat_interval: 30
  - type: listen
    tag: server_listen2
    listen_addr: 0.0.0.0:${LISTEN2_PORT}
    timeout: 120
    replace_old_mapping: false
    detour: [wg_forward]
    auth:
      secret: ${SECRET}
      enabled: true
      enable_encryption: false
      heartbeat_interval: 30
  - type: forward
    tag: wg_forward
    forwarders: [127.0.0.1:${WG_PORT}]
    reconnect_interval: 5
    connection_check_time: 30
    send_keepalive: false
    detour: [load_balancer]
  - type: load_balancer
    tag: load_balancer
    window_size: 3
    detour:
      - rule: "bps <= ${THRESH}"
        targets: [server_listen1, server_listen2]
      - rule: "(bps > ${THRESH}) && (seq % 2 == 0)"
        targets: [server_listen1]
      - rule: "(bps > ${THRESH}) && (seq % 2 == 1)"
        targets: [server_listen2]
YAML
  info server_cfg_written
}

write_wg_conf_client() {
  local LOCAL_ADDR="${1}"
  local PEER_ADDR="${2}"
  local PEER_PUBKEY="${3}"
  local WG_INPUT_PORT="${4}"

  local PRIV
  PRIV=$(cat "${WG_PRIV}")
  cat > "${WG_CONF}" <<CONF
[Interface]
PrivateKey = ${PRIV}
Address = ${LOCAL_ADDR}

[Peer]
PublicKey = ${PEER_PUBKEY}
Endpoint = 127.0.0.1:${WG_INPUT_PORT}
AllowedIPs = ${PEER_ADDR}/32
PersistentKeepalive = 25
CONF
  chmod 600 "${WG_CONF}"
  info wg_client_written
}

write_wg_conf_server() {
  local LOCAL_ADDR="${1}"
  local PEER_ADDR="${2}"
  local PEER_PUBKEY="${3}"
  local WG_PORT="${4}"

  local PRIV
  PRIV=$(cat "${WG_PRIV}")
  cat > "${WG_CONF}" <<CONF
[Interface]
PrivateKey = ${PRIV}
Address = ${LOCAL_ADDR}
ListenPort = ${WG_PORT}

[Peer]
PublicKey = ${PEER_PUBKEY}
AllowedIPs = ${PEER_ADDR}/32
PersistentKeepalive = 25
CONF
  chmod 600 "${WG_CONF}"
  info wg_server_written
}

# --------------------------------
# Install flow
# --------------------------------
install_flow() {
  need_root
  ensure_dirs
  install_docker
  install_wireguard
  generate_wg_keys
  show_local_pubkey

  # Language selection (optional)
  echo "Language / 语言: [1] English  [2] 中文"
  read -rp "> " lang_sel || true
  case "${lang_sel:-}" in
    1) set_lang_file "en" ;;
    2) set_lang_file "zh" ;;
    *) : ;; # keep default
  esac
  LANG_SEL="$(get_lang)"

  # Threshold
  local default_thresh="50000000"
  local THRESH="${default_thresh}"
  read -rp "$(T prompt_threshold)" inp_thresh || true
  if [[ -n "${inp_thresh:-}" && "${inp_thresh}" =~ ^[0-9]+$ ]]; then
    THRESH="${inp_thresh}"
  fi
  echo -n "${THRESH}" > "${THRESHOLD_FILE}"
  info threshold_saved "${THRESH}"

  # Role
  echo "$(T select_role)"
  read -rp "> " role_sel
  local ROLE
  if [[ "$role_sel" == "1" ]]; then
    ROLE="client"
  elif [[ "$role_sel" == "2" ]]; then
    ROLE="server"
  else
    err invalid_choice
    exit 1
  fi
  echo -n "${ROLE}" > "${ROLE_FILE}"

  # Shared secret
  local SECRET=""
  if [[ -f "${SECRET_FILE}" ]]; then
    info secret_found
    SECRET=$(cat "${SECRET_FILE}")
  else
    local default_secret
    default_secret=$(random_secret)
    read -rp "$(T prompt_secret)" SECRET_INPUT || true
    SECRET="${SECRET_INPUT:-$default_secret}"
    echo -n "${SECRET}" > "${SECRET_FILE}"
  fi

  # Peer public key
  local PEER_PUBKEY=""
  while true; do
    echo
    echo "$(T prompt_peer_pub)"
    read -r PEER_PUBKEY
    if validate_pubkey "${PEER_PUBKEY}"; then
      break
    else
      warn bad_pubkey
    fi
  done

  # docker-compose
  write_compose_file

  if [[ "$ROLE" == "client" ]]; then
    local WG_INPUT_PORT LINE1_ADDR LINE2_ADDR
    read -rp "$(T prompt_client_wg_port)" WG_INPUT_PORT || true
    WG_INPUT_PORT="${WG_INPUT_PORT:-7000}"
    read -rp "$(T prompt_line1)" LINE1_ADDR
    read -rp "$(T prompt_line2)" LINE2_ADDR
    if [[ -z "$LINE1_ADDR" || -z "$LINE2_ADDR" ]]; then
      err need_two_lines
      exit 1
    fi
    write_client_config "$LINE1_ADDR" "$LINE2_ADDR" "$WG_INPUT_PORT" "$SECRET" "$THRESH"

    local LOCAL_ADDR PEER_ADDR
    read -rp "$(T prompt_client_addr)" LOCAL_ADDR || true
    LOCAL_ADDR="${LOCAL_ADDR:-10.0.0.1/24}"
    read -rp "$(T prompt_client_peer)" PEER_ADDR || true
    PEER_ADDR="${PEER_ADDR:-10.0.0.2}"
    write_wg_conf_client "$LOCAL_ADDR" "$PEER_ADDR" "$PEER_PUBKEY" "$WG_INPUT_PORT"
  else
    local LISTEN1_PORT LISTEN2_PORT WG_PORT
    read -rp "$(T prompt_server_p1)" LISTEN1_PORT || true
    LISTEN1_PORT="${LISTEN1_PORT:-9000}"
    read -rp "$(T prompt_server_p2)" LISTEN2_PORT || true
    LISTEN2_PORT="${LISTEN2_PORT:-9001}"
    read -rp "$(T prompt_server_wg)" WG_PORT || true
    WG_PORT="${WG_PORT:-51820}"
    write_server_config "$LISTEN1_PORT" "$LISTEN2_PORT" "$WG_PORT" "$SECRET" "$THRESH"

    local LOCAL_ADDR PEER_ADDR
    read -rp "$(T prompt_server_addr)" LOCAL_ADDR || true
    LOCAL_ADDR="${LOCAL_ADDR:-10.0.0.2/24}"
    read -rp "$(T prompt_server_peer)" PEER_ADDR || true
    PEER_ADDR="${PEER_ADDR:-10.0.0.1}"
    write_wg_conf_server "$LOCAL_ADDR" "$PEER_ADDR" "$PEER_PUBKEY" "$WG_PORT"
  fi

  echo
  info install_done
  echo
  show_local_pubkey
}

# --------------------------------
# Manage
# --------------------------------
start_services() {
  need_root
  ensure_compose_cmd
  if [[ ! -f "${COMPOSE_FILE}" || ! -f "${CONFIG_FILE}" ]]; then
    err no_config
    exit 1
  fi

  $DOCKER_COMPOSE -f "${COMPOSE_FILE}" up -d
  info start_udplex

  if wg-quick up wg0 2>/dev/null; then
    info start_wg_ok
  else
    warn start_wg_fail
  fi

  systemctl enable wg-quick@wg0 >/dev/null 2>&1 || true
  info wg_enable_boot
}

stop_services() {
  need_root
  ensure_compose_cmd
  if wg show wg0 >/dev/null 2>&1; then
    wg-quick down wg0 || true
  fi
  if [[ -f "${COMPOSE_FILE}" ]]; then
    $DOCKER_COMPOSE -f "${COMPOSE_FILE}" down || true
  fi
  info stopped_all
}

pause_wg() {
  need_root
  if wg show wg0 >/dev/null 2>&1; then
    wg-quick down wg0 || true
    info paused_wg
  else
    info wg_not_running
  fi
}

resume_wg() {
  need_root
  if wg-quick up wg0; then
    info resumed_wg
  else
    err wg_start_failed
    exit 1
  fi
}

show_status() {
  ensure_compose_cmd
  echo "=== UDPlex ==="
  if [[ -f "${COMPOSE_FILE}" ]]; then
    $DOCKER_COMPOSE -f "${COMPOSE_FILE}" ps || true
  else
    T no_compose; echo
  fi
  echo
  echo "=== WireGuard ==="
  if command -v wg >/dev/null 2>&1; then
    wg show || true
  else
    echo "WireGuard not installed."
  fi
  echo
  echo "=== Listening Ports ==="
  if command -v ss >/dev/null 2>&1; then
    ss -lunpt | grep -E ":(7000|9000|9001|51820)\b" || true
  else
    netstat -tulpn | grep -E ":(7000|9000|9001|51820)\b" || true
  fi
  echo
  echo "=== Meta ==="
  if [[ -f "${ROLE_FILE}" ]]; then
    echo "Role: $(cat "${ROLE_FILE}")"
  else
    echo "Role: (unset)"
  fi
  echo "BASE_DIR: ${BASE_DIR}"
  echo "COMPOSE_FILE: ${COMPOSE_FILE}"
  echo "CONFIG_FILE: ${CONFIG_FILE}"
  echo "WG_CONF: ${WG_CONF}"
  if [[ -f "${THRESHOLD_FILE}" ]]; then
    echo "Threshold(bps): $(cat "${THRESHOLD_FILE}")"
  else
    echo "Threshold(bps): 50000000 (default)"
  fi
  echo "Language: $(get_lang)"
}

show_logs() {
  ensure_compose_cmd
  if [[ -f "${COMPOSE_FILE}" ]]; then
    T logs_follow; echo
    $DOCKER_COMPOSE -f "${COMPOSE_FILE}" logs -f
  else
    err no_compose
    exit 1
  fi
}

update_image() {
  ensure_compose_cmd
  if [[ -f "${COMPOSE_FILE}" ]]; then
    $DOCKER_COMPOSE -f "${COMPOSE_FILE}" pull
    $DOCKER_COMPOSE -f "${COMPOSE_FILE}" up -d
    info updated_image
  else
    err no_compose
    exit 1
  fi
}

reload_services() {
  ensure_compose_cmd
  if [[ -f "${COMPOSE_FILE}" ]]; then
    $DOCKER_COMPOSE -f "${COMPOSE_FILE}" up -d
    info reload_done
  else
    err no_compose
    exit 1
  fi
}

show_keys() {
  if [[ -f "${WG_PUB}" ]]; then
    T show_local_pub; echo
    cat "${WG_PUB}"
    echo
  else
    err no_local_pub
  fi
}

# --------------------------------
# Threshold update (patch existing config)
# --------------------------------
patch_threshold_in_config() {
  local NEW="${1}"
  if ! grep -q 'seq % 2' "${CONFIG_FILE}" 2>/dev/null; then
    # not new structure
    err threshold_patch_fail
    exit 1
  fi
  # Replace numbers in rules
  # bps <= old -> bps <= NEW
  sed -i -E "s/(\"rule\": \"bps <= )([0-9]+)(\")/\1${NEW}\3/" "${CONFIG_FILE}"
  # bps > old && seq % 2 == 0 -> bps > NEW ...
  sed -i -E "s/(\"rule\": \"bps > )([0-9]+)( && seq % 2 == 0\")/\1${NEW}\3/" "${CONFIG_FILE}"
  # bps > old && seq % 2 == 1 -> bps > NEW ...
  sed -i -E "s/(\"rule\": \"bps > )([0-9]+)( && seq % 2 == 1\")/\1${NEW}\3/" "${CONFIG_FILE}"
}

# --------------------------------
# Uninstall
# --------------------------------
uninstall_flow() {
  need_root
  printf "$(T uninstall_confirm "${BASE_DIR}")"
  read -r ans
  ans="${ans:-N}"
  if [[ ! "$ans" =~ ^[Yy]$ ]]; then
    echo "$(T uninstall_cancel)"
    exit 0
  fi

  stop_services

  rm -rf "${BASE_DIR}"
  info removed_base "${BASE_DIR}"

  printf "$(T prompt_del_wg)"
  read -r delwg
  delwg="${delwg:-N}"
  if [[ "$delwg" =~ ^[Yy]$ ]]; then
    rm -f "${WG_CONF}" "${WG_PRIV}" "${WG_PUB}"
    info deleted_wg
  else
    info kept_wg
  fi

  echo "$(T uninstall_done)"
}

# --------------------------------
# CLI
# --------------------------------
usage() {
  cat <<EOF
Usage: sudo bash $0 <command>

Commands:
  install         Install and configure (with prompts), build advanced config and WireGuard config
  uninstall       Uninstall (optionally remove WireGuard configs and keys)
  start           Start UDPlex and WireGuard (enable WG on boot)
  stop            Stop UDPlex and WireGuard
  pause           Pause WireGuard (down wg0), keep UDPlex running
  resume          Resume WireGuard (up wg0)
  status          Show status
  logs            Follow UDPlex logs
  update          Pull latest image and restart
  reload          Reload config (docker compose up -d)
  show-keys       Print local WireGuard public key
  lang <zh|en>    Switch script language
  set-threshold <bps>
                  Update bandwidth threshold in config rules (seq split). Then run 'reload'.

Examples:
  sudo bash $0 install
  sudo bash $0 lang en
  sudo bash $0 set-threshold 80000000 && sudo bash $0 reload
EOF
}

main() {
  local cmd="${1:-}"
  case "$cmd" in
    install) install_flow ;;
    uninstall) uninstall_flow ;;
    start) start_services ;;
    stop) stop_services ;;
    pause) pause_wg ;;
    resume) resume_wg ;;
    status) show_status ;;
    logs) show_logs ;;
    update) update_image ;;
    reload) reload_services ;;
    show-keys) show_keys ;;
    lang)
      shift || true
      local L="${1:-}"
      if [[ "$L" != "zh" && "$L" != "en" ]]; then
        L="zh"
      fi
      set_lang_file "$L"
      LANG_SEL="$(get_lang)"
      info lang_set "$L"
      ;;
    set-threshold)
      shift || true
      local NEW="${1:-}"
      if [[ -z "$NEW" || ! "$NEW" =~ ^[0-9]+$ ]]; then
        echo "Usage: sudo bash $0 set-threshold <bps>"
        exit 1
      fi
      echo -n "$NEW" > "${THRESHOLD_FILE}"
      if [[ -f "${CONFIG_FILE}" ]]; then
        patch_threshold_in_config "$NEW"
      fi
      info threshold_updated "$NEW"
      ;;
    ""|-h|--help|help)
      usage
      ;;
    *)
      err unknown_cmd "$cmd"
      usage
      exit 1
      ;;
  esac
}

main "$@"