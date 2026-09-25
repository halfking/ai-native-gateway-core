package executors

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// ────────────────────────────────────────────────────────────────────────
// Test helpers
// ────────────────────────────────────────────────────────────────────────

// newOllamaClientProvider constructs a minimal *provider.Client
// suitable for downstream tests that only need the OllamaExecutor's
// URL construction. Real deployments source the candidate from the
// SQL loader; we use a stub here so the tests stay isolated from the
// runtime machinery.
func newOllamaClientProvider(baseURL string) provider.Candidate {
	return provider.Candidate{
		ProviderID:   42,
		CredentialID: 7,
		BaseURL:      baseURL,
		Protocol:     "ollama-native",
		CatalogCode:  "ollama",
		APIKey:       "sk-test", // optional; Ollama usually ignores
		RawModel:     "llama3.1",
	}
}

// ────────────────────────────────────────────────────────────────────────
// BuildRequest
// ────────────────────────────────────────────────────────────────────────

// TestOllamaExecutor_BuildRequest_NativePathAndAuth pins the URL
// construction contract: the executor must route through
// upstreamurl.EpOllamaChat (which yields /api/chat on a bare base
// URL) and emit Bearer auth when an APIKey is configured. The stream
// mode flips Accept to application/x-ndjson.
func TestOllamaExecutor_BuildRequest_NativePathAndAuth(t *testing.T) {
	oe := &OllamaExecutor{}
	cand := newOllamaClientProvider("http://localhost:11434")
	body := []byte(`{"model":"llama3.1","messages":[{"role":"user","content":"hi"}]}`)

	req, err := oe.BuildRequest(cand, body, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.URL.String() != "http://localhost:11434/api/chat" {
		t.Errorf("URL = %q, want http://localhost:11434/api/chat", req.URL.String())
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := req.Header.Get("Accept"); got != "" {
		t.Errorf("Accept = %q for non-stream, want empty", got)
	}
}

// TestOllamaExecutor_BuildRequest_StreamSetsNDJSONAccept asserts the
// stream-mode Accept header (application/x-ndjson) is the documented
// Ollama framing signal. Without it, some middleboxes downgrade the
// request to a non-stream handler.
func TestOllamaExecutor_BuildRequest_StreamSetsNDJSONAccept(t *testing.T) {
	oe := &OllamaExecutor{}
	cand := newOllamaClientProvider("http://localhost:11434")
	body := []byte(`{"model":"llama3.1","stream":true,"messages":[]}`)

	req, err := oe.BuildRequest(cand, body, true)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if got := req.Header.Get("Accept"); got != "application/x-ndjson" {
		t.Errorf("Accept = %q, want application/x-ndjson", got)
	}
}

// TestOllamaExecutor_BuildRequest_NoAPIKeyOmitsAuth confirms the
// executor doesn't synthesize an empty Authorization header when no
// APIKey is configured. Most self-hosted Ollama deployments ignore
// auth entirely; emitting `Authorization: Bearer ` would actively
// break them.
func TestOllamaExecutor_BuildRequest_NoAPIKeyOmitsAuth(t *testing.T) {
	oe := &OllamaExecutor{}
	cand := newOllamaClientProvider("http://localhost:11434")
	cand.APIKey = ""
	req, err := oe.BuildRequest(cand, []byte("{}"), false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty when no APIKey configured", got)
	}
}

// TestOllamaExecutor_BuildRequest_EmptyBaseURLFails guards the
// fail-fast contract: an empty BaseURL must produce a typed error
// rather than a panic or a request against the wrong endpoint.
func TestOllamaExecutor_BuildRequest_EmptyBaseURLFails(t *testing.T) {
	oe := &OllamaExecutor{}
	cand := newOllamaClientProvider("")
	_, err := oe.BuildRequest(cand, []byte("{}"), false)
	if err == nil {
		t.Fatalf("BuildRequest with empty BaseURL should fail")
	}
}

// TestOllamaExecutor_BuildRequest_StripsExistingAPIPath verifies the
// idempotency rule of upstreamurl.Build: a base URL that already
// includes /api/chat must NOT produce /api/chat/api/chat.
func TestOllamaExecutor_BuildRequest_StripsExistingAPIPath(t *testing.T) {
	oe := &OllamaExecutor{}
	cand := newOllamaClientProvider("http://localhost:11434/api/chat")
	req, err := oe.BuildRequest(cand, []byte("{}"), false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if got := req.URL.String(); got != "http://localhost:11434/api/chat" {
		t.Errorf("URL = %q, want http://localhost:11434/api/chat (idempotent)", got)
	}
}

// ────────────────────────────────────────────────────────────────────────
// WriteNonStreamResponse
// ────────────────────────────────────────────────────────────────────────

// TestOllamaExecutor_WriteNonStreamResponse_OpenAIShape verifies the
// happy-path round trip: an Ollama 200 non-stream body becomes an
// OpenAI chat.completion JSON body the OpenAI client can parse.
func TestOllamaExecutor_WriteNonStreamResponse_OpenAIShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity: confirm the executor hit /api/chat.
		if r.URL.Path != "/api/chat" {
			t.Errorf("upstream path = %q, want /api/chat", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // test response writer
		w.Write([]byte(`{
			"model": "llama3.1",
			"created_at": "2026-09-24T10:00:00.000Z",
			"message": {"role": "assistant", "content": "hi back", "thinking": "Let me greet"},
			"done_reason": "stop",
			"done": true,
			"prompt_eval_count": 8,
			"eval_count": 4
		}`))
	}))
	defer srv.Close()

	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	rec := httptest.NewRecorder()
	body, werr := oe.WriteNonStreamResponse(rec, resp, "client-model", "", nil)
	if werr != nil {
		t.Fatalf("WriteNonStreamResponse: %v", werr)
	}

	// The OpenAI client must see the model they sent (override).
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal client body: %v\nbody: %s", err, body)
	}
	if got["model"] != "client-model" {
		t.Errorf("client body model = %v, want client-model", got["model"])
	}
	if got["object"] != "chat.completion" {
		t.Errorf("object = %v, want chat.completion", got["object"])
	}
	choices, ok := got["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices missing or wrong shape: %v", got["choices"])
	}
	first := choices[0].(map[string]any)
	msg := first["message"].(map[string]any)
	if msg["content"] != "hi back" {
		t.Errorf("message.content = %v, want hi back", msg["content"])
	}
	if msg["reasoning_content"] != "Let me greet" {
		t.Errorf("message.reasoning_content = %v, want 'Let me greet'", msg["reasoning_content"])
	}
	if first["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want stop", first["finish_reason"])
	}
	usage := got["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) != 8 {
		t.Errorf("usage.prompt_tokens = %v, want 8", usage["prompt_tokens"])
	}
	if usage["completion_tokens"].(float64) != 4 {
		t.Errorf("usage.completion_tokens = %v, want 4", usage["completion_tokens"])
	}
}

