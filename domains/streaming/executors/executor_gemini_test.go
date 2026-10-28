package executors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // matches executor.go
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// ────────────────────────────────────────────────────────────────────────
// Test helpers
// ────────────────────────────────────────────────────────────────────────

// newGeminiCandidate builds a minimal candidate for a Gemini-native
// provider.
func newGeminiCandidate(baseURL string) provider.Candidate {
	return provider.Candidate{
		ProviderID:   77,
		CredentialID: 9,
		BaseURL:      baseURL,
		Protocol:     "gemini-generate",
		CatalogCode:  "google-gemini",
		APIKey:       "AIza-test-key",
		RawModel:     "gemini-2.5-flash",
	}
}

// newGeminiTestExecutor builds a minimal *Executor able to drive a real
// HTTP round-trip against an httptest server.
func newGeminiTestExecutor() *Executor {
	return &Executor{
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}
}

// ────────────────────────────────────────────────────────────────────────
// BuildRequest
// ────────────────────────────────────────────────────────────────────────

// TestGeminiExecutor_BuildRequest_NativePathAndAuth pins the URL and
// credential contract of the Gemini native executor:
//
//   - path is the model-scoped native path
//     "/v1beta/models/{MODEL}:generateContent"
//   - the credential travels in the x-goog-api-key header
//   - the Authorization header is NOT set (one credential, one form)
func TestGeminiExecutor_BuildRequest_NativePathAndAuth(t *testing.T) {
	ge := &GeminiExecutor{Model: "gemini-2.5-flash"}
	cand := newGeminiCandidate("https://generativelanguage.googleapis.com")
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	req, err := ge.BuildRequest(cand, body, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if got, want := req.URL.Path, "/v1beta/models/gemini-2.5-flash:generateContent"; got != want {
		t.Errorf("upstream path = %q, want %q", got, want)
	}
	if got := req.Header.Get("x-goog-api-key"); got != "AIza-test-key" {
		t.Errorf("x-goog-api-key = %q, want the credential", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty (the credential has exactly one form)", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// ★ Reverse pin: the credential must NEVER reach the URL.
//
// The vendor API also accepts "?key=<key>", and the in-repo design note
// (docs/供应商协议优化.md §8.3) lists that form as an option. A query
// credential lands in every access log, every upstream-URL log line and
// every telemetry row that records the request URL ⇒ this pin is the
// difference between "the vendor accepts it" and "we leak it".
func TestGeminiExecutor_BuildRequest_CredentialNeverInURL(t *testing.T) {
	ge := &GeminiExecutor{Model: "gemini-2.5-flash"}
	cand := newGeminiCandidate("https://generativelanguage.googleapis.com")

	req, err := ge.BuildRequest(cand, []byte(`{}`), false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if strings.Contains(req.URL.String(), "AIza-test-key") {
		t.Errorf("credential leaked into the upstream URL: %s", req.URL.String())
	}
	if req.URL.RawQuery != "" {
		t.Errorf("unexpected query string on the upstream URL: %q", req.URL.RawQuery)
	}
}

// TestGeminiExecutor_BuildRequest_StreamActionAndAltSSE pins the
// streaming URL: the action flips to ":streamGenerateContent" and the
// vendor's "?alt=sse" framing selector is present (docs/vendor-formats/
// gemini.md §HTTP 形态).
func TestGeminiExecutor_BuildRequest_StreamActionAndAltSSE(t *testing.T) {
	ge := &GeminiExecutor{Model: "gemini-2.5-flash"}
	cand := newGeminiCandidate("https://generativelanguage.googleapis.com")

	req, err := ge.BuildRequest(cand, []byte(`{}`), true)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if got, want := req.URL.Path, "/v1beta/models/gemini-2.5-flash:streamGenerateContent"; got != want {
		t.Errorf("upstream path = %q, want %q", got, want)
	}
	if got := req.URL.Query().Get("alt"); got != "sse" {
		t.Errorf("alt query = %q, want sse", got)
	}
	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
}

// TestGeminiExecutor_BuildRequest_FallsBackToCandidateModel pins that
// the URL still carries a model segment when the executor has no
// explicit outbound model (the candidate's raw model is the fallback).
func TestGeminiExecutor_BuildRequest_FallsBackToCandidateModel(t *testing.T) {
	ge := &GeminiExecutor{}
	cand := newGeminiCandidate("https://generativelanguage.googleapis.com")

	req, err := ge.BuildRequest(cand, []byte(`{}`), false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if got, want := req.URL.Path, "/v1beta/models/gemini-2.5-flash:generateContent"; got != want {
		t.Errorf("upstream path = %q, want %q", got, want)
	}
}

// TestGeminiExecutor_BuildRequest_FailsClosed covers the two inputs that
// would otherwise produce a malformed upstream URL.
func TestGeminiExecutor_BuildRequest_FailsClosed(t *testing.T) {
	t.Run("empty base url", func(t *testing.T) {
		ge := &GeminiExecutor{Model: "gemini-2.5-flash"}
		cand := newGeminiCandidate("")
		if _, err := ge.BuildRequest(cand, []byte(`{}`), false); err == nil {
			t.Fatal("expected an error for an empty base URL")
		}
	})
	t.Run("empty model", func(t *testing.T) {
		ge := &GeminiExecutor{}
		cand := newGeminiCandidate("https://generativelanguage.googleapis.com")
		cand.RawModel = ""
		if _, err := ge.BuildRequest(cand, []byte(`{}`), false); err == nil {
			t.Fatal("expected an error for an empty model")
		}
	})
}

// ────────────────────────────────────────────────────────────────────────
// finalizeGeminiUpstreamBody
// ────────────────────────────────────────────────────────────────────────

// TestFinalizeGeminiUpstreamBody_OpenAIWireToGeminiWire pins the core
// request conversion: the inbound OpenAI Chat wire becomes the Gemini
// native wire (contents + systemInstruction), and the OUTBOUND model
// replaces the client model.
func TestFinalizeGeminiUpstreamBody_OpenAIWireToGeminiWire(t *testing.T) {
	e := newGeminiTestExecutor()
	params := &ExecParams{
		ClientProtocol: "openai-completions",
		ClientModel:    "client-model",
		OutboundModel:  "gemini-2.5-flash",
		IsStream:       false,
	}
	cand := newGeminiCandidate("https://generativelanguage.googleapis.com")
	wire := []byte(`{
		"model":"client-model",
		"messages":[
			{"role":"system","content":"be terse"},
			{"role":"user","content":"hello"}
		]
	}`)

	out, err := e.finalizeGeminiUpstreamBody(params, cand, wire)
	if err != nil {
		t.Fatalf("finalizeGeminiUpstreamBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("gemini body is not JSON: %v\n%s", err, out)
	}
	if _, ok := parsed["contents"]; !ok {
		t.Errorf("gemini body missing contents:\n%s", out)
	}
	if _, ok := parsed["systemInstruction"]; !ok {
		t.Errorf("gemini body missing systemInstruction:\n%s", out)
	}
	// The model lives in the URL, not the body — the body must not carry
	// a stale client model.
	if _, ok := parsed["model"]; ok {
		t.Errorf("gemini body must not carry a model field (the model is a URL segment):\n%s", out)
	}
	contents, _ := parsed["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %v, want exactly one user turn", parsed["contents"])
	}
	entry, _ := contents[0].(map[string]any)
	if entry["role"] != "user" {
		t.Errorf("contents[0].role = %v, want user", entry["role"])
	}
}

// TestFinalizeGeminiUpstreamBody_ProductionClientProtocolVocabulary pins the
// 2026-10-28 critical-audit fix: in production (IR converter wired) the
// dispatcher fills ExecParams.ClientProtocol from ir.DetectProtocol, which
// reports "openai-chat" for the OpenAI Chat wire — including the Gemini
// ingress's synthetic body. The client-wire gate previously whitelisted only
// the legacy "openai-completions" fallback, so every real Gemini-native
// ingress routed to a gemini-generate upstream died with 501 `client
// protocol "openai-chat" not yet wired` (live-verified on a temp gateway).
// The existing tests passed because they were written against the legacy
// name only. Both spellings must work; neither may regress.
func TestFinalizeGeminiUpstreamBody_ProductionClientProtocolVocabulary(t *testing.T) {
	e := newGeminiTestExecutor()
	cand := newGeminiCandidate("https://generativelanguage.googleapis.com")
	wire := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	for _, protocol := range []string{"openai-chat", "openai-completions", ""} {
		params := &ExecParams{ClientProtocol: protocol, ClientModel: "m", OutboundModel: "gemini-2.5-flash"}
		out, err := e.finalizeGeminiUpstreamBody(params, cand, wire)
		if err != nil {
			t.Fatalf("ClientProtocol %q must be accepted (canonicalInboundWire normalizes it): %v", protocol, err)
		}
		var parsed map[string]any
		if json.Unmarshal(out, &parsed) != nil {
			t.Fatalf("ClientProtocol %q: output not JSON", protocol)
		}
		if _, ok := parsed["contents"]; !ok {
			t.Errorf("ClientProtocol %q: gemini body missing contents", protocol)
		}
	}
}

// TestGeminiSerializeResponse_OpenAIChatVocabulary pins the response-side
// switch: a GeminiExecutor answering an "openai-chat" client must serialize
// the OpenAI Chat response shape (previously fell into the default arm and
// failed with 501 "not yet wired").
func TestGeminiSerializeResponse_OpenAIChatVocabulary(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-completions", ""} {
		g := &GeminiExecutor{ClientProtocol: protocol}
		out, err := g.serializeResponse(&ir.InternalResponse{
			Content: []ir.ResponseContentBlock{{Type: "text", Text: "ok"}},
			Model:   "m",
		}, "m")
		if err != nil {
			t.Fatalf("ClientProtocol %q: serializeResponse: %v", protocol, err)
		}
		var parsed map[string]any
		if json.Unmarshal(out, &parsed) != nil {
			t.Fatalf("ClientProtocol %q: output not JSON", protocol)
		}
		if _, ok := parsed["choices"]; !ok {
			t.Errorf("ClientProtocol %q: expected OpenAI Chat choices shape, got %s", protocol, out)
		}
	}
}

// TestFinalizeGeminiUpstreamBody_GatesFailClosed pins every rejection
// gate. Each one must produce a typed error rather than a body that
// would fail at the vendor with a confusing error.
func TestFinalizeGeminiUpstreamBody_GatesFailClosed(t *testing.T) {
	e := newGeminiTestExecutor()
	cand := newGeminiCandidate("https://generativelanguage.googleapis.com")

	cases := []struct {
		name     string
		params   *ExecParams
		body     string
		wantKind errorsx.ErrorKind
	}{
		{
			name:     "empty body",
			params:   &ExecParams{ClientProtocol: "openai-completions"},
			body:     "",
			wantKind: errorsx.KindClientBug,
		},
		{
			name:     "malformed wire",
			params:   &ExecParams{ClientProtocol: "openai-completions"},
			body:     "{not json",
			wantKind: errorsx.KindClientBug,
		},
		{
			name:     "trailing garbage",
			params:   &ExecParams{ClientProtocol: "openai-completions"},
			body:     `{"model":"m","messages":[{"role":"user","content":"x"}]}{"model":"evil"}`,
			wantKind: errorsx.KindClientBug,
		},
		{
			name:     "missing model",
			params:   &ExecParams{ClientProtocol: "openai-completions"},
			body:     `{"messages":[{"role":"user","content":"x"}]}`,
			wantKind: errorsx.KindClientBug,
		},
		{
			name:     "empty messages",
			params:   &ExecParams{ClientProtocol: "openai-completions"},
			body:     `{"model":"m","messages":[]}`,
			wantKind: errorsx.KindClientBug,
		},
		{
			// ★ anthropic-messages / openai-responses were "not wired"
			// until this round; they are covered by their own positive
			// tests now. This row pins that an UNKNOWN inbound protocol
			// still fails closed (the gate is not a rubber stamp).
			name:     "unknown client wire fails closed",
			params:   &ExecParams{ClientProtocol: "some-future-protocol"},
			body:     `{"model":"m","messages":[{"role":"user","content":"x"}]}`,
			wantKind: errorsx.KindUnsupportedFeature,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.finalizeGeminiUpstreamBody(tc.params, cand, []byte(tc.body))
			if err == nil {
				t.Fatal("expected an error")
			}
			var ue *upstreampkg.Error
			if !asUpstreamError(err, &ue) {
				t.Fatalf("error is not a typed *upstreampkg.Error: %v", err)
			}
			if ue.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q (%v)", ue.Kind, tc.wantKind, err)
			}
		})
	}
}

// asUpstreamError is a tiny local errors.As helper (the package already
// imports "errors" in other files; keeping this local avoids a name
// clash in this file's import block).
func asUpstreamError(err error, target **upstreampkg.Error) bool {
	for err != nil {
		if ue, ok := err.(*upstreampkg.Error); ok {
			*target = ue
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ────────────────────────────────────────────────────────────────────────
// WriteNonStreamResponse
// ────────────────────────────────────────────────────────────────────────

// TestGeminiExecutor_WriteNonStreamResponse_OpenAIShape pins that a
// Gemini native response is translated into the OpenAI chat wire the
// client expects, with the client-visible model substituted.
func TestGeminiExecutor_WriteNonStreamResponse_OpenAIShape(t *testing.T) {
	ge := &GeminiExecutor{}
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP","index":0}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5,"totalTokenCount":8},
			"modelVersion":"gemini-2.5-flash"
		}`)),
	}
	rec := httptest.NewRecorder()

	body, err := ge.WriteNonStreamResponse(rec, resp, "client-model", "", nil)
	if err != nil {
		t.Fatalf("WriteNonStreamResponse: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("no body returned")
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"pong"`) {
		t.Errorf("client body missing the answer:\n%s", out)
	}
	if !strings.Contains(out, `"model":"client-model"`) {
		t.Errorf("client body model = %s, want the client-visible model", out)
	}
	if !strings.Contains(out, `"object":"chat.completion"`) {
		t.Errorf("client body is not an OpenAI chat completion:\n%s", out)
	}
}

// TestGeminiExecutor_WriteNonStreamResponse_PassThrough4xx pins that a
// vendor error envelope reaches the client verbatim (the operator's
// debug log needs the real vendor error).
func TestGeminiExecutor_WriteNonStreamResponse_PassThrough4xx(t *testing.T) {
	ge := &GeminiExecutor{}
	resp := &http.Response{
		StatusCode: 400,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}`)),
	}
	rec := httptest.NewRecorder()

	if _, err := ge.WriteNonStreamResponse(rec, resp, "client-model", "", nil); err != nil {
		t.Fatalf("WriteNonStreamResponse: %v", err)
	}
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "INVALID_ARGUMENT") {
		t.Errorf("vendor error envelope was not preserved:\n%s", rec.Body.String())
	}
}

// TestGeminiExecutor_WriteNonStreamResponse_MalformedJSON pins that an
// unparseable upstream body becomes a typed conversion error (not a
// silent 200 with garbage).
func TestGeminiExecutor_WriteNonStreamResponse_MalformedJSON(t *testing.T) {
	ge := &GeminiExecutor{}
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`not json at all`)),
	}
	rec := httptest.NewRecorder()

	_, err := ge.WriteNonStreamResponse(rec, resp, "client-model", "", nil)
	if err == nil {
		t.Fatal("expected a conversion error")
	}
	var ue *upstreampkg.Error
	if !asUpstreamError(err, &ue) {
		t.Fatalf("error is not a typed *upstreampkg.Error: %v", err)
	}
	if ue.Kind != errorsx.KindConversion {
		t.Errorf("kind = %q, want %q", ue.Kind, errorsx.KindConversion)
	}
}

// ────────────────────────────────────────────────────────────────────────
// StreamResponse
// ────────────────────────────────────────────────────────────────────────

// TestGeminiExecutor_StreamResponse_SSEToOpenAISSE pins the stream
// translation: Gemini SSE frames become OpenAI chat.completion.chunk
// frames and the stream ends with the "[DONE]" sentinel the OpenAI wire
// requires (the Gemini stream has no sentinel of its own).
func TestGeminiExecutor_StreamResponse_SSEToOpenAISSE(t *testing.T) {
	ge := &GeminiExecutor{clientModelOverride: "client-model"}
	upstream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]},"index":0}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"index":0}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}
	rec := httptest.NewRecorder()

	outcome := ge.StreamResponse(t.Context(), rec, resp)
	if outcome.Interrupted {
		t.Fatalf("stream interrupted: %s (%s)", outcome.Reason, outcome.Kind)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"Hel"`) {
		t.Errorf("first delta missing:\n%s", out)
	}
	if !strings.Contains(out, `"content":"lo"`) {
		t.Errorf("second delta missing:\n%s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("stream did not end with the OpenAI [DONE] sentinel:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Errorf("terminal finish_reason missing:\n%s", out)
	}
}

// TestGeminiExecutor_StreamResponse_StableChatIDAndRoleMarker pins two
// OpenAI-wire details the Gemini chunk does not carry:
//
//   - the completion id must be the SAME on every frame (the IR chunk
//     has no id, so without an executor-supplied one every frame would
//     fall back to the IR's constant default — and a per-frame fallback
//     would give the client a DIFFERENT id per chunk);
//   - the assistant role must be marked exactly once, on the first
//     content-bearing delta.
func TestGeminiExecutor_StreamResponse_StableChatIDAndRoleMarker(t *testing.T) {
	ge := &GeminiExecutor{clientModelOverride: "client-model"}
	upstream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"a"}]},"index":0}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"b"}]},"index":0}]}`,
	}, "\n")
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}
	rec := httptest.NewRecorder()

	if outcome := ge.StreamResponse(t.Context(), rec, resp); outcome.Interrupted {
		t.Fatalf("stream interrupted: %s", outcome.Reason)
	}
	out := rec.Body.String()

	ids := geminiTestExtractIDs(out)
	if len(ids) < 2 {
		t.Fatalf("expected at least two frames with ids, got %d:\n%s", len(ids), out)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Errorf("chat id changed mid-stream: %q vs %q\n%s", id, ids[0], out)
		}
	}
	if !strings.Contains(out, `"role":"assistant"`) {
		t.Errorf("first delta is missing the assistant role marker:\n%s", out)
	}
	if strings.Count(out, `"role":"assistant"`) != 1 {
		t.Errorf("assistant role marked %d times, want exactly 1:\n%s",
			strings.Count(out, `"role":"assistant"`), out)
	}
}

// geminiTestExtractIDs pulls the "id" field out of every SSE frame.
func geminiTestExtractIDs(sse string) []string {
	var ids []string
	for _, line := range strings.Split(sse, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var frame struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			continue
		}
		if frame.ID != "" {
			ids = append(ids, frame.ID)
		}
	}
	return ids
}

// TestGeminiExecutor_StreamResponse_ChunkedJSONArrayAccepted pins the
// second vendor framing: without "?alt=sse" the API returns a chunked
// JSON array (one object per line, no "data:" prefix). A Gemini-
// compatible relay in front of the real API may drop the query
// parameter, so the bare form must work too.
func TestGeminiExecutor_StreamResponse_ChunkedJSONArrayAccepted(t *testing.T) {
	ge := &GeminiExecutor{clientModelOverride: "client-model"}
	upstream := strings.Join([]string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"bare"}]},"index":0}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"bare"}]},"finishReason":"STOP","index":0}]}`,
	}, "\n")
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}
	rec := httptest.NewRecorder()

	outcome := ge.StreamResponse(t.Context(), rec, resp)
	if outcome.Interrupted {
		t.Fatalf("stream interrupted: %s (%s)", outcome.Reason, outcome.Kind)
	}
	if !strings.Contains(rec.Body.String(), `"content":"bare"`) {
		t.Errorf("bare JSON framing produced no delta:\n%s", rec.Body.String())
	}
}

