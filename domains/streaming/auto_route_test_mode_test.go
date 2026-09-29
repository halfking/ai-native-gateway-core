package streaming

// auto_route_test_mode_test.go — X-Gw-Test-Mode header behaviour.
//
// 2026-09-29 (critical re-audit): the first cut of this file pinned an
// actor allowlist on X-Gw-Source-Actor. That gate could never authorise
// the callers it was written for: X-Gw-Source-Actor is listed in
// loopback.CorrelationHeaders, so middleware.StripUntrustedCorrelationHeaders
// deletes it from every request that lacks this process's per-boot
// loopback token. Every EXTERNAL caller (cmd/autoroute-e2e-audit, manual
// curl) therefore evaluated to "unauthorised" and was silently downgraded
// to live — the exact real upstream call the mode exists to avoid. The
// tests below pin the corrected, actually-reachable gate.
//
// Contracts pinned here:
//
//  1. Authorisation is a shared secret (LLM_GW_TEST_MODE_TOKEN), compared
//     in constant time. Unset env ⇒ fail-closed, NOT fail-open.
//  2. An unauthorised caller naming a non-live mode reads as live, so a
//     hostile probe cannot toggle the production code path by guessing a
//     header name/value.
//  3. The mock writers emit protocol-correct bodies for all three wire
//     protocols, in BOTH non-streaming and streaming (SSE) form — a
//     streaming client must never receive a bare JSON object.
//  4. The header survives the gateway's own loopback middleware: this is
//     the regression that made the original gate unreachable.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/middleware"
)

// ── ParseTestMode contract ─────────────────────────────────────────────

func TestParseTestMode_HeaderAbsent_ReturnsLiveNotSet(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"auto"}`))

	mode, set := ParseTestMode(r, true)
	if mode != TestModeLive {
		t.Errorf("mode = %q, want %q", mode, TestModeLive)
	}
	if set {
		t.Errorf("set = true, want false when header is absent")
	}
}

func TestParseTestMode_UnknownValue_DowngradedToLiveNotSet(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"auto"}`))
	r.Header.Set(autoTestModeHeader, "this-mode-does-not-exist")

	mode, set := ParseTestMode(r, true)
	if mode != TestModeLive {
		t.Errorf("mode = %q, want %q", mode, TestModeLive)
	}
	if set {
		t.Errorf("set = true, want false for unknown mode (log row must not record an unparseable value)")
	}
}

func TestParseTestMode_AllModes_AllowedCaller(t *testing.T) {
	cases := []struct {
		header string
		want   TestMode
	}{
		{"mock", TestModeMock},
		{"auto-only", TestModeAutoOnly},
		{"other-only", TestModeOtherOnly},
		{"full", TestModeLive},     // explicit alias for live
		{"MOCK", TestModeMock},     // case-insensitive header
		{"  mock  ", TestModeMock}, // header whitespace trim
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"auto"}`))
			r.Header.Set(autoTestModeHeader, tc.header)

			mode, set := ParseTestMode(r, true)
			if mode != tc.want {
				t.Errorf("mode = %q, want %q", mode, tc.want)
			}
			if tc.want != TestModeLive && !set {
				t.Errorf("set = false, want true for mode %q", tc.want)
			}
		})
	}
}

func TestParseTestMode_UnauthorizedCaller_DowngradedToLive(t *testing.T) {
	// A hostile probe must not be able to toggle the production path by
	// guessing the header name. Live is the only safe default.
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"auto"}`))
	r.Header.Set(autoTestModeHeader, "mock")

	mode, set := ParseTestMode(r, testModeAllowed(r))
	if mode != TestModeLive {
		t.Errorf("mode = %q, want %q (unauthorised caller must read as live)", mode, TestModeLive)
	}
	if set {
		t.Errorf("set = true, want false for unauthorised caller")
	}
}

