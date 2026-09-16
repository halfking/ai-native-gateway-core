package streaming

// success_auto_outcome_test.go — R33 (2026-09-17), F1 from the 09-16 154 10%
// canary field test (docs/planning/p2.2-staging-verification-report.md §A4):
// the success-terminal entry built inside emitTelemetry never carried
// IsAutoRequest, so both trailing gates (v2.1 implicit tuning signal and the
// routingopt ReportRoutingOutcome backfill) were dead on success — 120
// stashed auto decisions, only the 22 failures ever matched, 98 successes
// TTL-evicted (orphan=0 proved the success path never even reported).
//
// The pins below drive the real emitTelemetry with a pgxmock-backed telemetry
// client and observe the autoroute registry counters: an auto success MUST
// attempt an outcome report (no stash in a unit test → orphan +1), a plain
// success MUST NOT (orphan unchanged). Matching/stash mechanics themselves
// are covered by the autoroute package tests.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func successOutcomeStatsDelta(t *testing.T, run func()) (orphanDelta int64) {
	t.Helper()
	_, _, orphanBefore, _, _ := autoroute.RoutingOutcomeStats()
	run()
	_, _, orphanAfter, _, _ := autoroute.RoutingOutcomeStats()
	return orphanAfter - orphanBefore
}

func emitSuccessTelemetry(t *testing.T, ch *ChatHandler, requestID string, logCtx *RequestLogContext) {
	t.Helper()
	evt := audit.Event{
		RequestID:     requestID,
		ClientModel:   "auto",
		OutboundModel: "glm-5.2",
		CanonicalName: "glm-5.2",
		IdentityHash:  "hash-f1",
	}
	result := &executors.ExecuteResult{
		Candidate: provider.Candidate{CredentialID: 3, ProviderID: 5},
		LatencyMs: 42,
		Response:  &http.Response{StatusCode: http.StatusOK, Header: http.Header{}},
	}
	require.NotPanics(t, func() {
		ch.emitTelemetry(evt, result, "user-f1", nil, nil, "chat", nil,
			[]byte(`{"model":"auto"}`), []byte(`{"ok":true}`), logCtx)
	})
}

func newAutoLogCtx(t *testing.T, ch *ChatHandler, requestID string) *RequestLogContext {
	t.Helper()
	logCtx := ch.NewRequestLogContext(
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		requestID, time.Now())
	// SetAutoDecision marks the context as an auto request and carries the
	// decision payload (task_type travels into the entry via
	// applyAutoRouteFields, exactly as the decision-time path does).
	logCtx.SetAutoDecision(&autoRouteDecision{
		TaskType:    "code",
		Confidence:  0.82,
		Profile:     "balanced",
		Classifier:  "heuristic",
		ChosenModel: "glm-5.2",
	})
	return logCtx
}

// TestEmitTelemetry_AutoSuccessReportsOutcome: with the F1 fix, a successful
// auto request must reach autoroute.ReportRoutingOutcome. No decision is
// stashed under this id in the unit test, so the report surfaces as an
// orphan count — the point is that the gate OPENED (pre-fix: orphan stayed
// put because the call never fired).
func TestEmitTelemetry_AutoSuccessReportsOutcome(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()
	mockDB.MatchExpectationsInOrder(false)
	mockDB.ExpectExec(`INSERT INTO request_logs_hot`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectExec(`INSERT INTO routing_decision_log_hot`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	tc := telemetry.NewClientWithRequestLogDB(mockDB)
	defer tc.Stop()
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ch.SetTelemetry(tc)

	logCtx := newAutoLogCtx(t, ch, "req-f1-auto-success")
	require.True(t, logCtx.IsAutoRequest, "precondition: SetAutoDecision marks the context")

	delta := successOutcomeStatsDelta(t, func() {
		emitSuccessTelemetry(t, ch, "req-f1-auto-success", logCtx)
	})
	require.EqualValues(t, 1, delta,
		"successful auto request must attempt the routingopt outcome backfill (F1)")
}

// TestEmitTelemetry_PlainSuccessStaysSilent: a non-auto success must NOT
// report an outcome (the gate must stay closed; a plain request with no
// stashed decision would otherwise pollute the orphan signal).
func TestEmitTelemetry_PlainSuccessStaysSilent(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()
	mockDB.MatchExpectationsInOrder(false)
	mockDB.ExpectExec(`INSERT INTO request_logs_hot`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectExec(`INSERT INTO routing_decision_log_hot`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	tc := telemetry.NewClientWithRequestLogDB(mockDB)
	defer tc.Stop()
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ch.SetTelemetry(tc)

	logCtx := ch.NewRequestLogContext(
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		"req-f1-plain-success", time.Now())
	require.False(t, logCtx.IsAutoRequest)

	delta := successOutcomeStatsDelta(t, func() {
		emitSuccessTelemetry(t, ch, "req-f1-plain-success", logCtx)
	})
	require.EqualValues(t, 0, delta,
		"plain success must not attempt a routingopt outcome report")
}