// TestGeminiExecutor_StreamResponse_InBandError pins that a Gemini
// error frame is re-emitted as an OpenAI-shaped error frame instead of
// a silent connection drop.
func TestGeminiExecutor_StreamResponse_InBandError(t *testing.T) {
	ge := &GeminiExecutor{clientModelOverride: "client-model"}
	upstream := `data: {"error":{"code":429,"message":"resource exhausted","status":"RESOURCE_EXHAUSTED"}}` + "\n"
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}
	rec := httptest.NewRecorder()

	outcome := ge.StreamResponse(t.Context(), rec, resp)
	out := rec.Body.String()
	if !strings.Contains(out, "resource exhausted") {
		t.Errorf("upstream error message not surfaced:\n%s", out)
	}
	if !outcome.Interrupted {
		t.Error("an in-band upstream error must interrupt the stream")
	}
}

// ────────────────────────────────────────────────────────────────────────
// Usage / mismatch helpers
// ────────────────────────────────────────────────────────────────────────

func TestGeminiExecutor_ExtractUsage(t *testing.T) {
	ge := &GeminiExecutor{}
	body := []byte(`{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}`)
	in, out := ge.ExtractUsage(&http.Response{}, body)
	if in == nil || out == nil {
		t.Fatalf("usage not extracted: in=%v out=%v", in, out)
	}
	if *in != 11 || *out != 7 {
		t.Errorf("usage = %d/%d, want 11/7", *in, *out)
	}
	if in2, out2 := ge.ExtractUsage(&http.Response{}, []byte(`{}`)); in2 != nil || out2 != nil {
		t.Errorf("usage on an empty body = %v/%v, want nil/nil", in2, out2)
	}
}

