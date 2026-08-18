package burner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"token-devastator/internal/config"
)

func TestEndpoint(t *testing.T) {
	cases := []struct {
		base, proto, want string
	}{
		{"https://api.openai.com", config.ProtocolOpenAIResponses, "https://api.openai.com/v1/responses"},
		{"https://api.openai.com/", config.ProtocolOpenAIChat, "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1", config.ProtocolOpenAIResponses, "https://api.openai.com/v1/responses"},
		{"https://relay.example.com/v1/", config.ProtocolClaude, "https://relay.example.com/v1/messages"},
		{"https://x.com", config.ProtocolClaude, "https://x.com/v1/messages"},
	}
	for _, c := range cases {
		if got := endpoint(c.base, c.proto); got != c.want {
			t.Errorf("endpoint(%q,%q) = %q, 預期 %q", c.base, c.proto, got, c.want)
		}
	}
}

func TestBuildRequestBody(t *testing.T) {
	// openai-responses：input 字串 + max_output_tokens
	b, err := buildRequestBody(config.ProtocolOpenAIResponses, "gpt-4o", "hi", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "gpt-4o" || m["input"] != "hi" {
		t.Errorf("responses 請求體欄位錯誤: %v", m)
	}
	if mt, ok := m["max_output_tokens"].(float64); !ok || mt != 100 {
		t.Errorf("responses max_output_tokens 應為 100: %v", m)
	}
	if _, exists := m["max_tokens"]; exists {
		t.Error("responses 不應含 max_tokens")
	}

	// openai-chat：messages 陣列 + max_tokens
	b, err = buildRequestBody(config.ProtocolOpenAIChat, "gpt-4o", "hi", 200, false)
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	msgs, ok := m["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("chat messages 應有 1 條: %v", m)
	}
	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "hi" {
		t.Errorf("chat message 內容錯誤: %v", msg)
	}
	if mt, ok := m["max_tokens"].(float64); !ok || mt != 200 {
		t.Errorf("chat max_tokens 應為 200: %v", m)
	}

	// openai-chat + useMaxCompletion（o 系列回退）
	b, err = buildRequestBody(config.ProtocolOpenAIChat, "o3", "hi", 300, true)
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	json.Unmarshal(b, &m)
	if mt, ok := m["max_completion_tokens"].(float64); !ok || mt != 300 {
		t.Errorf("chat 回退應使用 max_completion_tokens=300: %v", m)
	}
	if _, exists := m["max_tokens"]; exists {
		t.Error("回退時不應含 max_tokens")
	}

	// claude：messages + max_tokens（必填）
	b, err = buildRequestBody(config.ProtocolClaude, "claude-3-7", "hi", 400, false)
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	json.Unmarshal(b, &m)
	if mt, ok := m["max_tokens"].(float64); !ok || mt != 400 {
		t.Errorf("claude max_tokens 應為 400: %v", m)
	}
	if _, ok := m["messages"].([]any); !ok {
		t.Errorf("claude 應含 messages: %v", m)
	}

	// 未知協議應報錯
	if _, err := buildRequestBody("gemini", "m", "hi", 1, false); err == nil {
		t.Error("未知協議應報錯")
	}
}

func TestBuildRequestHeaders(t *testing.T) {
	p := config.Profile{Protocol: config.ProtocolOpenAIChat, APIBase: "https://x.com", APIKey: "sk-1", Model: "m"}
	c := &Client{Profile: p, HTTP: http.DefaultClient}
	req, err := c.buildRequest(context.Background(), "hi", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-1" {
		t.Errorf("OpenAI Authorization = %q, 預期 Bearer sk-1", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}

	p.Protocol = config.ProtocolOpenAIResponses
	c = &Client{Profile: p, HTTP: http.DefaultClient}
	req, _ = c.buildRequest(context.Background(), "hi", 10, false)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-1" {
		t.Errorf("Responses Authorization = %q", got)
	}

	p.Protocol = config.ProtocolClaude
	c = &Client{Profile: p, HTTP: http.DefaultClient}
	req, _ = c.buildRequest(context.Background(), "hi", 10, false)
	if got := req.Header.Get("x-api-key"); got != "sk-1" {
		t.Errorf("Claude x-api-key = %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("Claude anthropic-version = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Claude 不應帶 Authorization，得到 %q", got)
	}
}

func TestParseUsage(t *testing.T) {
	cases := []struct {
		proto   string
		body    string
		in, out int64
		wantErr bool
	}{
		{config.ProtocolOpenAIResponses, `{"usage":{"input_tokens":11,"output_tokens":22}}`, 11, 22, false},
		{config.ProtocolOpenAIChat, `{"usage":{"prompt_tokens":33,"completion_tokens":44}}`, 33, 44, false},
		{config.ProtocolClaude, `{"usage":{"input_tokens":55,"output_tokens":66}}`, 55, 66, false},
		{config.ProtocolOpenAIChat, `{"no_usage":true}`, 0, 0, true},
		{config.ProtocolOpenAIChat, `not-json`, 0, 0, true},
	}
	for _, c := range cases {
		u, err := parseUsage(c.proto, []byte(c.body))
		if c.wantErr {
			if err == nil {
				t.Errorf("parseUsage(%s) 預期錯誤", c.proto)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseUsage(%s) 錯誤: %v", c.proto, err)
			continue
		}
		if u.InputTokens != c.in || u.OutputTokens != c.out {
			t.Errorf("parseUsage(%s) = %+v, 預期 in=%d out=%d", c.proto, u, c.in, c.out)
		}
	}
}

func TestBuildPrompt(t *testing.T) {
	base := config.Profile{
		Protocol: config.ProtocolOpenAIChat, APIBase: "https://x.com", APIKey: "k",
		Model: "m", ContextWindow: "128K", MaxOutputTokens: 8192,
		Concurrency: 2, Rounds: 1, Strategy: config.StrategyBoth,
	}

	// both：prompt 應逼近（窗口-最大輸出-餘量）× 4 字符，maxTokens = MaxOutputTokens
	p := base
	prompt, mt := BuildPrompt(p)
	if mt != 8192 {
		t.Errorf("both maxTokens = %d, 預期 8192", mt)
	}
	target := (128*1024 - 8192 - marginTokens(128*1024)) * 4
	lo, hi := target*90/100, target*110/100
	if int64(len(prompt)) < lo || int64(len(prompt)) > hi {
		t.Errorf("both prompt 長度 %d 應在 [%d, %d]", len(prompt), lo, hi)
	}

	// output：小 prompt + 指令
	p.Strategy = config.StrategyOutput
	prompt, mt = BuildPrompt(p)
	if mt != 8192 {
		t.Errorf("output maxTokens = %d, 預期 8192", mt)
	}
	if len(prompt) > 2000 {
		t.Errorf("output prompt 應精簡，長度 %d", len(prompt))
	}

	// input：大 prompt + maxTokens = 16
	p.Strategy = config.StrategyInput
	prompt, mt = BuildPrompt(p)
	if mt != 16 {
		t.Errorf("input maxTokens = %d, 預期 16", mt)
	}
	target = (128*1024 - 16 - marginTokens(128*1024)) * 4
	lo, hi = target*90/100, target*110/100
	if int64(len(prompt)) < lo || int64(len(prompt)) > hi {
		t.Errorf("input prompt 長度 %d 應在 [%d, %d]", len(prompt), lo, hi)
	}

	// 邊界：窗口極小（1K）且最大輸出很大 → 目標輸入 ≤ 0 時退化為精簡 prompt，不崩潰
	p.Strategy = config.StrategyBoth
	p.ContextWindow = "1K"
	p.MaxOutputTokens = 1024
	prompt, mt = BuildPrompt(p)
	if mt != 1024 {
		t.Errorf("極小窗口 maxTokens = %d, 預期 1024", mt)
	}
	if len(prompt) == 0 {
		t.Error("極小窗口 prompt 不應為空字串")
	}
}

// Do 端到端：httptest 模擬三協議，驗證 URL、headers、usage 解析。
func TestClientDoProtocols(t *testing.T) {
	mux := http.NewServeMux()
	var gotPath, gotAuth, gotXKey string
	var lastBody map[string]any

	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		readBody(t, r, &lastBody)
		io.WriteString(w, `{"usage":{"input_tokens":10,"output_tokens":20}}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		readBody(t, r, &lastBody)
		io.WriteString(w, `{"usage":{"prompt_tokens":30,"completion_tokens":40}}`)
	})
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotXKey = r.Header.Get("x-api-key")
		readBody(t, r, &lastBody)
		io.WriteString(w, `{"usage":{"input_tokens":50,"output_tokens":60}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := config.Profile{
		APIBase: srv.URL, APIKey: "sk-test", Model: "gpt-x",
		ContextWindow: "8K", MaxOutputTokens: 512, Concurrency: 1, Rounds: 1,
	}

	for _, tc := range []struct {
		proto   string
		path    string
		in, out int64
	}{
		{config.ProtocolOpenAIResponses, "/v1/responses", 10, 20},
		{config.ProtocolOpenAIChat, "/v1/chat/completions", 30, 40},
		{config.ProtocolClaude, "/v1/messages", 50, 60},
	} {
		p.Protocol = tc.proto
		c := &Client{Profile: p, HTTP: srv.Client()}
		u, err := c.Do(context.Background(), "burn", 100)
		if err != nil {
			t.Fatalf("%s Do 失敗: %v", tc.proto, err)
		}
		if gotPath != tc.path {
			t.Errorf("%s 請求路徑 = %q, 預期 %q", tc.proto, gotPath, tc.path)
		}
		if u.InputTokens != tc.in || u.OutputTokens != tc.out {
			t.Errorf("%s usage = %+v, 預期 in=%d out=%d", tc.proto, u, tc.in, tc.out)
		}
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("OpenAI Bearer header = %q", gotAuth)
	}
	if gotXKey != "sk-test" {
		t.Errorf("Claude x-api-key = %q", gotXKey)
	}
	if _, ok := lastBody["model"]; !ok {
		t.Error("請求體應含 model")
	}
}

// 429 應產生可重試的 StatusError；400 參數錯誤不可重試。
func TestClientDoStatusError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"rate limited"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := config.Profile{Protocol: config.ProtocolOpenAIChat, APIBase: srv.URL, APIKey: "k", Model: "m"}
	c := &Client{Profile: p, HTTP: srv.Client()}
	_, err := c.Do(context.Background(), "x", 10)
	se, ok := err.(*StatusError)
	if !ok {
		t.Fatalf("預期 StatusError，得到 %T: %v", err, err)
	}
	if se.Code != 429 || !se.Retryable() {
		t.Errorf("429 應可重試: %+v", se)
	}
	if !strings.Contains(se.Error(), "429") {
		t.Errorf("錯誤訊息應含狀態碼: %v", se)
	}

	// 401 不可重試
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	p.APIBase = srv2.URL
	c = &Client{Profile: p, HTTP: srv2.Client()}
	_, err = c.Do(context.Background(), "x", 10)
	if se, ok := err.(*StatusError); !ok || se.Code != 401 || se.Retryable() {
		t.Errorf("401 應為不可重試 StatusError，得到 %#v", err)
	}
}

// OpenAI chat：400 提示 max_completion_tokens 時自動回退重發。
func TestClientDoMaxCompletionFallback(t *testing.T) {
	var calls int
	var bodies []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		calls++
		var m map[string]any
		readBody(t, r, &m)
		bodies = append(bodies, m)
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Use 'max_completion_tokens' instead of 'max_tokens'"}}`)
			return
		}
		io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := config.Profile{Protocol: config.ProtocolOpenAIChat, APIBase: srv.URL, APIKey: "k", Model: "o-x"}
	c := &Client{Profile: p, HTTP: srv.Client()}
	u, err := c.Do(context.Background(), "x", 777)
	if err != nil {
		t.Fatalf("回退後應成功: %v", err)
	}
	if u.InputTokens != 1 || u.OutputTokens != 2 {
		t.Errorf("usage = %+v", u)
	}
	if calls != 2 {
		t.Fatalf("應發送 2 次請求，實際 %d", calls)
	}
	if _, ok := bodies[0]["max_tokens"].(float64); !ok {
		t.Errorf("第 1 次應用 max_tokens: %v", bodies[0])
	}
	if mt, ok := bodies[1]["max_completion_tokens"].(float64); !ok || mt != 777 {
		t.Errorf("第 2 次應用 max_completion_tokens=777: %v", bodies[1])
	}
}

func readBody(t *testing.T, r *http.Request, dst *map[string]any) {
	t.Helper()
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("請求體非 JSON: %v", err)
	}
}
