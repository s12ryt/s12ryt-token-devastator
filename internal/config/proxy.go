package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ProxyNone profile 層級的「強制直連」標記值。
const ProxyNone = "none"

// ValidateProxyURL 驗證代理 URL：
// 空字串（未設定）與 "none"（強制直連）皆合法；
// 其餘須為 http/https/socks5/socks5h scheme 且含 host，可內嵌帳密（user:pass@host:port）。
func ValidateProxyURL(s string) error {
	if s == "" || s == ProxyNone {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("代理 URL 解析失敗: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("不支援的代理 scheme %q（支援 http/https/socks5/socks5h）", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("代理 URL 缺少主機位址")
	}
	return nil
}

// ResolveProxyURL 解析 profile 實際使用的代理 URL。
// 回傳空字串代表直連。規則：profile 留空＝跟隨全域；"none"＝強制直連；其餘覆蓋全域。
func (c Config) ResolveProxyURL(p Profile) string {
	switch p.ProxyURL {
	case "":
		if c.ProxyURL == ProxyNone {
			return ""
		}
		return c.ProxyURL
	case ProxyNone:
		return ""
	default:
		return p.ProxyURL
	}
}
