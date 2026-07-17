// Package routeincident — observer.go
//
// Bounded asynchronous observer that consumes terminal request_logs
// rows from the telemetry persistence hook and applies the state
// machine. The observer never blocks the producer: it drops the
// event when the queue is full (and increments a counter) and
// retries failed transitions with bounded backoff.
//
// IMPORTANT: this MUST be wired through
// `telemetry.AddOnRequestLogPersisted` (not the "Emitted" hook) so
// the state machine only runs on a row that is already durable in
// request_logs. The live stream SSE hub uses the "Emitted" hook
// because it only needs to render the request; the incident state
// must never be derived from a request that might roll back.
package routeincident

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// Observer drains the terminal-request stream and applies
// transitions. It is created once at startup and started by the
// gateway main; it shuts down on context cancellation.
type Observer struct {
	store  *Store
	logger *slog.Logger

	queue      chan telemetry.RequestLogEntry
	maxRetries int
	retryBase  time.Duration
	maxBackoff time.Duration

	// publish is the optional callback invoked once per
	// non-NoOp transition. The Phase-1 wiring passes a function
	// that converts the transition result to the SSE
	// `incident_update` envelope and forwards it to the dashboard
	// hub. nil means the observer is a silent side-effect of the
	// telemetry stream.
	publish func(r *TransitionResult)

	// metrics
	enqueued  atomic.Int64
	dropped   atomic.Int64 // queue full
	processed atomic.Int64
	failed    atomic.Int64 // retried, gave up
	noops     atomic.Int64
	noDB      atomic.Int64
	published atomic.Int64

	startOnce sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// ObserverConfig tunes the observer. Zero values are safe.
type ObserverConfig struct {
	QueueSize    int
	MaxRetries   int
	RetryBackoff time.Duration // base backoff, doubles each retry
	MaxBackoff   time.Duration // cap on the per-retry backoff
	Logger       *slog.Logger
	// Publish is invoked once per non-NoOp transition. The
	// function MUST be non-blocking; the typical implementation
	// is `liveStreamHub.PublishIncidentUpdate`. Pass nil to
	// disable the SSE bridge.
	Publish func(r *TransitionResult)
}

func (c *ObserverConfig) defaults() {
	if c.QueueSize <= 0 {
		c.QueueSize = 1024
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 4
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = 200 * time.Millisecond
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 5 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// NewObserver constructs the observer; the caller MUST call Start to
// begin draining and Stop to terminate. Returns nil if store is nil.
func NewObserver(store *Store, cfg ObserverConfig) *Observer {
	if store == nil {
		return nil
	}
	cfg.defaults()
	return &Observer{
		store:      store,
		logger:     cfg.Logger,
		queue:      make(chan telemetry.RequestLogEntry, cfg.QueueSize),
		maxRetries: cfg.MaxRetries,
		retryBase:  cfg.RetryBackoff,
		maxBackoff: cfg.MaxBackoff,
		publish:    cfg.Publish,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// Start launches the worker goroutine. Safe to call once; subsequent
// calls are no-ops. Stop waits for the worker only if Start was
// actually called.
func (o *Observer) Start(ctx context.Context) {
	if o == nil {
		return
	}
	o.startOnce.Do(func() {
		go o.run(ctx)
	})
}

// Stop signals shutdown and waits for the worker to drain. Safe to
// call before or after Start; safe to call twice. When Start was
// never called, Stop is a no-op (no worker to wait for).
func (o *Observer) Stop() {
	if o == nil {
		return
	}
	select {
	case <-o.stopCh:
		// Already stopped or never started (stopCh is only
		// created once at construction and we never close it
		// without starting a worker that closes doneCh).
		return
	default:
	}
	// If the worker is running it will close doneCh; if it isn't,
	// nothing to wait for. We detect the "never started" case by
	// checking startOnce via a non-blocking receive on doneCh —
	// it's never closed if Start was never called.
	close(o.stopCh)
	select {
	case <-o.doneCh:
	case <-time.After(2 * time.Second):
		// Worker didn't exit in time; don't block the caller.
	}
}

// OnPersisted is the hook callback registered against
// telemetry.AddOnRequestLogPersisted. It is intentionally
// non-blocking: if the queue is full, the event is dropped and a
// counter is bumped. We accept drops here because the request is
// already durable in request_logs — a recovery sweeper can
// re-derive state if needed. Phase 1 does not implement the
// sweeper but keeps the seam in place.
func (o *Observer) OnPersisted(entry *telemetry.RequestLogEntry) {
	if o == nil || entry == nil {
		return
	}
	if !qualifiesForIncident(entry) {
		o.noops.Add(1)
		return
	}
	o.enqueued.Add(1)
	select {
	case o.queue <- *entry:
	default:
		dropped := o.dropped.Add(1)
		if dropped%100 == 1 {
			o.logger.Warn("routeincident queue full, dropping event",
				"request_id", entry.RequestID,
				"dropped_total", dropped)
		}
	}
}

// qualifiesForIncident applies the spec invariant: client key
// errors, client cancellations, input validation failures, and
// other non-routing failures MUST NOT count toward the trigger.
// "Terminal" here means the request completed; the state machine
// is only run for rows that reached an upstream route (or were
// rejected by the gateway in a way that still tells us the route
// was attempted — failure_stage = "gateway" or "upstream").
//
// The classification is intentionally permissive in the gateway
// stage because every authenticated request that produced a
// request_log row IS an attempted route. The narrower filter
// happens here: we exclude rows whose failure_kind / failure_detail_code
// clearly indicates a non-routing cause.
//
// References to the spec invariants:
//   - "Client cancellation, input validation, authorization, and
//     other non-routing failures are excluded."
//   - "Only terminal requests that reached an upstream route qualify."
func qualifiesForIncident(e *telemetry.RequestLogEntry) bool {
	if e == nil {
		return false
	}
	// Must be a terminal request. In-progress rows have neither a
	// request_status set nor a success flag; reject them outright.
	if e.RequestStatus != nil && *e.RequestStatus == telemetry.RequestStatusInProgress {
		return false
	}
	// Must have a non-zero tenant + model; without them we cannot
	// form a route key. The observer can still observe the row
	// but Treat it as noop (we never see route-less requests on
	// the dashboard's swim lane anyway).
	if e.TenantID == "" {
		return false
	}
	if e.ClientModel == nil && e.OutboundModel == nil {
		return false
	}
	// Exclude explicitly non-routing failures. These come from the
	// classifier in errorsx; we keep the list in sync via the
	// constant below.
	if isNonRoutingFailure(e) {
		return false
	}
	return true
}

// nonRoutingFailureKinds is the spec's exclusion list. Keep it
// small and obvious: anything that signals the gateway short-
// circuited before reaching the upstream.
var nonRoutingFailureKinds = map[string]struct{}{
	"client_key_invalid":   {},
	"client_key_missing":   {},
	"client_key_disabled":  {},
	"client_cancel":        {},
	"client_disconnected":  {},
	"input_validation":     {},
	"input_too_large":      {},
	"unauthorized":         {},
	"forbidden":            {},
	"rate_limited_client":  {},
	"context_length_input": {}, // user supplied too many tokens
	// 2026-07-15: empty-stream / benign-EOF signals are upstream quirks
	// (e.g. MiniMax returns no tokens), not a routing miss. detectEmpty-
	// StreamResponse marks these on the reqLog; without excluding them a
	// 3-streak opens a spurious active incident and pollutes the swim lane.
	"empty_response":          {},
	"upstream_empty_response": {},
}

func isNonRoutingFailure(e *telemetry.RequestLogEntry) bool {
	if e == nil {
		return false
	}
	if e.FailureDetailCode != nil {
		if _, ok := nonRoutingFailureKinds[*e.FailureDetailCode]; ok {
			return true
		}
	}
	if e.ErrorKind != nil {
		_, ok := nonRoutingFailureKinds[*e.ErrorKind]
		return ok
	}
	return false
}

// run drains the queue. Each event is processed with bounded
// backoff retries on transient errors.
func (o *Observer) run(ctx context.Context) {
	defer close(o.doneCh)
	for {
		select {
		case <-o.stopCh:
			return
		case <-ctx.Done():
			return
		case entry := <-o.queue:
			o.process(ctx, entry)
		}
	}
}

func (o *Observer) process(parent context.Context, entry telemetry.RequestLogEntry) {
	in, ok := transitionInputFromEntry(&entry)
	if !ok {
		o.noops.Add(1)
		return
	}
	backoff := o.retryBase
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		res, err := o.store.Transition(ctx, in)
		cancel()
		if err == nil {
			o.processed.Add(1)
			if res != nil && !res.NoOp && o.publish != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							o.logger.Warn("routeincident publish panic",
								"incident_id", res.Update.IncidentID,
								"recover", r,
							)
						}
					}()
					o.publish(res)
					o.published.Add(1)
				}()
			}
			return
		}
		if errors.Is(err, ErrUnqualified) {
			o.noops.Add(1)
			return
		}
		if errors.Is(err, ErrNoDatabase) {
			o.noDB.Add(1)
			return
		}
		// Transient: lock conflict, transient deadlock. Backoff and
		// retry. We never abort a transition; the next persisted
		// row will catch up because each request has a unique id
		// and the state machine is monotonic.
		if attempt == o.maxRetries {
			o.failed.Add(1)
			o.logger.Warn("routeincident transition gave up after retries",
				"request_id", entry.RequestID,
				"attempts", attempt+1,
				"err", err.Error())
			return
		}
		select {
		case <-time.After(backoff):
		case <-o.stopCh:
			return
		case <-parent.Done():
			return
		}
		backoff *= 2
		if backoff > o.maxBackoff {
			backoff = o.maxBackoff
		}
	}
}

