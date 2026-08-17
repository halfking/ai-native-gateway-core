package requestjourney

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultRecorderQueueCapacity = 4096
	defaultRecorderWriteTimeout  = 5 * time.Second
)

var (
	ErrRecorderQueueFull = errors.New("request journey recorder queue is full")
	ErrRecorderClosed    = errors.New("request journey recorder is closed")
)

type journeyEventWriter interface {
	Apply(context.Context, JourneyEvent) error
}

type ingressEventWriter interface {
	ApplyIngress(context.Context, IngressEvent) error
}

type recorderWrite struct {
	journey *JourneyEvent
	ingress *IngressEvent
}

type recorderOptions struct {
	queueCapacity int
	writeTimeout  time.Duration
}

// Recorder synchronously updates the local projection and asynchronously fans
// accepted events into optional shared stores through one bounded FIFO worker.
type Recorder struct {
	memory       *Projection
	redis        journeyEventWriter
	ingressRedis ingressEventWriter
	pg           journeyEventWriter
	queue        chan recorderWrite
	writeTimeout time.Duration
	done         chan struct{}

	applyMu sync.Mutex
	stateMu sync.RWMutex
	closed  bool

	handlerMu sync.RWMutex
	onError   func(error)
}

func NewRecorder(memory *Projection, redisStore *RedisStore, pg *PostgresRepository) *Recorder {
	var redisWriter journeyEventWriter
	if redisStore != nil && redisStore.client != nil {
		redisWriter = redisStore
	}
	var ingressRedisWriter ingressEventWriter
	if redisStore != nil && redisStore.client != nil {
		ingressRedisWriter = redisStore
	}
	var pgWriter journeyEventWriter
	if pg != nil && pg.db != nil {
		pgWriter = pg
	}
	return newRecorderWithIngress(memory, redisWriter, ingressRedisWriter, pgWriter, recorderOptions{})
}

func newRecorder(memory *Projection, redisWriter, pgWriter journeyEventWriter, options recorderOptions) *Recorder {
	return newRecorderWithIngress(memory, redisWriter, nil, pgWriter, options)
}

func newRecorderWithIngress(memory *Projection, redisWriter journeyEventWriter, ingressRedisWriter ingressEventWriter, pgWriter journeyEventWriter, options recorderOptions) *Recorder {
	if options.queueCapacity <= 0 {
		options.queueCapacity = defaultRecorderQueueCapacity
	}
	if options.writeTimeout <= 0 {
		options.writeTimeout = defaultRecorderWriteTimeout
	}
	r := &Recorder{
		memory:       memory,
		redis:        redisWriter,
		ingressRedis: ingressRedisWriter,
		pg:           pgWriter,
		queue:        make(chan recorderWrite, options.queueCapacity),
		writeTimeout: options.writeTimeout,
		done:         make(chan struct{}),
	}
	go r.run()
	return r
}

// SetErrorHandler atomically replaces the optional observation error callback.
// The callback must return promptly because queue rejection is reported inline.
func (r *Recorder) SetErrorHandler(handler func(error)) {
	if r == nil {
		return
	}
	r.handlerMu.Lock()
	r.onError = handler
	r.handlerMu.Unlock()
}

// Apply validates and updates memory synchronously, so local reads and sequence
// conflicts are immediate. Accepted events are enqueued without waiting for
// Redis or PostgreSQL; asynchronous failures are reported to the error handler.
func (r *Recorder) Apply(_ context.Context, event JourneyEvent) error {
	if r == nil {
		return errors.New("request journey recorder is nil")
	}
	if err := event.Validate(); err != nil {
		return err
	}

	r.applyMu.Lock()
	if r.memory != nil {
		if err := r.memory.Apply(event); err != nil {
			r.applyMu.Unlock()
			return err
		}
	}
	var enqueueErr error
	if r.redis != nil || r.pg != nil {
		eventCopy := cloneEvent(event)
		enqueueErr = r.enqueue(recorderWrite{journey: &eventCopy})
	}

	if enqueueErr != nil {
		r.markObservationDegraded(event)
	}
	r.applyMu.Unlock()

	if enqueueErr != nil {
		recordJourneyDrop(enqueueReason(enqueueErr))
		recordJourneyDegraded(enqueueReason(enqueueErr))
		r.report(enqueueErr)
	}
	return nil
}

