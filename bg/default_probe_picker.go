package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultProbePicker re-evaluates the default_probe_model for every active
// credential at 00:00 Beijing time (16:00 UTC) and on demand.
//
// Spec: docs/superpowers/specs/2026-06-12-credential-availability-audit-design.md §4
type DefaultProbePicker struct {
	db       *pgxpool.Pool
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewDefaultProbePicker(db *pgxpool.Pool) *DefaultProbePicker {
	return &DefaultProbePicker{
		db:       db,
		interval: 1 * time.Hour,
		done:     make(chan struct{}),
	}
}

func (p *DefaultProbePicker) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	go p.run(ctx)
	slog.Info("default probe picker started", "interval", p.interval)
}

func (p *DefaultProbePicker) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	<-p.done
}

func (p *DefaultProbePicker) run(ctx context.Context) {
	defer close(p.done)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	// Run immediately on start so service restart triggers repick
	p.repickAll(ctx)

	lastRunDate := time.Now().UTC().Format("2006-01-02")
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// Beijing time (UTC+8) crosses midnight at 16:00 UTC
			beijingDate := now.UTC().Add(16 * time.Hour).Format("2006-01-02")
			if beijingDate != lastRunDate && now.UTC().Hour() == 16 {
				p.repickAll(ctx)
				lastRunDate = beijingDate
			}
		}
	}
}

// repickAll iterates active credentials and updates default_probe_model
// using the 4-level priority (manual > request_logs > domestic_random > skip).
func (p *DefaultProbePicker) repickAll(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	rows, err := p.db.Query(timeoutCtx, `
		SELECT id FROM credentials
		WHERE status = 'active'
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND (default_probe_model_source IS NULL OR default_probe_model_source <> 'manual')
		ORDER BY id
	`)
	if err != nil {
		slog.Warn("default probe picker: query failed", "error", err)
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

	picked := 0
	skipped := 0
	for _, id := range ids {
		result, err := pickProbeModelForCredentialBg(timeoutCtx, p.db, id)
		if err != nil {
			slog.Warn("default probe picker: pick failed", "credential_id", id, "error", err)
			skipped++
			continue
		}
		if result.Model == "" {
			skipped++
			continue
		}
		// Persist
		_, err = p.db.Exec(timeoutCtx, `
			UPDATE credentials
			SET default_probe_model = $1, default_probe_model_source = $2, default_probe_model_picked_at = NOW()
			WHERE id = $3
		`, result.Model, result.Source, id)
		if err != nil {
			slog.Warn("default probe picker: persist failed", "credential_id", id, "error", err)
			skipped++
			continue
		}
		// Audit log
		//nolint:errcheck // best-effort exec, non-critical
		p.db.Exec(timeoutCtx, `
			INSERT INTO credential_probe_model_log
			    (tenant_id, credential_id, source, old_model, new_model, actor, reason)
			VALUES ('default', $1, $2, NULL, $3, 'system', 'daily 0:00 repick')
		`, id, result.Source, result.Model)
		picked++
	}

	slog.Info("default probe picker: cycle complete",
		"total", len(ids),
		"picked", picked,
		"skipped", skipped,
	)
}

// pickProbeModelForCredentialBg is the bg-worker version of the picker
// (the admin handler has a wrapper that includes actor tracking).
// Implementation lives in shared_pick.go to avoid duplication.
func pickProbeModelForCredentialBg(ctx context.Context, db *pgxpool.Pool, credID int) (PickProbeResult, error) {
	return PickProbeModelForCredential(ctx, db, credID)
}
