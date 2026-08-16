package dispatch

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// RouteFunc returns the ranked, availability-filtered credentials for the
// request's resolved model, EXCLUDING credentials in qr.TriedCredentials.
// Best candidate first. The executor implements this via
// Router.PlanCandidatesWithContext, mapping provider.Candidate → CredentialRef.
type RouteFunc func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error)

// ModelResolveFunc resolves a requested model (possibly "auto" or empty) to a
// concrete routable model and returns alternative models for model-change.
// tried are models already exhausted in this request and must be excluded
// from alternatives. If `requested` is already concrete and routable,
// resolved == requested. The executor implements this via autoroute.
type ModelResolveFunc func(ctx context.Context, requested string, tried []string) (resolved string, alternatives []string, err error)

// ModelRecommendFunc returns failure-time model alternatives in recommendation
// order. Production implements this with autoroute.Decider/Index.RecommendV2.
type ModelRecommendFunc func(ctx context.Context, qr *QueuedRequest, tried []string) (alternatives []string, err error)

// ForwardFunc performs one upstream forward attempt for (qr, cred) and returns
// the outcome. BytesSent MUST be accurate (first-byte boundary) so the mover
// can decide retry-vs-terminal. The executor implements this via the extracted
// forwardOneCandidate.
type ForwardFunc func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome

// Deps bundles the callbacks the executor supplies to the pipeline.
type Deps struct {
	RouteFunc          RouteFunc
	ModelResolveFunc   ModelResolveFunc
	ModelRecommendFunc ModelRecommendFunc
	ForwardFunc        ForwardFunc
	// AllowModelChange enables model-failover when all credentials under the
	// current model are exhausted. AllowModelChangeFunc, when non-nil, is read
	// at failover time so admin settings apply without rebuilding the Pipeline.
	AllowModelChange     bool
	AllowModelChangeFunc func() bool
	// HotCfg is read once at construction; live reload re-reads via Reload.
	HotCfg *atomic.Value // *Config; may be nil → DefaultConfig
}

// Pipeline is the multi-tier dispatch core. Construct once, Start, then Submit.
type Pipeline struct {
	routeFunc            RouteFunc
	modelResolveFunc     ModelResolveFunc
	modelRecommendFunc   ModelRecommendFunc
	forwardFunc          ForwardFunc
	allowModelChange     bool
	allowModelChangeFunc func() bool

	cfg atomic.Value // *Config

	// Tier-1: per-model queues + drainers feeding dispatchIn.
	modelMu sync.Mutex
	models  map[string]*modelQueue

	// dispatchIn is fed by model-queue drainers; consumed by ① dispatcher workers.
	dispatchIn chan *QueuedRequest

	// Tier-2: per-credential forwarders.
	credMu     sync.Mutex
	forwarders map[int]*credForwarder

	// ③ failover channel.
	failoverCh chan failoverItem

	stopCh   chan struct{}
	wg       sync.WaitGroup // dispatcher + drainer + failover goroutines
	started  atomic.Bool
	shutdown atomic.Bool

	// V3.1 waterfall ring (recent completed request timelines for admin UI).
	waterfallOnce sync.Once
	waterfall     *waterfallRing

	// liveActions (2026-08-15, V3.3-OBS OBS-B1) 请求生命周期动作事件发射器
	// （model_enqueued / node_enqueued / node_switch / model_switch / no_route，
	// docs/会话优化v3/24 §2）。nil 安全（Emit 对 nil receiver 是 no-op），
	// 旁路异步，热路径零阻塞。
	liveActions *liveactions.Emitter
}

// SetLiveActions wires the request-lifecycle action-event emitter (V3.3-OBS
// OBS-B1). Call before/after Start — emit sites are nil-safe either way.
func (p *Pipeline) SetLiveActions(e *liveactions.Emitter) {
	if p == nil {
		return
	}
	p.liveActions = e
}

