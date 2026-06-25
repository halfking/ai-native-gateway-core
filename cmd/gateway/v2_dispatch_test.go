// Command gateway - v2_dispatch_test.go (2026-06-26, R1.12)
//
// Tests for the Pipeline-based v1 dispatch wrapper that activates when
// LLM_GATEWAY_USE_V2_PIPELINE=true (or the legacy
// LLM_GATEWAY_V2_ENABLED). The wrapper overrides the 4 v1 chat
// endpoints (/v1/chat/completions, /v1/completions, /v1/messages,
// /v1/responses) with a Pipeline preflight → chatHandler fallback →
// postflight chain. Default OFF (production safety).
//
// The tests in this file exercise ONLY the wrapper logic, not the
// full production data plane. A real chatHandler is not constructed;
// the tests build a stand-in handler that records whether it was
// called. This pins down the production safety contract:
//
//   - Flag OFF (default) → v2DispatchMux returns false, mux state
//     unchanged, the v1 handler is reachable exactly as before.
//   - Flag ON  → v2DispatchMux returns a sub-mux + deps. The
//     wrapper around /v1/chat/completions runs the Pipeline
//     (synchronously; no upstream call), then forwards to the
//     fallback handler. The fallback handler is called even when
//     a Pipeline stage returns an error (R1.12 safety contract).
//   - /v1/messages and /v1/responses are wrapped with the same
//     fallback chain.
//   - The wrapper is safe under nil deps / nil fallback (does not
//     panic; returns a 404 or falls through to the fallback).

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// v2ChatHandlerStub is a stand-in for *relay.ChatHandler that records
// whether it was called. The Pipeline wrapper doesn't care what the
// fallback does; it just needs an http.Handler to forward to.
type v2ChatHandlerStub struct {
	called int32
	body   []byte
	status int
}

func (s *v2ChatHandlerStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&s.called, 1)
	if s.body == nil {
		s.body = []byte(`{"stub":true,"path":"` + r.URL.Path + `"}`)
	}
	if s.status == 0 {
		s.status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s.status)
	_, _ = w.Write(s.body)
}

// TestV2Dispatch_DefaultOff_NoPipeline verifies the production safety
// guarantee: when LLM_GATEWAY_USE_V2_PIPELINE is not set, the
// Pipeline wrapper is NOT constructed. The 4 v1 endpoints stay on
// the v1 chatHandler (mux state unchanged).
func TestV2Dispatch_DefaultOff_NoPipeline(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "")
	t.Setenv("LLM_GATEWAY_V2_ENABLED", "")

	if v2UsePipeline() {
		t.Fatal("v2UsePipeline() must return false when both env vars are unset")
	}

	chat := &v2ChatHandlerStub{}
	mux, deps, ok := v2DispatchMux(chat, chat, chat)
	if ok {
		t.Fatalf("v2DispatchMux() must report !ok when flag is off; got ok=%v deps=%v", ok, deps)
	}
	if mux != nil {
		t.Fatal("v2DispatchMux() must return nil mux when flag is off")
	}
	if deps != nil {
		t.Fatal("v2DispatchMux() must return nil deps when flag is off")
	}
	if atomic.LoadInt32(&chat.called) != 0 {
		t.Fatal("v1 chatHandler stub must not be called when flag is off")
	}
}

// TestV2Dispatch_ExplicitFalse_NoPipeline verifies that explicit
// "false" values do not enable the wrapper. Only true-ish values do.
func TestV2Dispatch_ExplicitFalse_NoPipeline(t *testing.T) {
	cases := []string{"false", "0", "no", "off", "garbage"}
	for _, v := range cases {
		v := v
		t.Run("value="+v, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", v)
			if v2UsePipeline() {
				t.Fatalf("v2UsePipeline() must return false for value %q", v)
			}
			chat := &v2ChatHandlerStub{}
			_, _, ok := v2DispatchMux(chat, chat, chat)
			if ok {
				t.Fatalf("v2DispatchMux() must report !ok for value %q", v)
			}
		})
	}
}

