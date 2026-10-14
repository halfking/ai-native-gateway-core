package persist

// 2026-10-14：留存 DROP 的跨实例互斥 + bytes_reclaimed 诚实计数。
//
// # 背景（§10.110.33 §九）
//
// 245 与 154 共用同一张 ursm_node_snapshot_min。当时档位不同（7 vs 30），
// 154 永远看不到过期分区、从不进 DROP 分支 ⇒ 双记缺陷**当时不可达**。
//
// 本轮把默认档位从 30 天收敛到 7 天（DefaultSnapshotRetentionConfig），
// **缺陷因此从"不可达"变成"每轮都会走到"** —— 所以互斥与诚实计数
// 必须与它同批落地，顺序不能反。先改档位再补锁 = 制造一个每天都在虚报
// 字节数的日志面。
//
// # 三种结局（见 dropPartition）
//
//	拿到锁 + DROP 成功  ⇒ 真删了，bytes_reclaimed 归它
//	锁被别人持有        ⇒ 本轮让位，不计数、不报错、不降级
//	DROP 时 42P01       ⇒ 已经不在了（对端/运维删的），不计数、不报错
//
// # 这组判据最要紧的一条是阴性对照
//
// TestSnapshotRetentionRealDropErrorStillDegrades：真实错误**必须仍然触发降级**。
// 没有它，「把任意错误都归类成良性跳过」这种过度放宽的变异能同时骗过
// 让位用例与 42P01 用例 —— 而它在生产上的后果恰恰相反：
// 锁超时、权限不足都会被悄悄吞掉，留存静默停摆。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 过期分区的 dayEnd 必须早于 cutoff。helper 复用 partitionWorker。
func expiredPartition(name string, bytes int64, now time.Time, retention time.Duration) fakePartition {
	dayEnd := now.Add(-retention).Add(-time.Hour)
	return fakePartition{name: name, dayEnd: &dayEnd, bytes: bytes}
}

// TestSnapshotRetentionYieldsToPeerWithoutCounting 是改动 B 的主判据：
// 另一实例持锁时，本轮**整体让位**，且三件事都不能发生：
// 计数、一条 DELETE、以及报错。
func TestSnapshotRetentionYieldsToPeerWithoutCounting(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 30, 0, 0, time.UTC)
	retention := 7 * 24 * time.Hour

	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions: []fakePartition{
			expiredPartition("ursm_node_snapshot_min_20261007", 501325824, now, retention),
			expiredPartition("ursm_node_snapshot_min_20261008", 1007534080, now, retention),
		},
		lockDenied: true, // 对端正持锁
	}
	w := partitionWorker(t, db, now, retention)

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("yielding to a peer must NOT surface as an error; got %v", err)
	}
	if !res.YieldedToPeer {
		t.Fatal("YieldedToPeer = false, want true — the round must announce that it stood down")
	}
	if res.PartitionsDropped != 0 || res.BytesReclaimed != 0 {
		t.Fatalf("yielded round reported dropped=%d bytes=%d, want 0/0 (we deleted nothing)",
			res.PartitionsDropped, res.BytesReclaimed)
	}
	if res.Degraded {
		t.Fatal("Degraded = true, want false — standing down is not a fallback to row delete")
	}
	if res.Candidates != 2 {
		t.Fatalf("Candidates = %d, want 2 (both were listed before the lock was refused)", res.Candidates)
	}
	if n := db.lockCalls; n != 1 {
		t.Fatalf("advisory lock attempts = %d, want 1 — the loser must stop after the first refusal, not grind through every candidate", n)
	}
	if got := db.droppedStatements(); len(got) != 0 {
		t.Fatalf("no DROP may be issued without the lock; got %v", got)
	}
	// ★ 最要命的一条：让位绝不能引发批 DELETE。
	// 若把让位当错误上抛，调用方会降级去对整张表批删行 ——
	// 为了 0 个字节的差异付出百万行写放大。
	for _, stmt := range db.statements() {
		if strings.Contains(stmt, "DELETE FROM") {
			t.Fatalf("yielding must not trigger row delete:\n%s", stmt)
		}
	}
}

// TestSnapshotRetentionDoesNotCountPartitionAlreadyGone 是改动 C 的主判据：
// DROP 时对象已不存在（42P01）⇒ 那是别人删的，**不归我们回收**。
func TestSnapshotRetentionDoesNotCountPartitionAlreadyGone(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 30, 0, 0, time.UTC)
	retention := 7 * 24 * time.Hour
	goneBytes := int64(501325824)

	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions: []fakePartition{
			expiredPartition("ursm_node_snapshot_min_20261007", goneBytes, now, retention),
		},
		dropSQLState: "42P01", // 对端刚删完
	}
	w := partitionWorker(t, db, now, retention)

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("an already-absent partition is not a failure; got %v", err)
	}
	// ★ 这正是旧版 `DROP TABLE IF EXISTS` 的漏洞：对端删完的那个分区在我们这里
	//   同样「成功」，于是两台各报一次 bytes_reclaimed，累计口径虚增。
	if res.BytesReclaimed != 0 {
		t.Fatalf("BytesReclaimed = %d, want 0 — the peer deleted it, not us", res.BytesReclaimed)
	}
	if res.PartitionsDropped != 0 {
		t.Fatalf("PartitionsDropped = %d, want 0 for a partition that was already gone", res.PartitionsDropped)
	}
	if res.Degraded {
		t.Fatal("Degraded = true, want false — 42P01 must not drag the whole round into row delete")
	}
	if res.YieldedToPeer {
		t.Fatal("YieldedToPeer = true, want false — we held the lock; nobody stood us down")
	}
	for _, stmt := range db.statements() {
		if strings.Contains(stmt, "DELETE FROM") {
			t.Fatalf("42P01 must not trigger row delete:\n%s", stmt)
		}
	}
}

