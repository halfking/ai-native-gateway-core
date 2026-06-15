package routing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/memora"
)

// TestExtractAPIKeyIDBearerToken verifies the primary path: a Bearer
// token in the Authorization header is hashed into a stable positive int.
func TestExtractAPIKeyIDBearerToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer sk-test-key-123")
	ci := identity.ClientIdentity{}
	got := extractAPIKeyID(r, ci)
	if got <= 0 {
		t.Fatalf("expected positive int from bearer token, got %d", got)
	}
	// Determinism: same token → same id.
	if got2 := extractAPIKeyID(r, ci); got2 != got {
		t.Fatalf("non-deterministic: %d vs %d", got, got2)
	}
	// Different token → different id.
	r2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r2.Header.Set("Authorization", "Bearer sk-different-key")
	if got3 := extractAPIKeyID(r2, ci); got3 == got {
		t.Fatalf("different tokens should produce different ids, both %d", got)
	}
}

// TestExtractAPIKeyIDIdentityHashFallback verifies the secondary path:
// when there's no Authorization header, the IdentityHash is used.
func TestExtractAPIKeyIDIdentityHashFallback(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ci := identity.ClientIdentity{IdentityHash: "abcdef0123456789"}
	got := extractAPIKeyID(r, ci)
	if got <= 0 {
		t.Fatalf("expected positive int from identity hash, got %d", got)
	}
	if got != stableHashKey("abcdef0123456789") {
		t.Fatalf("id %d != stableHashKey(IdentityHash) %d", got, stableHashKey("abcdef0123456789"))
	}
}

// TestExtractAPIKeyIDNilRequest verifies graceful handling of nil request.
func TestExtractAPIKeyIDNilRequest(t *testing.T) {
	got := extractAPIKeyID(nil, identity.ClientIdentity{})
	if got != 0 {
		t.Fatalf("expected 0 for nil request + empty identity, got %d", got)
	}
}

// TestSplitConversationBlocks verifies the parser that reverses the
// "[role]\ntext\n\n" format produced by extractConversationText.
func TestSplitConversationBlocks(t *testing.T) {
	text := "[user]\nWhat is 2+2?\n\n[assistant]\n4\n\n[user]\nThanks\n\n"
	blocks := splitConversationBlocks(text)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0].role != "user" || blocks[0].text != "What is 2+2?" {
		t.Fatalf("block0 = %+v", blocks[0])
	}
	if blocks[1].role != "assistant" || blocks[1].text != "4" {
		t.Fatalf("block1 = %+v", blocks[1])
	}
	if blocks[2].role != "user" || blocks[2].text != "Thanks" {
		t.Fatalf("block2 = %+v", blocks[2])
	}
}

// TestSplitConversationBlocksMultiline verifies a block whose text
// spans multiple lines is correctly joined.
func TestSplitConversationBlocksMultiline(t *testing.T) {
	text := "[user]\nline one\nline two\nline three\n\n[assistant]\nreply\n\n"
	blocks := splitConversationBlocks(text)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].text != "line one\nline two\nline three" {
		t.Fatalf("multiline text not joined: %q", blocks[0].text)
	}
}

// TestExtractAssistantReplyTextOpenAI verifies the non-stream OpenAI
// chat.completion response extractor.
func TestExtractAssistantReplyTextOpenAI(t *testing.T) {
	body := []byte(`{
		"choices":[{"message":{"role":"assistant","content":"The answer is 42"}}]
	}`)
	got := extractAssistantReplyText(body, "openai-completions")
	if got != "The answer is 42" {
		t.Fatalf("got %q", got)
	}
}