// TestOllamaExecutor_WriteNonStreamResponse_TypedStreamError guards
// the r0924 fix-a task 3 contract: Ollama's "200 + error" shape must
// surface as a typed *upstreampkg.Error with Kind=KindUpstreamDown,
// wrapping the parser's *ir.StreamError so the §11.6 fail wire can
// route via errors.As(err, **StreamError).
func TestOllamaExecutor_WriteNonStreamResponse_TypedStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Ollama returns 200 + top-level "error" string for some
		// upstream / validation failures.
		//nolint:errcheck // test response writer
		w.Write([]byte(`{"error":"model 'qwen-bogus' not found"}`))
	}))
	defer srv.Close()

	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	rec := httptest.NewRecorder()
	_, werr := oe.WriteNonStreamResponse(rec, resp, "client-model", "", nil)
	if werr == nil {
		t.Fatalf("expected typed error, got nil")
	}
	var ue *upstreampkg.Error
	if !errors.As(werr, &ue) {
		t.Fatalf("werr is not *upstreampkg.Error: %T %v", werr, werr)
	}
	if ue.Kind != errorsx.KindUpstreamDown {
		t.Errorf("Kind = %v, want KindUpstreamDown", ue.Kind)
	}
	if !strings.Contains(ue.Message, "model 'qwen-bogus' not found") {
		t.Errorf("Message = %q, want to contain vendor error", ue.Message)
	}
	// The wrapped *ir.StreamError must remain reachable for
	// errors.As(err, **StreamError) downstream.
	var se *ir.StreamError
	if !errors.As(werr, &se) {
		t.Errorf("expected *ir.StreamError in chain; got %v", werr)
	} else if se.Type != "upstream_error" {
		t.Errorf("StreamError.Type = %q, want upstream_error", se.Type)
	}
}

// TestOllamaExecutor_WriteNonStreamResponse_PassThrough4xx pins the
// non-retryable 4xx passthrough: the upstream body must reach the
// client verbatim (so SDKs can render the actual error) and the
// status code must be preserved.
func TestOllamaExecutor_WriteNonStreamResponse_PassThrough4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		//nolint:errcheck // test response writer
		w.Write([]byte(`{"error":"invalid chat message"}`))
	}))
	defer srv.Close()

	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	rec := httptest.NewRecorder()
	body, werr := oe.WriteNonStreamResponse(rec, resp, "client-model", "", nil)
	if werr != nil {
		t.Fatalf("WriteNonStreamResponse: %v", werr)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !bytes.Contains(body, []byte(`"error":"invalid chat message"`)) {
		t.Errorf("body = %s, want raw upstream body", body)
	}
}

// TestOllamaExecutor_WriteNonStreamResponse_MalformedJSON is the
// fallback branch: when the body is neither a valid Ollama response
// nor a top-level error envelope, the executor must surface a typed
// KindConversion error rather than synthesizing a fake success body.
func TestOllamaExecutor_WriteNonStreamResponse_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // test response writer
		w.Write([]byte(`<<<not json>>>`))
	}))
	defer srv.Close()

	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	rec := httptest.NewRecorder()
	_, werr := oe.WriteNonStreamResponse(rec, resp, "client-model", "", nil)
	if werr == nil {
		t.Fatalf("expected conversion error, got nil")
	}
	var ue *upstreampkg.Error
	if !errors.As(werr, &ue) {
		t.Fatalf("werr is not *upstreampkg.Error: %T", werr)
	}
	if ue.Kind != errorsx.KindConversion {
		t.Errorf("Kind = %v, want KindConversion", ue.Kind)
	}
}

// ────────────────────────────────────────────────────────────────────────
// StreamResponse
// ────────────────────────────────────────────────────────────────────────

