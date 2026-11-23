package streaming

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

type modalityKeyContext struct{}

const maxModalityEvidenceBody = 1 << 20

func withModalityAttemptKey(ctx context.Context, key *authentication.KeyInfo) context.Context {
	if key == nil {
		return ctx
	}
	return context.WithValue(ctx, modalityKeyContext{}, key)
}

type modalityAttempt struct {
	evidence   reconciliation.Attempt
	dispatchAt time.Time
	sentNanos  atomic.Int64
	endpoint   string
}

// providerAttemptRecorder adds evidence to modality handlers that share the
// upstream client, including retries performed inside that client.
type providerAttemptRecorder struct {
	store    reconciliation.AttemptStore
	base     reconciliation.Attempt
	protocol string
	original *http.Request
	count    atomic.Int64
	last     atomic.Pointer[modalityAttempt]
}

var _ upstream.AttemptErrorBodyObserver = (*providerAttemptRecorder)(nil)

func observeModalityRequest(req *http.Request, store reconciliation.AttemptStore, cand provider.Candidate, tenantID, requestID, source string) (*http.Request, *providerAttemptRecorder) {
	if req == nil || store == nil {
		return req, nil
	}
	traffic, client := "unknown", ""
	key, _ := req.Context().Value(modalityKeyContext{}).(*authentication.KeyInfo)
	if key != nil && key.ID > 0 && key.TenantID != "" && requestID != "" {
		tenantID = key.TenantID
		if origin := platformOriginForKey(req.Context(), key); origin != "" {
			traffic, client = "platform", "platform-automation"
			source = "platform:" + origin + ":" + source
		} else if reconciliationBusinessKey(key) {
			traffic, client = "business", "key:"+strconv.Itoa(key.ID)
		}
	}
	if tenantID == "" {
		requestID = ""
	}
	o := &providerAttemptRecorder{
		store: store, protocol: cand.Protocol, original: req,
		base: reconciliation.Attempt{TenantID: tenantID, RequestID: requestID, TrafficClass: traffic, ClientID: client,
			Source: source, ProviderID: cand.ProviderID, CredentialID: cand.CredentialID, Model: cand.RawModel},
	}
	return upstream.WithAttemptObserver(req, o), o
}

func (o *providerAttemptRecorder) write(attemptID string, operation func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := operation(ctx); err != nil {
		slog.Warn("modality provider attempt persistence failed", "attempt_id", attemptID, "error", err)
	}
}

func (o *providerAttemptRecorder) BeforeAttempt(req *http.Request) any {
	evidence := reconciliation.NewAttempt(o.base)
	if req.Response != nil {
		evidence = reconciliation.AttributeRedirect(evidence, o.original, req)
	}
	if upstream.IsTransportReplay(req) {
		evidence.Source = "transport_replay:" + evidence.Source
	}
	call := &modalityAttempt{evidence: evidence}
	a := &call.evidence
	a.AttemptNo = int(o.count.Add(1))
	o.write(a.ID, func(ctx context.Context) error { return o.store.Prepare(ctx, *a) })
	o.write(a.ID, func(ctx context.Context) error { return o.store.MarkDispatched(ctx, a.ID) })
	call.dispatchAt = time.Now().UTC()
	o.last.Store(call)
	return call
}

func (o *providerAttemptRecorder) ObserveRequestWrite(handle any, at time.Time) {
	call, _ := handle.(*modalityAttempt)
	if call != nil && call.sentNanos.CompareAndSwap(0, at.UnixNano()) {
		reconciliation.JournalTransportSend(o.store, call.evidence.ID, at)
	}
}

func (o *providerAttemptRecorder) AfterAttempt(_ *http.Request, handle any, resp *http.Response, doErr error) {
	call, _ := handle.(*modalityAttempt)
	if call == nil {
		return
	}
	id := call.evidence.ID
	sent := call.dispatchAt
	n := call.sentNanos.Load()
	if n != 0 {
		sent = time.Unix(0, n).UTC()
	}
	if resp == nil {
		if doErr != nil {
			if n == 0 {
				sent = time.Time{}
			}
			o.write(id, func(ctx context.Context) error {
				return reconciliation.RecordTransportFailure(ctx, o.store, id, doErr, sent)
			})
		}
		return
	}
	call.endpoint = reconciliation.ResponseEndpoint(resp)
	o.write(id, func(ctx context.Context) error {
		return o.store.RecordHTTP(ctx, id, resp.StatusCode, reconciliation.ProviderRequestID(resp.Header, nil), sent)
	})
	if resp.StatusCode >= 300 {
		outcome := "unknown"
		if resp.StatusCode >= 400 {
			outcome = "failure"
		}
		o.write(id, func(ctx context.Context) error { return o.store.Finish(ctx, id, outcome) })
	}
}

func (o *providerAttemptRecorder) ObserveErrorBody(handle any, _ *http.Response, body []byte) {
	call, _ := handle.(*modalityAttempt)
	if call == nil {
		return
	}
	evidence := reconciliation.ParseProviderResponseEvidence(body, o.protocol, call.endpoint)
	if evidence.Source != "" || evidence.ReportedAmount != nil {
		id := call.evidence.ID
		o.write(id, func(ctx context.Context) error { return o.store.RecordUsage(ctx, id, evidence) })
	}
}

func (o *providerAttemptRecorder) RecordResponse(body []byte, media ...reconciliation.MediaUsageEvidence) {
	if o == nil {
		return
	}
	call := o.last.Load()
	if call == nil {
		return
	}
	a := call.evidence
	var usage reconciliation.UsageEvidence
	if len(body) <= maxModalityEvidenceBody {
		usage = reconciliation.ParseProviderResponseEvidence(body, o.protocol, call.endpoint)
	}
	if len(media) > 0 {
		usage.Media = media[0]
	}
	if usage.Source != "" || usage.ReportedAmount != nil || usage.Media.Source != "" {
		o.write(a.ID, func(ctx context.Context) error { return o.store.RecordUsage(ctx, a.ID, usage) })
	}
}

func (o *providerAttemptRecorder) Finish(outcome string) {
	if o == nil {
		return
	}
	if call := o.last.Load(); call != nil {
		id := call.evidence.ID
		o.write(id, func(ctx context.Context) error { return o.store.Finish(ctx, id, outcome) })
	}
}
