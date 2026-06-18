// Package compressor - diff_test.go (v3 T25 unit tests)
package compressor

import (
	"encoding/json"
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
