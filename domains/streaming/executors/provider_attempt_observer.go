package executors

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

type observedProviderCall struct {
	attempt    reconciliation.Attempt
	dispatchAt time.Time
	sentNanos  atomic.Int64
}

type providerAttemptBatch struct {
	store    reconciliation.AttemptStore
	params   *ExecParams
	cand     provider.Candidate
	original *http.Request
	initial  reconciliation.Attempt
	count    atomic.Int64
	latest   atomic.Pointer[observedProviderCall]
}

var _ upstreampkg.AttemptObserver = (*providerAttemptBatch)(nil)
var _ upstreampkg.AttemptErrorBodyObserver = (*providerAttemptBatch)(nil)

func (e *Executor) observeProviderCalls(req *http.Request, params *ExecParams, cand provider.Candidate, initial reconciliation.Attempt) (*http.Request, *providerAttemptBatch) {
	if e == nil || e.AttemptRecorder == nil || req == nil {
		return req, nil
	}
	b := &providerAttemptBatch{store: e.AttemptRecorder, params: params, cand: cand, original: req, initial: initial}
	return upstreampkg.WithAttemptObserver(req, b), b
}

func newProviderAttempt(params *ExecParams, cand provider.Candidate, source string) reconciliation.Attempt {
	traffic, clientID, originSource, tenantID, requestID := providerAttemptClassification(params)
	return reconciliation.NewAttempt(reconciliation.Attempt{TenantID: tenantID, RequestID: requestID, JobID: params.GWTaskID, TrafficClass: traffic, ClientID: clientID, Source: originSource + ":" + source, AttemptNo: params.AttemptNo, ProviderID: cand.ProviderID, CredentialID: cand.CredentialID, Model: candidateRawModel(cand), RateSnapshot: candidateRateSnapshot(cand)})
}

func (b *providerAttemptBatch) BeforeAttempt(req *http.Request) any {
	index := b.count.Add(1)
	replay := upstreampkg.IsTransportReplay(req)
	a := b.initial
	if index > 1 {
		a = newProviderAttempt(b.params, b.cand, "internal_retry")
		a.Source = b.initial.Source
		if !replay {
			a.Source += ":internal_retry"
		}
		a.AttemptNo = b.initial.AttemptNo + int(index) - 1
	}
	if req.Response != nil {
		a.Source = b.initial.Source
		a = reconciliation.AttributeRedirect(a, b.original, req)
	}
	if replay {
		a.Source = "transport_replay:" + a.Source
	}
	call := &observedProviderCall{attempt: a}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	if err := b.store.Prepare(ctx, a); err != nil {
		slog.Warn("provider attempt intent persistence failed", "attempt_id", a.ID, "error", err)
	}
	cancel()
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	if err := b.store.MarkDispatched(ctx, a.ID); err != nil {
		slog.Warn("provider attempt dispatch persistence failed", "attempt_id", a.ID, "error", err)
	}
	cancel()
	call.dispatchAt = time.Now().UTC()
	b.latest.Store(call)
	return call
}

func (b *providerAttemptBatch) ObserveRequestWrite(handle any, at time.Time) {
	call, _ := handle.(*observedProviderCall)
	if call != nil && call.sentNanos.CompareAndSwap(0, at.UnixNano()) {
		reconciliation.JournalTransportSend(b.store, call.attempt.ID, at)
	}
}

func (b *providerAttemptBatch) AfterAttempt(_ *http.Request, handle any, resp *http.Response, doErr error) {
	call, _ := handle.(*observedProviderCall)
	if call == nil {
		return
	}
	if resp != nil {
		rid := reconciliation.ProviderRequestID(resp.Header, nil)
		sent := sentEvidenceTime(call.sentNanos.Load(), call.dispatchAt)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if err := b.store.RecordHTTP(ctx, call.attempt.ID, resp.StatusCode, rid, sent); err != nil {
			slog.Warn("provider attempt response persistence failed", "attempt_id", call.attempt.ID, "error", err)
		}
		cancel()
		if resp.StatusCode >= 300 {
			outcome := "unknown"
			if resp.StatusCode >= 400 {
				outcome = "failure"
			}
			ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
			_ = b.store.Finish(ctx, call.attempt.ID, outcome)
			cancel()
		}
		return
	}
	if doErr != nil {
		sent := sentEvidenceTime(call.sentNanos.Load(), time.Time{})
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if err := reconciliation.RecordTransportFailure(ctx, b.store, call.attempt.ID, doErr, sent); err != nil {
			slog.Warn("provider attempt transport error persistence failed", "attempt_id", call.attempt.ID, "error", err)
		}
		cancel()
	}
}

