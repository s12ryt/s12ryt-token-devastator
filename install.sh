#!/usr/bin/env bash
# =============================================================================
# token毀滅者 Token-Devastator — VPS 一鍵安裝腳本（全自動無交互）
#
# 用法 Usage:
#   curl -fsSL https://raw.githubusercontent.com/s12ryt/s12ryt-token-devastator/main/install.sh | bash
#   # 自訂埠： | bash -s - -p 8080
#   sudo ./install.sh -p 24300        # 安裝／升級（冪等）
#   sudo ./install.sh --uninstall     # 完整移除
#
# 支援：Ubuntu/Debian（apt）、amd64/arm64、需 root。
# 來源：GitHub Release 預建執行檔優先；失敗自動備援原始碼編譯。
# =============================================================================
set -euo pipefail

REPO="s12ryt/s12ryt-token-devastator"
REPO_URL="https://github.com/${REPO}"
BIN_PATH="/usr/local/bin/token-devastator"
# 候選安裝位置：部分容器型 VPS 對 /usr/local/bin 掛 noexec（execve 回 EACCES，
# systemd 報 203/EXEC Permission denied），安裝後逐一實測 exec，失敗自動換下一個。
# 可用環境變數 TD_BIN_CANDIDATES 覆蓋（冒號分隔）。
BIN_CANDIDATES="${TD_BIN_CANDIDATES:-/usr/local/bin/token-devastator:/opt/token-devastator/bin/token-devastator:/usr/bin/token-devastator}"
CONF_DIR="/etc/token-devastator"
CONF_FILE="${CONF_DIR}/config.json"
UNIT_FILE="/etc/systemd/system/token-devastator.service"
SERVICE="token-devastator"
USER_NAME="token-devastator"
LISTEN_HOST="0.0.0.0"
PORT="24300"
GO_VERSION="1.26.3"
TMP_DIR="$(mktemp -d)"

trap 'rm -rf "${TMP_DIR}"' EXIT

log()  { printf '\033[1;32m[安裝]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[警告]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[錯誤]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
}

# --- 參數解析 ---
ACTION="install"
while [[ $# -gt 0 ]]; do
  case "$1" in
    -p|--port)    PORT="${2:?}"; shift 2 ;;
    --uninstall)  ACTION="uninstall"; shift ;;
    -h|--help)    usage ;;
    *)            die "未知參數：$1（-h 看說明）" ;;
  esac done

[[ "${PORT}" =~ ^[0-9]+$ ]] || die "埠號需為數字，得到：${PORT}"

# --- 環境檢查 ---
[[ $EUID -eq 0 ]] || die "請以 root 執行（sudo bash install.sh）"
command -v apt-get >/dev/null 2>&1 || die "僅支援 Ubuntu/Debian（找不到 apt-get）"

ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  *)       die "不支援的架構：$(uname -m)（僅支援 amd64/arm64）" ;;
esac

# =============================================================================
# 移除模式
# =============================================================================
uninstall() {
  log "停止並移除 systemd 服務…"
  systemctl disable --now "${SERVICE}" >/dev/null 2>&1 || true
  rm -f "${UNIT_FILE}"
  systemctl daemon-reload
  # 清除所有候選位置的執行檔（可能裝在備援位置）
  IFS=':' read -ra _cands <<< "${BIN_CANDIDATES}"
  for p in "${_cands[@]}"; do
    rm -f "${p}"
  done
  rm -rf /opt/token-devastator
  rm -rf "${CONF_DIR}"
  if id -u "${USER_NAME}" >/dev/null 2>&1; then
    userdel "${USER_NAME}" >/dev/null 2>&1 || true
  fi
  log "已完整移除 ${SERVICE}（執行檔／服務／設定檔）。"
}

# =============================================================================
# 安裝模式
# =============================================================================
install_deps() {
  # curl 與 CA 憑證多數發行版預先就有；缺了才動 apt（避免與背景 apt 維護搶鎖）
  if command -v curl >/dev/null 2>&1 && [ -d /etc/ssl/certs ]; then
    return 0
  fi
  export DEBIAN_FRONTEND=noninteractive
  apt-get -o DPkg::Lock::Timeout=120 update -qq
  apt-get -o DPkg::Lock::Timeout=120 install -y -qq curl ca-certificates >/dev/null
}

# 從 GitHub Release 下載預建執行檔
try_release() {
  local url="${REPO_URL}/releases/latest/download/token-devastator-linux-${ARCH}.tar.gz"
  log "從 Release 下載預建執行檔（linux/${ARCH}）…"
  if ! curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 10 --max-time 120 \
      -o "${TMP_DIR}/pkg.tar.gz" "${url}"; then
    return 1
  fi
  tar -xzf "${TMP_DIR}/pkg.tar.gz" -C "${TMP_DIR}"
  [[ -f "${TMP_DIR}/token-devastator-linux-${ARCH}" ]] || return 1
  install -m 0755 "${TMP_DIR}/token-devastator-linux-${ARCH}" "${TMP_DIR}/token-devastator"
}

