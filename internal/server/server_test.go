package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"token-devastator/internal/config"
)

// newTestServer 建立綁定臨時 config.json 的 Server 與 httptest 實例。
func newTestServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, path)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, path
}

// login 以指定密碼登入，回傳 token 與 isDefault。
func login(t *testing.T, ts *httptest.Server, password string) (string, bool, int) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": password})
	resp, err := http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, resp.StatusCode
	}
	var out struct {
		Token     string `json:"token"`
		IsDefault bool   `json:"isDefault"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token, out.IsDefault, resp.StatusCode
}

func authReq(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func sampleProfile(id string) config.Profile {
	return config.Profile{
		ID: id, Name: "測試-" + id, Protocol: config.ProtocolOpenAIChat,
		APIBase: "https://example.com", APIKey: "sk-1", Model: "gpt-x",
		ContextWindow: "8K", MaxOutputTokens: 512, Concurrency: 1, Rounds: 1,
		Strategy: config.StrategyOutput,
	}
}

// --- 認證流程 ---

func TestAuthLoginAndGuard(t *testing.T) {
	_, ts, _ := newTestServer(t)

	// 預設密碼登入成功，標記 isDefault
	tok, isDefault, code := login(t, ts, "admin")
	if code != 200 || tok == "" || !isDefault {
		t.Fatalf("admin 登入應成功且 isDefault=true：code=%d tok=%q isDefault=%v", code, tok, isDefault)
	}

	// 錯誤密碼 401
	if _, _, code := login(t, ts, "wrong"); code != http.StatusUnauthorized {
		t.Errorf("錯誤密碼應 401，得到 %d", code)
	}

	// 未帶 token 存取受保護 API → 401
	resp, _ := http.Get(ts.URL + "/api/profiles")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("未認證 GET /api/profiles 應 401，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 帶錯 token → 401
	resp2 := authReq(t, "GET", ts.URL+"/api/profiles", "bogus-token", nil)
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("假 token 應 401，得到 %d", resp2.StatusCode)
	}
	resp2.Body.Close()

	// 帶正確 token → 200
	resp3 := authReq(t, "GET", ts.URL+"/api/profiles", tok, nil)
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("合法 token 應 200，得到 %d", resp3.StatusCode)
	}
	resp3.Body.Close()
}

func TestChangePasswordFlow(t *testing.T) {
	_, ts, path := newTestServer(t)
	tok, _, _ := login(t, ts, "admin")

	// 舊密碼錯誤 → 400
	resp := authReq(t, "POST", ts.URL+"/api/password", tok, map[string]string{"oldPassword": "wrong", "newPassword": "newpass1"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("舊密碼錯誤應 400，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 新密碼太短 → 400
	resp = authReq(t, "POST", ts.URL+"/api/password", tok, map[string]string{"oldPassword": "admin", "newPassword": "ab"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("過短新密碼應 400，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 正確更改 → 200
	resp = authReq(t, "POST", ts.URL+"/api/password", tok, map[string]string{"oldPassword": "admin", "newPassword": "newpass1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("更改密碼應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 舊密碼失效
	if _, _, code := login(t, ts, "admin"); code != http.StatusUnauthorized {
		t.Errorf("舊密碼應失效（401），得到 %d", code)
	}
	// 新密碼可用且 isDefault=false
	tok2, isDefault, code := login(t, ts, "newpass1")
	if code != 200 || isDefault {
		t.Errorf("新密碼登入應成功且 isDefault=false：code=%d isDefault=%v", code, isDefault)
	}
	_ = tok2

	// 持久化：以同一 config.json 重建 Server，新密碼仍有效
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s2 := New(cfg, path)
	ts2 := httptest.NewServer(s2.Handler())
	defer ts2.Close()
	if _, _, code := login(t, ts2, "newpass1"); code != 200 {
		t.Errorf("重啟後新密碼應仍有效，得到 %d", code)
	}
	if _, _, code := login(t, ts2, "admin"); code != 200 {
		// admin 已非當前密碼
		t.Logf("重啟後舊密碼狀態=%d（預期 401）", code)
	}
}

// --- profiles CRUD ---

func TestProfilesCRUD(t *testing.T) {
	_, ts, path := newTestServer(t)
	tok, _, _ := login(t, ts, "admin")

	p := sampleProfile("p1")

	// 新增（PUT upsert）
	resp := authReq(t, "PUT", ts.URL+"/api/profiles/p1", tok, p)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT profile 應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 持久化驗證
	cfg2, _ := config.Load(path)
	if len(cfg2.Profiles) != 1 || cfg2.Profiles[0].ID != "p1" {
		t.Fatalf("profile 應已持久化: %+v", cfg2.Profiles)
	}

	// 讀回
	resp = authReq(t, "GET", ts.URL+"/api/profiles", tok, nil)
	var out struct {
		Profiles []config.Profile `json:"profiles"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if len(out.Profiles) != 1 || out.Profiles[0].Name != p.Name {
		t.Fatalf("GET profiles 內容錯誤: %+v", out.Profiles)
	}

	// 更新（同 ID 覆蓋）
	p.Name = "改名"
	resp = authReq(t, "PUT", ts.URL+"/api/profiles/p1", tok, p)
	resp.Body.Close()
	cfg3, _ := config.Load(path)
	if cfg3.Profiles[0].Name != "改名" || len(cfg3.Profiles) != 1 {
		t.Fatalf("同 ID 應覆蓋: %+v", cfg3.Profiles)
	}

	// 無效 profile → 400
	bad := p
	bad.ContextWindow = "zzz"
	resp = authReq(t, "PUT", ts.URL+"/api/profiles/p1", tok, bad)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("無效 profile 應 400，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 刪除 → 404 再刪
	resp = authReq(t, "DELETE", ts.URL+"/api/profiles/p1", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("DELETE 應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = authReq(t, "DELETE", ts.URL+"/api/profiles/p1", tok, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("再刪應 404，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- start/stop/stats ---

func TestStartStopStats(t *testing.T) {
	// mock 上游 API
	var hits int64
	upMux := http.NewServeMux()
	upMux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		hits++
		io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":200}}`)
	})
	up := httptest.NewServer(upMux)
	defer up.Close()

	_, ts, _ := newTestServer(t)
	tok, _, _ := login(t, ts, "admin")

	p := sampleProfile("p1")
	p.APIBase = up.URL
	p.Concurrency = 2
	p.Rounds = 2
	authReq(t, "PUT", ts.URL+"/api/profiles/p1", tok, p).Body.Close()

	// 不存在 → 404
	resp := authReq(t, "POST", ts.URL+"/api/profiles/nope/start", tok, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("start 不存在 profile 應 404，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// start → stats 應出現且 Running（或已秒完成；輪詢等待完成）
	resp = authReq(t, "POST", ts.URL+"/api/profiles/p1/start", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start 應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 重複 start → 409
	resp = authReq(t, "POST", ts.URL+"/api/profiles/p1/start", tok, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("執行中重複 start 應 409，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 等待完成
	deadline := time.Now().Add(10 * time.Second)
	var stats map[string]map[string]any
	for time.Now().Before(deadline) {
		resp = authReq(t, "GET", ts.URL+"/api/stats", tok, nil)
		var out struct {
			Stats map[string]map[string]any `json:"stats"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		stats = out.Stats
		if st, ok := stats["p1"]; ok {
			if run, _ := st["running"].(bool); !run {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := stats["p1"]
	if st == nil {
		t.Fatal("stats 應包含 p1")
	}
	if okCount, _ := st["ok"].(float64); okCount != 4 { // 2 並發 × 2 輪
		t.Errorf("ok = %v, 預期 4（統計：%v）", st["ok"], st)
	}
	if in, _ := st["inputTokens"].(float64); in != 400 {
		t.Errorf("inputTokens = %v, 預期 400", st["inputTokens"])
	}
	if reason, _ := st["stopReason"].(string); reason != "completed" {
		t.Errorf("stopReason = %v, 預期 completed", st["stopReason"])
	}

	// stop（已完成時 stop 仍應 200）
	resp = authReq(t, "POST", ts.URL+"/api/profiles/p1/stop", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("stop 應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 刪除執行中 profile：先 start 一個懸掛的
	p.Rounds = 1000000
	authReq(t, "PUT", ts.URL+"/api/profiles/p1", tok, p).Body.Close()
	authReq(t, "POST", ts.URL+"/api/profiles/p1/start", tok, nil).Body.Close()
	resp = authReq(t, "DELETE", ts.URL+"/api/profiles/p1", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("刪除執行中 profile 應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 等待刪除後 stats 不再包含 p1
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp = authReq(t, "GET", ts.URL+"/api/stats", tok, nil)
		var out struct {
			Stats map[string]map[string]any `json:"stats"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if _, ok := out.Stats["p1"]; !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := stats["p1"]; st != nil {
		resp = authReq(t, "GET", ts.URL+"/api/stats", tok, nil)
		var out struct {
			Stats map[string]map[string]any `json:"stats"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if _, still := out.Stats["p1"]; still {
			t.Error("刪除 profile 後 stats 不應再包含它")
		}
	}
}

// --- 全域 proxy 設定 ---

func TestSettingsProxy(t *testing.T) {
	_, ts, path := newTestServer(t)
	tok, _, _ := login(t, ts, "admin")

	// 初始 GET → 空 proxyUrl
	resp := authReq(t, "GET", ts.URL+"/api/settings", tok, nil)
	var out struct {
		ProxyURL string `json:"proxyUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if out.ProxyURL != "" {
		t.Errorf("初始 proxyUrl 應為空，得到 %q", out.ProxyURL)
	}

	// 無效 scheme → 400
	resp = authReq(t, "PUT", ts.URL+"/api/settings", tok, map[string]string{"proxyUrl": "socks4://127.0.0.1:1080"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("socks4 應 400，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 有效 proxy（含帳密）→ 200 + 持久化
	resp = authReq(t, "PUT", ts.URL+"/api/settings", tok, map[string]string{"proxyUrl": "http://user:pass@127.0.0.1:9999"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("有效 proxy 應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	cfg2, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.ProxyURL != "http://user:pass@127.0.0.1:9999" {
		t.Errorf("proxyUrl 應持久化，得到 %q", cfg2.ProxyURL)
	}

	// none 合法（全域強制直連）
	resp = authReq(t, "PUT", ts.URL+"/api/settings", tok, map[string]string{"proxyUrl": "none"})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("none 應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	cfg3, _ := config.Load(path)
	if cfg3.ProxyURL != "none" {
		t.Errorf("none 應持久化，得到 %q", cfg3.ProxyURL)
	}
}

func TestStartUsesGlobalProxy(t *testing.T) {
	// fake 上游 API
	upMux := http.NewServeMux()
	upMux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"usage":{"prompt_tokens":10,"completion_tokens":20}}`)
	})
	up := httptest.NewServer(upMux)
	defer up.Close()

	// fake HTTP proxy：統計流量並轉發絕對 URI 請求
	var proxied int64
	pxMux := http.NewServeMux()
	pxMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&proxied, 1)
		target := r.RequestURI
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			target = "http://" + r.Host + target
		}
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		outReq.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(outReq)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
	px := httptest.NewServer(pxMux)
	defer px.Close()

	_, ts, _ := newTestServer(t)
	tok, _, _ := login(t, ts, "admin")

	// 設全域 proxy；profile.ProxyURL 留空 = 跟隨全域
	resp := authReq(t, "PUT", ts.URL+"/api/settings", tok, map[string]string{"proxyUrl": px.URL})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("設定全域 proxy 應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	p := sampleProfile("px1")
	p.APIBase = up.URL
	p.Concurrency = 1
	p.Rounds = 1
	authReq(t, "PUT", ts.URL+"/api/profiles/px1", tok, p).Body.Close()

	resp = authReq(t, "POST", ts.URL+"/api/profiles/px1/start", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start 應 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 等待完成並確認請求經過 proxy
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp = authReq(t, "GET", ts.URL+"/api/stats", tok, nil)
		var out struct {
			Stats map[string]map[string]any `json:"stats"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if st, ok := out.Stats["px1"]; ok {
			if run, _ := st["running"].(bool); !run {
				if okCount, _ := st["ok"].(float64); okCount != 1 {
					t.Errorf("ok = %v, 預期 1", st["ok"])
				}
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := atomic.LoadInt64(&proxied); n == 0 {
		t.Error("請求應經過全域 proxy（proxied=0）")
	}
}

// --- 靜態面板 ---

func TestStaticPanel(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / 應 200，得到 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type 應為 text/html，得到 %q", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "token毀滅者") && !strings.Contains(string(body), "<!DOCTYPE") {
		t.Error("面板 HTML 內容異常")
	}
	// 靜態資源
	resp2, _ := http.Get(ts.URL + "/app.js")
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("GET /app.js 應 200，得到 %d", resp2.StatusCode)
	}
	resp2.Body.Close()

	_ = os.Environ
}
