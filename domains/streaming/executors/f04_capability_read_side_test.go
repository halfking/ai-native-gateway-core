package executors

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

// F04 (V3 持久化协议能力, 2026-09-30) — request-path read side.
//
// The write side (credentialfpslot + probe) is pinned in
// credentialfpslot/node_state_capability_test.go. What is proven HERE is the
// behavior that actually motivated F04: once the probe has recorded a durable
// "Responses unsupported" verdict for a (credential, model), the executor must
// not send another doomed native Responses attempt — it goes straight to Chat
// Completions, and the upstream never sees a /responses request at all.
//
// The counter is the assertion. A test that only checked the final body would
// pass even if the executor sent the doomed request first and then fell back,
// which is precisely the waste F04 removes.

// responsesRejectingUpstream serves a real "Responses API unsupported" verdict
// on /v1/responses and a working chat completion on /v1/chat/completions,
// counting hits per endpoint.
func responsesRejectingUpstream(t *testing.T, responsesHits, chatHits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/responses"):
			responsesHits.Add(1)
			// The exact shape providercap.ResponsesUnsupportedError matches.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"The Responses API does not support this model. Use /v1/chat/completions instead."}}`))
		default:
			chatHits.Add(1)
			var req struct {
				Model    string `json:"model"`
				Messages []any  `json:"messages"`
			}
			_ = json.Unmarshal(body, &req)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-f04","object":"chat.completion","model":"` + req.Model +
				`","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func f04ExecParams(body string) *ExecParams {
	return &ExecParams{
		W:                  httptest.NewRecorder(),
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		BodyBytes:          []byte(body),
		ResponsesBodyBytes: []byte(body),
		ClientModel:        "gpt-5.6-terra",
		Model:              "gpt-5.6-terra",
		RequestID:          "f04-read-side",
		TenantID:           "default",
		IsStream:           false,
		UpstreamAttempts:   NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
}

func newF04ExecutorWithSlots(t *testing.T) (*Executor, *credentialfpslot.Manager) {
	exec, manager, _ := newF04ExecutorWithSlotsAndRedis(t)
	return exec, manager
}

func newF04ExecutorWithSlotsAndRedis(t *testing.T) (*Executor, *credentialfpslot.Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})
	fpMgr := credentialfpslot.New(credentialfpslot.Config{DefaultLimit: 5, Enabled: true}, client)
	limiter := newLimiterForTest()
	t.Cleanup(limiter.Stop)
	return &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         limiter,
		FpSlots:         fpMgr,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}, fpMgr, mr
}

// TestF04_RequestSkipsDoomedResponsesAttempt is the end-to-end read-side
// contract: 20 requests after a durable negative verdict must produce ZERO
// /responses hits and 20 /chat/completions hits.
func TestF04_RequestSkipsDoomedResponsesAttempt(t *testing.T) {
	exec, fpMgr := newF04ExecutorWithSlots(t)

	var responsesHits, chatHits atomic.Int32
	upstream := responsesRejectingUpstream(t, &responsesHits, &chatHits)

	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: "gpt-5.6-terra", APIKey: "test-key",
		SupportsNativeResponses: true,
	}
	// The probe has already concluded Responses is unsupported here.
	if err := fpMgr.SetSupportsResponses(t.Context(), candidate.CredentialID, candidate.RawModel, false); err != nil {
		t.Fatalf("seed durable capability: %v", err)
	}

	const requests = 20
	for i := 0; i < requests; i++ {
		params := f04ExecParams(`{"model":"gpt-5.6-terra","input":"hello"}`)
		if _, err := exec.executeOpenAI(params, candidate, 0, time.Now(), nil); err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}

	if got := responsesHits.Load(); got != 0 {
		t.Fatalf("durable verdict must skip native Responses; got %d /responses hits across %d requests", got, requests)
	}
	if got := chatHits.Load(); got != requests {
		t.Fatalf("every request must go to chat completions; got %d, want %d", got, requests)
	}
}

