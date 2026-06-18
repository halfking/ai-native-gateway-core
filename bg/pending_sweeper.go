package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/pending"
)

// PendingSweeper (Track C C6, 2026-06-18) is a periodic background
// worker that marks abandoned in_progress pending entries as
// failed. An entry is "abandoned" when:
//   - the async retry goroutine (routing.runAsyncRetry) crashed
//     without writing a terminal status, OR
//   - the client disconnected and never polled, OR
//   - the Redis write succeeded but the goroutine was lost to a
//     pod restart.
//
// In all three cases, the entry would otherwise sit in_progress
// for the full 7-day TTL, blocking the GET endpoint from
// returning a terminal response and the session from being
// considered clean.
//
// Lifecycle (mirrors bg/credential_recovery.go):
//   - Start() spawns a goroutine that runs every 60s
//   - Stop() is cooperative via context cancel; blocks on done
//   - Each tick scans pending_response:* via SCAN (see
//     pending.Store.ListStaleInProgress)
//   - Stale rows (in_progress AND created_at < now - StaleTimeout)
//     are marked failed with error_message="stale_timeout".
//
// The sweeper is best-effort. A real-time Redis outage mid-tick
// is logged at warn level; the next tick retries automatically.
type PendingSweeper struct {
	store        *pending.Store
	staleTimeout time.Duration // default 10 minutes
	interval     time.Duration // default 60 seconds
	cancel       context.CancelFunc
	done         chan struct{}
}

// NewPendingSweeper builds a sweeper. The store may be nil; in
// that case Start() is still safe (the sweeper just logs and
// skips every tick — useful for tests that want a no-op).
func NewPendingSweeper(store *pending.Store, staleTimeout, interval time.Duration) *PendingSweeper {
	if staleTimeout <= 0 {
		staleTimeout = 10 * time.Minute
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &PendingSweeper{
		store:        store,
		staleTimeout: staleTimeout,
		interval:     interval,
		done:         make(chan struct{}),
	}
}

func (s *PendingSweeper) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.run(ctx)
	slog.Info("pending_sweeper started",
		"interval", s.interval,
		"stale_timeout", s.staleTimeout,
	)
}

func (s *PendingSweeper) Stop() {
	if s.cancel != nil {
		s.cancel()
	} else {
		// Start() was never called — the done channel was
		// never closed by run(). Return immediately rather
		// than deadlocking on <-s.done.
		return
	}
	<-s.done
}

func (s *PendingSweeper) run(ctx context.Context) {
	defer close(s.done)

	// Per-tick budget. ListStaleInProgress uses SCAN + HGETALL
	// for each candidate. 30s is a generous upper bound for
	// realistic key-space sizes; if exceeded, the next tick
	// continues from where this one left off.
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

// sweep is one tick. The return value is unused; we log success
// / failure counts for operator visibility.
func (s *PendingSweeper) sweep(ctx context.Context) {
	if s.store == nil {
		// No-op tick; intentionally not logged every minute to
		// avoid log spam. The startup log already documents the
		// configuration.
		return
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	staleBefore := time.Now().Add(-s.staleTimeout)
	entries, err := s.store.ListStaleInProgress(timeoutCtx, staleBefore, 100)
	if err != nil {
		slog.Warn("pending_sweeper: list failed", "error", err)
		return
	}
	if len(entries) == 0 {
		// Quiet tick — nothing to do. Do not log so a healthy
		// sweeper does not flood the logs.
		return
	}

	marked := 0
	for _, e := range entries {
		// Reload the full entry, mark it failed, write back. The
		// reload-then-write is two round-trips; a future
		// optimisation is a Lua script that does it atomically
		// (see docs/.../2026-06-18-llm-gateway-stability-design.md
		// §11.4 Phase 2B for the upgrade path).
		writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
		entry, found, gerr := s.store.Get(writeCtx, e.SessionID, e.RequestID)
		writeCancel()
		if gerr != nil || !found {
			// Race: someone else (admin API, C3 GET, another
			// sweeper instance) already wrote a terminal status.
			// Not a failure; skip.
			continue
		}
		if entry.Status != pending.StatusInProgress {
			// Status changed between ListStaleInProgress and
			// Get. Probably the async goroutine finished. Skip.
			continue
		}
		// Audit fix 6.1: double-check status right before Save
		// to prevent overwriting a completed/failed entry that
		// the async goroutine wrote between our Get and Save.
		// The race window is small (~5ms) but real; without this
		// check, a legitimate successful response could be
		// silently overwritten as "failed: stale_timeout".
		recheckCtx, recheckCancel := context.WithTimeout(ctx, 3*time.Second)
		rechecked, refound, _ := s.store.Get(recheckCtx, e.SessionID, e.RequestID)
		recheckCancel()
		if refound && rechecked != nil && rechecked.Status != pending.StatusInProgress {
			continue
		}
		entry.Status = pending.StatusFailed
		entry.ErrorMessage = "stale_timeout"
		entry.CompletedAt = time.Now().Unix()
		saveCtx, saveCancel := context.WithTimeout(ctx, 5*time.Second)
		// Use Save for the terminal write. Save is a generic
		// write-status helper — it accepts any Response regardless
		// of previous status, sets CompletedAt = now, and writes
		// the body (which is empty here). The 1MB body cap is
		// not a concern because we do not modify Body.
		serr := s.store.Save(saveCtx, entry)
		saveCancel()
		if serr != nil {
			slog.Warn("pending_sweeper: save failed",
				"session_id", e.SessionID,
				"request_id", e.RequestID,
				"error", serr,
			)
			continue
		}
		marked++
	}
	if marked > 0 {
		slog.Info("pending_sweeper: marked stale entries",
			"scanned", len(entries),
			"marked", marked,
			"stale_timeout", s.staleTimeout,
		)
	}
}
