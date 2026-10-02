package executors

// responses_mode_bridge_wiring_test.go — drives executeOpenAI against a fake
// upstream that behaves like vapEUR: /chat/completions refuses the model with
// code 4006, /v1/responses answers 200. Before the bridge, this ended in
// "all N candidates failed"; after it, the chat-shaped client gets a
// chat.completion body.
//
// Every upstream path touched is recorded so a test can assert WHICH wire
// format was used — a green result alone would not prove the bridge ran rather
// than the retry having been skipped for some other reason.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // matches executor_prestream_test.go
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// bridgeCandidate is an openai-responses provider with the native-Responses
// capability OFF — the real vapEUR / credential 126 state, and the state that
// sends the chat entrypoint down the /chat/completions leg.
func bridgeCandidate(baseURL, rawModel string) provider.Candidate {
	return provider.Candidate{
		CredentialID:      126,
		ProviderID:        36,
		BaseURL:           baseURL,
		Protocol:          "openai-responses",
		CatalogCode:       "vapeur",
		RawModel:          rawModel,
		APIKey:            "sk-bridge-test",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
	}
}

type pathRecorder struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
}

func (p *pathRecorder) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.paths = append(p.paths, r.URL.Path)
	p.bodies = append(p.bodies, strings.TrimSpace(string(body)))
	p.mu.Unlock()
}

func (p *pathRecorder) snapshot() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.paths...), append([]string(nil), p.bodies...)
}

func (p *pathRecorder) hitResponses() bool {
	paths, _ := p.snapshot()
	for _, s := range paths {
		if strings.HasSuffix(s, "/responses") {
			return true
		}
	}
	return false
}

func TestExecuteOpenAI_ChatLegRejected_FallsBackToResponses(t *testing.T) {
	rec := &pathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			// vapEUR's real refusal for a Responses-only model.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(upstreamChatUnsupportedBody))
		case strings.HasSuffix(r.URL.Path, "/responses"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(upstreamCodexResponsesBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	clientRec := httptest.NewRecorder()
	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              clientRec,
		BodyBytes:      []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"say hi"}],"max_tokens":32}`),
		ClientProtocol: "openai-completions",
		ClientModel:    "gpt-5.3-codex",
		ClientID:       identity.ClientIdentity{IdentityHash: "bridge-test"},
	}, bridgeCandidate(upstream.URL, "gpt-5.3-codex"), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v, want the responses fallback to succeed", err)
	}

	paths, bodies := rec.snapshot()
	if len(paths) < 2 {
		t.Fatalf("upstream calls = %v, want a chat attempt then a responses attempt", paths)
	}
	if !strings.HasSuffix(paths[0], "/chat/completions") {
		t.Fatalf("first attempt = %q, want the original chat leg", paths[0])
	}
	if !strings.HasSuffix(paths[1], "/responses") {
		t.Fatalf("second attempt = %q, want the /v1/responses leg", paths[1])
	}
	if !strings.Contains(bodies[0], `"say hi"`) {
		t.Fatalf("first attempt lost the user text: %q", bodies[0])
	}
	// The retried body must be Responses-shaped, not the chat body re-sent.
	if !strings.Contains(bodies[1], `"input"`) {
		t.Fatalf("responses attempt did not carry a converted body: %q", bodies[1])
	}
	if strings.Contains(bodies[1], `"messages"`) {
		t.Fatalf("responses attempt still carried chat messages[]: %q", bodies[1])
	}
	if !strings.Contains(bodies[1], `"gpt-5.3-codex"`) {
		t.Fatalf("responses attempt lost the outbound model name: %q", bodies[1])
	}

	// The client asked /v1/chat/completions, so it must get a chat completion —
	// never the raw Responses envelope.
	out := clientRec.Body.String()
	if out == "" {
		t.Fatal("client received no body")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("client body is not JSON: %v (%s)", err, out)
	}
	if got["object"] != "chat.completion" {
		t.Fatalf("client object = %v, want chat.completion", got["object"])
	}
	if got["model"] != "gpt-5.3-codex" {
		t.Fatalf("client model = %v, want the requested model echoed back", got["model"])
	}
	choices, _ := got["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("client choices = %v, want exactly 1", got["choices"])
	}
	ch, _ := choices[0].(map[string]any)
	msg, _ := ch["message"].(map[string]any)
	if msg == nil {
		t.Fatalf("client choices[0].message missing: %s", out)
	}
	if txt, _ := msg["content"].(string); !strings.Contains(txt, "Hi") {
		t.Fatalf("client message.content = %q, want the upstream text", txt)
	}
}

// A chat leg rejected for a reason unrelated to the wire format must not spend a
// second upstream call on the other format — that would mask a request-shape
// bug and hide it from the probe.
func TestExecuteOpenAI_ChatLegParamRejection_DoesNotFallback(t *testing.T) {
	rec := &pathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: 'messages'"}}`))
	}))
	defer upstream.Close()

	_, _ = newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              httptest.NewRecorder(),
		BodyBytes:      []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		ClientProtocol: "openai-completions",
		ClientModel:    "m",
		ClientID:       identity.ClientIdentity{IdentityHash: "bridge-param-test"},
	}, bridgeCandidate(upstream.URL, "m"), 0, time.Now(), nil)

	if rec.hitResponses() {
		t.Fatal("a parameter-shaped rejection must not trigger a /responses retry")
	}
}

