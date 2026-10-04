// Package persist — retention_partition.go
//
// 按日分区快照表的 DROP 型留存（2026-10-04）。
// 设计稿：docs/06-deployment/04-runbooks/runbooks/
//
//	2026-10-04-ursm-snapshot-partitioning-design.md §3.5（目标 DDL）/ §4.2（留存重写）
//
// # 为什么必须换形态
//
// DELETE 型留存把行删掉之后，空页与死元组仍留在堆的物理头部，VACUUM 无法截断
// ⇒ 磁盘单调增长（设计稿 §1.3）。**分区化本身不改变「按行删」这一事实**：
// 批 DELETE 逐分区执行，仍要为每个过期分区做逐行索引删除。所以只有
// DROP 分区能让空间立即归还文件系统。
//
// # 双模与自动切换
//
// 本文件不读任何配置开关，而是每轮探测 pg_class.relkind：
//
//	表还是普通 heap（迁移未执行） ⇒ 走既有批 DELETE（retention.go 的循环）
//	已是分区父表              ⇒ 走 DROP
//
// 于是「先部署代码、后执行迁移」不需要任何协调，也不存在
// 「开关忘了翻、于是 DROP 打到了非分区表」的状态 —— 形态由数据库自己回答。
// 与 Config.ResolvePersistEnabled() 同源的思路：让运行时形态决定行为，
// 而不是让一个可能忘了翻的开关决定。
package persist

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// snapshotPartitionProbeSQL：relkind='p' 即分区父表（PG 11+）。
	// 表不存在时返回 0 行（不是 false 行），调用方据此按非分区处理。
	snapshotPartitionProbeSQL = `
SELECT c.relkind = 'p'
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relname = 'ursm_node_snapshot_min'`

	// snapshotPartitionListSQL：列出每个子分区的名字、它所覆盖时间区间的
	// **右开边界**（Asia/Shanghai 日历日的次日零点）、以及含索引的总字节数。
	//
	// ★ 日历语义全部留在 SQL，且用显式 `AT TIME ZONE 'Asia/Shanghai'` 钉住，
	//   不依赖会话时区 —— 于是 Go 侧不需要第二份时区常量。
	//   本仓库的分区边界另有 `bg/partition_manager.go:33` 的 partitionTZ
	//   （+08 固定偏移）是同一份日历语义在另一个包的副本；两处一旦漂移，
	//   分区边界会静默挪 8 小时，而 DROP 的判据正是这个边界 ⇒ 会提前删掉
	//   最多 8 小时的存活数据。把换算留在 SQL 是为了消掉这个漂移面。
	// ★ 名字不匹配 `_([0-9]{8})$` 的分区 day_end 为 NULL ⇒ Go 侧永不删它。
	//   分区名格式虽是契约，但**违约的后果是空间不回收，不是数据丢失** ——
	//   这是刻意选的方向（宁可留垃圾也不误删）。
	snapshotPartitionListSQL = `
SELECT c.relname,
       (substring(c.relname FROM '_([0-9]{8})$')::date + 1)::timestamp
           AT TIME ZONE 'Asia/Shanghai' AS day_end,
       pg_total_relation_size(c.oid) AS bytes
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
 WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
 ORDER BY c.relname`
)

// 留存模式标识，同时用于日志字段，便于事后从日志反查当时走的哪条路径。
const (
	RetentionModePartitionDrop = "partition-drop"
	RetentionModeRowDelete     = "row-delete"
	RetentionModeDisabled      = "disabled"
)

// RetentionResult 描述一轮清理的结果。
//
// 两种模式的"量纲"不同：DELETE 模式能精确数出删了多少行，但拿不到归还字节数
// （DELETE 归还的空间还要看后续 VACUUM，量不出来也不该猜）；DROP 模式能精确
// 拿到 DROP 前的大小，但**不数行** —— 逐分区 reltuples 相加得到的是估算值，
// 报成"删了 N 行"是撒谎。两个口径各报各的，不合并成一个数。
type RetentionResult struct {
	Mode              string
	RowsDeleted       int64
	PartitionsDropped int
	BytesReclaimed    int64
	// Dropped 候选分区数（未删干净的，用于日志解释"为什么少了"）
	Candidates int
	// Degraded 表示「本轮原打算走 DROP 但失败，已退回批 DELETE」。
	Degraded bool
}

// snapshotPartition 是列表查询的一行。
type snapshotPartition struct {
	name   string
	dayEnd *time.Time // nil = 分区名不符合 YYYYMMDD 契约，永不删除
	bytes  int64
}