// RecordIngress synchronously updates the local global FIFO and only queues the
// optional Redis write. Redis latency and request cancellation never block the
// request path.
func (r *Recorder) RecordIngress(_ context.Context, event IngressEvent) error {
	if r == nil {
		return errors.New("request journey recorder is nil")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	r.applyMu.Lock()
	if r.memory != nil {
		if err := r.memory.ApplyIngress(event); err != nil {
			r.applyMu.Unlock()
			return err
		}
	}
	var enqueueErr error
	if r.ingressRedis != nil {
		eventCopy := event
		enqueueErr = r.enqueue(recorderWrite{ingress: &eventCopy})
	}
	r.applyMu.Unlock()
	if enqueueErr != nil {
		if r.memory != nil {
			r.memory.MarkIngressDegraded()
		}
		recordJourneyDrop(enqueueReason(enqueueErr))
		r.report(enqueueErr)
	}
	return nil
}

// EmitJourneyEvent adapts Recorder to dispatch.EventSink without importing the
// dispatch package. Observation failures cannot change request execution.
func (r *Recorder) EmitJourneyEvent(ctx context.Context, event JourneyEvent) {
	if r == nil {
		return
	}
	if err := r.Apply(ctx, event); err != nil {
		r.report(err)
	}
}

// Close rejects new external writes, drains queued writes, and waits until the
// worker exits or ctx expires. It is safe to call concurrently and repeatedly.
func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.stateMu.Lock()
	if !r.closed {
		r.closed = true
		close(r.queue)
	}
	r.stateMu.Unlock()

	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Recorder) enqueue(write recorderWrite) error {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	if r.closed {
		return ErrRecorderClosed
	}
	select {
	case r.queue <- write:
		return nil
	default:
		return ErrRecorderQueueFull
	}
}

func (r *Recorder) run() {
	defer close(r.done)
	for write := range r.queue {
		if write.journey != nil {
			r.write("postgres", r.pg, *write.journey)
			r.write("redis", r.redis, *write.journey)
		}
		if write.ingress != nil {
			r.writeIngress(*write.ingress)
		}
	}
}

func (r *Recorder) writeIngress(event IngressEvent) {
	if r.ingressRedis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.writeTimeout)
	err := r.ingressRedis.ApplyIngress(ctx, event)
	cancel()
	if err != nil {
		if r.memory != nil {
			r.memory.MarkIngressDegraded()
		}
		r.report(fmt.Errorf("request journey Redis ingress write: %w", err))
	}
}

func (r *Recorder) write(storeName string, writer journeyEventWriter, event JourneyEvent) {
	if writer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.writeTimeout)
	err := writer.Apply(ctx, event)
	cancel()
	if err == nil {
		return
	}
	r.markObservationDegraded(event)
	recordJourneyDegraded(storeName)
	r.report(fmt.Errorf("request journey %s write: %w", storeName, err))
}

func (r *Recorder) report(err error) {
	if err == nil {
		return
	}
	r.handlerMu.RLock()
	handler := r.onError
	r.handlerMu.RUnlock()
	if handler != nil {
		handler(err)
	}
}

func enqueueReason(err error) string {
	if errors.Is(err, ErrRecorderClosed) {
		return "closed"
	}
	return "queue_full"
}

func (r *Recorder) markObservationDegraded(event JourneyEvent) {
	if r.memory == nil {
		return
	}
	p := r.memory
	p.mu.Lock()
	defer p.mu.Unlock()
	tenant := p.tenants[event.TenantID]
	if tenant == nil {
		return
	}
	journey := tenant.details[event.RequestID]
	if journey == nil {
		return
	}
	journey.ObservationStatus = ObservationDegraded
	markProjectionSnapshotsDegraded(tenant.total, event.RequestID)
	for _, snapshots := range tenant.models {
		markProjectionSnapshotsDegraded(snapshots, event.RequestID)
	}
	for _, snapshots := range tenant.nodes {
		markProjectionSnapshotsDegraded(snapshots, event.RequestID)
	}
}

func markProjectionSnapshotsDegraded(snapshots *orderedSnapshots, requestID string) {
	if snapshots == nil {
		return
	}
	for key, snapshot := range snapshots.items {
		if snapshot.RequestID == requestID {
			snapshot.ObservationStatus = ObservationDegraded
			snapshots.items[key] = snapshot
		}
	}
}

// Lifecycle owns the handler-side events for one request. Dispatch receives the
// same sequence and terminal atomics so both producers form one ordered stream.
type Lifecycle struct {
	recorder          *Recorder
	gatewayInstanceID string
	requestID         string

	mu              sync.Mutex
	tenantID        string
	requestedModel  string
	resolvedModel   string
	failureKind     string
	receivedAt      time.Time
	protocol        IngressProtocol
	pathClass       IngressPathClass
	received        bool
	routeResolved   bool
	seq             atomic.Int64
	terminal        atomic.Bool
	ingressTerminal atomic.Bool
}

func NewLifecycle(recorder *Recorder, gatewayInstanceID, requestID string) *Lifecycle {
	return &Lifecycle{
		recorder:          recorder,
		gatewayInstanceID: gatewayInstanceID,
		requestID:         requestID,
		receivedAt:        time.Now(),
	}
}

// NewIngressLifecycle records the transport arrival before any authentication
// or body parsing and returns the lifecycle used by later trusted stages.
func NewIngressLifecycle(recorder *Recorder, gatewayInstanceID, requestID string, protocol IngressProtocol, pathClass IngressPathClass, arrivedAt time.Time) *Lifecycle {
	if arrivedAt.IsZero() {
		arrivedAt = time.Now()
	}
	lifecycle := &Lifecycle{
		recorder: recorder, gatewayInstanceID: gatewayInstanceID, requestID: requestID,
		receivedAt: arrivedAt, protocol: protocol, pathClass: pathClass,
	}
	lifecycle.recordIngress(context.Background(), IngressStatusArrived, "", 0, arrivedAt)
	return lifecycle
}

