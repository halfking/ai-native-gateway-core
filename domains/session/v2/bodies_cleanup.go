package v2

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// BodiesCleanupWorker hourly-erases expired session bodies and trashed rows,
// then trims session_bodies_hot under the soft MiB cap (oldest first).
// Retention is deliberately decoupled from request_logs lifecycle (W1.4).
type BodiesCleanupWorker struct {
	db       *pgxpool.Pool
	interval time.Duration
	stopCh   chan struct{}
}

// NewBodiesCleanupWorker builds a worker. interval ≤0 defaults to 1h.
func NewBodiesCleanupWorker(db *pgxpool.Pool, interval time.Duration) *BodiesCleanupWorker {
	if interval <= 0 {
		interval = time.Hour
	}
	return &BodiesCleanupWorker{db: db, interval: interval, stopCh: make(chan struct{})}
}

// Start launches the hourly loop. Safe to call once.
func (w *BodiesCleanupWorker) Start() {
	if w == nil || w.db == nil {
		return
	}
	go w.loop()
}

// Stop signals the loop to exit.
func (w *BodiesCleanupWorker) Stop() {
	if w == nil {
		return
	}
	select {
	case <-w.stopCh:
	default:
		close(w.stopCh)
	}
}

func (w *BodiesCleanupWorker) loop() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.tick(context.Background())
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.tick(context.Background())
		}
	}
}

func (w *BodiesCleanupWorker) tick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	hours := 168
	maxMiB := 256
	if settings.Global != nil {
		hours = settings.RecordingBodyRetentionHours("")
		if hours <= 0 {
			hours = 168
		}
		maxMiB = settings.RecordingBodyStoreMaxMiB("")
		if maxMiB <= 0 {
			maxMiB = 256
		}
	}
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	for _, sql := range []string{
		`DELETE FROM public.session_bodies_hot WHERE ts < $1`,
		`DELETE FROM public.session_bodies_trash WHERE trashed_at < $1`,
	} {
		tag, err := w.db.Exec(ctx, sql, cutoff)
		if err != nil {
			n := 40
			if len(sql) < n {
				n = len(sql)
			}
			slog.Warn("session bodies cleanup failed", "sql", sql[:n], "error", err)
			continue
		}
		if tag.RowsAffected() > 0 {
			slog.Info("session bodies cleanup", "deleted", tag.RowsAffected(), "cutoff", cutoff)
		}
	}
	w.trimHotCap(ctx, maxMiB)
}

// trimHotCap deletes oldest session_bodies_hot rows until estimated size is
// under maxMiB. Soft cap — best-effort; never blocks writers.
func (w *BodiesCleanupWorker) trimHotCap(ctx context.Context, maxMiB int) {
	if maxMiB <= 0 {
		return
	}
	capBytes := int64(maxMiB) << 20
	var total int64
	err := w.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(
			pg_column_size(request_delta) + pg_column_size(response_delta)
			+ pg_column_size(outbound_body)
		), 0)
		FROM public.session_bodies_hot
	`).Scan(&total)
	if err != nil {
		slog.Warn("session bodies cap probe failed", "error", err)
		return
	}
	if total <= capBytes {
		return
	}
	// Delete oldest batches until under cap (or 50 batches max per tick).
	for i := 0; i < 50 && total > capBytes; i++ {
		tag, err := w.db.Exec(ctx, `
			DELETE FROM public.session_bodies_hot
			WHERE ctid IN (
				SELECT ctid FROM public.session_bodies_hot
				ORDER BY ts ASC
				LIMIT 200
			)
		`)
		if err != nil {
			slog.Warn("session bodies cap trim failed", "error", err)
			return
		}
		if tag.RowsAffected() == 0 {
			return
		}
		err = w.db.QueryRow(ctx, `
			SELECT COALESCE(SUM(
				pg_column_size(request_delta) + pg_column_size(response_delta)
				+ pg_column_size(outbound_body)
			), 0)
			FROM public.session_bodies_hot
		`).Scan(&total)
		if err != nil {
			return
		}
		slog.Info("session bodies cap trim", "deleted", tag.RowsAffected(), "bytes_left", total, "cap", capBytes)
	}
}