// transitionInputFromEntry maps a telemetry RequestLogEntry to the
// store's TransitionInput. It returns ok=false when the entry
// doesn't carry enough information to form a route key (e.g. a
// row that exists only because of a partial pre-persistence emit).
func transitionInputFromEntry(e *telemetry.RequestLogEntry) (TransitionInput, bool) {
	if e == nil {
		return TransitionInput{}, false
	}
	model := ""
	if e.OutboundModel != nil && *e.OutboundModel != "" {
		model = *e.OutboundModel
	} else if e.ClientModel != nil && *e.ClientModel != "" {
		model = *e.ClientModel
	}
	if model == "" {
		return TransitionInput{}, false
	}
	status := ""
	switch {
	case e.RequestStatus != nil && *e.RequestStatus != "":
		status = *e.RequestStatus
	case e.Success && (e.ErrorKind == nil || *e.ErrorKind == ""):
		status = TerminalSuccess
	case !e.Success:
		status = TerminalFailure
	}
	if status != TerminalSuccess && status != TerminalFailure {
		return TransitionInput{}, false
	}

	in := TransitionInput{
		TenantID:       e.TenantID,
		Protocol:       "openai_chat_completions", // default; resolved later when we have egress_protocol
		Model:          SanitizeModel(model),
		RequestID:      e.RequestID,
		TerminalStatus: status,
		OccurredAt:     time.Now().UTC(),
	}
	if e.CredentialID != nil {
		v := int64(*e.CredentialID)
		in.CredentialID = &v
	}
	if e.ProviderID != nil {
		v := int64(*e.ProviderID)
		in.ProviderID = &v
	}
	if e.ErrorKind != nil {
		in.FailureKind = *e.ErrorKind
	}
	if e.FailureStage != nil {
		in.FailureStage = *e.FailureStage
	}
	return in, true
}

