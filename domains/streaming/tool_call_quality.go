package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Quality fix mode constants — mirror the providers.quality_fix_mode
// column CHECK constraint (see db/migrations/017_quality_fix_mode.sql).
const (
	QualityModeOff        = "off"
	QualityModeDetectOnly = "detect_only"
	QualityModeFix        = "fix"
)

// Quality flag constants — stable identifiers used in
// _request_logs.quality_flags. Add new tags here, never inline string
// literals, so dashboards and SQL aggregations stay consistent.
const (
	QualityFlagEmptyToolName       = "empty_tool_name"
	QualityFlagDuplicateToolID     = "duplicate_tool_call_id"
	QualityFlagXMLInToolCalls      = "xml_in_tool_calls"
	QualityFlagEmptyToolNameAll    = "all_empty_tool_names"
	QualityFlagUpstreamContextLoss = "upstream_context_loss"
)

// QualityFixResult is the output of ProcessNonStreamBody. Callers
// persist Flags + FixActions into _request_logs and emit a metric.
// Score is 0..1 (higher = better). nil = "not evaluated" (off mode).
type QualityFixResult struct {
	Flags      []string       `json:"flags"`
	FixActions map[string]any `json:"fix_actions"`
	Score      *float64       `json:"score,omitempty"`
	// BodyChanged is true when ProcessNonStreamBody actually rewrote
	// the body. detect_only mode always returns BodyChanged=false.
	BodyChanged bool `json:"body_changed"`
}

// FixActionsJSON marshals FixActions to JSONB. Empty map → nil so
// the column DEFAULT '{}' takes over (avoids storing a useless
// empty object on detect_only rows).
func (r QualityFixResult) FixActionsJSON() []byte {
	if len(r.FixActions) == 0 {
		return nil
	}
	out, err := json.Marshal(r.FixActions)
	if err != nil {
		return nil
	}
	return out
}

// ProcessNonStreamBody runs tool_call quality detection on an OpenAI-
// chat-completions response body and, when mode is "fix", rewrites the
// body in place. Returns (possibly rewritten body, signals).
//
// Behaviour by mode:
//
//	off         : no work, returns (body, empty result).
//	detect_only : scan only; body returned byte-identical.
//	fix         : scan + rewrite; renamed/dropped counts recorded.
//
// This is the canonical entry point — both the routing executor and
// tests should funnel through here so a new check is added in ONE place.
func ProcessNonStreamBody(body []byte, mode string) ([]byte, QualityFixResult) {
	switch mode {
	case "", QualityModeOff:
		return body, QualityFixResult{}
	}

	report := analyzeChatBody(body)

	if mode == QualityModeDetectOnly {
		return body, reportToResult(report, false)
	}

	// mode == "fix" — apply rewrites
	rewritten, fixActions, changed := applyFixes(body, report)
	score := computeScore(report)
	return rewritten, QualityFixResult{
		Flags:       report.Flags,
		FixActions:  fixActions,
		Score:       &score,
		BodyChanged: changed,
	}
}

