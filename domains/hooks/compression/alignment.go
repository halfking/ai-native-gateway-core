// ── docs/design/2026-08-09 O-2: AlignmentMap ──────────────────────────────────
//
// buildAlignmentMap 计算"压缩前 → 压缩后"的消息位置映射，供审计查询
// "第 N 条原始消息去了哪里"。调用点：window-triggered 重写发生的三个分支
// （LLM 摘要成功 / LLM 回退机械裁剪 / degraded 机械裁剪）。
package compression

import "encoding/json"

// buildAlignmentMap computes the original→compressed message index mapping
// when a compression rewrote `before` into `after`. Returns one entry per
// original message (len(before) entries) so callers can index it by
// OriginalIndex directly. Returns nil when `before` has no messages or fails
// to parse.
//
// Semantics per message:
//   - Retained 1:1: IsCompressed=false, CompressedIndex = position in `after`.
//   - Folded into an LLM summary: IsCompressed=true, CompressedIndex and
//     CompressedInto both = the summary message index (summaryIdx).
//   - Dropped by mechanical trim: IsCompressed=true, CompressedIndex and
//     CompressedInto = -1 (no single replacement).
//
// summaryIdx is the index of the summary message in `after`, or -1 when the
// rewrite did not produce a summary (mechanical trim / degraded path).
func buildAlignmentMap(before, after []byte, summaryIdx int) []AlignmentInfo {
	beforeMsgs, err := extractMessages(before)
	if err != nil || len(beforeMsgs) == 0 {
		return nil
	}
	afterByHash := make(map[string][]int, len(beforeMsgs))
	if afterMsgs, err := extractMessages(after); err == nil {
		for i, m := range afterMsgs {
			if h := msgHash(m); h != "" {
				afterByHash[h] = append(afterByHash[h], i)
			}
		}
	}
	align := make([]AlignmentInfo, 0, len(beforeMsgs))
	usedAfter := make(map[string]int, len(afterByHash))
	for i, m := range beforeMsgs {
		h := msgHash(m)
		info := AlignmentInfo{
			OriginalIndex:   i,
			CompressedIndex: -1,
			IsCompressed:    true,
			CompressedInto:  -1,
			Hash:            h,
		}
		if h != "" {
			positions := afterByHash[h]
			used := usedAfter[h]
			if used < len(positions) {
				info.IsCompressed = false
				info.CompressedIndex = positions[used]
				usedAfter[h] = used + 1
			} else if summaryIdx >= 0 {
				info.CompressedIndex = summaryIdx
				info.CompressedInto = summaryIdx
			}
		}
		align = append(align, info)
	}
	return align
}

// firstAssistantIndex returns the index of the first assistant message in
// body, or -1 when none exists. Mirrors the lookup used by
// injectSummaryMarker so the summary message index stays consistent with the
// injected smm_v1 marker.
func firstAssistantIndex(body []byte) int {
	msgs, err := extractMessages(body)
	if err != nil {
		return -1
	}
	for i, m := range msgs {
		var msg struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(m, &msg) == nil && msg.Role == "assistant" {
			return i
		}
	}
	return -1
}

// marshalAlignment serializes an AlignmentMap for the result memo. Returns
// nil when the map is empty so the memo entry stays small.
func marshalAlignment(am []AlignmentInfo) json.RawMessage {
	if len(am) == 0 {
		return nil
	}
	b, _ := json.Marshal(am)
	return b
}
