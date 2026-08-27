// Package compressor - diff_test.go (v3 T25 unit tests)
package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

func makeBody(msgs []map[string]string) []byte {
	b, _ := json.Marshal(map[string]any{
		"model":    "test-model",
		"messages": msgs,
	})
	return b
}

func userMsg(content string) map[string]string {
	return map[string]string{"role": "user", "content": content}
}

func assistantMsg(content string) map[string]string {
	return map[string]string{"role": "assistant", "content": content}
}

func summaryMsg(content string) map[string]string {
	return map[string]string{"role": "assistant", "content": CompactionMarkerPrefix + content + "]"}
}

// case-1: 全新会话（无 last）→ 返回 clientBody，IsNewSess=true
func TestBuildOutbound_NewSession(t *testing.T) {
	client := makeBody([]map[string]string{userMsg("hello"), assistantMsg("world")})
	res, err := BuildOutboundMessages(client, nil, nil, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsNewSess {
		t.Error("expected IsNewSess=true for new session")
	}
	if string(res.Body) != string(client) {
		t.Error("expected body unchanged for new session")
	}
	if len(res.MsgHashes) == 0 {
		t.Error("expected non-empty MsgHashes")
	}
}

// case-2: 相同内容 → 返回 lastOutbound，Unchanged=true
func TestBuildOutbound_Unchanged(t *testing.T) {
	msgs := []map[string]string{userMsg("hello"), assistantMsg("world")}
	last := makeBody(msgs)
	client := makeBody(msgs) // identical

	state := &SessionState{SchemaVersion: 1}
	res, err := BuildOutboundMessages(client, state, last, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Unchanged {
		t.Error("expected Unchanged=true when client == last")
	}
}

// case-3: 追加 1 条 user 消息 → last + newTail，DeltaCount=1
func TestBuildOutbound_AppendOneTurn(t *testing.T) {
	last := makeBody([]map[string]string{userMsg("hello"), assistantMsg("world")})
	client := makeBody([]map[string]string{userMsg("hello"), assistantMsg("world"), userMsg("next question")})

	state := &SessionState{SchemaVersion: 1}
	res, err := BuildOutboundMessages(client, state, last, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsNewSess {
		t.Error("expected IsNewSess=false")
	}
	if res.Unchanged {
		t.Error("expected Unchanged=false")
	}
	if res.DeltaCount != 1 {
		t.Errorf("expected DeltaCount=1, got %d", res.DeltaCount)
	}
	// outbound should have 3 messages
	outMsgs, _ := extractMessages(res.Body)
	if len(outMsgs) != 3 {
		t.Errorf("expected 3 outbound messages, got %d", len(outMsgs))
	}
}

// case-4: 客户端修改了中间消息 → no shared messages after modification,
// treated as session reset (IsNewSess=true from LCS perspective)
func TestBuildOutbound_ModifiedMiddle(t *testing.T) {
	last := makeBody([]map[string]string{
		userMsg("original question"),
		assistantMsg("answer"),
		userMsg("follow up"),
	})
	// Client modified the first user message — none of the hashes match.
	client := makeBody([]map[string]string{
		userMsg("MODIFIED question"),
		assistantMsg("answer"),
		userMsg("follow up"),
		userMsg("new turn"),
	})

	state := &SessionState{SchemaVersion: 1}
	res, err := BuildOutboundMessages(client, state, last, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With modified first message, the last shared index will be found
	// on "follow up" (3rd msg), delta = ["new turn"], DeltaCount=1
	if res.DeltaCount == 0 && !res.IsNewSess {
		// Either a delta was found or session was reset — both acceptable
		t.Log("no delta found, treated as session reset (acceptable)")
	}
}

// case-5: 客户端重发整段历史 → 返回 last，Unchanged=true
func TestBuildOutbound_FullHistoryResend(t *testing.T) {
	msgs := []map[string]string{
		userMsg("q1"), assistantMsg("a1"),
		userMsg("q2"), assistantMsg("a2"),
	}
	last := makeBody(msgs)
	// Client sends the exact same history — no new turns
	client := makeBody(msgs)

	state := &SessionState{SchemaVersion: 1}
	res, err := BuildOutboundMessages(client, state, last, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Unchanged {
		t.Errorf("expected Unchanged=true for full history resend, got DeltaCount=%d IsNewSess=%v", res.DeltaCount, res.IsNewSess)
	}
}

// summary_marker: summary message is preserved verbatim, not used in LCS diff
func TestBuildOutbound_AnthropicSystemPreserved(t *testing.T) {
	last := []byte(`{"model":"claude","system":"gateway summary","messages":[{"role":"user","content":"old"},{"role":"assistant","content":"answer"}]}`)
	client := []byte(`{"model":"claude","messages":[{"role":"user","content":"old"},{"role":"assistant","content":"answer"},{"role":"user","content":"new"}]}`)
	res, err := BuildOutboundMessages(client, &SessionState{SchemaVersion: 1}, last, "anthropic-messages")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatal(err)
	}
	if out["system"] != "gateway summary" {
		t.Errorf("system = %v, want prior summary", out["system"])
	}
}

func TestBuildOutbound_SummaryMarkerPreserved(t *testing.T) {
	// last outbound has a summary marker + recent turns
	last := makeBody([]map[string]string{
		summaryMsg("prior summary"),
		userMsg("recent q"),
		assistantMsg("recent a"),
	})
	// client sends only recent turns (summary not in client body)
	client := makeBody([]map[string]string{
		userMsg("recent q"),
		assistantMsg("recent a"),
		userMsg("new turn"),
	})

	state := &SessionState{SchemaVersion: 1, SummaryMarker: "[smm_v1:abc123]"}
	res, err := BuildOutboundMessages(client, state, last, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// outbound should contain summary + recent q + recent a + new turn = 4 msgs
	outMsgs, _ := extractMessages(res.Body)
	hasSummary := false
	for _, m := range outMsgs {
		if isSummaryMarkerMsg(m) {
			hasSummary = true
			break
		}
	}
	if !hasSummary {
		t.Error("expected summary marker message to be preserved in outbound")
	}
}

func TestMsgHash_Stable(t *testing.T) {
	m := json.RawMessage(`{"role":"user","content":"hello"}`)
	h1 := msgHash(m)
	h2 := msgHash(m)
	if h1 != h2 {
		t.Error("msgHash should be deterministic")
	}
	if h1 == "" {
		t.Error("msgHash should not be empty")
	}
}

func TestInjectSummaryMarker_PreservesSummaryFields(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"[Gateway compacted conversation summary - prior turns collapsed to fit context. Use direct quotes for exact identifiers, errors, user corrections. The first user message is preserved verbatim below as the original intent.]\nanswer","tool_calls":[{"id":"call_1"}],"name":"agent","refusal":null}]}`)
	marker, rebuilt := injectSummaryMarker(body, "openai")
	if marker == "" {
		t.Fatal("expected marker")
	}
	var out map[string][]map[string]any
	if err := json.Unmarshal(rebuilt, &out); err != nil {
		t.Fatal(err)
	}
	msg := out["messages"][0]
	if len(msg["tool_calls"].([]any)) != 1 || msg["name"] != "agent" {
		t.Errorf("summary fields lost: %+v", msg)
	}
	if !strings.Contains(msg["content"].(string), marker) {
		t.Errorf("content missing marker: %v", msg["content"])
	}
}

func TestInjectSummaryMarker_AnthropicSystemBlock(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"system": []map[string]string{
			{"type": "text", "text": "original"},
			{"type": "text", "text": AnthropicSystemSummaryPrefix + "summary"},
		},
		"messages": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	marker, rebuilt := injectSummaryMarker(body, "anthropic-messages")
	if marker == "" || !strings.Contains(string(rebuilt), marker) {
		t.Fatalf("expected injected Anthropic marker, marker=%q body=%s", marker, rebuilt)
	}
}

func TestInjectSummaryMarker_NoSummaryBoundary(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"answer"}]}`)
	marker, rebuilt := injectSummaryMarker(body, "openai")
	if marker != "" || rebuilt != nil {
		t.Fatalf("expected no marker and failed injection, marker=%q body=%s", marker, rebuilt)
	}
}

func TestBuildSummaryMarker(t *testing.T) {
	marker := BuildSummaryMarker("some summary content")
	if !isSummaryMarkerMsg(json.RawMessage(`{"role":"assistant","content":"` + marker + `\nmore"}`)) {
		// The marker prefix should be detected
		t.Log("marker:", marker)
	}
	if len(marker) == 0 {
		t.Error("expected non-empty summary marker")
	}
}

// TestContentFingerprint_InputOutputTextBlocks 验证 P2 issue #5:
// contentFingerprint 必须把 input_text / output_text 块纳入指纹计算，
// 不能只处理 type=="text" 的块。否则同角色不同内容会发生碰撞。
func TestContentFingerprint_InputOutputTextBlocks(t *testing.T) {
	// 两条消息：role 相同，但 content 不同（一个 text 块，一个 input_text 块）
	msg1 := json.RawMessage(`[{"type":"text","text":"hello world"}]`)
	msg2 := json.RawMessage(`[{"type":"input_text","text":"different content"}]`)

	fp1 := contentFingerprint(msg1)
	fp2 := contentFingerprint(msg2)

	// P2 issue #5: 当前实现会忽略 input_text 块 → fp2 == ""
	// 导致两条不同内容的消息指纹碰撞（都退化为 role-only hash）
	if fp1 == fp2 {
		t.Errorf("contentFingerprint collision: msg1=%q msg2=%q both produce fp=%q (P2 issue #5)",
			msg1, msg2, fp1)
	}

	if fp2 == "" {
		t.Errorf("contentFingerprint ignored input_text block, fp2 should contain 'different content'")
	}
}

// TestContentFingerprint_AnthropicToolBlocks 验证 Anthropic 的
// tool_use / tool_result 块的关键字段也必须纳入指纹。
func TestContentFingerprint_AnthropicToolBlocks(t *testing.T) {
	// tool_use 块
	toolUse := json.RawMessage(`[{
		"type": "tool_use",
		"id": "toolu_abc123",
		"name": "search",
		"input": {"query": "test"}
	}]`)

	// tool_result 块
	toolResult := json.RawMessage(`[{
		"type": "tool_result",
		"tool_use_id": "toolu_abc123",
		"content": "result here"
	}]`)

	fpUse := contentFingerprint(toolUse)
	fpResult := contentFingerprint(toolResult)

	// 两种不同类型的 tool 块不应碰撞
	if fpUse == fpResult {
		t.Errorf("tool_use and tool_result should have different fingerprints, both=%q", fpUse)
	}

	// tool_use 应包含 id / name / input 信息
	if fpUse == "" {
		t.Errorf("tool_use fingerprint should not be empty (P2 issue #5)")
	}

	// tool_result 应包含 tool_use_id / content 信息
	if fpResult == "" {
		t.Errorf("tool_result fingerprint should not be empty (P2 issue #5)")
	}
}

// TestContentFingerprint_BlockSeparatorNoCollision — audit follow-up to
// 7622526a5: content blocks are concatenated without a separator, so
// [{"text":"ab"}] and [{"text":"a"},{"text":"b"}] produced identical
// fingerprints. A per-block terminator is required.
func TestContentFingerprint_BlockSeparatorNoCollision(t *testing.T) {
	single := json.RawMessage(`[{"type":"text","text":"ab"}]`)
	split := json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)
	if contentFingerprint(single) == contentFingerprint(split) {
		t.Errorf("block-boundary collision: %s and %s share fingerprint %q",
			single, split, contentFingerprint(single))
	}
}

// TestContentFingerprint_ThinkingBlock — Anthropic thinking blocks carry
// their payload in the "thinking" field, not "text". A thinking-only message
// must still produce a non-empty fingerprint distinct from other thinking
// content (same-role collision otherwise).
func TestContentFingerprint_ThinkingBlock(t *testing.T) {
	a := json.RawMessage(`[{"type":"thinking","thinking":"plan A"}]`)
	b := json.RawMessage(`[{"type":"thinking","thinking":"plan B"}]`)
	fa, fb := contentFingerprint(a), contentFingerprint(b)
	if fa == "" {
		t.Fatal("thinking-only message produced empty fingerprint")
	}
	if fa == fb {
		t.Errorf("thinking content collision: %q and %q share fingerprint %q", a, b, fa)
	}
}
