#!/usr/bin/env bash
set -euo pipefail

# UDPlex + WireGuard manager
# Commands:
#   install | uninstall | start | stop | pause | resume | status | logs | show-keys | update | reload | lang <zh|en> | set-threshold <number>

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

WG_DIR="${BASE_DIR}/wireguard"
WG_PRIV="${WG_DIR}/wg_private.key"
WG_PUB="${WG_DIR}/wg_public.key"
LEGACY_WG_DIR="/etc/wireguard"
LEGACY_WG_PRIV="${LEGACY_WG_DIR}/wg_private.key"
LEGACY_WG_PUB="${LEGACY_WG_DIR}/wg_public.key"
WG_IFACE="wg_udplex"
WG_ENDPOINT_LABEL="udplex-peer"

DOCKER_INSTALL_SCRIPT_URL="https://get.docker.com"
UDPlex_IMAGE="ghcr.io/tao08141/udplex:latest"

DOCKER_COMPOSE=""

# --------------------------------
# I18N
# --------------------------------
get_lang() {
  if [[ -f "${LANG_FILE}" ]]; then
    tr -d '\r\n' < "${LANG_FILE}"
  else
    printf '%s' "zh"
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

normalize_lang() {
  local L="${1:-}"
  L="${L//$'\r'/}"
  L="${L//$'\n'/}"
  case "$L" in
    zh|en) ;;
    *) L="zh" ;;
  esac
  printf '%s' "$L"
}