func TestTestMode_IsMockMode(t *testing.T) {
	cases := []struct {
		mode TestMode
		want bool
	}{
		{TestModeLive, false},
		{TestModeFull, false},
		{TestModeMock, true},
		{TestModeAutoOnly, true},
		{TestModeOtherOnly, false},
	}
	for _, tc := range cases {
		if got := tc.mode.IsMockMode(); got != tc.want {
			t.Errorf("%q.IsMockMode() = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestTestMode_SkipsAutoRoute(t *testing.T) {
	cases := []struct {
		mode TestMode
		want bool
	}{
		{TestModeLive, false},
		{TestModeFull, false},
		{TestModeMock, false},
		{TestModeAutoOnly, false},
		{TestModeOtherOnly, true},
	}
	for _, tc := range cases {
		if got := tc.mode.SkipsAutoRoute(); got != tc.want {
			t.Errorf("%q.SkipsAutoRoute() = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestTestMode_String(t *testing.T) {
	cases := []struct {
		mode TestMode
		want string
	}{
		{TestModeLive, "live"},
		{TestModeMock, "mock"},
		{TestModeAutoOnly, "auto-only"},
		{TestModeOtherOnly, "other-only"},
		{TestModeFull, "full"},
	}
	for _, tc := range cases {
		if got := tc.mode.String(); got != tc.want {
			t.Errorf("%q.String() = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

// ── testModeAllowed — access control (the P0 fix) ──────────────────────

func TestTestModeAllowed_NilRequest(t *testing.T) {
	if testModeAllowed(nil) {
		t.Errorf("nil request must not be allowed")
	}
}

func TestTestModeAllowed_UnsetEnvIsFailClosed(t *testing.T) {
	// The critical fail-closed property: with no configured secret, NO
	// caller may enable a non-live mode. An implementation that returned
	// true here would let any client disable upstream calls.
	t.Setenv(testModeTokenEnv, "")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(testModeTokenHeader, "")
	r.Header.Set(testModeTokenHeader, "anything")
	r.Header.Set(autoSourceActorHeader, "autoroute-e2e-audit")

	if testModeAllowed(r) {
		t.Fatalf("no secret configured ⇒ nobody is authorised; got allowed")
	}
}

func TestTestModeAllowed_CorrectSecretAuthorises(t *testing.T) {
	t.Setenv(testModeTokenEnv, "s3cr3t-value")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(testModeTokenHeader, "s3cr3t-value")

	if !testModeAllowed(r) {
		t.Errorf("matching secret must authorise the test mode")
	}
}

func TestTestModeAllowed_WrongSecretRejected(t *testing.T) {
	t.Setenv(testModeTokenEnv, "s3cr3t-value")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(testModeTokenHeader, "s3cr3t-valuE")

	if testModeAllowed(r) {
		t.Errorf("wrong secret must not authorise the test mode")
	}
}

func TestTestModeAllowed_MissingSecretHeaderRejected(t *testing.T) {
	t.Setenv(testModeTokenEnv, "s3cr3t-value")
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	if testModeAllowed(r) {
		t.Errorf("configured env + no header must not authorise")
	}
}

// TestTestModeAllowed_ActorHeaderAloneNeverAuthorises is the regression
// test for the P0 defect. The first implementation trusted an allowlist
// of X-Gw-Source-Actor values including the audit tool's own actor name.
// Forging that header from outside is exactly what the gateway's loopback
// middleware prevents, so the allowlist was unreachable in production —
// and worse, it LOOKED like it authorised the audit tool in unit tests
// that never traversed the middleware.
func TestTestModeAllowed_ActorHeaderAloneNeverAuthorises(t *testing.T) {
	t.Setenv(testModeTokenEnv, "")
	for _, actor := range []string{
		"autoroute-e2e-audit", "auto-testbench", "manual-probe",
		"auto-title-generator", "auto-summary-generator", "session-summary",
	} {
		t.Run(actor, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Header.Set(autoSourceActorHeader, actor)
			if testModeAllowed(r) {
				t.Errorf("actor %q must not authorise a test mode on its own", actor)
			}
		})
	}
}

func TestTestModeAllowed_EmptyActorsAllowlistIsEmpty(t *testing.T) {
	// The in-process actor allowlist must stay empty unless a real
	// gateway-internal caller needs mock mode. A non-empty list means some
	// actor can silence a real upstream call; keep this assertion so
	// adding an entry is a deliberate, visible act.
	if len(testModeActors) != 0 {
		t.Errorf("testModeActors = %v, want empty; adding an actor lets it bypass upstream calls", testModeActors)
	}
}

// ── Mock response writers — wire shape contract ─────────────────────────

func TestWriteMockChatResponse_OpenAICompatibleShape(t *testing.T) {
	rr := httptest.NewRecorder()
	writeMockChatResponse(rr, TestModeMock, "req-abc-123", "claude-sonnet-4.5", false)

	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rr.Header().Get("X-Gw-Mock-Marker"); got != "mock" {
		t.Errorf("X-Gw-Mock-Marker = %q, want %q", got, "mock")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		ID         string `json:"id"`
		Object     string `json:"object"`
		Model      string `json:"model"`
		MockMarker string `json:"mock_marker"`
		Choices    []struct {
			Index        int            `json:"index"`
			Message      map[string]any `json:"message"`
			FinishReason string         `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body must be a valid OpenAI chat completion, got: %v\nbody=%s", err, rr.Body.String())
	}
	if body.ID != "chatcmpl-mock-req-abc-123" {
		t.Errorf("id = %q, want %q", body.ID, "chatcmpl-mock-req-abc-123")
	}
	if body.Object != "chat.completion" {
		t.Errorf("object = %q, want %q", body.Object, "chat.completion")
	}
	if body.Model != "claude-sonnet-4.5" {
		t.Errorf("model = %q, want %q (chosen model from auto decider)", body.Model, "claude-sonnet-4.5")
	}
	if body.MockMarker != "mock" {
		t.Errorf("mock_marker = %q, want %q", body.MockMarker, "mock")
	}
	if len(body.Choices) != 1 {
		t.Fatalf("choices len = %d, want 1", len(body.Choices))
	}
	if body.Choices[0].Message["role"] != "assistant" {
		t.Errorf("message.role = %v, want assistant", body.Choices[0].Message["role"])
	}
	if cont, _ := body.Choices[0].Message["content"].(string); !strings.Contains(cont, "mock") {
		t.Errorf("message.content must contain a 'mock' marker so test runners can identify it, got %q", cont)
	}
	if body.Usage["mock"] != true {
		t.Errorf("usage.mock = %v, want true", body.Usage["mock"])
	}
}

func TestWriteMockChatResponse_AutoOnlyMode_MarkerMatches(t *testing.T) {
	rr := httptest.NewRecorder()
	writeMockChatResponse(rr, TestModeAutoOnly, "req-1", "minimax-m3", false)

	if got := rr.Header().Get("X-Gw-Mock-Marker"); got != "auto-only" {
		t.Errorf("X-Gw-Mock-Marker = %q, want %q", got, "auto-only")
	}
}

func TestWriteMockMessagesResponse_AnthropicShape(t *testing.T) {
	rr := httptest.NewRecorder()
	writeMockMessagesResponse(rr, TestModeMock, "req-msg-1", "claude-haiku-4.5", false)

	if got := rr.Header().Get("X-Gw-Mock-Marker"); got != "mock" {
		t.Errorf("X-Gw-Mock-Marker = %q, want %q", got, "mock")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("Anthropic-format body must parse, got: %v\nbody=%s", err, rr.Body.String())
	}
	if body["type"] != "message" {
		t.Errorf("type = %v, want 'message'", body["type"])
	}
	if body["model"] != "claude-haiku-4.5" {
		t.Errorf("model = %v, want %q", body["model"], "claude-haiku-4.5")
	}
	if body["mock_marker"] != "mock" {
		t.Errorf("mock_marker = %v, want 'mock'", body["mock_marker"])
	}
	content, ok := body["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content must be a non-empty array, got %T (%v)", body["content"], body["content"])
	}
}

func TestWriteMockResponsesResponse_ResponsesShape(t *testing.T) {
	rr := httptest.NewRecorder()
	writeMockResponsesResponse(rr, TestModeAutoOnly, "req-resp-1", "gpt-5.6", false)

	if got := rr.Header().Get("X-Gw-Mock-Marker"); got != "auto-only" {
		t.Errorf("X-Gw-Mock-Marker = %q, want %q", got, "auto-only")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("Responses-format body must parse, got: %v\nbody=%s", err, rr.Body.String())
	}
	if body["object"] != "response" {
		t.Errorf("object = %v, want 'response'", body["object"])
	}
	if body["status"] != "completed" {
		t.Errorf("status = %v, want 'completed'", body["status"])
	}
	if body["model"] != "gpt-5.6" {
		t.Errorf("model = %v, want %q", body["model"], "gpt-5.6")
	}
	output, ok := body["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("output must be a non-empty array, got %T (%v)", body["output"], body["output"])
	}
}

// ── Streaming (SSE) mock framing ───────────────────────────────────────
//
// A client that sent "stream": true holds an SSE connection open and
// blocks until the protocol's terminal event arrives. Replying with a
// single JSON object leaves such a client hanging until its own timeout.
// Each writer therefore has a stream branch, and each must emit the
// protocol's terminator.

func TestWriteMockChatResponse_Stream_EmitsSSEWithDoneSentinel(t *testing.T) {
	rr := httptest.NewRecorder()
	writeMockChatResponse(rr, TestModeMock, "req-stream-1", "gpt-5.6", true)

	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream for a streaming request", got)
	}
	if got := rr.Header().Get("X-Gw-Mock-Marker"); got != "mock" {
		t.Errorf("X-Gw-Mock-Marker = %q, want mock (marker must survive the stream branch)", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("OpenAI SSE stream must terminate with the [DONE] sentinel; got:\n%s", body)
	}
	if !strings.Contains(body, mockAutoResponseContent) {
		t.Errorf("stream must carry the mock content; got:\n%s", body)
	}
	// A JSON body leaked into an SSE stream would break every SDK parser.
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("stream response must not be a bare JSON object; got:\n%s", body)
	}
}

func TestWriteMockMessagesResponse_Stream_EmitsAnthropicEventSequence(t *testing.T) {
	rr := httptest.NewRecorder()
	writeMockMessagesResponse(rr, TestModeMock, "req-stream-2", "claude-sonnet-4.5", true)

	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	body := rr.Body.String()
	// The Anthropic stream grammar is order-sensitive; an SDK that sees
	// a missing terminator treats the turn as truncated.
	for _, event := range []string{
		"message_start", "content_block_start", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	} {
		if !strings.Contains(body, "event: "+event) {
			t.Errorf("Anthropic SSE stream missing %q event; got:\n%s", event, body)
		}
	}
}

func TestWriteMockResponsesResponse_Stream_EmitsCompletedEvent(t *testing.T) {
	rr := httptest.NewRecorder()
	writeMockResponsesResponse(rr, TestModeMock, "req-stream-3", "gpt-5.6", true)

	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	body := rr.Body.String()
	for _, event := range []string{
		"response.created", "response.output_text.delta", "response.completed",
	} {
		if !strings.Contains(body, "event: "+event) {
			t.Errorf("Responses SSE stream missing %q event; got:\n%s", event, body)
		}
	}
}

// TestMockWriters_StreamAndNonStreamDiffer guards the branch itself: if a
// future edit drops the stream parameter (making every response
// non-streaming), these three assertions fail rather than the bug
// surfacing as a hung client in production.
func TestMockWriters_StreamAndNonStreamDiffer(t *testing.T) {
	nonStream := httptest.NewRecorder()
	writeMockChatResponse(nonStream, TestModeMock, "r", "m", false)
	stream := httptest.NewRecorder()
	writeMockChatResponse(stream, TestModeMock, "r", "m", true)

	if nonStream.Body.String() == stream.Body.String() {
		t.Errorf("stream=true must not produce the non-stream body")
	}
	if nonStream.Header().Get("Content-Type") == stream.Header().Get("Content-Type") {
		t.Errorf("stream=true must change Content-Type (json=%q vs %q)",
			nonStream.Header().Get("Content-Type"), stream.Header().Get("Content-Type"))
	}
}

// ── End-to-end test_mode through request_log context ───────────────────

func TestRequestLogContext_SetTestMode_RoundTrip(t *testing.T) {
	c := &RequestLogContext{}
	if got := c.TestModeValue(); got != "" {
		t.Errorf("default TestModeValue() = %q, want empty", got)
	}

	c.SetTestMode("mock")
	if got := c.TestModeValue(); got != "mock" {
		t.Errorf("TestModeValue() after SetTestMode(mock) = %q, want %q", got, "mock")
	}

	c.SetTestMode("  auto-only  ")
	if got := c.TestModeValue(); got != "auto-only" {
		t.Errorf("TestModeValue() after trim = %q, want %q", got, "auto-only")
	}

	c.SetTestMode("")
	if got := c.TestModeValue(); got != "" {
		t.Errorf("TestModeValue() after clear = %q, want empty", got)
	}
}

func TestRequestLogContext_TestModeValue_NilReceiver(t *testing.T) {
	var c *RequestLogContext
	if got := c.TestModeValue(); got != "" {
		t.Errorf("nil receiver TestModeValue() = %q, want empty", got)
	}
}

// ── Allowed-gate round trip ────────────────────────────────────────────

// TestParseTestMode_RoundTripWithAllowedGate pins that the gate is the only
// thing between a probe and the production code path. Same request, same
// header, two gate values — different outcomes.
func TestParseTestMode_RoundTripWithAllowedGate(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(autoTestModeHeader, "mock")

	if mode, _ := ParseTestMode(r, false); mode != TestModeLive {
		t.Errorf("allowed=false must downgrade to live, got %q", mode)
	}
	if mode, set := ParseTestMode(r, true); mode != TestModeMock || !set {
		t.Errorf("allowed=true must honour header, got %q set=%v", mode, set)
	}
}

// TestTestModeAllowed_EndToEndThroughLoopbackMiddleware proves the header
// the gate depends on actually survives the gateway's own middleware.
//
// This is the check the original test suite was missing entirely: it
// exercised testModeAllowed against a hand-built request that never
// crossed the middleware, so an unreachable gate looked perfectly green.
// Here the real middleware runs first; only the token header (which
// X-Gw-Test-Mode is not) may be stripped.
func TestTestModeAllowed_EndToEndThroughLoopbackMiddleware(t *testing.T) {
	t.Setenv(testModeTokenEnv, "middleware-probe-secret")

	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(testModeTokenHeader); got != "middleware-probe-secret" {
			t.Errorf("X-Gw-Test-Mode-Token was stripped or altered by the middleware: %q", got)
		}
		if !testModeAllowed(r) {
			t.Errorf("authorised caller must stay authorised after the middleware chain")
		}
	})
	srv := httptest.NewServer(middleware.NewRequestIDMiddleware().Wrap(inner))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"auto"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(autoTestModeHeader, "mock")
	req.Header.Set(testModeTokenHeader, "middleware-probe-secret")
	// A forged actor header, as an external attacker would send.
	req.Header.Set(autoSourceActorHeader, "autoroute-e2e-audit")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	//nolint:errcheck
	defer resp.Body.Close()
}