# 備援：原始碼現場編譯（必要時安裝 Go）
build_from_source() {
  warn "Release 下載失敗，備援：原始碼編譯模式。"
  if ! command -v go >/dev/null 2>&1 && [[ ! -x /usr/local/go/bin/go ]]; then
    log "下載 Go ${GO_VERSION}（linux/${ARCH}）…"
    curl -fsSL --retry 3 --connect-timeout 10 --max-time 600 -o "${TMP_DIR}/go.tar.gz" \
      "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "${TMP_DIR}/go.tar.gz"
  fi
  export PATH="/usr/local/go/bin:${PATH}"
  log "下載原始碼並編譯…"
  curl -fsSL --retry 3 --connect-timeout 10 --max-time 120 -o "${TMP_DIR}/src.tar.gz" \
    "https://github.com/${REPO}/archive/refs/heads/main.tar.gz"
  tar -xzf "${TMP_DIR}/src.tar.gz" -C "${TMP_DIR}"
  ( cd "${TMP_DIR}/${REPO#*/}-main" && go build -trimpath -o "${TMP_DIR}/token-devastator" ./cmd/token-devastator )
}

write_config() {
  mkdir -p "${CONF_DIR}"
  if [[ ! -f "${CONF_FILE}" ]]; then
    log "建立設定檔 ${CONF_FILE}…"
    cat > "${CONF_FILE}" <<'EOF'
{
  "listen": "0.0.0.0:24300",
  "profiles": []
}
EOF
  else
    log "保留既有設定檔 ${CONF_FILE}。"
  fi
}

write_unit() {
  local harden=""
  if [ "${1:-}" != "compat" ]; then
    harden="NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=${CONF_DIR}
ProtectHome=true
PrivateTmp=true"
  fi
  log "寫入 systemd 服務${1:+（相容模式）}…"
  cat > "${UNIT_FILE}" <<EOF
[Unit]
Description=token-devastator (token burner web panel)
After=network.target

[Service]
Type=simple
User=${USER_NAME}
Group=${USER_NAME}
ExecStart=${BIN_PATH} -config ${CONF_FILE} -addr ${LISTEN_HOST}:${PORT}
Restart=on-failure
RestartSec=3
${harden}

[Install]
WantedBy=multi-user.target
EOF
}

ensure_user() {
  if ! id -u "${USER_NAME}" >/dev/null 2>&1; then
    log "建立系統使用者 ${USER_NAME}…"
    useradd --system --shell /usr/sbin/nologin --no-create-home "${USER_NAME}"
  fi
}

# 以「服務使用者」身分實測執行（systemd 即以該 user 執行）。部分環境 root 可
# 執行但服務使用者被拒（目錄 ACL／LSM／目錄權限），只有這層測得出來。
# 重要：一律經 /bin/sh -c 包裹——直接 exec 在 setpriv 路徑下可能殘留有效
# capabilities 而繞過目錄搜尋檢查，造成假通過（實測重現過）；sh 不帶 file caps，
# exec 時 capabilities 會被清空，語義與 systemd 乾淨環境一致。
exec_as_service_user() {
  local p="$1"
  if command -v su >/dev/null 2>&1; then
    su -s /bin/sh -c "exec '${p}' -h" "${USER_NAME}" >/dev/null 2>&1
  elif command -v setpriv >/dev/null 2>&1; then
    setpriv --reuid="$(id -u "${USER_NAME}")" --regid="$(id -g "${USER_NAME}")" \
      --clear-groups -- /bin/sh -c "exec '${p}' -h" >/dev/null 2>&1
  else
    "${p}" -h >/dev/null 2>&1
  fi
}

