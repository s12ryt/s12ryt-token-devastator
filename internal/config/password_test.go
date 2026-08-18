package config

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestHashPassword(t *testing.T) {
	h := HashPassword("admin")
	if h == "" || len(h) != 64 { // SHA-256 hex
		t.Errorf("HashPassword 應回傳 64 字元 hex，得到 %q", h)
	}
	if HashPassword("admin") != h {
		t.Error("同一密碼雜湊應一致")
	}
	if HashPassword("admin2") == h {
		t.Error("不同密碼雜湊不應相同")
	}
}

func TestCheckPassword(t *testing.T) {
	// 空雜湊 = 預設密碼 admin
	c := &Config{}
	if !c.CheckPassword("admin") {
		t.Error("空雜湊應接受預設密碼 admin")
	}
	if c.CheckPassword("wrong") {
		t.Error("空雜湊不應接受其他密碼")
	}
	c.PasswordHash = HashPassword("s3cret")
	if !c.CheckPassword("s3cret") {
		t.Error("已設密碼應通過")
	}
	if c.CheckPassword("admin") {
		t.Error("已設密碼後預設密碼應失效")
	}
	// 無鹽雜湊防長度擴展困擾：確認格式為 sha256(secret) hex
	want := sha256.Sum256([]byte("s3cret"))
	if c.PasswordHash != hex.EncodeToString(want[:]) {
		t.Error("雜湊格式應為 sha256 hex")
	}
}

func TestIsDefaultPassword(t *testing.T) {
	c := &Config{}
	if !c.IsDefaultPassword() {
		t.Error("空雜湊應視為預設密碼")
	}
	c.PasswordHash = HashPassword("x")
	if c.IsDefaultPassword() {
		t.Error("自訂密碼不應視為預設")
	}
	c.PasswordHash = HashPassword("admin")
	if !c.IsDefaultPassword() {
		t.Error("雜湊值等於 admin 的雜湊應視為預設密碼")
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{Listen: DefaultListen, PasswordHash: HashPassword("newpass")}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CheckPassword("newpass") {
		t.Error("密碼雜湊應隨配置持久化")
	}
}