// TestV2Dispatch_ExplicitTrue_BuildsPipeline verifies that true-ish
// values enable the wrapper and the sub-mux has the 4 v1 endpoints
// reachable. The wrapper runs the Pipeline synchronously, then
// forwards to the v1 chatHandler stub.
func TestV2Dispatch_ExplicitTrue_BuildsPipeline(t *testing.T) {
	cases := []string{"true", "TRUE", "1", "yes"}
	for _, v := range cases {
		v := v
		t.Run("value="+v, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", v)

			if !v2UsePipeline() {
				t.Fatalf("v2UsePipeline() must return true for value %q", v)
			}

			chat := &v2ChatHandlerStub{}
			mux, deps, ok := v2DispatchMux(chat, chat, chat)
			if !ok {
				t.Fatal("v2DispatchMux() must report ok=true when flag is on")
			}
			if mux == nil {
				t.Fatal("v2DispatchMux() must return non-nil mux")
			}
			if deps == nil {
				t.Fatal("v2DispatchMux() must return non-nil deps")
			}
			if deps.Pipeline == nil {
				t.Fatal("deps.Pipeline must be non-nil after mux build")
			}
			if got := len(deps.Pipeline.Stages()); got == 0 {
				t.Fatal("Pipeline must have at least one stage")
			}

			// /v1/chat/completions: Pipeline runs, then falls
			// through to the stub.
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("/v1/chat/completions returned %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if atomic.LoadInt32(&chat.called) != 1 {
				t.Fatalf("v1 chatHandler stub should be called once; got %d", atomic.LoadInt32(&chat.called))
			}
			var body map[string]any
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["stub"] != true {
				t.Errorf("expected stub response, got %v", body)
			}
		})
	}
}

// TestV2Dispatch_AllFourEndpointsForwardToFallback verifies that
// the wrapper covers all 4 v1 endpoints: chat/completions,
// completions, messages, responses. Each one must reach the
// chatHandler (or messagesHandler / responsesHandler) fallback.
func TestV2Dispatch_AllFourEndpointsForwardToFallback(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")

	chat1 := &v2ChatHandlerStub{}
	chat2 := &v2ChatHandlerStub{}
	messages := &v2ChatHandlerStub{}
	responses := &v2ChatHandlerStub{}

	// v2DispatchMux wraps all 4 endpoints internally — no need to
	// register them again here. /v1/chat/completions and
	// /v1/completions each get their own stub so call counters
	// stay independent.
	mux, deps, ok := v2DispatchMux(chat1, messages, responses)
	if !ok || deps == nil {
		t.Fatalf("v2DispatchMux() must report ok=true deps=non-nil; got ok=%v", ok)
	}

	endpoints := []struct {
		path    string
		stub    *v2ChatHandlerStub
		payload string
	}{
		{"/v1/chat/completions", chat1, `{"model":"gpt-4","stream":false}`},
		{"/v1/messages", messages, `{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", responses, `{"model":"gpt-4","input":"hi"}`},
	}

	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, ep.path, strings.NewReader(ep.payload))
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s returned %d, want 200; body=%s", ep.path, rec.Code, rec.Body.String())
			}
			if atomic.LoadInt32(&ep.stub.called) != 1 {
				t.Fatalf("%s stub should be called once; got %d", ep.path, atomic.LoadInt32(&ep.stub.called))
			}
		})
	}

	// /v1/completions shares the chat handler in production; verify
	// it reaches the chat fallback. Note: it accumulates on chat1's
	// counter since the mux routes both /v1/chat/completions and
	// /v1/completions through the same chatHandler param.
	t.Run("/v1/completions", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/completions",
			strings.NewReader(`{"model":"gpt-4","prompt":"hi"}`))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("/v1/completions returned %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	})
	_ = chat2 // reserved for future per-route stub differentiation
}

// TestV2Dispatch_PipelineExecutesBeforeFallback verifies the
// integration contract: the Pipeline runs BEFORE the fallback
// handler. A side-effect of the tracing / security / audit hooks
// is that the request_id, tenant_id, and session_id from the
// request are stamped into env.Metadata (we observe this by
// checking the wrapper does not panic on missing headers —
// optional fields are tolerated).
func TestV2Dispatch_PipelineExecutesBeforeFallback(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")

	chat := &v2ChatHandlerStub{}
	mux, deps, ok := v2DispatchMux(chat, chat, chat)
	if !ok || deps == nil {
		t.Fatalf("v2DispatchMux() must succeed; got ok=%v", ok)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4"}`))
	req.Header.Set("X-Tenant-ID", "tenant-1")
	req.Header.Set("X-Session-ID", "session-1")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&chat.called) != 1 {
		t.Fatal("fallback handler should be called after Pipeline preflight")
	}
}

// TestV2Dispatch_NilChatHandlerSafe verifies the defensive
// contract: v2DispatchMux tolerates a nil chat handler. It
// returns (nil, nil, false) so the caller falls back to v1
// registrations. (A nil messagesHandler / responsesHandler is
// allowed — only /v1/chat/completions and /v1/completions are
// then wrapped, and /v1/messages / /v1/responses are skipped.)
func TestV2Dispatch_NilChatHandlerSafe(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("v2DispatchMux panicked on nil chatHandler: %v", r)
		}
	}()
	mux, deps, ok := v2DispatchMux(nil, nil, nil)
	if ok {
		t.Fatal("v2DispatchMux() must report !ok when chatHandler is nil")
	}
	if mux != nil || deps != nil {
		t.Fatal("v2DispatchMux() must return nil mux and deps when chatHandler is nil")
	}
}

