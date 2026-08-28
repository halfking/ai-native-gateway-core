package requestjourney

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultRecorderQueueCapacity = 4096
	defaultRecorderWriteTimeout  = 5 * time.Second
	defaultRecorderMaxAttempts   = 5
	defaultRecorderBackoff       = 25 * time.Millisecond
	maxTrackedJourneyRequests    = 8192
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
	journey  *JourneyEvent
	ingress  *IngressEvent
	attempts int
	due      time.Time
}

type recorderOptions struct {
	queueCapacity  int
	writeTimeout   time.Duration
	maxAttempts    int
	initialBackoff time.Duration
}

// StoreStats exposes per-store writer health for tests and admin diagnostics:
// Pending is the lag (queue + outbox depth), the rest are lifetime counters.
type StoreStats struct {
	Store        string `json:"store"`
	Pending      int64  `json:"pending"`
	Written      int64  `json:"written"`
	Replayed     int64  `json:"replayed"`
	WriteDrops   int64  `json:"write_drops"`
	EnqueueDrops int64  `json:"enqueue_drops"`
	SequenceGaps int64  `json:"sequence_gaps"`
}

type storeStats struct {
	pending      atomic.Int64
	written      atomic.Int64
	replayed     atomic.Int64
	writeDrops   atomic.Int64
	enqueueDrops atomic.Int64
	gaps         atomic.Int64
}

func (s *storeStats) snapshot(store string) StoreStats {
	return StoreStats{
		Store: store, Pending: s.pending.Load(), Written: s.written.Load(),
		Replayed: s.replayed.Load(), WriteDrops: s.writeDrops.Load(),
		EnqueueDrops: s.enqueueDrops.Load(), SequenceGaps: s.gaps.Load(),
	}
}

// seqTracker finalizes per-store sequence gaps when a journey reaches its
// terminal event: gap = terminalSeq - delivered - stillParked. Counting only
// at the terminal makes the metric immune to outbox reordering (a retried
// write that lands after later sequences is not a hole). Journeys whose key
// the tracker never saw (eviction beyond the bound, or a terminal arriving
// with no prior observation) settle conservatively to zero instead of
// guessing. Worker-goroutine-only state; bounded insertion order evicts the
// oldest tracked request when the map exceeds its limit.
type seqTracker struct {
	count map[string]int64
	order []string
}

func newSeqTracker() *seqTracker {
	return &seqTracker{count: make(map[string]int64)}
}

func (t *seqTracker) observe(event JourneyEvent, parked int64) int64 {
	key := event.TenantID + "\x1f" + event.RequestID
	delivered, seenBefore := t.count[key]
	delivered++
	if event.Stage != StageTerminal {
		if !seenBefore {
			t.track(key)
		}
		t.count[key] = delivered
		return 0
	}
	delete(t.count, key)
	if !seenBefore {
		return 0
	}
	gap := event.Seq - delivered - parked
	if gap < 0 {
		return 0
	}
	return gap
}

func (t *seqTracker) track(key string) {
	t.order = append(t.order, key)
	for len(t.order) > maxTrackedJourneyRequests {
		delete(t.count, t.order[0])
		t.order = t.order[1:]
	}
}

// parkedFor counts outbox entries still waiting for the same journey, so a
// terminal settling while an earlier sequence is mid-replay is not misread
// as a hole. Worker-goroutine-only: pending is owned by the pump.
func (p *storePump) parkedFor(event JourneyEvent) int64 {
	if len(p.pending) == 0 {
		return 0
	}
	key := event.TenantID + "\x1f" + event.RequestID
	var parked int64
	for _, write := range p.pending {
		if write.journey != nil && write.journey.TenantID+"\x1f"+write.journey.RequestID == key {
			parked++
		}
	}
	return parked
}

// storePump is one bounded FIFO worker per external store. Writes that fail
// enter an in-store outbox and replay with backoff; a pump never blocks the
// request path, it only builds lag until it drops.
type storePump struct {
	name        string // metric label: redis, postgres, redis_ingress
	displayName string // error-message prefix
	queue       chan recorderWrite

	writeFn        func(context.Context, recorderWrite) error
	onWriteDrop    func(recorderWrite, error)
	writeTimeout   time.Duration
	maxAttempts    int
	initialBackoff time.Duration

	pendingCount atomic.Int64
	pending      []recorderWrite // in-worker outbox, kept sorted by due
	stats        storeStats
	seq          *seqTracker
	done         chan struct{}
}