// TestSnapshotRetentionRealDropErrorStillDegrades 是上面两条的**阴性对照**。
//
// 锁超时、权限不足这类真错误必须照旧降级去批删行 —— 宁可慢也不能让留存停摆
// （retention.go 的设计原则）。若把「所有错误」都归类成良性跳过，
// 留存会静默停摆，而停摆的表现是磁盘单调增长、没有任何告警。
func TestSnapshotRetentionRealDropErrorStillDegrades(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 30, 0, 0, time.UTC)
	retention := 7 * 24 * time.Hour

	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions: []fakePartition{
			expiredPartition("ursm_node_snapshot_min_20261007", 501325824, now, retention),
		},
		dropErr:      errors.New("canceling statement due to lock timeout"), // 真错误
		batchResults: []int64{42},                                           // 降级后删到 42 行
	}
	w := partitionWorker(t, db, now, retention)

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("a failed drop followed by a successful row delete returns nil; got %v", err)
	}
	if !res.Degraded {
		t.Fatal("Degraded = false, want true — a real drop error must still fall back to row delete")
	}
	if res.RowsDeleted != 42 {
		t.Fatalf("RowsDeleted = %d, want 42 (the row-delete fallback must actually run)", res.RowsDeleted)
	}
	if res.Mode != RetentionModeRowDelete {
		t.Fatalf("Mode = %q, want %q", res.Mode, RetentionModeRowDelete)
	}
	sawDelete := false
	for _, stmt := range db.statements() {
		if strings.Contains(stmt, "DELETE FROM") {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatal("expected the row-delete fallback to have issued at least one DELETE")
	}
}

// TestSnapshotRetentionDropTakesLockAndOmitsIfExists 把「为什么必须去掉
// IF EXISTS」钉成代码形态：那句 IF EXISTS 正是让对端竞态静默成功的元凶。
//
// ★ 断言写成「DROP 语句里不得出现 IF EXISTS」，而不是「代码里不得出现
// 那个字符串」—— 注释有权解释为什么要去掉它，否定的应该是形态不是词
// （批 116 的教训：not.toContain 会读到注释里的字）。
func TestSnapshotRetentionDropTakesLockAndOmitsIfExists(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 30, 0, 0, time.UTC)
	retention := 7 * 24 * time.Hour

	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions: []fakePartition{
			expiredPartition("ursm_node_snapshot_min_20261007", 501325824, now, retention),
		},
	}
	w := partitionWorker(t, db, now, retention)

	if _, err := w.CleanupWithStats(context.Background()); err != nil {
		t.Fatalf("CleanupWithStats() error = %v", err)
	}
	got := db.droppedStatements()
	if len(got) != 1 {
		t.Fatalf("DROP statements = %d, want 1: %v", len(got), got)
	}
	if strings.Contains(strings.ToUpper(got[0]), "IF EXISTS") {
		t.Fatalf("DROP must not carry IF EXISTS — it is what makes a peer's delete look like ours:\n%s", got[0])
	}
	if !strings.Contains(got[0], `"public"."ursm_node_snapshot_min_20261007"`) {
		t.Fatalf("DROP must still use a qualified quoted identifier:\n%s", got[0])
	}
	if db.lockCalls == 0 {
		t.Fatal("the retention advisory lock was never attempted")
	}
	var sawLock bool
	for _, stmt := range db.statements() {
		if strings.Contains(stmt, "pg_try_advisory_xact_lock") {
			sawLock = true
		}
	}
	if !sawLock {
		t.Fatal("expected a pg_try_advisory_xact_lock statement in the round")
	}
}

// TestDefaultSnapshotRetentionIsSevenDays 钉住改动 A。
//
// 默认值必须等于**生产实际生效的值**，否则它只在「env 丢失」那一刻接管，
// 而那一刻的后果是分区 8→31、体积 4~7 GB→13~19 GB（设计文档 §9 Finding E）。
// 生产生效值 = min(245 的 7 天, 154 的默认) = 7 天。
//
// 这条断言的价值不在「7」这个数，而在**任何一次把它调大的改动都会变红**。
func TestDefaultSnapshotRetentionIsSevenDays(t *testing.T) {
	if got, want := DefaultSnapshotRetentionConfig().Retention, 7*24*time.Hour; got != want {
		t.Fatalf("default retention = %s, want %s — the default is the fallback that fires when "+
			"URSM_SNAPSHOT_RETENTION_DAYS is missing, and it must equal the window production "+
			"actually runs (min of the fleet), not a more permissive number",
			got, want)
	}
	// env 未设与 env 非法都必须落回同一个默认值 —— 悬崖正是从这两条路径打开的。
	t.Run("unset env falls back to the production window", func(t *testing.T) {
		t.Setenv("URSM_SNAPSHOT_RETENTION_DAYS", "")
		if got, want := SnapshotRetentionConfigFromEnv().Retention, 7*24*time.Hour; got != want {
			t.Fatalf("unset env -> %s, want %s", got, want)
		}
	})
	t.Run("invalid env falls back to the production window", func(t *testing.T) {
		t.Setenv("URSM_SNAPSHOT_RETENTION_DAYS", "thirty")
		if got, want := SnapshotRetentionConfigFromEnv().Retention, 7*24*time.Hour; got != want {
			t.Fatalf("invalid env -> %s, want %s", got, want)
		}
	})
}
