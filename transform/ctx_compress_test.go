package transform

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompressMessagesIfNeeded_NoOpWhenFits(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"hi"}
	]}`)
	out := CompressMessagesIfNeeded(body, 100000)
	if string(out) != string(body) {
		t.Fatalf("expected no trim; got %s", out)
	}
}

func TestCompressMessagesIfNeeded_TrimsWhenOver(t *testing.T) {
	// Build a body whose messages add up to far more than 50 tokens.
	// With 50 token context window, the soft limit is ~42 tokens. The
	// body is ~ 1 + (1 + role+content overhead) * 11 ≈ 200+ chars;
	// 200/3.5 ≈ 57 tokens > 42 → must trim.
	long := strings.Repeat("a", 200)
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"}
	]}`)
	out := CompressMessagesIfNeeded(body, 50)

	var before, after struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(body, &before)
	_ = json.Unmarshal(out, &after)
	if len(after.Messages) >= len(before.Messages) {
		t.Fatalf("expected messages to shrink; before=%d after=%d", len(before.Messages), len(after.Messages))
	}
	// system message must be preserved
	if !isSystemMessage(after.Messages[0]) {
		t.Fatalf("expected first message to be system; got %s", after.Messages[0])
	}
}

func TestCompressMessagesIfNeeded_NoContextWindow(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 10000) + `"}]}`)
	out := CompressMessagesIfNeeded(body, 0)
	if string(out) != string(body) {
		t.Fatalf("expected no trim when context_window=0")
	}
}

func TestCompressMessagesIfNeeded_NotJSON(t *testing.T) {
	body := []byte(`not json at all`)
	out := CompressMessagesIfNeeded(body, 100000)
	if string(out) != string(body) {
		t.Fatalf("expected non-JSON body to be untouched")
	}
}

func TestCompressAnthropicMessagesIfNeeded_TrimsWhenOver(t *testing.T) {
	long := strings.Repeat("b", 200)
	body := []byte(`{"model":"MiniMax-M3","max_tokens":1024,"system":"You are helpful.","messages":[
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"latest question"}
	]}`)
	out := CompressAnthropicMessagesIfNeeded(body, 50)

	var before, after struct {
		Messages []json.RawMessage `json:"messages"`
		System   string            `json:"system"`
	}
	_ = json.Unmarshal(body, &before)
	_ = json.Unmarshal(out, &after)
	if len(after.Messages) >= len(before.Messages) {
		t.Fatalf("expected anthropic messages to shrink; before=%d after=%d", len(before.Messages), len(after.Messages))
	}
	if after.System != before.System {
		t.Fatalf("system field must be preserved")
	}
}

func TestCompressMessagesIfNeeded_PreservesOtherFields(t *testing.T) {
	long := strings.Repeat("a", 200)
	body := []byte(`{"model":"m","temperature":0.7,"stream":true,"tools":[{"type":"function","function":{"name":"x"}}],"messages":[
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"}
	]}`)
	out := CompressMessagesIfNeeded(body, 50)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if _, ok := got["temperature"]; !ok {
		t.Fatalf("temperature lost")
	}
	if _, ok := got["stream"]; !ok {
		t.Fatalf("stream lost")
	}
	if _, ok := got["tools"]; !ok {
		t.Fatalf("tools lost")
	}
}

// collectToolPairing scans an OpenAI Chat messages slice and returns the set
// of tool_call ids declared by assistants and the set of tool_call_ids
// referenced by tool-result messages. Every result id MUST appear in the call
// set, otherwise the upstream (MiniMax/Anthropic) rejects the request with
// "tool result's tool id(...) not found (2013)".
func collectToolPairing(t *testing.T, messages []json.RawMessage) (callIDs, resultIDs map[string]bool) {
	t.Helper()
	callIDs = map[string]bool{}
	resultIDs = map[string]bool{}
	for _, m := range messages {
		var probe struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		}
		if err := json.Unmarshal(m, &probe); err != nil {
			continue
		}
		if probe.ToolCallID != "" {
			resultIDs[probe.ToolCallID] = true
		}
		for _, c := range probe.ToolCalls {
			if c.ID != "" {
				callIDs[c.ID] = true
			}
		}
	}
	return callIDs, resultIDs
}

// assertNoOrphanedToolResults fails the test if any tool_result references a
// tool_call id that was dropped by the trim (i.e. not present among surviving
// assistant tool_calls). This is the exact invariant MiniMax error 2013
// enforces server-side.
func assertNoOrphanedToolResults(t *testing.T, messages []json.RawMessage) {
	t.Helper()
	callIDs, resultIDs := collectToolPairing(t, messages)
	for rid := range resultIDs {
		if !callIDs[rid] {
			t.Fatalf("ORPHANED tool_result id=%s has no matching tool_call after trim "+
				"(minimax error 2013: tool result's tool id not found)", rid)
		}
	}
}