func (p *storePump) pendingCap() int {
	return cap(p.queue)
}

func (p *storePump) start() {
	go p.run()
}

func (p *storePump) run() {
	defer close(p.done)
	setJourneyQueueDepth(p.name, 0)
	queueOpen := true
	for {
		if write, ok := p.nextDue(); ok {
			p.deliver(write)
			continue
		}
		if !queueOpen && p.pendingCount.Load() == 0 {
			return
		}
		if len(p.pending) == 0 {
			item, ok := <-p.queue
			if !ok {
				queueOpen = false
				continue
			}
			p.stats.pending.Store(int64(len(p.queue)) + p.pendingCount.Load())
			p.deliver(item)
			continue
		}
		// Outbox head not due yet: prefer new writes so a store recovering on
		// old retries is not starved by backoff, then wait for the head.
		if queueOpen {
			timer := time.NewTimer(time.Until(p.pending[0].due))
			select {
			case item, ok := <-p.queue:
				timer.Stop()
				if !ok {
					queueOpen = false
					continue
				}
				p.deliver(item)
			case <-timer.C:
			}
			continue
		}
		<-time.After(time.Until(p.pending[0].due))
	}
}

func (p *storePump) nextDue() (recorderWrite, bool) {
	if len(p.pending) == 0 || time.Now().Before(p.pending[0].due) {
		return recorderWrite{}, false
	}
	write := p.pending[0]
	p.pending = p.pending[1:]
	p.pendingCount.Add(-1)
	return write, true
}

func (p *storePump) deliver(write recorderWrite) {
	p.updateDepth()
	ctx, cancel := context.WithTimeout(context.Background(), p.writeTimeout)
	err := p.writeFn(ctx, write)
	cancel()
	if err == nil {
		if write.attempts > 0 {
			p.stats.replayed.Add(1)
			recordJourneyReplay(p.name)
		} else {
			p.stats.written.Add(1)
		}
		if write.journey != nil && p.seq != nil {
			if gap := p.seq.observe(*write.journey, p.parkedFor(*write.journey)); gap > 0 {
				p.stats.gaps.Add(gap)
				recordJourneyGap(p.name, gap)
			}
		}
		return
	}
	write.attempts++
	if write.attempts < p.maxAttempts && len(p.pending) < p.pendingCap() {
		write.due = time.Now().Add(p.backoffDelay(write.attempts))
		p.park(write)
		return
	}
	reason := "write_failed"
	if write.attempts < p.maxAttempts {
		reason = "outbox_full"
	}
	p.stats.writeDrops.Add(1)
	recordJourneyDropWithStore(reason, p.name)
	recordJourneyDegradedWithStore(reason, p.name)
	if p.onWriteDrop != nil {
		p.onWriteDrop(write, err)
	}
}

func (p *storePump) backoffDelay(failedAttempts int) time.Duration {
	delay := p.initialBackoff
	for i := 1; i < failedAttempts && delay < time.Second; i++ {
		delay *= 2
	}
	return delay
}

// park inserts a retry into the outbox in due order so an earlier-deadline
// retry is never gated behind a later one. Worker-goroutine-only.
func (p *storePump) park(write recorderWrite) {
	index := len(p.pending)
	for index > 0 && p.pending[index-1].due.After(write.due) {
		index--
	}
	p.pending = append(p.pending, recorderWrite{})
	copy(p.pending[index+1:], p.pending[index:])
	p.pending[index] = write
	p.pendingCount.Add(1)
}

func (p *storePump) updateDepth() {
	depth := p.depthValue()
	p.stats.pending.Store(depth)
	setJourneyQueueDepth(p.name, int(depth))
}

// depthValue is safe from any goroutine: channel length and the outbox
// counter are both atomic-safe reads, unlike the worker-owned pending slice.
func (p *storePump) depthValue() int64 {
	return int64(len(p.queue)) + p.pendingCount.Load()
}

