package bg

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PeriodicQuotaProbe struct {
	db             *pgxpool.Pool
	interval       time.Duration
	probeSubmitter func(credID int)
	stopCh         chan struct{}
	stopOnce       sync.Once
}

func NewPeriodicQuotaProbe(db *pgxpool.Pool) *PeriodicQuotaProbe {
	interval := 5 * time.Minute
	if v := os.Getenv("LLM_GATEWAY_PERIODIC_QUOTA_PROBE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Minute
		}
	}
	return &PeriodicQuotaProbe{
		db:       db,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

func (p *PeriodicQuotaProbe) SetProbeSubmitter(fn func(credID int)) {
	p.probeSubmitter = fn
}

func (p *PeriodicQuotaProbe) Start(ctx context.Context) {
	slog.Info("periodic_quota_probe started", "interval", p.interval)
	go func() {
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("periodic_quota_probe stopping")
				return
			case <-p.stopCh:
				slog.Info("periodic_quota_probe stopped")
				return
			case <-ticker.C:
				if err := p.probePeriodicExhausted(ctx); err != nil {
					slog.Error("periodic_quota_probe failed", "error", err)
				}
			}
		}
	}()
}

func (p *PeriodicQuotaProbe) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
}

func (p *PeriodicQuotaProbe) probePeriodicExhausted(ctx context.Context) error {
	if p.db == nil {
		return nil
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Periodic quota recovery is an upstream self-check, not a profile-quality
	// check. An automatically profile-disabled credential may therefore be
	// probed after its quota window expires; manual disables remain excluded.
	rows, err := p.db.Query(timeoutCtx, `
		SELECT c.id
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.quota_state = 'periodic_exhausted'
		  AND c.status = 'active'
		  AND (
		      c.lifecycle_status = 'active'
		      OR (
		          c.lifecycle_status = 'disabled'
		          AND c.auto_disabled_at IS NOT NULL
		      )
		  )
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  AND (c.quota_recover_at IS NULL OR c.quota_recover_at <= now())
		  AND COALESCE(c.default_probe_model, '') <> ''
		LIMIT 100
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var credID int
		if err := rows.Scan(&credID); err != nil {
			slog.Warn("periodic_quota_probe: scan failed", "error", err)
			continue
		}
		if p.probeSubmitter != nil {
			p.probeSubmitter(credID)
		}
		count++
	}
	if count > 0 {
		slog.Info("periodic_quota_probe: submitted probes for periodic_exhausted credentials",
			"count", count, "interval", p.interval)
	}
	return nil
}