// TestCompressMessagesIfNeeded_DoesNotOrphanToolResult reproduces the
// production bug: glm-5.2 -> minimax-m3 Q3 conversion path. A multi-turn
// OpenAI conversation with a tool round is trimmed by context-window, and
// the naive pair-drop removes the assistant tool_calls while keeping the
// matching tool(result), orphaning tool_use_id and triggering MiniMax 2013.
func TestCompressMessagesIfNeeded_DoesNotOrphanToolResult(t *testing.T) {
	big := strings.Repeat("x", 300)
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"s"},
		{"role":"user","content":"` + big + `"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_019ec667b6a27e51b5c03544","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_019ec667b6a27e51b5c03544","content":"r"},
		{"role":"user","content":"q"},
		{"role":"assistant","content":"a"}
	]}`)
	// cw in [60..200] previously produced the orphan (verified by sweep).
	for _, cw := range []int{60, 100, 150, 200} {
		out := CompressMessagesIfNeeded(body, cw)
		var req struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(out, &req); err != nil {
			t.Fatalf("cw=%d: output not JSON: %v", cw, err)
		}
		assertNoOrphanedToolResults(t, req.Messages)
	}
}

// TestCompressAnthropicMessagesIfNeeded_DoesNotOrphanToolResult is the Q4
// (anthropic passthrough) variant: an assistant tool_use block dropped while
// its tool_result survives orphaning the tool_use_id.
func TestCompressAnthropicMessagesIfNeeded_DoesNotOrphanToolResult(t *testing.T) {
	big := strings.Repeat("x", 300)
	body := []byte(`{"model":"MiniMax-M3","messages":[
		{"role":"user","content":"` + big + `"},
		{"role":"assistant","content":[{"type":"tool_use","id":"call_019ec667b6a27e51b5c03544","name":"f","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_019ec667b6a27e51b5c03544","content":"r"}]},
		{"role":"user","content":"q"},
		{"role":"assistant","content":"a"}
	]}`)
	for _, cw := range []int{60, 100, 150, 200} {
		out := CompressAnthropicMessagesIfNeeded(body, cw)
		var req struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(out, &req); err != nil {
			t.Fatalf("cw=%d: output not JSON: %v", cw, err)
		}
		// Collect surviving tool_use ids and tool_result tool_use_ids.
		useIDs := map[string]bool{}
		resultIDs := map[string]bool{}
		for _, m := range req.Messages {
			var probe struct {
				Content []struct {
					Type      string `json:"type"`
					ToolUseID string `json:"tool_use_id"`
					ID        string `json:"id"`
				} `json:"content"`
			}
			if err := json.Unmarshal(m, &probe); err != nil {
				continue
			}
			for _, b := range probe.Content {
				switch b.Type {
				case "tool_use":
					if b.ID != "" {
						useIDs[b.ID] = true
					}
				case "tool_result":
					if b.ToolUseID != "" {
						resultIDs[b.ToolUseID] = true
					}
				}
			}
		}
		for rid := range resultIDs {
			if !useIDs[rid] {
				t.Fatalf("cw=%d: ORPHANED anthropic tool_result tool_use_id=%s has no matching tool_use", cw, rid)
			}
		}
	}
}

// TestCompressMessagesIfNeeded_DropsWholeToolRoundTogether verifies that when
// a tool round is trimmed, BOTH the assistant tool_calls and its tool(result)
// are removed together (no half-drop), and the message count actually shrinks.
func TestCompressMessagesIfNeeded_DropsWholeToolRoundTogether(t *testing.T) {
	big := strings.Repeat("x", 300)
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"s"},
		{"role":"user","content":"` + big + `"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_A","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_A","content":"r"},
		{"role":"user","content":"q"},
		{"role":"assistant","content":"a"}
	]}`)
	out := CompressMessagesIfNeeded(body, 100)
	var before, after struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(body, &before)
	_ = json.Unmarshal(out, &after)
	if len(after.Messages) >= len(before.Messages) {
		t.Fatalf("expected shrink; before=%d after=%d", len(before.Messages), len(after.Messages))
	}
	// No surviving message may mention the dropped tool round at all.
	for _, m := range after.Messages {
		s := string(m)
		if strings.Contains(s, "call_A") {
			t.Fatalf("tool round was half-dropped; surviving message still references call_A: %s", s)
		}
	}
}