// isSnapshotPartitioned 探测快照表当前是否为分区父表。
//
// 探测失败一律按「非分区」处理并记 warn：一次目录读失败不该让留存停摆，
// 退回批 DELETE 至少还能回收空间。表尚未建好（迁移未执行）时同样落到
// 非分区分支，由既有 DELETE 路径去报错，语义与迁移前完全一致。
func (w *SnapshotRetentionWorker) isSnapshotPartitioned(ctx context.Context) bool {
	rows, err := w.db.Query(ctx, snapshotPartitionProbeSQL)
	if err != nil {
		slog.Warn("ursm.v2: snapshot partition probe failed; falling back to row delete",
			"error", err)
		return false
	}
	defer rows.Close()
	if !rows.Next() {
		// 表不存在（例如快照表尚未创建）。
		return false
	}
	var partitioned bool
	if err := rows.Scan(&partitioned); err != nil {
		slog.Warn("ursm.v2: snapshot partition probe scan failed; falling back to row delete",
			"error", err)
		return false
	}
	return partitioned
}

// listExpiredPartitions 列出应当删除的分区：day_end <= cutoff。
// cutoff = now - retention。判定在 Go 侧只有一次时间比较，日历换算在 SQL 里。
func (w *SnapshotRetentionWorker) listExpiredPartitions(ctx context.Context, cutoff time.Time) ([]snapshotPartition, error) {
	rows, err := w.db.Query(ctx, snapshotPartitionListSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []snapshotPartition
	for rows.Next() {
		var p snapshotPartition
		if err := rows.Scan(&p.name, &p.dayEnd, &p.bytes); err != nil {
			return nil, fmt.Errorf("scan snapshot partition row: %w", err)
		}
		if p.dayEnd == nil {
			slog.Warn("ursm.v2: snapshot partition name does not follow the YYYYMMDD contract; never dropped",
				"partition", p.name)
			continue
		}
		if p.dayEnd.After(cutoff) {
			continue
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// dropPartition 在独立事务里 SET LOCAL lock_timeout 后 DROP。
//
// PG 会隐式把分区从父表摘掉，一步到位（不走 DETACH + DROP 两步，失败面小一半）。
// 分区名虽来自系统目录，仍经 pgx.Identifier 转义后拼接，不做裸字符串互拼。
func (w *SnapshotRetentionWorker) dropPartition(ctx context.Context, name string) error {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // safe no-op if Commit succeeds

	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5min'`); err != nil {
		return fmt.Errorf("set lock_timeout for dropping %s: %w", name, err)
	}
	stmt := `DROP TABLE IF EXISTS ` + pgx.Identifier{"public", name}.Sanitize()
	if _, err := tx.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("drop partition %s: %w", name, err)
	}
	return tx.Commit(ctx)
}

// cleanupPartitioned 走 DROP 型留存：查目录 → 逐个 DROP。
//
// 单轮仍受 MaxCleanupWindow 墙钟上限约束：单个 DROP 很快，但分区数可能因
// 长期停机堆积，保留上限把剩余量留给下一个 tick。DROP 型留存没有"批"的概念，
// BatchSize 在此模式下不参与判定（仍用于回退的 DELETE 路径）。
func (w *SnapshotRetentionWorker) cleanupPartitioned(ctx context.Context) (RetentionResult, error) {
	res := RetentionResult{Mode: RetentionModePartitionDrop}
	now := w.nowTime()
	cutoff := now.Add(-w.cfg.Retention)

	parts, err := w.listExpiredPartitions(ctx, cutoff)
	if err != nil {
		return res, err
	}
	res.Candidates = len(parts)

	deadline := now.Add(w.cfg.MaxCleanupWindow)
	for _, p := range parts {
		if w.nowTime().After(deadline) {
			slog.Info("ursm.v2: snapshot partition drop hit the cleanup window; remainder deferred to next tick",
				"partitions_dropped", res.PartitionsDropped, "candidates", res.Candidates)
			break
		}
		if err := w.dropPartition(ctx, p.name); err != nil {
			// 已删的计数保留在返回值里（上面循环已逐个累加），
			// 错误交由调用方决定是否回退。★ 这里绝不能再补加 ——
			// 补加会把同一批分区数两遍。
			return res, err
		}
		res.PartitionsDropped++
		res.BytesReclaimed += p.bytes
	}
	return res, nil
}