T() {
  local key="$1"; shift || true
  local lang
  lang="$(normalize_lang "${LANG_SEL:-$(get_lang)}")"
  local msg=""
  case "${lang}:${key}" in
    zh:need_root) msg="请使用 root 权限运行此脚本，例如：sudo bash $0 ..." ;;
    en:need_root) msg="Please run this script as root (e.g., sudo bash $0 ...)" ;;
    zh:docker_installed) msg="Docker 已安装。" ;;
    en:docker_installed) msg="Docker is already installed." ;;
    zh:docker_installing) msg="正在安装 Docker..." ;;
    en:docker_installing) msg="Installing Docker..." ;;
    zh:compose_missing) msg="未检测到 docker compose，正在尝试安装 compose 插件；如果失败请手动安装。" ;;
    en:compose_missing) msg="Docker compose not found. Trying to install docker compose plugin (install manually if it fails)." ;;
    zh:compose_failed) msg="自动安装 docker compose 失败，请手动安装后重试。" ;;
    en:compose_failed) msg="Failed to auto-install docker compose. Please install it manually and retry." ;;
    zh:wg_installed) msg="WireGuard 工具已安装。" ;;
    en:wg_installed) msg="WireGuard tools are already installed." ;;
    zh:wg_installing) msg="正在安装 WireGuard 工具..." ;;
    en:wg_installing) msg="Installing WireGuard tools..." ;;
    zh:gen_keys) msg="正在生成 WireGuard 密钥..." ;;
    en:gen_keys) msg="Generating WireGuard keys..." ;;
    zh:keys_exist) msg="WireGuard 密钥已存在，跳过生成。" ;;
    en:keys_exist) msg="WireGuard keys exist. Skipping generation." ;;
    zh:show_pubkey_title) msg="本机 WireGuard 公钥，请发送给对端：" ;;
    en:show_pubkey_title) msg="Local WireGuard public key (share it with the peer):" ;;
    zh:select_role) msg="请选择角色：[1] 入口端(client)  [2] 出口端(server)" ;;
    en:select_role) msg="Select role: [1] Entry (client)  [2] Exit (server)" ;;
    zh:enable_tcp) msg="是否启用 UDP over TCP（通过 TCP 隧道转发 UDP）？(y/N): " ;;
    en:enable_tcp) msg="Enable UDP over TCP (Tunneling UDP over TCP)? (y/N): " ;;
    zh:prompt_line1_proto) msg="线路 1 外层协议 [1] UDP  [2] TCP（默认 1，可自由组合）: " ;;
    en:prompt_line1_proto) msg="Outer protocol for line #1 [1] UDP  [2] TCP (default 1, mix freely): " ;;
    zh:prompt_line2_proto) msg="线路 2 外层协议 [1] UDP  [2] TCP（默认 1，可自由组合）: " ;;
    en:prompt_line2_proto) msg="Outer protocol for line #2 [1] UDP  [2] TCP (default 1, mix freely): " ;;
    zh:invalid_proto) msg="无效协议选择，请输入 1/2、udp 或 tcp。" ;;
    en:invalid_proto) msg="Invalid protocol choice. Enter 1/2, udp, or tcp." ;;
    zh:prompt_high_traffic_mode) msg="大流量阶段策略 [1] 两条线路均衡分流  [2] 全部走单条线路（默认 1）: " ;;
    en:prompt_high_traffic_mode) msg="High-traffic strategy [1] Balance across both lines  [2] Use a single line only (default 1): " ;;
    zh:invalid_high_traffic_mode) msg="无效策略选择，请输入 1/2、balance 或 single。" ;;
    en:invalid_high_traffic_mode) msg="Invalid strategy choice. Enter 1/2, balance, or single." ;;
    zh:prompt_preferred_line) msg="大流量固定走哪条线路 [1] 线路 1  [2] 线路 2（默认 1）: " ;;
    en:prompt_preferred_line) msg="Which line should carry high traffic [1] Line #1  [2] Line #2 (default 1): " ;;
    zh:invalid_preferred_line) msg="无效线路选择，请输入 1 或 2。" ;;
    en:invalid_preferred_line) msg="Invalid line choice. Enter 1 or 2." ;;
    zh:invalid_choice) msg="无效选择。" ;;
    en:invalid_choice) msg="Invalid choice." ;;
    zh:secret_found) msg="检测到已有共享密钥，将继续复用。" ;;
    en:secret_found) msg="Existing shared secret found. Reusing it." ;;
    zh:prompt_secret) msg="设置 UDPlex 鉴权密钥（两端必须一致，留空则自动生成）: " ;;
    en:prompt_secret) msg="Set UDPlex auth secret (must match on both ends, empty to auto-generate): " ;;
    zh:show_secret_title) msg="当前 UDPlex 共享密钥（请复制到对端）:" ;;
    en:show_secret_title) msg="Current UDPlex shared secret (copy this to the peer):" ;;
    zh:using_wg_port) msg="使用固定 WireGuard 端口：%s" ;;
    en:using_wg_port) msg="Using fixed WireGuard port: %s" ;;
    zh:prompt_peer_pub) msg="请输入对端 WireGuard 公钥（在对端执行 install 时可以看到）:" ;;
    en:prompt_peer_pub) msg="Paste the peer WireGuard public key (shown by running install on the peer):" ;;
    zh:bad_pubkey) msg="公钥格式看起来不正确，请重新输入。" ;;
    en:bad_pubkey) msg="Invalid-looking public key. Please paste again." ;;
    zh:compose_written) msg="docker-compose.yml 已生成。" ;;
    en:compose_written) msg="docker-compose.yml generated." ;;
    zh:prompt_line1) msg="线路 1 目标地址（出口服务器 IP:端口，默认端口 9000，例如 1.2.3.4:9000）: " ;;
    en:prompt_line1) msg="Forward line #1 target (exit server IP:port, default 9000, e.g. 1.2.3.4:9000): " ;;
    zh:prompt_line2) msg="线路 2 目标地址（出口服务器 IP:端口，默认端口 9001，例如 1.2.3.4:9001）: " ;;
    en:prompt_line2) msg="Forward line #2 target (exit server IP:port, default 9001, e.g. 1.2.3.4:9001): " ;;
    zh:need_two_lines) msg="必须同时提供两条线路的目标地址。" ;;
    en:need_two_lines) msg="Both forward line targets are required." ;;
    zh:client_cfg_written) msg="高级客户端 config.yaml 已生成。" ;;
    en:client_cfg_written) msg="Advanced client config.yaml generated." ;;
    zh:prompt_server_p1) msg="服务端线路 1 监听端口（默认 9000）: " ;;
    en:prompt_server_p1) msg="Server listen port for line #1 (default 9000): " ;;
    zh:prompt_server_p2) msg="服务端线路 2 监听端口（默认 9001）: " ;;
    en:prompt_server_p2) msg="Server listen port for line #2 (default 9001): " ;;
    zh:prompt_server_wg) msg="WireGuard 端口（默认 51820）: " ;;
    en:prompt_server_wg) msg="WireGuard server port (default 51820): " ;;
    zh:server_cfg_written) msg="高级服务端 config.yaml 已生成。" ;;
    en:server_cfg_written) msg="Advanced server config.yaml generated." ;;
    zh:prompt_client_addr) msg="WireGuard 本机地址（默认 10.0.0.1/24）: " ;;
    en:prompt_client_addr) msg="WireGuard local address (default 10.0.0.1/24): " ;;
    zh:prompt_client_peer) msg="WireGuard 对端地址（默认 10.0.0.2）: " ;;
    en:prompt_client_peer) msg="WireGuard peer address (default 10.0.0.2): " ;;
    zh:wg_client_written) msg="内嵌 WireGuard 客户端配置已写入 UDPlex config.yaml。" ;;
    en:wg_client_written) msg="Embedded WireGuard client config written into UDPlex config.yaml." ;;
    zh:prompt_server_addr) msg="WireGuard 本机地址（默认 10.0.0.2/24）: " ;;
    en:prompt_server_addr) msg="WireGuard local address (default 10.0.0.2/24): " ;;
    zh:prompt_server_peer) msg="WireGuard 对端地址（默认 10.0.0.1）: " ;;
    en:prompt_server_peer) msg="WireGuard peer address (default 10.0.0.1): " ;;
    zh:wg_server_written) msg="内嵌 WireGuard 服务端配置已写入 UDPlex config.yaml。" ;;
    en:wg_server_written) msg="Embedded WireGuard server config written into UDPlex config.yaml." ;;
    zh:prompt_threshold) msg="设置带宽阈值（bps，默认 50000000）: " ;;
    en:prompt_threshold) msg="Set bandwidth threshold (bps, default 50000000): " ;;
    zh:threshold_saved) msg="带宽阈值已设置为 %s bps" ;;
    en:threshold_saved) msg="Bandwidth threshold set to %s bps" ;;
    zh:install_done) msg="安装准备完成。现在可以执行：sudo bash $0 start" ;;
    en:install_done) msg="Installation prepared. You can now run: sudo bash $0 start" ;;
    zh:start_udplex) msg="UDPlex 容器已启动。" ;;
    en:start_udplex) msg="UDPlex container started." ;;
    zh:start_wg_ok) msg="内嵌 WireGuard 接口已就绪。" ;;
    en:start_wg_ok) msg="Embedded WireGuard interface is up." ;;
    zh:start_wg_fail) msg="内嵌 WireGuard 接口未就绪，请检查容器日志、/dev/net/tun 与配置。" ;;
    en:start_wg_fail) msg="Embedded WireGuard interface is not ready. Check container logs, /dev/net/tun, and config." ;;
    zh:wg_enable_boot) msg="容器已设置自动重启，内嵌 WireGuard 会随 UDPlex 一起启动。" ;;
    en:wg_enable_boot) msg="Container restart policy is enabled; embedded WireGuard starts with UDPlex." ;;
    zh:stopped_all) msg="UDPlex 与内嵌 WireGuard 已停止。" ;;
    en:stopped_all) msg="UDPlex and embedded WireGuard stopped." ;;
    zh:paused_wg) msg="内嵌 WireGuard 接口已暂停（link down），UDPlex 容器保持运行。" ;;
    en:paused_wg) msg="Embedded WireGuard interface paused (link down). UDPlex container remains running." ;;
    zh:wg_not_running) msg="内嵌 WireGuard 接口未运行。" ;;
    en:wg_not_running) msg="Embedded WireGuard interface is not running." ;;
    zh:resumed_wg) msg="内嵌 WireGuard 接口已恢复（link up）。" ;;
    en:resumed_wg) msg="Embedded WireGuard interface resumed (link up)." ;;
    zh:wg_start_failed) msg="恢复内嵌 WireGuard 接口失败，请检查配置或容器状态。" ;;
    en:wg_start_failed) msg="Failed to resume embedded WireGuard interface. Check config or container state." ;;
    zh:no_config) msg="未找到配置文件，请先执行：sudo bash $0 install" ;;
    en:no_config) msg="Config not found. Run: sudo bash $0 install first." ;;
    zh:no_compose) msg="未找到 docker-compose.yml。" ;;
    en:no_compose) msg="docker-compose.yml not found." ;;
    zh:logs_follow) msg="正在跟随 UDPlex 容器日志..." ;;
    en:logs_follow) msg="Following UDPlex container logs..." ;;
    zh:updated_image) msg="UDPlex 镜像已更新并重启。" ;;
    en:updated_image) msg="UDPlex image updated and restarted." ;;
    zh:show_local_pub) msg="本机 WireGuard 公钥：" ;;
    en:show_local_pub) msg="Local WireGuard public key:" ;;
    zh:no_local_pub) msg="未找到本机公钥，请先执行 install 生成密钥。" ;;
    en:no_local_pub) msg="No local public key found. Run install to generate keys first." ;;
    zh:uninstall_confirm) msg="将执行卸载操作：\n- 停止 UDPlex 与内嵌 WireGuard\n- 删除 %s 下的文件\n- 保留 /etc/wireguard 密钥（可选删除）\n确认继续？(y/N): " ;;
    en:uninstall_confirm) msg="This will uninstall:\n- Stop UDPlex and embedded WireGuard\n- Remove files under %s\n- WireGuard keys are stored in %s (you can delete or back them up)\nProceed? (y/N): " ;;
    zh:uninstall_cancel) msg="已取消。" ;;
    en:uninstall_cancel) msg="Cancelled." ;;
    zh:removed_base) msg="已删除 %s" ;;
    en:removed_base) msg="%s removed." ;;
    zh:prompt_del_wg) msg="是否同时删除 WireGuard 密钥（/etc/wireguard/wg_private.key, wg_public.key）？(y/N): " ;;
    en:prompt_del_wg) msg="Also delete WireGuard keys (%s, %s)? Choose N to back them up before uninstall. (y/N): " ;;
    zh:deleted_wg) msg="WireGuard 密钥已删除。" ;;
    en:deleted_wg) msg="WireGuard keys deleted." ;;
    zh:kept_wg) msg="已保留 WireGuard 密钥。" ;;
    en:kept_wg) msg="WireGuard keys backed up to: %s" ;;
    zh:wg_keys_migrated) msg="已将旧路径 WireGuard 密钥迁移到：%s" ;;
    en:wg_keys_migrated) msg="Migrated legacy WireGuard keys to: %s" ;;
    zh:uninstall_done) msg="卸载完成。" ;;
    en:uninstall_done) msg="Uninstall completed." ;;
    zh:uninstall_confirm_keys) msg="将执行卸载操作：\n- 停止 UDPlex 与内嵌 WireGuard\n- 删除 %s 下的文件\n- WireGuard 密钥当前位于 %s（可选择删除或备份）\n确认继续？(y/N): " ;;
    en:uninstall_confirm_keys) msg="This will uninstall:\n- Stop UDPlex and embedded WireGuard\n- Remove files under %s\n- WireGuard keys are stored in %s (you can delete or back them up)\nProceed? (y/N): " ;;
    zh:prompt_del_wg_keys) msg="是否同时删除 WireGuard 密钥（%s, %s）？选择 N 将先备份再卸载。(y/N): " ;;
    en:prompt_del_wg_keys) msg="Also delete WireGuard keys (%s, %s)? Choose N to back them up before uninstall. (y/N): " ;;
    zh:kept_wg_backup) msg="WireGuard 密钥已备份到：%s" ;;
    en:kept_wg_backup) msg="WireGuard keys backed up to: %s" ;;
    zh:lang_set) msg="语言已切换为：%s" ;;
    en:lang_set) msg="Language switched to: %s" ;;
    zh:threshold_updated) msg="带宽阈值已更新为 %s bps" ;;
    en:threshold_updated) msg="Bandwidth threshold updated to %s bps" ;;
    zh:threshold_patch_fail) msg="当前配置不包含新的阈值分流规则，请重新执行 install 重建配置。" ;;
    en:threshold_patch_fail) msg="Config does not contain the new threshold rule structure. Please re-run install to rebuild config." ;;
    zh:reload_done) msg="配置已重载（容器已重新创建或重启）。" ;;
    en:reload_done) msg="Configuration reloaded (container recreated/restarted)." ;;
    zh:unknown_cmd) msg="未知命令：%s" ;;
    en:unknown_cmd) msg="Unknown command: %s" ;;
    *) msg="${key}" ;;
  esac
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