// TestExtractAssistantReplyTextAnthropic verifies the non-stream
// Anthropic Messages response extractor.
func TestExtractAssistantReplyTextAnthropic(t *testing.T) {
	body := []byte(`{
		"content":[
			{"type":"text","text":"First sentence. "},
			{"type":"text","text":"Second sentence."}
		]
	}`)
	got := extractAssistantReplyText(body, "anthropic-messages")
	want := "First sentence. \nSecond sentence."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestExtractAssistantReplyTextEmpty verifies graceful handling of
// malformed/empty bodies.
func TestExtractAssistantReplyTextEmpty(t *testing.T) {
	if got := extractAssistantReplyText(nil, "openai-completions"); got != "" {
		t.Fatalf("expected empty for nil body, got %q", got)
	}
	if got := extractAssistantReplyText([]byte("not json"), "openai-completions"); got != "" {
		t.Fatalf("expected empty for invalid json, got %q", got)
	}
	if got := extractAssistantReplyText([]byte(`{"choices":[]}`), "openai-completions"); got != "" {
		t.Fatalf("expected empty for no choices, got %q", got)
	}
}

// TestExtractMemoraMessages verifies the full request→memora.Message
// extraction pipeline for an OpenAI-format body.
func TestExtractMemoraMessages(t *testing.T) {
	body := []byte(`{
		"model":"test",
		"messages":[
			{"role":"system","content":"You are helpful"},
			{"role":"user","content":"Hello"},
			{"role":"assistant","content":"Hi there"}
		]
	}`)
	msgs := extractMemoraMessages(body, "openai-completions", nil)
	if len(msgs) == 0 {
		t.Fatal("expected non-empty messages")
	}
	// Should contain at least the user and assistant turns.
	var hasUser, hasAssistant bool
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "Hello" {
			hasUser = true
		}
		if m.Role == "assistant" && m.Content == "Hi there" {
			hasAssistant = true
		}
	}
	if !hasUser || !hasAssistant {
		t.Fatalf("missing user(%v) or assistant(%v) in %d msgs", hasUser, hasAssistant, len(msgs))
	}
}

// TestExtractMemoraMessagesWithResponse verifies that when a non-stream
// response body is provided, the assistant reply is appended.
func TestExtractMemoraMessagesWithResponse(t *testing.T) {
	body := []byte(`{
		"messages":[{"role":"user","content":"What is 2+2?"}]
	}`)
	respBody := []byte(`{
		"choices":[{"message":{"role":"assistant","content":"4"}}]
	}`)
	msgs := extractMemoraMessages(body, "openai-completions", respBody)
	// Last message should be the assistant reply.
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || last.Content != "4" {
		t.Fatalf("last msg = %+v, want assistant/4", last)
	}
}

// TestEnqueueMemoraWriteNilSink verifies the nil-sink no-op.
func TestEnqueueMemoraWriteNilSink(t *testing.T) {
	e := &Executor{} // MemoraSink is nil
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer sk-test")
	params := &ExecParams{R: r, BodyBytes: []byte(`{"messages":[]}`)}
	// Should not panic.
	e.enqueueMemoraWrite(params, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), nil)
}

// TestEnqueueMemoraWriteEnqueues verifies the happy path: a valid
// request with X-Task-Id is enqueued into the sink and the worker
// processes it (AddMessage is called against a stub Memora server).
func TestEnqueueMemoraWriteEnqueues(t *testing.T) {
	// Start a stub Memora server that accepts /product/add.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	client := memora.NewClient(memora.ClientConfig{
		BaseURL:    srv.URL,
		AddTimeout: 2 * time.Second,
	})
	sink := memora.NewSink(client, 1, 4)
	sink.Start()
	defer sink.Stop(nil)

	e := &Executor{MemoraSink: sink}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer sk-test")
	r.Header.Set("X-Task-Id", "task-abc")
	params := &ExecParams{
		R:              r,
		BodyBytes:      []byte(`{}`),
		ClientProtocol: "openai-completions",
	}
	sourceBody := []byte(`{
		"messages":[{"role":"user","content":"Hello world"}]
	}`)
	e.enqueueMemoraWrite(params, sourceBody, nil)
	stats := sink.Stats()
	if stats.Enqueued == 0 {
		t.Fatal("expected Enqueued >= 1")
	}
}

