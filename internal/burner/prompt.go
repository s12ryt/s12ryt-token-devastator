package burner

import (
	"strings"

	"token-devastator/internal/config"
)

// charsPerToken 填充文字的粗略估計：約 4 個 ASCII 字符 ≈ 1 token。
const charsPerToken = 4

// minInputTokens 純輸出策略的精簡 prompt 基本內容。
const outputPrompt = "請輸出盡可能長的連續文字，內容不拘，直到達到你的輸出上限。不要停、不要摘要、不要問問題，直接開始輸出。"

// marginTokens 估計扣除系統預留（來回開銷與安全餘量）的 token 數。
func marginTokens(window int64) int64 {
	m := window / 20
	if m < 256 {
		m = 256
	}
	return m
}

// fillPrompt 生成約 targetTokens（×4 字符）長度的填充文字。
func fillPrompt(targetTokens int64) string {
	if targetTokens < 8 {
		return outputPrompt
	}
	head := "以下是一段需要完整讀取的資料，讀完後請簡短回覆「已完成」：\n"
	n := int(targetTokens) * charsPerToken
	if int64(len(head)) >= int64(n) {
		return head
	}
	n -= len(head)
	return head + strings.Repeat("x", n)
}

// BuildPrompt 依消耗策略生成 prompt 與本次請求的 maxTokens。
//
//   - both：輸入逼近（窗口 − 最大輸出 − 餘量），輸出上限 = MaxOutputTokens
//   - output：精簡 prompt，輸出上限 = MaxOutputTokens
//   - input：輸入逼近（窗口 − 16 − 餘量），輸出上限 = 16（僅滿足協議必填）
func BuildPrompt(p config.Profile) (string, int) {
	win, err := p.ContextWindowTokens()
	if err != nil || win <= 0 {
		win = 8192 // 防禦性保底；正常流程 Validate 已擋下非法值
	}
	switch p.Strategy {
	case config.StrategyInput:
		return fillPrompt(win - 16 - marginTokens(win)), 16
	case config.StrategyOutput:
		return outputPrompt, p.MaxOutputTokens
	default: // both
		target := win - int64(p.MaxOutputTokens) - marginTokens(win)
		return fillPrompt(target), p.MaxOutputTokens
	}
}
