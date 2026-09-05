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
	RetryScheduler  RetryScheduler
	ObservationSink ObservationSink
	// JournalSink (audit-24h-20260828-r3) receives the per-request attempt
	// journal snapshot exactly once at terminal time. nil = disabled.
	JournalSink          JournalSink
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
	// dueScheduler (v6 G-Ⅱ, 定时请求) parks requests whose DueAt is in the
	// future and re-admits them into Tier-0 at the due time. Unlike
	// retryScheduler it is pipeline-owned: created in Start, closed in Stop.
	dueScheduler *HeapRetryScheduler
	// dimensionIndex (v6 G-Ⅳ, 分维队列) tracks request membership per
	// model/credential/provider dimension. Observation-only — never gates
	// execution.
	dimensionIndex *DimensionIndex
	// queueMirror optionally projects queue state to Redis (observation only).
	queueMirror *QueueMirror
	// queueBackend (V6-W1.7) is the optional CLUSTER admission plane (Tier-0
	// waiting room + lane capacities + due visibility, memory|redis). nil or
	// the local pass-through keeps the pure in-process behavior; redis adds
	// a cluster-wide check BEFORE the local primitives (fail-open on Redis
	// outage). Never moves execution between instances (connection affinity).
	queueBackend QueueBackend
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

	// drainerWg / forwarderWg (audit 2026-09-05 C-#2) sub-track the producers
	// of dispatchIn (runModelDrainer goroutines) and failoverCh (credForwarder
	// loop goroutines) inside the pipeline-wide wg. Dispatcher/failover
	// shutdown drains wait on them so the drain loop's "channel empty"
	// observation is final: a worker that exited while a producer was still
	// handing items in would orphan those requests (Submit caller hangs on
	// qr.ResultCh until its ctx expires — 2h for survival streams).
	// Add sites mirror wg's exactly (getOrCreateModelQueue / newCredForwarder,
	// both shutdown-gated under modelMu / credMu), so a Wait that started after
	// the matching Stop barrier can never race a late Add.
	drainerWg   sync.WaitGroup
	forwarderWg sync.WaitGroup

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

	// journalSink (audit-24h-20260828-r3) holds the terminal-time consumer
	// of the per-request attempt journal. Mirrors observationSink — guarded
	// by its own mutex so emitJournalSnapshot can read lock-free against
	// SetJournalSink.
	journalSinkMu sync.RWMutex
	journalSink   JournalSink

	// journal delivery queue (audit 2026-09-05 C-#4): complete() used to call
	// sink.ApplyJournalSnapshot synchronously, putting an unbounded DB write on
	// the serial completion path (the production sink serializes the whole
	// process under one mutex). Snapshots are now built synchronously in
	// complete() (qr is not safe to touch after complete returns) and handed
	// to a single background worker through this bounded queue. Drops are
	// best-effort-logged like every other terminal side-channel.
	// journalChMu guards create/close/send: the send is non-blocking under the
	// lock so a Stop-time close can never race an in-flight send (send on a
	// closed channel would panic).
	journalChMu   sync.Mutex
	journalCh     chan journalDeliveryItem
	journalClosed bool
	journalWg     sync.WaitGroup

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
	policyMu        sync.Mutex

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

	// pendingGov (Stage F): per-cred Governor produced by ApplyPolicy but
	// not yet attached to a live credForwarder. Consulted by
	// getOrCreateForwarder under credMu so that a policy published BEFORE
	// the first request creates a forwarder uses the spec-derived Governor
	// instead of the CredentialRef snapshot. Map entry is deleted when the
	// forwarder claims it.
	pendingGov map[int]Governor
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

// SetJournalSink replaces the optional terminal-time journal sink. Safe
// before or after Start; a nil sink disables journal emission. Mirrors
// SetObservationSink and is invoked at shutdown by the composition root to
// release the sink reference promptly.
func (p *Pipeline) SetJournalSink(sink JournalSink) {
	if p == nil {
		return
	}
	p.journalSinkMu.Lock()
	p.journalSink = sink
	p.journalSinkMu.Unlock()
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
		journalSink:          deps.JournalSink,
		queueObservationSink: deps.QueueObservationSink,
		affinitySink:         deps.SessionAffinitySink,
		minuteStatsSink:      deps.MinuteStatsSink,
		allowModelChangeFunc: deps.AllowModelChangeFunc,
		retryScheduler:       deps.RetryScheduler,
		models:               make(map[string]*modelQueue),
		forwarders:           make(map[int]*credForwarder),
		snapshotAgeMS:        make(map[int]int64),
		credStateCache:       make(map[int]SnapshotState),
		pendingGov:           make(map[int]Governor),
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
	p.dimensionIndex = NewDimensionIndex(DimensionIndexConfig{
		TTL:            time.Duration(cfg.DimensionTTLSeconds) * time.Second,
		PerKeyCapacity: cfg.DimensionCapacity,
		MaxKeys:        defaultDimensionMaxKeys,
	})
	p.cfg.Store(&cfg)
	return p
}