migrate_legacy_wg_keys() {
  if [[ -f "${WG_PRIV}" && -f "${WG_PUB}" ]]; then
    return
  fi

  if [[ -f "${LEGACY_WG_PRIV}" && -f "${LEGACY_WG_PUB}" ]]; then
    mkdir -p "${WG_DIR}"
    mv -f "${LEGACY_WG_PRIV}" "${WG_PRIV}"
    mv -f "${LEGACY_WG_PUB}" "${WG_PUB}"
    chmod 600 "${WG_PRIV}" "${WG_PUB}"
    rmdir "${LEGACY_WG_DIR}" >/dev/null 2>&1 || true
    info wg_keys_migrated "${WG_DIR}"
  fi
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
  if command -v wg >/dev/null 2>&1; then
    info wg_installed
  else
    info wg_installing
    local pm
    pm=$(detect_pkg_mgr)
    case "$pm" in
      apt)
        apt-get update -y
        apt-get install -y wireguard-tools
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
  migrate_legacy_wg_keys
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
  migrate_legacy_wg_keys
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

show_shared_secret() {
  local secret="${1:-}"
  if [[ -z "$secret" ]]; then
    return
  fi
  echo
  echo "========================================"
  T show_secret_title; echo
  printf '%s\n' "$secret"
  echo
  echo "========================================"
  echo
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
    devices:
      - /dev/net/tun:/dev/net/tun
    cap_add:
      - NET_ADMIN
    network_mode: host
    logging:
      options:
        max-size: "10m"
        max-file: "3"
YAML
  info compose_written
}