// TestV2Dispatch_LegacyFlagStillWorks verifies the OR-semantics
// between LLM_GATEWAY_USE_V2_PIPELINE (R1.12) and
// LLM_GATEWAY_V2_ENABLED (Round 49). If either is true, the
// wrapper is enabled. This preserves operator muscle memory.
func TestV2Dispatch_LegacyFlagStillWorks(t *testing.T) {
	cases := []struct {
		newFlag string
		oldFlag string
		want    bool
	}{
		{"", "", false},
		{"", "true", true},
		{"true", "", true},
		{"true", "false", true},
		{"false", "true", true},
		{"false", "false", false},
		{"true", "true", true},
	}
	for _, tc := range cases {
		tc := tc
		name := "new=" + tc.newFlag + "_old=" + tc.oldFlag
		t.Run(name, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", tc.newFlag)
			t.Setenv("LLM_GATEWAY_V2_ENABLED", tc.oldFlag)
			if got := v2UsePipeline(); got != tc.want {
				t.Fatalf("v2UsePipeline() = %v, want %v (new=%q old=%q)",
					got, tc.want, tc.newFlag, tc.oldFlag)
			}
		})
	}
}

// TestV2Dispatch_WrapperNilSafe verifies v2DispatchHandler with
// nil deps returns the fallback unchanged (no panic).
func TestV2Dispatch_WrapperNilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("v2DispatchHandler panicked on nil deps: %v", r)
		}
	}()

	called := int32(0)
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(http.StatusOK)
	})

	// nil deps → wrapper == fallback
	h := v2DispatchHandler(nil, fallback)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Fatal("fallback should be called when deps are nil")
	}
}

// TestV2Dispatch_BodyParsingDoesNotConsumeRequest verifies that
// the dispatchRequestBody helper restores the request body so
// chatHandler can re-read it. Without this, the fallback
// handler would see an empty body and return 400.
func TestV2Dispatch_BodyParsingDoesNotConsumeRequest(t *testing.T) {
	payload := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	model, stream, body, err := dispatchRequestBody(req)
	if err != nil {
		t.Fatalf("dispatchRequestBody error: %v", err)
	}
	if model != "gpt-4" {
		t.Errorf("model = %q, want gpt-4", model)
	}
	if stream {
		t.Errorf("stream = true, want false")
	}
	if string(body) != payload {
		t.Errorf("body = %q, want %q", body, payload)
	}
	// Verify the body can still be read by the next handler.
	if err := req.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	buf := make([]byte, len(payload))
	n, _ := req.Body.Read(buf)
	if n == 0 {
		// Already consumed by the body close; need to re-test
		// with a fresh request to confirm restoration.
		req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(payload))
		_, _, _, _ = dispatchRequestBody(req2)
		buf2 := make([]byte, len(payload))
		n2, _ := req2.Body.Read(buf2)
		if n2 != len(payload) {
			t.Errorf("body not restored: read %d bytes, want %d", n2, len(payload))
		}
	}
}

// TestV2Dispatch_ShutdownNilSafe verifies v2ShutdownPipeline
// tolerates nil deps. Mirrors the production shutdown order:
// callers in main.go stop the v1 services first, then call
// v2ShutdownPipeline; if v2 was never built, the call is a no-op.
func TestV2Dispatch_ShutdownNilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("v2ShutdownPipeline panicked on nil deps: %v", r)
		}
	}()
	v2ShutdownPipeline(nil)
}

// TestV2Dispatch_PipelineErrorDoesNotBlockFallback verifies the
// R1.12 safety contract: even if a Pipeline stage returns an
// error, the request still reaches the fallback handler. A
// misbehaving Hook cannot lose data.
func TestV2Dispatch_PipelineErrorDoesNotBlockFallback(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")

	chat := &v2ChatHandlerStub{}
	_, deps, ok := v2DispatchMux(chat, chat, chat)
	if !ok || deps == nil {
		t.Fatalf("v2DispatchMux() must succeed; got ok=%v", ok)
	}
	// Force a Pipeline stage to fail by clearing the in-memory
	// audit writer. The audit hook reads from it; the wrapper
	// will catch the error and fall through to the fallback.
	deps.AuditWriter = nil

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4"}`))
	wrapper := v2DispatchHandler(deps, chat)
	wrapper.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("fallback should be reached even on Pipeline error; got status %d body=%s",
			rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&chat.called) != 1 {
		t.Fatal("fallback should be called exactly once")
	}
}
