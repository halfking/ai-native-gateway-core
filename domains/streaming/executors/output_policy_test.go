package executors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

type policyCommitRecorder struct {
	*httptest.ResponseRecorder
	writes int
}

func (w *policyCommitRecorder) WriteHeader(status int) {
	w.writes++
	w.ResponseRecorder.WriteHeader(status)
}

func TestChatExecutorWriteTimePolicyEnvelopeStopsBeforeCommit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		blocked    bool
	}{
		{"structured-code", `{"error":{"code":"output_policy_blocked","message":"unrelated text"}}`, true},
		{"message-only", `{"error":{"message":"response blocked by output policy"}}`, false},
		{"different-code", `{"error":{"code":"upstream_error","message":"output_policy_blocked"}}`, false},
		{"quoted-content", `{"choices":[{"message":{"content":"output_policy_blocked"}}]}`, false},
		{"nested-error", `{"choices":[{"message":{"error":{"code":"output_policy_blocked"}}}]}`, false},
		{"malformed", `{"error":{"code":"output_policy_blocked"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &ChatExecutor{RedactBodyFn: func([]byte, string, string) []byte { return []byte(tc.body) }}
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Provider-Hint": {"raw"}},
				Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"synthetic-secret"}}]}`))}
			rec := &policyCommitRecorder{ResponseRecorder: httptest.NewRecorder()}
			body, err := executor.WriteNonStreamResponse(rec, resp, "", "", nil)
			if tc.blocked {
				if !errors.Is(err, errorsx.ErrOutputPolicyBlocked) || len(body) != 0 || rec.writes != 0 || rec.Body.Len() != 0 || rec.Header().Get("X-Provider-Hint") != "" {
					t.Fatalf("policy block committed bytes/headers or lost typed cause: body=%q err=%v writes=%d headers=%v", body, err, rec.writes, rec.Header())
				}
			} else if err != nil || string(body) != tc.body || rec.Body.String() != tc.body || rec.Code != http.StatusOK {
				t.Fatalf("ordinary response mistaken for policy block: err=%v body=%q", err, body)
			}
		})
	}
}

func TestDispatchOutputPolicyCauseSurvivesWrappingWithoutRetry(t *testing.T) {
	err := &dispatch.ExhaustedError{Cause: fmt.Errorf("policy callback: %w", errorsx.ErrOutputPolicyBlocked)}
	got := dispatchErrToExecuteError(err)
	if !errors.Is(got, errorsx.ErrOutputPolicyBlocked) || got.Exhausted || got.LastKind != errorsx.KindContentFilter {
		t.Fatalf("policy terminal flattened into provider exhaustion: %+v", got)
	}
}

type policyFailureDB struct{ calls atomic.Int64 }

func (db *policyFailureDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	db.calls.Add(1)
	return pgconn.CommandTag{}, nil
}

func TestDispatchWriteTimePolicyDoesNotRecordProviderFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"synthetic-secret"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	limiter := newLimiterForTest()
	defer limiter.Stop()
	health := &dispatchHealthCapture{}
	failures := &policyFailureDB{}
	executor := &Executor{
		Limiter: limiter, Circuit: newCircuitManagerForTest(), UpstreamTimeout: 5 * time.Second,
		NodeHealthAdapter: health, FailureLogger: NewCandidateFailureWriter(failures),
		RedactBodyFn: func([]byte, string, string) []byte {
			return []byte(`{"error":{"code":"output_policy_blocked"}}`)
		},
	}
	candidate := provider.Candidate{ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-completions", CatalogCode: "openai", RawModel: "m", APIKey: "synthetic-key"}
	params := &ExecParams{
		W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		Model:     "m", ClientModel: "m", RequestID: "policy-attempt", TenantID: "tenant-1",
	}
	dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{candidate}, retryPerCred: 0, tTotal: time.Now(), stickyCredID: &candidate.CredentialID}
	out := executor.forwardForDispatch(dctx, candidate, "policy-attempt-1", nil, nil)
	if !errors.Is(out.Err, errorsx.ErrOutputPolicyBlocked) || out.ErrorKind != string(errorsx.KindContentFilter) || out.HTTPStatus != http.StatusForbidden || out.BytesSent {
		t.Errorf("policy did not terminate before commit: %+v", out)
	}
	if got := failures.calls.Load(); got != 0 {
		t.Errorf("policy written to candidate failure table: calls=%d", got)
	}
	if got := health.snapshot(); len(got) != 0 {
		t.Errorf("policy applied provider health decision: %+v", got)
	}
	if dctx.stickyFailed {
		t.Error("policy marked sticky credential failed")
	}
}