// ProcessStreamLine is the streaming equivalent of ProcessNonStreamBody.
// `line` is one full "data: {...}\n" SSE chunk. When the line carries a
// `delta.tool_calls` array with explicit empty names, we tag the line without
// inventing an executable name. The returned flags accumulate across the
// entire stream — caller passes the accumulator back in on every line.
//
// Tool identity is (choice index, tool index), so a repeated id on an argument
// continuation is preserved. Different tools sharing an id are tagged and, in
// fix mode, receive stable suffixes. seenIDs carries this request-local state;
// its keys are opaque. Empty content is left to the stream-level renderer.
func ProcessStreamLine(line string, mode string, accFlags []string, seenIDs map[string]int) (string, []string, map[string]int) {
	if mode == "" || mode == QualityModeOff {
		return line, accFlags, seenIDs
	}
	if !strings.HasPrefix(line, "data: ") {
		return line, accFlags, seenIDs
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if payload == "" || payload == "[DONE]" {
		return line, accFlags, seenIDs
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return line, accFlags, seenIDs
	}
	choices, ok := obj["choices"].([]any)
	if !ok {
		return line, accFlags, seenIDs
	}

	changed := false
	emptyCount := 0
	dupCount := 0
	for choicePosition, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		rawTCs, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}
		choiceIndex := streamQualityIndex(choice["index"], choicePosition)
		for toolPosition, rawTC := range rawTCs {
			tc, ok := rawTC.(map[string]any)
			if !ok {
				continue
			}
			if seenIDs == nil {
				seenIDs = map[string]int{}
			}
			slot := fmt.Sprintf("%d/%d", choiceIndex, streamQualityIndex(tc["index"], toolPosition))
			fn, _ := tc["function"].(map[string]any)
			name, hasName := fn["name"].(string)
			id, _ := tc["id"].(string)
			if name != "" {
				seenIDs["named:"+slot] = 1
			} else if seenIDs["named:"+slot] == 0 && seenIDs["empty:"+slot] == 0 &&
				(hasName || id != "") {
				// ★ 2026-10-20 12h 审计轮修正：**缺 name 键**与**显式空串**
				// 同性质，都是「空名」——原实现只认显式空串（hasName），
				// 于是「有 id 但没有 name 键」的 tool_call（正是本特性要抓的
				// 缺陷）在流式路径下**不再被标记**，而**非流式**路径
				// （结构体字段零值 == ""）仍然标记 ⇒ 同一上游缺陷的判定结果
				// 取决于走没走 SSE。
				// 判别依据：tool_call 的**首帧**带 id，续帧不带 id
				// ⇒ 「无 name 键 + 有 id」= 首帧就缺 name ⇒ 缺陷。
				// 「empty:slot」保证同一槽位只计一次（续帧不重复计）。
				emptyCount++
				seenIDs["empty:"+slot] = 1
			}
			if id != "" {
				callKey := "call:" + slot + ":" + id
				ordinal := seenIDs[callKey]
				if ordinal == 0 {
					idKey := "id:" + id
					ordinal = seenIDs[idKey] + 1
					seenIDs[idKey] = ordinal
					seenIDs[callKey] = ordinal
				}
				if ordinal > 1 {
					dupCount++
					if mode == QualityModeFix {
						tc["id"] = fmt.Sprintf("%s__dup%d", id, ordinal)
						changed = true
					}
				}
			}
		}
	}

	if emptyCount > 0 {
		accFlags = appendOrUnique(accFlags, QualityFlagEmptyToolName)
	}
	if dupCount > 0 {
		accFlags = appendOrUnique(accFlags, QualityFlagDuplicateToolID)
	}

	if !changed {
		return line, accFlags, seenIDs
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return line, accFlags, seenIDs
	}
	return "data: " + string(out) + "\n", accFlags, seenIDs
}

func streamQualityIndex(value any, fallback int) int {
	if index, ok := value.(float64); ok && index >= 0 && index < 1<<31 && index == float64(int(index)) {
		return int(index)
	}
	return fallback
}

// chatQualityReport is the internal scan result for a non-stream body.
type chatQualityReport struct {
	Flags            []string
	EmptyNameCount   int
	DuplicateIDCount int
	AllEmptyName     bool
	HasStructuredTC  bool
}

// analyzeChatBody walks a chat.completion JSON body and reports
// tool_call quality issues. Pure function — does NOT mutate body.
func analyzeChatBody(body []byte) chatQualityReport {
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return chatQualityReport{}
	}

	rep := chatQualityReport{HasStructuredTC: true}
	seenIDs := map[string]int{}
	totalTCs := 0
	emptyTCs := 0

	for _, ch := range resp.Choices {
		for _, tc := range ch.Message.ToolCalls {
			totalTCs++
			if tc.Function.Name == "" {
				emptyTCs++
			}
			if tc.ID != "" {
				if seen, ok := seenIDs[tc.ID]; ok {
					rep.DuplicateIDCount++
					seenIDs[tc.ID] = seen + 1
				} else {
					seenIDs[tc.ID] = 1
				}
			}
		}
	}

	if emptyTCs > 0 {
		rep.Flags = append(rep.Flags, QualityFlagEmptyToolName)
		rep.EmptyNameCount = emptyTCs
		if totalTCs > 0 && emptyTCs == totalTCs {
			rep.AllEmptyName = true
			rep.Flags = append(rep.Flags, QualityFlagEmptyToolNameAll)
		}
	}
	if rep.DuplicateIDCount > 0 {
		rep.Flags = append(rep.Flags, QualityFlagDuplicateToolID)
	}
	return rep
}

