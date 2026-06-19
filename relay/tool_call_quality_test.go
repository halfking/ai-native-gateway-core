package relay

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProcessNonStreamBody_OffMode verifies that 'off' is a hard
// no-op: body bytes are returned untouched and the result carries
// no flags / no score / no fix actions. This is the default mode
// for every existing provider after the migration; the test
// guarantees the upgrade is behaviour-preserving.
func TestProcessNonStreamBody_OffMode(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"x","function":{"name":""}}]}}]}`)
	out, res := ProcessNonStreamBody(body, QualityModeOff)
	if string(out) != string(body) {
		t.Fatalf("off mode must be byte-identical; got diff")
	}
	if len(res.Flags) != 0 || res.Score != nil || len(res.FixActions) != 0 {
		t.Fatalf("off mode must not produce signals; got %+v", res)
	}
}

// TestProcessNonStreamBody_DetectOnly verifies detect_only mode flags
// the issue and exposes the score but does NOT modify the body. The
// raw upstream payload is still visible in the request_log for
// forensic analysis.
func TestProcessNonStreamBody_DetectOnly(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":"b"}}]}}]}`)
	out, res := ProcessNonStreamBody(body, QualityModeDetectOnly)
	if string(out) != string(body) {
		t.Fatalf("detect_only must be byte-identical; got diff")
	}
	if len(res.Flags) != 0 {
		t.Fatalf("clean body must have no flags, got %v", res.Flags)
	}
	if res.Score != nil {
		t.Fatalf("clean body must have nil score, got %v", *res.Score)
	}
	if res.BodyChanged {
		t.Fatalf("detect_only must report BodyChanged=false")
	}
}

// TestProcessNonStreamBody_FixMode_RenamesEmptyName is the
// regression test for the gpt-5.4 bug that started this whole
// effort. Empty function.name MUST be renamed to
// __unknown_tool_<i>__ so the consumer (opencode/AI SDK) finds
// the tool in its tool set and doesn't throw "Model tried to call
// unavailable tool ''".
func TestProcessNonStreamBody_FixMode_RenamesEmptyName(t *testing.T) {
	body := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[
					{"id":"a","type":"function","function":{"name":"","arguments":"{\"x\":1}"}},
					{"id":"b","type":"function","function":{"name":"get_weather","arguments":"{}"}}
				]
			},
			"finish_reason":"tool_calls"
		}]
	}`)
	out, res := ProcessNonStreamBody(body, QualityModeFix)
	if !res.BodyChanged {
		t.Fatal("fix mode must report BodyChanged=true")
	}
	if len(res.Flags) == 0 || res.Flags[0] != QualityFlagEmptyToolName {
		t.Fatalf("expected empty_tool_name flag, got %v", res.Flags)
	}
	if res.Score == nil || *res.Score != 0.5 {
		t.Fatalf("expected score=0.5 for single empty name, got %v", res.Score)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("rewritten body not valid JSON: %v\nbody=%s", err, out)
	}
	if got := parsed.Choices[0].Message.ToolCalls[0].Function.Name; got != "__unknown_tool_0__" {
		t.Fatalf("expected rename to __unknown_tool_0__, got %q", got)
	}
	if got := parsed.Choices[0].Message.ToolCalls[1].Function.Name; got != "get_weather" {
		t.Fatalf("non-empty name must be preserved, got %q", got)
	}
}

// TestProcessNonStreamBody_FixMode_AllEmptyDowngradesFinishReason
// covers the worst-case upstream output: every tool_call is empty
// named. The fix mode renames each to __unknown_tool_<i>__ AND
// downgrades finish_reason from "tool_calls" to "stop" so the
// consumer treats the response as a normal assistant turn instead
// of an unusable tool turn.
func TestProcessNonStreamBody_FixMode_AllEmptyDowngradesFinishReason(t *testing.T) {
	body := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[
					{"id":"a","type":"function","function":{"name":""}},
					{"id":"b","type":"function","function":{"name":""}}
				]
			},
			"finish_reason":"tool_calls"
		}]
	}`)
	out, res := ProcessNonStreamBody(body, QualityModeFix)
	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("rewritten body not valid JSON: %v", err)
	}
	if parsed.Choices[0].FinishReason != "stop" {
		t.Fatalf("expected finish_reason downgrade to stop, got %q", parsed.Choices[0].FinishReason)
	}
	if parsed.Choices[0].Message.ToolCalls[0].Function.Name != "__unknown_tool_0__" ||
		parsed.Choices[0].Message.ToolCalls[1].Function.Name != "__unknown_tool_1__" {
		t.Fatalf("expected both renamed with positional suffix, got %+v", parsed.Choices[0].Message.ToolCalls)
	}
	hasAllEmptyFlag := false
	for _, f := range res.Flags {
		if f == QualityFlagEmptyToolNameAll {
			hasAllEmptyFlag = true
		}
	}
	if !hasAllEmptyFlag {
		t.Fatalf("expected all_empty_tool_names flag, got %v", res.Flags)
	}
	if res.Score == nil || *res.Score != 0.0 {
		t.Fatalf("expected score=0.0 for all-empty, got %v", res.Score)
	}
}

