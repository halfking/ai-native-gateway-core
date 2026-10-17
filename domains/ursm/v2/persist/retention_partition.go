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
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"
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

// snapshotRetentionLockKey 是守住 DROP 型留存跨实例互斥的 advisory lock 键。
// 任意常量，只要与本仓其它 advisory lock 不撞即可（MV 刷新用的是 632_2026_08_31）。
//
// ★ 用**事务级**（pg_try_advisory_xact_lock）而不是会话级：本包的 DB 抽象
//
//	走的是连接池，会话级锁绑在哪条连接上并不可靠（下次 Query 可能就换连接），
//	而事务级锁随 COMMIT/ROLLBACK 自动释放，天然不漏锁、不误放。
//
//	有了它，"两台实例同时删同一个分区" 才从「静默双记」变成「一方显式让位」。
const snapshotRetentionLockKey int64 = 632_2026_10_14

var (
	// errPartitionHeldByPeer：另一实例正持有留存锁，本分区留到下一轮。
	errPartitionHeldByPeer = errors.New("snapshot retention lock held by another instance")

	// errPartitionGoneAlready：分区在我们列完目录、拿到锁之前就已经不存在了
	//（典型来源是对端实例刚删完，或运维手工 DROP）。**这不是错误。**
	errPartitionGoneAlready = errors.New("snapshot partition already absent before drop")
)

// isUndefinedTable 判 PG 的 42P01（对象不存在）。
//
// 去掉 IF EXISTS 之后，这一码是「已经被别人删掉了」的唯一可靠信号，
// 而它**必须**被归类为「跳过」而不是「错误」——
// 否则运维手工删过一个分区就会触发整轮降级去批删行（§10.110.33 §九 的取舍）。
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

// isPoolClosed 判「连接池已关闭」这一**关停态**。
//
// ★ 为什么需要它：main 的 defer（cmd/gateway/main.go）在 main 返回那一刻
//
//	关掉共享池，而关停 goroutine 此时可能还在排空后台 worker
//	⇒ worker 的「退出前尽力清一次」必然撞上 closed pool。
//	实测每次关停都会命中（生产见 §10.110.37）。
//
// ★ 关键在于**不能把它当失败**：关停时探针拿不到结果，于是
//
//	「探针失败 → 降级到行删 → 行删也失败」，
//	在日志里留下一条 **mode=row-delete 的 WARN** ——
//	那是**假的**：表并没有真的走行删路径。
//	每一次正常部署都会往日志里掺一条这种谎报，久了足以让读者
//	（和对账脚本）以为生产实际在跑行删形态。
//
// ★ 哨兵来源：pgxpool v5 不导出该错误，它来自底层 puddle。
//
//	已实测（见 shutdown_pool_probe_test.go）pgxpool.Close() 之后
//	Begin/Acquire 的错误可被 errors.Is 认成 puddle.ErrClosedPool，
//	而不是被包成 *pgconn.PgError —— 所以这里用 errors.Is 而非错误码比对。
func isPoolClosed(err error) bool {
	return err != nil && errors.Is(err, puddle.ErrClosedPool)
}

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
	// YieldedToPeer 表示「本轮主动让位给持有留存锁的另一实例」。
	// 它与 Degraded 相反：Degraded 是降级（仍在干活），YieldedToPeer 是
	// 干脆没干（下一个 tick 再说）。两者都必须可观测 —— 分母不同，
	// 混在一起会让"这一轮到底发生了什么"无法事后重建。
	YieldedToPeer bool
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
//
// ★ 例外（2026-10-17）：连接池已关闭时**不记那条 warn**。
//
//	关停态下探针必然失败，而「falling back to row delete」在这里是**谎报** ——
//	表并没有真的走行删路径，只是没机会知道它是分区表。
//	每次正常部署都会掺一条这种 WARN，久了足以让读者以为生产在跑行删形态。
//	真失败仍照记（下面的分支不变），两类态因此可分辨。
func (w *SnapshotRetentionWorker) isSnapshotPartitioned(ctx context.Context) bool {
	rows, err := w.db.Query(ctx, snapshotPartitionProbeSQL)
	if err != nil {
		if !isPoolClosed(err) {
			slog.Warn("ursm.v2: snapshot partition probe failed; falling back to row delete",
				"error", err)
		}
		return false
	}
	defer rows.Close()
	if !rows.Next() {
		// 表不存在（例如快照表尚未创建）。
		return false
	}
	var partitioned bool
	if err := rows.Scan(&partitioned); err != nil {
		if !isPoolClosed(err) {
			slog.Warn("ursm.v2: snapshot partition probe scan failed; falling back to row delete",
				"error", err)
		}
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

// dropPartition 在独立事务里拿留存锁后 DROP。
//
// PG 会隐式把分区从父表摘掉，一步到位（不走 DETACH + DROP 两步，失败面小一半）。
// 分区名虽来自系统目录，仍经 pgx.Identifier 转义后拼接，不做裸字符串互拼。
//
// ★ 三种结局，缺一不可（2026-10-34 / §10.110.33 §九）：
//
//  1. 拿到锁、DROP 成功        ⇒ 真删了，调用方**可以**累加 bytes_reclaimed
//  2. 锁被别人持有            ⇒ 本轮让位（errPartitionHeldByPeer），不计数、不报错
//  3. DROP 时对象已不存在     ⇒ 42P01（errPartitionGoneAlready），不计数、不报错
//
// 第 2 步是**互斥**，第 3 步是**诚实**：早先用 `DROP TABLE IF EXISTS`，
// 对端删完的那个分区在我们这里同样「成功」，于是两台各报一次
// bytes_reclaimed —— 而那个字段是留存唯一成功日志里的数字，累计口径被虚增。
// 去掉 IF EXISTS 后，对端已删变成一条可分类的错误，计数也就只属于真正删掉它的那台。
//
// ⚠️ 代价与补偿：去掉 IF EXISTS 会让「运维手工删过某个分区」从静默容忍变成 42P01。
// 正因为第 3 步把它归类为跳过，这个代价被完全补偿 —— 净行为与旧版一致，
// 唯一变化是**不再虚报**。
func (w *SnapshotRetentionWorker) dropPartition(ctx context.Context, name string) error {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // safe no-op if Commit succeeds

	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5min'`); err != nil {
		return fmt.Errorf("set lock_timeout for dropping %s: %w", name, err)
	}

	// 事务级互斥：同一时刻全网只有一台实例能进 DROP 分支。
	// pg_try_advisory_xact_lock 本身是非阻塞的（try 模式），但它仍然遵守
	// SET LOCAL lock_timeout：若沿用 5min，advisory 拿不到锁时本端会等满 5min
	// 才返回 false——这是浪费（try 语义本应立即失败）。把 lock_timeout 临时设
	// 为 0 让 advisory 立刻返回，advisory 拿回后再恢复 5min 保护后续 DROP TABLE
	// 的表锁等待。
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '0'`); err != nil {
		return fmt.Errorf("set lock_timeout=0 for advisory lock: %w", err)
	}
	var gotLock bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, snapshotRetentionLockKey).
		Scan(&gotLock); err != nil {
		return fmt.Errorf("acquire snapshot retention lock for %s: %w", name, err)
	}
	if !gotLock {
		return fmt.Errorf("%s: %w", name, errPartitionHeldByPeer)
	}
	// 恢复 lock_timeout=5min 保护后续 DROP TABLE 的 ACCESS SHARE 锁等待。
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5min'`); err != nil {
		return fmt.Errorf("restore lock_timeout=5min for drop: %w", err)
	}

	stmt := `DROP TABLE ` + pgx.Identifier{"public", name}.Sanitize()
	if _, err := tx.Exec(ctx, stmt); err != nil {
		if isUndefinedTable(err) {
			return fmt.Errorf("%s: %w", name, errPartitionGoneAlready)
		}
		return fmt.Errorf("drop partition %s: %w", name, err)
	}
	return tx.Commit(ctx)
}

