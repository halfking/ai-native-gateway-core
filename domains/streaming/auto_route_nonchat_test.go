package streaming

// auto_route_nonchat_test.go — tests for model=auto on /v1/messages and
// /v1/responses (signal extraction + flag gating).

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// TestExtractSignalsForMessages verifies Anthropic body signal extraction.
func TestExtractSignalsForMessages(t *testing.T) {
	// body contains a fenced code block (triple backtick) to test HasCodeBlock.
	body := []byte(`{"model":"auto","system":"You are a helpful assistant","messages":[{"role":"user","content":"def foo()"}],"max_tokens":100}`)
	body = append(body, []byte("```")...) // inject code fence without raw-string clash
	rb := &messagesRequestBody{
		Model:    "auto",
		System:   "You are a helpful assistant",
		Messages: []byte(`[{"role":"user","content":"def foo()"}]`),
	}
	sigs := extractSignalsForMessages(rb, body)
	if sigs.SystemPrompt != "You are a helpful assistant" {
		t.Fatalf("system: got %q", sigs.SystemPrompt)
	}
	if sigs.LastUserPrompt == "" {
		t.Fatal("last user prompt should be non-empty")
	}
	if !sigs.HasCodeBlock {
		t.Fatal("HasCodeBlock should be true (body contains code fence)")
	}
	if sigs.EstimatedTokens <= 0 {
		t.Fatalf("tokens: got %d", sigs.EstimatedTokens)
	}
}

// TestExtractSignalsForMessages_ImageBlock verifies image detection in
// Anthropic content blocks.
func TestExtractSignalsForMessages_ImageBlock(t *testing.T) {
	rb := &messagesRequestBody{
		Messages: []byte(`[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image","source":{"type":"base64"}}]}]`),
	}
	sigs := extractSignalsForMessages(rb, []byte(`{}`))
	if !sigs.HasImages {
		t.Fatal("HasImages should be true")
	}
	if sigs.LastUserPrompt != "what is this " {
		t.Fatalf("last user: got %q", sigs.LastUserPrompt)
	}
}

// TestExtractSignalsForResponses_StringInput verifies OpenAI Responses
// with a string input.
func TestExtractSignalsForResponses_StringInput(t *testing.T) {
	rb := &responsesRequestBody{
		Model:        "auto",
		Instructions: "Translate to French",
		Input:        []byte(`"Hello world"`),
	}
	sigs := extractSignalsForResponses(rb, []byte(`{}`))
	if sigs.SystemPrompt != "Translate to French" {
		t.Fatalf("instructions: got %q", sigs.SystemPrompt)
	}
	if sigs.LastUserPrompt != "Hello world" {
		t.Fatalf("input: got %q", sigs.LastUserPrompt)
	}
	if sigs.MessageCount != 1 {
		t.Fatalf("msg count: got %d", sigs.MessageCount)
	}
}

// TestExtractSignalsForResponses_ArrayInput verifies array-of-items input.
func TestExtractSignalsForResponses_ArrayInput(t *testing.T) {
	rb := &responsesRequestBody{
		Input: []byte(`[{"role":"system","content":"sys"},{"role":"user","content":"do the thing"}]`),
	}
	sigs := extractSignalsForResponses(rb, []byte(`{}`))
	if sigs.MessageCount != 2 {
		t.Fatalf("msg count: got %d", sigs.MessageCount)
	}
	// last user role content (raw JSON text of the content field)
	if sigs.LastUserPrompt == "" {
		t.Fatal("last user prompt should be non-empty")
	}
}

// TestMaybeResolveAutoForMessages_FlagOff verifies the flag gate: when
// AutoOnMessages is off, the resolver returns no-op even for model=auto.
func TestMaybeResolveAutoForMessages_FlagOff(t *testing.T) {
	old := autoroute.GetFeatureFlags()
	autoroute.SetGlobalFeatureFlagsForTest(&autoroute.FeatureFlags{AutoOnMessages: false})
	defer autoroute.SetGlobalFeatureFlagsForTest(old)

	h := &MessagesHandler{chatHandler: &ChatHandler{}}
	rb := &messagesRequestBody{Model: "auto"}
	body := []byte(`{"model":"auto"}`)
	newBody, wire, fail := h.maybeResolveAutoForMessages(rb, body, nil, 0)
	if newBody != nil || wire != nil || fail {
		t.Fatalf("flag off should be no-op: body=%v wire=%v fail=%v", newBody != nil, wire != nil, fail)
	}
}

// TestMaybeResolveAutoForResponses_FlagOff same for responses.
func TestMaybeResolveAutoForResponses_FlagOff(t *testing.T) {
	old := autoroute.GetFeatureFlags()
	autoroute.SetGlobalFeatureFlagsForTest(&autoroute.FeatureFlags{AutoOnResponses: false})
	defer autoroute.SetGlobalFeatureFlagsForTest(old)

	h := &ResponsesHandler{chatHandler: &ChatHandler{}}
	rb := &responsesRequestBody{Model: "auto"}
	newBody, wire, fail := h.maybeResolveAutoForResponses(rb, []byte(`{"model":"auto"}`), nil, 0)
	if newBody != nil || wire != nil || fail {
		t.Fatalf("flag off should be no-op: body=%v wire=%v fail=%v", newBody != nil, wire != nil, fail)
	}
}

// TestMaybeResolveAutoForMessages_NonAuto verifies non-auto model is a no-op.
func TestMaybeResolveAutoForMessages_NonAuto(t *testing.T) {
	h := &MessagesHandler{chatHandler: &ChatHandler{}}
	rb := &messagesRequestBody{Model: "claude-sonnet-4.5"}
	newBody, wire, fail := h.maybeResolveAutoForMessages(rb, []byte(`{}`), nil, 0)
	if newBody != nil || wire != nil || fail {
		t.Fatal("non-auto model should be no-op")
	}
}