// NewPipeline constructs a pipeline. Call Start before Submit.
func NewPipeline(deps Deps) *Pipeline {
	p := &Pipeline{
		routeFunc:            deps.RouteFunc,
		modelResolveFunc:     deps.ModelResolveFunc,
		modelRecommendFunc:   deps.ModelRecommendFunc,
		forwardFunc:          deps.ForwardFunc,
		allowModelChange:     deps.AllowModelChange,
		allowModelChangeFunc: deps.AllowModelChangeFunc,
		models:               make(map[string]*modelQueue),
		forwarders:           make(map[int]*credForwarder),
		stopCh:               make(chan struct{}),
	}
	cfg := DefaultConfig()
	if deps.HotCfg != nil {
		if v, ok := deps.HotCfg.Load().(*Config); ok && v != nil {
			cfg = *v
		}
	}
	p.cfg.Store(&cfg)
	return p
}

func (p *Pipeline) modelChangeEnabled() bool {
	if p == nil {
		return false
	}
	if p.allowModelChangeFunc != nil {
		return p.allowModelChangeFunc()
	}
	return p.allowModelChange
}

// Reload swaps the live config (hot-reload). Worker/forwarder counts do not
// resize live, but queue-wait budgets and retry budgets pick up immediately.
func (p *Pipeline) Reload(cfg Config) {
	p.cfg.Store(&cfg)
}

func (p *Pipeline) config() Config {
	if v, ok := p.cfg.Load().(*Config); ok && v != nil {
		return *v
	}
	return DefaultConfig()
}

// Start launches the dispatcher and failover worker pools. Idempotent.
func (p *Pipeline) Start() {
	if !p.started.CompareAndSwap(false, true) {
		return
	}
	cfg := p.config()
	p.dispatchIn = make(chan *QueuedRequest, cfg.DispatcherWorkers*4)
	p.failoverCh = make(chan failoverItem, cfg.FailoverWorkers*4)
	for i := 0; i < cfg.DispatcherWorkers; i++ {
		p.wg.Add(1)
		go p.runDispatcher()
	}
	for i := 0; i < cfg.FailoverWorkers; i++ {
		p.wg.Add(1)
		go p.runFailover()
	}
}

// Stop drains workers. After Stop, Submit returns ErrShutdown.
//
// We do NOT close the model-queue channels: senders in enqueueModel send
// outside modelMu, so closing would race with an in-flight send and panic.
// Drainers exit via stopCh instead; the channels are GC'd with the pipeline.
func (p *Pipeline) Stop() {
	if !p.shutdown.CompareAndSwap(false, true) {
		return
	}
	close(p.stopCh)
	p.modelMu.Lock()
	p.models = map[string]*modelQueue{}
	p.modelMu.Unlock()
	p.credMu.Lock()
	for _, cf := range p.forwarders {
		cf.cancel()
	}
	p.forwarders = map[int]*credForwarder{}
	p.credMu.Unlock()
	p.wg.Wait()
}

// Submit enqueues a request into the Tier-1 model queue and blocks until the
// pipeline completes the request (success/terminal-failure) or ctx expires.
// Returns (opaqueResult, nil) on success, (nil, err) otherwise.
func (p *Pipeline) Submit(ctx context.Context, qr *QueuedRequest) (any, error) {
	if p.shutdown.Load() {
		return nil, ErrShutdown
	}
	if !p.started.Load() {
		return nil, ErrShutdown
	}
	qr.Ctx = ctx
	if qr.EnqueuedAt.IsZero() {
		qr.EnqueuedAt = time.Now()
	}

	// V3.1: Record T1 timestamp (total queue enqueue)
	qr.SetT1_TotalEnqueued()

	modelKey := queueKeyFor(qr.RequestedModel)
	if !p.enqueueModel(modelKey, qr) {
		if p.shutdown.Load() {
			// Stop raced us between the entry check and admission: report
			// shutdown, not a misleading queue-full overflow.
			return nil, ErrShutdown
		}
		metricOverflow.WithLabelValues("model_queue_full").Inc()
		return nil, ErrOverflow
	}
	select {
	case out := <-qr.ResultCh:
		return out.Result, out.Err
	case <-ctx.Done():
		select {
		case out := <-qr.ResultCh:
			return out.Result, out.Err
		default:
		}
		qr.abandoned.Store(true)
		return nil, ctx.Err()
	}
}

