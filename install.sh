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
  rm -f "${BIN_PATH}"
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
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq curl ca-certificates >/dev/null
}

# 從 GitHub Release 下載預建執行檔
try_release() {
  local url="${REPO_URL}/releases/latest/download/token-devastator-linux-${ARCH}.tar.gz"
  log "從 Release 下載預建執行檔（linux/${ARCH}）…"
  if ! curl -fsSL --retry 3 --retry-delay 2 -o "${TMP_DIR}/pkg.tar.gz" "${url}"; then
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
    curl -fsSL --retry 3 -o "${TMP_DIR}/go.tar.gz" \
      "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "${TMP_DIR}/go.tar.gz"
  fi
  export PATH="/usr/local/go/bin:${PATH}"
  log "下載原始碼並編譯…"
  curl -fsSL --retry 3 -o "${TMP_DIR}/src.tar.gz" \
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
  log "寫入 systemd 服務…"
  cat > "${UNIT_FILE}" <<EOF
[Unit]
Description=token-devastator (token burner web panel)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${USER_NAME}
Group=${USER_NAME}
ExecStart=${BIN_PATH} -config ${CONF_FILE} -addr ${LISTEN_HOST}:${PORT}
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=${CONF_DIR}
ProtectHome=true
PrivateTmp=true

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

main_install() {
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

  local was_running=0
  if systemctl is-active --quiet "${SERVICE}"; then
    was_running=1
    log "偵測到既有服務，先停止以升級…"
    systemctl stop "${SERVICE}"
  elif systemctl list-unit-files "${SERVICE}.service" 2>/dev/null | grep -q "${SERVICE}"; then
    : # 已安裝但未運行（升級路徑）
  fi

  install -m 0755 "${TMP_DIR}/token-devastator" "${BIN_PATH}"
  write_unit
  systemctl daemon-reload
  systemctl enable "${SERVICE}" >/dev/null 2>&1
  systemctl start "${SERVICE}"
  [[ ${was_running} -eq 1 ]] || true # stop 過必 start；未運行過也 start（首次安裝）

  # 健康檢查（最多 15 秒）
  log "等待服務就緒…"
  local i
  for i in $(seq 1 15); do
    if curl -fsS --max-time 2 "http://127.0.0.1:${PORT}/" >/dev/null 2>&1; then
      break
    fi
    sleep 1
    [[ ${i} -eq 15 ]] && {
      journalctl -u "${SERVICE}" -n 20 --no-pager || true
      die "服務未能就緒（埠 ${PORT}），請檢查上方日誌。"
    }
  done

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
