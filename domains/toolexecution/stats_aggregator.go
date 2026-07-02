package toolexecution

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// StatsAggregator 工具使用统计聚合器。
//
// 聚合维度：tool_name × date（自然日，UTC）
// 聚合指标：
//   - total/success/failed/timeout calls
//   - avg/p50/p95/p99 duration
//   - unique users (identity_hash) & sessions
//   - top N users (按调用次数)
//
// 使用方式：
//   - AggregateDaily(date)：对指定日（默认 UTC）做一次全量聚合
//   - 定时任务按需调用（如每天凌晨 02:00 跑前一日聚合）
type StatsAggregator struct {
	store  Store
	logger *slog.Logger
}

// NewStatsAggregator 构造一个聚合器。
func NewStatsAggregator(store Store, logger *slog.Logger) *StatsAggregator {
	if store == nil {
		panic("toolexecution: nil store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &StatsAggregator{store: store, logger: logger}
}

// AggregateDaily 对指定日期（默认 UTC，截断到 00:00:00）做一次聚合。
// date 为零值时取昨日 UTC。
//
// 该方法是幂等的：同一 (tool_name, date) 多次调用，结果保持一致。
func (a *StatsAggregator) AggregateDaily(ctx context.Context, date time.Time) error {
	if date.IsZero() {
		date = yesterdayUTC()
	}
	day := date.UTC().Truncate(24 * time.Hour)
	start := day
	end := day.Add(24 * time.Hour)

	toolNames, err := a.store.ListToolNamesWithActivity(ctx, start, end)
	if err != nil {
		return fmt.Errorf("toolexecution: list active tools: %w", err)
	}

	a.logger.Info("toolexecution: aggregate daily start",
		"date", day.Format("2006-01-02"),
		"tool_count", len(toolNames),
	)

	var failures int
	for _, toolName := range toolNames {
		stats, err := a.aggregateOne(ctx, toolName, day, start, end)
		if err != nil {
			a.logger.Error("toolexecution: aggregate tool failed",
				"tool", toolName, "error", err,
			)
			failures++
			continue
		}
		if err := a.store.SaveStats(ctx, stats); err != nil {
			a.logger.Error("toolexecution: save stats failed",
				"tool", toolName, "error", err,
			)
			failures++
			continue
		}
	}
	a.logger.Info("toolexecution: aggregate daily done",
		"date", day.Format("2006-01-02"),
		"tools", len(toolNames),
		"failures", failures,
	)
	return nil
}

// aggregateOne 聚合单个工具在某天的统计。
func (a *StatsAggregator) aggregateOne(
	ctx context.Context,
	toolName string,
	day, start, end time.Time,
) (*ToolUsageStats, error) {
	execs, err := a.store.ListByToolName(ctx, toolName, start, end)
	if err != nil {
		return nil, fmt.Errorf("list executions: %w", err)
	}

	stats := &ToolUsageStats{
		ToolName:   toolName,
		Date:       day,
		TotalCalls: int64(len(execs)),
	}

	var durations []int64
	identityCounts := make(map[string]int64)
	sessionSet := make(map[string]struct{})

	for _, exec := range execs {
		switch exec.Status {
		case StatusSuccess:
			stats.SuccessCalls++
		case StatusError:
			stats.FailedCalls++
		case StatusTimeout:
			stats.TimeoutCalls++
		}

		// 延迟统计：仅纳入有有效时长的成功调用，
		// 避免被失败的瞬时错误（duration_ms ≈ 0）污染。
		if exec.DurationMs > 0 && exec.Status == StatusSuccess {
			durations = append(durations, exec.DurationMs)
		}

		if exec.IdentityHash != "" {
			identityCounts[exec.IdentityHash]++
		}
		if exec.SessionID != "" {
			sessionSet[exec.SessionID] = struct{}{}
		}
	}

	// 计算延迟分位数
	computeDurationStats(stats, durations)

	// 用户与会话去重计数
	stats.UniqueUsers = len(identityCounts)
	stats.UniqueSessions = len(sessionSet)

	// Top Users
	type kv struct {
		hash  string
		count int64
	}
	users := make([]kv, 0, len(identityCounts))
	for h, c := range identityCounts {
		users = append(users, kv{h, c})
	}
	sort.Slice(users, func(i, j int) bool {
		if users[i].count != users[j].count {
			return users[i].count > users[j].count
		}
		return users[i].hash < users[j].hash
	})
	limit := TopUsersLimit
	if len(users) < limit {
		limit = len(users)
	}
	if limit > 0 {
		stats.TopUsers = make([]UserUsage, 0, limit)
		for i := 0; i < limit; i++ {
			stats.TopUsers = append(stats.TopUsers, UserUsage{
				IdentityHash: users[i].hash,
				CallCount:    users[i].count,
			})
		}
	}
	return stats, nil
}

// computeDurationStats 排序后计算平均与分位数。
// 使用 nearest-rank 方法（常用实现）；分位数索引使用整数截断。
func computeDurationStats(stats *ToolUsageStats, durations []int64) {
	if len(durations) == 0 {
		return
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	var sum int64
	for _, d := range durations {
		sum += d
	}
	stats.AvgDurationMs = float64(sum) / float64(len(durations))
	stats.P50DurationMs = percentile(durations, 0.50)
	stats.P95DurationMs = percentile(durations, 0.95)
	stats.P99DurationMs = percentile(durations, 0.99)
}

// percentile 给出 nearest-rank 分位数。
//   - p ∈ (0, 1]
//   - 索引 = ceil(p * n) - 1（n 为样本数）
//   - n=0 时返回 0
func percentile(sorted []int64, p float64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[n-1]
	}
	idx := int(float64(n)*p) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

// yesterdayUTC 返回昨日 UTC 00:00:00。
func yesterdayUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, time.UTC)
}
