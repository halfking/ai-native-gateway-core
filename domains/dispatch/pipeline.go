package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
	// RetryScheduler, when non-nil, defers same-credential retries until
	// retry_at (v4 T3-8). nil keeps the legacy immediate-retry behavior.
	// The caller owns the scheduler's lifecycle (pipeline does not Close it).
	RetryScheduler       RetryScheduler
	ObservationSink      ObservationSink
	QueueObservationSink QueueObservationSink
	SessionAffinitySink  SessionAffinitySink
	MinuteStatsSink      MinuteStatsSink
}

// Pipeline is the multi-tier dispatch core. Construct once, Start, then Submit.
type Pipeline struct {
	routeFunc            RouteFunc
	modelResolveFunc     ModelResolveFunc
	modelRecommendFunc   ModelRecommendFunc
	forwardFunc          ForwardFunc
	allowModelChange     bool
	allowModelChangeFunc func() bool

	// lifecycle registry (v4 R1.1/T2): request_id → pending/in-flight/
	// completed + retry_at. Bookkeeping bypass only — never gates execution
	// except lifecycle projection updates.
	registry *LifecycleRegistry
	// totalQueue is the real execution admission bound. A slot is held from
	// Submit until complete and is independent from registry bookkeeping.
	totalQueue *totalExecutionQueue
	// retryScheduler optionally defers failed re-execution until retry_at.
	// nil ⇒ immediate failover (legacy behavior).
	retryScheduler RetryScheduler
	// queueMirror optionally projects queue state to Redis (observation only).
	queueMirror *QueueMirror
	// affinitySink persists successful session routes and invalidates a failed
	// sticky credential. It is best-effort and never gates execution.
	affinitySink    SessionAffinitySink
	minuteStatsSink MinuteStatsSink

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

	observationSinkMu sync.RWMutex
	observationSink   ObservationSink
	observationChMu   sync.RWMutex
	observationCh     chan observationItem
	observationClosed bool
	observationWg     sync.WaitGroup

	queueObservationMu   sync.RWMutex
	queueObservationSink QueueObservationSink
	inFlight             atomic.Int64

	// governorBackend (Stage B): the optional management-layer backend
	// the credForwarder construction can read in Stage D/E. Stage B
	// itself does NOT alter how credForwarder.gov is built — the field
	// is wired here so future stages can flip the read path without
	// touching the constructor signature again. The setter is
	// SetGovernorBackend below; Stage B.4 composes the production
	// value.
	backendMu       sync.RWMutex
	governorBackend GovernorBackend

	// snapshotObserver (Stage C.2): optional 100ms tick that walks the
	// credForwarders map under credMu and emits a GovernorSnapshot per
	// cred into the C.1 closed-enum metric set. Nil = observer inactive
	// (no goroutine spawned, no metric emission). Wired via
	// SetGovernorSnapshotObserver before Pipeline.Start.
	snapshotObserver *governorSnapshotObserver

	// activePolicyRevision (Stage C.2 reader, Stage E writer). The
	// observer reads it to stamp snap.SpecRevision; Stage E's
	// ApplyPolicySnapshot writes it via Pipeline.SetActivePolicyRevision.
	activePolicyRevision atomic.Uint64

	// snapshotAgeMS tracks the wall-clock millisecond timestamp of the
	// last ForEachCredSnapshot visit per credForwarder, so the observer
	// can compute a per-cred AgeMS for the snapshot it just built. The
	// map is only touched under credMu in the read path; no contention
	// with the tick goroutine because ForEachCredSnapshot takes credMu
	// for the duration of the walk.
	snapshotAgeMu sync.Mutex
	snapshotAgeMS map[int]int64

	// credStateCache (Stage D): per-cred latest SnapshotState populated
	// by ForEachCredSnapshot under credMu; read on the per-request hot
	// path via SnapshotForCred under credStateCacheMu.RLock() — does
	// NOT block forwarder construction. The observer tick writes
	// (State != Unknown only); a closed observer leaves the cache empty
	// and SnapshotForCred fails open (ok=false).
	credStateCacheMu sync.RWMutex
	credStateCache   map[int]SnapshotState
}

type observationItem struct {
	ctx         context.Context
	observation Observation
}

// SetLiveActions wires the request-lifecycle action-event emitter (V3.3-OBS
// OBS-B1). Call before/after Start — emit sites are nil-safe either way.
func (p *Pipeline) SetLiveActions(e *liveactions.Emitter) {
	if p == nil {
		return
	}
	p.liveActions = e
}

// SetObservationSink replaces the optional lifecycle observation sink. It is
// safe before or after Start; a nil sink disables lifecycle emission.
func (p *Pipeline) SetObservationSink(sink ObservationSink) {
	if p == nil {
		return
	}
	p.observationSinkMu.Lock()
	p.observationSink = sink
	p.observationSinkMu.Unlock()
}

