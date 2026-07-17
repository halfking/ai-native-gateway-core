package executors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestExecutor_NoCandidateProbe_Recovers verifies the 2026-07-17 sync-probe
// hold path: when the router returns zero candidates AND ProbeSync signals
// recovery, Execute retries with the freshly-recovered candidate and
// returns a successful result.
func TestExecutor_NoCandidateProbe_Recovers(t *testing.T) {
	upstreamHit := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit.Store(true)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"minimax-m3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()
	router := NewRouter(NewStickyCache(), lim)

	e := &Executor{
		Circuit:                cm,
		Limiter:                lim,
		Router:                 router,
		UpstreamTimeout:        5 * time.Second,
		StreamTimeout:          10 * time.Second,
		SyncNoCandidateProbe:   true,
		SyncNoCandidateTimeout: 2 * time.Second,
	}

	probeCalled := atomic.Bool{}
	e.ProbeSync = func(ctx context.Context, candidates []credentialstate.NoCandidatesCandidate, tenantID, parentReqID string) bool {
		probeCalled.Store(true)
		return len(candidates) > 0
	}

	cand := provider.Candidate{
		ProviderID:   14,
		CredentialID: 6,
		BaseURL:      srv.URL,
		Protocol:     "openai-completions",
		APIKey:       "sk-test",
		RawModel:     "minimax-m3",
		Tier:         2,
		BillingMode:  "token",
		Routable:     false,
	}

	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	r.Header.Set("X-Request-Id", "test-no-cand-recover")
	rec := httptest.NewRecorder()

	candSlice := []provider.Candidate{cand}
	params := &ExecParams{
		W:                rec,
		R:                r,
		BodyBytes:        []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:         false,
		ClientModel:      "minimax-m3",
		OutboundModel:    "minimax-m3",
		Candidates:       candSlice,
		StickyKey:        "test-no-cand-recover",
		Policy:           &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		RequestID:        "test-no-cand-recover",
		OnProbeHoldStart: func() {},
		OnProbeHoldEnd: func(recovered bool) {
			if recovered {
				candSlice[0].Routable = true
			}
		},
	}

	result, err := e.Execute(params)
	if err != nil {
		t.Fatalf("Execute returned error after sync probe recovery: %v", err)
	}
	if result == nil {
		t.Fatal("Execute returned nil result after sync probe recovery")
	}
	if !probeCalled.Load() {
		t.Fatal("ProbeSync was never called")
	}
	if !upstreamHit.Load() {
		t.Fatal("upstream was never hit — recovery did not reach the upstream")
	}
}

// TestExecutor_NoCandidateProbe_Exhausted verifies the legacy 503 path
// still triggers when ProbeSync reports failure.
func TestExecutor_NoCandidateProbe_Exhausted(t *testing.T) {
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()
	router := NewRouter(NewStickyCache(), lim)

	e := &Executor{
		Circuit:                cm,
		Limiter:                lim,
		Router:                 router,
		UpstreamTimeout:        5 * time.Second,
		StreamTimeout:          10 * time.Second,
		SyncNoCandidateProbe:   true,
		SyncNoCandidateTimeout: 500 * time.Millisecond,
	}

	holdStartCount := atomic.Int32{}
	holdEndCount := atomic.Int32{}
	recoveredSeen := atomic.Bool{}

	e.ProbeSync = func(ctx context.Context, candidates []credentialstate.NoCandidatesCandidate, tenantID, parentReqID string) bool {
		select {
		case <-time.After(50 * time.Millisecond):
			return false
		case <-ctx.Done():
			return false
		}
	}

	cand := provider.Candidate{
		ProviderID:   14,
		CredentialID: 6,
		BaseURL:      "http://127.0.0.1:1",
		Protocol:     "openai-completions",
		APIKey:       "sk-test",
		RawModel:     "minimax-m3",
		Tier:         2,
		BillingMode:  "token",
		Routable:     false,
	}

	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:                rec,
		R:                r,
		BodyBytes:        []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:         false,
		ClientModel:      "minimax-m3",
		OutboundModel:    "minimax-m3",
		Candidates:       []provider.Candidate{cand},
		StickyKey:        "test-no-cand-exhausted",
		Policy:           &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		RequestID:        "test-no-cand-exhausted",
		OnProbeHoldStart: func() { holdStartCount.Add(1) },
		OnProbeHoldEnd: func(recovered bool) {
			holdEndCount.Add(1)
			recoveredSeen.Store(recovered)
		},
	}

	_, err := e.Execute(params)
	if err == nil {
		t.Fatal("Execute should return error when sync probe fails")
	}
	if holdStartCount.Load() != 1 {
		t.Errorf("expected exactly one OnProbeHoldStart call, got %d", holdStartCount.Load())
	}
	if holdEndCount.Load() != 1 {
		t.Errorf("expected exactly one OnProbeHoldEnd call, got %d", holdEndCount.Load())
	}
	if recoveredSeen.Load() {
		t.Error("OnProbeHoldEnd should have been called with recovered=false")
	}
}

