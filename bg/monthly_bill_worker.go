package bg

// monthly_bill_worker.go — package 07: postpaid monthly bill issuer.
// Pattern mirrors AuditTrimmer (sync.Once Stop + ticker). Uses existing
// worker mode (no taskbus). Multi-instance: claim_llm_gateway_task_slot
// when DB is attached; degrades to always-run when slot missing.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/maas"
)

const monthlyBillSlotName = "maas_monthly_bill_issue"

// MonthlyBillWorker periodically issues the previous calendar month.
type MonthlyBillWorker struct {
	svc      *maas.Service
	db       *pgxpool.Pool
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewMonthlyBillWorker constructs a daily issuer (interval default 24h).
func NewMonthlyBillWorker(svc *maas.Service, interval time.Duration) *MonthlyBillWorker {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &MonthlyBillWorker{
		svc:      svc,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// WithSharedThrottleDB enables cross-instance slot claiming (optional).
func (w *MonthlyBillWorker) WithSharedThrottleDB(db *pgxpool.Pool) *MonthlyBillWorker {
	w.db = db
	return w
}

// Start spawns the background goroutine.
func (w *MonthlyBillWorker) Start(ctx context.Context) {
	Go("monthly_bill_worker.run", func() { w.run(ctx) })
	slog.Info("monthly bill worker started", "interval", w.interval.String())
}

// Stop terminates the goroutine (idempotent).
func (w *MonthlyBillWorker) Stop() {
	if w.stop == nil || w.done == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stop) })
	select {
	case <-w.done:
	default:
	}
}

func (w *MonthlyBillWorker) run(ctx context.Context) {
	defer close(w.done)
	w.tick(ctx)
	tk := time.NewTicker(w.interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-tk.C:
			w.tick(ctx)
		}
	}
}

func (w *MonthlyBillWorker) tick(ctx context.Context) {
	if w.svc == nil || !w.svc.Enabled() {
		return
	}
	if w.db != nil {
		var claimed bool
		err := w.db.QueryRow(ctx,
			`SELECT public.claim_llm_gateway_task_slot($1, $2::interval)`,
			monthlyBillSlotName, w.interval.String(),
		).Scan(&claimed)
		if err == nil && !claimed {
			slog.Info("monthly bill worker skipped (peer holds shared throttle slot)",
				"task", monthlyBillSlotName)
			return
		}
		// missing function / table → degrade to run
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	issued, skipped, err := w.svc.IssuePreviousMonthForAllTenants(runCtx, time.Now().UTC())
	if err != nil {
		slog.Warn("monthly bill worker: issue failed", "error", err)
		return
	}
	slog.Info("monthly bill worker: issue done",
		"issued", issued, "skipped", skipped,
		"period", maas.PreviousPeriodYM(time.Now().UTC(), time.UTC))
}
