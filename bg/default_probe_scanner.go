package bg

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/modelcatalog"
)

// DefaultProbeScanner periodically fills credentials.default_probe_model
// for credentials where the operator never set one and the daily
// DefaultProbePicker hasn't run yet.
//
// Why this exists (2026-08-31 hzx-2 round-5):
//
//   - DefaultProbePicker runs once on startup + once per Beijing-day at
//     16:00 UTC. A credential that misses both (e.g. onboarding happens
//     23:59 Beijing time, gateway restarts 00:01, next daily tick is
//     ~24h away) sits without a probe target for the entire window.
//     PeriodicQuotaProbe / BalanceQuotaProbe / ProbeNow all silently
//     skip such credentials — visible to operators as a credential
//     that's "active" but never probes.
//
//   - discovery.Service.discoverForCredential and
//     admin.Handler.discoverAndUpsertForCredential each auto-fill
//     default_probe_model right after a model-list upsert. But these
//     paths only fire when discovery / a manual refresh runs; for a
//     brand-new credential whose first /v1/models fetch fails the
//     path never executes, so the auto-fill never happens.
//
// This worker closes both gaps. It scans every LLM_GATEWAY_DEFAULT_PROBE_SCAN_INTERVAL
// (default 5 min) and re-issues modelcatalog.AutoFillDefaultProbeModel
// for any credential that:
//   - has no default_probe_model (NULL/empty), AND
//   - has default_probe_model_source <> 'manual', AND
//   - is otherwise routable (active lifecycle, not manual_disabled,
//     provider enabled, has at least one cmb row).
//
// Best-effort: AutoFillDefaultProbeModel already swallows pgx.ErrNoRows
// (the "no eligible binding" outcome is the common case for a
// just-created credential whose cmb is still empty), and the scan
// itself logs DB errors without aborting the loop.
type DefaultProbeScanner struct {
	db       *pgxpool.Pool
	interval time.Duration

	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
	cancel      context.CancelFunc
	done        chan struct{}
}

// NewDefaultProbeScanner constructs the periodic scanner. The interval
// is LLM_GATEWAY_DEFAULT_PROBE_SCAN_INTERVAL (default 5 min) — short
// enough that a freshly-onboarded credential is reachable within the
// operator's first coffee, long enough that DB load is negligible.
func NewDefaultProbeScanner(db *pgxpool.Pool) *DefaultProbeScanner {
	interval := 5 * time.Minute
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_DEFAULT_PROBE_SCAN_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Minute
		} else {
			slog.Warn("default probe scanner: invalid LLM_GATEWAY_DEFAULT_PROBE_SCAN_INTERVAL, using 5m",
				"value", v)
		}
	}
	return newDefaultProbeScannerWithInterval(db, interval)
}

// newDefaultProbeScannerWithInterval is the constructor the tests use
// to inject a short interval. Production callers always go through
// NewDefaultProbeScanner so the env override path stays the only
// public way to change the cadence.
func newDefaultProbeScannerWithInterval(db *pgxpool.Pool, interval time.Duration) *DefaultProbeScanner {
	return &DefaultProbeScanner{
		db:       db,
		interval: interval,
		done:     make(chan struct{}),
	}
}

// Start launches the periodic scan loop. Safe to call once; subsequent
// calls are no-ops (returns silently without restarting the loop).
// The loop exits when ctx is cancelled OR Stop() is invoked.
func (s *DefaultProbeScanner) Start(ctx context.Context) {
	s.lifecycleMu.Lock()
	if s.started {
		s.lifecycleMu.Unlock()
		return
	}
	s.started = true
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.lifecycleMu.Unlock()
	go s.run(runCtx)
	slog.Info("default probe scanner started", "interval", s.interval)
}

// Stop halts the periodic scan loop and blocks until the goroutine has
// exited. Safe to call before Start, after Stop, or concurrently with
// the run loop; the sync.Once guarantees the underlying channel close
// fires at most once.
func (s *DefaultProbeScanner) Stop() {
	s.lifecycleMu.Lock()
	if !s.started || s.stopped {
		s.lifecycleMu.Unlock()
		return
	}
	s.stopped = true
	cancel := s.cancel
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-s.done
}

func (s *DefaultProbeScanner) run(ctx context.Context) {
	defer close(s.done)

	// Run immediately on start so a fresh boot doesn't wait the first
	// interval. Mirrors DefaultProbePicker.run so the two workers have
	// the same observable behaviour on startup.
	s.scanOnce(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("default probe scanner stopping")
			return
		case <-ticker.C:
			s.scanOnce(ctx)
		}
	}
}

// scanOnce runs one scan pass. It selects candidate credentials with
// the eligibility SQL below and hands each to
// modelcatalog.AutoFillDefaultProbeModel, which itself enforces the
// "skip when default_probe_model already non-empty" guard. The
// candidate SQL is the cheap eligibility filter — the helper is the
// authoritative writer that also handles the NULL/empty /
// source='manual' cases atomically.
func (s *DefaultProbeScanner) scanOnce(ctx context.Context) {
	if s.db == nil {
		return
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rows, err := s.db.Query(timeoutCtx, `
		SELECT c.id
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  AND (c.default_probe_model IS NULL OR c.default_probe_model = '')
		  AND COALESCE(c.default_probe_model_source, '') <> 'manual'
		  AND EXISTS (
		      SELECT 1
		      FROM credential_model_bindings cmb
		      JOIN provider_models pm ON pm.id = cmb.provider_model_id
		      WHERE cmb.credential_id = c.id
		        AND COALESCE(cmb.available, FALSE) = TRUE
		        AND COALESCE(pm.available, FALSE) = TRUE
		  )
		LIMIT 100
	`)
	if err != nil {
		slog.Warn("default probe scanner: candidate query failed", "error", err)
		return
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		slog.Warn("default probe scanner: candidate scan failed", "error", err)
	}
	if len(ids) == 0 {
		return
	}

	filled := 0
	for _, id := range ids {
		picked, err := modelcatalog.AutoFillDefaultProbeModel(timeoutCtx, s.db, id)
		if err != nil {
			slog.Warn("default probe scanner: auto-fill failed",
				"credential_id", id, "error", err)
			continue
		}
		if picked != "" {
			filled++
			slog.Info("default probe scanner: filled default_probe_model",
				"credential_id", id,
				"default_probe_model", picked,
				"source", modelcatalog.DefaultProbeModelSourceRefreshLatest,
			)
		}
	}
	if filled > 0 || len(ids) > 0 {
		slog.Info("default probe scanner: scan complete",
			"candidates", len(ids),
			"filled", filled,
			"interval", s.interval,
		)
	}
}