// SetQueueObservationSink replaces the queue read-model sink.
func (p *Pipeline) SetQueueObservationSink(sink QueueObservationSink) {
	if p == nil {
		return
	}
	p.queueObservationMu.Lock()
	p.queueObservationSink = sink
	p.queueObservationMu.Unlock()
}

// SetSessionAffinitySink replaces the best-effort session affinity writer.
// It is safe before or after Start because all calls read the interface value
// atomically under the same mutex used for queue observation wiring.
func (p *Pipeline) SetSessionAffinitySink(sink SessionAffinitySink) {
	if p == nil {
		return
	}
	p.queueObservationMu.Lock()
	p.affinitySink = sink
	p.queueObservationMu.Unlock()
}

// SetMinuteStatsSink replaces the optional immediate minute-bucket projection.
func (p *Pipeline) SetMinuteStatsSink(sink MinuteStatsSink) {
	if p == nil {
		return
	}
	p.queueObservationMu.Lock()
	p.minuteStatsSink = sink
	p.queueObservationMu.Unlock()
}

func (p *Pipeline) observeQueue(observation QueueObservation) {
	if p == nil {
		return
	}
	p.queueObservationMu.RLock()
	sink := p.queueObservationSink
	p.queueObservationMu.RUnlock()
	if sink != nil {
		func() {
			defer func() { _ = recover() }()
			sink.ObserveQueue(observation)
		}()
	}
	// Redis mirror bypass (async, bounded, never blocks the transition site).
	p.queueMirror.ObserveQueue(observation)
}

func (p *Pipeline) observeOverflow(reason string) {
	p.observeQueue(QueueObservation{Kind: QueueOverflow, OverflowReason: reason})
}

// NewPipeline constructs a pipeline. Call Start before Submit.
func NewPipeline(deps Deps) *Pipeline {
	p := &Pipeline{
		routeFunc:            deps.RouteFunc,
		modelResolveFunc:     deps.ModelResolveFunc,
		modelRecommendFunc:   deps.ModelRecommendFunc,
		forwardFunc:          deps.ForwardFunc,
		allowModelChange:     deps.AllowModelChange,
		observationSink:      deps.ObservationSink,
		queueObservationSink: deps.QueueObservationSink,
		affinitySink:         deps.SessionAffinitySink,
		minuteStatsSink:      deps.MinuteStatsSink,
		allowModelChangeFunc: deps.AllowModelChangeFunc,
		retryScheduler:       deps.RetryScheduler,
		models:               make(map[string]*modelQueue),
		forwarders:           make(map[int]*credForwarder),
		snapshotAgeMS:        make(map[int]int64),
		credStateCache:       make(map[int]SnapshotState),
		stopCh:               make(chan struct{}),
	}
	cfg := DefaultConfig()
	if deps.HotCfg != nil {
		if v, ok := deps.HotCfg.Load().(*Config); ok && v != nil {
			cfg = *v
		}
	}
	p.registry = NewLifecycleRegistry(cfg.RegistryCapacity, cfg.CompletedWatermark, 0)
	p.totalQueue = newTotalExecutionQueue(cfg.TotalQueueCapacity)
	p.registry.SetEvictHook(p.emitRegistryEviction)
	p.cfg.Store(&cfg)
	return p
}

// SetRetryScheduler swaps the optional timed-retry scheduler (before or
// after Start; nil disables timed retries). The caller owns Close().
func (p *Pipeline) SetRetryScheduler(s RetryScheduler) {
	if p == nil {
		return
	}
	p.retryScheduler = s
}

// NewDefaultRetryScheduler builds a started HeapRetryScheduler wired to
// this pipeline's pickup path, completing still-parked requests with
// ErrShutdown on Close so Submit callers cannot block forever during
// graceful shutdown. Convenience for composition roots; the caller must
// Close() it (typically after Pipeline.Stop).
func (p *Pipeline) NewDefaultRetryScheduler() *HeapRetryScheduler {
	if p == nil {
		return nil
	}
	return NewHeapRetrySchedulerWithCloseHandler(
		p.onRetryDue,
		func(qr *QueuedRequest) { p.complete(qr, ForwardOutcome{Err: ErrShutdown}) },
		nil, nil)
}

// SetQueueMirror wires the optional Redis queue-state mirror. Nil disables
// mirroring (nil-receiver methods are no-ops).
func (p *Pipeline) SetQueueMirror(m *QueueMirror) {
	if p == nil {
		return
	}
	p.queueMirror = m
}

