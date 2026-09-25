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
	"fmt"
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

// rollupCatchupLookbackDays 追赶回看窗口：每次触发后检查最近 N 天内
// 「daily_total 快照缺失」的日期并补跑（2026-09-25 审计轮：原实现单轮
// 失败/停机跨过钟点后，当日数据要等次日或人工触发才补）。历史早于窗口
// 的日期不做自动追赶，需要时走管理端手动 /run。
const rollupCatchupLookbackDays = 7

// ReportRollupWorker 每日对账报表聚合器。
type ReportRollupWorker struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	// RunHourOverride 测试/管理端注入的固定钟点；nil 时从 settings 读。
	RunHourOverride *int
	// MissingDatesOverride 测试注入的缺失日期探测函数；nil 时用
	// reportrollup.MissingRollupDates（q 固定为 worker 的 pool）。
	MissingDatesOverride func(ctx context.Context, lookbackDays int, now time.Time) ([]time.Time, error)
}

// NewReportRollupWorker 构造 worker。
func NewReportRollupWorker(db *pgxpool.Pool) *ReportRollupWorker {
	return &ReportRollupWorker{db: db, done: make(chan struct{})}
}

// Start 启动调度循环：先同步补跑昨日 + 追赶缺失日，再进入钟点等待。
func (w *ReportRollupWorker) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(cctx)
	slog.Info("report rollup worker started",
		"schedule", "daily (hour from settings reports.daily_rollup.hour, default 02:00 UTC)",
		"catchup_lookback_days", rollupCatchupLookbackDays)
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

	// 启动补跑昨日 + 追赶：进程重启/停机跨过钟点时靠这里补齐（幂等可重入）。
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
	// 截断到 UTC 午夜：MissingRollupDates 返回的就是午夜日期，skip 比对
	// 必须同刻度（审计轮实修：未截断的 yesterday 与探测日期永不相等，
	// 跳过逻辑失效导致昨日被重复聚合——幂等兜底正确性但白跑一趟）。
	now := time.Now().UTC()
	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	if _, err := w.RollupDate(ctx, yesterday); err != nil {
		slog.Warn("report rollup worker run failed", "trigger", trigger, "error", err)
	}
	w.catchUpMissed(ctx, trigger, yesterday)
}

// catchUpMissed 有界追赶：昨日已跑，再补齐回看窗口内其余缺失日。
// 逐日独立事务，单日失败不阻塞其余日期（下一轮会再探测到并重试）。
// 返回实际补跑（成功）的天数，供测试断言跳过语义。
func (w *ReportRollupWorker) catchUpMissed(ctx context.Context, trigger string, skip time.Time) int {
	probe := w.MissingDatesOverride
	if probe == nil {
		pool := w.db
		probe = func(ctx context.Context, lookbackDays int, now time.Time) ([]time.Time, error) {
			return reportrollup.MissingRollupDates(ctx, pool, lookbackDays, now)
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Minute)
	missing, err := probe(probeCtx, rollupCatchupLookbackDays, time.Now().UTC())
	cancel()
	if err != nil {
		slog.Warn("report rollup catchup probe failed", "trigger", trigger, "error", err)
		return 0
	}
	attempted := 0
	for _, d := range missing {
		if d.Equal(skip) {
			continue // runRecovered 刚聚合过的昨日不重复跑
		}
		attempted++
		if _, err := w.RollupDate(ctx, d); err != nil {
			attempted--
			slog.Warn("report rollup catchup day failed", "trigger", trigger,
				"date", d.Format("2006-01-02"), "error", err)
		}
	}
	if len(missing) > 0 || attempted > 0 {
		slog.Info("report rollup catchup completed", "trigger", trigger,
			"missing_probed", len(missing), "days_backfilled", attempted)
	}
	return attempted
}

// RollupDate 聚合指定 UTC 日（调度与测试入口）。单日聚合包在一个事务里
// （2026-09-25 审计轮：原实现逐行 upsert 无事务，中途失败留新旧混合快照），
// 失败整体回滚， MissingRollupDates 下一轮会重新探测到该日。
func (w *ReportRollupWorker) RollupDate(ctx context.Context, day time.Time) (reportrollup.RollupStats, error) {
	runCtx, cancel := context.WithTimeout(ctx, rollupRunTimeout)
	defer cancel()
	tx, err := w.db.Begin(runCtx)
	if err != nil {
		return reportrollup.RollupStats{}, fmt.Errorf("begin rollup tx: %w", err)
	}
	defer tx.Rollback(runCtx)
	stats, err := reportrollup.RollupDay(runCtx, tx, day)
	if err != nil {
		return stats, err
	}
	if err := tx.Commit(runCtx); err != nil {
		return stats, fmt.Errorf("commit rollup tx: %w", err)
	}
	slog.Info("report rollup completed",
		"date", day.Format("2006-01-02"),
		"rows_written", stats.RowsWritten,
		"requests_seen", stats.RequestsSeen,
	)
	return stats, nil
}

// RollupDateDetached 与请求生命周期解耦的单日聚合（管理端手动 /run 用）：
// 客户端断开不中断聚合（原实现绑 r.Context()，断连=聚合半途取消）。
func (w *ReportRollupWorker) RollupDateDetached(day time.Time) (reportrollup.RollupStats, error) {
	return w.RollupDate(context.Background(), day)
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