select_line_protocol() {
  local prompt_key="${1}"
  local answer=""
  while true; do
    read -rp "$(T "${prompt_key}")" answer || true
    answer="${answer,,}"
    answer="${answer:-1}"
    case "${answer}" in
      1|u|udp)
        printf '%s' "udp"
        return 0
        ;;
      2|t|tcp)
        printf '%s' "tcp"
        return 0
        ;;
      *)
        warn invalid_proto
        ;;
    esac
  done
}

select_high_traffic_mode() {
  local answer=""
  while true; do
    read -rp "$(T prompt_high_traffic_mode)" answer || true
    answer="${answer,,}"
    answer="${answer:-1}"
    case "${answer}" in
      1|b|balance)
        printf '%s' "balance"
        return 0
        ;;
      2|s|single)
        printf '%s' "single"
        return 0
        ;;
      *)
        warn invalid_high_traffic_mode
        ;;
    esac
  done
}

select_preferred_line() {
  local answer=""
  while true; do
    read -rp "$(T prompt_preferred_line)" answer || true
    answer="${answer:-1}"
    case "${answer}" in
      1|2)
        printf '%s' "${answer}"
        return 0
        ;;
      *)
        warn invalid_preferred_line
        ;;
    esac
  done
}

render_load_balancer_rules() {
  local THRESH="${1}"
  local TAG1="${2}"
  local TAG2="${3}"
  local HIGH_TRAFFIC_MODE="${4:-balance}"
  local PREFERRED_LINE="${5:-1}"

  cat <<YAML
      - rule: "bps <= ${THRESH} || !available_${TAG1} || !available_${TAG2}"
        targets: [${TAG1}, ${TAG2}]
YAML

  if [[ "${HIGH_TRAFFIC_MODE}" == "single" ]]; then
    local PRIMARY_TAG="${TAG1}"
    if [[ "${PREFERRED_LINE}" == "2" ]]; then
      PRIMARY_TAG="${TAG2}"
    fi
    cat <<YAML
      - rule: "(bps > ${THRESH}) && available_${PRIMARY_TAG}"
        targets: [${PRIMARY_TAG}]
YAML
  else
    cat <<YAML
      - rule: "(bps > ${THRESH}) && (seq % 2 == 0) && available_${TAG1} && available_${TAG2}"
        targets: [${TAG1}]
      - rule: "(bps > ${THRESH}) && (seq % 2 == 1) && available_${TAG2} && available_${TAG1}"
        targets: [${TAG2}]
YAML
  fi
}