func TestGeminiExecutor_CheckSoftMismatch(t *testing.T) {
	ge := &GeminiExecutor{}
	cases := []struct {
		req, resp string
		want      bool
	}{
		{"gemini-2.5-flash", "gemini-2.5-flash", false},
		{"gemini-2.5-flash", "gemini-2.5-flash-preview-05-15", false},
		{"gemini-2.5-flash", "gemini-2.5-pro", true},
		{"gemini-2.5-flash", "", false},
		{"", "gemini-2.5-pro", false},
	}
	for _, tc := range cases {
		got, reason := ge.CheckSoftMismatch(tc.req, tc.resp)
		if got != tc.want {
			t.Errorf("CheckSoftMismatch(%q, %q) = %v (%s), want %v", tc.req, tc.resp, got, reason, tc.want)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────
// End-to-end: executeGemini against a real Gemini-shaped upstream
// ────────────────────────────────────────────────────────────────────────

// TestExecutor_ExecuteGemini_NonStream exercises the whole outbound path:
// raw OpenAI Chat Completions wire body → executeGemini → real
// Gemini-shaped upstream → OpenAI chat wire for the client.
//
// ★ This is the test that pins the bug this round fixes. Before the
// Gemini executor existed, a provider configured with protocol
// "gemini-generate" was skipped by the dispatcher with
// KindUnsupportedFeature ⇒ the configuration was dead. The assertion
// that the upstream actually RECEIVED a Gemini-native body (and that
// the credential arrived in the x-goog-api-key header) is what makes the
// round meaningful.
func TestExecutor_ExecuteGemini_NonStream(t *testing.T) {
	var seenPath, seenQuery, seenAPIKey, seenAuth string
	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenQuery = r.URL.RawQuery
		seenAPIKey = r.Header.Get("x-goog-api-key")
		seenAuth = r.Header.Get("Authorization")
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // test response writer
		w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP","index":0}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5,"totalTokenCount":8},
			"modelVersion":"gemini-2.5-flash"
		}`))
	}))
	defer srv.Close()

	wireBody := []byte(`{"model":"client-model","messages":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(wireBody))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:              rec,
		R:              req,
		BodyBytes:      wireBody,
		IsStream:       false,
		ClientModel:    "client-model",
		OutboundModel:  "gemini-2.5-flash",
		ClientProtocol: "openai-completions",
		RequestID:      "test-gemini-1",
	}
	cand := newGeminiCandidate(srv.URL)

	e := newGeminiTestExecutor()
	result, err := e.executeGemini(params, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeGemini: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if seenPath != "/v1beta/models/gemini-2.5-flash:generateContent" {
		t.Errorf("upstream path = %q, want the model-scoped native path", seenPath)
	}
	if seenQuery != "" {
		t.Errorf("upstream query = %q, want empty (the credential must not ride in the URL)", seenQuery)
	}
	if seenAPIKey != "AIza-test-key" {
		t.Errorf("x-goog-api-key = %q, want the credential", seenAPIKey)
	}
	if seenAuth != "" {
		t.Errorf("Authorization = %q, want empty", seenAuth)
	}
	var upstreamReq map[string]any
	if err := json.Unmarshal(seenBody, &upstreamReq); err != nil {
		t.Fatalf("upstream body is not JSON: %v\n%s", err, seenBody)
	}
	if _, ok := upstreamReq["contents"]; !ok {
		t.Errorf("upstream body is not Gemini native (no contents):\n%s", seenBody)
	}
	if _, ok := upstreamReq["messages"]; ok {
		t.Errorf("upstream body still carries an OpenAI messages array:\n%s", seenBody)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"pong"`) {
		t.Errorf("client body missing the answer:\n%s", out)
	}
	if !strings.Contains(out, `"model":"client-model"`) {
		t.Errorf("client body model = %s, want client-model", out)
	}
}

// TestExecutor_ExecuteGemini_Stream exercises the streaming outbound
// path end-to-end.
func TestExecutor_ExecuteGemini_Stream(t *testing.T) {
	var seenPath, seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, frame := range []string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]},"index":0}]}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP","index":0}]}`,
		} {
			//nolint:errcheck // test response writer
			w.Write([]byte(frame + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	wireBody := []byte(`{"model":"client-model","messages":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(wireBody))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:              rec,
		R:              req,
		BodyBytes:      wireBody,
		IsStream:       true,
		ClientModel:    "client-model",
		OutboundModel:  "gemini-2.5-flash",
		ClientProtocol: "openai-completions",
		RequestID:      "test-gemini-2",
	}
	cand := newGeminiCandidate(srv.URL)

	e := newGeminiTestExecutor()
	if _, err := e.executeGemini(params, cand, 0, time.Now(), nil); err != nil {
		t.Fatalf("executeGemini: %v", err)
	}
	if seenPath != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" {
		t.Errorf("upstream path = %q, want the streaming action", seenPath)
	}
	if seenQuery != "alt=sse" {
		t.Errorf("upstream query = %q, want alt=sse", seenQuery)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"Hel"`) || !strings.Contains(out, `"content":"lo"`) {
		t.Errorf("client stream missing deltas:\n%s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("client stream missing the [DONE] sentinel:\n%s", out)
	}
}

// TestExecutor_ExecuteGemini_UpstreamErrorPropagates pins that a
// vendor 4xx is classified (not swallowed), so the dispatcher can
// failover instead of returning a misleading 200.
func TestExecutor_ExecuteGemini_UpstreamErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		//nolint:errcheck // test response writer
		w.Write([]byte(`{"error":{"code":401,"message":"API key not valid","status":"UNAUTHENTICATED"}}`))
	}))
	defer srv.Close()

	wireBody := []byte(`{"model":"client-model","messages":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(wireBody))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:              rec,
		R:              req,
		BodyBytes:      wireBody,
		ClientModel:    "client-model",
		OutboundModel:  "gemini-2.5-flash",
		ClientProtocol: "openai-completions",
		RequestID:      "test-gemini-3",
	}
	cand := newGeminiCandidate(srv.URL)

	e := newGeminiTestExecutor()
	_, err := e.executeGemini(params, cand, 0, time.Now(), nil)
	if err == nil {
		t.Fatal("expected an error for a 401 upstream")
	}
	var ue *upstreampkg.Error
	if !asUpstreamError(err, &ue) {
		t.Fatalf("error is not a typed *upstreampkg.Error: %v", err)
	}
	if ue.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", ue.StatusCode)
	}
}

// ────────────────────────────────────────────────────────────────────────
// IR interop
// ────────────────────────────────────────────────────────────────────────

// TestGeminiExecutor_RoundTripsThroughIR pins the IR contract this
// executor depends on: a Gemini native response parsed by the IR can be
// re-serialized into the OpenAI wire. If the IR ever stops round-
// tripping, the executor silently starts producing empty answers — this
// test makes that loud.
func TestGeminiExecutor_RoundTripsThroughIR(t *testing.T) {
	raw := []byte(`{
		"candidates":[{"content":{"role":"model","parts":[{"text":"round trip"}]},"finishReason":"STOP","index":0}],
		"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}
	}`)
	parsed, err := ir.ParseGeminiResponse(raw)
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	out, err := ir.SerializeOpenAIResponse(parsed, "client-model")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	if !strings.Contains(string(out), "round trip") {
		t.Errorf("round-tripped body lost the answer:\n%s", out)
	}
}

// TestGeminiExecutor_GeminiClientRoundTrip pins the FULL path a native
// Gemini client traverses when the upstream is also Gemini:
//
//	upstream Gemini JSON → IR → OpenAI shape (what the executor writes)
//	→ IR → Gemini shape (what the Gemini handler writes back to the client)
//
// The middle hop is the executor's contract with the Gemini ingress
// (domains/streaming/handler_gemini.go), and the last hop is the
// handler's. If the OpenAI shape the executor produces is not parseable
// back by the handler, a native Gemini client gets an empty answer —
// and nothing in either half would fail loudly.
func TestGeminiExecutor_GeminiClientRoundTrip(t *testing.T) {
	raw := []byte(`{
		"candidates":[{"content":{"role":"model","parts":[{"text":"hello back"}]},"finishReason":"STOP","index":0}],
		"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5},
		"modelVersion":"gemini-2.5-flash"
	}`)
	// Hop 1: upstream Gemini → IR → OpenAI shape (executor).
	parsed, err := ir.ParseGeminiResponse(raw)
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	openAIShape, err := ir.SerializeOpenAIResponse(parsed, "client-model")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	// Hop 2: OpenAI shape → IR → Gemini shape (the Gemini ingress's tail).
	backToIR, err := ir.ParseOpenAIResponse(openAIShape)
	if err != nil {
		t.Fatalf("ParseOpenAIResponse (the Gemini handler's hop): %v\n%s", err, openAIShape)
	}
	geminiShape, err := ir.SerializeGeminiResponse(backToIR, "client-model")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse: %v", err)
	}
	out := string(geminiShape)
	if !strings.Contains(out, "hello back") {
		t.Errorf("the Gemini client would receive an empty answer:\n%s", out)
	}
	if !strings.Contains(out, `"role":"model"`) {
		t.Errorf("the Gemini shape lost the native model role:\n%s", out)
	}
	if !strings.Contains(out, `"finishReason"`) {
		t.Errorf("the Gemini shape lost the native finishReason:\n%s", out)
	}
}

// ────────────────────────────────────────────────────────────────────────
// Client wires: anthropic-messages / openai-responses
// ────────────────────────────────────────────────────────────────────────

// TestFinalizeGeminiUpstreamBody_AnthropicClientWire pins that an
// Anthropic Messages request is converted into the Gemini native wire.
//
// ★ This closes a real gap: before this round an Anthropic client could
// NOT use a Gemini-native provider at all (the executor failed closed
// with KindUnsupportedFeature on the request side).
func TestFinalizeGeminiUpstreamBody_AnthropicClientWire(t *testing.T) {
	e := newGeminiTestExecutor()
	params := &ExecParams{
		ClientProtocol: "anthropic-messages",
		ClientModel:    "client-model",
		OutboundModel:  "gemini-2.5-flash",
	}
	cand := newGeminiCandidate("https://generativelanguage.googleapis.com")
	wire := []byte(`{
		"model":"client-model",
		"max_tokens":256,
		"system":"be terse",
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`)

	out, err := e.finalizeGeminiUpstreamBody(params, cand, wire)
	if err != nil {
		t.Fatalf("finalizeGeminiUpstreamBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("gemini body is not JSON: %v\n%s", err, out)
	}
	if _, ok := parsed["contents"]; !ok {
		t.Errorf("gemini body missing contents:\n%s", out)
	}
	if _, ok := parsed["systemInstruction"]; !ok {
		t.Errorf("anthropic system prompt did not reach the Gemini wire:\n%s", out)
	}
	if _, ok := parsed["messages"]; ok {
		t.Errorf("gemini body still carries an OpenAI messages array:\n%s", out)
	}
}

// TestFinalizeGeminiUpstreamBody_ResponsesClientWire pins that an
// OpenAI Responses request is converted into the Gemini native wire.
func TestFinalizeGeminiUpstreamBody_ResponsesClientWire(t *testing.T) {
	e := newGeminiTestExecutor()
	params := &ExecParams{
		ClientProtocol: "openai-responses",
		ClientModel:    "client-model",
		OutboundModel:  "gemini-2.5-flash",
	}
	cand := newGeminiCandidate("https://generativelanguage.googleapis.com")
	wire := []byte(`{
		"model":"client-model",
		"instructions":"be terse",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]
	}`)

	out, err := e.finalizeGeminiUpstreamBody(params, cand, wire)
	if err != nil {
		t.Fatalf("finalizeGeminiUpstreamBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("gemini body is not JSON: %v\n%s", err, out)
	}
	if _, ok := parsed["contents"]; !ok {
		t.Errorf("gemini body missing contents:\n%s", out)
	}
	if _, ok := parsed["input"]; ok {
		t.Errorf("gemini body still carries a Responses input array:\n%s", out)
	}
}

// TestGeminiExecutor_WriteNonStreamResponse_AnthropicShape pins that a
// Gemini native response reaches an Anthropic client in Anthropic
// Messages shape (not an OpenAI-shaped body with an Anthropic label).
func TestGeminiExecutor_WriteNonStreamResponse_AnthropicShape(t *testing.T) {
	ge := &GeminiExecutor{ClientProtocol: "anthropic-messages"}
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP","index":0}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5,"totalTokenCount":8},
			"modelVersion":"gemini-2.5-flash"
		}`)),
	}
	rec := httptest.NewRecorder()

	if _, err := ge.WriteNonStreamResponse(rec, resp, "client-model", "", nil); err != nil {
		t.Fatalf("WriteNonStreamResponse: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"pong"`) {
		t.Errorf("client body missing the answer:\n%s", out)
	}
	if !strings.Contains(out, `"type":"message"`) {
		t.Errorf("client body is not an Anthropic message:\n%s", out)
	}
	if !strings.Contains(out, `"role":"assistant"`) {
		t.Errorf("client body missing the assistant role:\n%s", out)
	}
	if !strings.Contains(out, `"stop_reason"`) {
		t.Errorf("client body missing stop_reason:\n%s", out)
	}
}

// TestGeminiExecutor_WriteNonStreamResponse_ResponsesShape pins that a
// Gemini native response reaches a Responses client in Responses shape.
func TestGeminiExecutor_WriteNonStreamResponse_ResponsesShape(t *testing.T) {
	ge := &GeminiExecutor{ClientProtocol: "openai-responses"}
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP","index":0}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5,"totalTokenCount":8},
			"modelVersion":"gemini-2.5-flash"
		}`)),
	}
	rec := httptest.NewRecorder()

	if _, err := ge.WriteNonStreamResponse(rec, resp, "client-model", "", nil); err != nil {
		t.Fatalf("WriteNonStreamResponse: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"pong"`) {
		t.Errorf("client body missing the answer:\n%s", out)
	}
	if !strings.Contains(out, `"object":"response"`) {
		t.Errorf("client body is not a Responses object:\n%s", out)
	}
}

