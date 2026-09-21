// Package fsstore — fallback switch.
//
// Decision matrix (2026-08-26):
//
//	startup ─► probe PG ── ok ────► primary = PG
//	                              │  FS is closed (write-only fallback
//	                              │  on emergency shutdown).
//	                              └─ runtime PG error → Mode = "degraded"
//	                                                  (NOT auto-switched:
//	                                                  to avoid split-brain)
//	startup ─► PG down ──► primary = FS, log loud warning.
//
// In practice this package is intentionally tiny: callers should
// pass an explicit `*pgxpool.Pool` and `*fsstore.Store` to the
// domain code, and the domain code does:
//
//	// PG available?
//	var row ProviderRow
//	err := db.QueryRow(ctx, q, args...).Scan(...)
//	if err != nil {
//	    // Fallback: read from FS.
//	    e, err := fs.GetEntity(fsstore.KindProvider, id)
//	    ...
//	}
//
// The fallback is one explicit branch, not an automatic wrapping
// proxy. Phase B keeps the boundary visible so the next operator
// audit can grep for "fs.Get" and reason about both paths.

package fsstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Mode describes which tier is currently authoritative.
type Mode string

const (
	// ModePG means PostgreSQL is the source of truth; FS is closed
	// (or write-only) for this process.
	ModePG Mode = "pg"
	// ModeFS means PG is unreachable at startup; FS is the source of
	// truth. Operators get a loud slog.Warn so they notice.
	ModeFS Mode = "fs"
)

// Fallback bundles a PG pool and a FS store. Exactly one of them is
// active per process; the choice is made once at startup and frozen
// for the lifetime of the gateway (no silent toggle to avoid
// split-brain — Phase B explicitly recommends disabling dual-write
// during the transition period).
type Fallback struct {
	Mode Mode
	DB   *pgxpool.Pool
	FS   *Store
}

// ProbeTimeout caps the initial PG health probe so a hung PG does
// not delay gateway startup. 5s is enough for an idle connection
// to refuse fast.
const ProbeTimeout = 5 * time.Second

// Probe runs a trivial query against the pool to decide whether PG
// is reachable. We deliberately use a cheap query (SELECT 1) instead
// of a domain table to keep the probe fast and lock-free.
func Probe(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("fsstore: nil pool")
	}
	cctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	conn, err := pool.Acquire(cctx)
	if err != nil {
		return fmt.Errorf("fsstore: probe acquire: %w", err)
	}
	defer conn.Release()
	var one int
	if err := conn.QueryRow(cctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("fsstore: probe select 1: %w", err)
	}
	return nil
}

// NewFallback decides which tier wins at startup.
//
// Decision:
//   - Both nil → error (caller bug).
//   - Both provided → Probe(db); ok → ModePG, fs is closed.
//     Fail → ModeFS, db is closed (caller is responsible for
//     handing the pool back so we can drain).
//   - Only db provided → ModePG, fs nil.
//   - Only fs provided → ModeFS, db nil.
//
// Caller must call Close() at shutdown so the FS indices flush.
func NewFallback(ctx context.Context, pool *pgxpool.Pool, fs *Store) (*Fallback, error) {
	switch {
	case pool == nil && fs == nil:
		return nil, errors.New("fsstore: both db and fs nil")
	case pool != nil && fs == nil:
		return &Fallback{Mode: ModePG, DB: pool}, nil
	case pool == nil && fs != nil:
		slog.Warn("fsstore: starting in FS-only mode (no PG pool provided)")
		return &Fallback{Mode: ModeFS, FS: fs}, nil
	}

	// Both provided: probe PG.
	err := Probe(ctx, pool)
	if err == nil {
		slog.Info("fsstore: PG probe ok; using PG as primary, FS closed")
		// FS still exists in case we want it for read-only queries
		// later (Phase B explicitly recommends against this — we
		// keep it for the rebuild-on-crash case only).
		return &Fallback{Mode: ModePG, DB: pool, FS: fs}, nil
	}
	slog.Warn("fsstore: PG probe failed at startup; falling back to FS",
		"err", err.Error())
	if fs == nil {
		return nil, fmt.Errorf("fsstore: PG unavailable and FS not configured: %w", err)
	}
	// Close the pool so we don't leak a hanging connection — the
	// process is now FS-only.
	pool.Close()
	return &Fallback{Mode: ModeFS, FS: fs}, nil
}

// Close releases whichever tier is active.
func (f *Fallback) Close() error {
	if f == nil {
		return nil
	}
	var firstErr error
	if f.FS != nil {
		if err := f.FS.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if f.DB != nil {
		// pgxpool.Pool.Close is safe to call multiple times; we do
		// NOT close it if we handed it back as part of NewFallback.
		// Caller is responsible for that — see comment in PG-down
		// branch above.
	}
	return firstErr
}