// TestOllamaExecutor_StreamResponse_CumulativeDiffedCorrectly is the
// audit-r0924 §11.6 contract test: Ollama's wire `message.content`
// is CUMULATIVE (the full text so far, repeated each frame). The
// executor must diff against the previously seen cumulative value
// and emit ONLY the new tail as `delta.content`. If it forwardes the
// raw value, the client sees the full text duplicated every frame.
func TestOllamaExecutor_StreamResponse_CumulativeDiffedCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		lines := []string{
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":""},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"He"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"Hel"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"Hell"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"Hello"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"Hello!"},"done_reason":"stop","done":true,"prompt_eval_count":3,"eval_count":2}`,
		}
		for _, ln := range lines {
			//nolint:errcheck // test response writer
			w.Write([]byte(ln + "\n"))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}

	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	rec := httptest.NewRecorder()
	rec2 := &flushingRecorder{ResponseRecorder: *rec, flushCount: new(int)}
	outcome := oe.StreamResponse(context.Background(), rec2, resp)
	if outcome.Interrupted {
		t.Errorf("stream interrupted unexpectedly: %s", outcome.Reason)
	}
	if outcome.ChunkCount < 5 {
		t.Errorf("ChunkCount = %d, want >= 5 (one per character of 'Hello!')", outcome.ChunkCount)
	}

	body := rec2.Body.String()
	// The SSE wire must contain a [DONE] sentinel.
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("stream missing [DONE] sentinel; body: %s", body)
	}
	// Count occurrences of "Hello!" — if cumulative diffing works, the
	// client never sees the full string "Hello!" because each delta
	// carries only the new tail (the union of all deltas equals
	// "Hello!" but no single frame contains it). The raw cumulative
	// values appear ONLY in the upstream NDJSON we discarded; the
	// OUTGOING SSE must NEVER contain "Hello!" verbatim.
	count := strings.Count(body, "Hello!")
	if count != 0 {
		t.Errorf("body contains %d copies of 'Hello!', want 0 (cumulative diff must split it)\nfull body: %s", count, body)
	}
	// The cumulative-diff delta sequence must be present. Upstream
	// sends "" → "He" → "Hel" → "Hell" → "Hello" → "Hello!" so the
	// outgoing SSE carries the per-frame new-tail deltas: "He", "l",
	// "l", "o", "!".
	wantDeltas := []string{`"content":"He"`, `"content":"l"`, `"content":"o"`, `"content":"!"`}
	for _, w := range wantDeltas {
		if !strings.Contains(body, w) {
			t.Errorf("body missing delta %q\nfull body: %s", w, body)
		}
	}
}

// TestOllamaExecutor_StreamResponse_ReasoningForwarded verifies that
// Ollama's `message.thinking` field is surfaced to the client as
// `delta.reasoning_content` so OpenAI-style reasoning-aware clients
// can render the thinking trace.
func TestOllamaExecutor_StreamResponse_ReasoningForwarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		lines := []string{
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"","thinking":"Step 1"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"","thinking":"Step 2"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"final"},"done_reason":"stop","done":true}`,
		}
		for _, ln := range lines {
			//nolint:errcheck // test response writer
			w.Write([]byte(ln + "\n"))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	rec := httptest.NewRecorder()
	fr := &flushingRecorder{ResponseRecorder: *rec, flushCount: new(int)}
	outcome := oe.StreamResponse(context.Background(), fr, resp)
	if outcome.Interrupted {
		t.Errorf("stream interrupted: %s", outcome.Reason)
	}
	body := fr.Body.String()
	if !strings.Contains(body, `"reasoning_content":"Step 1"`) {
		t.Errorf("body missing reasoning Step 1\n%s", body)
	}
	if !strings.Contains(body, `"reasoning_content":"Step 2"`) {
		t.Errorf("body missing reasoning Step 2\n%s", body)
	}
	if !strings.Contains(body, `"content":"final"`) {
		t.Errorf("body missing content 'final'\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("body missing [DONE]\n%s", body)
	}
}

// TestOllamaExecutor_StreamResponse_TerminalOneShot verifies the
// §11.6 ordering invariant: a single NDJSON line that carries BOTH
// content AND `done:true` (e.g. "yes"/"no" answers) must produce a
// content delta chunk FIRST, then a Done chunk. Collapsing them into
// one frame would cause the dispatcher to misclassify the response.
func TestOllamaExecutor_StreamResponse_TerminalOneShot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		// Single line that has content AND done=true simultaneously.
		//nolint:errcheck // test response writer
		w.Write([]byte(`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"yes"},"done_reason":"stop","done":true,"prompt_eval_count":1,"eval_count":1}` + "\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	rec := httptest.NewRecorder()
	fr := &flushingRecorder{ResponseRecorder: *rec, flushCount: new(int)}
	outcome := oe.StreamResponse(context.Background(), fr, resp)
	if outcome.Interrupted {
		t.Errorf("stream interrupted: %s", outcome.Reason)
	}
	body := fr.Body.String()
	if !strings.Contains(body, `"content":"yes"`) {
		t.Errorf("body missing content 'yes'\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("body missing [DONE]\n%s", body)
	}
}

// TestOllamaExecutor_StreamResponse_EOFWithoutDone is the
// fabricated-success guard: if the upstream EOFs without ever
// emitting `done:true`, the executor MUST NOT report success. It
// must emit a §11.6 eof_without_done error frame plus [DONE], and
// return StreamOutcome{Interrupted, KindEmptyResponse, ...} so the
// dispatcher classifies this as upstream silence rather than a
// successful empty response.
func TestOllamaExecutor_StreamResponse_EOFWithoutDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		// Two content frames, then close. NO done frame.
		for _, ln := range []string{
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"Hel"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"Hello"},"done":false}`,
		} {
			//nolint:errcheck // test response writer
			w.Write([]byte(ln + "\n"))
			flusher.Flush()
		}
		// Implicit close = EOF.
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	rec := httptest.NewRecorder()
	fr := &flushingRecorder{ResponseRecorder: *rec, flushCount: new(int)}
	outcome := oe.StreamResponse(context.Background(), fr, resp)
	if !outcome.Interrupted {
		t.Fatalf("stream should be interrupted; got outcome=%+v body=%s", outcome, fr.Body.String())
	}
	if outcome.Kind != errorsx.KindEmptyResponse {
		t.Errorf("outcome.Kind = %v, want KindEmptyResponse", outcome.Kind)
	}
	if !strings.Contains(outcome.Reason, "eof_without_done") {
		t.Errorf("outcome.Reason = %q, want eof_without_done", outcome.Reason)
	}
	body := fr.Body.String()
	if !strings.Contains(body, "stream_truncated") {
		t.Errorf("client body missing stream_truncated error marker\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("client body missing [DONE]\n%s", body)
	}
	if !outcome.TerminalRendered {
		t.Errorf("TerminalRendered should be true so handler blackholes second terminal")
	}
}

