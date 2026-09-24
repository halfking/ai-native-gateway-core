package bg

// report_rollup_worker.go —— 对账报表每日聚合 worker（2026-09-25 对账报表
// 落地轮，设计 docs/reconciliation/design-report-rollup.md §3）。
//
// 调度语义（对齐 feedback_analyzer.go 的每日钟点模式）：
//   - 启动即补跑「昨日」一次（ON CONFLICT 幂等，迟到数据可回填修正）；
//   - 之后每天在 RunHour（UTC 钟点，settings 键 reports.daily_rollup.hour，
//     默认 2 = 凌晨 02:00）滚动触发，聚合「昨天」的 usage_facts；
//   - RunHour 每轮重读（HotReload），改设置无需重启；
//   - 单轮 panic recover 守护 + 超时上限，绝不上抛阻塞请求面。
//
// 聚合本体在 domains/reportrollup.RollupDay（六 scope 快照，见该包注释）。

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// SettingKeyDailyRollupHour 每日聚合钟点的平台设置键。
const SettingKeyDailyRollupHour = "reports.daily_rollup.hour"

// defaultRollupHour 与设置默认值一致（凌晨 02:00 UTC）。
const defaultRollupHour = 2

// rollupRunTimeout 单轮聚合超时；usage_facts 单日分区扫描量级下富余。
const rollupRunTimeout = 30 * time.Minute

// ReportRollupWorker 每日对账报表聚合器。
type ReportRollupWorker struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	// RunHourOverride 测试/管理端注入的固定钟点；nil 时从 settings 读。
	RunHourOverride *int
}

// NewReportRollupWorker 构造 worker。
func NewReportRollupWorker(db *pgxpool.Pool) *ReportRollupWorker {
	return &ReportRollupWorker{db: db, done: make(chan struct{})}
}

// Start 启动调度循环：先同步补跑昨日，再进入钟点等待。
func (w *ReportRollupWorker) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(cctx)
	slog.Info("report rollup worker started",
		"schedule", "daily (hour from settings reports.daily_rollup.hour, default 02:00 UTC)")
}

// Stop 停止并等待调度循环退出。
func (w *ReportRollupWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *ReportRollupWorker) run(ctx context.Context) {
	defer close(w.done)

	// 启动补跑昨日：进程重启/停机跨过钟点时靠这里补齐（幂等可重入）。
	w.runRecovered(ctx, "startup-backfill")

	for {
		hour := w.currentHour()
		now := time.Now().UTC()
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		slog.Debug("report rollup worker next run scheduled", "at", next)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			w.runRecovered(ctx, "scheduled")
		}
	}
}

// runRecovered 守护单轮聚合：panic 记日志后调度循环继续（R51 教训：
// 无 recover 的调度 goroutine 一次 panic 即整进程崩溃）。
func (w *ReportRollupWorker) runRecovered(ctx context.Context, trigger string) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("report rollup worker panic recovered", "trigger", trigger, "recover", rec)
		}
	}()
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	if _, err := w.RollupDate(ctx, yesterday); err != nil {
		slog.Warn("report rollup worker run failed", "trigger", trigger, "error", err)
	}
}

// RollupDate 聚合指定 UTC 日（供管理端手动触发与测试）。ctx 已由调用方
// 控制生命周期，这里再包一层超时防长锁。
func (w *ReportRollupWorker) RollupDate(ctx context.Context, day time.Time) (reportrollup.RollupStats, error) {
	runCtx, cancel := context.WithTimeout(ctx, rollupRunTimeout)
	defer cancel()
	stats, err := reportrollup.RollupDay(runCtx, w.db, day)
	if err != nil {
		return stats, err
	}
	slog.Info("report rollup completed",
		"date", day.Format("2006-01-02"),
		"rows_written", stats.RowsWritten,
		"requests_seen", stats.RequestsSeen,
	)
	return stats, nil
}

// currentHour 解析当前生效钟点：Override 优先，否则 settings（缺省 2，
// 非法值回落默认）。
func (w *ReportRollupWorker) currentHour() int {
	if w.RunHourOverride != nil {
		h := *w.RunHourOverride
		if h >= 0 && h <= 23 {
			return h
		}
		return defaultRollupHour
	}
	h := settings.GetPlatformInt(SettingKeyDailyRollupHour, defaultRollupHour)
	if h < 0 || h > 23 {
		return defaultRollupHour
	}
	return h
}