// Stats returns a snapshot of the observer's counters for the
// /api/admin/route-incidents/stats endpoint (read-only).
type Stats struct {
	Enqueued  int64 `json:"enqueued"`
	Dropped   int64 `json:"dropped"`
	Processed int64 `json:"processed"`
	Failed    int64 `json:"failed"`
	Noops     int64 `json:"noops"`
	NoDB      int64 `json:"no_db"`
	Published int64 `json:"published"`
	QueueLen  int   `json:"queue_len"`
	QueueCap  int   `json:"queue_cap"`
}

// Stats returns the current counter snapshot. Safe for concurrent use.
func (o *Observer) Stats() Stats {
	if o == nil {
		return Stats{}
	}
	return Stats{
		Enqueued:  o.enqueued.Load(),
		Dropped:   o.dropped.Load(),
		Processed: o.processed.Load(),
		Failed:    o.failed.Load(),
		Noops:     o.noops.Load(),
		NoDB:      o.noDB.Load(),
		Published: o.published.Load(),
		QueueLen:  len(o.queue),
		QueueCap:  cap(o.queue),
	}
}

// AsHook returns a function suitable for
// telemetry.SetOnRequestLogPersisted.
func (o *Observer) AsHook() func(*telemetry.RequestLogEntry) {
	return o.OnPersisted
}