// queueKeyFor normalizes the requested model into a Tier-1 queue key. Empty or
// "auto" (case-insensitive) collapse to the "auto" bucket resolved by the
// dispatcher.
func queueKeyFor(requested string) string {
	if requested == "" {
		return "auto"
	}
	if isAutoModel(requested) {
		return "auto"
	}
	return requested
}

func isAutoModel(m string) bool {
	if len(m) != 4 {
		return false
	}
	// case-insensitive "auto"
	return (m[0] == 'a' || m[0] == 'A') &&
		(m[1] == 'u' || m[1] == 'U') &&
		(m[2] == 't' || m[2] == 'T') &&
		(m[3] == 'o' || m[3] == 'O')
}

// enqueueModel pushes qr into the named model queue, creating it (and its
// drainer goroutine) on first use. Returns false if the model queue is full
// (overflow) or the pipeline is shutting down (admission refused).
func (p *Pipeline) enqueueModel(name string, qr *QueuedRequest) bool {
	mq := p.getOrCreateModelQueue(name)
	if mq == nil {
		return false
	}
	select {
	case mq.ch <- qr:
		mq.depth.Add(1)
		metricModelQueueDepth.WithLabelValues(name).Inc()
		// V3.3-OBS OBS-B1 (2026-08-15): model_enqueued 动作事件（S4）。
		p.liveActions.Emit(ctxOf(qr), liveactions.ActionEvent{
			RequestID: qr.ID,
			Action:    liveactions.ActionModelEnqueued,
			Model:     name,
			Detail: map[string]string{
				"queue_depth": strconv.FormatInt(mq.depth.Load(), 10),
			},
		})
		return true
	default:
		return false
	}
}

func (p *Pipeline) getOrCreateModelQueue(name string) *modelQueue {
	p.modelMu.Lock()
	defer p.modelMu.Unlock()
	if mq, ok := p.models[name]; ok {
		return mq
	}
	// After Stop, do not spawn new drainers (Stop's wg.Wait may already be
	// running; a late wg.Add would race it) and do NOT hand back a throwaway
	// queue: enqueueModel would "succeed" into a channel nobody drains, so
	// the qr would never complete and its Submit caller would block forever
	// (concurrency audit 2026-08-13 D5). Return nil — enqueueModel then fails
	// admission and the caller completes the qr with ErrShutdown.
	if p.shutdown.Load() {
		return nil
	}
	mq := &modelQueue{name: name, ch: make(chan *QueuedRequest, p.config().MaxQueueDepth)}
	p.models[name] = mq
	p.wg.Add(1)
	go p.runModelDrainer(mq)
	return mq
}

// runModelDrainer forwards items from a per-model queue into the shared
// dispatchIn, preserving per-model FIFO and providing per-model backpressure.
//
// It reads via select+stopCh (NOT `for range`) so Stop() never has to close
// mq.ch. Closing mq.ch would race with concurrent senders in enqueueModel
// (which send outside modelMu) and panic on send-to-closed. The channel is
// simply left to be garbage-collected with the pipeline.
//
// CRITICAL: after popping a qr from mq.ch, the drainer MUST complete it
// (with ErrShutdown) if stopCh wins the inner select. Otherwise qr is
// silently dropped — its Submit caller blocks forever on qr.ResultCh and
// the goroutine leaks. See TestStopNoDrainLoss.
func (p *Pipeline) runModelDrainer(mq *modelQueue) {
	defer p.wg.Done()
	for {
		select {
		case qr := <-mq.ch:
			mq.depth.Add(-1)
			metricModelQueueDepth.WithLabelValues(mq.name).Dec()

			// V3.1: Record T2 timestamp (model queue dequeue, routing start)
			qr.SetT2_TotalDequeued()

			wait := time.Since(qr.EnqueuedAt).Seconds()
			metricModelQueueWait.WithLabelValues(mq.name).Observe(wait)

			// V3.1: Record T3 timestamp (enqueue to dispatcher - model resolution queue)
			qr.SetT3_ModelEnqueued()

			select {
			case p.dispatchIn <- qr:
			case <-p.stopCh:
				// qr was already popped from mq.ch, depth/metrics already
				// decremented; we MUST report outcome to the Submit caller
				// or it leaks forever waiting on qr.ResultCh.
				p.complete(qr, ForwardOutcome{Err: ErrShutdown})
				return
			}
		case <-p.stopCh:
			return
		}
	}
}