// TestProcessNonStreamBody_FixMode_DedupesToolCallIDs is the
// duplicate-id case. Two tool_calls with the same id confuse the
// consumer because it cannot map tool_result back to the call.
// Fix mode renames the second (and any subsequent) one with a
// __dup<n> suffix.
func TestProcessNonStreamBody_FixMode_DedupesToolCallIDs(t *testing.T) {
	body := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[
					{"id":"abc","type":"function","function":{"name":"a","arguments":"{}"}},
					{"id":"abc","type":"function","function":{"name":"b","arguments":"{}"}}
				]
			},
			"finish_reason":"tool_calls"
		}]
	}`)
	out, res := ProcessNonStreamBody(body, QualityModeFix)
	if !res.BodyChanged {
		t.Fatal("fix mode must report BodyChanged=true")
	}
	hasDupFlag := false
	for _, f := range res.Flags {
		if f == QualityFlagDuplicateToolID {
			hasDupFlag = true
		}
	}
	if !hasDupFlag {
		t.Fatalf("expected duplicate_tool_call_id flag, got %v", res.Flags)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("rewritten body not valid JSON: %v", err)
	}
	if parsed.Choices[0].Message.ToolCalls[0].ID != "abc" {
		t.Fatalf("first id must be preserved, got %q", parsed.Choices[0].Message.ToolCalls[0].ID)
	}
	if parsed.Choices[0].Message.ToolCalls[1].ID != "abc__dup2" {
		t.Fatalf("second duplicate must be renamed, got %q", parsed.Choices[0].Message.ToolCalls[1].ID)
	}
}

// TestProcessNonStreamBody_NoToolCallsIsUnchanged makes sure the
// common case (a normal text response, no tool_calls) doesn't get
// accidentally rewritten. This is the regression test for "did
// the migration break 99% of requests".
func TestProcessNonStreamBody_NoToolCallsIsUnchanged(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	for _, mode := range []string{QualityModeOff, QualityModeDetectOnly, QualityModeFix} {
		out, res := ProcessNonStreamBody(body, mode)
		if string(out) != string(body) {
			t.Fatalf("mode=%s: body must be unchanged when no tool_calls, got diff", mode)
		}
		if len(res.Flags) > 0 {
			t.Fatalf("mode=%s: must not flag non-tool_call responses, got %v", mode, res.Flags)
		}
	}
}

// TestProcessStreamLine_RenamesEmptyNameInDelta verifies the
// streaming path applies the same rename rule per SSE chunk. The
// test feeds a single delta chunk with an empty function.name and
// expects the line to be rewritten to __unknown_tool_stream_<n>__
// in fix mode.
func TestProcessStreamLine_RenamesEmptyNameInDelta(t *testing.T) {
	original := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"","arguments":"{}"}}]}}]}` + "\n"
	out, flags, _ := ProcessStreamLine(original, QualityModeFix, nil, nil)
	if out == original {
		t.Fatal("fix mode must rewrite the line")
	}
	if !strings.Contains(out, `__unknown_tool_stream_0__`) {
		t.Fatalf("rewritten line must contain __unknown_tool_stream_0__, got %s", out)
	}
	if len(flags) == 0 || flags[0] != QualityFlagEmptyToolName {
		t.Fatalf("expected empty_tool_name flag, got %v", flags)
	}
}

// TestProcessStreamLine_OffModePassesThrough confirms the
// streaming path is also a no-op in off mode. Important because
// the per-line call sits on the hot path; the cost must stay at
// one string check + one map lookup.
func TestProcessStreamLine_OffModePassesThrough(t *testing.T) {
	line := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{}"}}]}}]}` + "\n"
	out, flags, _ := ProcessStreamLine(line, QualityModeOff, nil, nil)
	if out != line {
		t.Fatalf("off mode must be byte-identical; got diff")
	}
	if len(flags) > 0 {
		t.Fatalf("off mode must not flag; got %v", flags)
	}
}

