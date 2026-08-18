// Package config 定義 token毀滅者 的配置結構、驗證與 JSON 持久化。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// 支援的 API 協議。
const (
	ProtocolOpenAIResponses = "openai-responses" // POST /v1/responses
	ProtocolOpenAIChat      = "openai-chat"      // POST /v1/chat/completions
	ProtocolClaude          = "claude"           // POST /v1/messages
)

// 消耗策略。
const (
	StrategyBoth   = "both"   // 大輸入＋大輸出，雙向燒
	StrategyOutput = "output" // 只燒輸出 token
	StrategyInput  = "input"  // 只燒輸入 token
)

// DefaultListen 面板預設監聽地址（需求指定 0.0.0.0:24300）。
const DefaultListen = "0.0.0.0:24300"

// Profile 一組獨立的 API 燒毀配置，可單獨啟停。
type Profile struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Protocol        string `json:"protocol"` // 見 Protocol* 常數
	APIBase         string `json:"apiBase"`  // 例如 https://api.openai.com
	APIKey          string `json:"apiKey"`   // OpenAI: Bearer；Claude: x-api-key
	Model           string `json:"model"`
	ContextWindow   string `json:"contextWindow"`   // 支援 K/k 結尾，如 128K
	MaxOutputTokens int    `json:"maxOutputTokens"` // 單請求最大輸出 token
	Concurrency     int    `json:"concurrency"`     // 並發連接數
	Rounds          int    `json:"rounds"`          // 輪次：一輪＝每並發槽各完成一次請求
	Strategy        string `json:"strategy"`        // 見 Strategy* 常數
	ProxyURL        string `json:"proxyUrl"`        // 空=跟隨全域；none=直連；否則覆蓋（http/https/socks5/socks5h，支援帳密）
}

// Config 全域配置，持久化為 config.json。
type Config struct {
	Listen       string    `json:"listen"`
	PasswordHash string    `json:"passwordHash,omitempty"` // 空 = 預設密碼（見 DefaultPassword）
	ProxyURL     string    `json:"proxyUrl,omitempty"`     // 全域預設代理；空=直連；none=全部直連
	Profiles     []Profile `json:"profiles"`
}

// ParseContextWindow 解析上下文窗口長度字串。
// 支援純數字（如 131072）與 K/k 結尾（如 128K、128k → 128*1024）。
func ParseContextWindow(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("上下文窗口不可為空")
	}
	mult := int64(1)
	numPart := s
	if last := s[len(s)-1]; last == 'K' || last == 'k' {
		mult = 1024
		numPart = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("無法解析上下文窗口 %q：需為正整數或以 K/k 結尾", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("上下文窗口必須為正整數，得到 %q", s)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("上下文窗口 %q 數值溢位", s)
	}
	return n * mult, nil
}

// ContextWindowTokens 回傳解析後的上下文窗口 token 數。
func (p Profile) ContextWindowTokens() (int64, error) {
	return ParseContextWindow(p.ContextWindow)
}

// Validate 檢查 profile 欄位合法性，回傳第一個發現的錯誤。
func (p Profile) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("名稱不可為空")
	}
	switch p.Protocol {
	case ProtocolOpenAIResponses, ProtocolOpenAIChat, ProtocolClaude:
	default:
		return fmt.Errorf("未知協議 %q", p.Protocol)
	}
	if !strings.HasPrefix(p.APIBase, "http://") && !strings.HasPrefix(p.APIBase, "https://") {
		return fmt.Errorf("API 地址 %q 需以 http:// 或 https:// 開頭", p.APIBase)
	}
	if p.APIKey == "" {
		return errors.New("API Key 不可為空")
	}
	if p.Model == "" {
		return errors.New("模型不可為空")
	}
	if _, err := p.ContextWindowTokens(); err != nil {
		return err
	}
	if p.MaxOutputTokens < 1 {
		return errors.New("最大輸出 token 至少為 1")
	}
	if p.Concurrency < 1 {
		return errors.New("並發連接數至少為 1")
	}
	if p.Concurrency > 1024 {
		return errors.New("並發連接數上限 1024")
	}
	if p.Rounds < 1 {
		return errors.New("輪次至少為 1")
	}
	switch p.Strategy {
	case StrategyBoth, StrategyOutput, StrategyInput:
	default:
		return fmt.Errorf("未知消耗策略 %q", p.Strategy)
	}
	if err := ValidateProxyURL(p.ProxyURL); err != nil {
		return fmt.Errorf("代理設定無效: %w", err)
	}
	return nil
}

// UpsertProfile 依 ID 覆蓋或新增 profile。
func (c *Config) UpsertProfile(p Profile) {
	for i := range c.Profiles {
		if c.Profiles[i].ID == p.ID {
			c.Profiles[i] = p
			return
		}
	}
	c.Profiles = append(c.Profiles, p)
}

// RemoveProfile 依 ID 移除 profile；不存在時回傳 false。
func (c *Config) RemoveProfile(id string) bool {
	for i := range c.Profiles {
		if c.Profiles[i].ID == id {
			c.Profiles = append(c.Profiles[:i], c.Profiles[i+1:]...)
			return true
		}
	}
	return false
}

// Profile 依 ID 查詢 profile；不存在時回傳 false。
func (c *Config) Profile(id string) (*Profile, bool) {
	for i := range c.Profiles {
		if c.Profiles[i].ID == id {
			return &c.Profiles[i], true
		}
	}
	return nil, false
}

// Load 從 path 讀取配置；檔案不存在時回傳含預設值的空配置。
func Load(path string) (*Config, error) {
	cfg := &Config{Listen: DefaultListen, Profiles: []Profile{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("讀取配置失敗: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置失敗: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Profiles == nil {
		cfg.Profiles = []Profile{}
	}
	return cfg, nil
}

// Save 將配置以 JSON 寫入 path（自動縮排便於人工檢視）。
func Save(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失敗: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}