// BindTenant emits request_received the first time a trusted tenant is known.
// Supplying a model here preserves the original client model on later events.
func (l *Lifecycle) BindTenant(ctx context.Context, tenantID, requestedModel string) {
	if l == nil || tenantID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tenantID == "" {
		l.tenantID = tenantID
	}
	if l.tenantID != tenantID {
		return
	}
	if requestedModel != "" && l.requestedModel == "" {
		l.requestedModel = requestedModel
	}
	if !l.received {
		l.received = true
		l.emitLocked(ctx, JourneyEvent{Type: EventRequestReceived, Stage: StageReceived, OccurredAt: l.receivedAt})
	}
}

func (l *Lifecycle) MarkFailure(errorKind string) {
	if l == nil || errorKind == "" {
		return
	}
	l.mu.Lock()
	if l.failureKind == "" {
		l.failureKind = errorKind
	}
	l.mu.Unlock()
}

func (l *Lifecycle) FailureKind() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failureKind
}

func (l *Lifecycle) RouteResolved(ctx context.Context, tenantID, requestedModel, resolvedModel string) {
	if l == nil || resolvedModel == "" {
		return
	}
	l.BindTenant(ctx, tenantID, requestedModel)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.routeResolved || l.tenantID == "" {
		return
	}
	if l.requestedModel == "" {
		l.requestedModel = requestedModel
	}
	l.resolvedModel = resolvedModel
	l.routeResolved = true
	l.emitLocked(ctx, JourneyEvent{
		Type:          EventRouteResolved,
		Stage:         StageRouting,
		ResolvedModel: resolvedModel,
	})
}

// Finish completes global ingress and, only when a trusted tenant was already
// bound, emits the tenant journey terminal. It never infers a tenant.
func (l *Lifecycle) Finish(ctx context.Context, outcome Outcome, errorKind string, httpStatus int) {
	if l == nil {
		return
	}
	status := IngressStatusSucceeded
	if outcome == OutcomeCanceled {
		status = IngressStatusCanceled
	} else if outcome != OutcomeSuccess {
		status = IngressStatusFailed
		outcome = OutcomeFailure
	}
	if l.ingressTerminal.CompareAndSwap(false, true) {
		l.recordIngress(ctx, status, errorKind, httpStatus, time.Now())
	}
	if !l.terminal.CompareAndSwap(false, true) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tenantID == "" {
		return
	}
	eventType := EventRequestSucceeded
	if outcome == OutcomeCanceled {
		eventType = EventRequestCanceled
	} else if outcome != OutcomeSuccess {
		eventType = EventRequestFailed
	}
	l.emitLocked(ctx, JourneyEvent{
		Type:       eventType,
		Stage:      StageTerminal,
		Outcome:    outcome,
		ErrorKind:  errorKind,
		HTTPStatus: httpStatus,
	})
}

// Terminal is retained for existing callers. tenantID is deliberately ignored
// for binding: only BindTenant can establish trusted tenant identity.
func (l *Lifecycle) Terminal(ctx context.Context, tenantID string, outcome Outcome, errorKind string, httpStatus int) {
	l.Finish(ctx, outcome, errorKind, httpStatus)
}

func (l *Lifecycle) recordIngress(ctx context.Context, status IngressStatus, errorKind string, httpStatus int, updatedAt time.Time) {
	if l == nil || l.recorder == nil || !l.protocol.Valid() || !l.pathClass.Valid() {
		return
	}
	if updatedAt.Before(l.receivedAt) {
		updatedAt = l.receivedAt
	}
	if err := l.recorder.RecordIngress(ctx, IngressEvent{
		RequestID: l.requestID, GatewayInstanceID: l.gatewayInstanceID,
		Protocol: l.protocol, PathClass: l.pathClass,
		ArrivedAt: l.receivedAt, UpdatedAt: updatedAt, Status: status,
		ErrorKind: errorKind, HTTPStatus: httpStatus,
	}); err != nil {
		l.recorder.report(err)
	}
}

func (l *Lifecycle) Sequence() *atomic.Int64 {
	if l == nil {
		return nil
	}
	return &l.seq
}

func (l *Lifecycle) TerminalState() *atomic.Bool {
	if l == nil {
		return nil
	}
	return &l.terminal
}

func (l *Lifecycle) GatewayInstanceID() string {
	if l == nil {
		return ""
	}
	return l.gatewayInstanceID
}

func (l *Lifecycle) emitLocked(ctx context.Context, event JourneyEvent) {
	if l.recorder == nil || l.tenantID == "" || l.gatewayInstanceID == "" || l.requestID == "" {
		return
	}
	event.TenantID = l.tenantID
	event.GatewayInstanceID = l.gatewayInstanceID
	event.RequestID = l.requestID
	event.Seq = l.seq.Add(1)
	event.RequestedModel = l.requestedModel
	if event.ResolvedModel == "" {
		event.ResolvedModel = l.resolvedModel
	}
	event.ObservationStatus = ObservationComplete
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	l.recorder.EmitJourneyEvent(context.WithoutCancel(ctx), event)
}