// TestGeminiExecutor_StreamResponse_AnthropicBridgeReceivesOpenAISSE
// pins the plumbing of the Anthropic client wire.
//
// ★ The executor does NOT hand-roll Anthropic framing: it produces
// OpenAI-shaped frames and feeds them into the repo's shipped
// OpenAI→Anthropic bridge through a pipe. This test pins that contract
// (the bridge receives OpenAI-shaped SSE) — the bridge's own Anthropic
// output is covered by domains/streaming/anthropic_bridge_test.go.
func TestGeminiExecutor_StreamResponse_AnthropicBridgeReceivesOpenAISSE(t *testing.T) {
	var bridgeSaw string
	ge := &GeminiExecutor{
		ClientProtocol: "anthropic-messages",
		OpenAIToAnthropicStream: func(_ context.Context, w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture, _ any, _ int, _ bool) StreamOutcome {
			body, _ := io.ReadAll(resp.Body)
			bridgeSaw = string(body)
			_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
			return StreamOutcome{ChunkCount: 7}
		},
	}
	upstream := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP","index":0}]}` + "\n"
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstream))}
	rec := httptest.NewRecorder()

	outcome := ge.StreamResponse(t.Context(), rec, resp)
	if outcome.Interrupted {
		t.Fatalf("stream interrupted: %s (%s)", outcome.Reason, outcome.Kind)
	}
	if !strings.Contains(bridgeSaw, `"content":"hi"`) {
		t.Errorf("the bridge did not receive OpenAI-shaped frames:\n%s", bridgeSaw)
	}
	if !strings.Contains(bridgeSaw, "data: [DONE]") {
		t.Errorf("the bridge did not receive the OpenAI [DONE] sentinel:\n%s", bridgeSaw)
	}
	if outcome.ChunkCount != 7 {
		t.Errorf("chunk count = %d, want the bridge's own count (7)", outcome.ChunkCount)
	}
	if !strings.Contains(rec.Body.String(), "message_stop") {
		t.Errorf("the client's bytes did not come from the bridge:\n%s", rec.Body.String())
	}
}