render_client_outer_service() {
  local TAG="${1}"
  local TARGET_ADDR="${2}"
  local PROTO="${3}"
  local SECRET="${4}"

  if [[ "${PROTO}" == "tcp" ]]; then
    cat <<YAML
  - type: tcp_tunnel_forward
    tag: ${TAG}
    forwarders: [${TARGET_ADDR}:4]
    reconnect_interval: 5
    connection_check_time: 30
    no_delay: true
    detour: [wg_component]
    auth:
      secret: ${SECRET}
      enabled: true
      enable_encryption: false
      heartbeat_interval: 30
YAML
  else
    cat <<YAML
  - type: forward
    tag: ${TAG}
    forwarders: [${TARGET_ADDR}]
    reconnect_interval: 5
    connection_check_time: 30
    detour: [wg_component]
    auth:
      secret: ${SECRET}
      enabled: true
      enable_encryption: false
      heartbeat_interval: 30
YAML
  fi
}

render_server_outer_service() {
  local TAG="${1}"
  local LISTEN_PORT="${2}"
  local PROTO="${3}"
  local SECRET="${4}"

  if [[ "${PROTO}" == "tcp" ]]; then
    cat <<YAML
  - type: tcp_tunnel_listen
    tag: ${TAG}
    listen_addr: 0.0.0.0:${LISTEN_PORT}
    timeout: 120
    replace_old_mapping: false
    no_delay: true
    detour: [wg_component]
    auth:
      secret: ${SECRET}
      enabled: true
      enable_encryption: false
      heartbeat_interval: 30
YAML
  else
    cat <<YAML
  - type: listen
    tag: ${TAG}
    listen_addr: 0.0.0.0:${LISTEN_PORT}
    timeout: 120
    replace_old_mapping: false
    detour: [wg_component]
    auth:
      secret: ${SECRET}
      enabled: true
      enable_encryption: false
      heartbeat_interval: 30
YAML
  fi
}