// applyFixes rewrites the body in place per the report. Returns the
// (possibly new) body, a per-flag action summary, and whether anything
// actually changed. Pure-with-side-effects-on-copy: input body is never
// mutated.
func applyFixes(body []byte, rep chatQualityReport) ([]byte, map[string]any, bool) {
	if len(rep.Flags) == 0 {
		return body, map[string]any{}, false
	}

	// Operate on a deep copy so we can decide "did anything change" by
	// comparing the marshalled bytes. Cheap because chat completions
	// bodies are < 1 MB in practice.
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return body, map[string]any{}, false
	}

	actions := map[string]any{}
	renamed := 0
	dupFixed := 0
	allRenamed := true

	choices, ok := resp["choices"].([]any)
	if !ok {
		return body, map[string]any{}, false
	}

	seenIDs := map[string]int{}
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		message, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}
		rawTCs, ok := message["tool_calls"].([]any)
		if !ok {
			continue
		}
		for i, rawTC := range rawTCs {
			tc, ok := rawTC.(map[string]any)
			if !ok {
				allRenamed = false
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			if fn == nil {
				fn = map[string]any{}
				tc["function"] = fn
			}
			if name, _ := fn["name"].(string); name == "" {
				fn["name"] = fmt.Sprintf("__unknown_tool_%d__", i)
				renamed++
			} else {
				allRenamed = false
			}
			if id, _ := tc["id"].(string); id != "" {
				if seen, ok := seenIDs[id]; ok {
					tc["id"] = fmt.Sprintf("%s__dup%d", id, seen+1)
					dupFixed++
					seenIDs[id] = seen + 1
				} else {
					seenIDs[id] = 1
				}
			}
		}
		// If every tool_call in this message was empty-named, downgrade
		// finish_reason to "stop" so the consumer treats the response as
		// a normal assistant turn instead of a tool turn. This is the
		// safety net for the "whole model output is one big NoSuchTool"
		// failure mode that brought us here in the first place.
		if rep.AllEmptyName {
			if fr, _ := choice["finish_reason"].(string); fr == "tool_calls" {
				choice["finish_reason"] = "stop"
			}
		}
	}

	if renamed > 0 {
		actions[QualityFlagEmptyToolName] = map[string]int{
			"detected": rep.EmptyNameCount,
			"renamed":  renamed,
		}
	}
	if dupFixed > 0 {
		actions[QualityFlagDuplicateToolID] = map[string]int{
			"detected": rep.DuplicateIDCount,
			"renamed":  dupFixed,
		}
	}
	if rep.AllEmptyName && allRenamed {
		// Add an extra action so dashboards can tell the "all-empty"
		// case apart from "mixed". The downgraded finish_reason is the
		// structural side-effect; this counter is the bookkeeping.
		actions[QualityFlagEmptyToolNameAll] = map[string]int{
			"downgraded_finish_reason": 1,
		}
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return body, map[string]any{}, false
	}
	return out, actions, true
}

// reportToResult converts the internal report into the exported
// QualityFixResult for detect_only mode (no score, no body change).
func reportToResult(rep chatQualityReport, bodyChanged bool) QualityFixResult {
	var score *float64
	if len(rep.Flags) > 0 {
		v := computeScore(rep)
		score = &v
	}
	return QualityFixResult{
		Flags:       rep.Flags,
		FixActions:  map[string]any{},
		Score:       score,
		BodyChanged: bodyChanged,
	}
}

// computeScore is a simple 0..1 quality score. Currently binary:
//   - 1.00 when no issues detected
//   - 0.50 when only `empty_tool_name` was detected (the most common
//     "dumb upstream" case; still usable after rename)
//   - 0.00 when `all_empty_tool_names` (whole response was unusable
//     before the fix)
//   - 0.30 when only `duplicate_tool_call_id` (less severe)
//
// Future tuning: weight by number of affected tool_calls vs total.
func computeScore(rep chatQualityReport) float64 {
	if len(rep.Flags) == 0 {
		return 1.0
	}
	if rep.AllEmptyName {
		return 0.0
	}
	hasEmpty := false
	hasDup := false
	for _, f := range rep.Flags {
		switch f {
		case QualityFlagEmptyToolName:
			hasEmpty = true
		case QualityFlagDuplicateToolID:
			hasDup = true
		}
	}
	switch {
	case hasEmpty && hasDup:
		return 0.4
	case hasEmpty:
		return 0.5
	case hasDup:
		return 0.3
	default:
		return 0.7
	}
}

func appendOrUnique(slice []string, v string) []string {
	for _, s := range slice {
		if s == v {
			return slice
		}
	}
	return append(slice, v)
}

// WrapQualityProcessNonStream returns a closure suitable for
// ChatExecutor.QualityProcessNonStream. The closure unpacks the
// QualityFixResult and exposes its inner fields as the flat
// (body, flags, fixActions, score) tuple the executor expects.
//
// The indirection exists so cmd/gateway/main.go can wire the relay-
// side processor into the routing-side executor without an import
// cycle (routing cannot import relay).
func WrapQualityProcessNonStream() func(body []byte, mode string) (outBody []byte, flags []string, fixActions []byte, score *float64) {
	return func(body []byte, mode string) ([]byte, []string, []byte, *float64) {
		out, res := ProcessNonStreamBody(body, mode)
		return out, res.Flags, res.FixActionsJSON(), res.Score
	}
}

// WrapSetQualityFixModeOnContext exposes SetQualityFixModeOnContext
// as a closure that matches the streaming.QualitySetModeFunc shape
// (context.Context, string) → context.Context. cmd/gateway/main.go
// wires this into Executor.QualitySetMode so the routing executor
// can stamp the per-provider mode without importing relay.
func WrapSetQualityFixModeOnContext() func(ctx context.Context, mode string) context.Context {
	return SetQualityFixModeOnContext
}
