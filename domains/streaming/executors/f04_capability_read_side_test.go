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
		W:                httptest.NewRecorder(),
		R:                httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		BodyBytes:        []byte(body),
		ResponsesBodyBytes: []byte(body),
		ClientModel:      "gpt-5.6-terra",
		Model:            "gpt-5.6-terra",
		RequestID:        "f04-read-side",
		TenantID:         "default",
		IsStream:         false,
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
}

func newF04ExecutorWithSlots(t *testing.T) (*Executor, *credentialfpslot.Manager) {
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
	}, fpMgr
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
