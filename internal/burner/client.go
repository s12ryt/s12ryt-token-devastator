// Package burner 實作 token 燒毀核心：API client、請求構造與消耗策略。
package burner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"token-devastator/internal/config"
)

// Usage 單次請求的 token 用量。
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// StatusError 非 2xx 回應的錯誤，攜帶狀態碼與（截斷的）回應本文。
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	msg := e.Body
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return fmt.Sprintf("API 回應狀態碼 %d：%s", e.Code, msg)
}

// Retryable 判斷此錯誤是否值得重試（限流、逾時與暫時性伺服器錯誤）。
func (e *StatusError) Retryable() bool {
	return e.Code == http.StatusTooManyRequests ||
		e.Code == http.StatusRequestTimeout ||
		e.Code == http.StatusConflict ||
		e.Code == http.StatusTooEarly ||
		e.Code >= 500
}

// endpoint 由 API base 與協議推導完整端點 URL。
// base 可為 https://host、https://host/、https://host/v1 或 https://host/v1/。
func endpoint(base, proto string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") {
		// 已含 /v1，直接串接路徑
	} else {
		base += "/v1"
	}
	switch proto {
	case config.ProtocolOpenAIResponses:
		return base + "/responses"
	case config.ProtocolOpenAIChat:
		return base + "/chat/completions"
	case config.ProtocolClaude:
		return base + "/messages"
	}
	return base
}

// buildRequestBody 構造各協議的 JSON 請求體。
// useMaxCompletion 僅對 openai-chat 生效：以 max_completion_tokens 取代 max_tokens（o 系列模型）。
func buildRequestBody(proto, model, prompt string, maxTokens int, useMaxCompletion bool) ([]byte, error) {
	switch proto {
	case config.ProtocolOpenAIResponses:
		return json.Marshal(map[string]any{
			"model":             model,
			"input":             prompt,
			"max_output_tokens": maxTokens,
		})
	case config.ProtocolOpenAIChat:
		m := map[string]any{
			"model": model,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
		}
		if useMaxCompletion {
			m["max_completion_tokens"] = maxTokens
		} else {
			m["max_tokens"] = maxTokens
		}
		return json.Marshal(m)
	case config.ProtocolClaude:
		return json.Marshal(map[string]any{
			"model": model,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
			"max_tokens": maxTokens,
		})
	}
	return nil, fmt.Errorf("未知協議 %q", proto)
}

// Client 對單一 profile 的 API 客戶端。
type Client struct {
	Profile config.Profile
	HTTP    *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// buildRequest 組裝帶有正確 header 的 HTTP 請求。
func (c *Client) buildRequest(ctx context.Context, prompt string, maxTokens int, useMaxCompletion bool) (*http.Request, error) {
	body, err := buildRequestBody(c.Profile.Protocol, c.Profile.Model, prompt, maxTokens, useMaxCompletion)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(c.Profile.APIBase, c.Profile.Protocol), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Profile.Protocol == config.ProtocolClaude {
		req.Header.Set("x-api-key", c.Profile.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+c.Profile.APIKey)
	}
	return req, nil
}

// doOnce 發送單次請求並解析 usage；非 2xx 回傳 *StatusError。
func (c *Client) doOnce(ctx context.Context, prompt string, maxTokens int, useMaxCompletion bool) (Usage, error) {
	req, err := c.buildRequest(ctx, prompt, maxTokens, useMaxCompletion)
	if err != nil {
		return Usage{}, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Usage{}, fmt.Errorf("讀取回應失敗: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Usage{}, &StatusError{Code: resp.StatusCode, Body: string(body)}
	}
	return parseUsage(c.Profile.Protocol, body)
}

// Do 發送燒毀請求。openai-chat 遇到 400 且對方要求 max_completion_tokens 時，
// 自動以該參數重發一次。
func (c *Client) Do(ctx context.Context, prompt string, maxTokens int) (Usage, error) {
	u, err := c.doOnce(ctx, prompt, maxTokens, false)
	var se *StatusError
	if errors.As(err, &se) &&
		se.Code == http.StatusBadRequest &&
		c.Profile.Protocol == config.ProtocolOpenAIChat &&
		strings.Contains(strings.ToLower(se.Body), "max_completion_tokens") {
		return c.doOnce(ctx, prompt, maxTokens, true)
	}
	return u, err
}

// parseUsage 由各協議回應體解析 usage 欄位。
func parseUsage(proto string, body []byte) (Usage, error) {
	switch proto {
	case config.ProtocolOpenAIChat:
		var r struct {
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return Usage{}, fmt.Errorf("解析回應失敗: %w", err)
		}
		if r.Usage == nil {
			return Usage{}, errors.New("回應缺少 usage 欄位")
		}
		return Usage{InputTokens: r.Usage.PromptTokens, OutputTokens: r.Usage.CompletionTokens}, nil
	default: // openai-responses 與 claude 皆為 input_tokens/output_tokens
		var r struct {
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return Usage{}, fmt.Errorf("解析回應失敗: %w", err)
		}
		if r.Usage == nil {
			return Usage{}, errors.New("回應缺少 usage 欄位")
		}
		return Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}, nil
	}
}
