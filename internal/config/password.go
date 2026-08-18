package config

import (
	"crypto/sha256"
	"encoding/hex"
)

// DefaultPassword 面板預設密碼；登入後面板會提示更改。
const DefaultPassword = "admin"

// HashPassword 以 SHA-256 產生密碼雜湊（hex）。
// 本工具為本地/內網自用，無需抗 GPU 破解的慢雜湊；配合記憶體 session 已足夠。
func HashPassword(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

// CheckPassword 驗證密碼。PasswordHash 為空時接受預設密碼。
func (c *Config) CheckPassword(password string) bool {
	want := c.PasswordHash
	if want == "" {
		want = HashPassword(DefaultPassword)
	}
	return HashPassword(password) == want
}

// IsDefaultPassword 回傳當前是否仍使用預設密碼。
func (c *Config) IsDefaultPassword() bool {
	want := c.PasswordHash
	if want == "" {
		return true
	}
	return want == HashPassword(DefaultPassword)
}