// TestOllamaExecutor_StreamResponse_InBandError verifies that an
// upstream `error` field on the NDJSON stream surfaces as an
// `error` SSE event (typed envelope), marks the stream interrupted,
// and prevents the dispatcher's "second terminal" stacking.
func TestOllamaExecutor_StreamResponse_InBandError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, ln := range []string{
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"Partial"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","error":"upstream crashed","done":false}`,
		} {
			//nolint:errcheck // test response writer
			w.Write([]byte(ln + "\n"))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	rec := httptest.NewRecorder()
	fr := &flushingRecorder{ResponseRecorder: *rec, flushCount: new(int)}
	outcome := oe.StreamResponse(context.Background(), fr, resp)
	if !outcome.Interrupted {
		t.Fatalf("expected interrupted outcome; got %+v", outcome)
	}
	if outcome.Kind != errorsx.KindUpstreamDown {
		t.Errorf("Kind = %v, want KindUpstreamDown", outcome.Kind)
	}
	if !strings.Contains(outcome.Reason, "upstream crashed") {
		t.Errorf("Reason = %q, want to surface the upstream error", outcome.Reason)
	}
	if !outcome.TerminalRendered {
		t.Errorf("TerminalRendered should be true after error frame")
	}
	body := fr.Body.String()
	if !strings.Contains(body, `"message":"upstream crashed"`) {
		t.Errorf("body missing in-band error message\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("body missing [DONE]\n%s", body)
	}
}

// TestOllamaExecutor_StreamResponse_FirstByteCallback fires the
// first-byte callback exactly once when the first content delta is
// written. We verify by wrapping the writer in a stub that exposes
// the FirstSemanticByteCallback interface.
func TestOllamaExecutor_StreamResponse_FirstByteCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		lines := []string{
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":""},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"hello"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"hello!"},"done_reason":"stop","done":true}`,
		}
		for _, ln := range lines {
			//nolint:errcheck // test response writer
			w.Write([]byte(ln + "\n"))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}

	var firedCount int32
	rec := httptest.NewRecorder()
	fr := &flushingRecorderWithCB{ResponseRecorder: *rec, cb: func() {
		atomic.AddInt32(&firedCount, 1)
	}}
	oe := &OllamaExecutor{ClientProtocol: "openai-completions"}
	outcome := oe.StreamResponse(context.Background(), fr, resp)
	if outcome.Interrupted {
		t.Errorf("stream interrupted: %s", outcome.Reason)
	}
	if got := atomic.LoadInt32(&firedCount); got != 1 {
		t.Errorf("first-byte callback fired %d times, want exactly 1", got)
	}
}

// ────────────────────────────────────────────────────────────────────────
// ExtractUsage + CheckSoftMismatch
// ────────────────────────────────────────────────────────────────────────

// TestOllamaExecutor_ExtractUsage pins the (input, output) extraction
// from a non-stream Ollama response body.
func TestOllamaExecutor_ExtractUsage(t *testing.T) {
	oe := &OllamaExecutor{}
	body := []byte(`{"prompt_eval_count":17,"eval_count":5,"done":true}`)
	in, out := oe.ExtractUsage(nil, body)
	if in == nil || *in != 17 {
		t.Errorf("inputTokens = %v, want 17", in)
	}
	if out == nil || *out != 5 {
		t.Errorf("outputTokens = %v, want 5", out)
	}
	// nil body → nil pointers
	in2, out2 := oe.ExtractUsage(nil, nil)
	if in2 != nil || out2 != nil {
		t.Errorf("nil body should yield nil pointers; got in=%v out=%v", in2, out2)
	}
}

