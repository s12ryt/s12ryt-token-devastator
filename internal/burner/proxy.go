package burner

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/proxy"

	"token-devastator/internal/config"
)

// ProxyTransport 依代理 URL 建立 http.Transport。
//   - 空字串或 "none"：直連（零值 Transport）
//   - http/https：走 Transport.Proxy；URL 內嵌帳密由 net/http 自動轉為
//     Proxy-Authorization: Basic（RFC 7617）
//   - socks5/socks5h：走 x/net/proxy dialer；帳密以 RFC 1929 認證，
//     主機名稱直接交由代理端解析（socks5h 語義）
func ProxyTransport(proxyURL string) (*http.Transport, error) {
	if proxyURL == "" || proxyURL == config.ProxyNone {
		return &http.Transport{}, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("代理 URL 解析失敗: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("代理 URL 缺少主機位址: %q", proxyURL)
	}
	tr := &http.Transport{}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		pu := u
		tr.Proxy = func(*http.Request) (*url.URL, error) { return pu, nil }
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("建立 socks5 連線器失敗: %w", err)
		}
		tr.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
			// x/net/proxy 的 Dialer 不支援 context；連線交由上層請求逾時保護。
			// addr（目標主機）直接交給代理端解析（socks5h 語義）。
			return d.Dial(network, addr)
		}
	default:
		return nil, fmt.Errorf("不支援的代理 scheme %q（支援 http/https/socks5/socks5h）", u.Scheme)
	}
	return tr, nil
}

// NewHTTPClient 以解析後的代理 URL 建立 API 用 http.Client。
func NewHTTPClient(resolvedProxyURL string) (*http.Client, error) {
	tr, err := ProxyTransport(resolvedProxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: tr}, nil
}
