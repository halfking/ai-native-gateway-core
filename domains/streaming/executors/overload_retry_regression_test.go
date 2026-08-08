package executors

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"  //nolint:depguard // matches executor_prestream_test.go
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // matches executor_prestream_test.go
	"github.com/kaixuan/llm-gateway-go/domains/identity"    //nolint:depguard // matches executor_prestream_test.go
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// overloadRelayBody is the verbatim 502 payload observed from apiclaude.cc on
// 2026-08-08.
const overloadRelayBody = `{"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"upstream_error"}}`

// newOverloadTestExecutor builds the same minimal executor wiring used by
// executor_prestream_test.go, forwarding the upstream stream verbatim.
func newOverloadTestExecutor() *Executor {
	return NewExecutor(
		NewRouter(NewStickyCache(), credential.NewLimiter()),
		credential.NewManager(),
		credential.NewLimiter(),
		pool.NewPoolManager(nil),
		nil,
		func(chunk []byte, isStream bool) []byte { return chunk },
		func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, catalogCode string, norm NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) StreamOutcome {
			defer func() { _ = resp.Body.Close() }()
			buf := make([]byte, 4096)
			n, _ := resp.Body.Read(buf)
			_, _ = w.Write(buf[:n])
			return StreamOutcome{}
		},
		nil,
	)
}

func overloadTestCandidate(baseURL string) provider.Candidate {
	return provider.Candidate{
		CredentialID:      2,
		ProviderID:        314,
		Tier:              1,
		BaseURL:           baseURL,
		Protocol:          "openai-completions",
		CatalogCode:       "openai",
		RawModel:          "gpt-5.6-luna",
		Weight:            100,
		BillingMode:       "token_plan",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
		APIKey:            "sk-test",
	}
}

// TestExecuteOpenAI_OverloadRetriesSameCredential is a production regression
// test. It fails against the first (reverted) revision of the overload fix.
//
// That revision returned the upstream error unwrapped for
// KindUpstreamOverloaded to force an immediate switch to another credential,
// reasoning that retrying a saturated relay is wasted effort. Deployed to 154
// as seq 1475, it broke the very requests it meant to improve: gpt-5.6-luna
// resolves to a SINGLE candidate, so there was no sibling to switch to. The
// candidate walk ended immediately and the client received an empty 200
// (stream_chunks=0, success=false) where the previous build had returned a
// full answer.
//
// The 24h of logs before that deploy are unambiguous: 8 of 8 overloaded
// requests were rescued by the same-credential retry, usually on attempt 1.
// For this failure the same credential moments later IS the fastest recovery,
// so overload must stay on the ordinary retryable path. Cross-credential
// failover still happens afterwards, via the transient continue-list in
// executor.go, once the per-credential budget is spent.
func TestExecuteOpenAI_OverloadRetriesSameCredential(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// The exact failure from production: 502 + overload body.
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(overloadRelayBody))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	exec := newOverloadTestExecutor()
	result, err := exec.executeOpenAI(
		&ExecParams{
			W:              rec,
			R:              req,
			BodyBytes:      []byte(`{"model":"gpt-5.6-luna","messages":[],"stream":true}`),
			IsStream:       true,
			ClientProtocol: "openai-completions",
			ClientModel:    "gpt-5.6-luna",
			ClientID:       identity.ClientIdentity{IdentityHash: "test"},
		},
		overloadTestCandidate(upstream.URL),
		1, // RetryPerCredential — the production default
		time.Now(),
		nil,
	)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v; an overloaded 502 must be retried on the same credential, not surfaced", err)
	}
	if result == nil {
		t.Fatal("expected a non-nil result after the retry succeeded")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (overloaded 502, then success). "+
			"1 means the executor gave up without retrying — the seq-1475 regression", got)
	}
}

// TestExecuteOpenAI_OverloadExhaustedSurfacesRetryableError verifies the other
// half of the contract: when the per-credential budget IS spent, the error must
// still surface as retryable so the candidate loop can advance to a sibling.
// Recovering on the same credential is preferred, not mandatory.
func TestExecuteOpenAI_OverloadExhaustedSurfacesRetryableError(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(overloadRelayBody))
	}))
	defer upstream.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	exec := newOverloadTestExecutor()
	_, err := exec.executeOpenAI(
		&ExecParams{
			W:              rec,
			R:              req,
			BodyBytes:      []byte(`{"model":"gpt-5.6-luna","messages":[],"stream":true}`),
			IsStream:       true,
			ClientProtocol: "openai-completions",
			ClientModel:    "gpt-5.6-luna",
			ClientID:       identity.ClientIdentity{IdentityHash: "test"},
		},
		overloadTestCandidate(upstream.URL),
		1,
		time.Now(),
		nil,
	)
	if err == nil {
		t.Fatal("expected an error once the retry budget is exhausted")
	}
	// The retry budget must actually have been spent before giving up.
	if got := calls.Load(); got < 2 {
		t.Fatalf("upstream calls = %d, want >= 2 (the per-credential retry must be used)", got)
	}
	// The surfaced kind drives the candidate loop's failover decision. Derived
	// the same way the loop does: prefer the typed Kind, fall back to
	// re-classifying the text.
	var kind errorsx.ErrorKind
	var ue *upstreampkg.Error
	if errors.As(err, &ue) && ue.Kind != "" {
		kind = ue.Kind
	} else {
		kind = errorsx.ClassifyError(err, nil)
	}
	if kind != errorsx.KindUpstreamOverloaded {
		t.Errorf("surfaced kind = %q, want %q", kind, errorsx.KindUpstreamOverloaded)
	}
	if !isTransientFailoverKind(kind) {
		t.Fatalf("surfaced kind = %q, which the candidate loop will NOT fail over on; "+
			"a streaming request would end as all_candidates_failed", kind)
	}
}