// DimensionIndex exposes the per-dimension membership index for admin reads
// (v6 G-Ⅳ). Nil-safe callers only — the index is always constructed with the
// pipeline.
func (p *Pipeline) DimensionIndex() *DimensionIndex {
	if p == nil {
		return nil
	}
	return p.dimensionIndex
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

// governorForCredential returns the live Governor for a credential.
//
// specRevision is the policy revision stamped onto the Redis-backed
// GovernorSpec. Cold-start (newCredForwarder) passes p.ActiveRevision() so the
// freshly constructed forwarder carries the currently-active revision;
// ApplyPolicy passes pol.Revision so a hot-swapped Redis governor is tagged
// with the revision the publisher will publish, not the previous one. Backend
// failure is propagated as an ErrGovernorUnavailable-wrapped error so
// ApplyPolicy can fail-closed (no swap, no revision advance);
// newCredForwarder wraps this in a fail-open fallback so a transient Redis
// outage cannot block the very first dispatch to a fresh forwarder.
func (p *Pipeline) governorForCredential(cred CredentialRef, specRevision uint64) (Governor, error) {
	backend := p.GovernorBackend()
	mode := cred.ConcurrencyMode
	if mode == "" {
		mode = ModeConcurrency
	}
	if backend == nil || backend.Kind() != BackendRedisEnforce {
		return newGovernor(cred), nil
	}
	limit := cred.ConcurrencyLimit
	if mode == ModeRPM {
		limit = cred.RPMLimit
	} else if mode == ModeTPM {
		limit = cred.TPMLimit
	}
	if limit <= 0 || mode == ModeDisabled {
		return newGovernor(cred), nil
	}

	gov, err := backend.New(context.Background(), GovernorSpec{
		CredentialID: cred.CredentialID,
		ProviderID:   cred.ProviderID,
		Mode:         mode,
		Limit:        limit,
		RPMLimit:     cred.RPMLimit,
		TPMLimit:     cred.TPMLimit,
		Backend:      BackendRedisEnforce,
		Revision:     specRevision,
	})
	if err == nil && gov != nil {
		return gov, nil
	}
	if err == nil {
		err = errors.New("governor backend returned nil governor")
	}
	slog.Error("dispatch: credential governor initialization failed",
		"credential_id", cred.CredentialID,
		"provider_id", cred.ProviderID,
		"backend", backend.Kind(),
		"revision", specRevision,
		"error", err)
	return nil, fmt.Errorf("%w: %w", ErrGovernorUnavailable, err)
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
//
// NOTE: retained as an internal helper for tests / shadow-load
// scenarios. Production code paths MUST go through Pipeline.ApplyPolicy
// (Stage F) so that the active revision moves together with the
// forwarder governor swap and the backend NotifyRevisions round-trip.
func (p *Pipeline) SetActivePolicyRevision(rev uint64) {
	if p == nil {
		return
	}
	p.activePolicyRevision.Store(rev)
}

// ApplyPolicy is the Stage F live-path entry point for a new
// GovernorPolicy. It is the production implementation behind
// policy_applier.ApplyPolicySnapshot (policy_applier.go:17): a strictly-
// monotonic revision stamps the new active policy; each spec rebuilds
// the per-credential Governor and either swaps it on the live
// credForwarder or queues it for the first forwarder construction.
//
// Backend wiring: when GovernorBackend is BackendRedisEnforce, the
// backend's NotifyRevisions round-trip MUST succeed before any local
// swap or revision stamp advance; otherwise the active revision stays
// put so the next NOTIFY replays the same delta (mirrors the
// publisher's Stage E fail-closed contract).
//
// Mode/limit derivation reuses governorForCredential so the same mode-
// mapping rules govern cold-start and live-swap. Specs whose
// CredentialID is not in the credentials table are still parsed; their
// Governor is built from the spec alone, so a forwarder created later
// for that ID picks up the spec-derived Governor via pendingGov.
func (p *Pipeline) ApplyPolicy(ctx context.Context, pol GovernorPolicy) error {
	if p == nil {
		return errors.New("dispatch: nil pipeline")
	}
	p.policyMu.Lock()
	defer p.policyMu.Unlock()
	if pol.Revision == 0 {
		return errors.New("dispatch: ApplyPolicy revision must be > 0")
	}
	current := p.activePolicyRevision.Load()
	if pol.Revision <= current {
		// No-op per the ApplyPolicySnapshot contract: same-or-older
		// revisions mean the upstream publisher replayed a known delta.
		return nil
	}
	backend := p.GovernorBackend()

	// 1. Backend NotifyRevisions FIRST (fail-closed). We need the cluster-
	// wide pubsub to acknowledge the new revision before any local swap so
	// that a partial failure does not leave this process out of sync with
	// its peers.
	if backend != nil && backend.Kind() == BackendRedisEnforce {
		if err := backend.NotifyRevisions(ctx, pol.Revision); err != nil {
			return fmt.Errorf("dispatch: notify backend revision %d: %w", pol.Revision, err)
		}
	}

	// 2. Build a credentialID -> Governor map from the new specs. Specs
	// without a matching live credForwarder are stashed in pendingGov so
	// getOrCreateForwarder picks them up when the forwarder is finally
	// constructed for that credential.
	//
	// Limit semantics: spec.Limit is mode-dependent (concurrency cap, RPM,
	// or TPM). The canonical contract is one canonical limit per mode, so
	// we route it into the corresponding CredentialRef field based on
	// spec.Mode. When the publisher populates the mirror fields, the max
	// of canonical-vs-mirror wins because a populated mirror is the
	// upstream-declared value (e.g. RPM mirrors TPM when publisher has a
	// richer view).
	newGovByCredID := make(map[int]Governor, len(pol.Specs))
	newDepthByCredID := make(map[int]int, len(pol.Specs))
	defDepth := p.config().MaxQueueDepth
	for _, spec := range pol.Specs {
		cred := specToCredentialRef(spec)
		gov, err := p.governorForCredential(cred, pol.Revision)
		if err != nil {
			// Fail-closed: any backend error leaves activePolicyRevision
			// pinned and pendingGov untouched. The publisher's retry loop
			// will replay the same policy delta on the next NOTIFY.
			return fmt.Errorf("dispatch: build governor for credential %d: %w", spec.CredentialID, err)
		}
		newGovByCredID[spec.CredentialID] = gov
		// Effective queue depth: an explicit max_queue_depth wins; otherwise
		// fall back to the pipeline's global default — the same fallback
		// getOrCreateForwarder uses when constructing a fresh forwarder.
		d := spec.MaxQueueDepth
		if d <= 0 {
			d = defDepth
		}
		newDepthByCredID[spec.CredentialID] = d
	}

	// 3. Swap on live credForwarders and queue the rest.
	p.credMu.Lock()
	for credID, newGov := range newGovByCredID {
		if cf, ok := p.forwarders[credID]; ok {
			cf.replaceGov(newGov)
			// Stage F residual: hot-reload the forwarder's queue depth so a
			// live forwarder tracks max_queue_depth changes instead of the
			// value it captured at construction.
			if want := newDepthByCredID[credID]; int64(want) != cf.Limit() {
				cf.replaceDepth(want)
			}
			continue
		}
		p.pendingGov[credID] = newGov
	}
	// 4. Stamp the active revision while forwarders remain locked so an
	// observer cannot pair a new Governor with the old policy revision.
	p.activePolicyRevision.Store(pol.Revision)
	p.credMu.Unlock()
	return nil
}

// specToCredentialRef derives the CredentialRef for a GovernorSpec.
//
// spec.Limit is mode-dependent: the canonical limit for the credential's
// mode. RPMLimit/TPMLimit on the spec are upstream mirrors — when both are
// populated we use the max so a richer upstream view wins, matching the
// pre-Stage-F publisher behavior.
//
// This belongs in pipeline.go rather than governor_spec.go because it is
// part of the policy-application contract, not the backend-call contract.
func specToCredentialRef(spec GovernorSpec) CredentialRef {
	cred := CredentialRef{
		CredentialID:    spec.CredentialID,
		ProviderID:      spec.ProviderID,
		ConcurrencyMode: spec.Mode,
		MaxQueueDepth:   spec.MaxQueueDepth,
		MaxQueueWaitMS:  spec.MaxQueueWaitMS,
	}
	switch spec.Mode {
	case ModeRPM:
		cred.RPMLimit = spec.Limit
		if spec.RPMLimit > cred.RPMLimit {
			cred.RPMLimit = spec.RPMLimit
		}
		cred.TPMLimit = spec.TPMLimit
	case ModeTPM:
		cred.TPMLimit = spec.Limit
		if spec.TPMLimit > cred.TPMLimit {
			cred.TPMLimit = spec.TPMLimit
		}
		cred.RPMLimit = spec.RPMLimit
	default:
		// ModeConcurrency or ModeDisabled or empty (default concurrency).
		cred.ConcurrencyLimit = spec.Limit
		cred.RPMLimit = spec.RPMLimit
		cred.TPMLimit = spec.TPMLimit
	}
	return cred
}

// ActiveRevision satisfies SnapshotProvider; returns the current
// GovernorPolicy revision (0 = no policy applied yet).
func (p *Pipeline) ActiveRevision() uint64 {
	if p == nil {
		return 0
	}
	return p.activePolicyRevision.Load()
}

var _ ApplyPolicySnapshot = (*Pipeline)(nil)

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

	// Stage F: capture the Governor under govMu.RLock. The caller already
	// holds credMu, so ordering is credMu -> govMu which matches the
	// ApplyPolicy path (credMu -> govMu.Lock). Holding RLock for the type-
	// switch block is fine: the observer tick is the only goroutine that
	// walks forwarders in this way, and ApplyPolicy holds govMu.Lock for
	// only the duration of the pointer assignment.
	gov := cf.govLocked()

	snap := GovernorSnapshot{
		SpecRevision: p.activePolicyRevision.Load(),
		Backend:      backendKind,
		Mode:         gov.Mode(),
		Limit:        int(cf.limit.Load()),
		InFlight:     int(cf.depth.Load()),
		QueueDepth:   int(cf.depth.Load()),
		State:        SnapshotStateReady,
	}
	if !cf.HasCapacity() {
		snap.State = SnapshotStateQueueFull
	}
	// Pull governor-specific "Used" counters without changing the
	// Governor interface — type-assert on the four impls.
	switch g := gov.(type) {
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

	// v6 G-Ⅱ (定时请求): pipeline-owned due scheduler. Parked requests are
	// parked at Tier-0 drain time and re-admitted via onScheduledDue; Close
	// completes still-parked requests with ErrShutdown so Submit callers
	// never block through a shutdown.
	p.dueScheduler = NewHeapRetrySchedulerWithCloseHandler(
		p.onScheduledDue,
		func(qr *QueuedRequest) { p.complete(qr, ForwardOutcome{Err: ErrShutdown}) },
		nil, nil)

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
	// v6 G-Ⅱ: close the due scheduler BEFORE waiting on workers so parked
	// scheduled requests complete with ErrShutdown instead of leaking their
	// Submit callers.
	if p.dueScheduler != nil {
		p.dueScheduler.Close()
	}
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
	// Audit 2026-09-05 C-#4: close the journal snapshot delivery queue and
	// give its worker a bounded drain window so the last batch of terminal
	// snapshots is flushed before the process exits instead of dying in the
	// FIFO. The close is under journalChMu so it cannot race an in-flight
	// enqueueJournalSnapshot send; late senders see journalClosed and drop.
	p.journalChMu.Lock()
	p.journalClosed = true
	if p.journalCh != nil {
		close(p.journalCh)
	}
	p.journalChMu.Unlock()
	journalDone := make(chan struct{})
	go func() {
		p.journalWg.Wait()
		close(journalDone)
	}()
	select {
	case <-journalDone:
	case <-time.After(journalDrainTimeout):
		// A wedged sink must not hang process exit; the worker keeps draining
		// in the background and exits once the channel (already closed) is
		// empty. Queued snapshots are lost — best-effort contract.
		slog.Warn("dispatch: journal sink drain timed out on Stop, dropping queued snapshots",
			"timeout", journalDrainTimeout)
	}
	p.queueMirror.Close()
	// V6-W1.7: stop the cluster admission plane's background work (redis
	// heartbeat loop) after every worker has drained.
	if p.queueBackend != nil {
		_ = p.queueBackend.Close()
	}
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
	// v6 G-Ⅱ: bound scheduled requests to maxScheduleAhead so dead client
	// timers cannot park in the due heap forever.
	if qr.DueAt.After(time.Now().Add(maxScheduleAhead)) {
		p.emitRequestTerminal(qr, ForwardOutcome{Err: ErrScheduleTooFar})
		return nil, ErrScheduleTooFar
	}
	// Stamp before the FIFO hand-off. After totalQueue accepts the request its
	// worker may complete it immediately, so later writes would race metrics.
	if qr.EnqueuedAt.IsZero() {
		qr.EnqueuedAt = time.Now()
	}
	qr.SetT1_TotalEnqueued()
	// LifecycleRegistry is a projection only. Its admission result must never
	// reject execution; totalQueue is the sole execution capacity boundary.
	p.registry.RegisterPending(qr.ID, qr.RequestedModel, time.Now())
	p.dimensionIndex.Track(qr, time.Now())
	// V6-W1.7: cluster admission first, then the local CAS. When the local
	// bound refuses after the cluster admitted, the compensating release
	// below returns the token (admit/release symmetry). No backend / local
	// backend → admitTotal is a free pass-through.
	if !p.admitTotal(ctx, qr) || !p.totalQueue.tryEnqueue(qr) {
		p.releaseClusterTotal(qr)
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
		p.complete(qr, ForwardOutcome{Err: ctx.Err()})
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
			// C-#14 (audit round2): recover per item so a panic in the
			// scheduling callbacks cannot kill the drainer goroutine; the
			// take-once releases make the recovery sweep safe (same pattern
			// as forwarder.attempt).
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						slog.Error("total drainer panic recovered",
							"request_id", qr.ID, "panic", recovered)
						p.releaseTotal(qr)
						p.complete(qr, ForwardOutcome{
							Err:       fmt.Errorf("total drainer panic: %v", recovered),
							ErrorKind: "dispatch_panic",
						})
					}
				}()
				p.drainTotalOne(qr)
			}()
		case <-p.stopCh:
			p.drainTotalQueueResidue()
			return
		}
	}
}

