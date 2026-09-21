package autoroute

// outcome_feedback.go — real-outcome backfill for the routingopt feedback loop
// (2026-09-08 24h audit, round 2 Track A).
//
// Before this file, recordFeedbackAsync fired the plugin's RecordFeedback at
// DECISION time with IsSuccess hardcoded true — an open feedback loop: the
// optimizer's learning input never saw a failure. The real outcome only
// existed on the request-completion path (request_logs success/latency/cost)
// and in the async settle chain (auto_route_selections ← settle worker), which
// routingopt never consumed.
//
// The closed loop:
//
//  1. Decide (fresh decision only) → recordFeedbackAsync. When the relay layer
//     injected the real X-Request-Id into the Decide context (maybeResolveAuto
//     → WithRequestID), the feedback row is STASHED here instead of written —
//     decision-time placeholder writes for correlatable requests are gone.
//  2. The request completes → domains/streaming calls ReportRoutingOutcome
//     with the terminal success/latency/cost (same choke points that emit
//     tuning signals and terminal request_logs rows). The stashed feedback is
//     popped, filled with the REAL outcome, and written through the same
//     bounded fire-and-forget dispatch as before.
//  3. Requests that never report an outcome (rate-limited before execution,
//     process crash, >TTL streams) are evicted after pendingFeedbackTTL by a
//     lazy janitor and DROPPED — counted, never written. A decision whose
//     outcome never arrived carries no learnable signal, and writing the old
//     IsSuccess=true placeholder would turn every such request into a false
//     positive training row (2026-09-09 audit round 3: rate-limited auto
//     requests were systematically labelled success).
//
// Import direction: domains/streaming already imports autoroute (decider
// wiring); ReportRoutingOutcome is called from that side, so no new edge —
// and no cycle — is created. autoroute itself stays I/O free here: it only
// remembers in-process state, it never polls the database.
//
// Non-blocking guarantees (unchanged from the decision-time writer): the
// write still goes through the feedbackWriteSlots bounded semaphore with
// drop-on-full semantics and a panic-recovering goroutine. The registry
// itself is bounded (maxPendingFeedback) and never blocks Decide: an
// overflowing stash evicts the oldest entry (dropped, counted — same
// no-signal-no-write rule as the TTL janitor).

import (
	"container/list"
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/routingopt"
)

// RoutingOutcome is the terminal result of one auto-routed request, reported
// by the request-completion path (domains/streaming) once success, latency
// and cost are final. It mirrors the columns the settle worker later joins
// from request_logs_hot, but arrives in-process instead of via DB polling.
type RoutingOutcome struct {
	// RequestID is the correlation id (X-Request-Id) the decision was stashed
	// under. Empty ids are ignored — they cannot match anything.
	RequestID string
	// OriginActor is the request's origin_actor (X-Gw-Source-Actor). Synthetic
	// rounds (goal-% shadow rounds, internal title/summary loopbacks) are
	// dropped by ReportRoutingOutcome (R35-R2 灰度口径).
	OriginActor string
	// Success is the terminal request_logs outcome (2xx upstream response).
	Success bool
	// LatencyMs is the settled end-to-end latency (0 when unknown).
	LatencyMs int64
	// CostUSD is the settled request cost (0 when unknown).
	CostUSD float64
}

// pendingFeedbackEntry is one decision awaiting its outcome. optimizer is the
// plugin the decision was made with — the registry is package-level, but each
// entry ships back through its own decider's optimizer, so tests (and any
// future multi-decider wiring) never cross wires.
type pendingFeedbackEntry struct {
	fb        *routingopt.RoutingFeedback
	optimizer RoutingOptimizer
	stashedAt time.Time
	// elem is this entry's node in pendingFeedbackOrder, giving O(1) removal
	// (2026-09-09 audit round 3: the old []string order index made every
	// removal an O(n) scan+shift inside the global mutex — on the Decide hot
	// path when the registry ran near capacity).
	elem *list.Element
}

