package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseContextWindow(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"128K", 128 * 1024, false},
		{"128k", 128 * 1024, false},
		{"1K", 1024, false},
		{"2k", 2048, false},
		{"131072", 131072, false},
		{"1000", 1000, false},
		{" 128K ", 128 * 1024, false},     // 容忍前後空白
		{"", 0, true},                     // 空字串
		{"abc", 0, true},                  // 非數字
		{"12K5", 0, true},                 // 尾綴後有多餘字元
		{"0", 0, true},                    // 必須 > 0
		{"-5", 0, true},                   // 負數
		{"12.5K", 0, true},                // 不支援小數
		{"1M", 0, true},                   // 只支援 K/k
		{"99999999999999999999", 0, true}, // 溢位
	}
	for _, c := range cases {
		got, err := ParseContextWindow(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseContextWindow(%q) 預期錯誤，卻得到 %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseContextWindow(%q) 非預期錯誤: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseContextWindow(%q) = %d, 預期 %d", c.in, got, c.want)
		}
	}
}

func validProfile() Profile {
	return Profile{
		ID:              "p1",
		Name:            "測試端點",
		Protocol:        ProtocolOpenAIChat,
		APIBase:         "https://api.openai.com",
		APIKey:          "sk-test",
		Model:           "gpt-4o",
		ContextWindow:   "128K",
		MaxOutputTokens: 4096,
		Concurrency:     4,
		Rounds:          3,
		Strategy:        StrategyBoth,
	}
}

func TestProfileValidate(t *testing.T) {
	if err := validProfile().Validate(); err != nil {
		t.Fatalf("合法 profile 應通過驗證，卻得到: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*Profile)
	}{
		{"空名稱", func(p *Profile) { p.Name = "" }},
		{"未知協議", func(p *Profile) { p.Protocol = "gemini" }},
		{"空 API 地址", func(p *Profile) { p.APIBase = "" }},
		{"API 地址非 http", func(p *Profile) { p.APIBase = "ftp://x" }},
		{"空 API Key", func(p *Profile) { p.APIKey = "" }},
		{"空模型", func(p *Profile) { p.Model = "" }},
		{"上下文窗口非法", func(p *Profile) { p.ContextWindow = "abc" }},
		{"最大輸出 token 為 0", func(p *Profile) { p.MaxOutputTokens = 0 }},
		{"並發數為 0", func(p *Profile) { p.Concurrency = 0 }},
		{"並發數過大", func(p *Profile) { p.Concurrency = 5000 }},
		{"輪次為 0", func(p *Profile) { p.Rounds = 0 }},
		{"未知策略", func(p *Profile) { p.Strategy = "mega" }},
	}
	for _, c := range cases {
		p := validProfile()
		c.mut(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s：預期驗證失敗，卻通過了", c.name)
		}
	}
}

func TestProfileContextWindowTokens(t *testing.T) {
	p := validProfile()
	p.ContextWindow = "64K"
	got, err := p.ContextWindowTokens()
	if err != nil {
		t.Fatalf("非預期錯誤: %v", err)
	}
	if want := int64(64 * 1024); got != want {
		t.Errorf("ContextWindowTokens() = %d, 預期 %d", got, want)
	}
}

func TestConfigSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{
		Listen:   "0.0.0.0:24300",
		Profiles: []Profile{validProfile()},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save 失敗: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("配置檔未寫入: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失敗: %v", err)
	}
	if got.Listen != cfg.Listen {
		t.Errorf("Listen = %q, 預期 %q", got.Listen, cfg.Listen)
	}
	if !reflect.DeepEqual(got.Profiles, cfg.Profiles) {
		t.Errorf("Profiles round-trip 不一致:\n got  %+v\n want %+v", got.Profiles, cfg.Profiles)
	}
}

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "不存在.json"))
	if err != nil {
		t.Fatalf("檔案不存在時應返回預設配置而非報錯: %v", err)
	}
	if got.Listen != DefaultListen {
		t.Errorf("預設 Listen = %q, 預期 %q", got.Listen, DefaultListen)
	}
	if len(got.Profiles) != 0 {
		t.Errorf("預設應無 profile，卻有 %d 個", len(got.Profiles))
	}
}

func TestUpsertAndRemoveProfile(t *testing.T) {
	cfg := &Config{Listen: DefaultListen}
	p := validProfile()
	cfg.UpsertProfile(p)
	if len(cfg.Profiles) != 1 {
		t.Fatalf("新增後應有 1 個 profile")
	}
	p.Name = "改名"
	cfg.UpsertProfile(p) // 同 ID 應覆蓋
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Name != "改名" {
		t.Fatalf("同 ID 應覆蓋而非新增: %+v", cfg.Profiles)
	}
	if !cfg.RemoveProfile("p1") {
		t.Fatal("移除存在 ID 應返回 true")
	}
	if cfg.RemoveProfile("p1") {
		t.Fatal("移除不存在 ID 應返回 false")
	}
	if len(cfg.Profiles) != 0 {
		t.Fatal("移除後應為空")
	}
}

func TestProfileLookup(t *testing.T) {
	cfg := &Config{Listen: DefaultListen}
	p := validProfile()
	cfg.UpsertProfile(p)

	got, ok := cfg.Profile("p1")
	if !ok || got.Name != p.Name {
		t.Fatalf("查詢存在的 ID 應返回該 profile: ok=%v got=%+v", ok, got)
	}
	if _, ok := cfg.Profile("nope"); ok {
		t.Fatal("查詢不存在的 ID 應返回 false")
	}
}