// cleanupPartitioned 走 DROP 型留存：查目录 → 逐个 DROP。
//
// 单轮仍受 MaxCleanupWindow 墙钟上限约束：单个 DROP 很快，但分区数可能因
// 长期停机堆积，保留上限把剩余量留给下一个 tick。DROP 型留存没有"批"的概念，
// BatchSize 在此模式下不参与判定（仍用于回退的 DELETE 路径）。
//
// ★ 两种良性竞态在这里被「跳过」而不是「上抛」（见 dropPartition 的三种结局）：
// 把它们当成错误返回，会让调用方 CleanupWithStats 以为 DROP 整体失败，
// 进而**降级去对整张表批删行** —— 为了 0 个字节的差异付出百万行的写放大。
// 代价只是这个分区晚一小时被删，而留存的语义本身就是「小时级」。
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
		err := w.dropPartition(ctx, p.name)
		switch {
		case errors.Is(err, errPartitionHeldByPeer):
			// 另一实例正在删 ⇒ 本轮整体让位。剩下的候选留到下一轮，
			// 提前 break 是有意的：锁是全局的，继续往下试也只是徒劳。
			res.YieldedToPeer = true
			slog.Info("ursm.v2: snapshot retention yielded this round to another instance",
				"mode", res.Mode, "partitions_dropped", res.PartitionsDropped,
				"candidates", res.Candidates, "partition", p.name)
			return res, nil
		case errors.Is(err, errPartitionGoneAlready):
			// 已经不在了（对端或运维删的）⇒ 我们没删成，**不计数**。
			slog.Info("ursm.v2: snapshot partition already absent; not counted as reclaimed",
				"mode", res.Mode, "partition", p.name)
			continue
		case err != nil:
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
