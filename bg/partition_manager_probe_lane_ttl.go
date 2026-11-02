package bg

// partition_manager_probe_lane_ttl.go — 探测物理隔离族表的 TTL 清理（U1）。
//
// ★ 为什么要单独文件：
//   928 建了 session_turns_probe_hot / request_stats_probe_minute，注释自称
//   「TTL 由后续清理作业管」，但全仓此前对这两张表零 DELETE ⇒ 单调增长。
//   方案 §0 原口径是「热表 + 短 TTL 为终态」；retireProbeMinutes 只退役
//   「桶已无源数据」的分钟行，**不**处理源流水本身过期。
//
// ★ 口径：
//   · 默认 14 天（方案建议 7–14d；与 model_probe_runs_hot / node_probe_runs
//     同档，可经 lifecycle.session_turns_probe_hot_ttl_days 热加载）
//   · 两表共用同一 TTL（探测分钟表是流水的派生面，源清了派生也应清）
//   · 下限夹到 1：0/负值若透传会在一个 tick 清空整表
//   · db==nil 时 no-op（与 session_turn_logs TTL 同款）
//
// ★ 索引：session_turns_probe_hot 有 idx_*_ts（ts DESC）；
//   request_stats_probe_minute 有 bucket 主键前缀 ⇒ DELETE 走索引下降。

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	defaultProbeLaneTTLDays = 14
	minProbeLaneTTLDays     = 1
	maxProbeLaneTTLDays     = 90
)

// clampProbeLaneTTLDays keeps retention inside [1, 90].
// 0 / 负值夹到 1：否则一个 tick 会把探测族整表过期清空。
func clampProbeLaneTTLDays(raw int) int {
	if raw < minProbeLaneTTLDays {
		return minProbeLaneTTLDays
	}
	if raw > maxProbeLaneTTLDays {
		return maxProbeLaneTTLDays
	}
	return raw
}

// cleanupOldProbeLaneTables deletes expired rows from the probe physical
// isolation tables. Called every PartitionManager tick (same cadence as
// model_probe_runs_hot TTL).
func (pm *PartitionManager) cleanupOldProbeLaneTables(ctx context.Context) {
	if pm == nil || pm.db == nil {
		return
	}
	retentionDays := clampProbeLaneTTLDays(
		settings.GetPlatformInt("lifecycle.session_turns_probe_hot_ttl_days", defaultProbeLaneTTLDays))

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pm.deleteProbeLaneExpired(timeoutCtx, "session_turns_probe_hot",
		"DELETE FROM session_turns_probe_hot WHERE ts < now() - make_interval(days => $1::int)",
		retentionDays)
	pm.deleteProbeLaneExpired(timeoutCtx, "request_stats_probe_minute",
		"DELETE FROM request_stats_probe_minute WHERE bucket < now() - make_interval(days => $1::int)",
		retentionDays)
}

func (pm *PartitionManager) deleteProbeLaneExpired(ctx context.Context, table, sql string, retentionDays int) {
	tag, err := pm.db.Exec(ctx, sql, retentionDays)
	if err != nil {
		slog.Error("partition_manager: probe lane TTL cleanup failed",
			"table", table, "retention_days", retentionDays, "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned probe lane table",
			"table", table, "deleted_rows", n, "retention_days", retentionDays)
	}
}