// For a provider that is NOT declared openai-responses, a bare "operation
// unsupported" verdict carries no "the other wire format would work" meaning.
func TestExecuteOpenAI_ChatLegFallbackGatedOnProviderProtocol(t *testing.T) {
	rec := &pathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(upstreamChatUnsupportedBody))
	}))
	defer upstream.Close()

	cand := bridgeCandidate(upstream.URL, "m")
	cand.Protocol = "openai-completions" // not responses-native

	_, _ = newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              httptest.NewRecorder(),
		BodyBytes:      []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		ClientProtocol: "openai-completions",
		ClientModel:    "m",
		ClientID:       identity.ClientIdentity{IdentityHash: "bridge-gate-test"},
	}, cand, 0, time.Now(), nil)

	if rec.hitResponses() {
		t.Fatal("a non-responses provider must never get a /responses retry")
	}
}

// The bridge must be entered at most once per request. The ordinary retry
// budget may legitimately re-send the SAME leg (that is normal retry
// behaviour), but the request must never flip back to the chat leg after it
// upgraded — that would be an unbounded alternating loop.
func TestExecuteOpenAI_ChatLegFallbackDoesNotLoop(t *testing.T) {
	rec := &pathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusBadRequest)
		// Both legs refuse with the same verdict.
		_, _ = w.Write([]byte(upstreamChatUnsupportedBody))
	}))
	defer upstream.Close()

	_, _ = newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              httptest.NewRecorder(),
		BodyBytes:      []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		ClientProtocol: "openai-completions",
		ClientModel:    "m",
		ClientID:       identity.ClientIdentity{IdentityHash: "bridge-loop-test"},
	}, bridgeCandidate(upstream.URL, "m"), 0, time.Now(), nil)

	paths, _ := rec.snapshot()
	sawResponses := false
	for i, p := range paths {
		switch {
		case strings.HasSuffix(p, "/responses"):
			sawResponses = true
		case strings.HasSuffix(p, "/chat/completions"):
			if sawResponses {
				t.Fatalf("request returned to the chat leg after upgrading: %v (call %d)", paths, i)
			}
		}
	}
	if !sawResponses {
		t.Fatalf("bridge never ran; upstream calls = %v", paths)
	}
}

