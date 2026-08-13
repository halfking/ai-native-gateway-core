// Package compression - sanitize_stats.go
//
// Phase 2 Task 2.2: 脱敏统计信息收集
//
// 从 sanitize.SanitizeResult 生成 SanitizeStats，用于填充 SessionState 的 L3 字段。
package compression

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

// SanitizeStats tracks sanitization metrics for L3 (audited session).
// Phase 2 (2026-08-13): Added for three-tier cache semantic alignment.
type SanitizeStats struct {
	PlaceholderCount int   `json:"placeholder_count"`       // total placeholders generated
	PhoneCount       int   `json:"phone_count,omitempty"`   // phone numbers sanitized
	EmailCount       int   `json:"email_count,omitempty"`   // emails sanitized
	IDCardCount      int   `json:"id_card_count,omitempty"` // ID cards sanitized
	CreditCardCount  int   `json:"cc_count,omitempty"`      // credit cards sanitized
	SecretCount      int   `json:"secret_count,omitempty"`  // secrets sanitized
	SanitizedAt      int64 `json:"sanitized_at"`            // unix timestamp
}

// BuildSanitizeStats 从 SanitizeResult 构建统计信息
//
// 参数：
//   - result: 脱敏结果（包含 Fragments 和 SanitizeMap）
//
// 返回：
//   - SanitizeStats: 脱敏统计（各类型计数 + 时间戳）
func BuildSanitizeStats(result *sanitize.SanitizeResult) SanitizeStats {
	if result == nil || len(result.Fragments) == 0 {
		return SanitizeStats{}
	}

	stats := SanitizeStats{
		PlaceholderCount: len(result.SanitizeMap),
		SanitizedAt:      time.Now().Unix(),
	}

	// 统计各类型数量
	for _, fragment := range result.Fragments {
		switch fragment.Type {
		case sanitize.TypePhone:
			stats.PhoneCount++
		case sanitize.TypeEmail:
			stats.EmailCount++
		case sanitize.TypeIDCard:
			stats.IDCardCount++
		case sanitize.TypeCreditCard:
			stats.CreditCardCount++
		case sanitize.TypeSecret:
			stats.SecretCount++
		}
	}

	return stats
}
