// token毀滅者面板邏輯
"use strict";

const TOKEN_KEY = "td_token";

const $ = (id) => document.getElementById(id);
const loginView = $("login-view");
const mainView = $("main-view");

let token = localStorage.getItem(TOKEN_KEY) || "";
let profiles = [];          // config.Profile[]
let stats = {};             // id -> Stats
let editingId = null;       // 正在編輯的 profile ID；null=新增
let pollTimer = null;

// ---------- 工具 ----------

function escapeHtml(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[c]);
}

function fmtTokens(n) {
  if (n == null) return "0";
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (n >= 1e4) return (n / 1e3).toFixed(1) + "K";
  return Number(n).toLocaleString("zh-Hant");
}

function showToast(msg) {
  const t = $("toast");
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(showToast._h);
  showToast._h = setTimeout(() => { t.hidden = true; }, 2600);
}

// ---------- API ----------

async function api(path, opts = {}) {
  const headers = { "Content-Type": "application/json", ...(opts.headers || {}) };
  if (token) headers["Authorization"] = "Bearer " + token;
  const resp = await fetch(path, { ...opts, headers });
  let body = {};
  try { body = await resp.json(); } catch { /* 空回應 */ }
  if (resp.status === 401 && path !== "/api/login") {
    logout(false);
    throw new Error("登入已過期，請重新登入");
  }
  if (!resp.ok) throw new Error(body.error || `HTTP ${resp.status}`);
  return body;
}

// ---------- 登入 / 登出 ----------

function logout(clear = true) {
  if (clear) localStorage.removeItem(TOKEN_KEY);
  token = "";
  stopPolling();
  mainView.hidden = true;
  loginView.hidden = false;
}

async function tryRestoreSession() {
  if (!token) return;
  try {
    await api("/api/profiles");
    await enterMainView();
  } catch {
    /* 401 已由 api 處理 */
  }
}

async function enterMainView() {
  loginView.hidden = true;
  mainView.hidden = false;
  await Promise.all([loadProfiles(), loadSettings()]);
  startPolling();
}

// ---------- 全域設定 ----------

async function loadSettings() {
  try {
    const out = await api("/api/settings");
    $("global-proxy").value = out.proxyUrl || "";
  } catch (e) { showToast("載入設定失敗：" + e.message); }
}

async function saveProxy() {
  const val = $("global-proxy").value.trim();
  try {
    await api("/api/settings", { method: "PUT", body: JSON.stringify({ proxyUrl: val }) });
    const msg = $("proxy-msg");
    msg.textContent = val === "none" ? "已儲存：全部直連（none）" : val ? "已儲存全域代理：" + val : "已儲存：不使用代理（留空）";
    msg.hidden = false;
    showToast("全域代理已儲存");
  } catch (e) { showToast("儲存失敗：" + e.message); }
}

// ---------- Profiles ----------

async function loadProfiles() {
  try {
    const out = await api("/api/profiles");
    profiles = out.profiles || [];
    renderProfiles();
  } catch (e) { showToast("載入 profiles 失敗：" + e.message); }
}

const PROTOCOL_LABEL = {
  "openai-chat": "OpenAI Chat",
  "openai-responses": "OpenAI Responses",
  "claude": "Claude",
};
const STRATEGY_LABEL = {
  both: "雙向燒",
  output: "只燒輸出",
  input: "只燒輸入",
};
const STOP_REASON_LABEL = {
  completed: "已完成",
  manual: "手動停止",
  "consecutive-failures": "連續失敗停止",
};

function proxyLabel(p) {
  if (!p.proxyUrl) return "跟隨全域";
  if (p.proxyUrl === "none") return "直連";
  return "自訂代理";
}

function renderProfiles() {
  const list = $("profile-list");
  list.innerHTML = "";
  $("empty-hint").hidden = profiles.length > 0;

  for (const p of profiles) list.appendChild(profileCard(p));
  refreshStatsDom();
}

