// Package threetier - tiers.go (2026-09-01)
//
// 定义三层枚举与 SessionState 字段映射。
package threetier

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// Tier 是三层缓存的语义枚举。Tier 序号与 SessionState 字段版本号一致。
type Tier int

const (
	TierRaw Tier = iota + 1 // 1: 原始会话（uncompressed body）
	TierCompressed           // 2: 压缩后（after Lite/Caveman/ToolFocused/LLM summary）
	TierSanitized            // 3: 脱敏后（after security/sanitize smart_sani_guard）
)

// Name 把 Tier 映射为可读字符串，与 request_logs.compression_strategy 标签对齐。
func (t Tier) Name() string {
	switch t {
	case TierRaw:
		return "raw"
	case TierCompressed:
		return "compressed"
	case TierSanitized:
		return "sanitized"
	default:
		return "unknown"
	}
}

// String 实现 fmt.Stringer。
func (t Tier) String() string { return t.Name() }

// FieldName 返回 SessionState 上承载该 tier 数据的 struct 字段名（用于反射 / 测试）。
// 注意：仅暴露静态名称，不执行实际 reflect.Value.FieldByName 调用，避免性能损耗。
func (t Tier) FieldName() string {
	switch t {
	case TierRaw:
		return "RawTokenEstimate/RawMsgCount"
	case TierCompressed:
		return "CompressedTokens/CompressedMsgs/CompressedPrefixHash/CompressionQuality"
	case TierSanitized:
		return "SanitizeMapRef/SanitizeStats"
	default:
		return ""
	}
}

// TokenCount 返回该 tier 的 token 计数（best-effort；脱敏层无 token 数据时返回 0）。
func TokenCount(s *compression.SessionState, t Tier) int {
	if s == nil {
		return 0
	}
	switch t {
	case TierRaw:
		return s.RawTokenEstimate
	case TierCompressed:
		return s.CompressedTokens
	case TierSanitized:
		return 0 // SanitizeStats 不含 token 计数；保守返回 0
	default:
		return 0
	}
}

// MessageCount 返回该 tier 的 message 计数。
func MessageCount(s *compression.SessionState, t Tier) int {
	if s == nil {
		return 0
	}
	switch t {
	case TierRaw:
		return s.RawMsgCount
	case TierCompressed:
		return s.CompressedMsgs
	case TierSanitized:
		// SanitizeStats 不含 message 计数。返回 0。
		return 0
	default:
		return 0
	}
}

// TierSnapshot 是某一层在某一时刻的紧凑快照，便于跨层校验。
type TierSnapshot struct {
	Tier      Tier
	Tokens    int
	Messages  int
	HasPrefix bool   // 是否设置了 prefix hash（压缩层才有效）
	PrefixSHA string // CompressedPrefixHash 的 16 字符前缀
	HasMap    bool   // 是否有 SanitizeMapRef（脱敏层才有效）
}

// Snapshot 一次性提取三层快照，避免多次字段访问。
func Snapshot(s *compression.SessionState) [3]TierSnapshot {
	if s == nil {
		return [3]TierSnapshot{}
	}
	raw := TierSnapshot{Tier: TierRaw, Tokens: s.RawTokenEstimate, Messages: s.RawMsgCount}
	cmp := TierSnapshot{
		Tier:      TierCompressed,
		Tokens:    s.CompressedTokens,
		Messages:  s.CompressedMsgs,
		HasPrefix: s.CompressedPrefixHash != "",
		PrefixSHA: truncate16(s.CompressedPrefixHash),
	}
	san := TierSnapshot{
		Tier:   TierSanitized,
		HasMap: s.SanitizeMapRef != "",
	}
	return [3]TierSnapshot{raw, cmp, san}
}

// ShortHash 把任意字节流 hash 后取 16 字符前缀，用于跨层对齐比对。
func ShortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

func truncate16(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[:16]
}