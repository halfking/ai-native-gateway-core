// Package credential - Background worker that aggregates per-credential
// BanditScorer snapshots into daily provider_reputation_timeseries rows.
//
// Default schedule: once per day at 02:00 local time (configurable via
// NextRunAfter()). The Run() method is also exported for ad-hoc / one-shot
// invocation (tests, manual triggers, after-deploy backfill).
package credential

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// CredentialLister 是 worker 拉取活跃凭据的接口。
// 任何实现了 List(tenantID string) ([]*Credential, error) 的 store 都可以注入；
// 注意：传 "" 拉取所有租户的凭据。
type CredentialLister interface {
	List(tenantID string) ([]*Credential, error)
}

// ReputationWorker 后台聚合 worker
type ReputationWorker struct {
	store  ReputationStore
	scorer *BanditScorer
	creds  CredentialLister
	logger *slog.Logger

	// runHour: 每日运行的本地小时（默认 2 = 凌晨 2 点）
	runHour int

	// now: 测试可注入
	now func() time.Time

	// location: 计算 runHour 的时区（默认 Local）
	location *time.Location

	stopCh chan struct{}
	doneCh chan struct{}
}

// ReputationWorkerConfig worker 配置
type ReputationWorkerConfig struct {
	Store    ReputationStore
	Scorer   *BanditScorer
	Creds    CredentialLister
	Logger   *slog.Logger
	RunHour  int            // 0-23
	Location *time.Location // 默认 time.Local
}

// NewReputationWorker 创建 worker
func NewReputationWorker(cfg ReputationWorkerConfig) *ReputationWorker {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RunHour < 0 || cfg.RunHour > 23 {
		cfg.RunHour = 2
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	return &ReputationWorker{
		store:    cfg.Store,
		scorer:   cfg.Scorer,
		creds:    cfg.Creds,
		logger:   cfg.Logger,
		runHour:  cfg.RunHour,
		now:      time.Now,
		location: cfg.Location,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start 启动后台循环
func (w *ReputationWorker) Start(ctx context.Context) {
	if w.scorer == nil || w.store == nil {
		w.logger.Warn("reputation_worker: scorer or store is nil, not starting")
		close(w.doneCh)
		return
	}
	go w.loop(ctx)
}

func (w *ReputationWorker) loop(ctx context.Context) {
	defer close(w.doneCh)
	for {
		next := w.NextRunAfter(w.now())
		wait := next.Sub(w.now())
		if wait < 0 {
			wait = time.Minute
		}
		w.logger.Info("reputation_worker: scheduled", "next_run", next.Format(time.RFC3339), "wait", wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			w.logger.Info("reputation_worker: stopping (ctx done)")
			return
		case <-w.stopCh:
			timer.Stop()
			w.logger.Info("reputation_worker: stopping (stop signal)")
			return
		case <-timer.C:
			if err := w.Run(ctx); err != nil {
				w.logger.Error("reputation_worker: run failed", "error", err)
			}
		}
	}
}

// Stop 优雅停止
//
// 安全语义：若 Start 从未被调用，Stop 不阻塞（doneCh 由 NewReputationWorker
// 初始化但从未被关闭；为避免 goroutine 泄漏，这里用 select 非阻塞等待）。
func (w *ReputationWorker) Stop() {
	if w == nil {
		return
	}
	select {
	case <-w.stopCh:
		// already closed
	default:
		close(w.stopCh)
	}
	// 等待 doneCh 关闭，最长 2s（避免测试 / 异常路径永久阻塞）
	select {
	case <-w.doneCh:
	case <-time.After(2 * time.Second):
	}
}

// NextRunAfter 计算下次运行时间（基于 runHour 的下一个本地时间点）
func (w *ReputationWorker) NextRunAfter(now time.Time) time.Time {
	loc := w.location
	localNow := now.In(loc)
	next := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), w.runHour, 0, 0, 0, loc)
	if !next.After(localNow) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// Run 执行一次完整聚合（用于测试 / 手动触发）
func (w *ReputationWorker) Run(ctx context.Context) error {
	if w.scorer == nil || w.store == nil {
		return errors.New("credential: reputation_worker not configured")
	}
	if w.creds == nil {
		return errors.New("credential: credential lister not configured")
	}

	creds, err := w.creds.List("")
	if err != nil {
		return fmt.Errorf("reputation_worker: list credentials: %w", err)
	}

	// 按 (provider, model) 分组
	groups := make(map[ProviderModelKey][]*Credential)
	for _, c := range creds {
		if c == nil {
			continue
		}
		if c.Status == StatusDisabled || c.Status == StatusUnhealthy {
			continue
		}
		key := ProviderModelKey{ProviderID: c.ProviderID, Model: c.Model}
		groups[key] = append(groups[key], c)
	}

	today := w.now().UTC().Truncate(24 * time.Hour)

	var (
		saved   int
		failed  int
		skipped int
	)
	// 稳定顺序，便于日志与可重现性
	keys := make([]ProviderModelKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ProviderID != keys[j].ProviderID {
			return keys[i].ProviderID < keys[j].ProviderID
		}
		return keys[i].Model < keys[j].Model
	})

	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := w.aggregateDailyMetrics(groups[key])
		row.ProviderID = key.ProviderID
		row.Model = key.Model
		row.Date = today

		if row.RequestCount == 0 {
			skipped++
			continue
		}
		if err := w.store.SaveTimeseries(ctx, row); err != nil {
			failed++
			w.logger.Error("reputation_worker: save timeseries failed",
				"provider", key.ProviderID, "model", key.Model, "error", err)
			continue
		}
		saved++
	}

	w.logger.Info("reputation_worker: aggregated",
		"groups", len(groups), "saved", saved, "failed", failed, "skipped", skipped, "date", today.Format("2006-01-02"))
	return nil
}

// aggregateDailyMetrics 聚合一组凭据的当日指标
func (w *ReputationWorker) aggregateDailyMetrics(creds []*Credential) *TimeseriesRow {
	row := &TimeseriesRow{}
	if len(creds) == 0 {
		return row
	}

	// 加权聚合（按总请求数）
	var (
		weightedLatency float64
		latencyWeight   float64
		banditAlphaSum  float64
		banditBetaSum   float64
	)
	for _, c := range creds {
		snap := w.scorer.SnapshotScore(c.ID)
		if snap.TotalRequests <= 0 {
			continue
		}
		row.RequestCount += snap.TotalRequests
		row.SuccessCount += int64(float64(snap.TotalRequests) * snap.SuccessRate)
		weightedLatency += snap.AvgLatencyMs * float64(snap.TotalRequests)
		latencyWeight += float64(snap.TotalRequests)

		score := w.scorer.GetScore(c.ID)
		banditAlphaSum += score.Alpha
		banditBetaSum += score.Beta
	}

	if latencyWeight > 0 {
		row.AvgLatencyMs = weightedLatency / latencyWeight
	}
	if row.RequestCount > 0 {
		row.SuccessRate = float64(row.SuccessCount) / float64(row.RequestCount)
		row.ErrorRate = 1.0 - row.SuccessRate
		row.ReliabilityScore = row.SuccessRate
	}
	if len(creds) > 0 {
		// 取平均（不按请求加权 — bandit 参数本身是 per-credential 的）
		row.BanditAlpha = banditAlphaSum / float64(len(creds))
		row.BanditBeta = banditBetaSum / float64(len(creds))
	}
	return row
}