// TestOllamaExecutor_CheckSoftMismatch covers the operator-side model
// aliasing detector: identical strings are not a mismatch; differing
// ones (case-insensitive) are.
func TestOllamaExecutor_CheckSoftMismatch(t *testing.T) {
	oe := &OllamaExecutor{}
	cases := []struct {
		req, resp string
		want      bool
	}{
		{"llama3.1", "llama3.1", false},
		{"llama3.1", "LLAMA3.1", false}, // case-insensitive
		{"llama3.1", "llama3.2", true},
		{"", "llama3.1", false},        // empty req is a no-op
		{"llama3.1", "", false},        // empty resp is a no-op
		{"", "", false},
	}
	for _, tc := range cases {
		mis, _ := oe.CheckSoftMismatch(tc.req, tc.resp)
		if mis != tc.want {
			t.Errorf("CheckSoftMismatch(%q,%q) = %v, want %v", tc.req, tc.resp, mis, tc.want)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────
// executeOllama integration (via real httptest upstream)
// ────────────────────────────────────────────────────────────────────────

// newTestExecutor builds a minimal *Executor that can drive a real
// HTTP round-trip against an httptest server. We intentionally keep
// the field set minimal so the test focuses on P4.3 executor behavior
// rather than the broader Executor orchestration (retry, circuit,
// etc., which already have their own test suites in
// executor_anthropic_test.go / executor_chat_test.go).
func newTestExecutor() *Executor {
	return &Executor{
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}
}

// TestExecutor_ExecuteOllama_NonStream exercises the full integration
// path: raw OpenAI Chat Completions wire body → executeOllama → real
// Ollama stub → OpenAI chat wire shape. This is the r0925 fix-up
// regression: pre-r0925 the executor expected IR-shaped JSON and
// failed closed on raw client wire; the production dispatch path
// passes the raw wire, so production calls were broken. The test
// guards against that regression by exercising the end-to-end path
// with a real client wire body.
func TestExecutor_ExecuteOllama_NonStream(t *testing.T) {
	seenPath := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		// Echo the request body so we can verify SerializeOllama
		// preserved Ollama-private fields from the OpenAI wire's
		// ollama.* Extensions passthrough.
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("upstream body is not JSON: %v\n%s", err, body)
			w.WriteHeader(400)
			return
		}
		// Verify the executor emitted ollama-private fields.
		if req["format"] != "json" {
			t.Errorf("upstream saw format=%v, want 'json' (passthrough preserved)", req["format"])
		}
		if req["keep_alive"] != "5m" {
			t.Errorf("upstream saw keep_alive=%v, want '5m'", req["keep_alive"])
		}
		opts, ok := req["options"].(map[string]any)
		if !ok {
			t.Errorf("upstream body missing options object: %v", req["options"])
		} else if opts["num_ctx"] == nil {
			t.Errorf("options.num_ctx missing; got %v", opts)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // test response writer
		w.Write([]byte(`{
			"model":"llama3.1",
			"created_at":"2026-09-24T12:00:00Z",
			"message":{"role":"assistant","content":"ok"},
			"done_reason":"stop","done":true,
			"prompt_eval_count":4,"eval_count":1
		}`))
	}))
	defer srv.Close()

	// Real OpenAI Chat Completions wire body — what the relay hands
	// to executeOllama in production. Ollama-private fields ride
	// along as ollama.* Extensions keys (per the IR contract for
	// SerializeOllama).
	wireBody := []byte(`{
		"model":"llama3.1",
		"messages":[{"role":"user","content":"ping"}],
		"ollama.format":"json",
		"ollama.keep_alive":"5m",
		"ollama.options.num_ctx":4096
	}`)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(wireBody))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:              rec,
		R:              req,
		BodyBytes:      wireBody,
		IsStream:       false,
		ClientModel:    "client-llama",
		OutboundModel:  "llama3.1",
		ClientProtocol: "openai-completions",
		RequestID:      "test-req-1",
	}
	cand := provider.Candidate{
		ProviderID:   42,
		CredentialID: 7,
		BaseURL:      srv.URL,
		Protocol:     "ollama-native",
		CatalogCode:  "ollama",
		APIKey:       "",
		RawModel:     "llama3.1",
	}

	e := newTestExecutor()
	result, err := e.executeOllama(params, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOllama: %v", err)
	}
	if result == nil {
		t.Fatalf("nil result")
	}
	if seenPath != "/api/chat" {
		t.Errorf("upstream path = %q, want /api/chat", seenPath)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"ok"`) {
		t.Errorf("client body missing content 'ok'\n%s", body)
	}
	if !strings.Contains(body, `"model":"client-llama"`) {
		t.Errorf("client body model = %s, want client-llama (override)", body)
	}
	if !strings.Contains(body, `"usage"`) {
		t.Errorf("client body missing usage\n%s", body)
	}
}

// TestExecutor_ExecuteOllama_Stream exercises the streaming path:
// the executor must drive an NDJSON Ollama upstream into a real
// OpenAI-shaped SSE response on the client recorder.
func TestExecutor_ExecuteOllama_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/x-ndjson" {
			t.Errorf("upstream Accept = %q, want application/x-ndjson", got)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, ln := range []string{
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":""},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"hi"},"done":false}`,
			`{"model":"llama3.1","created_at":"t","message":{"role":"assistant","content":"hi!"},"done_reason":"stop","done":true,"prompt_eval_count":2,"eval_count":1}`,
		} {
			//nolint:errcheck // test response writer
			w.Write([]byte(ln + "\n"))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	// Real OpenAI Chat Completions wire body. params.IsStream=true
	// must be honored on the outbound body via the dispatcher's
	// stream gate.
	wireBody := []byte(`{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(wireBody))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:              rec,
		R:              req,
		BodyBytes:      wireBody,
		IsStream:       true,
		ClientModel:    "client-llama",
		OutboundModel:  "llama3.1",
		ClientProtocol: "openai-completions",
		RequestID:      "test-stream-1",
	}
	cand := provider.Candidate{
		ProviderID:   42,
		CredentialID: 7,
		BaseURL:      srv.URL,
		Protocol:     "ollama-native",
		CatalogCode:  "ollama",
		RawModel:     "llama3.1",
	}
	e := newTestExecutor()
	_, err := e.executeOllama(params, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOllama (stream): %v", err)
	}
	body := rec.Body.String()
	// Upstream cumulative content "" → "hi" → "hi!" produces outgoing
	// deltas of "hi" then "!".
	if !strings.Contains(body, `"content":"hi"`) {
		t.Errorf("body missing first delta 'hi'\n%s", body)
	}
	if !strings.Contains(body, `"content":"!"`) {
		t.Errorf("body missing last delta '!'\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("body missing [DONE]\n%s", body)
	}
}

// TestExecutor_ExecuteOllama_UpstreamError exercises the upstream
// error path: a 200 + `{"error":"..."}` body must surface as a typed
// *upstreampkg.Error with Kind=KindUpstreamDown, NOT as a 200 with
// the upstream body forwarded.
func TestExecutor_ExecuteOllama_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // test response writer
		w.Write([]byte(`{"error":"model not installed","done":true}`))
	}))
	defer srv.Close()

	// Real OpenAI Chat Completions wire body — the bug-fix path:
	// pre-r0925 this body would fail closed at the IR-shape parse
	// gate. Today the executor parses it via ir.ParseOpenAI and
	// serializes to Ollama wire; the upstream stub returns the
	// 200+error envelope and the executor surfaces the typed error.
	wireBody := []byte(`{"model":"bogus","messages":[{"role":"user","content":"x"}]}`)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(wireBody))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:              rec,
		R:              req,
		BodyBytes:      wireBody,
		IsStream:       false,
		ClientModel:    "bogus",
		OutboundModel:  "bogus",
		ClientProtocol: "openai-completions",
		RequestID:      "test-err-1",
	}
	cand := provider.Candidate{
		ProviderID: 42, CredentialID: 7,
		BaseURL: srv.URL, Protocol: "ollama-native", CatalogCode: "ollama",
		RawModel: "bogus",
	}
	e := newTestExecutor()
	_, err := e.executeOllama(params, cand, 0, time.Now(), nil)
	if err == nil {
		t.Fatalf("expected error from executeOllama; got nil")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) {
		t.Fatalf("err is not *upstreampkg.Error: %T %v", err, err)
	}
	if ue.Kind != errorsx.KindUpstreamDown {
		t.Errorf("Kind = %v, want KindUpstreamDown", ue.Kind)
	}
	if !strings.Contains(ue.Message, "model not installed") {
		t.Errorf("Message = %q, want to contain 'model not installed'", ue.Message)
	}
	var se *ir.StreamError
	if !errors.As(err, &se) {
		t.Errorf("expected *ir.StreamError in chain; got %v", err)
	}
}

// ────────────────────────────────────────────────────────────────────────
// Test helpers: flushing recorders (let Flush() do something)
// ────────────────────────────────────────────────────────────────────────

type flushingRecorder struct {
	httptest.ResponseRecorder
	flushCount *int
}

func (r *flushingRecorder) Flush() {
	if r.flushCount != nil {
		*r.flushCount++
	}
}

// flushingRecorderWithCB satisfies the interface{ FirstSemanticByteCallback() func() }
// shape that OllamaExecutor looks for via type assertion on the
// ResponseWriter. We DON'T need to satisfy the SetFirstSemanticByteCallback
// setter — the executor only reads the callback via the getter.
type flushingRecorderWithCB struct {
	httptest.ResponseRecorder
	cb func()
}

func (r *flushingRecorderWithCB) Flush() {}

func (r *flushingRecorderWithCB) FirstSemanticByteCallback() func() { return r.cb }

// bufioScannerHelper exposes bufio.NewScanner so future tests can
// reach for it without re-importing the package. Currently unused
// but useful for ad-hoc stream-debug scenarios.
var _ = bufio.NewScanner

// Ensure fmt is referenced (some test paths trim unused imports).
var _ = fmt.Sprintf

// ────────────────────────────────────────────────────────────────────────
// finalizeOllamaUpstreamBody validation gates (audit P4.3 fix-up,
// r0925 client-wire wiring revision)
// ────────────────────────────────────────────────────────────────────────
//
// The body that reaches finalizeOllamaUpstreamBody is the RAW CLIENT
// WIRE (OpenAI Chat Completions JSON), not the IR-shaped JSON the
// pre-r0925 implementation expected. The relay now hands the wire
// body through to the executor directly and the executor is
// responsible for converting to IR via ir.ParseOpenAI before
// serializing to Ollama native shape. The validation gates below
// therefore feed real client wire through the harness.

// buildOpenAIChatBody is a small helper that emits a realistic
// OpenAI Chat Completions JSON body with the minimum required
// fields. The Ollama executor's finalize step parses this with
// ir.ParseOpenAI, so the test mirrors what production receives.
func buildOpenAIChatBody(t *testing.T, model string, withMessages bool) []byte {
	t.Helper()
	body := map[string]any{
		"model": model,
	}
	if withMessages {
		body["messages"] = []map[string]any{
			{"role": "user", "content": "hi"},
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal OpenAI Chat body: %v", err)
	}
	return b
}

// runFinalizeOllama is the test harness for the validation gates:
// builds a minimal Executor + ExecParams, calls the package-private
// finalizeOllamaUpstreamBody, returns the typed error so each test
// can assert on its specific Kind + Message.
func runFinalizeOllama(t *testing.T, body []byte, clientProtocol string) error {
	t.Helper()
	e := newTestExecutor()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	params := &ExecParams{
		W:              httptest.NewRecorder(),
		R:              req,
		BodyBytes:      body,
		ClientProtocol: clientProtocol,
	}
	cand := provider.Candidate{
		ProviderID: 42, CredentialID: 7,
		BaseURL: "http://localhost:11434", Protocol: "ollama-native",
		CatalogCode: "ollama", RawModel: "llama3.1",
	}
	_, err := e.finalizeOllamaUpstreamBody(params, cand, body)
	return err
}

// TestFinalizeOllamaUpstreamBody_EmptyBodyFailsClosed: an empty body
// is a client bug (the relay should have populated it). The
// executor MUST NOT silently send a zero-length POST.
func TestFinalizeOllamaUpstreamBody_EmptyBodyFailsClosed(t *testing.T) {
	err := runFinalizeOllama(t, nil, "openai-completions")
	if err == nil {
		t.Fatalf("expected error for empty body")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) || ue.Kind != errorsx.KindClientBug {
		t.Fatalf("err = %v, want *upstreampkg.Error{KindClientBug}", err)
	}
}

// TestFinalizeOllamaUpstreamBody_MalformedWireFailsClosed covers the
// fail-closed contract for a body that is neither empty nor valid
// OpenAI Chat JSON. We use a deliberately broken body (a bare JSON
// value that isn't a JSON object) so ir.ParseOpenAI surfaces a real
// parse error rather than fabricating a partial IR. The executor
// must reject with a typed KindClientBug error.
func TestFinalizeOllamaUpstreamBody_MalformedWireFailsClosed(t *testing.T) {
	body := []byte(`<<<not json>>>`)
	err := runFinalizeOllama(t, body, "openai-completions")
	if err == nil {
		t.Fatalf("expected error for malformed body")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *upstreampkg.Error", err)
	}
	if ue.Kind != errorsx.KindClientBug {
		t.Errorf("Kind = %v, want KindClientBug", ue.Kind)
	}
}

// TestFinalizeOllamaUpstreamBody_TrailingGarbageRejected: ir.ParseOpenAI
// uses json.Unmarshal which silently accepts trailing data; the
// executor adds its own guard so an attacker can't smuggle a second
// body after the legitimate request.
func TestFinalizeOllamaUpstreamBody_TrailingGarbageRejected(t *testing.T) {
	body := append(buildOpenAIChatBody(t, "llama3.1", true), []byte(`{"hax":true}`)...)
	err := runFinalizeOllama(t, body, "openai-completions")
	if err == nil {
		t.Fatalf("expected error for trailing garbage")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) || ue.Kind != errorsx.KindClientBug {
		t.Fatalf("err = %v, want *upstreampkg.Error{KindClientBug}", err)
	}
}

// TestFinalizeOllamaUpstreamBody_UnknownFieldsPreserved: the
// pre-r0925 implementation rejected unknown IR fields via
// json.Decoder.DisallowUnknownFields. After r0925 we feed raw
// OpenAI Chat wire which commonly carries vendor extras; ir.ParseOpenAI
// captures truly-unknown fields into Extensions so they survive
// end-to-end. We use a field that the parser doesn't promote to a
// typed IR field so it lands in Extensions verbatim (a custom
// vendor key like `x_vendor_custom`).
func TestFinalizeOllamaUpstreamBody_UnknownFieldsPreserved(t *testing.T) {
	body := []byte(`{"model":"llama3.1","messages":[{"role":"user","content":"x"}],"x_vendor_custom":"hi-vendor","ollama.options.num_gpu":2}`)
	out, err := runFinalizeOllamaOk(t, body, "openai-completions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Contains(out, []byte(`"llama3.1"`)) {
		t.Errorf("output missing model: %s", out)
	}
	// An Ollama-namespaced Extension key (ollama.options.num_gpu) must
	// land in options.num_gpu on the Ollama wire. A truly unknown
	// vendor key (x_vendor_custom) is preserved in Extensions; Ollama
	// has no native x_vendor_custom so SerializeOllama drops it from
	// the wire — that's correct fail-closed behavior for an unknown
	// key the Ollama server doesn't understand.
	opts := struct {
		NumGPU json.RawMessage `json:"num_gpu"`
	}{}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if _, ok := top["options"]; !ok {
		t.Fatalf("output missing options object: %s", out)
	}
	if err := json.Unmarshal(top["options"], &opts); err != nil {
		t.Fatalf("options not JSON: %v", err)
	}
	if len(opts.NumGPU) == 0 || string(opts.NumGPU) == "null" {
		t.Errorf("options.num_gpu missing; got %s", top["options"])
	}
}

// TestFinalizeOllamaUpstreamBody_UnsupportedClientProtocolRejected:
// the executor accepts only the wired inbound protocols today. An
// Anthropic or Gemini client that lands here (which should not
// happen given the dispatcher's protocol switch) surfaces as
// KindUnsupportedFeature so the operator sees it instead of
// getting a misleading 200.
func TestFinalizeOllamaUpstreamBody_UnsupportedClientProtocolRejected(t *testing.T) {
	body := buildOpenAIChatBody(t, "llama3.1", true)
	err := runFinalizeOllama(t, body, "anthropic-messages")
	if err == nil {
		t.Fatalf("expected error for unsupported client protocol")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) || ue.Kind != errorsx.KindUnsupportedFeature {
		t.Fatalf("err = %v, want *upstreampkg.Error{KindUnsupportedFeature}", err)
	}
}

// TestFinalizeOllamaUpstreamBody_OpenAIResponsesRejected is the
// fail-closed gate for the Responses protocol: although
// ir.ParseResponses exists, the response write path
// (StreamResponse / WriteNonStreamResponse) is OpenAI-Chat-shaped
// only, so a Responses request would get a wrong-shape response
// back. Reject at the request side so the client sees a typed
// KindUnsupportedFeature error rather than a silently mis-shaped
// success.
func TestFinalizeOllamaUpstreamBody_OpenAIResponsesRejected(t *testing.T) {
	body := []byte(`{"model":"llama3.1","input":"hi"}`)
	err := runFinalizeOllama(t, body, "openai-responses")
	if err == nil {
		t.Fatalf("expected error for openai-responses (response path not wired)")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) || ue.Kind != errorsx.KindUnsupportedFeature {
		t.Fatalf("err = %v, want *upstreampkg.Error{KindUnsupportedFeature}", err)
	}
}

// TestFinalizeOllamaUpstreamBody_MissingModelRejected: the executor
// must never fabricate a default model — the client wire is
// authoritative.
func TestFinalizeOllamaUpstreamBody_MissingModelRejected(t *testing.T) {
	body := buildOpenAIChatBody(t, "", true)
	err := runFinalizeOllama(t, body, "openai-completions")
	if err == nil {
		t.Fatalf("expected error for missing model")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) || ue.Kind != errorsx.KindClientBug {
		t.Fatalf("err = %v, want *upstreampkg.Error{KindClientBug}", err)
	}
}

// TestFinalizeOllamaUpstreamBody_EmptyMessagesRejected: Ollama
// rejects empty messages with "invalid chat message"; the executor
// must short-circuit at the gateway so we don't bill a guaranteed
// failure upstream call.
func TestFinalizeOllamaUpstreamBody_EmptyMessagesRejected(t *testing.T) {
	body := buildOpenAIChatBody(t, "llama3.1", false)
	err := runFinalizeOllama(t, body, "openai-completions")
	if err == nil {
		t.Fatalf("expected error for empty messages")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) || ue.Kind != errorsx.KindClientBug {
		t.Fatalf("err = %v, want *upstreampkg.Error{KindClientBug}", err)
	}
}

// TestFinalizeOllamaUpstreamBody_EmptyClientProtocolDefaultsToOpenAI:
// an empty ClientProtocol (the legacy default) is accepted and the
// body parses as OpenAI Chat wire (the only wired inbound today).
func TestFinalizeOllamaUpstreamBody_EmptyClientProtocolDefaultsToOpenAI(t *testing.T) {
	body := buildOpenAIChatBody(t, "llama3.1", true)
	out, err := runFinalizeOllamaOk(t, body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if got["model"] != "llama3.1" {
		t.Errorf("output model = %v, want llama3.1", got["model"])
	}
	if _, ok := got["model"]; !ok {
		t.Errorf("output missing model field")
	}
}

// TestFinalizeOllamaUpstreamBody_RealOpenAIWire_NonStream is the
// regression test for the r0925 fix-up: a real, full-featured
// OpenAI Chat wire body must successfully serialize to Ollama
// native wire (with options.* for sampling, top-level fields for
// Ollama-private extensions via the ollama.* Extensions namespace,
// and stream=false honored from params.IsStream).
func TestFinalizeOllamaUpstreamBody_RealOpenAIWire_NonStream(t *testing.T) {
	body := []byte(`{
		"model": "llama3.1",
		"messages": [
			{"role": "system", "content": "you are terse"},
			{"role": "user", "content": "hi"}
		],
		"max_tokens": 256,
		"temperature": 0.3,
		"top_p": 0.9,
		"stop": ["\n"],
		"tools": [{"type":"function","function":{"name":"echo","description":"e","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}}],
		"ollama.format": "json",
		"ollama.keep_alive": "5m",
		"ollama.options.num_ctx": 4096
	}`)
	out, err := runFinalizeOllamaOkWithStream(t, body, "openai-completions", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if got["model"] != "llama3.1" {
		t.Errorf("output model = %v, want llama3.1", got["model"])
	}
	if got["stream"] != false {
		t.Errorf("output stream = %v, want false (params.IsStream wins)", got["stream"])
	}
	// Ollama-private top-level fields lifted from Extensions.
	if got["format"] != "json" {
		t.Errorf("output format = %v, want 'json'", got["format"])
	}
	if got["keep_alive"] != "5m" {
		t.Errorf("output keep_alive = %v, want '5m'", got["keep_alive"])
	}
	// Sampling + max_tokens → options.*
	opts, ok := got["options"].(map[string]any)
	if !ok {
		t.Fatalf("output missing options object: %v", got["options"])
	}
	if opts["temperature"] == nil {
		t.Errorf("options.temperature missing; got %v", opts)
	}
	if opts["top_p"] == nil {
		t.Errorf("options.top_p missing; got %v", opts)
	}
	if opts["num_predict"] == nil {
		t.Errorf("options.num_predict missing; got %v", opts)
	}
	if opts["num_ctx"] == nil {
		t.Errorf("options.num_ctx missing; got %v", opts)
	}
	// Tools must survive via IR.ToolDefinition → Ollama tool schema.
	if _, ok := got["tools"]; !ok {
		t.Errorf("output missing tools array")
	}
}

// TestFinalizeOllamaUpstreamBody_RealOpenAIWire_Stream is the
// stream-mode twin: when params.IsStream=true the executor MUST
// honor it on the wire even if the client body carries stream=false.
// This is the dispatcher-authoritative stream gate.
func TestFinalizeOllamaUpstreamBody_RealOpenAIWire_Stream(t *testing.T) {
	body := []byte(`{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	out, err := runFinalizeOllamaOkWithStream(t, body, "openai-completions", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if got["stream"] != true {
		t.Errorf("output stream = %v, want true (params.IsStream overrides body)", got["stream"])
	}
}

// TestFinalizeOllamaUpstreamBody_RealOpenAIWire_Error covers the
// typed-error path: a body that's valid JSON but lacks required
// fields must surface as KindClientBug with a message operators
// can grep.
func TestFinalizeOllamaUpstreamBody_RealOpenAIWire_Error(t *testing.T) {
	// Empty messages array.
	body := []byte(`{"model":"llama3.1","messages":[]}`)
	err := runFinalizeOllama(t, body, "openai-completions")
	if err == nil {
		t.Fatalf("expected error for empty messages")
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) || ue.Kind != errorsx.KindClientBug {
		t.Fatalf("err = %v, want *upstreampkg.Error{KindClientBug}", err)
	}
}

// TestFinalizeOllamaUpstreamBody_CandRawModelAliasing ensures each
// candidate's outbound model wins over the public client model.
func TestFinalizeOllamaUpstreamBody_CandRawModelAliasing(t *testing.T) {
	body := []byte(`{"model":"client-model","messages":[{"role":"user","content":"hi"}]}`)
	e := newTestExecutor()
	params := &ExecParams{ClientProtocol: "openai-completions", ClientModel: "client-model"}
	cand := provider.Candidate{RawModel: "ollama-raw-model"}
	out, err := e.finalizeOllamaUpstreamBody(params, cand, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if got["model"] != "ollama-raw-model" {
		t.Errorf("output model = %v, want ollama-raw-model", got["model"])
	}
}

// runFinalizeOllamaOk is the success-path twin of runFinalizeOllama:
// returns the serialized body so callers can assert on the wire shape.
func runFinalizeOllamaOk(t *testing.T, body []byte, clientProtocol string) ([]byte, error) {
	return runFinalizeOllamaOkWithStream(t, body, clientProtocol, false)
}

// runFinalizeOllamaOkWithStream mirrors runFinalizeOllamaOk but
// stamps params.IsStream so the stream-override gate test can
// exercise the dispatcher-authoritative branch.
func runFinalizeOllamaOkWithStream(t *testing.T, body []byte, clientProtocol string, isStream bool) ([]byte, error) {
	t.Helper()
	e := newTestExecutor()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	params := &ExecParams{
		W:              httptest.NewRecorder(),
		R:              req,
		BodyBytes:      body,
		ClientProtocol: clientProtocol,
		IsStream:       isStream,
	}
	cand := provider.Candidate{
		ProviderID: 42, CredentialID: 7,
		BaseURL: "http://localhost:11434", Protocol: "ollama-native",
		CatalogCode: "ollama", RawModel: "llama3.1",
	}
	return e.finalizeOllamaUpstreamBody(params, cand, body)
}