// TestCompressMessagesIfNeeded_PreservesRecentToolRound verifies the most
// recent tool round (which the model must answer) is kept intact while older
// ordinary pairs are trimmed.
func TestCompressMessagesIfNeeded_PreservesRecentToolRound(t *testing.T) {
	big := strings.Repeat("x", 300)
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"s"},
		{"role":"user","content":"` + big + `"},
		{"role":"assistant","content":"` + big + `"},
		{"role":"user","content":"` + big + `"},
		{"role":"assistant","content":"` + big + `"},
		{"role":"user","content":"` + big + `"},
		{"role":"assistant","content":"` + big + `"},
		{"role":"user","content":"what is the weather"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_RECENT","type":"function","function":{"name":"weather","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_RECENT","content":"sunny"},
		{"role":"user","content":"thanks"}
	]}`)
	out := CompressMessagesIfNeeded(body, 120)
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(out, &req)
	assertNoOrphanedToolResults(t, req.Messages)
	// The recent tool round must still be present.
	found := false
	for _, m := range req.Messages {
		if strings.Contains(string(m), "call_RECENT") {
			found = true
		}
	}
	if !found {
		t.Fatalf("most-recent tool round (call_RECENT) was dropped; should be preserved")
	}
}

// Round 47 compression v7 T3: public token/byte estimators for the
// mode=1 auto_threshold path (compressor/ package).

func TestEstimateTokens_CoarseChars(t *testing.T) {
	// 3.5 chars/token rule. 350 chars → 100 tokens (truncation toward zero).
	body := []byte(strings.Repeat("a", 350))
	got := EstimateTokens(body)
	if got != 100 {
		t.Fatalf("EstimateTokens(350 chars): want 100, got %d", got)
	}
}

func TestEstimateTokens_EmptyBody(t *testing.T) {
	if got := EstimateTokens(nil); got != 0 {
		t.Fatalf("EstimateTokens(nil): want 0, got %d", got)
	}
	if got := EstimateTokens([]byte{}); got != 0 {
		t.Fatalf("EstimateTokens(empty): want 0, got %d", got)
	}
}

func TestThresholdBytes_DynamicByContextWindow(t *testing.T) {
	// v7 §2: threshold = contextWindow × fraction × charsPerToken.
	// fraction=0.8 is LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION default.
	cases := []struct {
		window   int
		frac     float64
		wantByte int
	}{
		// 64K models (small/cheap) → 179.2K byte trigger
		{64000, 0.8, 179200},
		// 128K models (default Cursor/RooCode profile) → 358.4K
		{128000, 0.8, 358400},
		// 200K models (mid-range) → 560K
		{200000, 0.8, 560000},
		// 256K models (Claude family top tier) → 716.8K
		{256000, 0.8, 716800},
		// fraction=0.85 matches in-place soft-limit trim default
		{128000, 0.85, 380800},
		// Zero window → 0 (caller falls through to 4xx path)
		{0, 0.8, 0},
		// Negative window → 0
		{-1, 0.8, 0},
	}
	for _, tc := range cases {
		got := ThresholdBytes(tc.window, tc.frac)
		if got != tc.wantByte {
			t.Errorf("ThresholdBytes(window=%d, frac=%.2f): want %d, got %d",
				tc.window, tc.frac, tc.wantByte, got)
		}
	}
}

func TestThresholdBytes_ZeroFractionFallsBackToDefault(t *testing.T) {
	// fraction ≤ 0 → use defaultSoftLimitFraction (0.85).
	got := ThresholdBytes(100000, 0)
	want := ThresholdBytes(100000, defaultSoftLimitFraction)
	if got != want {
		t.Fatalf("ThresholdBytes(window=100000, frac=0) should fall back to default: want %d, got %d",
			want, got)
	}
}

func TestNeedsCompression_GateLogic(t *testing.T) {
	// 128K window × 0.8 → 358400 byte threshold.
	thr := ThresholdBytes(128000, 0.8)
	if thr != 358400 {
		t.Fatalf("setup: want threshold=358400, got %d", thr)
	}
	cases := []struct {
		name string
		body []byte
		want bool
	}{
		{"empty", []byte{}, false},
		{"under_threshold", make([]byte, 100000), false},
		{"exactly_threshold", make([]byte, 358400), false}, // strictly greater
		{"one_over", make([]byte, 358401), true},
		{"well_over", make([]byte, 500000), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NeedsCompression(tc.body, 128000, 0.8)
			if got != tc.want {
				t.Errorf("NeedsCompression(len=%d, window=128000, frac=0.8): want %v, got %v",
					len(tc.body), tc.want, got)
			}
		})
	}
}

func TestNeedsCompression_UnknownWindowReturnsFalse(t *testing.T) {
	// Unknown context window (ContextWindow=0) must NOT trigger compression.
	// The downstream compressor falls through to the 4xx recovery path
	// instead — see v7 §3.4 and executor_chat.go comment on pre-request trim.
	body := make([]byte, 1000000) // huge body
	if NeedsCompression(body, 0, 0.8) {
		t.Fatal("NeedsCompression with window=0 must return false")
	}
	if NeedsCompression(body, -1, 0.8) {
		t.Fatal("NeedsCompression with negative window must return false")
	}
}