// TestEnqueueMemoraWriteNoTaskID verifies that without a task id
// (and without a body for auto-derive), nothing is enqueued.
func TestEnqueueMemoraWriteNoTaskID(t *testing.T) {
	// Start a stub server so the sink is fully enabled.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := memora.NewClient(memora.ClientConfig{BaseURL: srv.URL})
	sink := memora.NewSink(client, 1, 4)
	sink.Start()
	defer sink.Stop(nil)

	e := &Executor{MemoraSink: sink}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer sk-test")
	params := &ExecParams{R: r, BodyBytes: nil}
	// Empty source body → TaskID returns "" → no enqueue.
	e.enqueueMemoraWrite(params, nil, nil)
	if sink.Stats().Enqueued != 0 {
		t.Fatal("expected 0 enqueued for empty body")
	}
}

// TestShouldHeuristicCompactBoundary verifies the core heuristic:
// 4xx (non-auth) + large body → true; everything else → false.
func TestShouldHeuristicCompactBoundary(t *testing.T) {
	cw := 100_000 // 100k tokens context window
	// Use KindTransient as a generic non-excluded error kind.
	genericKind := errorsx.KindTransient

	tests := []struct {
		name       string
		status     int
		kind       errorsx.ErrorKind
		bodyLen    int
		contextWin *int
		want       bool
	}{
		{"200 ok", 200, genericKind, 500_000, &cw, false},
		{"500 server", 500, genericKind, 500_000, &cw, false},
		{"401 auth", 401, genericKind, 500_000, &cw, false},
		{"429 ratelimit", 429, genericKind, 500_000, &cw, false},
		{"400 small body", 400, genericKind, 1_000, &cw, false},
		{"concurrent excluded", 400, errorsx.KindConcurrent, 500_000, &cw, false},
		{"model_not_found excluded", 404, errorsx.KindModelNotFound, 500_000, &cw, false},
		{"400 large body", 400, genericKind, 400_000, &cw, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldHeuristicCompact(tt.status, tt.kind, tt.bodyLen, tt.contextWin)
			if got != tt.want {
				t.Fatalf("shouldHeuristicCompact(%d, %v, %d, _) = %v, want %v",
					tt.status, tt.kind, tt.bodyLen, got, tt.want)
			}
		})
	}
}

// TestShouldHeuristicCompactNilWindow verifies that nil ContextWindow
// disables the heuristic.
func TestShouldHeuristicCompactNilWindow(t *testing.T) {
	if shouldHeuristicCompact(400, errorsx.KindTransient, 999_999, nil) {
		t.Fatal("nil context window should disable heuristic")
	}
}

// TestShouldHeuristicCompactLargeBody verifies that a 4xx with a body
// exceeding 90% of the context window triggers the heuristic.
func TestShouldHeuristicCompactLargeBody(t *testing.T) {
	cw := 100_000
	// 90% of 100k = 90k tokens. 90k * 3.5 chars/token = 315k chars.
	// A 400k-char body exceeds this threshold.
	bodyLen := 400_000
	if !shouldHeuristicCompact(400, errorsx.KindTransient, bodyLen, &cw) {
		t.Fatal("400 with large body should trigger heuristic")
	}
}

// TestRebuildBodyPreservesModel verifies that the memora rebuilder
// preserves top-level fields (model, stream, temperature).
func TestRebuildBodyPreservesModel(t *testing.T) {
	body := []byte(`{
		"model":"glm-5.2",
		"stream":true,
		"temperature":0.7,
		"messages":[
			{"role":"system","content":"sys"},
			{"role":"user","content":"old1"},
			{"role":"assistant","content":"reply1"},
			{"role":"user","content":"latest"}
		]
	}`)
	snippets := []memora.Memory{{Text: "Important fact from earlier turns."}}
	got, ok := memora.RebuildBodyWithMemoraSnippets(body, snippets, 2)
	if !ok {
		t.Fatal("expected rebuild to succeed")
	}
	var parsed struct {
		Model       string `json:"model"`
		Stream      bool   `json:"stream"`
		Temperature float64 `json:"temperature"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Model != "glm-5.2" {
		t.Fatalf("model = %q", parsed.Model)
	}
	if !parsed.Stream {
		t.Fatal("stream should be preserved")
	}
	if parsed.Temperature != 0.7 {
		t.Fatalf("temperature = %v", parsed.Temperature)
	}
}
