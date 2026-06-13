package routing

import (
	"context"
	"encoding/json"
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

func TestApplyMechanicalThenLLMCompaction_MechanicalOnly(t *testing.T) {
	e := &Executor{}
	big := strings.Repeat("x", 500)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + big + `"}]}`)
	mechanical := func(b []byte) []byte {
		return []byte(`{"model":"m","messages":[{"role":"user","content":"small"}]}`)
	}
	out, ok := e.applyMechanicalThenLLMCompaction(context.Background(), nil, provider.Candidate{}, body, mechanical)
	if !ok {
		t.Fatal("expected mechanical compaction")
	}
	if string(out) != `{"model":"m","messages":[{"role":"user","content":"small"}]}` {
		t.Fatalf("out = %s", out)
	}
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
