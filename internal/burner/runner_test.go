package burner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"token-devastator/internal/config"
)

func runnerProfile() config.Profile {
	return config.Profile{
		ID:              "p1",
		Name:            "測試",
		Protocol:        config.ProtocolOpenAIChat,
		APIBase:         "https://x.com",
		APIKey:          "k",
		Model:           "m",
		ContextWindow:   "4K",
		MaxOutputTokens: 256,
		Concurrency:     3,
		Rounds:          4,
		Strategy:        config.StrategyOutput,
	}
}

// mockBurnServer 回傳固定 usage，並統計請求次數。
func mockBurnServer(t *testing.T, hits *int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(hits, 1)
		io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":200}}`)
	})
	return httptest.NewServer(mux)
}

func awaitNotRunning(t *testing.T, r *Runner, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !r.Stats().Running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Runner 未在 %v 內停止", timeout)
}

// 輪次語義：並發 N × 輪次 R → 總完成請求 N×R；統計準確；正常完成 StopReason=completed。
func TestRunnerRoundsAndStats(t *testing.T) {
	var hits int64
	srv := mockBurnServer(t, &hits)
	defer srv.Close()

	p := runnerProfile()
	p.APIBase = srv.URL
	r, err := NewRunner(p, srv.Client(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stats().Running {
		t.Fatal("新建 Runner 不應處於執行中")
	}
	r.Start(context.Background())
	awaitNotRunning(t, r, 10*time.Second)

	s := r.Stats()
	if hits != 3*4 {
		t.Errorf("總請求數 = %d, 預期 12（3 並發 × 4 輪）", hits)
	}
	if s.OK != 12 {
		t.Errorf("OK = %d, 預期 12", s.OK)
	}
	if s.Failed != 0 || s.Retries != 0 {
		t.Errorf("不應有失敗/重試: %+v", s)
	}
	if s.InputTokens != 12*100 || s.OutputTokens != 12*200 {
		t.Errorf("token 統計錯誤: in=%d out=%d, 預期 1200/2400", s.InputTokens, s.OutputTokens)
	}
	if s.Round != 4 || s.RoundDone != 4 {
		t.Errorf("輪次統計 Round=%d RoundDone=%d, 預期 4/4", s.Round, s.RoundDone)
	}
	if s.StopReason != StopReasonCompleted {
		t.Errorf("StopReason = %q, 預期 %q", s.StopReason, StopReasonCompleted)
	}
	if s.LastError != "" {
		t.Errorf("不應有 LastError: %q", s.LastError)
	}

	// 重啟：統計重置且可再跑
	r.Start(context.Background())
	awaitNotRunning(t, r, 10*time.Second)
	s2 := r.Stats()
	if s2.OK != 12 || s2.Round != 4 {
		t.Errorf("重啟後統計應重置並重新完成: %+v", s2)
	}
	if hits != 24 {
		t.Errorf("兩輪執行總請求 = %d, 預期 24", hits)
	}
}

// 手動停止：Running=false、StopReason=manual。
func TestRunnerManualStop(t *testing.T) {
	var hits int64
	block := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		<-block // 懸掛請求直到測試放行
		io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	srv := httptest.NewServer(mux)
	defer func() { close(block); srv.Close() }()

	p := runnerProfile()
	p.APIBase = srv.URL
	p.Concurrency = 1
	p.Rounds = 1000000 // 跑不完，靠手動停
	r, err := NewRunner(p, srv.Client(), 0)
	if err != nil {
		t.Fatal(err)
	}
	r.Start(context.Background())

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&hits) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !r.Stats().Running {
		t.Fatal("Runner 應仍在執行")
	}
	r.Stop()
	awaitNotRunning(t, r, 5*time.Second)
	if got := r.Stats().StopReason; got != StopReasonManual {
		t.Errorf("StopReason = %q, 預期 %q", got, StopReasonManual)
	}
}

// 連續失敗 10 次（每次含 5 次嘗試）→ 自動停止並標記原因。
func TestRunnerConsecutiveFailuresStop(t *testing.T) {
	attempts := int64(0)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `boom`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := runnerProfile()
	p.APIBase = srv.URL
	p.Concurrency = 1
	p.Rounds = 1000000
	r, err := NewRunner(p, srv.Client(), time.Millisecond) // 1ms 退避基準加速測試
	if err != nil {
		t.Fatal(err)
	}
	r.Start(context.Background())
	awaitNotRunning(t, r, 30*time.Second)

	s := r.Stats()
	if s.Failed != 10 {
		t.Errorf("Failed = %d, 預期 10（連續失敗門檻）", s.Failed)
	}
	if s.Retries != 10*(maxAttempts-1) {
		t.Errorf("Retries = %d, 預期 %d（每邏輯請求重試 4 次）", s.Retries, 10*(maxAttempts-1))
	}
	if s.StopReason != StopReasonConsecutiveFailures {
		t.Errorf("StopReason = %q, 預期 %q", s.StopReason, StopReasonConsecutiveFailures)
	}
	if s.LastError == "" {
		t.Error("應記錄最後錯誤")
	}
}

// 暫時性錯誤（429）後成功：重試生效、Retries 計數、OK 記錄、連續失敗歸零。
func TestRunnerRetryThenSuccess(t *testing.T) {
	var n int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&n, 1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `rate limited`)
			return
		}
		io.WriteString(w, `{"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := runnerProfile()
	p.APIBase = srv.URL
	p.Concurrency = 1
	p.Rounds = 1
	r, err := NewRunner(p, srv.Client(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	r.Start(context.Background())
	awaitNotRunning(t, r, 10*time.Second)

	s := r.Stats()
	if s.OK != 1 || s.Failed != 0 {
		t.Errorf("OK=%d Failed=%d, 預期 1/0", s.OK, s.Failed)
	}
	if s.Retries != 2 {
		t.Errorf("Retries = %d, 預期 2", s.Retries)
	}
	if s.InputTokens != 10 || s.OutputTokens != 5 {
		t.Errorf("usage 統計錯誤: %+v", s)
	}
}

// 不可重試錯誤（401）：單次嘗試即失敗。
func TestRunnerNonRetryableFailsFast(t *testing.T) {
	var n int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&n, 1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := runnerProfile()
	p.APIBase = srv.URL
	p.Concurrency = 1
	p.Rounds = 1
	r, err := NewRunner(p, srv.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r.Start(context.Background())
	awaitNotRunning(t, r, 10*time.Second)

	s := r.Stats()
	if s.Failed != 1 || s.OK != 0 {
		t.Errorf("Failed=%d OK=%d, 預期 1/0", s.Failed, s.OK)
	}
	if n != 1 {
		t.Errorf("401 不應重試，實際嘗試 %d 次", n)
	}
}

// 建立時即驗證 profile；執行中不可重複 Start。
func TestRunnerStartGuards(t *testing.T) {
	p := runnerProfile()
	p.ContextWindow = "bad"
	if _, err := NewRunner(p, http.DefaultClient, 0); err == nil {
		t.Error("非法 profile 應在建 Runner 時報錯")
	}

	p = runnerProfile()
	r, err := NewRunner(p, http.DefaultClient, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 無法真的跑（API base 假的），但 Running 標記應立即為 true
	r.Start(context.Background())
	if !r.Stats().Running {
		t.Error("Start 後應標記 Running")
	}
	if err := r.Start(context.Background()); err == nil {
		t.Error("執行中重複 Start 應報錯")
	}
	r.Stop()
	awaitNotRunning(t, r, 5*time.Second)
}