var (
	// pendingFeedbackTTL is how long a stashed decision waits for its outcome
	// before the janitor drops it. Longer than any sane request (streams
	// included); var so tests can shrink it.
	pendingFeedbackTTL = 10 * time.Minute

	// maxPendingFeedback bounds the registry. 8k pending decisions ≈ tens of
	// KB; beyond that the oldest entries are evicted (dropped, counted) rather
	// than letting a reporting outage grow memory without bound. Var so tests
	// can shrink it.
	maxPendingFeedback = 8192

	pendingFeedbackMu    sync.Mutex
	pendingFeedback      = map[string]*pendingFeedbackEntry{}
	pendingFeedbackOrder list.List // insertion-ordered request ids, for O(1) oldest eviction

	pendingJanitorOnce sync.Once

	// Observability counters (read by RoutingOutcomeStats; asserted in tests).
	outcomeMatched atomic.Int64 // ReportRoutingOutcome found its decision
	outcomeOrphan  atomic.Int64 // outcome with no stashed decision (non-auto request, cache hit, already settled)
	outcomeExpired atomic.Int64 // entries dropped on TTL/eviction — outcome never arrived, never written
	outcomeDropped atomic.Int64 // bounded semaphore full — write shed, matching decision-time semantics
	outcomeStashed atomic.Int64 // decisions parked awaiting an outcome
)

// stashPendingFeedback parks a decision-time feedback under its request id.
// Never blocks: when the registry is full the oldest entry is evicted early
// (dropped, counted — no outcome ever arrived for it). Re-stashing the same
// request id (client retry reusing X-Request-Id) evicts the previous entry
// the same way so a decision can never be silently lost.
func stashPendingFeedback(requestID string, optimizer RoutingOptimizer, fb *routingopt.RoutingFeedback) {
	startPendingJanitor()
	var evicted int // counted after the lock is released
	pendingFeedbackMu.Lock()
	// Same id stashed twice: drop the previous entry (its request either
	// crashed or was superseded) and let the new decision own the id.
	if old := pendingFeedback[requestID]; old != nil {
		removePendingLocked(requestID)
		evicted++
	}
	// Capacity guard: evict the oldest entries before inserting.
	for len(pendingFeedback) >= maxPendingFeedback && pendingFeedbackOrder.Len() > 0 {
		front := pendingFeedbackOrder.Front()
		removePendingLocked(front.Value.(string))
		evicted++
	}
	entry := &pendingFeedbackEntry{fb: fb, optimizer: optimizer, stashedAt: time.Now()}
	entry.elem = pendingFeedbackOrder.PushBack(requestID)
	pendingFeedback[requestID] = entry
	pendingFeedbackMu.Unlock()
	outcomeExpired.Add(int64(evicted))
	outcomeStashed.Add(1)
}

// removePendingLocked deletes a request id from both the map and the
// insertion-order index, both O(1). Caller holds pendingFeedbackMu.
func removePendingLocked(requestID string) {
	if e := pendingFeedback[requestID]; e != nil && e.elem != nil {
		pendingFeedbackOrder.Remove(e.elem)
	}
	delete(pendingFeedback, requestID)
}

// takePendingFeedback pops the stashed feedback for a request id, or nil.
func takePendingFeedback(requestID string) *pendingFeedbackEntry {
	pendingFeedbackMu.Lock()
	defer pendingFeedbackMu.Unlock()
	entry := pendingFeedback[requestID]
	if entry == nil {
		return nil
	}
	removePendingLocked(requestID)
	return entry
}

// evictExpiredPendingFeedback drops entries whose request never reported an
// outcome within pendingFeedbackTTL. Called by the lazy janitor (and directly
// by tests). Expired decisions are counted, never written: without a real
// outcome there is nothing to learn, and a placeholder IsSuccess=true row
// would inject false positives into the optimizer (see header note 3).
func evictExpiredPendingFeedback() {
	cutoff := time.Now().Add(-pendingFeedbackTTL)
	var expired int
	pendingFeedbackMu.Lock()
	// pendingFeedbackOrder is insertion-ordered, so everything to drop sits
	// at the head; stop at the first fresh entry.
	for pendingFeedbackOrder.Len() > 0 {
		frontID := pendingFeedbackOrder.Front().Value.(string)
		e := pendingFeedback[frontID]
		if e == nil || !e.stashedAt.Before(cutoff) {
			break
		}
		removePendingLocked(frontID)
		expired++
	}
	pendingFeedbackMu.Unlock()
	if expired > 0 {
		outcomeExpired.Add(int64(expired))
	}
}