write_client_config() {
  local LINE1_ADDR="${1}"
  local LINE2_ADDR="${2}"
  local SECRET="${3}"
  local THRESH="${4}"
  local LINE1_PROTO="${5:-udp}"
  local LINE2_PROTO="${6:-udp}"
  local HIGH_TRAFFIC_MODE="${7:-balance}"
  local PREFERRED_LINE="${8:-1}"
  local LOCAL_ADDR="${9}"
  local PEER_ADDR="${10}"
  local PEER_PUBKEY="${11}"
  local PRIV
  PRIV=$(cat "${WG_PRIV}")

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
  - type: wg
    tag: wg_component
    interface_name: ${WG_IFACE}
    mtu: 1420
    addresses: [${LOCAL_ADDR}]
    private_key: ${PRIV}
    reuse_incoming_detour: true
    detour: [load_balancer]
    peers:
      - public_key: ${PEER_PUBKEY}
        endpoint: ${WG_ENDPOINT_LABEL}
        allowed_ips: [${PEER_ADDR}/32]
        persistent_keepalive: 25
$(render_client_outer_service "redundant_forward1" "${LINE1_ADDR}" "${LINE1_PROTO}" "${SECRET}")
$(render_client_outer_service "redundant_forward2" "${LINE2_ADDR}" "${LINE2_PROTO}" "${SECRET}")
  - type: load_balancer
    tag: load_balancer
    window_size: 3
    detour:
$(render_load_balancer_rules "${THRESH}" "redundant_forward1" "redundant_forward2" "${HIGH_TRAFFIC_MODE}" "${PREFERRED_LINE}")
YAML
  info client_cfg_written
}