# 列出路徑逐級權限，輔助排查目錄 ACL／LSM 問題
path_diag() {
  local d="$1" part="" s
  case "${d}" in
    /*) ;;
    *) echo "  ${d}"; return ;;
  esac
  IFS='/' read -ra _segs <<< "${d}"
  for s in "${_segs[@]}"; do
    [ -z "${s}" ] && continue
    part="${part}/${s}"
    ls -ld "${part}" 2>/dev/null | sed 's/^/  /'
  done
}

# 以「面板 HTTP 實際應答」驗證服務就緒——203/EXEC 重啟風暴永遠開不了埠，
# 杜絕 is-active 在重啟循環瞬間 active 的競態誤判
wait_port() {
  local tries="$1" i
  for ((i = 0; i < tries; i++)); do
    if curl -fsS --max-time 2 "http://127.0.0.1:${PORT}/" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

start_unit() {
  systemctl daemon-reload
  systemctl enable "${SERVICE}" >/dev/null 2>&1
  systemctl reset-failed "${SERVICE}" >/dev/null 2>&1 || true
  systemctl start "${SERVICE}"
}

stop_unit() {
  systemctl stop "${SERVICE}" >/dev/null 2>&1 || true
  systemctl reset-failed "${SERVICE}" >/dev/null 2>&1 || true
}

main_install() {
  local p installed=""

  install_deps

  if ! try_release; then
    build_from_source
  fi
  [[ -x "${TMP_DIR}/token-devastator" ]] || die "取得執行檔失敗"

  ensure_user
  write_config
  chown -R "${USER_NAME}:${USER_NAME}" "${CONF_DIR}"
  chmod 750 "${CONF_DIR}"
  chmod 640 "${CONF_FILE}"

  # 停止既有服務（含失敗重啟循環），避免換檔與自動重啟互搶
  log "停止既有服務（若存在）…"
  stop_unit

  # 候選位置逐一：安裝 → 服務使用者 exec 自檢 → systemd 啟動＋HTTP 實測驗證
  IFS=':' read -ra _cands <<< "${BIN_CANDIDATES}"
  for p in "${_cands[@]}"; do
    mkdir -p "$(dirname "${p}")" 2>/dev/null || true
    if ! install -m 0755 "${TMP_DIR}/token-devastator" "${p}" 2>/dev/null; then
      warn "無法寫入 ${p}，嘗試下一個安裝位置…"
      continue
    fi
    if ! exec_as_service_user "${p}"; then
      warn "服務使用者（${USER_NAME}）無法執行 ${p}，嘗試下一個安裝位置…"
      path_diag "${p}"
      rm -f "${p}"
      continue
    fi
    BIN_PATH="${p}"
    write_unit
    start_unit
    if wait_port 10; then
      installed="${p}"
      break
    fi
    warn "服務於 ${p} 啟動後無法應答（埠 ${PORT}），嘗試下一個安裝位置…"
    journalctl -u "${SERVICE}" -n 3 --no-pager 2>/dev/null | sed 's/^/  /' || true
    stop_unit
    rm -f "${p}"
  done

  # 最後手段：相容模式 unit（停用沙箱強化）重試第一個候選
  if [ -z "${installed}" ]; then
    warn "候選位置全數失敗，改以相容模式（停用沙箱強化）重試…"
    p="${_cands[0]}"
    mkdir -p "$(dirname "${p}")" 2>/dev/null || true
    if install -m 0755 "${TMP_DIR}/token-devastator" "${p}" 2>/dev/null \
      && exec_as_service_user "${p}"; then
      BIN_PATH="${p}"
      write_unit compat
      start_unit
      if wait_port 10; then
        installed="${p}"
        warn "已以相容模式啟動（停用 ProtectSystem／NoNewPrivileges 等沙箱強化）。"
      else
        journalctl -u "${SERVICE}" -n 10 --no-pager 2>/dev/null | sed 's/^/  /' || true
        stop_unit
        rm -f "${p}"
      fi
    fi
  fi

  if [ -z "${installed}" ]; then
    echo "診斷資訊（各候選路徑逐級權限）："
    for p in "${_cands[@]}"; do path_diag "${p}"; done
    command -v getenforce >/dev/null 2>&1 && getenforce || true
    die "所有候選位置皆無法以服務使用者執行程式（候選：${BIN_CANDIDATES}）。可用 TD_BIN_CANDIDATES=/其他/路徑/token-devastator 指定位置後重試。"
  fi
  if [ "${installed}" != "/usr/local/bin/token-devastator" ]; then
    warn "已安裝至備援位置 ${installed}"
  fi

  local ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')" || ip=""
  [[ -n "${ip}" ]] || ip="<你的VPS IP>"

  echo
  printf '\033[1;32m============================================\033[0m\n'
  printf '  🔥 token毀滅者 安裝完成！\n'
  printf '\033[1;32m============================================\033[0m\n'
  printf '  面板網址：http://%s:%s\n' "${ip}" "${PORT}"
  printf '  預設密碼：admin（請登入後立即更改！）\n'
  printf '  設定檔：%s\n' "${CONF_FILE}"
  echo
  printf '  常用指令：\n'
  printf '    systemctl status  %s   # 狀態\n' "${SERVICE}"
  printf '    journalctl -u %s -f    # 日誌\n' "${SERVICE}"
  printf '    sudo bash %s --uninstall  # 移除\n' "$0"
  echo
}

# =============================================================================
if [[ "${ACTION}" == "uninstall" ]]; then
  uninstall
else
  main_install
fi
