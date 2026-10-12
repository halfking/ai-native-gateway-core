package executors

// proactive_responses_upgrade_test.go — gates the proactive chat→responses
// upgrade added in R-vapeur4 (2026-10-12) so a regression in either direction
// is loud:
//
//   - false-positive: turning chat→responses for a binding that has NOT been
//     verified (or whose capability was unknown / explicitly false) silently
//     rots every chat-shaped request to a doomed Responses call. That is the
//     reactive-bridge inversion the tests must catch.
//   - false-negative: failing to upgrade a verified binding makes the chat→
//     responses behaviour identical to the original "always chat first"
//     pattern, which is the bug R-vapeur4 exists to fix.
//
// Each test binds to one gate; mutation verification (see testMutationGuide
// below) is done by toggling the corresponding line in the helper and
// re-running `go test ./domains/streaming/executors/...`.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// chatShapedClientBody is the smallest body that survives ir.ParseOpenAI +
// ir.SerializeResponsesRequest end-to-end. content "say hi" is the same shape
// the existing bridge_test fixtures use, so a green here aligns with the
// 2026-10-02 round-3 fixtures.
const chatShapedClientBody = `{"model":"grok-4.6","messages":[{"role":"user","content":"say hi"}],"max_tokens":32}`

// upstreamResponsesOK is the minimum Responses object that
// responsesBodyToChatBody will accept on the success path: object=="response"
// with a non-empty output item so the converted reply carries content. The
// shape is what a production Responses call returns (verified by
// credential_model_capabilities.evidence_json for vapor/grok-4.6).
const upstreamResponsesOK = `{"id":"resp_test","object":"response","created_at":1,"status":"completed","model":"grok-4.6","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hi"}]}]}`

type proactivePathRecorder struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
}

func (p *proactivePathRecorder) record(r *http.Request) {
	buf := make([]byte, 4096)
	n, _ := r.Body.Read(buf)
	p.mu.Lock()
	p.paths = append(p.paths, r.URL.Path)
	p.bodies = append(p.bodies, strings.TrimSpace(string(buf[:n])))
	p.mu.Unlock()
}

func (p *proactivePathRecorder) snapshot() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.paths...), append([]string(nil), p.bodies...)
}

// verifiedVapeurCandidate returns an openai-responses candidate with a
// positive, KNOWN capability row. This is the canonical state for credential
// 126 / vapor / grok-4.6 in production per the 2026-10-02 round-3 audit.
func verifiedVapeurCandidate(baseURL, rawModel string) provider.Candidate {
	return provider.Candidate{
		CredentialID:                 126,
		ProviderID:                   36,
		BaseURL:                      baseURL,
		Protocol:                     "openai-responses",
		CatalogCode:                  "vapeur",
		RawModel:                     rawModel,
		APIKey:                       "sk-proactive-test",
		Routable:                     true,
		LifecycleStatus:              "active",
		AvailabilityState:            "ready",
		QuotaState:                   "ok",
		CircuitState:                 "closed",
		SupportsNativeResponses:      true,
		SupportsNativeResponsesKnown: true,
	}
}