// Recorder synchronously updates the local projection and asynchronously fans
// accepted events into optional shared stores. Each store has its own bounded
// worker, so a slow PostgreSQL never starves the Redis hot projection.
type Recorder struct {
	memory      *Projection
	pumps       []*storePump
	ingressPump *storePump

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
	if options.maxAttempts <= 0 {
		options.maxAttempts = defaultRecorderMaxAttempts
	}
	if options.initialBackoff <= 0 {
		options.initialBackoff = defaultRecorderBackoff
	}
	r := &Recorder{memory: memory}
	if redisWriter != nil {
		r.pumps = append(r.pumps, newJourneyPump("redis", "Redis", options, redisWriter.Apply))
	}
	if pgWriter != nil {
		r.pumps = append(r.pumps, newJourneyPump("postgres", "PostgreSQL", options, pgWriter.Apply))
	}
	if ingressRedisWriter != nil {
		r.ingressPump = newIngressPump(options, ingressRedisWriter.ApplyIngress)
		r.pumps = append(r.pumps, r.ingressPump)
	}
	for _, pump := range r.pumps {
		pump.onWriteDrop = r.makeWriteDropHandler(pump)
		pump.start()
	}
	return r
}

func newJourneyPump(name, displayName string, options recorderOptions, apply func(context.Context, JourneyEvent) error) *storePump {
	return &storePump{
		name: name, displayName: displayName,
		queue:          make(chan recorderWrite, options.queueCapacity),
		writeFn:        func(ctx context.Context, write recorderWrite) error { return apply(ctx, *write.journey) },
		writeTimeout:   options.writeTimeout,
		maxAttempts:    options.maxAttempts,
		initialBackoff: options.initialBackoff,
		seq:            newSeqTracker(),
		done:           make(chan struct{}),
	}
}

func newIngressPump(options recorderOptions, apply func(context.Context, IngressEvent) error) *storePump {
	return &storePump{
		name: "redis_ingress", displayName: "Redis ingress",
		queue:          make(chan recorderWrite, options.queueCapacity),
		writeFn:        func(ctx context.Context, write recorderWrite) error { return apply(ctx, *write.ingress) },
		writeTimeout:   options.writeTimeout,
		maxAttempts:    options.maxAttempts,
		initialBackoff: options.initialBackoff,
		done:           make(chan struct{}),
	}
}

func (r *Recorder) makeWriteDropHandler(pump *storePump) func(recorderWrite, error) {
	return func(write recorderWrite, writeErr error) {
		if write.journey != nil {
			r.markObservationDegraded(*write.journey)
		} else if r.memory != nil {
			r.memory.MarkIngressDegraded()
		}
		r.report(fmt.Errorf("request journey %s write: %w", pump.displayName, writeErr))
	}
}

// StoreStats returns a snapshot of per-store writer health in pump order.
// Read-only: the Prometheus gauge stays owned by the worker.
func (r *Recorder) StoreStats() []StoreStats {
	if r == nil {
		return nil
	}
	result := make([]StoreStats, 0, len(r.pumps))
	for _, pump := range r.pumps {
		pump.stats.pending.Store(pump.depthValue())
		result = append(result, pump.stats.snapshot(pump.name))
	}
	return result
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
// conflicts are immediate. Accepted events fan out into each store's bounded
// queue without waiting for Redis or PostgreSQL; a store that is slow or full
// degrades only its own observation stream.
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
	var dropped []*storePump
	var enqueueErrs []error
	for _, pump := range r.journeyPumps() {
		eventCopy := cloneEvent(event)
		if err := r.enqueuePump(pump, recorderWrite{journey: &eventCopy}); err != nil {
			pump.stats.enqueueDrops.Add(1)
			dropped = append(dropped, pump)
			enqueueErrs = append(enqueueErrs, fmt.Errorf("request journey %s enqueue: %w", pump.displayName, err))
		}
	}
	if len(dropped) > 0 {
		r.markObservationDegraded(event)
	}
	r.applyMu.Unlock()

	for i, pump := range dropped {
		recordJourneyDropWithStore(enqueueReasonPump(enqueueErrs[i]), pump.name)
		recordJourneyDegradedWithStore(enqueueReasonPump(enqueueErrs[i]), pump.name)
		r.report(enqueueErrs[i])
	}
	return nil
}