// complete delivers the outcome to the Submit caller exactly once. Safe to
// call from any executor goroutine.
func (p *Pipeline) complete(qr *QueuedRequest, out ForwardOutcome) {
	if !qr.completed.CompareAndSwap(false, true) {
		return
	}

	// V3.1: Record T9 timestamp (response end - stream completed)
	qr.SetT9_ResponseEnd()

	// V3.1: Export stage histograms + waterfall ring even if the caller
	// already left — abandoned requests still carry useful latency signal.
	p.recordStageMetrics(qr, out)

	if qr.abandoned.Load() {
		return // caller already left; drop
	}
	select {
	case qr.ResultCh <- out:
	default:
		// cap-1 channel with a single reader; shouldn't happen. Drain-safe.
	}
}

// resultLabel maps a ForwardOutcome to the closed-enum "result" label used by
// V3.1 stage histograms.
func resultLabel(out ForwardOutcome) string {
	if out.Err == nil {
		return "success"
	}
	if IsShutdown(out.Err) {
		return "shutdown"
	}
	if out.BytesSent {
		return "fail_postfirstbyte"
	}
	return "fail_prefirstbyte"
}

// recordStageMetrics observes the 9 V3.1 lifecycle histograms for one completed
// request. Missing timestamps are skipped (early complete / failover paths).
// When called via Pipeline.complete, also pushes a waterfall ring sample.
func (p *Pipeline) recordStageMetrics(qr *QueuedRequest, out ForwardOutcome) {
	observeStageMetrics(qr, out)
	p.recordWaterfall(qr, out)
}

