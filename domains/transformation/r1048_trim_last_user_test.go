// Package transformation — r1048 trim invariant tests (2026-11-13).
//
// Incident shape (211 gw_23a0614a turn 1030, Zhipu 400 code 1214): a hydrated
// client session carried 853 system messages, two surviving tool rounds and
// exactly three user messages at the head of the non-system section. The
// mechanical trim's pair-drop + user-ack swallow consumed all three users in
// one iteration; the outbound body shipped with zero user messages and Zhipu
// rejected it ("messages 参数非法。请检查文档。"). The gateway's entry layer
// enforces "at least one user message" (HasUserMessage) — the trim must not
// break the contract the gateway itself demands of clients.
//
// These tests pin the invariant synthetically (the captured production bodies
// contain private conversation data and stay out of the repo).
package transformation

import (
	"encoding/json"
	"strings"
	"testing"
)

func r1048Sys(i int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"role":    "system",
		"content": "<total_tokens>14995481 tokens left</total_tokens> The date is 2026-11-13. filler " + strings.Repeat("s", 300) + " " + jsonItoa(i),
	})
	return b
}

func jsonItoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func r1048User(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"role": "user", "content": text})
	return b
}

func r1048AssistantToolCall(id string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"role": "assistant",
		"tool_calls": []any{map[string]any{
			"id": id, "type": "function",
			"function": map[string]any{"name": "ls", "arguments": `{"path":"/tmp"}`},
		}},
	})
	return b
}

func r1048ToolResult(id, text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"role": "tool", "tool_call_id": id, "content": text})
	return b
}

// r1048IncidentBody mirrors the incident body: heavy system block (weight that
// keeps the trim's message-only budget over the soft limit), three users at
// the head of the non-system section, two completed tool rounds after them.
// toolsPad pushes the full body over the 80% window trigger.
func r1048IncidentBody(t *testing.T, sysCount int) []byte {
	t.Helper()
	msgs := make([]json.RawMessage, 0, sysCount+12)
	for i := 0; i < sysCount; i++ {
		msgs = append(msgs, r1048Sys(i))
	}
	msgs = append(msgs,
		r1048User("<system-reminder> Codebase and user instructions are shown below."+strings.Repeat("a", 2000)),
		r1048User("[Earlier history reduced; original archive SHA256 abc123]"+strings.Repeat("b", 2000)),
		r1048User("请继续"),
		r1048AssistantToolCall("call_1"),
		r1048ToolResult("call_1", strings.Repeat("r", 2000)),
		r1048AssistantToolCall("call_2"),
		r1048ToolResult("call_2", strings.Repeat("r", 2000)),
		r1048AssistantToolCall("call_3"),
		r1048ToolResult("call_3", strings.Repeat("r", 2000)),
	)
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "pad",
			"description": strings.Repeat("t", 4000),
			"parameters":  map[string]any{"type": "object"},
		},
	}}
	body, err := json.Marshal(map[string]any{"model": "glm-5.3-flash", "messages": msgs, "tools": tools})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func r1048Roles(t *testing.T, body []byte) []string {
	t.Helper()
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		out = append(out, messageRole(m))
	}
	return out
}

// T1 (end-to-end incident replica): the default-window trim must leave at
// least one user message in the body it returns.
func TestR1048TrimPreservesLastUserIncidentShape(t *testing.T) {
	body := r1048IncidentBody(t, 900) // ~ system weight alone exceeds 76.8K est tokens
	out := CompressMessagesIfNeeded(body, 128000)
	if len(out) == len(body) {
		t.Fatalf("trim did not run — test premise broken (body %d bytes)", len(body))
	}
	roles := r1048Roles(t, out)
	hasUser := false
	for _, r := range roles {
		if r == "user" {
			hasUser = true
			break
		}
	}
	if !hasUser {
		t.Fatalf("trim produced a user-less body (roles head=%v tail=%v) — zhipu 1214 shape",
			roles[:min(3, len(roles))], roles[len(roles)-3:])
	}
	// The restored user must sit after the (preserved) system messages, i.e.
	// it must not be spliced ahead of the leading system block.
	if roles[0] != "system" {
		t.Fatalf("restored user broke the leading-system layout: first role=%q", roles[0])
	}
}

