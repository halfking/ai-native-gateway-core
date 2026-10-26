package executors

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// Drive both the ordinary candidate prefilter (without a trusted probe pin)
// and the production forward path. A stale DB mirror must not win over the
// live breaker; removing the mirror must not disable real cooling/quarantine.
func TestDispatchCircuitAdmissionUsesLiveBreakerNotDBMirror(t *testing.T) {
	if !settings.IsEnabled("circuit_degradation") {
		t.Fatal("test requires the real circuit admission gate enabled")
	}
	for _, tc := range []struct {
		name        string
		mirror      string
		kind        errorsx.ErrorKind
		wantBlocked bool
	}{
		{"restart_with_stale_open_mirror", "open", "", false},
		{"closed_mirror_cannot_bypass_live_open", "closed", errorsx.KindTransient, true},
		{"open_mirror_and_live_open", "open", errorsx.KindTransient, true},
		{"closed_mirror_cannot_bypass_quarantine", "closed", errorsx.KindQuota, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				upstreamCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-mirror","object":"chat.completion","model":"grok-4.6","choices":[{"index":0,"message":{"role":"assistant","content":"VAPEUR_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			}))
			defer upstream.Close()
			cm := credential.NewManager()
			if tc.wantBlocked {
				cm.RecordFailure(36, 63, tc.kind)
				cm.RecordFailure(36, 63, tc.kind)
				if cm.PeekAllowed(36, 63) {
					t.Fatal("fixture did not actually open/quarantine the live breaker")
				}
			}
			limiter := newLimiterForTest()
			defer limiter.Stop()
			exec := &Executor{Circuit: cm, Limiter: limiter, UpstreamTimeout: 5 * time.Second, StreamTimeout: 10 * time.Second}
			params := &ExecParams{W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
				BodyBytes:   []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hello"}]}`),
				ClientModel: "grok-4.6", Model: "grok-4.6", RequestID: tc.name, TenantID: "default",
				UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit)}
			cand := provider.Candidate{ProviderID: 36, CredentialID: 63, BaseURL: upstream.URL,
				Protocol: "openai-completions", RawModel: "grok-4.6", APIKey: "fixture-only",
				Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: tc.mirror}
			if !dispatchCandidateAllowed(cand, params.PinCredentialID) {
				t.Fatal("DB mirror prevented the unpinned candidate from reaching live admission")
			}
			dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{cand}, tTotal: time.Now()}
			out := exec.forwardForDispatch(dctx, cand, "mirror-attempt-"+tc.name, func() {}, nil, t.Context())
			if tc.wantBlocked {
				if !errors.Is(out.Err, errDispatchCircuitOpen) || upstreamCalls.Load() != 0 {
					t.Fatalf("live breaker bypassed: err=%v upstream_calls=%d", out.Err, upstreamCalls.Load())
				}
			} else if out.Err != nil || out.Result == nil || upstreamCalls.Load() != 1 {
				t.Fatalf("stale mirror blocked fresh breaker: err=%v result=%v upstream_calls=%d", out.Err, out.Result, upstreamCalls.Load())
			}
		})
	}
}
