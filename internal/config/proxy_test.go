package config

import (
	"path/filepath"
	"testing"
)

func TestValidateProxyURL(t *testing.T) {
	ok := []string{
		"",
		"none",
		"http://127.0.0.1:7890",
		"https://proxy.example.com:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
		"http://user:pass@127.0.0.1:7890", // 帳密內嵌
		"socks5://user:p%40ss@10.0.0.1:1080",
		"https://u@proxy.example.com:8443", // 僅帳號
	}
	for _, s := range ok {
		if err := ValidateProxyURL(s); err != nil {
			t.Errorf("ValidateProxyURL(%q) 不應報錯: %v", s, err)
		}
	}
	bad := []string{
		"ftp://127.0.0.1:21",    // 不支援的 scheme
		"127.0.0.1:7890",        // 缺 scheme
		"http://",               // 缺 host
		"://x",                  // 解析失敗
		"socks4://1.2.3.4:1080", // socks4 不支援
		"NONE",                  // 大小寫敏感？容忍大寫應報錯以避免歧義
	}
	for _, s := range bad {
		if err := ValidateProxyURL(s); err == nil {
			t.Errorf("ValidateProxyURL(%q) 應報錯", s)
		}
	}
}

func TestResolveProxyURL(t *testing.T) {
	cases := []struct {
		global, profile, want string
	}{
		// 留空 = 跟隨全域
		{"http://g:1", "", "http://g:1"},
		{"", "", ""}, // 兩者皆空 = 直連
		// none = 強制直連
		{"http://g:1", "none", ""},
		// 覆蓋
		{"http://g:1", "socks5://p:2", "socks5://p:2"},
		// 全域 none（未設）＋profile 覆蓋
		{"none", "http://p:3", "http://p:3"},
		// 帳密保留
		{"http://u:pw@g:1", "socks5://a:b@p:2", "socks5://a:b@p:2"},
	}
	for _, c := range cases {
		cfg := &Config{ProxyURL: c.global}
		p := Profile{ProxyURL: c.profile}
		if got := cfg.ResolveProxyURL(p); got != c.want {
			t.Errorf("Resolve(global=%q, profile=%q) = %q, 預期 %q", c.global, c.profile, got, c.want)
		}
	}
}

func TestProxyRoundTripAndProfileValidate(t *testing.T) {
	// Profile 驗證含 proxy 欄位
	p := validProfile()
	p.ProxyURL = "socks5://u:pw@127.0.0.1:1080"
	if err := p.Validate(); err != nil {
		t.Errorf("合法 proxy 應通過驗證: %v", err)
	}
	p.ProxyURL = "bogus"
	if err := p.Validate(); err == nil {
		t.Error("非法 proxy 應驗證失敗")
	}
	// Config JSON round-trip 保留欄位
	path := testProxyPath(t)
	cfg := &Config{Listen: DefaultListen, ProxyURL: "http://u:pw@127.0.0.1:7890", Profiles: []Profile{validProfile()}}
	cfg.Profiles[0].ProxyURL = "none"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyURL != "http://u:pw@127.0.0.1:7890" || got.Profiles[0].ProxyURL != "none" {
		t.Errorf("proxy 欄位 round-trip 失敗: global=%q profile=%q", got.ProxyURL, got.Profiles[0].ProxyURL)
	}
}

func testProxyPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.json")
}