// observeStageMetrics is the package-level histogram observer (testable without
// a full Pipeline).
func observeStageMetrics(qr *QueuedRequest, out ForwardOutcome) {
	result := resultLabel(out)

	if s, ok := stageSecondsFrom(qr.T0_ArrivedAt, qr.T6_CredDequeuedAt); ok {
		metricStageQueueWaitT0T6.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.T1_TotalEnqueuedAt, qr.T2_TotalDequeuedAt); ok {
		metricStageTotalQueueT1T2.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.T3_ModelEnqueuedAt, qr.T4_ModelDequeuedAt); ok {
		metricStageModelQueueT3T4.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.T5_CredEnqueuedAt, qr.T6_CredDequeuedAt); ok {
		metricStageCredQueueT5T6.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.T2_TotalDequeuedAt, qr.T5_CredEnqueuedAt); ok {
		metricStageRoutingT2T5.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.T6_CredDequeuedAt, qr.T7_ForwardStartAt); ok {
		metricStageAcquireT6T7.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.T7_ForwardStartAt, qr.T8_ResponseStartAt); ok {
		metricStageUpstreamT7T8.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.T8_ResponseStartAt, qr.T9_ResponseEndAt); ok {
		metricStageStreamingT8T9.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSecondsFrom(qr.T0_ArrivedAt, qr.T9_ResponseEndAt); ok {
		metricStageTotalT0T9.WithLabelValues(result).Observe(s)
	}
}

// routeFailover hands a pre-firstbyte failure to the ③ mover. fatalCredential
// mirrors ForwardOutcome.FatalCredential so the mover can skip the
// same-credential retry ladder for credential-fatal errors.
func (p *Pipeline) routeFailover(qr *QueuedRequest, err error, fatalCredential bool) {
	select {
	case p.failoverCh <- failoverItem{qr: qr, err: err, fatalCredential: fatalCredential}:
	case <-p.stopCh:
		// Pipeline is shutting down and the failover channel may never be
		// drained. Complete the request so its Submit caller is not left
		// blocking on qr.ResultCh forever. Mirrors runModelDrainer's
		// stopCh handling (see TestStopNoDrainLoss).
		p.complete(qr, ForwardOutcome{Err: ErrShutdown})
	}
}

// tryEnqueueCred pushes qr into the selected credential's Tier-2 queue,
// creating the forwarder on first use. Returns false if the queue is full or
// the pipeline is shutting down (admission refused).
func (p *Pipeline) tryEnqueueCred(cred CredentialRef, qr *QueuedRequest) bool {
	cf := p.getOrCreateForwarder(cred)
	if cf == nil {
		metricOverflow.WithLabelValues("shutdown").Inc()
		return false
	}
	if !cf.tryReserve() {
		metricOverflow.WithLabelValues("cred_queue_full").Inc()
		return false
	}
	// Stamp the enqueue time BEFORE the channel send. The send/receive on
	// cf.queue establishes happens-before, so the forwarder goroutine's read of
	// CredEnqueuedAt in acquire() (forwarder.go) is race-free. Setting it after
	// the send (the previous order) raced with the receiver (go test -race).

	// V3.1: Record T5 timestamp (credential queue enqueue)
	qr.SetT5_CredEnqueued()

	// Snapshot the event fields BEFORE the send: cf.queue <- qr hands qr to the
	// forwarder goroutine, which may fail fast, re-enter failover, and rewrite
	// ResolvedModel (tryModelChange) while this goroutine is still emitting —
	// same bug class as the T5 ordering above (caught by go test -race,
	// TestModelChange).
	reqID, model, emitCtx := qr.ID, qr.ResolvedModel, ctxOf(qr)

	select {
	case cf.queue <- qr:
		metricCredQueueDepth.WithLabelValues(itoa(cred.CredentialID), cred.ConcurrencyMode).Inc()
		// V3.3-OBS OBS-B1 (2026-08-15): node_enqueued 动作事件（S6，落入
		// 凭据队列）。
		p.liveActions.Emit(emitCtx, liveactions.ActionEvent{
			RequestID:    reqID,
			Action:       liveactions.ActionNodeEnqueued,
			Model:        model,
			CredentialID: cred.CredentialID,
			Detail: map[string]string{
				"queue_depth": strconv.FormatInt(cf.depth.Load(), 10),
				"vendor":      cred.Vendor,
			},
		})
		return true
	default:
		cf.depth.Add(-1)
		metricOverflow.WithLabelValues("cred_queue_full").Inc()
		return false
	}
}

func (p *Pipeline) getOrCreateForwarder(cred CredentialRef) *credForwarder {
	p.credMu.Lock()
	defer p.credMu.Unlock()
	if cf, ok := p.forwarders[cred.CredentialID]; ok {
		return cf
	}
	// Shutdown guard (concurrency audit 2026-08-13 D4): without it, a late
	// Submit→failover path could spawn a forwarder AFTER Stop cleared the map,
	// leaving a goroutine with a context.Background() nobody cancels — a
	// permanent leak. Fail admission instead; callers complete with
	// ErrShutdown via the failover ladder's terminal path.
	if p.shutdown.Load() {
		return nil
	}
	depth := cred.MaxQueueDepth
	if depth <= 0 {
		depth = p.config().MaxQueueDepth
	}
	cf := newCredForwarder(cred, depth, p)
	p.forwarders[cred.CredentialID] = cf
	return cf
}

// queueWaitBudget returns the max pacing wait for a request on its selected cred.
func (p *Pipeline) queueWaitBudget(qr *QueuedRequest) time.Duration {
	ms := qr.SelectedCred.MaxQueueWaitMS
	if ms <= 0 {
		ms = p.config().MaxQueueWaitMS
	}
	if ms <= 0 {
		return 30 * time.Second // hard floor safety
	}
	return time.Duration(ms) * time.Millisecond
}

// triedList converts the tried-models set to a slice for ModelResolveFunc.
func triedList(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	return out
}

func itoa(i int) string {
	// strconv without importing it everywhere; small helper.
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