// TestGeminiExecutor_StreamResponse_ResponsesBridgeReceivesOpenAISSE
// pins the same plumbing for the Responses client wire.
func TestGeminiExecutor_StreamResponse_ResponsesBridgeReceivesOpenAISSE(t *testing.T) {
	var bridgeSaw string
	ge := &GeminiExecutor{
		ClientProtocol: "openai-responses",
		OpenAIToResponsesStream: func(_ context.Context, w http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture, _ any, _ bool) StreamOutcome {
			body, _ := io.ReadAll(resp.Body)
			bridgeSaw = string(body)
			_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
			return StreamOutcome{ChunkCount: 5}
		},
	}
	upstream := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP","index":0}]}` + "\n"
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstream))}
	rec := httptest.NewRecorder()

	outcome := ge.StreamResponse(t.Context(), rec, resp)
	if outcome.Interrupted {
		t.Fatalf("stream interrupted: %s (%s)", outcome.Reason, outcome.Kind)
	}
	if !strings.Contains(bridgeSaw, `"content":"hi"`) {
		t.Errorf("the bridge did not receive OpenAI-shaped frames:\n%s", bridgeSaw)
	}
	if outcome.ChunkCount != 5 {
		t.Errorf("chunk count = %d, want the bridge's own count (5)", outcome.ChunkCount)
	}
}