// TestProcessStreamLine_DedupesAcrossChunks makes sure the seen
// IDs map is passed through correctly: chunk 1 introduces id=abc
// with a real name, chunk 2 with the same id is rewritten with
// __dup2 suffix. We use a non-empty name to isolate the dedup
// behaviour from the empty-name rename path.
func TestProcessStreamLine_DedupesAcrossChunks(t *testing.T) {
	seen := map[string]int{}
	line1 := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"abc","function":{"name":"a","arguments":"{}"}}]}}]}` + "\n"
	line2 := `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"abc","function":{"name":"b","arguments":"{}"}}]}}]}` + "\n"
	out1, _, _ := ProcessStreamLine(line1, QualityModeFix, nil, seen)
	out2, flags, _ := ProcessStreamLine(line2, QualityModeFix, nil, seen)
	// First call: id is not a duplicate (seenIDs starts empty),
	// name is not empty → line should pass through byte-identical.
	if out1 != line1 {
		t.Fatalf("first call (unique id, non-empty name) must not rewrite; got diff")
	}
	if out2 == line2 {
		t.Fatal("second call should rewrite the duplicate id")
	}
	if !strings.Contains(out2, `abc__dup2`) {
		t.Fatalf("second chunk must contain abc__dup2, got %s", out2)
	}
	hasDupFlag := false
	for _, f := range flags {
		if f == QualityFlagDuplicateToolID {
			hasDupFlag = true
		}
	}
	if !hasDupFlag {
		t.Fatalf("expected duplicate_tool_call_id flag after second chunk, got %v", flags)
	}
}

// TestProcessStreamLine_DoneAndKeepAlivePassthrough makes sure
// stream-level control lines (data: [DONE], ": keep-alive\n\n")
// are never touched, even in fix mode. Otherwise the consumer
// would never see the terminator and the stream would hang.
func TestProcessStreamLine_DoneAndKeepAlivePassthrough(t *testing.T) {
	for _, line := range []string{
		"data: [DONE]\n\n",
		": keep-alive\n\n",
		"",
		"event: ping\n\n",
	} {
		out, flags, _ := ProcessStreamLine(line, QualityModeFix, nil, nil)
		if out != line {
			t.Fatalf("control line %q must pass through verbatim in fix mode, got %q", line, out)
		}
		if len(flags) > 0 {
			t.Fatalf("control line %q must not flag anything, got %v", line, flags)
		}
	}
}

// TestComputeScore_NoFlags verifies the no-issue score is 1.0.
// This is the baseline that dashboards compare against.
func TestComputeScore_NoFlags(t *testing.T) {
	if got := computeScore(chatQualityReport{}); got != 1.0 {
		t.Fatalf("empty report must score 1.0, got %v", got)
	}
}

// TestComputeScore_AllEmpty verifies the all-empty case scores
// 0.0 so dashboards can pin "this provider's response was
// completely unusable" in one query.
func TestComputeScore_AllEmpty(t *testing.T) {
	rep := chatQualityReport{
		Flags:          []string{QualityFlagEmptyToolName, QualityFlagEmptyToolNameAll},
		AllEmptyName:   true,
		EmptyNameCount: 2,
	}
	if got := computeScore(rep); got != 0.0 {
		t.Fatalf("all-empty must score 0.0, got %v", got)
	}
}

// TestWrapQualityProcessNonStream verifies the indirection
// closure (used by cmd/gateway/main.go to bridge routing ↔ relay
// without an import cycle) returns the same (body, flags,
// actions, score) tuple that ProcessNonStreamBody produces.
//
// Body has a single empty-named tool_call, so AllEmptyName fires
// and the score is 0.0 (whole response was unusable before the
// fix). This is the same shape as the gpt-5.4 bug case.
func TestWrapQualityProcessNonStream(t *testing.T) {
	fn := WrapQualityProcessNonStream()
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"x","function":{"name":""}}]}}]}`)
	out, flags, actions, score := fn(body, QualityModeFix)
	if !strings.Contains(string(out), `__unknown_tool_0__`) {
		t.Fatalf("wrapped hook must rewrite, got %s", out)
	}
	if len(flags) == 0 || flags[0] != QualityFlagEmptyToolName {
		t.Fatalf("wrapped hook must flag empty name, got %v", flags)
	}
	if len(actions) == 0 {
		t.Fatalf("wrapped hook must carry fix actions, got empty")
	}
	if score == nil || *score != 0.0 {
		t.Fatalf("wrapped hook must carry score=0.0 (all-empty), got %v", score)
	}
}

// TestWrapQualityProcessStreamLine mirrors the non-stream wrapper
// test for the streaming hook. We feed a single chunk and verify
// the rewrite + flag pass-through.
func TestWrapQualityProcessStreamLine(t *testing.T) {
	fn := WrapQualityProcessStreamLine()
	line := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{}"}}]}}]}` + "\n"
	out, flags, _ := fn(line, QualityModeFix, nil, nil)
	if out == line {
		t.Fatalf("wrapped stream hook must rewrite, got %s", out)
	}
	if len(flags) == 0 || flags[0] != QualityFlagEmptyToolName {
		t.Fatalf("wrapped stream hook must flag empty name, got %v", flags)
	}
}

// TestAppendOrUnique guards the small dedup helper used by both
// the non-stream flag accumulator and the per-chunk flag
// accumulator. Cheap to maintain, easy to break.
func TestAppendOrUnique(t *testing.T) {
	got := appendOrUnique([]string{"a"}, "a")
	if len(got) != 1 {
		t.Fatalf("appendOrUnique must dedup; got %v", got)
	}
	got = appendOrUnique(nil, "a")
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("appendOrUnique must accept nil; got %v", got)
	}
	got = appendOrUnique([]string{"a"}, "b")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("appendOrUnique must append new values; got %v", got)
	}
}
