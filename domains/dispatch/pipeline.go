package dispatch

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
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

// ForwardFunc performs one upstream forward attempt for (qr, cred) and returns
// the outcome. BytesSent MUST be accurate (first-byte boundary) so the mover
// can decide retry-vs-terminal. The executor implements this via the extracted
// forwardOneCandidate.
type ForwardFunc func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome

// Deps bundles the callbacks the executor supplies to the pipeline.
type Deps struct {
	RouteFunc        RouteFunc
	ModelResolveFunc ModelResolveFunc
	ForwardFunc      ForwardFunc
	// AllowModelChange enables model-failover when all credentials under the
	// current model are exhausted (the Tier-1 escape hatch).
	AllowModelChange bool
	// HotCfg is read once at construction; live reload re-reads via Reload.
	HotCfg *atomic.Value // *Config; may be nil → DefaultConfig
}

// Pipeline is the multi-tier dispatch core. Construct once, Start, then Submit.
type Pipeline struct {
	routeFunc        RouteFunc
	modelResolveFunc ModelResolveFunc
	forwardFunc      ForwardFunc
	allowModelChange bool

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
}

// NewPipeline constructs a pipeline. Call Start before Submit.
func NewPipeline(deps Deps) *Pipeline {
	p := &Pipeline{
		routeFunc:        deps.RouteFunc,
		modelResolveFunc: deps.ModelResolveFunc,
		forwardFunc:      deps.ForwardFunc,
		allowModelChange: deps.AllowModelChange,
		models:           make(map[string]*modelQueue),
		forwarders:       make(map[int]*credForwarder),
		stopCh:           make(chan struct{}),
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
	modelKey := queueKeyFor(qr.RequestedModel)
	if !p.enqueueModel(modelKey, qr) {
		metricOverflow.WithLabelValues("model_queue_full").Inc()
		return nil, ErrOverflow
	}
	select {
	case out := <-qr.ResultCh:
		return out.Result, out.Err
	case <-ctx.Done():
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
// (overflow).
func (p *Pipeline) enqueueModel(name string, qr *QueuedRequest) bool {
	mq := p.getOrCreateModelQueue(name)
	select {
	case mq.ch <- qr:
		mq.depth.Add(1)
		metricModelQueueDepth.WithLabelValues(name).Inc()
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
	// running; a late wg.Add would race it). Return a throwaway queue whose
	// sends will buffer or overflow — Submit has already started rejecting.
	if p.shutdown.Load() {
		mq := &modelQueue{name: name, ch: make(chan *QueuedRequest, 1)}
		return mq
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
			wait := time.Since(qr.EnqueuedAt).Seconds()
			metricModelQueueWait.WithLabelValues(mq.name).Observe(wait)
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
	if qr.abandoned.Load() {
		return // caller already left; drop
	}
	select {
	case qr.ResultCh <- out:
	default:
		// cap-1 channel with a single reader; shouldn't happen. Drain-safe.
	}
}

// routeFailover hands a pre-firstbyte failure to the ③ mover.
func (p *Pipeline) routeFailover(qr *QueuedRequest, err error) {
	select {
	case p.failoverCh <- failoverItem{qr: qr, err: err}:
	case <-p.stopCh:
	}
}

// tryEnqueueCred pushes qr into the selected credential's Tier-2 queue,
// creating the forwarder on first use. Returns false if the queue is full.
func (p *Pipeline) tryEnqueueCred(cred CredentialRef, qr *QueuedRequest) bool {
	cf := p.getOrCreateForwarder(cred)
	if !cf.tryReserve() {
		metricOverflow.WithLabelValues("cred_queue_full").Inc()
		return false
	}
	select {
	case cf.queue <- qr:
		qr.CredEnqueuedAt = time.Now()
		metricCredQueueDepth.WithLabelValues(itoa(cred.CredentialID), cred.ConcurrencyMode).Inc()
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
