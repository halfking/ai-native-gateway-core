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
//   · 默认 7 天（2026-11-17：从 14d 收窄——211 上 14d≈10 万行/21MB，
//     retireProbeMinutes 全表相关子查询单次 650–900ms；批删 + 更短 TTL
//     双管齐下。可经 lifecycle.session_turns_probe_hot_ttl_days 热加载）
//   · 两表共用同一 TTL（探测分钟表是流水的派生面，源清了派生也应清）
//   · 下限夹到 1：0/负值若透传会在一个 tick 清空整表
//   · TTL DELETE 走 LIMIT 批删（收窄瞬间可能一次过期数万行）
//   · db==nil 时 no-op（与 session_turn_logs TTL 同款）
//
// ★ 索引：session_turns_probe_hot 有 idx_*_ts（ts DESC）；
//   request_stats_probe_minute 有 bucket 主键前缀 ⇒ DELETE 走索引下降。

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	// defaultProbeLaneTTLDays 是 settings 键 lifecycle.session_turns_probe_hot_ttl_days
	// spec 缺失/未装载时的 fallback。★ 必须与 settings/spec_lifecycle.go 同键的
	// spec Default 保持一致（2026-11-17 b6ee9e543 有意从 14 收窄到 7——
	// 那次只改了本侧与钉测，spec 侧漏改，由 2026-11-20 审计轮补齐到 7）。
	defaultProbeLaneTTLDays = 7
	minProbeLaneTTLDays     = 1
	maxProbeLaneTTLDays     = 90
	// probeLaneTTLBatch 是 TTL DELETE 每 tick 上限。
	// 与 retireProbeMinutesBatch 同量级：积压靠后续 tick 收敛，
	// 避免收窄默认窗后一个 tick 删半表卡 30s 超时。
	probeLaneTTLBatch = 2000
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
		`WITH doomed AS (
			SELECT ctid FROM session_turns_probe_hot
			 WHERE ts < now() - make_interval(days => $1::int)
			 ORDER BY ts
			 LIMIT $2
		)
		DELETE FROM session_turns_probe_hot t
		 USING doomed WHERE t.ctid = doomed.ctid`,
		retentionDays)
	pm.deleteProbeLaneExpired(timeoutCtx, "request_stats_probe_minute",
		`WITH doomed AS (
			SELECT ctid FROM request_stats_probe_minute
			 WHERE bucket < now() - make_interval(days => $1::int)
			 ORDER BY bucket
			 LIMIT $2
		)
		DELETE FROM request_stats_probe_minute t
		 USING doomed WHERE t.ctid = doomed.ctid`,
		retentionDays)
}

func (pm *PartitionManager) deleteProbeLaneExpired(ctx context.Context, table, sql string, retentionDays int) {
	tag, err := pm.db.Exec(ctx, sql, retentionDays, probeLaneTTLBatch)
	if err != nil {
		slog.Error("partition_manager: probe lane TTL cleanup failed",
			"table", table, "retention_days", retentionDays,
			"batch", probeLaneTTLBatch, "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("partition_manager: cleaned probe lane table",
			"table", table, "deleted_rows", n,
			"retention_days", retentionDays, "batch", probeLaneTTLBatch)
		if n >= int64(probeLaneTTLBatch) {
			slog.Info("partition_manager: probe lane TTL backlog remains",
				"table", table, "note", fmt.Sprintf("deleted full batch=%d; next tick continues", probeLaneTTLBatch))
		}
	}
}