// TestExecutor_NoCandidateProbe_DisabledByFlag verifies the kill-switch.
func TestExecutor_NoCandidateProbe_DisabledByFlag(t *testing.T) {
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()
	router := NewRouter(NewStickyCache(), lim)

	e := &Executor{
		Circuit:              cm,
		Limiter:              lim,
		Router:               router,
		UpstreamTimeout:      5 * time.Second,
		StreamTimeout:        10 * time.Second,
		SyncNoCandidateProbe: false, // KILL-SWITCH
	}

	probeCalled := atomic.Bool{}
	e.ProbeSync = func(ctx context.Context, candidates []credentialstate.NoCandidatesCandidate, tenantID, parentReqID string) bool {
		probeCalled.Store(true)
		return true
	}

	cand := provider.Candidate{
		ProviderID:   14,
		CredentialID: 6,
		BaseURL:      "http://127.0.0.1:1",
		Protocol:     "openai-completions",
		APIKey:       "sk-test",
		RawModel:     "minimax-m3",
		Tier:         2,
		BillingMode:  "token",
		Routable:     false,
	}

	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:             rec,
		R:             r,
		BodyBytes:     []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		ClientModel:   "minimax-m3",
		OutboundModel: "minimax-m3",
		Candidates:    []provider.Candidate{cand},
		StickyKey:     "test-no-cand-off",
		Policy:        &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		RequestID:     "test-no-cand-off",
	}

	_, err := e.Execute(params)
	if err == nil {
		t.Fatal("Execute should return error when no candidates exist")
	}
	if probeCalled.Load() {
		t.Error("ProbeSync was invoked despite kill-switch=false")
	}
}

// TestExecutor_NoCandidateProbe_HonorsClientCancel verifies that client
// disconnect during the probe hold aborts the hold immediately.
func TestExecutor_NoCandidateProbe_HonorsClientCancel(t *testing.T) {
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()
	router := NewRouter(NewStickyCache(), lim)

	e := &Executor{
		Circuit:                cm,
		Limiter:                lim,
		Router:                 router,
		UpstreamTimeout:        5 * time.Second,
		StreamTimeout:          10 * time.Second,
		SyncNoCandidateProbe:   true,
		SyncNoCandidateTimeout: 5 * time.Second,
	}

	probeStartCalled := atomic.Bool{}
	e.ProbeSync = func(ctx context.Context, candidates []credentialstate.NoCandidatesCandidate, tenantID, parentReqID string) bool {
		probeStartCalled.Store(true)
		<-ctx.Done()
		return false
	}

	cand := provider.Candidate{
		ProviderID:   14,
		CredentialID: 6,
		BaseURL:      "http://127.0.0.1:1",
		Protocol:     "openai-completions",
		APIKey:       "sk-test",
		RawModel:     "minimax-m3",
		Tier:         2,
		BillingMode:  "token",
		Routable:     false,
	}

	rCtx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	r = r.WithContext(rCtx)
	rec := httptest.NewRecorder()

	params := &ExecParams{
		W:             rec,
		R:             r,
		BodyBytes:     []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		ClientModel:   "minimax-m3",
		OutboundModel: "minimax-m3",
		Candidates:    []provider.Candidate{cand},
		StickyKey:     "test-no-cand-cancel",
		Policy:        &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		RequestID:     "test-no-cand-cancel",
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := e.Execute(params)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Execute should return error after client cancel")
	}
	if !probeStartCalled.Load() {
		t.Error("ProbeSync was never called")
	}
	if elapsed > 1*time.Second {
		t.Errorf("Execute took %v after client cancel; expected <1s", elapsed)
	}
}