// TestGeminiExecutor_StreamResponse_BridgeNotWiredFailsClosed pins that
// a missing bridge fails closed instead of writing OpenAI frames to an
// Anthropic/Responses client (which would be a wrong-shaped 200).
func TestGeminiExecutor_StreamResponse_BridgeNotWiredFailsClosed(t *testing.T) {
	for _, proto := range []string{"anthropic-messages", "openai-responses"} {
		t.Run(proto, func(t *testing.T) {
			ge := &GeminiExecutor{ClientProtocol: proto}
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: {}\n"))}
			rec := httptest.NewRecorder()
			outcome := ge.StreamResponse(t.Context(), rec, resp)
			if !outcome.Interrupted {
				t.Fatal("expected the stream to fail closed")
			}
			if outcome.Kind != errorsx.KindUnsupportedFeature {
				t.Errorf("kind = %q, want %q", outcome.Kind, errorsx.KindUnsupportedFeature)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("a wrong-shaped body was written to the client: %s", rec.Body.String())
			}
		})
	}
}

// ────────────────────────────────────────────────────────────────────────
// Header profile linkage
// ────────────────────────────────────────────────────────────────────────

// TestExecutor_ExecuteGemini_HeaderProfileApplied pins the
// header_profile_code linkage (docs/供应商协议优化.md §8.3): the endpoint's
// header profile is applied to the outbound request, so a Gemini-
// compatible relay whose profile declares its own credential header gets
// it.
//
// ★ Without this, the Gemini executor would send ONLY x-goog-api-key and
// such a relay would reject the request.
func TestExecutor_ExecuteGemini_HeaderProfileApplied(t *testing.T) {
	var seenAuth, seenGoogKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenGoogKey = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // test response writer
		w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}]}`))
	}))
	defer srv.Close()

	cache := NewHeaderProfileCache(nil)
	cache.cache["google-gemini:gemini-generate"] = &headerProfile{
		Headers: map[string]string{"Authorization": "Bearer profile-token"},
	}

	wireBody := []byte(`{"model":"client-model","messages":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(wireBody))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W: rec, R: req, BodyBytes: wireBody,
		ClientModel: "client-model", OutboundModel: "gemini-2.5-flash",
		ClientProtocol: "openai-completions", RequestID: "gemini-profile",
	}
	cand := newGeminiCandidate(srv.URL)

	e := newGeminiTestExecutor()
	e.HeaderProfiles = cache
	if _, err := e.executeGemini(params, cand, 0, time.Now(), nil); err != nil {
		t.Fatalf("executeGemini: %v", err)
	}
	if seenAuth != "Bearer profile-token" {
		t.Errorf("outbound Authorization = %q, want the header profile's value", seenAuth)
	}
	if seenGoogKey != "AIza-test-key" {
		t.Errorf("outbound x-goog-api-key = %q, want the credential", seenGoogKey)
	}
}

