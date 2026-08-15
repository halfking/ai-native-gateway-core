// Package bg — Cost Reconciliation Worker (M3 CO-3, 2026-08-15)
//
// 激活 provider_cost_reconciliation（供应商成本对账，doc 19 §3 CO-3）：
//
//	request_logs_hot + request_logs --按 provider+月聚合--> 对账表
//	管理端导入供应商账单 ------------------------------> 对账表（provider_* 列）
//	差异率超阈值 -------------------------------------> provider_events 告警事件
//
// 集成方式与 ProfileAggregator 相同：initProviderProfile（cmd/gateway/
// provider_profile_init.go）在 provider_profile.enabled 与
// provider_profile.cost_reconciliation.enabled 同时开启时启动本 worker，
// 默认关闭（行为与 main 完全一致）。
package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
)

// CostReconciliationWorker 周期性聚合网关侧用量到对账表。
type CostReconciliationWorker struct {
	reconciler *providerprofile.CostReconciler
	interval   time.Duration
	cancel     context.CancelFunc
	done       chan struct{}
}

// NewCostReconciliationWorker 创建 worker。thresholds 为差异告警阈值。
func NewCostReconciliationWorker(db *pgxpool.Pool, interval time.Duration, thresholds providerprofile.DiffThresholds) *CostReconciliationWorker {
	source := providerprofile.NewPGGatewayMonthlyUsageSource(db)
	store := providerprofile.NewPGReconciliationStore(db)
	sink := providerprofile.NewPGReconciliationEventSink(db)
	return NewCostReconciliationWorkerFromReconciler(
		providerprofile.NewCostReconciler(source, store, sink, thresholds), interval)
}

// NewCostReconciliationWorkerFromReconciler 用已构造的 reconciler 创建
// worker（cmd/gateway 里 worker 与管理端账单导入共用同一实例）。
func NewCostReconciliationWorkerFromReconciler(reconciler *providerprofile.CostReconciler, interval time.Duration) *CostReconciliationWorker {
	return &CostReconciliationWorker{
		reconciler: reconciler,
		interval:   interval,
		done:       make(chan struct{}),
	}
}

// Start begins the reconciliation loop. 启动时立即跑一次，使开启开关当天
// 就能查到当前月对账数据（与 ProfileAggregator 的首跑语义一致）。
func (w *CostReconciliationWorker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go w.run(ctx)
	slog.Info("provider cost reconciliation worker started", "interval", w.interval)
}

// Stop gracefully stops the worker.
func (w *CostReconciliationWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
		slog.Info("provider cost reconciliation worker stopped")
	}
}

func (w *CostReconciliationWorker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.reconcile(ctx)
		}
	}
}

func (w *CostReconciliationWorker) reconcile(ctx context.Context) {
	for _, month := range CostReconciliationMonthsToAggregate(time.Now()) {
		reconCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		if err := w.reconciler.AggregateMonth(reconCtx, month); err != nil {
			slog.Error("provider cost reconciliation failed",
				"error", err, "month", month.Format("2006-01"))
		} else {
			slog.Info("provider cost reconciliation completed", "month", month.Format("2006-01"))
		}
		cancel()
	}
}

// CostReconciliationMonthsToAggregate 返回给定时刻应聚合的月份首日列表：
// 当前月（数据持续累计，需保持最新）+ 上一月（月初后补齐最终聚合）。
func CostReconciliationMonthsToAggregate(now time.Time) []time.Time {
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	previous := current.AddDate(0, -1, 0)
	return []time.Time{current, previous}
}
