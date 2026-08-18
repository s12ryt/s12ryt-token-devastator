// Package server 提供 Token-Devastator 的 Web 面板與 REST API。
package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"token-devastator/internal/burner"
	"token-devastator/internal/config"
)

//go:embed static
var staticFiles embed.FS

const (
	sessionTTL   = 24 * time.Hour
	minPassword  = 4
	maxBodyBytes = 1 << 20
)

// Server 管理設定、認證 session 與各 profile 的燒毀任務。
type Server struct {
	mu       sync.Mutex
	cfg      *config.Config
	path     string
	sessions map[string]time.Time // token -> 到期時間
	runners  map[string]*burner.Runner
	baseCtx  context.Context
}

// New 建立綁定設定檔路徑的 Server。
func New(cfg *config.Config, path string) *Server {
	return &Server{
		cfg:      cfg,
		path:     path,
		sessions: make(map[string]time.Time),
		runners:  make(map[string]*burner.Runner),
		baseCtx:  context.Background(),
	}
}

// Handler 回傳面板與 API 的完整路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/password", s.auth(s.handlePassword))
	mux.HandleFunc("/api/settings", s.auth(s.handleSettings))
	mux.HandleFunc("/api/profiles", s.auth(s.handleProfilesList))
	mux.HandleFunc("/api/profiles/", s.auth(s.handleProfilesItem))
	mux.HandleFunc("/api/stats", s.auth(s.handleStats))

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("embedded static files: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
}

// --- 共用工具 ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "請求內容格式錯誤: "+err.Error())
		return false
	}
	return true
}

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand 失敗: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// auth 包裝需要登入的 handler，驗證 Bearer token。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		exp, ok := s.sessions[token]
		if ok && time.Now().After(exp) {
			delete(s.sessions, token)
			ok = false
		}
		s.mu.Unlock()
		if !ok {
			writeError(w, http.StatusUnauthorized, "未認證或登入已過期")
			return
		}
		next(w, r)
	}
}

// --- 認證 ---

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "僅支援 POST")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.CheckPassword(in.Password) {
		writeError(w, http.StatusUnauthorized, "密碼錯誤")
		return
	}
	token := newToken()
	s.sessions[token] = time.Now().Add(sessionTTL)
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"isDefault": s.cfg.IsDefaultPassword(),
	})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "僅支援 POST")
		return
	}
	var in struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.CheckPassword(in.OldPassword) {
		writeError(w, http.StatusBadRequest, "舊密碼錯誤")
		return
	}
	if len(in.NewPassword) < minPassword {
		writeError(w, http.StatusBadRequest, "新密碼長度至少 4 個字元")
		return
	}
	s.cfg.PasswordHash = config.HashPassword(in.NewPassword)
	if err := config.Save(s.path, s.cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "儲存失敗: "+err.Error())
		return
	}
	// 改密後所有舊 session 失效，強制重新登入
	s.sessions = make(map[string]time.Time)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- 設定 ---

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"proxyUrl": s.cfg.ProxyURL})
	case http.MethodPut:
		var in struct {
			ProxyURL string `json:"proxyUrl"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if err := config.ValidateProxyURL(in.ProxyURL); err != nil {
			writeError(w, http.StatusBadRequest, "代理設定無效: "+err.Error())
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cfg.ProxyURL = in.ProxyURL
		if err := config.Save(s.path, s.cfg); err != nil {
			writeError(w, http.StatusInternalServerError, "儲存失敗: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"proxyUrl": s.cfg.ProxyURL})
	default:
		writeError(w, http.StatusMethodNotAllowed, "僅支援 GET / PUT")
	}
}

// --- profiles ---

func (s *Server) handleProfilesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "僅支援 GET")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"profiles": s.cfg.Profiles})
}

func (s *Server) handleProfilesItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
	if rest == "" {
		writeError(w, http.StatusNotFound, "找不到資源")
		return
	}
	parts := strings.Split(rest, "/")
	switch {
	case len(parts) == 1 && r.Method == http.MethodPut:
		s.upsertProfile(w, parts[0], r)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.deleteProfile(w, parts[0])
	case len(parts) == 2 && parts[1] == "start" && r.Method == http.MethodPost:
		s.startProfile(w, parts[0])
	case len(parts) == 2 && parts[1] == "stop" && r.Method == http.MethodPost:
		s.stopProfile(w, parts[0])
	default:
		writeError(w, http.StatusNotFound, "找不到資源")
	}
}

func (s *Server) upsertProfile(w http.ResponseWriter, id string, r *http.Request) {
	var p config.Profile
	if !decodeBody(w, r, &p) {
		return
	}
	if p.ID != "" && p.ID != id {
		writeError(w, http.StatusBadRequest, "路徑與內容的 profile ID 不一致")
		return
	}
	p.ID = id
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "profile 無效: "+err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.UpsertProfile(p)
	if err := config.Save(s.path, s.cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "儲存失敗: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProfile(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rn, ok := s.runners[id]; ok {
		rn.Stop() // 冪等：執行中先停，已結束無副作用
		delete(s.runners, id)
	}
	if !s.cfg.RemoveProfile(id) {
		writeError(w, http.StatusNotFound, "profile 不存在")
		return
	}
	if err := config.Save(s.path, s.cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "儲存失敗: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- 啟停與統計 ---

func (s *Server) startProfile(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.cfg.Profile(id)
	if !ok {
		writeError(w, http.StatusNotFound, "profile 不存在")
		return
	}
	if rn, ok := s.runners[id]; ok && rn.Stats().Running {
		writeError(w, http.StatusConflict, "任務執行中，無法重複啟動")
		return
	}
	hc, err := burner.NewHTTPClient(s.cfg.ResolveProxyURL(*p))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "代理設定無效: "+err.Error())
		return
	}
	rn, err := burner.NewRunner(*p, hc, time.Second)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "建立任務失敗: "+err.Error())
		return
	}
	if err := rn.Start(s.baseCtx); err != nil {
		writeError(w, http.StatusInternalServerError, "啟動失敗: "+err.Error())
		return
	}
	s.runners[id] = rn
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) stopProfile(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rn, ok := s.runners[id]; ok {
		rn.Stop()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "僅支援 GET")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := make(map[string]burner.Stats, len(s.runners))
	for id, rn := range s.runners {
		stats[id] = rn.Stats()
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats})
}