function profileCard(p) {
  const card = document.createElement("div");
  card.className = "profile-card card";
  card.dataset.id = p.id;
  card.innerHTML = `
    <div class="profile-head">
      <div>
        <div class="profile-title">${escapeHtml(p.name)}</div>
        <div class="profile-sub">${escapeHtml(PROTOCOL_LABEL[p.protocol] || p.protocol)} · ${escapeHtml(p.model)}</div>
        <div class="profile-sub">${escapeHtml(STRATEGY_LABEL[p.strategy] || p.strategy)} · ${p.concurrency} 並發 × ${p.rounds} 輪 · ${escapeHtml(proxyLabel(p))}</div>
      </div>
      <span class="badge stopped" data-stat="badge">未啟動</span>
    </div>
    <div class="progress"><div data-stat="bar"></div></div>
    <div class="stats">
      <div class="stat"><span class="k">輪次</span><span class="v" data-stat="round">–</span></div>
      <div class="stat"><span class="k">成功</span><span class="v" data-stat="ok">0</span></div>
      <div class="stat"><span class="k">輸入 tokens</span><span class="v in" data-stat="in">0</span></div>
      <div class="stat"><span class="k">失敗</span><span class="v" data-stat="failed">0</span></div>
      <div class="stat"><span class="k">輸出 tokens</span><span class="v out" data-stat="out">0</span></div>
      <div class="stat"><span class="k">重試</span><span class="v" data-stat="retries">0</span></div>
    </div>
    <div class="last-error" data-stat="err" hidden></div>
    <div class="profile-actions">
      <button class="btn primary" data-act="start">啟動</button>
      <button class="btn ghost" data-act="stop" hidden>停止</button>
      <button class="btn ghost" data-act="edit">編輯</button>
      <button class="btn danger" data-act="delete">刪除</button>
    </div>`;
  card.querySelector('[data-act="start"]').onclick = () => startProfile(p.id);
  card.querySelector('[data-act="stop"]').onclick = () => stopProfile(p.id);
  card.querySelector('[data-act="edit"]').onclick = () => openProfileDialog(p);
  card.querySelector('[data-act="delete"]').onclick = () => deleteProfile(p);
  return card;
}

// 依最新 stats 更新卡片內數字（避免全量重繪閃爍）
function refreshStatsDom() {
  for (const card of document.querySelectorAll(".profile-card")) {
    const id = card.dataset.id;
    const st = stats[id];
    const p = profiles.find((x) => x.id === id);
    if (!st || !p) continue;

    const badge = card.querySelector('[data-stat="badge"]');
    const bar = card.querySelector('[data-stat="bar"]');
    const total = p.rounds || 1;
    const pct = Math.min(100, Math.round(((st.roundDone || 0) / total) * 100));

    card.classList.toggle("running", !!st.running);
    if (st.running) {
      badge.textContent = `執行中 第 ${(st.round || 1)}/${total} 輪`;
      badge.className = "badge run";
      bar.style.width = pct + "%";
    } else {
      const reason = STOP_REASON_LABEL[st.stopReason] || "已停止";
      const cls = st.stopReason === "completed" ? "done" : st.stopReason === "consecutive-failures" ? "failed" : "stopped";
      badge.textContent = (st.ok || st.failed) ? reason : "未啟動";
      badge.className = "badge " + cls;
      if (st.roundDone) bar.style.width = pct + "%";
    }

    card.querySelector('[data-stat="round"]').textContent = `${st.roundDone || 0} / ${total}`;
    card.querySelector('[data-stat="ok"]').textContent = st.ok || 0;
    card.querySelector('[data-stat="failed"]').textContent = st.failed || 0;
    card.querySelector('[data-stat="retries"]').textContent = st.retries || 0;
    card.querySelector('[data-stat="in"]').textContent = fmtTokens(st.inputTokens);
    card.querySelector('[data-stat="out"]').textContent = fmtTokens(st.outputTokens);

    const errEl = card.querySelector('[data-stat="err"]');
    if (st.lastError) { errEl.textContent = st.lastError; errEl.hidden = false; }
    else errEl.hidden = true;

    card.querySelector('[data-act="start"]').hidden = !!st.running;
    card.querySelector('[data-act="stop"]').hidden = !st.running;
  }
}

async function startProfile(id) {
  try {
    await api(`/api/profiles/${encodeURIComponent(id)}/start`, { method: "POST" });
    showToast(`已啟動：${id}`);
    pollOnce();
  } catch (e) { showToast("啟動失敗：" + e.message); }
}

async function stopProfile(id) {
  try {
    await api(`/api/profiles/${encodeURIComponent(id)}/stop`, { method: "POST" });
    showToast(`已停止：${id}`);
  } catch (e) { showToast("停止失敗：" + e.message); }
}

async function deleteProfile(p) {
  if (!confirm(`確定刪除 profile「${p.name}」（${p.id}）？執行中的任務會一併停止。`)) return;
  try {
    await api(`/api/profiles/${encodeURIComponent(p.id)}`, { method: "DELETE" });
    delete stats[p.id];
    await loadProfiles();
    showToast("已刪除：" + p.id);
  } catch (e) { showToast("刪除失敗：" + e.message); }
}

// ---------- Profile 對話框 ----------

