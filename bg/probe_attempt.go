package bg

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

var (
	platformAttemptMu    sync.RWMutex
	platformAttemptStore reconciliation.AttemptStore
)

// SetAttemptRecorder wires durable evidence for trusted platform probe calls.
func SetAttemptRecorder(r reconciliation.AttemptStore) {
	platformAttemptMu.Lock()
	defer platformAttemptMu.Unlock()
	platformAttemptStore = r
}

type platformProbeBindingKey struct{}

type platformProbeBinding struct {
	providerID, credentialID int
	model                    string
}

func withPlatformProbeProvider(ctx context.Context, providerID int) context.Context {
	return withPlatformProbeBinding(ctx, providerID, 0, "")
}

func withPlatformProbeBinding(ctx context.Context, providerID, credentialID int, model string) context.Context {
	return context.WithValue(ctx, platformProbeBindingKey{}, platformProbeBinding{providerID: providerID, credentialID: credentialID, model: model})
}

func platformProbeDo(ctx context.Context, client *http.Client, req *http.Request, credID int, model, source string) (*http.Response, error) {
	platformAttemptMu.RLock()
	recorder := platformAttemptStore
	platformAttemptMu.RUnlock()
	if recorder == nil {
		return client.Do(req)
	}
	binding, _ := ctx.Value(platformProbeBindingKey{}).(platformProbeBinding)
	if credID == 0 {
		credID = binding.credentialID
	}
	if binding.credentialID != 0 && binding.credentialID != credID {
		binding = platformProbeBinding{}
	}
	if binding.model != "" {
		model = binding.model
	}
	observer := &platformProbeObserver{store: recorder, original: req, base: reconciliation.Attempt{
		TrafficClass: "platform", ClientID: "platform-automation", Source: source,
		ProviderID: binding.providerID, CredentialID: credID, Model: model,
	}}
	return upstream.DoObserved(upstream.WithAttemptObserver(req, observer), client)
}

type platformProbeObserver struct {
	store    reconciliation.AttemptStore
	original *http.Request
	base     reconciliation.Attempt
	count    int
}

type platformProbeAttempt struct {
	id         string
	dispatchAt time.Time
	sentNanos  atomic.Int64
}

func (o *platformProbeObserver) write(id string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := fn(ctx); err != nil {
		slog.Warn("platform probe attempt persistence failed", "attempt_id", id, "error", err)
	}
}

func (o *platformProbeObserver) BeforeAttempt(req *http.Request) any {
	o.count++
	a := reconciliation.NewAttempt(o.base)
	a.AttemptNo = o.count
	if req.Response != nil {
		a = reconciliation.AttributeRedirect(a, o.original, req)
	}
	if upstream.IsTransportReplay(req) {
		a.Source = "transport_replay:" + a.Source
	}
	o.write(a.ID, func(ctx context.Context) error { return o.store.Prepare(ctx, a) })
	o.write(a.ID, func(ctx context.Context) error { return o.store.MarkDispatched(ctx, a.ID) })
	return &platformProbeAttempt{id: a.ID, dispatchAt: time.Now().UTC()}
}

func (o *platformProbeObserver) ObserveRequestWrite(handle any, at time.Time) {
	attempt := handle.(*platformProbeAttempt)
	if attempt.sentNanos.CompareAndSwap(0, at.UnixNano()) {
		reconciliation.JournalTransportSend(o.store, attempt.id, at)
	}
}

func (o *platformProbeObserver) AfterAttempt(req *http.Request, handle any, resp *http.Response, err error) {
	attempt := handle.(*platformProbeAttempt)
	var sent time.Time
	if n := attempt.sentNanos.Load(); n != 0 {
		sent = time.Unix(0, n).UTC()
	}
	if err != nil {
		o.write(attempt.id, func(ctx context.Context) error {
			return reconciliation.RecordTransportFailure(ctx, o.store, attempt.id, err, sent)
		})
		return
	}
	if resp == nil {
		return
	}
	if sent.IsZero() {
		sent = attempt.dispatchAt
	}
	rid := reconciliation.ProviderRequestID(resp.Header, nil)
	o.write(attempt.id, func(ctx context.Context) error {
		return o.store.RecordHTTP(ctx, attempt.id, resp.StatusCode, rid, sent)
	})
	outcome := "failure"
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		outcome = "success"
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		outcome = "unknown"
	}
	o.write(attempt.id, func(ctx context.Context) error { return o.store.Finish(ctx, attempt.id, outcome) })
	if req.Method == http.MethodPost && resp.Body != nil && resp.ContentLength <= credentialProbeBodyLimit {
		endpoint := reconciliation.ResponseEndpoint(resp)
		status := resp.StatusCode
		resp.Body = &probeEvidenceBody{ReadCloser: resp.Body, length: resp.ContentLength, complete: func(body []byte) {
			if rid == "" {
				if id := reconciliation.ProviderRequestID(nil, body); id != "" {
					o.write(attempt.id, func(ctx context.Context) error { return o.store.RecordHTTP(ctx, attempt.id, status, id, sent) })
				}
			}
			usage := reconciliation.ParseProviderResponseEvidence(body, "", endpoint)
			if usage.Source != "" || usage.ReportedAmount != nil {
				o.write(attempt.id, func(ctx context.Context) error { return o.store.RecordUsage(ctx, attempt.id, usage) })
			}
		}}
	}
}

// probeEvidenceBody observes only the bytes the probe reads. In particular, a
// reader that stops at its diagnostic limit cannot turn a prefix into evidence.
type probeEvidenceBody struct {
	io.ReadCloser
	length   int64
	body     []byte
	complete func([]byte)
}

func (b *probeEvidenceBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.complete == nil {
		return n, err
	}
	if len(b.body)+n > credentialProbeBodyLimit || (err != nil && err != io.EOF) {
		b.body, b.complete = nil, nil
		return n, err
	}
	b.body = append(b.body, p[:n]...)
	if b.length > 0 && b.length == int64(len(b.body)) || err == io.EOF && b.length <= 0 {
		complete, body := b.complete, b.body
		b.body, b.complete = nil, nil
		if len(body) != 0 {
			complete(body)
		}
	} else if err == io.EOF {
		b.body, b.complete = nil, nil
	}
	return n, err
}