// MaxSeq returns the highest sequence number currently held in memory for
// (tenantID, requestID), or 0 if no journey exists. Used by the dispatch
// journal sink to assign monotonic seq numbers to the attempt-journal
// events it ships at terminal time without colliding with the seqs already
// used by the observation path. Best-effort peek — memory is the local
// view; persistence stores may lag, but the projection's seq-monotonic
// invariant is what guards correctness for the seq space.
func (r *Recorder) MaxSeq(tenantID, requestID string) int64 {
	if r == nil || r.memory == nil {
		return 0
	}
	journey, err := r.memory.Detail(tenantID, requestID)
	if err != nil || journey == nil || len(journey.Events) == 0 {
		return 0
	}
	return journey.Events[len(journey.Events)-1].Seq
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
	ingressPump := r.ingressPump
	if ingressPump != nil {
		eventCopy := event
		err := r.enqueuePump(ingressPump, recorderWrite{ingress: &eventCopy})
		if err != nil {
			ingressPump.stats.enqueueDrops.Add(1)
			enqueueErr = fmt.Errorf("request journey %s enqueue: %w", ingressPump.displayName, err)
		}
	}
	r.applyMu.Unlock()
	if enqueueErr != nil {
		if r.memory != nil {
			r.memory.MarkIngressDegraded()
		}
		recordJourneyDropWithStore(enqueueReasonPump(enqueueErr), "redis_ingress")
		recordJourneyDegradedWithStore(enqueueReasonPump(enqueueErr), "redis_ingress")
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

// Close rejects new external writes, drains queued writes and pending outbox
// retries, and waits until every store worker exits or ctx expires. It is safe
// to call concurrently and repeatedly.
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
		for _, pump := range r.pumps {
			close(pump.queue)
		}
	}
	r.stateMu.Unlock()

	for _, pump := range r.pumps {
		select {
		case <-pump.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (r *Recorder) journeyPumps() []*storePump {
	var pumps []*storePump
	for _, pump := range r.pumps {
		if pump.name == "redis" || pump.name == "postgres" {
			pumps = append(pumps, pump)
		}
	}
	return pumps
}

func (r *Recorder) enqueuePump(pump *storePump, write recorderWrite) error {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	if r.closed {
		return ErrRecorderClosed
	}
	select {
	case pump.queue <- write:
		return nil
	default:
		return ErrRecorderQueueFull
	}
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

func enqueueReasonPump(err error) string {
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

// RetryScheduled records the retry boundary between two attempts of a
// retried request: the wrapped handler attempt already emitted its own
// terminal, and this event explains why another attempt follows it. Emission
// requires a trusted tenant; there is deliberately no terminal guard.
func (l *Lifecycle) RetryScheduled(ctx context.Context, retryReason string) {
	if l == nil || strings.TrimSpace(retryReason) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tenantID == "" {
		return
	}
	l.emitLocked(ctx, JourneyEvent{
		Type:        EventRetryScheduled,
		Stage:       StageRetrying,
		RetryReason: retryReason,
	})
}

// SeedSequence raises the sequence base to highWater when it is above the
// current value. A retry wrapper re-invokes the whole handler per attempt, so
// each attempt owns a fresh lifecycle; seeding it from the previous attempt's
// high-water mark keeps one strictly increasing sequence per request instead
// of restarting at 1 and being rejected as a sequence conflict.
func (l *Lifecycle) SeedSequence(highWater int64) {
	if l == nil || highWater <= 0 {
		return
	}
	for {
		current := l.seq.Load()
		if highWater <= current || l.seq.CompareAndSwap(current, highWater) {
			return
		}
	}
}

// SequenceHighWater returns the last allocated sequence for this lifecycle.
// Retry wrappers thread it into the next attempt's lifecycle via SeedSequence.
func (l *Lifecycle) SequenceHighWater() int64 {
	if l == nil {
		return 0
	}
	return l.seq.Load()
}

// ArrivalTime returns the request's original ingress arrival. Retry wrappers
// thread it into the next attempt's lifecycle so re-entry updates the same
// ingress record instead of being rejected as an identity change (memory
// projection) or duplicated as a second FIFO entry (Redis).
func (l *Lifecycle) ArrivalTime() time.Time {
	if l == nil {
		return time.Time{}
	}
	return l.receivedAt
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