// drainTotalOne processes one Tier-0 item (schedule park or model-lane
// hand-off). Split from runTotalDrainer so the panic guard stays readable.
func (p *Pipeline) drainTotalOne(qr *QueuedRequest) {
	if p.shutdown.Load() {
		p.releaseTotal(qr)
		p.complete(qr, ForwardOutcome{Err: ErrShutdown})
		// Audit 2026-09-05 C-#2: complete the CURRENT request only was
		// not enough — the rest of the Tier-0 buffer (capacity 1000)
		// must be drained too, or those Submit callers hang until
		// their ctx expires.
		p.drainTotalQueueResidue()
		return
	}
	// v6 G-Ⅱ (定时请求): a future DueAt parks the request back
	// into the pending set (due heap) instead of executing it. The
	// Tier-0 slot is released so scheduled backlog never consumes
	// the waiting room; the promoter re-admits at the due time.
	// minScheduleLead: a DueAt inside the lead window executes
	// immediately — parking must leave enough room to finish the
	// park-side metadata writes before the picker takes ownership.
	if qr.DueAt.After(time.Now().Add(minScheduleLead)) {
		if p.parkScheduledRequest(qr) {
			p.releaseTotal(qr)
			return
		}
		// Scheduler unavailable (shutting down) → execute now.
	}
	if !p.enqueueModelFromTotal(queueKeyFor(qr.RequestedModel), qr) {
		p.releaseTotal(qr)
		if p.shutdown.Load() {
			p.complete(qr, ForwardOutcome{Err: ErrShutdown})
			p.drainTotalQueueResidue()
			return
		}
		p.complete(qr, ForwardOutcome{Err: ctxOf(qr).Err()})
	} else {
		p.releaseTotal(qr)
	}
}