func (b *providerAttemptBatch) ObserveErrorBody(handle any, resp *http.Response, body []byte) {
	call, _ := handle.(*observedProviderCall)
	if call == nil {
		return
	}
	recordAttemptResponseBodyID(b.store, call.attempt.ID, resp, body)
	evidence := reconciliation.ParseProviderResponseEvidence(body, b.cand.Protocol, reconciliation.ResponseEndpoint(resp))
	if evidence.Source == "" && evidence.ReportedAmount == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := b.store.RecordUsage(ctx, call.attempt.ID, evidence); err != nil {
		slog.Warn("provider error response usage persistence failed", "attempt_id", call.attempt.ID, "error", err)
	}
}

func recordAttemptResponseBodyID(store reconciliation.AttemptStore, attemptID string, resp *http.Response, body []byte) {
	if reconciliation.ProviderRequestID(resp.Header, nil) != "" {
		return
	}
	if id := reconciliation.ProviderRequestID(nil, body); id != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if err := store.RecordHTTP(ctx, attemptID, resp.StatusCode, id, time.Time{}); err != nil {
			slog.Warn("provider attempt request-id persistence failed", "attempt_id", attemptID, "error", err)
		}
	}
}

func (b *providerAttemptBatch) latestAttempt() reconciliation.Attempt {
	if b == nil {
		return reconciliation.Attempt{}
	}
	if call := b.latest.Load(); call != nil {
		return call.attempt
	}
	return reconciliation.Attempt{}
}

func (e *Executor) recordAttemptUsage(attempt reconciliation.Attempt, usage reconciliation.UsageEvidence, body []byte, endpoint string) {
	if e == nil || e.AttemptRecorder == nil || attempt.ID == "" {
		return
	}
	if amount, currency, semantics, ok := reconciliation.ParseReportedCost(body, endpoint); ok {
		usage.ReportedAmount = &amount
		usage.ReportedCurrency = currency
		usage.ReportedSemantics = semantics
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := e.AttemptRecorder.RecordUsage(ctx, attempt.ID, usage); err != nil {
		slog.Warn("provider attempt usage persistence failed", "attempt_id", attempt.ID, "error", err)
	}
}

func (e *Executor) finishObservedStream(attempt reconciliation.Attempt, capture *audit.StreamCapture, outcome StreamOutcome, protocol string) {
	if e == nil || e.AttemptRecorder == nil || attempt.ID == "" {
		return
	}
	if capture != nil {
		u := capture.CurrentAttemptUsage()
		evidence := reconciliation.UsageEvidence{InputTokens: int64TokenPtr(u.PromptTokens), OutputTokens: int64TokenPtr(u.CompletionTokens), CacheReadTokens: int64TokenPtr(u.CacheReadTokens), CacheWriteTokens: int64TokenPtr(u.CacheWriteTokens), Source: "stream_capture_current_attempt:" + strings.ToLower(protocol)}
		e.recordAttemptUsage(attempt, evidence, nil, "")
	}
	status := "unknown"
	if outcome.Interrupted {
		status = "failure"
		if isClientStreamInterruption(outcome.Kind, outcome.Reason) {
			status = "cancelled"
		}
	} else if capture != nil {
		if done, ok := capture.SummaryAsMap()["stream_done_received"].(bool); ok && done {
			status = "success"
		}
	}
	e.finishObservedAttempt(attempt, status)
}

func (e *Executor) finishObservedAttempt(attempt reconciliation.Attempt, status string) {
	if e == nil || e.AttemptRecorder == nil || attempt.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := e.AttemptRecorder.Finish(ctx, attempt.ID, status); err != nil {
		slog.Warn("provider attempt outcome persistence failed", "attempt_id", attempt.ID, "error", err)
	}
}
