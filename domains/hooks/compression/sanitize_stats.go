// Package compression - sanitize_stats.go
//
// Phase 2 Task 2.2: 脱敏统计信息收集
//
// SanitizeStats 类型定义（由 security/sanitize 包填充）
package compression

// SanitizeStats tracks sanitization metrics for L3 (audited session).
// Phase 2 (2026-08-13): Added for three-tier cache semantic alignment.
//
// This type is defined here to avoid import cycles. The construction
// logic (BuildSanitizeStats) lives in security/sanitize package.
type SanitizeStats struct {
	PlaceholderCount int `json:"placeholder_count"`       // total placeholders generated
	PhoneCount       int `json:"phone_count,omitempty"`   // phone numbers sanitized
	EmailCount       int `json:"email_count,omitempty"`   // emails sanitized
	IDCardCount      int `json:"id_card_count,omitempty"` // ID cards sanitized
	CreditCardCount  int `json:"cc_count,omitempty"`      // credit cards sanitized
	SecretCount      int `json:"secret_count,omitempty"`  // secrets sanitized
	// OtherCount counts placeholders whose type has no named bucket above
	// (internal_ip, name, custom, …). Without it the sum of the named buckets
	// was silently smaller than PlaceholderCount — a self-contradicting
	// statistic persisted into session state, where internal_ip alone was ~30%
	// of all sanitized spans. Additive/omitempty, so older readers are
	// unaffected.
	OtherCount  int   `json:"other_count,omitempty"`
	SanitizedAt int64 `json:"sanitized_at"` // unix timestamp
}