// startPendingJanitor lazily launches the TTL sweep. Runs forever once started
// (mirrors the routingopt batch writer); a stopped janitor would only degrade
// into the bounded-registry eviction path, never into unbounded growth.
func startPendingJanitor() {
	pendingJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				// Recover per tick (2026-09-09 audit round 3): a recover on
				// the goroutine body would let one panic permanently kill the
				// sweep; per-tick recovery keeps the janitor alive.
				func() {
					defer func() {
						if rec := recover(); rec != nil {
							slog.Error("autoroute outcome janitor tick panicked", "panic", rec)
						}
					}()
					evictExpiredPendingFeedback()
				}()
				if stashed, matched, orphan, expired, dropped := RoutingOutcomeStats(); stashed > 0 {
					slog.Debug("autoroute outcome registry stats",
						"stashed", stashed, "matched", matched,
						"orphan", orphan, "expired", expired, "dropped", dropped)
				}
			}
		}()
	})
}

// ReportRoutingOutcome backfills a stashed decision-time feedback with the
// request's real terminal outcome and ships it to the plugin. Safe to call
// for any request (auto or not): ids without a stashed decision are counted
// and ignored. Never blocks — matching a pending entry is O(1) and the write
// goes through the same bounded drop-on-full dispatch as before.
//
// Called by domains/streaming at the terminal request_logs choke points
// (success telemetry + EmitFailure), NOT at decision time.
func ReportRoutingOutcome(outcome RoutingOutcome) {
	if outcome.RequestID == "" {
		return
	}
	// R37 (R35-R2 灰度口径): gateway-synthetic rounds never enter the outcome
	// counters — a title/summary loopback terminal has no stash (its decision
	// was gated at recordFeedbackAsync), so letting it through would inflate
	// orphan with self-call noise; a goal-% shadow round would inflate
	// matched. Skipped BEFORE any counter so the synthetic flow is invisible
	// to the registry (it still flows through request_logs for auditability).
	if IsSyntheticActor(outcome.OriginActor) {
		return
	}
	entry := takePendingFeedback(outcome.RequestID)
	if entry == nil {
		outcomeOrphan.Add(1)
		return
	}
	entry.fb.IsSuccess = outcome.Success
	entry.fb.Latency = time.Duration(outcome.LatencyMs) * time.Millisecond
	entry.fb.Cost = outcome.CostUSD
	outcomeMatched.Add(1)
	dispatchFeedback(entry.optimizer, entry.fb)
}

// dispatchFeedback ships one feedback write on the bounded semaphore with
// drop-on-full semantics and a panic-recovering goroutine. Extracted verbatim
// from the old recordFeedbackAsync body so decision-time (legacy synthetic-id)
// writes and outcome-backfilled writes share one bounded path.
func dispatchFeedback(opt RoutingOptimizer, fb *routingopt.RoutingFeedback) {
	if opt == nil || fb == nil {
		return
	}
	select {
	case feedbackWriteSlots <- struct{}{}:
	default:
		// Bound reached: drop instead of queueing — a burst of decisions
		// must not convert feedback into pool pressure.
		outcomeDropped.Add(1)
		slog.DebugContext(context.Background(),
			"optimizer.RecordFeedback dropped, write bound reached",
			"task_type", fb.TaskType)
		return
	}
	go func() {
		// 2026-09-08 audit: panic in a plugin chain would kill the process —
		// this is a fire-and-forget write, a recovered panic only loses one
		// feedback row (best-effort by contract).
		defer func() {
			if rec := recover(); rec != nil {
				slog.WarnContext(context.Background(),
					"optimizer.RecordFeedback panicked", "panic", rec)
			}
			<-feedbackWriteSlots
		}()
		ctx, cancel := context.WithTimeout(context.Background(), feedbackWriteTimeout)
		defer cancel()
		if err := opt.RecordFeedback(ctx, *fb); err != nil {
			slog.WarnContext(ctx, "optimizer.RecordFeedback failed", "err", err)
		}
	}()
}

// RoutingOutcomeStats returns the registry counters for tests and
// observability: stashed, matched, orphan, expired, dropped.
func RoutingOutcomeStats() (stashed, matched, orphan, expired, dropped int64) {
	return outcomeStashed.Load(), outcomeMatched.Load(), outcomeOrphan.Load(),
		outcomeExpired.Load(), outcomeDropped.Load()
}
