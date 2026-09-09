package streaming

// rate_limited_decision_log_test.go — 2026-09-09 24h audit round 3 (#4):
// gateway rate-limit rejections previously left NO routing_decision_log
// signal at all. EmitRateLimited now writes an explicit not-run-class row
// (success=false, failure_stage="gateway", error_class = the rate-limit
// code, zero candidates, no provider/credential attribution) so a rate-limit
// storm is visible in decision views and statistics without inventing a new
// enum: failure_stage keeps its documented two-value vocabulary
// ("gateway" | "upstream") and the error codes are the existing
// gateway early-exit codes.

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// captureDecisionEmissions registers the onDecisionEmitted hook and returns
// a thread-safe collector.
func captureDecisionEmissions(tc *telemetry.Client) *decisionCapture {
	c := &decisionCapture{}
	tc.SetOnDecisionLogEmitted(func(entry *telemetry.DecisionLogEntry) {
		c.mu.Lock()
		c.entries = append(c.entries, entry)
		c.mu.Unlock()
	})
	return c
}

type decisionCapture struct {
	mu      sync.Mutex
	entries []*telemetry.DecisionLogEntry
}

func (c *decisionCapture) all() []*telemetry.DecisionLogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*telemetry.DecisionLogEntry(nil), c.entries...)
}

func TestEmitRateLimited_EmitsNotRunDecisionRow(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()
	mockDB.MatchExpectationsInOrder(false)

	// The completion-side request_logs UPSERT fires first (insertRateLimited
	// placeholder aside, EmitRequestLogUpdate upserts on request_id).
	// No WithArgs: the SQL shape is the contract here, not the column list.
	mockDB.ExpectExec(`INSERT INTO request_logs_hot`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// The not-run-class decision row.
	mockDB.ExpectExec(`INSERT INTO routing_decision_log_hot`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	tc := telemetry.NewClientWithRequestLogDB(mockDB)
	defer tc.Stop()
	capture := captureDecisionEmissions(tc)

	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ch.SetTelemetry(tc)

	start := time.Now()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	logCtx := ch.NewRequestLogContext(req, "req-rl-decision", start)
	logCtx.SetKey(&authentication.KeyInfo{ID: 7, TenantID: "tenant-rl"})
	logCtx.SetClientModel("kimi-k3")

	logCtx.EmitRateLimited("rate_limit_exceeded", "rate limit exceeded", nil, nil)
	require.True(t, logCtx.IsLogged(), "rate-limited request must claim the terminal transition")

	entries := capture.all()
	require.Len(t, entries, 1, "exactly one routing_decision_log row must be emitted")
	e := entries[0]
	require.Equal(t, "req-rl-decision", e.RequestID)
	require.Equal(t, "tenant-rl", e.TenantID)
	require.NotNil(t, e.APIKeyID)
	require.Equal(t, 7, *e.APIKeyID)
	// not_run-class marker: existing vocabulary only.
	require.False(t, e.Success, "rate-limited request must not be marked successful")
	require.NotNil(t, e.ErrorClass)
	require.Equal(t, "rate_limit_exceeded", *e.ErrorClass)
	require.NotNil(t, e.FailureStage)
	require.Equal(t, "gateway", *e.FailureStage, "stage enum must stay gateway|upstream")
	require.NotNil(t, e.FailureDetailCode)
	require.Equal(t, "rate_limit_exceeded", *e.FailureDetailCode)
	// Nothing ran: zero candidates, no provider/credential attribution —
	// provider-scoped aggregations stay clean.
	require.Equal(t, 0, e.CandidatesTried)
	require.Nil(t, e.ChosenProviderID)
	require.Nil(t, e.ChosenCredentialID)
	require.Equal(t, "kimi-k3", e.Model)
	require.NotNil(t, e.ClientModel)
	require.Equal(t, "kimi-k3", *e.ClientModel)
}

func TestEmitRateLimited_DisabledClientSkipsDecisionRow(t *testing.T) {
	// No telemetry client at all.
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	logCtx := ch.NewRequestLogContext(httptest.NewRequest("POST", "/v1/chat/completions", nil), "req-rl-disabled", time.Now())
	require.NotPanics(t, func() {
		logCtx.emitRateLimitedDecisionLog("key_throttled")
	})

	// Client present but disabled (no PG): the emission guard must skip.
	tc := telemetry.NewClient()
	defer tc.Stop()
	capture := captureDecisionEmissions(tc)
	ch2 := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ch2.SetTelemetry(tc)
	logCtx2 := ch2.NewRequestLogContext(httptest.NewRequest("POST", "/v1/chat/completions", nil), "req-rl-disabled2", time.Now())
	logCtx2.emitRateLimitedDecisionLog("key_throttled")
	require.Empty(t, capture.all(), "disabled client must not emit decision rows")
}

func TestBuildRateLimitedDecisionEntry_NilSafetyAndDefaults(t *testing.T) {
	// Pure builder: nil receiver must produce the minimal required shape.
	e := (*RequestLogContext)(nil).buildRateLimitedDecisionEntry("tpm_limit_exceeded")
	require.NotNil(t, e)
	require.False(t, e.Success)
	require.Equal(t, "gateway", *e.FailureStage)
	require.Equal(t, "tpm_limit_exceeded", *e.ErrorClass)
	require.Equal(t, "tpm_limit_exceeded", *e.FailureDetailCode)
	require.Equal(t, "<unknown>", e.Model, "model is NOT NULL in routing_decision_log")
	require.Equal(t, "default", e.TenantID)

	// Empty client model falls back to the same NOT-NULL placeholder the
	// failure path uses.
	ctx := &RequestLogContext{}
	e2 := ctx.buildRateLimitedDecisionEntry("concurrent_limit_exceeded")
	require.Equal(t, "<unknown>", e2.Model)
	require.NotNil(t, e2.ClientModel)
	require.Equal(t, "<unknown>", *e2.ClientModel)
}
