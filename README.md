# 🔥 token毀滅者 Token-Devastator

[![CI](https://github.com/s12ryt/s12ryt-token-devastator/actions/workflows/ci.yml/badge.svg)](https://github.com/s12ryt/s12ryt-token-devastator/actions/workflows/ci.yml)
[![Release](https://github.com/s12ryt/s12ryt-token-devastator/actions/workflows/release.yml/badge.svg)](https://github.com/s12ryt/s12ryt-token-devastator/actions/workflows/release.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-orange.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8.svg)](go.mod)

**繁體中文** | [English](#english)

把用不完的 API token 額度燒成灰燼——訂閱制月底剩一堆額度？開多個並發把它燒乾淨。

單一 Go 執行檔，內建繁體中文 Web 面板，支援 OpenAI（新舊協議）與 Claude，可走 HTTP/SOCKS 代理。

## 功能特色

- **三協議支援**
  - OpenAI Responses API（`POST /v1/responses`，新協議）
  - OpenAI Chat Completions（`POST /v1/chat/completions`，舊協議；o 系列模型遇 `max_completion_tokens` 限制會自動回退重發）
  - Claude Messages（`POST /v1/messages`，`x-api-key` + `anthropic-version`）
- **三種消耗策略**（面板即時切換）
  - `both` 雙向燒：逼近上下文窗口的大輸入 ＋ 最大輸出
  - `output` 只燒輸出：小 prompt、大輸出
  - `input` 只燒輸入：大填充 prompt、`max_tokens=16`
- **高並發＋輪次語義**：一輪 ＝ 每個並發槽各完成一次請求（輪間屏障），跑完設定輪數自動停止
- **失敗重試**：指數退避（1s × 2ⁿ，上限 30s，單請求最多 5 次嘗試）；連續失敗 10 次自動停止該任務
- **代理支援**：全域預設＋profile 個別覆蓋（留空＝跟隨全域、`none`＝強制直連）；`http/https/socks5/socks5h`，支援 `user:pass@host:port` 內嵌帳密（http→Basic 認證、socks5→RFC 1929）
- **Web 面板**（繁中、深色主題、go:embed 打包進二進位）
  - 登入認證（預設密碼 `admin`，SHA-256 雜湊儲存，session 僅存記憶體）
  - 多 profile 管理：新增／編輯／刪除／啟動／停止
  - 即時統計（每秒輪詢）：輪次進度、輸入/輸出 token、成功/失敗/重試數、最後錯誤、停止原因
- **配置持久化**：全部設定存 `config.json`，重啟自動載入

## 快速開始

### VPS 一鍵安裝（Ubuntu/Debian，全自動）

```bash
curl -fsSL https://raw.githubusercontent.com/s12ryt/s12ryt-token-devastator/main/install.sh | sudo bash

# 自訂埠：
curl -fsSL https://raw.githubusercontent.com/s12ryt/s12ryt-token-devastator/main/install.sh | sudo bash -s - -p 8080

# 完整移除：
curl -fsSL https://raw.githubusercontent.com/s12ryt/s12ryt-token-devastator/main/install.sh | sudo bash -s - --uninstall
```

腳本會：從 Release 下載預建執行檔（失敗自動備援原始碼編譯）→ 註冊 systemd 服務（開機自啟、當機自動重啟）→ 建立專用系統使用者與 `/etc/token-devastator/config.json`（重複執行＝升級，既有設定保留）。安裝完成即可打開 `http://<VPS IP>:24300`。

> **疑難排解**：若 VPS 對 `/usr/local/bin` 掛載 `noexec`（常見於容器型 VPS，症狀為 systemd `203/EXEC Permission denied`），腳本會自動偵測並改安裝至 `/opt/token-devastator/bin`；也可用環境變數 `TD_BIN_CANDIDATES=/路徑1:/路徑2` 自訂候選位置。可用 `findmnt -T /usr/local/bin -no OPTIONS` 查看掛載選項。

### Docker（ghcr.io 多架構映像）

```bash
docker run -d --name token-devastator \
  -p 24300:24300 \
  -v token-devastator-config:/etc/token-devastator \
  ghcr.io/s12ryt/s12ryt-token-devastator:latest
```

映像為 linux/amd64＋linux/arm64 多架構，基底 distroless（內建 CA 憑證、非 root 執行）。設定檔掛載於 `/etc/token-devastator`（named volume 首次會自動以正確權限初始化；若掛載本機目錄請先 `chown 65532:65532`）。版本標籤：`latest`、`1.2`、`1.2.3` 對應 Release 版本。

### 手動執行

```bash
# 從原始碼建置（需 Go 1.26+）
go build -o token-devastator ./cmd/token-devastator
./token-devastator

# 或從 Release 下載對應平台的執行檔直接執行
```

啟動後打開 `http://localhost:24300`，以預設密碼 `admin` 登入（**強烈建議立即更改**——同網段的人都能嘗試登入你的面板）。

### 命令列參數

| 參數 | 預設 | 說明 |
|------|------|------|
| `-addr` | （沿用 config.json，初始 `0.0.0.0:24300`） | 覆蓋監聽地址，僅影響本次執行 |
| `-config` | `config.json` | 設定檔路徑（不存在會自動建立） |

## 使用流程

1. **登入** → 首次會提示更改預設密碼
2. **全域代理設定**（可選）：填 `http://user:pass@host:port` 或 `socks5://host:port`；`none`＝全部直連
3. **新增 profile**：協議、API 地址、API Key、模型、上下文窗口（支援 `128K`/`128k`/`131072`）、最大輸出 token、並發數（1–1024）、輪次、消耗策略、代理覆蓋
4. 點**啟動**開始燒；面板每秒刷新統計；跑完自動停，可再啟動（統計歸零重跑）

## REST API（面板同款）

| 方法 | 路徑 | 說明 |
|------|------|------|
| POST | `/api/login` | `{password}` → `{token, isDefault}` |
| POST | `/api/password` | `{oldPassword, newPassword}`（≥4 字元）更改密碼 |
| GET/PUT | `/api/settings` | 讀/寫全域 `{proxyUrl}` |
| GET | `/api/profiles` | 列出全部 profiles |
| PUT | `/api/profiles/{id}` | 新增/覆蓋 profile（body＝完整 profile JSON） |
| DELETE | `/api/profiles/{id}` | 刪除（執行中會先停止） |
| POST | `/api/profiles/{id}/start` | 啟動燒毀（執行中回 409） |
| POST | `/api/profiles/{id}/stop` | 停止 |
| GET | `/api/stats` | 各 profile 即時統計 |

除 `/api/login` 外皆需 `Authorization: Bearer <token>`。

## 開發

```bash
go test ./... -count=1   # 單元＋整合測試
go vet ./...
gofmt -l .
```

-push tag 即觸發 Release 建置：`git tag v0.1.0 && git push origin v0.1.0`

## 免責聲明

本工具設計用途是消耗**你自己擁有**的 API 額度（例如月底將過期的訂閱額度）。請遵守各家 API 服務條款；對濫用行為（攻擊他人端點、規避限流條款等）不負任何責任。

## 授權

[MIT](LICENSE) © 2026 s12ryt

---

# English

Burn your leftover API token quota — subscription resetting with a pile of unused credits? Spin up concurrency and burn it all down.

Single Go binary with a built-in web panel, supporting OpenAI (both new and legacy protocols) and Claude, with HTTP/SOCKS proxy support.

## Features

- **Three protocols**
  - OpenAI Responses API (`POST /v1/responses`)
  - OpenAI Chat Completions (`POST /v1/chat/completions`; auto-falls-back to `max_completion_tokens` for o-series models)
  - Claude Messages (`POST /v1/messages`)
- **Three burn strategies** (switchable in the panel)
  - `both`: huge input near the context window + max output
  - `output`: tiny prompt, max output
  - `input`: huge filler prompt, `max_tokens=16`
- **Concurrency with round semantics**: one round = every concurrency slot completes one request (barrier between rounds); auto-stops after the configured rounds
- **Retries**: exponential backoff (1s × 2ⁿ, capped at 30s, max 5 attempts per request); auto-stops a profile after 10 consecutive failures
- **Proxy support**: global default + per-profile override (empty = follow global, `none` = direct); `http/https/socks5/socks5h` with embedded credentials `user:pass@host:port` (http→Basic auth, socks5→RFC 1929)
- **Web panel** (embedded, dark theme)
  - Login (default password `admin`, SHA-256 hashed, in-memory sessions)
  - Multi-profile management: create / edit / delete / start / stop
  - Live stats (1s polling): round progress, input/output tokens, success/failure/retries, last error, stop reason
- **Persistence**: everything saved to `config.json`

## Quick Start

### One-line VPS install (Ubuntu/Debian, fully automated)

```bash
curl -fsSL https://raw.githubusercontent.com/s12ryt/s12ryt-token-devastator/main/install.sh | sudo bash

# Custom port:
curl -fsSL https://raw.githubusercontent.com/s12ryt/s12ryt-token-devastator/main/install.sh | sudo bash -s - -p 8080

# Full uninstall:
curl -fsSL https://raw.githubusercontent.com/s12ryt/s12ryt-token-devastator/main/install.sh | sudo bash -s - --uninstall
```

The script downloads the prebuilt binary from Releases (falls back to building from source), registers a systemd service (auto-start on boot, auto-restart on crash), and creates a dedicated system user plus `/etc/token-devastator/config.json` (re-running upgrades in place; existing settings are preserved). The panel is then at `http://<VPS IP>:24300`.

> **Troubleshooting**: if your VPS mounts `/usr/local/bin` with `noexec` (common on container-based VPS; symptom: systemd `203/EXEC Permission denied`), the script detects it and automatically installs to `/opt/token-devastator/bin` instead. Override candidate paths with `TD_BIN_CANDIDATES=/path1:/path2`. Check mount options with `findmnt -T /usr/local/bin -no OPTIONS`.

### Docker (multi-arch image on ghcr.io)

```bash
docker run -d --name token-devastator \
  -p 24300:24300 \
  -v token-devastator-config:/etc/token-devastator \
  ghcr.io/s12ryt/s12ryt-token-devastator:latest
```

The image is multi-arch (linux/amd64 + linux/arm64) on a distroless base (bundled CA certs, runs as non-root). Config is mounted at `/etc/token-devastator` (a named volume is initialized with correct permissions; for a bind-mounted host directory, `chown 65532:65532` it first). Tags: `latest`, `1.2`, `1.2.3` mirror the releases.

### Manual

```bash
# Build from source (Go 1.26+)
go build -o token-devastator ./cmd/token-devastator
./token-devastator

# Or grab a prebuilt binary from Releases
```

Open `http://localhost:24300` and log in with the default password `admin` (**change it immediately** — anyone on your network can reach the panel).

### CLI flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | (from config.json, initially `0.0.0.0:24300`) | Override listen address for this run |
| `-config` | `config.json` | Config file path (auto-created) |

## Workflow

1. **Log in** → prompted to change the default password
2. **Global proxy** (optional): `http://user:pass@host:port` or `socks5://host:port`; `none` = always direct
3. **Create a profile**: protocol, API base, API key, model, context window (`128K`/`131072`), max output tokens, concurrency (1–1024), rounds, strategy, proxy override
4. Hit **Start**; stats refresh every second; auto-stops when done; restart resets stats

## REST API

See the table in the [Chinese section](#rest-api面板同款) — all endpoints except `/api/login` require `Authorization: Bearer <token>`.

## Development

```bash
go test ./... -count=1
go vet ./...
gofmt -l .
```

Releases are built by pushing a tag: `git tag v0.1.0 && git push origin v0.1.0`

## Disclaimer

This tool is designed to burn API quota **you own** (e.g., expiring subscription credits). Comply with each provider's terms of service. No responsibility is taken for misuse.

## License

[MIT](LICENSE) © 2026 s12ryt
