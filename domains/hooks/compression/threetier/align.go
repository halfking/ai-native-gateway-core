// Package threetier - align.go (2026-09-01)
//
// 把 v7 AlignmentInfo 归并为按 Tier 排列的 Range 序列，并提供跨层一致性校验。
package threetier

import (
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// Range 表示某一层在某段 [Start, End) 索引范围内的内容。
type Range struct {
	Tier  Tier
	Start int // inclusive message index
	End   int // exclusive message index
	// Layer 是 Stage 标签（如 "summary"/"trim"/"sanitize"），用于日志可观测。
	Layer string
}

// BuildAlignments 把 SessionState.AlignmentMap 转换为按 Tier 排序的 []Range。
// 当 AlignmentMap 为 nil 或空时返回空切片（不视为缺失 — 全新会话在首请求前
// 不会有 alignment）。
func BuildAlignments(s *compression.SessionState) []Range {
	if s == nil || len(s.AlignmentMap) == 0 {
		return nil
	}
	out := make([]Range, 0, len(s.AlignmentMap))
	for _, a := range s.AlignmentMap {
		if a.CompressedIndex < 0 {
			continue
		}
		layer := "retained"
		if a.IsCompressed {
			layer = "compressed"
		}
		out = append(out, Range{
			Tier:  TierCompressed,
			Start: a.CompressedIndex,
			End:   a.CompressedIndex + 1,
			Layer: layer,
		})
	}
	return out
}

// Misalignment 描述一层 offset 与参考层不一致的具体情形。
type Misalignment struct {
	Tier          Tier
	ExpectedStart int
	ExpectedEnd   int
	ActualStart   int
	ActualEnd     int
	Reason        string
}

// DetectMisalignment 校验三层 offset 的语义一致性。返回 []Misalignment；
// 空切片表示三层对齐良好。
//
// 不变量：
//   - Raw.Tokens ≥ Compressed.Tokens（压缩必然减少或保持 token）
//   - Compressed.Messages ≤ Raw.Messages
//   - Sanitize 引用存在 → Raw 与 Compressed 必须都已设置（否则中间层被跳过）
func DetectMisalignment(s *compression.SessionState) []Misalignment {
	if s == nil {
		return nil
	}
	var out []Misalignment

	// L1 vs L2
	if s.RawTokenEstimate > 0 && s.CompressedTokens > 0 {
		if s.CompressedTokens > s.RawTokenEstimate {
			out = append(out, Misalignment{
				Tier:          TierCompressed,
				ExpectedStart: s.RawTokenEstimate,
				ExpectedEnd:   s.RawTokenEstimate,
				ActualStart:   s.CompressedTokens,
				ActualEnd:     s.CompressedTokens,
				Reason:        "compressed tokens exceed raw tokens (regression)",
			})
		}
	}
	if s.RawMsgCount > 0 && s.CompressedMsgs > 0 {
		if s.CompressedMsgs > s.RawMsgCount {
			out = append(out, Misalignment{
				Tier:          TierCompressed,
				ExpectedStart: s.RawMsgCount,
				ExpectedEnd:   s.RawMsgCount,
				ActualStart:   s.CompressedMsgs,
				ActualEnd:     s.CompressedMsgs,
				Reason:        "compressed messages exceed raw messages (regression)",
			})
		}
	}

	// L3 vs L1/L2：sanitize 仅在 L1/L2 都设置过的情况下有意义
	if s.SanitizeMapRef != "" {
		if s.RawTokenEstimate == 0 && s.CompressedTokens == 0 {
			out = append(out, Misalignment{
				Tier:   TierSanitized,
				Reason: "sanitize map set but no raw/compressed tokens recorded (intermediate layer missing)",
			})
		}
	}

	return out
}

// String 便于日志输出。
func (m Misalignment) String() string {
	if m.Reason == "" {
		return fmt.Sprintf("tier=%s expected=[%d,%d) actual=[%d,%d)",
			m.Tier.Name(), m.ExpectedStart, m.ExpectedEnd, m.ActualStart, m.ActualEnd)
	}
	return fmt.Sprintf("tier=%s %s expected=[%d,%d) actual=[%d,%d)",
		m.Tier.Name(), m.Reason, m.ExpectedStart, m.ExpectedEnd, m.ActualStart, m.ActualEnd)
}