// TestF04_NoVerdictStillAttemptsResponses is the control: with no durable
// verdict on record, the executor must keep using native Responses. Without
// this test, a mutation that short-circuits unconditionally (ignoring the
// verdict) would still satisfy the test above.
func TestF04_NoVerdictStillAttemptsResponses(t *testing.T) {
	exec, _ := newF04ExecutorWithSlots(t)

	var responsesHits, chatHits atomic.Int32
	upstream := responsesRejectingUpstream(t, &responsesHits, &chatHits)

	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: "gpt-5.6-terra", APIKey: "test-key",
		SupportsNativeResponses: true,
	}
	// Deliberately NO SetSupportsResponses call: never probed.
	params := f04ExecParams(`{"model":"gpt-5.6-terra","input":"hello"}`)
	if _, err := exec.executeOpenAI(params, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("unprobed request failed: %v", err)
	}

	if responsesHits.Load() == 0 {
		t.Fatal("an unprobed credential must still attempt native Responses (live detection path)")
	}
}

// A real request that receives the provider's precise unsupported verdict is
// new durable evidence. The current request falls back to Chat; the next one
// must reuse that verdict and avoid another doomed /responses round trip.
func TestF04_LiveUnsupportedVerdictPersistsForNextRequest(t *testing.T) {
	exec, fpMgr := newF04ExecutorWithSlots(t)
	var responsesHits, chatHits atomic.Int32
	upstream := responsesRejectingUpstream(t, &responsesHits, &chatHits)
	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: "gpt-5.6-terra", APIKey: "test-key",
		SupportsNativeResponses: true,
	}

	first := f04ExecParams(`{"model":"gpt-5.6-terra","input":"hello"}`)
	if _, err := exec.executeOpenAI(first, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("first request fallback failed: %v", err)
	}
	if got := responsesHits.Load(); got != 1 {
		t.Fatalf("first request Responses hits=%d, want one live capability observation", got)
	}
	if got := chatHits.Load(); got != 1 {
		t.Fatalf("first request Chat fallback hits=%d, want one", got)
	}

	supported, known, err := fpMgr.GetSupportsResponses(t.Context(), candidate.CredentialID, candidate.RawModel, nil)
	if err != nil {
		t.Fatalf("read persisted capability: %v", err)
	}
	if !known || supported {
		t.Fatalf("live unsupported evidence = (supported=%t, known=%t), want (false, true)", supported, known)
	}

	second := f04ExecParams(`{"model":"gpt-5.6-terra","input":"hello again"}`)
	if _, err := exec.executeOpenAI(second, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("second request using durable capability failed: %v", err)
	}
	if got := responsesHits.Load(); got != 1 {
		t.Fatalf("second request repeated doomed Responses call; hits=%d, want still one", got)
	}
	if got := chatHits.Load(); got != 2 {
		t.Fatalf("Chat calls=%d after two requests, want two", got)
	}
	supported, known, err = fpMgr.GetSupportsResponses(t.Context(), candidate.CredentialID, candidate.RawModel, nil)
	if err != nil {
		t.Fatalf("read capability after Chat fallback: %v", err)
	}
	if !known || supported {
		t.Fatalf("Chat fallback overwrote native Responses evidence = (supported=%t, known=%t), want (false, true)", supported, known)
	}
}

