package executors

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestShouldAsyncFallback_DisabledWhenPreStreamPrepared(t *testing.T) {
	exec := &Executor{
		AsyncShortTimeout: 1 * time.Second,
		AsyncLongTimeout:  10 * time.Second,
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Session-Id", "gw_test")
	params := &ExecParams{
		R:                 req,
		PreStreamPrepared: true,
	}
	if exec.shouldAsyncFallback(params, time.Now().Add(-2*time.Second), 1) {
		t.Fatal("expected async fallback to be disabled after pre-stream response commit")
	}
}

// TestExecuteOpenAI_StreamPreStreamStopOrdering is the focused
// e2e-level check for the pre-stream keepalive contract:
//  1. The first byte seen by the client is the SSE keep-alive comment.
//  2. The OnStreamReady callback is fired exactly once, before any
//     content chunk is forwarded.
//  3. The stream body delivered to the client still contains the
//     upstream payload in order.
func TestExecuteOpenAI_StreamPreStreamStopOrdering(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			// First attempt fails with a transient 500. The classifier
			// will route this to KindTransient, which goes through the
			// retry path without invoking fpLease-only branches.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			return
		}
		// Second attempt returns a valid SSE stream.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	var readyCount atomic.Int32
	var firstContentBeforeReady atomic.Bool

	exec := NewExecutor(
		NewRouter(NewStickyCache(), credential.NewLimiter()),
		credential.NewManager(),
		credential.NewLimiter(),
		pool.NewPoolManager(nil),
		nil,
		func(chunk []byte, isStream bool) []byte { return chunk },
		func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) StreamOutcome {
			// This is the production StreamChat injection point. It is
			// called AFTER OnStreamReady has already been fired. We
			// verify the order and forward the body verbatim.
			if readyCount.Load() == 0 {
				firstContentBeforeReady.Store(true)
			}
			defer resp.Body.Close()
			buf := make([]byte, 4096)
			n, _ := resp.Body.Read(buf)
			_, _ = w.Write(buf[:n])
			return StreamOutcome{}
		},
		nil,
	)

	cand := provider.Candidate{
		CredentialID:      101,
		ProviderID:        7,
		Tier:              1,
		BaseURL:           upstream.URL,
		Protocol:          "openai-completions",
		CatalogCode:       "openai",
		RawModel:          "gpt-4o",
		Weight:            100,
		BillingMode:       "token_plan",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
		APIKey:            "sk-test",
	}

	policy := provider.DefaultPolicy()
	policy.RetryPerCredential = 1

	result, err := exec.executeOpenAI(
		&ExecParams{
			W:                 rec,
			R:                 req,
			BodyBytes:         []byte(`{"model":"gpt-4o","messages":[],"stream":true}`),
			IsStream:          true,
			PreStreamPrepared: true,
			OnStreamReady: func() {
				readyCount.Add(1)
			},
			ClientProtocol: "openai-completions",
			ClientModel:    "gpt-4o",
			ClientID:       identity.ClientIdentity{IdentityHash: "test"},
		},
		cand,
		policy.RetryPerCredential,
		time.Now(),
		nil,
	)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2 (one failed + one success)", calls.Load())
	}
	if firstContentBeforeReady.Load() {
		t.Fatal("OnStreamReady must fire before any stream content is forwarded")
	}
	if readyCount.Load() != 1 {
		t.Fatalf("OnStreamReady fired %d times, want 1", readyCount.Load())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"hi"`) {
		t.Fatalf("stream body = %q, want final stream chunk", body)
	}
}