write_server_config() {
  local LISTEN1_PORT="${1}"
  local LISTEN2_PORT="${2}"
  local SECRET="${3}"
  local THRESH="${4}"
  local LINE1_PROTO="${5:-udp}"
  local LINE2_PROTO="${6:-udp}"
  local HIGH_TRAFFIC_MODE="${7:-balance}"
  local PREFERRED_LINE="${8:-1}"
  local LOCAL_ADDR="${9}"
  local PEER_ADDR="${10}"
  local PEER_PUBKEY="${11}"
  local PRIV
  PRIV=$(cat "${WG_PRIV}")

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
  - type: wg
    tag: wg_component
    interface_name: ${WG_IFACE}
    mtu: 1420
    addresses: [${LOCAL_ADDR}]
    private_key: ${PRIV}
    reuse_incoming_detour: false
    detour: [load_balancer]
    peers:
      - public_key: ${PEER_PUBKEY}
        allowed_ips: [${PEER_ADDR}/32]
$(render_server_outer_service "server_listen1" "${LISTEN1_PORT}" "${LINE1_PROTO}" "${SECRET}")
$(render_server_outer_service "server_listen2" "${LISTEN2_PORT}" "${LINE2_PROTO}" "${SECRET}")
  - type: load_balancer
    tag: load_balancer
    window_size: 3
    detour:
$(render_load_balancer_rules "${THRESH}" "server_listen1" "server_listen2" "${HIGH_TRAFFIC_MODE}" "${PREFERRED_LINE}")
YAML
  info server_cfg_written
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
  show_shared_secret "${SECRET}"

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

  # Outer protocol selection per line
  local LINE1_PROTO="udp"
  local LINE2_PROTO="udp"
  local HIGH_TRAFFIC_MODE="balance"
  local PREFERRED_LINE="1"
  echo
  LINE1_PROTO="$(select_line_protocol prompt_line1_proto)"
  LINE2_PROTO="$(select_line_protocol prompt_line2_proto)"
  HIGH_TRAFFIC_MODE="$(select_high_traffic_mode)"
  if [[ "${HIGH_TRAFFIC_MODE}" == "single" ]]; then
    PREFERRED_LINE="$(select_preferred_line)"
  fi

  # docker-compose
  write_compose_file

  if [[ "$ROLE" == "client" ]]; then
    local LINE1_ADDR LINE2_ADDR
    read -rp "$(T prompt_line1)" LINE1_ADDR
    read -rp "$(T prompt_line2)" LINE2_ADDR
    if [[ -z "$LINE1_ADDR" || -z "$LINE2_ADDR" ]]; then
      err need_two_lines
      exit 1
    fi

    local LOCAL_ADDR PEER_ADDR
    read -rp "$(T prompt_client_addr)" LOCAL_ADDR || true
    LOCAL_ADDR="${LOCAL_ADDR:-10.0.0.1/24}"
    read -rp "$(T prompt_client_peer)" PEER_ADDR || true
    PEER_ADDR="${PEER_ADDR:-10.0.0.2}"
    write_client_config "$LINE1_ADDR" "$LINE2_ADDR" "$SECRET" "$THRESH" "$LINE1_PROTO" "$LINE2_PROTO" "$HIGH_TRAFFIC_MODE" "$PREFERRED_LINE" "$LOCAL_ADDR" "$PEER_ADDR" "$PEER_PUBKEY"
    info wg_client_written
  else
    local LISTEN1_PORT LISTEN2_PORT
    read -rp "$(T prompt_server_p1)" LISTEN1_PORT || true
    LISTEN1_PORT="${LISTEN1_PORT:-9000}"
    read -rp "$(T prompt_server_p2)" LISTEN2_PORT || true
    LISTEN2_PORT="${LISTEN2_PORT:-9001}"

    local LOCAL_ADDR PEER_ADDR
    read -rp "$(T prompt_server_addr)" LOCAL_ADDR || true
    LOCAL_ADDR="${LOCAL_ADDR:-10.0.0.2/24}"
    read -rp "$(T prompt_server_peer)" PEER_ADDR || true
    PEER_ADDR="${PEER_ADDR:-10.0.0.1}"
    write_server_config "$LISTEN1_PORT" "$LISTEN2_PORT" "$SECRET" "$THRESH" "$LINE1_PROTO" "$LINE2_PROTO" "$HIGH_TRAFFIC_MODE" "$PREFERRED_LINE" "$LOCAL_ADDR" "$PEER_ADDR" "$PEER_PUBKEY"
    info wg_server_written
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

  local ready="0"
  for _ in $(seq 1 15); do
    if ip link show "${WG_IFACE}" >/dev/null 2>&1; then
      ready="1"
      break
    fi
    sleep 1
  done

  if [[ "${ready}" == "1" ]]; then
    info start_wg_ok
  else
    warn start_wg_fail
  fi

  info wg_enable_boot
}

stop_services() {
  need_root
  ensure_compose_cmd
  if [[ -f "${COMPOSE_FILE}" ]]; then
    $DOCKER_COMPOSE -f "${COMPOSE_FILE}" down || true
  fi
  info stopped_all
}

pause_wg() {
  need_root
  if ip link show "${WG_IFACE}" >/dev/null 2>&1; then
    ip link set dev "${WG_IFACE}" down || true
    info paused_wg
  else
    info wg_not_running
  fi
}

