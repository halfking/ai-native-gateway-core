package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Quality fix mode constants — mirror the providers.quality_fix_mode
// column CHECK constraint (see db/migrations/017_quality_fix_mode.sql).
const (
	QualityModeOff         = "off"
	QualityModeDetectOnly  = "detect_only"
	QualityModeFix         = "fix"
)

// Quality flag constants — stable identifiers used in
// request_logs.quality_flags. Add new tags here, never inline string
// literals, so dashboards and SQL aggregations stay consistent.
const (
	QualityFlagEmptyToolName      = "empty_tool_name"
	QualityFlagDuplicateToolID    = "duplicate_tool_call_id"
	QualityFlagXMLInToolCalls     = "xml_in_tool_calls"
	QualityFlagEmptyToolNameAll   = "all_empty_tool_names"
)

// QualityFixResult is the output of ProcessNonStreamBody. Callers
// persist Flags + FixActions into request_logs and emit a metric.
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
//   off         : no work, returns (body, empty result).
//   detect_only : scan only; body returned byte-identical.
//   fix         : scan + rewrite; renamed/dropped counts recorded.
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
// `delta.tool_calls` array with empty names, we either tag (detect_only)
// or rewrite (fix) the line. The returned flags accumulate across the
// entire stream — caller passes the accumulator back in on every line.
//
// Streaming detection is intentionally narrower than non-stream: we only
// look at the per-chunk `delta.tool_calls[].function.name` and dedup
// `delta.tool_calls[].id` within the chunk. Empty `content` fields are
// left alone — that's the stream-level renderer's job, not ours.
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
	for _, rawChoice := range choices {
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
		for _, rawTC := range rawTCs {
			tc, ok := rawTC.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			if fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			id, _ := tc["id"].(string)
			if name == "" {
				emptyCount++
				if mode == QualityModeFix {
					// Index in the per-chunk array is best we can do for a
					// streaming rewrite — we don't have the call_id until
					// the model decides on it. Use a position-based suffix
					// so the consumer can at least tell which chunk the
					// unknown tool came from.
					fn["name"] = fmt.Sprintf("__unknown_tool_stream_%d__", len(accFlags))
					changed = true
				}
			}
			if id != "" {
				// First-line callers (and a few tests) pass a nil map;
				// lazy-init here so the dedup path is allocation-free
				// when no tool_call ids repeat within the stream.
				if seenIDs == nil {
					seenIDs = map[string]int{}
				}
				if seen, ok := seenIDs[id]; ok {
					dupCount++
					if mode == QualityModeFix {
						tc["id"] = fmt.Sprintf("%s__dup%d", id, seen+1)
						changed = true
					}
					seenIDs[id] = seen + 1
				} else {
					seenIDs[id] = 1
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

// chatQualityReport is the internal scan result for a non-stream body.
type chatQualityReport struct {
	Flags             []string
	EmptyNameCount    int
	DuplicateIDCount  int
	AllEmptyName      bool
	HasStructuredTC   bool
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
			} else if seen, ok := seenIDs[tc.ID]; ok && tc.ID != "" {
				rep.DuplicateIDCount++
				seenIDs[tc.ID] = seen + 1
				continue
			}
			if tc.ID != "" {
				seenIDs[tc.ID] = 1
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
	return QualityFixResult{
		Flags:       rep.Flags,
		FixActions:  map[string]any{},
		Score:       nil,
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

// WrapQualityProcessStreamLine mirrors WrapQualityProcessNonStream
// for the streaming path. The closure takes an accumulator slice +
// seen-ids map and returns the rewritten accumulator state; the
// executor's StreamChat hook in main.go can call it on every SSE
// line, but in practice we drive the call from relay/stream.go
// directly via the context value (SetQualityFixModeOnContext).
//
// This wrapper is provided for callers that prefer the explicit
// function-pointer path over context plumbing.
func WrapQualityProcessStreamLine() func(line, mode string, accFlags []string, seenIDs map[string]int) (outLine string, outFlags []string, outSeen map[string]int) {
	return func(line, mode string, accFlags []string, seenIDs map[string]int) (string, []string, map[string]int) {
		return ProcessStreamLine(line, mode, accFlags, seenIDs)
	}
}

// WrapSetQualityFixModeOnContext exposes SetQualityFixModeOnContext
// as a closure that matches the routing.QualitySetModeFunc shape
// (context.Context, string) → context.Context. cmd/gateway/main.go
// wires this into Executor.QualitySetMode so the routing executor
// can stamp the per-provider mode without importing relay.
func WrapSetQualityFixModeOnContext() func(ctx context.Context, mode string) context.Context {
	return SetQualityFixModeOnContext
}
