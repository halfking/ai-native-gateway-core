package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestRebuildOpenAIBodyAfterSummary(t *testing.T) {
	body := []byte(`{
		"model":"minimax-m3",
		"messages":[
			{"role":"system","content":"You are helpful"},
			{"role":"user","content":"turn1"},
			{"role":"assistant","content":"reply1"},
			{"role":"user","content":"turn2"},
			{"role":"assistant","content":"reply2"},
			{"role":"user","content":"latest"}
		]
	}`)
	got, err := rebuildOpenAIBodyAfterSummary(body, "prior work summary", 2)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Model != "minimax-m3" {
		t.Fatalf("model = %q", parsed.Model)
	}
	if len(parsed.Messages) != 6 { // system + summary + 4 tail (2 pairs)
		t.Fatalf("messages len = %d, want 6", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "system" {
		t.Fatalf("first role = %q", parsed.Messages[0].Role)
	}
	if !strings.Contains(parsed.Messages[1].Content, "prior work summary") {
		t.Fatalf("summary msg = %q", parsed.Messages[1].Content)
	}
	if parsed.Messages[len(parsed.Messages)-1].Content != "latest" {
		t.Fatalf("last msg = %q", parsed.Messages[len(parsed.Messages)-1].Content)
	}
}

func TestRebuildAnthropicBodyAfterSummary(t *testing.T) {
	body := []byte(`{
		"model":"minimax-m3",
		"max_tokens":1024,
		"system":"sys prompt",
		"messages":[
			{"role":"user","content":"a"},
			{"role":"assistant","content":"b"},
			{"role":"user","content":"c"},
			{"role":"assistant","content":"d"},
			{"role":"user","content":"latest"}
		]
	}`)
	got, err := rebuildAnthropicBodyAfterSummary(body, "anthropic summary", 2)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(got, &generic); err != nil {
		t.Fatal(err)
	}
	if string(generic["system"]) != `"sys prompt"` {
		t.Fatalf("system = %s", generic["system"])
	}
	var msgs []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(generic["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 { // summary + 4 tail
		t.Fatalf("messages len = %d, want 5", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "anthropic summary") {
		t.Fatalf("summary = %q", msgs[0].Content)
	}
	if msgs[len(msgs)-1].Content != "latest" {
		t.Fatalf("last = %q", msgs[len(msgs)-1].Content)
	}
}

func TestExtractOpenAIConversationText(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"hello"},
		{"role":"assistant","content":[{"type":"text","text":"world"}]}
	]}`)
	text, err := extractOpenAIConversationText(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "[user]") || !strings.Contains(text, "hello") {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(text, "world") {
		t.Fatalf("text = %q", text)
	}
}

func TestMessageRoleAndSummary_ToolCalls(t *testing.T) {
	raw := json.RawMessage(`{
		"role":"assistant",
		"content":"checking weather",
		"tool_calls":[{"id":"tc1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]
	}`)
	role, text := messageRoleAndSummary(raw)
	if role != "assistant" {
		t.Fatalf("role = %q", role)
	}
	if !strings.Contains(text, "checking weather") || !strings.Contains(text, "tool_call(get_weather)") {
		t.Fatalf("text = %q", text)
	}
}

func TestTailMessagesToolAware_PreservesToolRound(t *testing.T) {
	messages := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"old"}`),
		json.RawMessage(`{"role":"assistant","content":"old reply"}`),
		json.RawMessage(`{"role":"user","content":"go"}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"read","input":{}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"file contents"}]}`),
		json.RawMessage(`{"role":"assistant","content":"done"}`),
	}
	tail := tailMessagesToolAware(messages, 1)
	if len(tail) != 4 {
		t.Fatalf("tail len = %d, want 4 (trigger user + full tool round)", len(tail))
	}
	// Must include the user turn that triggered tool_use, then tool_use/tool_result/final reply.
	if !strings.Contains(string(tail[0]), `"content":"go"`) {
		t.Fatalf("tail[0] should be triggering user, got %s", tail[0])
	}
	if !strings.Contains(string(tail[1]), "tool_use") {
		t.Fatalf("tail[1] should be tool_use assistant, got %s", tail[1])
	}
	if !strings.Contains(string(tail[2]), "tool_result") {
		t.Fatalf("tail[2] should be tool_result user, got %s", tail[2])
	}
}

func TestHandleContextLengthRecovery_TwoPhase(t *testing.T) {
	e := &Executor{}
	params := &ExecParams{R: httptestNewRequest()}
	window := 50
	cand := provider.Candidate{ContextWindow: &window}
	big := strings.Repeat("x", 400)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + big + `"},{"role":"assistant","content":"` + big + `"},{"role":"user","content":"` + big + `"}]}`)

	st := contextLengthRecoveryState{}
	source := append([]byte(nil), body...)
	action := e.handleContextLengthRecovery(context.Background(), params, cand, &source, &st, 400)
	if action != ctxLenRetry {
		t.Fatalf("phase1 = %v, want retry", action)
	}
	if !st.mechanicalAttempted || st.llmAttempted {
		t.Fatalf("mechanical=%v llm=%v", st.mechanicalAttempted, st.llmAttempted)
	}

	st2 := contextLengthRecoveryState{mechanicalAttempted: true}
	source2 := append([]byte(nil), body...)
	action2 := e.handleContextLengthRecovery(context.Background(), params, cand, &source2, &st2, 400)
	if action2 != ctxLenGiveUp {
		t.Fatalf("phase2 = %v, want give up", action2)
	}
	if !st2.llmAttempted {
		t.Fatal("llm should be attempted in phase2")
	}
}

func httptestNewRequest() *http.Request {
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	return req
}

func TestCompactionModelsFromEnv(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", " glm-5.1 , gemini-2.5-flash ")
	got := compactionModelsFromEnv()
	if len(got) != 2 || got[0] != "glm-5.1" || got[1] != "gemini-2.5-flash" {
		t.Fatalf("got %v", got)
	}
}

func TestClassifyContextLengthFromStatus(t *testing.T) {
	if !classifyContextLengthFromStatus(400, []byte(`{"error":{"type":"context_length_exceeded"}}`)) {
		t.Fatal("expected context length")
	}
}