// drainTotalQueueResidue completes every request still buffered in the Tier-0
// total FIFO with ErrShutdown (audit 2026-09-05 C-#2). Non-blocking: producers
// (Submit / onScheduledDue) check shutdown before tryEnqueue, so the buffer is
// final for practical purposes by the time stopCh fired; anything that still
// races in afterwards is the pre-existing Submit-vs-Stop admission window
// (bounded by the caller's ctx), unchanged by this fix.
func (p *Pipeline) drainTotalQueueResidue() {
	for {
		select {
		case qr := <-p.totalQueue.ch:
			if qr == nil {
				continue
			}
			p.releaseTotal(qr)
			p.complete(qr, ForwardOutcome{Err: ErrShutdown})
		default:
			return
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
		// V6-W1.7: cluster lane slot, reserved per attempt; released again
		// when the local lane cannot take the request (backpressure loop).
		// No backend / local → free pass-through.
		if p.reserveLane(ctxOf(qr), LaneModel, name, p.config().MaxQueueDepth, &qr.clusterModel) {
			mq.mu.Lock()
			// Audit 2026-09-05 C-#2: shutdown gate INSIDE the mq.mu section so
			// check+send is atomic against drainModelQueueOnShutdown — a sender
			// that already holds mq.ch must either have completed its send
			// (the drain then collects the request) or never get in at all.
			if p.shutdown.Load() {
				mq.mu.Unlock()
				p.releaseLaneAdmission(&qr.clusterModel)
				return false
			}
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
				qr.emitObservation(Observation{Type: ObservationModelEnqueued, Stage: StageModelQueue, Model: name, ResolvedModel: qr.resolvedModel()})
				mq.mu.Unlock()
				return true
			default:
				mq.depth.Add(-1)
				metricModelQueueDepth.WithLabelValues().Dec()
				mq.mu.Unlock()
				p.releaseLaneAdmission(&qr.clusterModel)
			}
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

// minScheduleLead is the minimum remaining time before DueAt for the drainer
// to park a scheduled request. Inside the window the request executes
// immediately: Schedule() hands ownership to the picker goroutine, so all
// park-side writes must complete strictly before the picker can fire.
const minScheduleLead = 100 * time.Millisecond

// parkScheduledRequest parks a not-yet-due scheduled request in the due heap
// (v6 G-Ⅱ). All metadata is written BEFORE Schedule — Schedule is the
// ownership handoff to the picker goroutine, after which this goroutine must
// not touch qr. Returns false when the scheduler refused (shutting down);
// the caller then treats the request as immediate (the acceptance notice may
// already have been sent — harmless: the request simply runs right away).
func (p *Pipeline) parkScheduledRequest(qr *QueuedRequest) bool {
	if p == nil || p.dueScheduler == nil || ctxOf(qr).Err() != nil {
		return false
	}
	// Cancellation race guard (audit 2026-09-05 round2 C-#12): a terminal
	// complete()/abandon in the caller's goroutine must not be followed by
	// journal/registry/mirror writes for a request that already left.
	if qr.completed.Load() || qr.abandoned.Load() {
		return false
	}
	dueAt := qr.DueAt
	now := time.Now()
	at := dueAt
	qr.emitObservation(Observation{
		Type:          ObservationRetryScheduled,
		Stage:         StageRetrying,
		Model:         qr.RequestedModel,
		ResolvedModel: qr.resolvedModel(),
		RetryReason:   "scheduled_wait",
		RetryAt:       &at,
	})
	p.registry.MarkRetryScheduled(qr.ID, dueAt)
	p.queueMirror.MirrorRetryAt(qr.ID, dueAt)
	// v6 G-Ⅱ: dedicated scheduled key so the Redis-side pending set
	// distinguishes 定时停靠 from failure backoff (observation-only).
	p.queueMirror.MirrorScheduledAt(qr.ID, dueAt)
	// V6-W1.7: cluster due view (observation-only; pickup stays local).
	if p.queueBackend != nil {
		_ = p.queueBackend.ParkDue(context.WithoutCancel(ctxOf(qr)), qr.ID, dueAt)
	}
	qr.recordDecision(JournalEntry{
		Model:   qr.resolvedModel(),
		Action:  NextActionScheduledWait,
		Attempt: qr.AttemptCount,
		At:      now,
	})
	p.dimensionIndex.UpdateWait(qr, dueAt, NextActionScheduledWait, now)
	metricScheduledParked.Inc()
	qr.notifyDispatch(DispatchNotice{
		Kind:     NoticeKindScheduled,
		Message:  scheduledAcceptedMessage(dueAt),
		RetryAt:  dueAt,
		WaitHint: waitHint(dueAt.Sub(now)),
		ToModel:  qr.RequestedModel,
		Attempt:  qr.AttemptCount,
	})
	return p.dueScheduler.Schedule(qr, dueAt)
}

// onScheduledDue is the due-scheduler pickup: the parked request re-enters
// Tier-0 admission at (or just after) its DueAt.
func (p *Pipeline) onScheduledDue(qr *QueuedRequest, dueAt time.Time) {
	if qr == nil || qr.completed.Load() {
		return // already terminal (client cancel raced the timer)
	}
	if p.shutdown.Load() {
		p.complete(qr, ForwardOutcome{Err: ErrShutdown})
		return
	}
	if ctxOf(qr).Err() != nil {
		p.complete(qr, ForwardOutcome{Err: ctxOf(qr).Err()})
		return
	}
	now := time.Now()
	p.registry.MarkInFlight(qr.ID, now)
	p.queueMirror.ClearRetryAt(qr.ID)
	p.queueMirror.ClearScheduledAt(qr.ID)
	if p.queueBackend != nil {
		_ = p.queueBackend.ClearDue(context.WithoutCancel(ctxOf(qr)), qr.ID)
	}
	metricScheduledDue.Inc()
	qr.notifyDispatch(DispatchNotice{
		Kind:    NoticeKindScheduled,
		Message: "定时请求到期，开始执行",
		RetryAt: dueAt,
	})
	if !p.admitTotal(ctxOf(qr), qr) || !p.totalQueue.tryEnqueue(qr) {
		p.releaseClusterTotal(qr)
		metricOverflow.WithLabelValues("total_queue_full_on_due").Inc()
		p.observeOverflow("total_queue_full_on_due")
		p.complete(qr, ForwardOutcome{Err: &OverflowError{Reason: "total_queue_full_on_due", RetryAfter: DefaultOverflowRetryAfter}})
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
	// V6-W1.7: cluster lane slot before the local channel; released again
	// when the local lane refuses (caller follows the existing full path).
	if !p.reserveLane(ctxOf(qr), LaneModel, name, p.config().MaxQueueDepth, &qr.clusterModel) {
		return false
	}
	resolvedModel := qr.resolvedModel()
	qr.journeyMu.Lock()
	enqueuedAt := time.Now()
	mq.mu.Lock()
	// Audit 2026-09-05 C-#2: shutdown gate INSIDE the mq.mu section (same
	// rationale as enqueueModelFromTotal — check+send must be atomic against
	// drainModelQueueOnShutdown so a raced enqueue can never land in a lane
	// whose drainer already exited).
	if p.shutdown.Load() {
		mq.mu.Unlock()
		qr.journeyMu.Unlock()
		p.releaseLaneAdmission(&qr.clusterModel)
		return false
	}
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
		p.releaseLaneAdmission(&qr.clusterModel)
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
	p.drainerWg.Add(1) // audit 2026-09-05 C-#2: sub-track dispatchIn producers
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
//
// Shutdown also drains whatever is still BUFFERED in mq.ch (audit 2026-09-05
// C-#2): Stop clears p.models and the drainer exits, so residue left in the
// lane's channel would never be completed either. See
// TestStopDrainsModelQueueResidue.
func (p *Pipeline) runModelDrainer(mq *modelQueue) {
	defer p.wg.Done()
	defer p.drainerWg.Done() // audit 2026-09-05 C-#2 (runs first; wg.Done last as before)
	for {
		select {
		case qr := <-mq.ch:
			mq.mu.Lock()
			depth := mq.depth.Add(-1)
			metricModelQueueDepth.WithLabelValues().Dec()
			slog.Debug("dispatch: model dequeue", "model", mq.name, "depth", depth)
			p.observeQueue(QueueObservation{Kind: QueueModelDepth, Model: mq.name, Depth: depth, Delta: -1, AbsoluteDepth: true})
			mq.mu.Unlock()
			// V6-W1.7: the request left the model lane — return the cluster
			// slot at the same point the local depth is given back.
			p.releaseLaneAdmission(&qr.clusterModel)

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
				// Anything still buffered in the lane has the same fate now
				// that this drainer is about to exit.
				p.drainModelQueueOnShutdown(mq)
				return
			}
		case <-p.stopCh:
			p.drainModelQueueOnShutdown(mq)
			return
		}
	}
}

// drainModelQueueOnShutdown completes every request still buffered in a model
// lane with ErrShutdown (audit 2026-09-05 C-#2). It runs under mq.mu so the
// "channel is empty" observation is atomic w.r.t. enqueueModel /
// enqueueModelFromTotal: both check p.shutdown INSIDE their mq.mu section
// before sending, so once the drain observes an empty channel under the lock,
// no later enqueue can land (any sender entering mq.mu afterwards observes
// shutdown=true — Stop's shutdown store happens-before close(stopCh), which
// happens-before this drain).
func (p *Pipeline) drainModelQueueOnShutdown(mq *modelQueue) {
	if p == nil || mq == nil {
		return
	}
	mq.mu.Lock()
	var residue []*QueuedRequest
	for {
		select {
		case qr := <-mq.ch:
			residue = append(residue, qr)
		default:
			mq.mu.Unlock()
			for _, qr := range residue {
				mq.depth.Add(-1)
				metricModelQueueDepth.WithLabelValues().Dec()
				p.releaseLaneAdmission(&qr.clusterModel)
				p.complete(qr, ForwardOutcome{Err: ErrShutdown})
			}
			if n := len(residue); n > 0 {
				slog.Warn("dispatch: model queue residue completed on shutdown",
					"model", mq.name, "requests", n)
			}
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
	// V6-W1.6 R9: the terminal journal entry rides inside the completion CAS
	// so it is written exactly once and no entry can follow it (invariant 3).
	// Written before Complete() so the dimension snapshots see the full trace.
	terminalKind := out.ErrorKind
	if terminalKind == "" && out.Err != nil {
		terminalKind = classifyError(out.Err)
	}
	cred := qr.selectedCredential()
	qr.recordDecision(JournalEntry{
		Model:        qr.resolvedModel(),
		CredentialID: cred.CredentialID,
		ProviderID:   cred.ProviderID,
		Vendor:       cred.Vendor,
		Action:       terminalActionOf(out),
		ErrorKind:    terminalKind,
		HTTPStatus:   out.HTTPStatus,
		Attempt:      qr.AttemptCount,
	})
	// V4 R1.1: registry terminal transition (completed; kept until the
	// R1.8 watermark evicts it). Bookkeeping bypass — never gates delivery.
	now := time.Now()
	p.registry.MarkCompleted(qr.ID, now)
	p.queueMirror.ClearRetryAt(qr.ID)
	p.queueMirror.ClearScheduledAt(qr.ID)
	// V6-W1.7: due view cleanup + defensive admission sweep. The lane
	// leave-points normally released the tokens already; take-once makes
	// this a no-op then (a safety net when a request dies parked in a lane).
	if p.queueBackend != nil {
		_ = p.queueBackend.ClearDue(context.WithoutCancel(ctxOf(qr)), qr.ID)
	}
	p.releaseAllClusterAdmissions(qr)
	// v6 G-Ⅳ: terminal transition mutates the membership entries in place;
	// entries stay in their dimension rings until TTL/capacity evicts them.
	p.dimensionIndex.Complete(qr, out, now)

	// V3.1: Record T9 timestamp (response end - stream completed)
	qr.SetT9_ResponseEnd()
	p.recordSessionAffinity(qr, out)
	p.recordMinuteStats(qr, out)
	p.emitRequestTerminal(qr, out)
	// audit-24h-20260828-r3: ship the per-request attempt journal to the
	// optional JournalSink so post-hoc ops/CS diagnosis can see the trace
	// after the global /api/admin/dispatch/journal endpoint was removed in
	// ff18dc3b6. Runs before the abandoned-guard so a caller that already
	// left still gets the trace persisted (the trace is for the system, not
	// the caller). Exactly-once is guaranteed by the CAS at line 1402.
	p.emitJournalSnapshot(qr)

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
		ResolvedModel: qr.resolvedModel(),
		Model:         qr.resolvedModel(),
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

// emitJournalSnapshot reads the optional terminal-time journal sink and, if
// present and the journal is non-empty, queues a detached snapshot for
// delivery. The journal's terminal entry is already in the ring at this point
// (recordDecision at the top of complete), so consumers see the full trace.
//
// Audit 2026-09-05 C-#4: the snapshot is BUILT synchronously here (qr's
// lifecycle ends when complete returns — JournalSnapshot() returns a detached
// copy, verified in journal.go) but DELIVERED asynchronously by a single
// background worker: the production sink (main_dispatch_observation.go
// journey adapter) serializes the whole process behind one mutex and performs
// untimed DB writes, so a synchronous ApplyJournalSnapshot on the completion
// path was a global head-of-line blocker whenever the DB degraded. Delivery
// remains best-effort: a full queue drops the snapshot with a metric + log,
// like every other terminal side-channel (observations, queue mirror).
//
// Per ADR 2026-08-28-requestjourney-journal-snapshot.md §Decision point 3,
// snapshots are bounded by maxJournalSnapshotEvents. When the journal exceeds
// this limit, the oldest entries are dropped and the snapshot's Truncated
// field is set to true, preserving the most recent execution context.
//
// §Decision point 4 / §6 extensions (audit-24h-20260829-r5 §5.5 merged):
// SnapshotVersion is the terminal-time journalSeq (idempotency key);
// CallerTenantID / CallerAuthorized are stamped from the trusted dispatch
// path so downstream sinks can short-circuit duplicate retries and reject
// unauthorized callers.
func (p *Pipeline) emitJournalSnapshot(qr *QueuedRequest) {
	if p == nil || qr == nil {
		return
	}
	p.journalSinkMu.RLock()
	sink := p.journalSink
	p.journalSinkMu.RUnlock()
	if sink == nil {
		return
	}
	entries := qr.JournalSnapshot()
	if len(entries) == 0 {
		return
	}

	// Apply bounded consumer limit: keep the most recent maxJournalSnapshotEvents
	// entries. The terminal entry is always included (it's the newest).
	var truncated bool
	var truncatedCount int
	if len(entries) > maxJournalSnapshotEvents {
		truncatedCount = len(entries) - maxJournalSnapshotEvents
		entries = entries[truncatedCount:]
		truncated = true
	}

	snap := JournalSnapshot{
		TenantID:         qr.TenantID,
		RequestID:        qr.ID,
		Entries:          entries,
		Truncated:        truncated,
		TruncatedCount:   truncatedCount,
		SnapshotVersion:  int64(qr.journalSeq),
		CallerTenantID:   qr.TenantID, // trusted in dispatch's terminal path
		CallerAuthorized: true,
	}
	p.enqueueJournalSnapshot(journalDeliveryItem{
		// Capture the request ctx (cancel-detached) now: qr is not safe to
		// read after complete() returns.
		ctx:  context.WithoutCancel(ctxOf(qr)),
		snap: snap,
	})
}

// journalDeliveryItem is one queued snapshot hand-off to the background
// delivery worker. ctx is the completion-time request ctx with cancellation
// stripped (mirrors the previous synchronous sink invocation).
type journalDeliveryItem struct {
	ctx  context.Context
	snap JournalSnapshot
}

// journalDeliveryQueueCapacity bounds the snapshot FIFO between complete()
// and the delivery worker. Sized for terminal-rate bursts; overflow drops.
const journalDeliveryQueueCapacity = 256

// journalDrainTimeout bounds how long Stop() waits for the delivery worker to
// flush the queue on shutdown — enough to keep the last batch of snapshots
// without letting a wedged sink hang process exit.
const journalDrainTimeout = 5 * time.Second

// enqueueJournalSnapshot hands one snapshot to the background delivery
// worker, spawning the worker lazily on first use (a pipeline that never
// wires a sink — or never completes a journaled request — pays nothing).
// The send is non-blocking UNDER journalChMu so it can never race Stop's
// close of the same channel (send-on-closed panics otherwise).
func (p *Pipeline) enqueueJournalSnapshot(item journalDeliveryItem) {
	p.journalChMu.Lock()
	defer p.journalChMu.Unlock()
	if p.journalClosed {
		// Pipeline already stopped; the delivery worker is gone. Drop with a
		// metric — terminal delivery is best-effort by contract.
		metricJournalSnapshotDropped.WithLabelValues("stopped").Inc()
		return
	}
	if p.journalCh == nil {
		p.journalCh = make(chan journalDeliveryItem, journalDeliveryQueueCapacity)
		p.journalWg.Add(1)
		go p.runJournalDelivery()
	}
	select {
	case p.journalCh <- item:
	default:
		// Queue full: drop rather than block the completion path (the very
		// head-of-line blocking this queue exists to prevent).
		metricJournalSnapshotDropped.WithLabelValues("queue_full").Inc()
		slog.Warn("dispatch: journal snapshot delivery queue full, dropping snapshot",
			"request_id", item.snap.RequestID,
			"capacity", journalDeliveryQueueCapacity)
	}
}

// runJournalDelivery is the single background consumer of the snapshot FIFO.
// It reads the sink per item under journalSinkMu so SetJournalSink swaps at
// runtime are honored (same contract as the observation worker).
func (p *Pipeline) runJournalDelivery() {
	defer p.journalWg.Done()
	for item := range p.journalCh {
		p.journalSinkMu.RLock()
		sink := p.journalSink
		p.journalSinkMu.RUnlock()
		if sink == nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("dispatch journal sink panic", "request_id", item.snap.RequestID, "panic", r)
				}
			}()
			sink.ApplyJournalSnapshot(item.ctx, item.snap)
		}()
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
// request.
func (p *Pipeline) recordStageMetrics(qr *QueuedRequest, out ForwardOutcome) {
	observeStageMetrics(qr, out)
}

// observeStageMetrics is the package-level histogram observer (testable without
// a full Pipeline).
func observeStageMetrics(qr *QueuedRequest, out ForwardOutcome) {
	result := resultLabel(out)
	t0, t1, t2, t3, t4, t5, t6, t7, t8, t9 := qr.StageTimestamps()
	stage := func(t *time.Time) time.Time {
		if t == nil {
			return time.Time{}
		}
		return *t
	}
	stages := [10]time.Time{stage(t0), stage(t1), stage(t2), stage(t3), stage(t4), stage(t5), stage(t6), stage(t7), stage(t8), stage(t9)}

	if s, ok := stageSeconds(stages[ReqStageArrived], stages[ReqStageCredDequeued]); ok {
		metricStageQueueWaitT0T6.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(stages[ReqStageTotalEnqueued], stages[ReqStageTotalDequeued]); ok {
		metricStageTotalQueueT1T2.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(stages[ReqStageModelEnqueued], stages[ReqStageModelDequeued]); ok {
		metricStageModelQueueT3T4.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(stages[ReqStageCredEnqueued], stages[ReqStageCredDequeued]); ok {
		metricStageCredQueueT5T6.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(stages[ReqStageTotalDequeued], stages[ReqStageCredEnqueued]); ok {
		metricStageRoutingT2T5.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(stages[ReqStageCredDequeued], stages[ReqStageForwardStart]); ok {
		metricStageAcquireT6T7.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(stages[ReqStageForwardStart], stages[ReqStageResponseStart]); ok {
		metricStageUpstreamT7T8.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(stages[ReqStageResponseStart], stages[ReqStageResponseEnd]); ok {
		metricStageStreamingT8T9.WithLabelValues(result).Observe(s)
	}
	if s, ok := stageSeconds(stages[ReqStageArrived], stages[ReqStageResponseEnd]); ok {
		metricStageTotalT0T9.WithLabelValues(result).Observe(s)
	}
}

// routeFailover hands a pre-firstbyte failure to the ③ mover.
func (p *Pipeline) routeFailover(qr *QueuedRequest, out ForwardOutcome) {
	// Terminal race (audit 2026-09-05 C-#1): if the caller's ctx.Done path
	// already CAS-won complete(), handoff to the mover would race its
	// unsynchronized journal/count mutations — drop instead.
	if qr.completed.Load() || qr.abandoned.Load() {
		return
	}
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
	// V6-W1.7: cluster lane capacity mirrors the local forwarder bound
	// (per-cred override wins over config, same as getOrCreateForwarder).
	laneCap := cred.MaxQueueDepth
	if laneCap <= 0 {
		laneCap = p.config().MaxQueueDepth
	}
	if !p.reserveLane(ctxOf(qr), LaneCredential, itoa(cred.CredentialID), laneCap, &qr.clusterCred) {
		metricOverflow.WithLabelValues("cred_queue_full").Inc()
		p.observeOverflow("cred_queue_full")
		return false
	}
	if !cf.tryReserve() {
		p.releaseLaneAdmission(&qr.clusterCred)
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
	reqID, model, emitCtx := qr.ID, qr.resolvedModel(), ctxOf(qr)

	qr.journeyMu.Lock()
	cf.handoffMu.Lock()
	select {
	case *cf.queue.Load() <- qr:
		// The forwarder waits on handoffMu before reading the request, so this
		// successful-send marker is published before its node-selected event.
		// Keeping MarkNode after the send prevents the observation index from
		// advertising a credential/provider lane that rejected the request.
		p.dimensionIndex.MarkNode(qr, cred, time.Now())
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
		p.releaseLaneAdmission(&qr.clusterCred)
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
	// Stage F: prefer the spec-derived Governor queued by an earlier
	// ApplyPolicy over the CredentialRef snapshot. Fall back to the
	// ref-derived Governor when no spec has been published yet.
	if cached, ok := p.pendingGov[cred.CredentialID]; ok && cached != nil {
		cf.replaceGov(cached)
		delete(p.pendingGov, cred.CredentialID)
	}
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
	ms := qr.selectedCredential().MaxQueueWaitMS
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