// SetGovernorBackend wires the optional management-layer GovernorBackend.
// Stage B.3 ships this seam; Stage D/E is responsible for consuming the
// backend inside credForwarder.gov construction. Nil disables backend-
// backed admission (LocalBackend-equivalent behavior) — the existing
// newGovernor(cred) in forwarder.go is the default until the read path
// is rewritten.
//
// Lifecycle: safe to call before OR after Start; safe to swap on a
// running pipeline (idempotent). The mutex is the canonical sync
// strategy — Stage B.3 deliberately diverges from SetQueueMirror's
// bare-assignment pattern because credForwarder.gov is expected to
// read this field at construction time in a future stage, and a
// post-Start swap there needs race-safety against an in-flight
// getOrCreateForwarder.
func (p *Pipeline) SetGovernorBackend(b GovernorBackend) {
	if p == nil {
		return
	}
	p.backendMu.Lock()
	p.governorBackend = b
	p.backendMu.Unlock()
}

// GovernorBackend returns the currently-wired backend (nil if none).
// Stage B.3: only consumed by tests; Stage D/E will read this from
// credForwarder construction.
func (p *Pipeline) GovernorBackend() GovernorBackend {
	if p == nil {
		return nil
	}
	p.backendMu.RLock()
	defer p.backendMu.RUnlock()
	return p.governorBackend
}

// governorForCredential uses Redis enforcement for all configured modes.
func (p *Pipeline) governorForCredential(cred CredentialRef) Governor {
	backend := p.GovernorBackend()
	mode := cred.ConcurrencyMode
	if mode == "" {
		mode = ModeConcurrency
	}
	if backend == nil || backend.Kind() != BackendRedisEnforce {
		return newGovernor(cred)
	}
	limit := cred.ConcurrencyLimit
	if mode == ModeRPM {
		limit = cred.RPMLimit
	} else if mode == ModeTPM {
		limit = cred.TPMLimit
	}
	if limit <= 0 || mode == ModeDisabled {
		return newGovernor(cred)
	}

	gov, err := backend.New(context.Background(), GovernorSpec{
		CredentialID: cred.CredentialID,
		ProviderID:   cred.ProviderID,
		Mode:         ModeConcurrency,
		Limit:        limit,
		RPMLimit:     cred.RPMLimit,
		TPMLimit:     cred.TPMLimit,
		Backend:      BackendRedisEnforce,
		Revision:     p.ActiveRevision(),
	})
	if err == nil && gov != nil {
		return gov
	}
	if err == nil {
		err = errors.New("governor backend returned nil governor")
	}
	slog.Error("dispatch: credential governor initialization failed",
		"credential_id", cred.CredentialID,
		"provider_id", cred.ProviderID,
		"backend", backend.Kind(),
		"error", err)
	return unavailableGovernor{mode: mode, err: fmt.Errorf("%w: %w", ErrGovernorUnavailable, err)}
}

// SetGovernorSnapshotObserver wires the optional C.2 snapshot observer.
// Must be called before Start; calling after Start panics (the observer
// would race with the in-flight walk). Composition-root order:
//
//	NewPipeline → SetGovernorBackend → SetGovernorSnapshotObserver → Start
//
// Passing nil disables the observer.
func (p *Pipeline) SetGovernorSnapshotObserver(o *governorSnapshotObserver) {
	if p == nil {
		return
	}
	if p.started.Load() {
		panic("dispatch: SetGovernorSnapshotObserver must be called before Pipeline.Start")
	}
	if o != nil {
		// Wire the read-side dependencies now so the observer doesn't
		// need its own pipeline pointer. Provider/ActiveRevision are
		// methods on *Pipeline; backendFn is a closure over GovernorBackend()
		// so a swap on backendMu is observed at the next tick.
		o.SetProvider(p, p.GovernorBackend, p.ActiveRevision)
	}
	p.snapshotObserver = o
}

// SetActivePolicyRevision stamps the currently applied GovernorPolicy
// revision. Stage E's policy_publisher calls this after a successful
// ApplyPolicySnapshot; Stage C.2's observer reads it via
// Pipeline.ActiveRevision. Safe before/after Start.
func (p *Pipeline) SetActivePolicyRevision(rev uint64) {
	if p == nil {
		return
	}
	p.activePolicyRevision.Store(rev)
}

// ActiveRevision satisfies SnapshotProvider; returns the current
// GovernorPolicy revision (0 = no policy applied yet).
func (p *Pipeline) ActiveRevision() uint64 {
	if p == nil {
		return 0
	}
	return p.activePolicyRevision.Load()
}

// SnapshotForCred satisfies SnapshotProvider. Reads from the
// per-cred cache populated by ForEachCredSnapshot (and consumed by
// Stage D's capacity-aware soft sort). Returns (_, false) on cache
// miss — callers must treat this as Ready (fail-open) so an
// un-initialized cache does not penalize candidates.
//
// O(1) read under credStateCacheMu.RLock(). Does NOT touch credMu, so
// it never blocks getOrCreateForwarder.
func (p *Pipeline) SnapshotForCred(credID int) (SnapshotState, bool) {
	if p == nil {
		return SnapshotStateUnknown, false
	}
	p.credStateCacheMu.RLock()
	state, ok := p.credStateCache[credID]
	p.credStateCacheMu.RUnlock()
	if !ok {
		// Map zero value is empty string; callers must not see "" as
		// a valid state. Return SnapshotStateUnknown explicitly so
		// shouldKeep's default branch treats it as Ready (fail-open).
		return SnapshotStateUnknown, false
	}
	return state, true
}

