package burner

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 建構層：各 scheme 產生正確的 Transport 屬性。
func TestProxyTransportBuild(t *testing.T) {
	// 空字串：直連，不設 Proxy / DialContext
	tr, err := ProxyTransport("")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil {
		t.Error("直連 Transport 不應設 Proxy")
	}

	// none：同直連
	tr, err = ProxyTransport("none")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil {
		t.Error("none Transport 不應設 Proxy")
	}

	// http 代理（含帳密）：Proxy 指向該 URL，保留 userinfo
	tr, err = ProxyTransport("http://user:pw@127.0.0.1:7890")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy == nil {
		t.Fatal("http 代理應設 Proxy")
	}
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/x", nil)
	pu, err := tr.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if pu.String() != "http://user:pw@127.0.0.1:7890" {
		t.Errorf("Proxy URL = %q，帳密應保留", pu.String())
	}
	if pw, ok := pu.User.Password(); !ok || pw != "pw" {
		t.Errorf("Proxy URL userinfo 應保留密碼: %v", pu.User)
	}

	// socks5 / socks5h：DialContext 由 dialer 提供
	for _, scheme := range []string{"socks5", "socks5h"} {
		tr, err = ProxyTransport(scheme + "://127.0.0.1:1080")
		if err != nil {
			t.Fatalf("%s: %v", scheme, err)
		}
		if tr.Proxy != nil {
			t.Errorf("%s 不應用 http Proxy 機制", scheme)
		}
		if tr.DialContext == nil {
			t.Errorf("%s 應設 DialContext", scheme)
		}
	}

	// 帳密 socks5 應可建構
	if _, err := ProxyTransport("socks5://u:p@127.0.0.1:1080"); err != nil {
		t.Errorf("socks5 帳密應支援: %v", err)
	}

	// 非法值報錯
	for _, bad := range []string{"ftp://x:1", "://x", "http://"} {
		if _, err := ProxyTransport(bad); err == nil {
			t.Errorf("ProxyTransport(%q) 應報錯", bad)
		}
	}
}

// 端到端：HTTP 代理帳密認證 —— 請求應帶 Proxy-Authorization: Basic，且經代理轉發成功。
func TestHTTPProxyWithAuthEndToEnd(t *testing.T) {
	var gotAuth, gotTarget string
	// 目標 API
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	}))
	defer target.Close()

	// fake HTTP 代理
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Proxy-Authorization")
		gotTarget = r.Host
		// 代理收到的是絕對 URI 形式的請求（http 目標），轉發給目標
		uri := r.RequestURI
		if !strings.HasPrefix(uri, "http://") && !strings.HasPrefix(uri, "https://") {
			uri = target.URL + uri
		}
		outReq, err := http.NewRequest(r.Method, uri, r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultTransport.RoundTrip(outReq)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	tr, err := ProxyTransport("http://john:s3cret@" + proxySrv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get(target.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if string(body) != `{"ok":true}` {
		t.Errorf("經代理的回應錯誤: %s", body)
	}
	if gotTarget == "" {
		t.Error("請求應經過代理")
	}
	// Basic base64("john:s3cret") = am9objpzM2NyZXQ=
	if gotAuth != "Basic am9objpzM2NyZXQ=" {
		t.Errorf("Proxy-Authorization = %q，預期 Basic am9objpzM2NyZXQ=", gotAuth)
	}
}