// T2 (primitive): trimOldestPairs restores the newest dropped user when the
// budget consumes every user message.
func TestR1048TrimOldestPairsRestoresNewestUser(t *testing.T) {
	sys := make([]json.RawMessage, 200)
	for i := range sys {
		sys[i] = r1048Sys(i)
	}
	msgs := append(append([]json.RawMessage{}, sys...),
		r1048User("oldest user"),
		r1048User("middle user"),
		r1048User("请继续"),
		r1048AssistantToolCall("c1"),
		r1048ToolResult("c1", "result"),
	)
	// budget below the system block alone ⇒ the loop drops the whole
	// non-system head in its first iterations.
	trimmed := trimOldestPairs(msgs, 1)
	roles := r1048Roles(t, mustMarshalMsgs(t, trimmed))
	found := ""
	for i, r := range roles {
		if r == "user" {
			found = r1048ContentAt(t, trimmed[i])
			break
		}
	}
	if found == "" {
		t.Fatalf("no user survived: %v", roles)
	}
	if found != "请继续" {
		t.Fatalf("restored user = %q, want the newest user turn 请继续", found)
	}
}

// T3 (anthropic edge): a user turn that only carries tool_result blocks must
// not be chosen for restoration — restoring it would orphan the tool results
// (their tool_use anchor was dropped with the round).
func TestR1048RestoreSkipsToolResultBearers(t *testing.T) {
	toolResultUser, _ := json.Marshal(map[string]any{
		"role": "user",
		"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "tu_1", "content": "r",
		}},
	})
	msgs := []json.RawMessage{
		r1048Sys(0),
		json.RawMessage(toolResultUser),
		r1048AssistantToolCall("c1"),
		r1048ToolResult("c1", "result"),
	}
	trimmed := trimOldestPairs(msgs, 1)
	roles := r1048Roles(t, mustMarshalMsgs(t, trimmed))
	for _, r := range roles {
		if r == "user" {
			t.Fatalf("tool_result-bearing user was restored — would orphan tool results: %v", roles)
		}
	}
}

// T4 (aggressive continuation primitive): dropOldestMessageUnit also must not
// ship a user-less array.
func TestR1048DropOldestMessageUnitPreservesUser(t *testing.T) {
	msgs := []json.RawMessage{
		r1048User("the only user"),
		r1048AssistantToolCall("c1"),
		r1048ToolResult("c1", "result"),
		r1048AssistantToolCall("c2"),
		r1048ToolResult("c2", "result 2"),
	}
	next := dropOldestMessageUnit(msgs)
	roles := r1048Roles(t, mustMarshalMsgs(t, next))
	hasUser := false
	for _, r := range roles {
		if r == "user" {
			hasUser = true
		}
	}
	if !hasUser {
		t.Fatalf("dropOldestMessageUnit removed the last user: %v", roles)
	}
}

// T5: bodies that never had a user message are returned unchanged by the
// guard (nothing to restore; not the trim's job to invent context).
func TestR1048GuardNoopWithoutUsers(t *testing.T) {
	original := []json.RawMessage{r1048Sys(0), r1048AssistantToolCall("c1"), r1048ToolResult("c1", "r")}
	trimmed := []json.RawMessage{r1048Sys(0), r1048ToolResult("c1", "r")}
	got := ensureUserSurvived(original, trimmed)
	if len(got) != len(trimmed) {
		t.Fatalf("guard invented a user message for a user-less original: %d -> %d", len(trimmed), len(got))
	}
}

// T6: when the trimmed array still has a user message the guard is a no-op.
func TestR1048GuardNoopWhenUserSurvived(t *testing.T) {
	original := []json.RawMessage{r1048User("u1"), r1048User("u2"), r1048AssistantToolCall("c1")}
	trimmed := []json.RawMessage{r1048User("u2"), r1048AssistantToolCall("c1")}
	got := ensureUserSurvived(original, trimmed)
	if len(got) != len(trimmed) {
		t.Fatalf("guard modified an array that already has a user: %d -> %d", len(trimmed), len(got))
	}
}

func mustMarshalMsgs(t *testing.T, msgs []json.RawMessage) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func r1048ContentAt(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var probe struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.Content
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