// ForEachCredSnapshot satisfies SnapshotProvider. Walks the live
// credForwarders under credMu (read lock) and yields one
// GovernorSnapshot per forwarder. The pipeline-level state is read
// directly (no extra synchronization beyond credMu).
//
// State cache writes (Stage D) happen AFTER credMu is dropped — see
// runForEachCredSnapshot. Keeping the credStateCacheMu.Lock acquisition
// outside the credMu section is what decouples the per-request
// SnapshotForCred reader from forwarder construction under contention.
func (p *Pipeline) ForEachCredSnapshot(fn func(snap GovernorSnapshot) error) error {
	if p == nil || fn == nil {
		return nil
	}
	return p.runForEachCredSnapshot(fn)
}

// runForEachCredSnapshot does the actual walk. It is split from
// ForEachCredSnapshot so the credMu critical region can end before
// applyStateWrites takes credStateCacheMu.Lock. The two-mutex dance
// would otherwise hold credMu across the cache write, which would
// stall getOrCreateForwarder behind every observer tick.
func (p *Pipeline) runForEachCredSnapshot(fn func(snap GovernorSnapshot) error) error {
	nowMS := time.Now().UnixMilli()
	// stateWrites collected under credMu; flushed after credMu.Unlock().
	stateWrites := make(map[int]SnapshotState)
	var walkErr error

	p.credMu.Lock()
	if len(p.forwarders) == 0 {
		p.credMu.Unlock()
		// No-op: nothing to apply.
		return nil
	}
	for _, cf := range p.forwarders {
		snap := p.snapshotForCredForwarderLocked(cf)
		// AgeMS = delta from previous visit; 0 on the first visit.
		p.snapshotAgeMu.Lock()
		prev, seen := p.snapshotAgeMS[cf.cred.CredentialID]
		p.snapshotAgeMS[cf.cred.CredentialID] = nowMS
		p.snapshotAgeMu.Unlock()
		if seen {
			snap.AgeMS = nowMS - prev
			if snap.AgeMS < 0 {
				snap.AgeMS = 0
			}
		}
		// Unknown states carry BackendErr (Validate invariant); we
		// don't cache those — the per-request lookup should fall back
		// to "no snapshot" rather than appear as a stable Ready.
		if snap.State != SnapshotStateUnknown {
			stateWrites[cf.cred.CredentialID] = snap.State
		}
		if err := fn(snap); err != nil {
			walkErr = err
			break
		}
	}
	p.credMu.Unlock()

	// Cache write happens OUTSIDE credMu — the whole point of the
	// split. SnapshotForCred readers take credStateCacheMu.RLock() only.
	p.applyStateWrites(stateWrites)
	return walkErr
}

// applyStateWrites performs the credStateCacheMu.Lock() batch write.
// Called once per ForEachCredSnapshot tick (after credMu is dropped).
func (p *Pipeline) applyStateWrites(writes map[int]SnapshotState) {
	if len(writes) == 0 || p == nil {
		return
	}
	p.credStateCacheMu.Lock()
	for id, st := range writes {
		p.credStateCache[id] = st
	}
	p.credStateCacheMu.Unlock()
}