// TestProactiveChatToResponsesUpgrade_HappyPath verifies the core behaviour:
// a chat-shape client to a VERIFIED-true binding goes to /v1/responses on the
// very first attempt, with a Responses-shaped body that no longer carries the
// chat `messages` field. The reply is converted back to chat.completion so the
// chat-shape client receives the envelope it asked for.
func TestProactiveChatToResponsesUpgrade_HappyPath(t *testing.T) {
	rec := &proactivePathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("upstream first attempt went to %q, want /v1/responses (proactive)", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamResponsesOK))
	}))
	defer upstream.Close()

	clientRec := httptest.NewRecorder()
	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              clientRec,
		BodyBytes:      []byte(chatShapedClientBody),
		ClientProtocol: "openai-completions",
		ClientModel:    "grok-4.6",
		ClientID:       identity.ClientIdentity{IdentityHash: "proactive-happy"},
	}, verifiedVapeurCandidate(upstream.URL, "grok-4.6"), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v, want proactive upgrade to succeed", err)
	}

	paths, bodies := rec.snapshot()
	if len(paths) != 1 {
		t.Fatalf("upstream calls = %d (%v), want exactly 1 — proactive means no chat attempt", len(paths), paths)
	}
	if !strings.HasSuffix(paths[0], "/responses") {
		t.Fatalf("first attempt = %q, want /v1/responses", paths[0])
	}
	if strings.Contains(bodies[0], `"messages"`) {
		t.Fatalf("responses attempt still carried chat messages[]: %q", bodies[0])
	}
	if !strings.Contains(bodies[0], `"input"`) {
		t.Fatalf("responses attempt did not carry a converted Responses body: %q", bodies[0])
	}
	if !strings.Contains(bodies[0], `"grok-4.6"`) {
		t.Fatalf("responses attempt lost the outbound model name: %q", bodies[0])
	}

	// The chat-shape client must see a chat.completion envelope, not the raw
	// Responses object — without it the chat client would crash on the response
	// shape.
	out := clientRec.Body.String()
	if !strings.Contains(out, `"object":"chat.completion"`) {
		t.Fatalf("client did not receive a chat.completion envelope: %s", out)
	}
	if strings.Contains(out, `"object":"response"`) {
		t.Fatalf("client received raw Responses envelope: %s", out)
	}
}

// TestProactiveChatToResponsesUpgrade_NoChatLegCall is the load-bearing
// assertion for R-vapeur4: no /v1/chat/completions upstream call should happen
// at all when the binding has been verified. A single chat attempt is
// exactly the regression we are fixing.
func TestProactiveChatToResponsesUpgrade_NoChatLegCall(t *testing.T) {
	rec := &proactivePathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("upstream received a chat attempt for a verified binding: %s — this is exactly R-vapeur4", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"chat leg is not allowed for verified binding"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamResponsesOK))
	}))
	defer upstream.Close()

	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              httptest.NewRecorder(),
		BodyBytes:      []byte(chatShapedClientBody),
		ClientProtocol: "openai-completions",
		ClientModel:    "grok-4.6",
		ClientID:       identity.ClientIdentity{IdentityHash: "proactive-nochat"},
	}, verifiedVapeurCandidate(upstream.URL, "grok-4.6"), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v, want success on verified binding", err)
	}

	paths, _ := rec.snapshot()
	for i, p := range paths {
		if strings.HasSuffix(p, "/chat/completions") {
			t.Fatalf("call %d = %q, chat leg must not run on a verified binding", i, p)
		}
	}
}

// TestProactiveChatToResponsesUpgrade_ReactiveBridgeStillFallsBackToChat
// covers the safety net: if the proactive Responses call itself returns a
// "Responses API not supported" verdict (capability row is stale / wrong),
// the existing reactive bridge must still flip the request to chat so a wrong
// row cannot deadlock the request.
func TestProactiveChatToResponsesUpgrade_ReactiveBridgeStillFallsBackToChat(t *testing.T) {
	rec := &proactivePathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case strings.HasSuffix(r.URL.Path, "/responses"):
			// Stale-capability simulation: upstream rejects responses
			// even though our row says true. The body mentions "responses api"
			// with negation so providercap.ResponsesUnsupportedError matches,
			// which is what tryUnsupportedResponsesFallback (the
			// responses→chat half) is gated on after R-vapeur4.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"The Responses API is not supported on this model.","code":4006}}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"grok-4.6","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}
	}))
	defer upstream.Close()

	clientRec := httptest.NewRecorder()
	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              clientRec,
		BodyBytes:      []byte(chatShapedClientBody),
		ClientProtocol: "openai-completions",
		ClientModel:    "grok-4.6",
		ClientID:       identity.ClientIdentity{IdentityHash: "proactive-fallback"},
	}, verifiedVapeurCandidate(upstream.URL, "grok-4.6"), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v, want reactive fallback to chat to succeed", err)
	}

	paths, _ := rec.snapshot()
	if len(paths) < 2 {
		t.Fatalf("upstream calls = %d (%v), want responses attempt + chat fallback", len(paths), paths)
	}
	if !strings.HasSuffix(paths[0], "/responses") {
		t.Fatalf("first attempt = %q, want proactive /responses", paths[0])
	}
	if !strings.HasSuffix(paths[1], "/chat/completions") {
		t.Fatalf("second attempt = %q, want reactive /chat/completions fallback", paths[1])
	}

	out := clientRec.Body.String()
	if !strings.Contains(out, `"object":"chat.completion"`) {
		t.Fatalf("client did not receive chat envelope after fallback: %s", out)
	}
}