resume_wg() {
  need_root
  if ip link show "${WG_IFACE}" >/dev/null 2>&1 && ip link set dev "${WG_IFACE}" up; then
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
  echo "=== Interface ==="
  ip addr show "${WG_IFACE}" 2>/dev/null || echo "${WG_IFACE} not present."
  echo
  echo "=== Listening Ports ==="
  if command -v ss >/dev/null 2>&1; then
    ss -lunpt | grep -E ":(9000|9001)\b" || true
  else
    netstat -tulpn | grep -E ":(9000|9001)\b" || true
  fi
  echo
  echo "=== Meta ==="
  if [[ -f "${ROLE_FILE}" ]]; then
    echo "Role: $(cat "${ROLE_FILE}")"
  else
    echo "Role: (unset)"
  fi
  echo "BASE_DIR: ${BASE_DIR}"
  echo "WG_DIR: ${WG_DIR}"
  echo "COMPOSE_FILE: ${COMPOSE_FILE}"
  echo "CONFIG_FILE: ${CONFIG_FILE}"
  echo "WG_IFACE: ${WG_IFACE}"
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
  migrate_legacy_wg_keys
  if [[ -f "${WG_PUB}" ]]; then
    T show_local_pub; echo
    cat "${WG_PUB}"
    echo
  else
    err no_local_pub
  fi
}

backup_wg_keys() {
  local backup_dir="/root/udplex-wireguard-backup-$(date +%Y%m%d%H%M%S)"
  mkdir -p "${backup_dir}"

  if [[ -f "${WG_PRIV}" ]]; then
    mv -f "${WG_PRIV}" "${backup_dir}/wg_private.key"
  fi
  if [[ -f "${WG_PUB}" ]]; then
    mv -f "${WG_PUB}" "${backup_dir}/wg_public.key"
  fi

  printf '%s' "${backup_dir}"
}

# --------------------------------
# Threshold update (patch existing config)
# --------------------------------
patch_threshold_in_config() {
  local NEW="${1}"
  if ! grep -q 'rule: "bps <=' "${CONFIG_FILE}" 2>/dev/null || ! grep -q 'rule: "(bps > ' "${CONFIG_FILE}" 2>/dev/null; then
    err threshold_patch_fail
    exit 1
  fi
  sed -i -E "s/(rule: \"bps <= )([0-9]+)/\1${NEW}/" "${CONFIG_FILE}"
  sed -i -E "s/(rule: \"\(bps > )([0-9]+)/\1${NEW}/" "${CONFIG_FILE}"
}

# --------------------------------
# Uninstall
# --------------------------------
uninstall_flow() {
  need_root
  printf "%b" "$(T uninstall_confirm_keys "${BASE_DIR}" "${WG_DIR}")"
  read -r ans
  ans="${ans:-N}"
  if [[ ! "$ans" =~ ^[Yy]$ ]]; then
    echo "$(T uninstall_cancel)"
    exit 0
  fi

  stop_services

  printf "$(T prompt_del_wg_keys "${WG_PRIV}" "${WG_PUB}")"
  read -r delwg
  delwg="${delwg:-N}"
  if [[ "$delwg" =~ ^[Yy]$ ]]; then
    rm -f "${WG_PRIV}" "${WG_PUB}"
    info deleted_wg
  else
    local backup_dir
    backup_dir="$(backup_wg_keys)"
    info kept_wg_backup "${backup_dir}"
  fi

  rm -rf "${BASE_DIR}"
  info removed_base "${BASE_DIR}"

  echo "$(T uninstall_done)"
}

# --------------------------------
# CLI
# --------------------------------
usage() {
  cat <<EOF
Usage: sudo bash $0 <command>

Commands:
  install         Install and configure (with prompts), build advanced config with embedded WireGuard
  uninstall       Uninstall (optionally remove WireGuard keys)
  start           Start UDPlex with embedded WireGuard
  stop            Stop UDPlex and embedded WireGuard
  pause           Pause embedded WireGuard interface (link down), keep UDPlex running
  resume          Resume embedded WireGuard interface (link up)
  status          Show status
  logs            Follow UDPlex logs
  update          Pull latest image and restart
  reload          Reload config (docker compose up -d)
  show-keys       Print local WireGuard public key
  lang <zh|en>    Switch script language
  set-threshold <bps>
                  Update bandwidth threshold in config rules. Then run 'reload'.

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