function openProfileDialog(p) {
  editingId = p ? p.id : null;
  $("profile-dlg-title").textContent = p ? "編輯 profile：" + p.id : "新增 profile";
  $("f-id").value = p ? p.id : "";
  $("f-id").disabled = !!p;
  $("f-name").value = p ? p.name : "";
  $("f-protocol").value = p ? p.protocol : "openai-chat";
  $("f-model").value = p ? p.model : "";
  $("f-apibase").value = p ? p.apiBase : "";
  $("f-apikey").value = p ? p.apiKey : "";
  $("f-context").value = p ? p.contextWindow : "128K";
  $("f-maxout").value = p ? p.maxOutputTokens : 4096;
  $("f-concurrency").value = p ? p.concurrency : 4;
  $("f-rounds").value = p ? p.rounds : 1;
  $("f-strategy").value = p ? p.strategy : "both";
  $("f-proxy").value = p ? p.proxyUrl : "";
  $("profile-error").hidden = true;
  $("dlg-profile").showModal();
}

async function submitProfile(ev) {
  ev.preventDefault();
  const errEl = $("profile-error");
  errEl.hidden = true;
  const id = $("f-id").value.trim();
  const body = {
    id,
    name: $("f-name").value.trim(),
    protocol: $("f-protocol").value,
    apiBase: $("f-apibase").value.trim(),
    apiKey: $("f-apikey").value,
    model: $("f-model").value.trim(),
    contextWindow: $("f-context").value.trim(),
    maxOutputTokens: Number($("f-maxout").value),
    concurrency: Number($("f-concurrency").value),
    rounds: Number($("f-rounds").value),
    strategy: $("f-strategy").value,
    proxyUrl: $("f-proxy").value.trim(),
  };
  try {
    await api(`/api/profiles/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(body) });
    $("dlg-profile").close();
    await loadProfiles();
    showToast("已儲存 profile：" + id);
  } catch (e) {
    errEl.textContent = e.message;
    errEl.hidden = false;
  }
}

// ---------- 更改密碼 ----------

async function submitPassword(ev) {
  ev.preventDefault();
  const errEl = $("pw-error");
  errEl.hidden = true;
  const oldPw = $("pw-old").value;
  const newPw = $("pw-new").value;
  if (newPw !== $("pw-confirm").value) {
    errEl.textContent = "兩次輸入的新密碼不一致";
    errEl.hidden = false;
    return;
  }
  try {
    await api("/api/password", { method: "POST", body: JSON.stringify({ oldPassword: oldPw, newPassword: newPw }) });
    $("dlg-password").close();
    showToast("密碼已更改，請以新密碼重新登入");
    logout(); // 改密後 session 全失效
  } catch (e) {
    errEl.textContent = e.message;
    errEl.hidden = false;
  }
}

// ---------- 輪詢 ----------

function startPolling() {
  stopPolling();
  pollTimer = setInterval(pollOnce, 1000);
}

function stopPolling() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
}

async function pollOnce() {
  try {
    const out = await api("/api/stats");
    stats = out.stats || {};
    refreshStatsDom();
  } catch { /* 401 已導向登入；暫時網路錯誤忽略 */ }
}

// ---------- 事件繫結 ----------

$("login-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const errEl = $("login-error");
  errEl.hidden = true;
  try {
    const out = await api("/api/login", {
      method: "POST",
      body: JSON.stringify({ password: $("login-password").value }),
    });
    token = out.token;
    localStorage.setItem(TOKEN_KEY, token);
    $("login-password").value = "";
    await enterMainView();
    if (out.isDefault) {
      setTimeout(() => {
        if (confirm("目前使用預設密碼（admin），建議立即更改。要現在更改密碼嗎？")) {
          $("dlg-password").showModal();
        }
      }, 300);
    }
  } catch (e) {
    errEl.textContent = e.message;
    errEl.hidden = false;
  }
});

$("btn-logout").onclick = () => logout();
$("btn-change-password").onclick = () => {
  $("pw-old").value = "";
  $("pw-new").value = "";
  $("pw-confirm").value = "";
  $("pw-error").hidden = true;
  $("dlg-password").showModal();
};
$("btn-save-proxy").onclick = saveProxy;
$("btn-new-profile").onclick = () => openProfileDialog(null);
$("form-profile").addEventListener("submit", submitProfile);
$("form-password").addEventListener("submit", submitPassword);

// 對話框「取消」按鈕
for (const btn of document.querySelectorAll("[data-close]")) {
  btn.addEventListener("click", () => btn.closest("dialog").close());
}

// 起動：優先嘗試恢復既有 session
loginView.hidden = false;
tryRestoreSession();