// TestProactiveChatToResponsesUpgrade_NotForUnknownCapability: an
// UNKNOWN-capability binding (no row, or a row that has not been read yet)
// must NOT be upgraded. Otherwise we are making the same "trust the
// capability" mistake at every request — and a missing capability must fall
// back to the original chat-leg-first behaviour, which is what the 2026-09-25
// "ability is irrelevant" comment in executor_chat.go documents.
//
// Mutation target: remove the SupportsNativeResponsesKnown clause from
// proactiveChatToResponsesUpgrade — this test should then fail.
func TestProactiveChatToResponsesUpgrade_NotForUnknownCapability(t *testing.T) {
	rec := &proactivePathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"grok-4.6","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	c := verifiedVapeurCandidate(upstream.URL, "grok-4.6")
	c.SupportsNativeResponsesKnown = false // <-- gate condition

	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              httptest.NewRecorder(),
		BodyBytes:      []byte(chatShapedClientBody),
		ClientProtocol: "openai-completions",
		ClientModel:    "grok-4.6",
		ClientID:       identity.ClientIdentity{IdentityHash: "proactive-unknown"},
	}, c, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}

	paths, _ := rec.snapshot()
	if len(paths) != 1 {
		t.Fatalf("upstream calls = %d (%v), want exactly 1 (chat leg first)", len(paths), paths)
	}
	if !strings.HasSuffix(paths[0], "/chat/completions") {
		t.Fatalf("first attempt = %q, want /v1/chat/completions (capability unknown)", paths[0])
	}
}

// TestProactiveChatToResponsesUpgrade_NotForNegativeCapability: an EXPLICIT
// negative capability (durable verdict says upstream rejects /v1/responses)
// must NOT trigger the proactive upgrade. Otherwise the user-visible
// regression is "every chat request now always errors" — a worse failure
// than the bug we are fixing.
//
// Mutation target: remove the SupportsNativeResponses clause from
// proactiveChatToResponsesUpgrade — this test should then fail.
func TestProactiveChatToResponsesUpgrade_NotForNegativeCapability(t *testing.T) {
	rec := &proactivePathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"grok-4.6","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	c := verifiedVapeurCandidate(upstream.URL, "grok-4.6")
	c.SupportsNativeResponses = false // <-- gate condition: known but false

	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              httptest.NewRecorder(),
		BodyBytes:      []byte(chatShapedClientBody),
		ClientProtocol: "openai-completions",
		ClientModel:    "grok-4.6",
		ClientID:       identity.ClientIdentity{IdentityHash: "proactive-negative"},
	}, c, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}

	paths, _ := rec.snapshot()
	if len(paths) != 1 {
		t.Fatalf("upstream calls = %d (%v), want exactly 1 (chat leg, never responses)", len(paths), paths)
	}
	if !strings.HasSuffix(paths[0], "/chat/completions") {
		t.Fatalf("first attempt = %q, want /v1/chat/completions (negative capability)", paths[0])
	}
}

// TestProactiveChatToResponsesUpgrade_NotForOpenAICompletionsProtocol: a
// provider whose catalog protocol is openai-completions (chat-only) must
// not trigger the proactive upgrade helper at all. The helper is the
// gatekeeper, so this test calls it directly rather than going through the
// executor (the chat-completions protocol already routes through the chat
// leg regardless — the helper itself just refuses to act).
//
// Mutation target: remove the Protocol check from
// proactiveChatToResponsesUpgrade — this test should then fail (helper
// returns converted body, falsely).
func TestProactiveChatToResponsesUpgrade_NotForOpenAICompletionsProtocol(t *testing.T) {
	c := verifiedVapeurCandidate("https://api.example.invalid/v1", "grok-4.6")
	c.Protocol = "openai-completions" // <-- gate condition: chat-only provider

	body, upgraded := proactiveChatToResponsesUpgrade(c, &ExecParams{
		BodyBytes:      []byte(chatShapedClientBody),
		ClientProtocol: "openai-completions",
		ClientModel:    "grok-4.6",
	})
	if upgraded {
		t.Fatalf("proactive upgrade fired for chat-only provider (protocol=%q)", c.Protocol)
	}
	if body != nil {
		t.Fatalf("proactive upgrade produced a body for chat-only provider: len=%d", len(body))
	}
}

