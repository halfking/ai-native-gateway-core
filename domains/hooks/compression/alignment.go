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
	return buildAlignmentMapForProtocol(before, after, summaryIdx, "openai")
}

func buildAlignmentMapForProtocol(before, after []byte, summaryIdx int, protocol string) []AlignmentInfo {
	beforeMsgs, err := extractMessages(before)
	if err != nil || len(beforeMsgs) == 0 {
		return nil
	}
	afterMsgs, err := extractMessages(after)
	if err != nil {
		afterMsgs = nil
	}
	if summaryIdx >= len(afterMsgs) {
		summaryIdx = -1
	}
	summaryInSystem := protocol == "anthropic-messages" && hasAnthropicSystemSummary(after)
	afterByHash := make(map[string][]int, len(beforeMsgs))
	for i, m := range afterMsgs {
		if h := msgHash(m); h != "" && !isSummaryMarkerMsg(m) {
			afterByHash[h] = append(afterByHash[h], i)
		}
	}
	align := make([]AlignmentInfo, 0, len(beforeMsgs))
	usedAfter := make(map[string]int, len(afterByHash))
	occurrences := make(map[string]int, len(beforeMsgs))
	for i, m := range beforeMsgs {
		h := msgHash(m)
		occurrence := occurrences[h]
		occurrences[h] = occurrence + 1
		info := AlignmentInfo{
			OriginalIndex: i, CompressedIndex: -1, IsCompressed: true,
			CompressedInto: -1, Hash: h, Occurrence: occurrence,
			TargetKind: "dropped", TargetSpace: "none",
		}
		if h != "" {
			positions := afterByHash[h]
			used := usedAfter[h]
			if used < len(positions) {
				info.IsCompressed = false
				info.CompressedIndex = positions[used]
				info.TargetKind = "retained"
				info.TargetSpace = "messages"
				usedAfter[h] = used + 1
			} else if summaryIdx >= 0 {
				info.CompressedIndex = summaryIdx
				info.CompressedInto = summaryIdx
				info.TargetKind = "summary"
				info.TargetSpace = "messages"
			} else if summaryInSystem {
				info.TargetKind = "summary"
				info.TargetSpace = "top_level_system"
			}
		}
		align = append(align, info)
	}
	return align
}

func hasAnthropicSystemSummary(body []byte) bool {
	var top struct {
		System json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(body, &top); err != nil || len(top.System) == 0 || string(top.System) == "null" {
		return false
	}
	var content string
	if json.Unmarshal(top.System, &content) == nil {
		return isAnthropicSummaryContent(content)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(top.System, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		if block.Type == "text" && isAnthropicSummaryContent(block.Text) {
			return true
		}
	}
	return false
}

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