// snapshotForCredForwarderLocked builds a GovernorSnapshot for one
// credForwarder. The caller MUST hold credMu (the credForwarder fields
// can be inspected without further locking — depth is atomic, gov.Mode
// is pure, the four governor impls have internal locks for used/tokens).
func (p *Pipeline) snapshotForCredForwarderLocked(cf *credForwarder) GovernorSnapshot {
	backendKind := string(BackendLocal)
	p.backendMu.RLock()
	if p.governorBackend != nil {
		backendKind = string(p.governorBackend.Kind())
	}
	p.backendMu.RUnlock()

	snap := GovernorSnapshot{
		SpecRevision: p.activePolicyRevision.Load(),
		Backend:      backendKind,
		Mode:         cf.gov.Mode(),
		Limit:        int(cf.limit),
		InFlight:     int(cf.depth.Load()),
		QueueDepth:   int(cf.depth.Load()),
		State:        SnapshotStateReady,
	}
	if !cf.HasCapacity() {
		snap.State = SnapshotStateQueueFull
	}
	// Pull governor-specific "Used" counters without changing the
	// Governor interface — type-assert on the four impls.
	switch g := cf.gov.(type) {
	case *concurrencyGovernor:
		// Limit must be the concurrency cap (g.cap), NOT the Tier-2 queue
		// depth (cf.limit, default 300). Comparing used vs queue depth made
		// GovernorSaturated almost never light up.
		snap.Limit = int(g.cap)
		snap.Used = int(g.used.Load())
		if snap.Limit > 0 && snap.Used >= snap.Limit && snap.State == SnapshotStateReady {
			snap.State = SnapshotStateGovernorSaturated
		}
	case *rpmGovernor:
		g.mu.Lock()
		snap.Used = int(math.Floor(g.tokens))
		// Saturated when fewer than 1 token remains: the next Acquire would
		// have to wait, so surface the governor as saturated for Stage D
		// capacity-aware soft-sort to demote the credential.
		if snap.State == SnapshotStateReady && g.tokens < 1 {
			snap.State = SnapshotStateGovernorSaturated
		}
		g.mu.Unlock()
	case *tpmGovernor:
		g.mu.Lock()
		snap.Used = int(math.Floor(g.tokens))
		// Saturated when fewer than one default-cost request remains
		// (defaultTokenEstimate). Using the default cost keeps the
		// saturation signal stable across requests whose real cost is
		// unknown at snapshot time.
		if snap.State == SnapshotStateReady && g.tokens < defaultTokenEstimate {
			snap.State = SnapshotStateGovernorSaturated
		}
		g.mu.Unlock()
	case *noopGovernor:
		// 0
	}
	return snap
}

// LifecycleSnapshot exposes the request registry view (admin/tests). The
// optional states filter narrows the result; empty returns every entry.
func (p *Pipeline) LifecycleSnapshot(states ...LifecycleState) RegistrySnapshot {
	if p == nil || p.registry == nil {
		return RegistrySnapshot{Entries: []RegistryEntry{}}
	}
	return p.registry.Snapshot(states...)
}

