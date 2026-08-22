package dispatch

// governor_snapshot_observer.go — Stage C.2: periodic per-credential
// snapshot collector that emits via the closed-enum metric allowlist
// introduced in C.1. The observer is OPT-IN: the Pipeline owns a nilable
// *governorSnapshotObserver field and only spawns the tick goroutine
// when one is wired (LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER=observe).
//
// Why a new type: the existing SetQueueMirror / SetQueueObservationSink
// observability paths are observation-event driven (push), while the
// governor state is a derived projection (poll). A separate
// SnapshotProvider interface keeps the push/poll split clean — Stage D's
// capacity-aware sort reads the same snapshot API without coupling to
// the metric emit path.
//
// Lifecycle: Start spawns one tick goroutine under the Pipeline's
// existing wg. Stop cancels the ticker and waits for the goroutine to
// exit. Start must be called before Pipeline.Start (or by
// composition-root after SetGovernorBackend); Stop is called from
// Pipeline.Stop before wg.Wait() to ensure the observer drains before
// the credForwarders it walks are cancelled.

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// SnapshotProvider exposes a read-only per-credential snapshot view to
// the observer. Pipeline implements it; tests can substitute a stub.
type SnapshotProvider interface {
	// ForEachCredSnapshot walks the live credForwarders under the
	// pipeline's read-lock and invokes fn for each. The function MUST
	// NOT retain references to the snapshot past return; the
	// implementation may reuse a single buffer.
	ForEachCredSnapshot(fn func(snap GovernorSnapshot) error) error
	// ActiveRevision returns the currently applied GovernorPolicy
	// revision (0 = no policy applied yet). Used to populate
	// snap.SpecRevision so observers can drop stale observations.
	ActiveRevision() uint64
}

// governorSnapshotObserver is the periodic collector. Construct via
// newGovernorSnapshotObserver; never null-out the tick interval.
type governorSnapshotObserver struct {
	tickInterval time.Duration
	provider     SnapshotProvider
	backend      func() GovernorBackend
	revisionFn   func() uint64

	// stopCh is closed by Stop to signal the tick goroutine to exit.
	// Stop blocks on wg.Wait() until the goroutine returns.
	stopCh chan struct{}
	wg     sync.WaitGroup

	// started protects against double-Start. The composition root calls
	// SetGovernorSnapshotObserver (which only sets the field); the
	// observer itself is lazily started inside Pipeline.Start.
	started atomic.Bool

	// nowFn is overridable for deterministic tests.
	nowFn func() time.Time
}

// NewGovernorSnapshotObserver constructs the observer. tickInterval
// must be > 0 (callers default to 100ms when 0 is passed). provider,
// backend, revisionFn are wired later via SetProvider before
// Pipeline.Start is called (composition root order:
//
//	NewPipeline → SetGovernorBackend → SetGovernorSnapshotObserver → Start
//
// NewGovernorSnapshotObserver does NOT itself start the tick goroutine;
// call Start(ctx) explicitly. This separation lets tests construct
// observers without spawning goroutines.
func NewGovernorSnapshotObserver(tick time.Duration) *governorSnapshotObserver {
	if tick <= 0 {
		tick = 100 * time.Millisecond
	}
	return &governorSnapshotObserver{
		tickInterval: tick,
		stopCh:       make(chan struct{}),
		nowFn:        time.Now,
	}
}

// SetProvider wires the read-side dependency. May be called before Start;
// must NOT be called after Start (would race with the tick goroutine).
func (o *governorSnapshotObserver) SetProvider(p SnapshotProvider, backend func() GovernorBackend, revisionFn func() uint64) {
	if o == nil {
		return
	}
	o.provider = p
	o.backend = backend
	o.revisionFn = revisionFn
}

// Start spawns the tick goroutine. Idempotent: a second call without
// an intervening Stop is a no-op.
func (o *governorSnapshotObserver) Start(ctx context.Context) {
	if o == nil {
		return
	}
	if !o.started.CompareAndSwap(false, true) {
		return
	}
	o.wg.Add(1)
	go o.loop(ctx)
}

// Stop signals shutdown and waits for the goroutine to exit. Idempotent.
func (o *governorSnapshotObserver) Stop() {
	if o == nil {
		return
	}
	if !o.started.CompareAndSwap(true, false) {
		return
	}
	close(o.stopCh)
	o.wg.Wait()
}

func (o *governorSnapshotObserver) loop(ctx context.Context) {
	defer o.wg.Done()
	ticker := time.NewTicker(o.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-o.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.tick()
		}
	}
}

func (o *governorSnapshotObserver) tick() {
	if o.provider == nil {
		return
	}
	backend := string(BackendLocal)
	if o.backend != nil {
		if b := o.backend(); b != nil {
			backend = string(b.Kind())
		}
	}
	revision := uint64(0)
	if o.revisionFn != nil {
		revision = o.revisionFn()
	}

	// Collect snapshots first, then emit. This keeps the read-locked
	// section (provider.ForEachCredSnapshot) short and free of metric
	// emit (which may take a lock on the prom registry under contention).
	var collected []GovernorSnapshot
	_ = o.provider.ForEachCredSnapshot(func(snap GovernorSnapshot) error {
		// Validate enforces the bidirectional invariant; panic only when
		// the invariant is violated (State=Unknown ⇔ BackendErr!=nil).
		// We recover here so a single bad snapshot doesn't kill the
		// tick goroutine — the warn message carries enough context.
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("dispatch: observer recovered from snapshot.Validate panic", "panic", r)
				}
			}()
			snap.Validate()
		}()

		// Drop snapshots with off-list labels — same allowlist as the
		// metric registration guard. We trust the SnapshotProvider to
		// fill closed-enum values, but defend in depth.
		if !isBackend(snap.Backend) || !isMode(snap.Mode) {
			slog.Warn("dispatch: observer dropped snapshot with off-list label",
				"backend", snap.Backend, "mode", snap.Mode, "state", string(snap.State))
			metricGovernorUnknownLabelDrops.Inc()
			return nil
		}
		if snap.State == SnapshotStateUnknown {
			// Validate() guarantees BackendErr!=nil in this branch; the
			// snapshot is the normal "backend faulted" path and we
			// don't emit it (no consumer can use it).
			return nil
		}

		// Fill Backend / SpecRevision from the live pipeline if the
		// provider left them empty. Provider implementations are
		// expected to set these, but defense in depth keeps a misuse
		// from regressing into an open-enum label.
		if snap.Backend == "" {
			snap.Backend = backend
		}
		if snap.SpecRevision == 0 {
			snap.SpecRevision = revision
		}
		// AgeMS is provider-owned; observers derive a per-cred wall-clock
		// delta only if the provider explicitly opts in via
		// ProviderWantsDerivedAge. Default is snap.AgeMS as filled by
		// the pipeline (which sets it to the delta from the previous
		// ForEachCredSnapshot visit). See pipeline.go's ForEachCredSnapshot.
		collected = append(collected, snap)
		return nil
	})

	for _, snap := range collected {
		RecordSnapshot(snap)
	}
}