// The streaming half: a chat client whose model only exists on /responses must
// receive chat SSE, not Responses SSE and not an error.
func TestExecuteOpenAI_ChatStreamLegRejected_FallsBackToResponsesStream(t *testing.T) {
	rec := &pathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(upstreamChatUnsupportedBody))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(vapEURTextStream))
	}))
	defer upstream.Close()

	clientRec := httptest.NewRecorder()
	e := newOverloadTestExecutor()
	// The shared test executor's StreamChat stub performs a SINGLE Read of
	// 4096 bytes. The bridge writes chat frames through an io.Pipe, so one
	// Read legitimately returns only the first frame and the assertion would
	// be measuring the stub, not the bridge. Install a draining stub, which is
	// what the production chat stream reader does.
	e.StreamChat = func(_ context.Context, w http.ResponseWriter, resp *http.Response, _, _, _ string, _ NormalizerFunc, _ *audit.StreamCapture, _ bool) StreamOutcome {
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(w, resp.Body)
		return StreamOutcome{}
	}
	_, err := e.executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              clientRec,
		BodyBytes:      []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"stream":true}`),
		ClientProtocol: "openai-completions",
		ClientModel:    "gpt-5.3-codex",
		IsStream:       true,
		ClientID:       identity.ClientIdentity{IdentityHash: "bridge-stream-test"},
	}, bridgeCandidate(upstream.URL, "gpt-5.3-codex"), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v, want the streaming responses fallback to succeed", err)
	}

	paths, _ := rec.snapshot()
	if !rec.hitResponses() {
		t.Fatalf("streaming bridge never reached /responses: %v", paths)
	}
	out := clientRec.Body.String()
	if !strings.Contains(out, "chat.completion.chunk") {
		t.Fatalf("client did not receive chat SSE chunks: %q", out)
	}
	if strings.Contains(out, "response.output_text.delta") {
		t.Fatalf("client received raw Responses SSE frames: %q", out)
	}
	// The upstream split the text across two deltas, so assemble the client's
	// frames rather than searching for a contiguous substring.
	var text strings.Builder
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "[DONE]" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			t.Fatalf("client frame is not JSON: %v (%s)", err, payload)
		}
		choices, _ := frame["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		ch, _ := choices[0].(map[string]any)
		delta, _ := ch["delta"].(map[string]any)
		if s, _ := delta["content"].(string); s != "" {
			text.WriteString(s)
		}
	}
	if text.String() != "Hi there" {
		t.Fatalf("client stream text = %q, want %q", text.String(), "Hi there")
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("client stream was not terminated with [DONE]: %q", out)
	}
}

// A Responses client whose model only lives on /responses must keep receiving
// RESPONSES SSE.
//
// This is the regression for a real contract violation found in the 2026-10-02
// audit: the bridge originally down-converted the reply to chat for every
// client, so a /v1/responses client received chat.completion.chunk frames, the
// responses handler could not find a terminal frame, and the request died with
// "response.failed / All providers unavailable". The matrix had recorded that
// case as a pass because the HTTP status was still 200 — the failure was only
// visible in the body and in the gateway's own success flag.
func TestExecuteOpenAI_ResponsesClient_KeepsResponsesStreamOnBridge(t *testing.T) {
	rec := &pathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(upstreamChatUnsupportedBody))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(vapEURTextStream))
	}))
	defer upstream.Close()

	responsesBody := []byte(`{"model":"gpt-5.3-codex","input":"say hi","max_output_tokens":32,"stream":true}`)
	clientRec := httptest.NewRecorder()
	e := newOverloadTestExecutor()
	e.StreamChat = func(_ context.Context, w http.ResponseWriter, resp *http.Response, _, _, _ string, _ NormalizerFunc, _ *audit.StreamCapture, _ bool) StreamOutcome {
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(w, resp.Body)
		return StreamOutcome{}
	}
	// Native passthrough is the correct renderer for a Responses client.
	e.NativeResponsesStream = func(_ context.Context, w http.ResponseWriter, resp *http.Response, _ string, _ *audit.StreamCapture, _ *atomic.Bool) StreamOutcome {
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(w, resp.Body)
		return StreamOutcome{}
	}

	_, err := e.executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		W:                  clientRec,
		BodyBytes:          []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"say hi"}]}`),
		ResponsesBodyBytes: responsesBody,
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-5.3-codex",
		IsStream:           true,
		ClientID:           identity.ClientIdentity{IdentityHash: "bridge-resp-client"},
	}, bridgeCandidate(upstream.URL, "gpt-5.3-codex"), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}

	paths, bodies := rec.snapshot()
	if !rec.hitResponses() {
		t.Fatalf("bridge never reached /responses: %v", paths)
	}
	// The retried request must be the client's own Responses body, not a
	// chat→responses conversion of it.
	var sent string
	for i, p := range paths {
		if strings.HasSuffix(p, "/responses") {
			sent = bodies[i]
			break
		}
	}
	if !strings.Contains(sent, `"input"`) {
		t.Fatalf("responses attempt did not carry a Responses body: %q", sent)
	}
	if strings.Contains(sent, `"messages"`) {
		t.Fatalf("responses attempt carried chat messages[]: %q", sent)
	}

	out := clientRec.Body.String()
	if strings.Contains(out, "chat.completion.chunk") {
		t.Fatalf("a Responses client received chat SSE frames: %q", out)
	}
	if !strings.Contains(out, "response.output_text.delta") {
		t.Fatalf("a Responses client did not receive Responses SSE frames: %q", out)
	}
	if !strings.Contains(out, "response.completed") {
		t.Fatalf("Responses stream lacks its terminal event: %q", out)
	}
}