// TestProactiveChatToResponsesUpgrade_NotForResponsesClient: a genuine
// /v1/responses client must keep its own body — converting its bytes via
// chatBodyToResponsesBody would corrupt the request.
//
// The end-to-end behaviour here is captured by two cooperating gates:
// `params.ClientProtocol == openai-responses` and `len(params.ResponsesBodyBytes)
// > 0`. Removing only the ResponsesBodyBytes gate is not directly mutation-
// testable end-to-end (the ClientProtocol sibling gate catches the same
// /v1/responses client case), so this test guards the end-to-end contract.
// The ResponsesBodyBytes gate itself is kept as defense-in-depth: the two
// flags are set together today, but if the handler ever sets one without the
// other (e.g. for an internal re-dispatch path), the ResponsesBodyBytes gate
// stops the helper from corrupting the client's own body.
func TestProactiveChatToResponsesUpgrade_NotForResponsesClient(t *testing.T) {
	rec := &proactivePathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamResponsesOK))
	}))
	defer upstream.Close()

	clientResponsesBody := []byte(`{"model":"grok-4.6","input":"say hi","max_output_tokens":32}`)

	clientRec := httptest.NewRecorder()
	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		W:                  clientRec,
		BodyBytes:          []byte(chatShapedClientBody),
		ResponsesBodyBytes: clientResponsesBody,
		ClientProtocol:     "openai-responses",
		ClientModel:        "grok-4.6",
		ClientID:           identity.ClientIdentity{IdentityHash: "proactive-respclient"},
	}, verifiedVapeurCandidate(upstream.URL, "grok-4.6"), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}

	paths, bodies := rec.snapshot()
	for i, p := range paths {
		if strings.HasSuffix(p, "/responses") {
			sent := bodies[i]
			if !strings.Contains(sent, `"input":"say hi"`) {
				t.Fatalf("call %d sent a non-client body to /responses: %q (input was overwritten)", i, sent)
			}
			if strings.Contains(sent, `"messages"`) {
				t.Fatalf("call %d sent a chat body to /responses: %q (overwrote client body)", i, sent)
			}
		}
	}
}

// TestProactiveChatToResponsesUpgrade_StreamRequiresStreamCapability:
// streaming needs its OWN capability row. A binding with non-stream
// SupportsNativeResponses=true but SupportsNativeResponsesStream=false must
// NOT trigger a proactive /v1/responses call when the client asked for
// stream=true — the upstream would reject SSE on a chat-only wire format.
//
// Mutation target: remove the params.IsStream && !SupportsNativeResponsesStream
// gate from proactiveChatToResponsesUpgrade — this test should then fail.
func TestProactiveChatToResponsesUpgrade_StreamRequiresStreamCapability(t *testing.T) {
	rec := &proactivePathRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	c := verifiedVapeurCandidate(upstream.URL, "grok-4.6")
	// Non-stream capability verified, stream capability NOT verified.
	// SupportsNativeResponsesStream is a plain bool with no "known" flag
	// (per audit 2026-10-02 §五 #5: stream capability is only ever set
	// manually, so its zero value is "not verified"). Leaving it false here
	// is exactly the gate condition we want to test.
	_ = c // explicit no-op: verifiedVapeurCandidate already has SupportsNativeResponsesStream=false

	streamBody := []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              httptest.NewRecorder(),
		BodyBytes:      streamBody,
		ClientProtocol: "openai-completions",
		ClientModel:    "grok-4.6",
		IsStream:       true,
		ClientID:       identity.ClientIdentity{IdentityHash: "proactive-stream-gate"},
	}, c, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}

	paths, _ := rec.snapshot()
	for i, p := range paths {
		if strings.HasSuffix(p, "/responses") {
			t.Fatalf("call %d = %q, /v1/responses must not run on a stream-incapable binding", i, p)
		}
	}
}