// A successful native Responses exchange is equally authoritative positive
// evidence. After a cached negative verdict expires, a fresh native attempt
// can prove support again and persist the positive result.
func TestF04_LiveSuccessfulResponsesPersistsSupportedVerdict(t *testing.T) {
	exec, fpMgr, redisServer := newF04ExecutorWithSlotsAndRedis(t)
	var responsesHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		responsesHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_live","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	t.Cleanup(upstream.Close)
	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: "gpt-5.6-terra", APIKey: "test-key",
		SupportsNativeResponses: true,
	}
	if err := fpMgr.SetSupportsResponses(t.Context(), candidate.CredentialID, candidate.RawModel, false); err != nil {
		t.Fatalf("seed stale unsupported capability: %v", err)
	}
	state, err := fpMgr.GetNodeState(t.Context(), candidate.CredentialID, candidate.RawModel)
	if err != nil {
		t.Fatalf("read seeded node state: %v", err)
	}
	if state == nil {
		t.Fatal("seeded node state missing")
	}
	// Expire only the protocol verdict while keeping the shared health key
	// alive, then prove the executor retries native Responses on fresh evidence.
	state.CapabilityExpiresAt = time.Now().Add(-time.Minute).Unix()
	if err := fpMgr.SetNodeState(t.Context(), state); err != nil {
		t.Fatalf("expire test capability verdict: %v", err)
	}
	if !redisServer.Exists("llmgw:cred_fp_node:22:gpt-5.6-terra") {
		t.Fatal("node health key should remain present while capability verdict is expired")
	}
	if _, known, err := fpMgr.GetSupportsResponses(t.Context(), candidate.CredentialID, candidate.RawModel, nil); err != nil {
		t.Fatalf("read expired capability verdict: %v", err)
	} else if known {
		t.Fatal("expired capability verdict must return to unknown before native retry")
	}

	params := f04ExecParams(`{"model":"gpt-5.6-terra","input":"hello"}`)
	if _, err := exec.executeOpenAI(params, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("native Responses request failed: %v", err)
	}
	if got := responsesHits.Load(); got != 1 {
		t.Fatalf("Responses hits=%d, want one native success", got)
	}
	supported, known, err := fpMgr.GetSupportsResponses(t.Context(), candidate.CredentialID, candidate.RawModel, nil)
	if err != nil {
		t.Fatalf("read persisted capability: %v", err)
	}
	if !known || !supported {
		t.Fatalf("live successful evidence = (supported=%t, known=%t), want (true, true)", supported, known)
	}
}

// TestF04_PositiveVerdictStillAttemptsResponses pins the other direction: a
// durable "supported" verdict must not block native Responses either.
func TestF04_PositiveVerdictStillAttemptsResponses(t *testing.T) {
	exec, fpMgr := newF04ExecutorWithSlots(t)

	var responsesHits, chatHits atomic.Int32
	upstream := responsesRejectingUpstream(t, &responsesHits, &chatHits)

	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: "gpt-5.6-terra", APIKey: "test-key",
		SupportsNativeResponses: true,
	}
	if err := fpMgr.SetSupportsResponses(t.Context(), candidate.CredentialID, candidate.RawModel, true); err != nil {
		t.Fatalf("seed durable capability: %v", err)
	}

	params := f04ExecParams(`{"model":"gpt-5.6-terra","input":"hello"}`)
	if _, err := exec.executeOpenAI(params, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("supported-verdict request failed: %v", err)
	}
	if responsesHits.Load() == 0 {
		t.Fatal("a durable SUPPORTED verdict must not short-circuit native Responses")
	}
}

// TestF04_VerdictIsScopedToCredentialAndModel guards the blast radius on the
// request path: a verdict for another model / another credential must not
// suppress native Responses here.
func TestF04_VerdictIsScopedToCredentialAndModel(t *testing.T) {
	exec, fpMgr := newF04ExecutorWithSlots(t)

	var responsesHits, chatHits atomic.Int32
	upstream := responsesRejectingUpstream(t, &responsesHits, &chatHits)

	// Sibling model on the same credential, and the same model on another
	// credential, both unsupported — neither applies to our node.
	if err := fpMgr.SetSupportsResponses(t.Context(), 22, "other-model", false); err != nil {
		t.Fatalf("seed sibling model verdict: %v", err)
	}
	if err := fpMgr.SetSupportsResponses(t.Context(), 99, "gpt-5.6-terra", false); err != nil {
		t.Fatalf("seed other credential verdict: %v", err)
	}

	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: "gpt-5.6-terra", APIKey: "test-key",
		SupportsNativeResponses: true,
	}
	params := f04ExecParams(`{"model":"gpt-5.6-terra","input":"hello"}`)
	if _, err := exec.executeOpenAI(params, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if responsesHits.Load() == 0 {
		t.Fatal("another model's verdict must not suppress this node's native Responses")
	}
}
