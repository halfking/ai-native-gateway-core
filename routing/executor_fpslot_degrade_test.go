package routing

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestExecutor_FpSlotAllSaturated_DegradesInsteadOfFailing pins the 2026-06-23
// fix: when the credential fingerprint-slot layer filters every candidate out
// (all slot pools saturated), the executor must degrade to the full candidate
// set and still route the request, rather than returning
// "cred_fp_slot: all saturated" and failing the request.
//
// Root cause this guards against: minimax-m3 had only one reachable candidate
// (minimax-prod-1, credential_id=6) because the other offers carried a provider
// prefix in raw_model_name (see the standardized_name fix in provider/client.go).
// That single credential's 5 fp_slots were held by long-lived sessions (24h TTL),
// so every new session hit "cred_fp_slot: all saturated" and got a 503, even
// though the credential was otherwise healthy.
func TestExecutor_FpSlotAllSaturated_DegradesInsteadOfFailing(t *testing.T) {
	// FpSlots manager with a single-slot limit so we can saturate it trivially.
	fpMgr := credentialfpslot.New(credentialfpslot.Config{
		DefaultLimit: 1,
		Enabled:      true,
	}, nil)
	ctx := t.Context()

	// Saturate credential 6's only slot with a different holder.
	lease, ok := fpMgr.Acquire(ctx, 6, nil, "other-session", "default")
	if !ok || lease == nil {
		t.Fatal("expected to acquire the single slot for saturation setup")
	}
	// Intentionally do NOT release — slot stays saturated for the duration.

	// Mock upstream that records the request reached it.
	var upstreamHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		//nolint:errcheck // test helper
		w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"minimax-m3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()
	router := NewRouter(NewStickyCache(), lim)

	e := &Executor{
		Circuit:         cm,
		Limiter:         lim,
		Router:          router,
		FpSlots:         fpMgr,
		UpstreamTimeout:  5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}

	fpLimit := 1
	cand := provider.Candidate{
		ProviderID:    14,
		CredentialID:  6,
		BaseURL:       srv.URL,
		Protocol:      "openai-completions",
		APIKey:        "sk-test",
		RawModel:      "MiniMax-M3",
		FpSlotLimit:   &fpLimit,
		Tier:          2,
		BillingMode:   "token",
	}
	// Mark available so filterAvailable keeps it (Routable=true,
	// no BlockReason, healthy states).
	cand.Routable = true

	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	r.Header.Set("X-Request-Id", "test-degrade-1")
	rec := httptest.NewRecorder()

	params := &ExecParams{
		W:             rec,
		R:             r,
		BodyBytes:     []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		ClientModel:   "minimax-m3",
		OutboundModel: "minimax-m3",
		Candidates:    []provider.Candidate{cand},
		StickyKey:     "new-session-degrade",
		Policy:        &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
	}

	result, err := e.Execute(params)
	// The degradation path means Execute should NOT return an
	// "all saturated" error — it should attempt the upstream.
	if err != nil {
		t.Fatalf("Execute returned error after fp_slot saturation; expected degradation: %v", err)
	}
	if result == nil {
		t.Fatal("Execute returned nil result after degradation")
	}
	if !upstreamHit {
		t.Fatal("upstream was never hit — degradation did not fall back to the full candidate set")
	}
	t.Logf("degradation OK: upstream hit, candidate credential_id=%d", result.Candidate.CredentialID)
}

// TestExecutor_FpSlotNotSaturated_PrefersFilteredSet ensures the degradation
// path does NOT kick in when at least one candidate has a free slot. In that
// case the normal filtered set is used and the upstream is hit normally.
func TestExecutor_FpSlotNotSaturated_PrefersFilteredSet(t *testing.T) {
	fpMgr := credentialfpslot.New(credentialfpslot.Config{
		DefaultLimit: 5,
		Enabled:      true,
	}, nil)

	var upstreamHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		//nolint:errcheck // test helper
		w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"minimax-m3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()
	router := NewRouter(NewStickyCache(), lim)

	e := &Executor{
		Circuit:         cm,
		Limiter:         lim,
		Router:          router,
		FpSlots:         fpMgr,
		UpstreamTimeout:  5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}

	fpLimit := 5
	cand := provider.Candidate{
		ProviderID:    14,
		CredentialID:  7,
		BaseURL:       srv.URL,
		Protocol:      "openai-completions",
		APIKey:        "sk-test",
		RawModel:      "minimax-m3",
		FpSlotLimit:   &fpLimit,
		Tier:          2,
		BillingMode:   "token",
	}
	cand.Routable = true

	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	r.Header.Set("X-Request-Id", "test-nosat-1")
	rec := httptest.NewRecorder()

	params := &ExecParams{
		W:             rec,
		R:             r,
		BodyBytes:     []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		ClientModel:   "minimax-m3",
		OutboundModel: "minimax-m3",
		Candidates:    []provider.Candidate{cand},
		StickyKey:     "session-nosat",
		Policy:        &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
	}

	result, err := e.Execute(params)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Execute returned nil result")
	}
	if !upstreamHit {
		t.Fatal("upstream was never hit despite free slots")
	}
}