// emitRegistryEviction reports registry completed-entry eviction as a queue
// observation (v4 R1.8: 淘汰必须发 journey/action 事件). Runs on registry
// mutex release, never inline with the hot path.
func (p *Pipeline) emitRegistryEviction(evicted []RegistryEntry) {
	if p == nil || len(evicted) == 0 {
		return
	}
	ids := make([]string, 0, len(evicted))
	for _, entry := range evicted {
		ids = append(ids, entry.RequestID)
	}
	p.observeQueue(QueueObservation{Kind: QueueLifecycleEviction, EvictedRequestIDs: ids})
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
// resize live, but queue-wait budgets, retry budgets and registry limits
// pick up immediately.
func (p *Pipeline) Reload(cfg Config) {
	p.cfg.Store(&cfg)
	if p.registry != nil {
		p.registry.UpdateLimits(cfg.RegistryCapacity, cfg.CompletedWatermark, 0)
	}
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
	p.wg.Add(1)
	go p.runTotalDrainer()
	for i := 0; i < cfg.DispatcherWorkers; i++ {
		p.wg.Add(1)
		go p.runDispatcher()
	}
	for i := 0; i < cfg.FailoverWorkers; i++ {
		p.wg.Add(1)
		go p.runFailover()
	}
	p.observationCh = make(chan observationItem, maxInt(1024, cfg.StatsBuffer*16))
	p.observationWg.Add(1)
	go p.runObservations()

	// Stage C.2: start the optional snapshot observer. Its lifecycle is
	// independent of the Pipeline's wg — Stop() drains it explicitly so
	// no in-flight walk races with the credForwarder cancel loop below.
	if p.snapshotObserver != nil {
		p.snapshotObserver.Start(context.Background())
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (p *Pipeline) runObservations() {
	defer p.observationWg.Done()
	for item := range p.observationCh {
		p.observationSinkMu.RLock()
		sink := p.observationSink
		p.observationSinkMu.RUnlock()
		if sink != nil {
			func() {
				defer func() { _ = recover() }()
				sink.ObserveDispatch(item.ctx, item.observation)
			}()
		}
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
	// Stage C.2: stop the snapshot observer FIRST so it doesn't try to
	// walk forwarders we are about to clear.
	if p.snapshotObserver != nil {
		p.snapshotObserver.Stop()
	}
	p.credMu.Lock()
	for _, cf := range p.forwarders {
		cf.cancel()
	}
	p.forwarders = map[int]*credForwarder{}
	p.credMu.Unlock()
	// Clear the Stage D caches so a Stop→Start cycle (or credential churn)
	// does not surface stale state for forwarders that no longer exist.
	// They repopulate on the next observer tick / SnapshotForCred call.
	p.snapshotAgeMu.Lock()
	p.snapshotAgeMS = make(map[int]int64)
	p.snapshotAgeMu.Unlock()
	p.credStateCacheMu.Lock()
	p.credStateCache = make(map[int]SnapshotState)
	p.credStateCacheMu.Unlock()
	p.wg.Wait()
	p.observationChMu.Lock()
	if p.observationCh != nil && !p.observationClosed {
		p.observationClosed = true
		close(p.observationCh)
	}
	p.observationChMu.Unlock()
	p.observationWg.Wait()
	p.queueMirror.Close()
}

// Submit enqueues a request into the Tier-1 model queue and blocks until the
// pipeline completes the request (success/terminal-failure) or ctx expires.
// Returns (opaqueResult, nil) on success, (nil, err) otherwise.
func (p *Pipeline) Submit(ctx context.Context, qr *QueuedRequest) (any, error) {
	qr.Ctx = ctx
	qr.setObservationEmitter(func(observation Observation) {
		p.observationChMu.RLock()
		if p.observationCh == nil || p.observationClosed {
			p.observationChMu.RUnlock()
			return
		}
		item := observationItem{ctx: context.WithoutCancel(ctxOf(qr)), observation: observation}
		select {
		case p.observationCh <- item:
		default:
			metricOverflow.WithLabelValues("dispatch_observation_full").Inc()
			p.observeOverflow("dispatch_observation_full")
		}
		p.observationChMu.RUnlock()
	})
	if p.shutdown.Load() || !p.started.Load() {
		p.emitRequestTerminal(qr, ForwardOutcome{Err: ErrShutdown})
		return nil, ErrShutdown
	}
	// Stamp before the FIFO handoff. After totalQueue accepts the request its
	// worker may complete it immediately, so later writes would race metrics.
	if qr.EnqueuedAt.IsZero() {
		qr.EnqueuedAt = time.Now()
	}
	qr.SetT1_TotalEnqueued()
	// LifecycleRegistry is a projection only. Its admission result must never
	// reject execution; totalQueue is the sole execution capacity boundary.
	p.registry.RegisterPending(qr.ID, qr.RequestedModel, time.Now())
	if !p.totalQueue.tryEnqueue(qr) {
		metricOverflow.WithLabelValues("total_queue_full").Inc()
		p.observeOverflow("total_queue_full")
		overflow := &OverflowError{Reason: "total_queue_full", RetryAfter: DefaultOverflowRetryAfter}
		p.registry.MarkCompleted(qr.ID, time.Now())
		p.emitRequestTerminal(qr, ForwardOutcome{Err: overflow})
		return nil, overflow
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
		// Clean up Registry to prevent unfinishedCount leak (P0 fix)
		if !qr.completed.CompareAndSwap(false, true) {
			return nil, ctx.Err()
		}
		p.totalQueue.release(qr)
		p.registry.MarkCompleted(qr.ID, time.Now())
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

// runTotalDrainer forwards the bounded total FIFO into the model queues.
func (p *Pipeline) runTotalDrainer() {
	defer p.wg.Done()
	for {
		select {
		case qr := <-p.totalQueue.ch:
			if qr == nil {
				continue
			}
			if p.shutdown.Load() {
				p.totalQueue.release(qr)
				p.complete(qr, ForwardOutcome{Err: ErrShutdown})
				return
			}
			if !p.enqueueModelFromTotal(queueKeyFor(qr.RequestedModel), qr) {
				p.totalQueue.release(qr)
				if p.shutdown.Load() {
					p.complete(qr, ForwardOutcome{Err: ErrShutdown})
					return
				}
				p.complete(qr, ForwardOutcome{Err: ctxOf(qr).Err()})
			} else {
				p.totalQueue.release(qr)
			}
		case <-p.stopCh:
			for {
				select {
				case qr := <-p.totalQueue.ch:
					if qr != nil {
						p.totalQueue.release(qr)
						p.complete(qr, ForwardOutcome{Err: ErrShutdown})
					}
				default:
					return
				}
			}
		}
	}
}

// enqueueModelFromTotal keeps an already admitted request in the total FIFO
// until its model lane can receive it. This is deliberately different from
// Submit's non-blocking external admission path: totalQueue is the bounded
// waiting room, so a full model lane applies backpressure instead of dropping.
func (p *Pipeline) enqueueModelFromTotal(name string, qr *QueuedRequest) bool {
	mq := p.getOrCreateModelQueue(name)
	if mq == nil {
		return false
	}
	for {
		if p.shutdown.Load() || ctxOf(qr).Err() != nil {
			return false
		}
		mq.mu.Lock()
		depth := mq.depth.Add(1)
		metricModelQueueDepth.WithLabelValues().Inc()
		select {
		case mq.ch <- qr:
			p.observeQueue(QueueObservation{Kind: QueueModelDepth, Model: name, Depth: depth, Delta: 1, AbsoluteDepth: true})
			p.liveActions.Emit(ctxOf(qr), liveactions.ActionEvent{
				RequestID: qr.ID,
				Action:    liveactions.ActionModelEnqueued,
				Model:     name,
				Detail: map[string]string{
					"queue_depth": strconv.FormatInt(mq.depth.Load(), 10),
				},
			})
			qr.emitObservation(Observation{Type: ObservationModelEnqueued, Stage: StageModelQueue, Model: name, ResolvedModel: qr.ResolvedModel})
			mq.mu.Unlock()
			return true
		default:
			mq.depth.Add(-1)
			metricModelQueueDepth.WithLabelValues().Dec()
			mq.mu.Unlock()
		}
		select {
		case <-p.stopCh:
			return false
		case <-ctxOf(qr).Done():
			return false
		case <-time.After(time.Millisecond):
		}
	}
}

// enqueueModel pushes qr into the named model queue, creating it (and its
// drainer goroutine) on first use. Returns false if the model queue is full
// (overflow) or the pipeline is shutting down (admission refused).
func (p *Pipeline) enqueueModel(name string, qr *QueuedRequest) bool {
	mq := p.getOrCreateModelQueue(name)
	if mq == nil {
		return false
	}
	resolvedModel := qr.ResolvedModel
	qr.journeyMu.Lock()
	enqueuedAt := time.Now()
	mq.mu.Lock()
	// Reserve the observable depth before handing qr to mq.ch. The channel send
	// can wake the drainer immediately; incrementing after a successful send
	// races the drainer's decrement and leaves a phantom queue item.
	depth := mq.depth.Add(1)
	metricModelQueueDepth.WithLabelValues().Inc()
	select {
	case mq.ch <- qr:
		slog.Debug("dispatch: model enqueue", "model", name, "depth", depth)
		p.observeQueue(QueueObservation{Kind: QueueModelDepth, Model: name, Depth: depth, Delta: 1, AbsoluteDepth: true})
		// V3.3-OBS OBS-B1 (2026-08-15): model_enqueued 动作事件（S4）。
		p.liveActions.Emit(ctxOf(qr), liveactions.ActionEvent{
			RequestID: qr.ID,
			Action:    liveactions.ActionModelEnqueued,
			Model:     name,
			Detail: map[string]string{
				"queue_depth": strconv.FormatInt(mq.depth.Load(), 10),
			},
		})
		qr.emitObservationLocked(Observation{
			Type:          ObservationModelEnqueued,
			Stage:         StageModelQueue,
			Model:         name,
			ResolvedModel: resolvedModel,
			OccurredAt:    enqueuedAt,
		})
		mq.mu.Unlock()
		qr.journeyMu.Unlock()
		return true
	default:
		mq.depth.Add(-1)
		metricModelQueueDepth.WithLabelValues().Dec()
		mq.mu.Unlock()
		qr.journeyMu.Unlock()
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
			mq.mu.Lock()
			depth := mq.depth.Add(-1)
			metricModelQueueDepth.WithLabelValues().Dec()
			slog.Debug("dispatch: model dequeue", "model", mq.name, "depth", depth)
			p.observeQueue(QueueObservation{Kind: QueueModelDepth, Model: mq.name, Depth: depth, Delta: -1, AbsoluteDepth: true})
			mq.mu.Unlock()

			// V3.1: Record T2 timestamp (model queue dequeue, routing start)
			qr.SetT2_TotalDequeued()

			wait := time.Since(qr.EnqueuedAt).Seconds()
			metricModelQueueWait.WithLabelValues().Observe(wait)
			slog.Debug("dispatch: model dequeue wait", "model", mq.name, "wait_ms", int(wait*1000))

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
	// V4 R1.1: registry terminal transition (completed; kept until the
	// R1.8 watermark evicts it). Bookkeeping bypass — never gates delivery.
	now := time.Now()
	p.registry.MarkCompleted(qr.ID, now)
	p.queueMirror.ClearRetryAt(qr.ID)

	// V3.1: Record T9 timestamp (response end - stream completed)
	qr.SetT9_ResponseEnd()
	p.recordSessionAffinity(qr, out)
	p.recordMinuteStats(qr, out)
	p.emitRequestTerminal(qr, out)

	// V3.1: Export stage histograms + waterfall/projection samples even if the
	// caller already left — abandoned requests still carry useful latency signal.
	p.recordStageMetrics(qr, out)
	p.recordWaterfall(qr, out)
	completed := buildWaterfallRequest(qr, out)
	p.observeQueue(QueueObservation{Kind: QueueRequestCompleted, Completed: &completed})

	if qr.abandoned.Load() {
		return // caller already left; drop
	}
	select {
	case qr.ResultCh <- out:
	default:
		// cap-1 channel with a single reader; shouldn't happen. Drain-safe.
	}
}

func (p *Pipeline) emitRequestTerminal(qr *QueuedRequest, out ForwardOutcome) {
	event := Observation{
		Type:          ObservationRequestSucceeded,
		Stage:         StageTerminal,
		ResolvedModel: qr.ResolvedModel,
		Model:         qr.ResolvedModel,
		Outcome:       OutcomeSuccess,
	}
	if out.Err != nil {
		event.Type = ObservationRequestFailed
		event.Outcome = OutcomeFailure
		event.ErrorKind = out.ErrorKind
		if event.ErrorKind == "" {
			event.ErrorKind = classifyError(out.Err)
		}
		event.HTTPStatus = out.HTTPStatus
		if errors.Is(out.Err, context.Canceled) || errors.Is(out.Err, context.DeadlineExceeded) {
			event.Type = ObservationRequestCanceled
			event.Outcome = OutcomeCanceled
		}
	}
	qr.emitObservation(event)
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
// request.
func (p *Pipeline) recordStageMetrics(qr *QueuedRequest, out ForwardOutcome) {
	observeStageMetrics(qr, out)
}

// observeStageMetrics is the package-level histogram observer (testable without
// a full Pipeline).
func observeStageMetrics(qr *QueuedRequest, out ForwardOutcome) {
	result := resultLabel(out)

	if s, ok := stageSeconds(qr.stages[ReqStageArrived], qr.stages[ReqStageCredDequeued]); ok {
		metricStageQueueWaitT0T6.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.stages[ReqStageTotalEnqueued], qr.stages[ReqStageTotalDequeued]); ok {
		metricStageTotalQueueT1T2.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.stages[ReqStageModelEnqueued], qr.stages[ReqStageModelDequeued]); ok {
		metricStageModelQueueT3T4.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.stages[ReqStageCredEnqueued], qr.stages[ReqStageCredDequeued]); ok {
		metricStageCredQueueT5T6.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.stages[ReqStageTotalDequeued], qr.stages[ReqStageCredEnqueued]); ok {
		metricStageRoutingT2T5.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.stages[ReqStageCredDequeued], qr.stages[ReqStageForwardStart]); ok {
		metricStageAcquireT6T7.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.stages[ReqStageForwardStart], qr.stages[ReqStageResponseStart]); ok {
		metricStageUpstreamT7T8.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.stages[ReqStageResponseStart], qr.stages[ReqStageResponseEnd]); ok {
		metricStageStreamingT8T9.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(qr.stages[ReqStageArrived], qr.stages[ReqStageResponseEnd]); ok {
		metricStageTotalT0T9.WithLabelValues(result).Observe(s)
	}
}

// routeFailover hands a pre-firstbyte failure to the ③ mover.
func (p *Pipeline) routeFailover(qr *QueuedRequest, out ForwardOutcome) {
	select {
	case p.failoverCh <- failoverItem{qr: qr, out: out}:
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
		p.observeOverflow("shutdown")
		return false
	}
	if !cf.tryReserve() {
		metricOverflow.WithLabelValues("cred_queue_full").Inc()
		p.observeOverflow("cred_queue_full")
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

	qr.journeyMu.Lock()
	cf.handoffMu.Lock()
	select {
	case cf.queue <- qr:
		depth := cf.depth.Load()
		metricCredQueueDepth.WithLabelValues(itoa(cred.CredentialID), cred.ConcurrencyMode).Inc()
		p.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cred.CredentialID, Mode: cred.ConcurrencyMode, Depth: depth, Delta: 1, AbsoluteDepth: true})
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
		qr.emitObservationLocked(Observation{
			Type:          ObservationNodeEnqueued,
			Stage:         StageCredentialQueue,
			ResolvedModel: model,
			Model:         model,
			ProviderID:    int64(cred.ProviderID),
			Provider:      cred.Vendor,
			CredentialID:  int64(cred.CredentialID),
		})
		cf.handoffMu.Unlock()
		qr.journeyMu.Unlock()
		return true
	default:
		cf.handoffMu.Unlock()
		qr.journeyMu.Unlock()
		cf.depth.Add(-1)
		metricOverflow.WithLabelValues("cred_queue_full").Inc()
		p.observeOverflow("cred_queue_full")
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

func (p *Pipeline) getForwarderIfExists(credentialID int) *credForwarder {
	p.credMu.Lock()
	defer p.credMu.Unlock()
	return p.forwarders[credentialID]
}

// queueWaitBudget returns the max pacing wait for a request on its selected
// cred. Since v4 (R1.3) a non-positive budget means ZERO wait: a saturated
// governor fails over immediately instead of parking the request. The old
// 30s hard floor was deliberately REMOVED — see UT-DQ-02.
func (p *Pipeline) queueWaitBudget(qr *QueuedRequest) time.Duration {
	ms := qr.SelectedCred.MaxQueueWaitMS
	if ms <= 0 {
		ms = p.config().MaxQueueWaitMS
	}
	if ms <= 0 {
		return 0
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