// ────────────────────────────────────────────────────────────────────────
// Context-length recovery
// ────────────────────────────────────────────────────────────────────────

// TestExecutor_ExecuteGemini_ContextLengthRecoveryRetriesOnceWithSmallerBody
// pins that a Gemini upstream rejecting the request as too long triggers
// exactly ONE compressed retry (the same recovery coordinator the OpenAI
// and Ollama paths use).
func TestExecutor_ExecuteGemini_ContextLengthRecoveryRetriesOnceWithSmallerBody(t *testing.T) {
	var requests [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, append([]byte(nil), body...))
		if len(requests) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			//nolint:errcheck // test response writer
			w.Write([]byte(`{"error":{"code":400,"message":"This model's maximum context length is 2000 tokens. Your messages resulted in 7000 tokens.","status":"INVALID_ARGUMENT"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // test response writer
		w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP","index":0}]}`))
	}))
	defer srv.Close()

	var messages strings.Builder
	messages.WriteString(`{"model":"gemini-2.5-flash","messages":[`)
	for i := 0; i < 30; i++ {
		if i > 0 {
			messages.WriteByte(',')
		}
		messages.WriteString(fmt.Sprintf(`{"role":"user","content":%q}`, strings.Repeat("important context ", 120)))
	}
	messages.WriteString(`]}`)
	wireBody := []byte(messages.String())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(wireBody))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W: rec, R: req, BodyBytes: wireBody,
		ClientModel: "gemini-2.5-flash", OutboundModel: "gemini-2.5-flash",
		ClientProtocol: "openai-completions", RequestID: "gemini-recovery",
	}
	cand := newGeminiCandidate(srv.URL)

	result, err := newGeminiTestExecutor().executeGemini(params, cand, 0, time.Now(), nil)
	if err != nil || result == nil {
		t.Fatalf("executeGemini recovery = result %v, err %v", result, err)
	}
	if len(requests) != 2 {
		t.Fatalf("upstream requests = %d, want exactly one recovery retry", len(requests))
	}
	if len(requests[1]) >= len(requests[0]) {
		t.Fatalf("recovery body did not shrink: first=%d second=%d", len(requests[0]), len(requests[1]))
	}
	if !strings.Contains(rec.Body.String(), "recovered") {
		t.Fatalf("client response missing recovered content: %s", rec.Body.String())
	}
	if result.CompressionReason == nil || *result.CompressionReason == "" {
		t.Fatalf("CompressionReason = %v, want recorded recovery reason", result.CompressionReason)
	}
}
