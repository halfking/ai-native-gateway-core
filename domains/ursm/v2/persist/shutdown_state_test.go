package persist

// 2026-10-17：关停态（连接池已关闭）不得被渲染成留存故障，也不得被渲染成降级。
//
// # 根因（不在本文件，在 cmd/gateway/main.go）
//
//	main 早期注册的 defer 在 main **返回那一刻**关掉共享池；
//	而关停 goroutine 此时可能还在排空后台 worker
//	⇒ 每次正常部署，worker 的「退出前尽力清一次」必然撞上 closed pool。
//
// # 生产证据（2026-10-15 18:28:17.853，245 canary-8781）
//
//	18:28:17.853341  WARN ursm.v2: snapshot partition probe failed; falling back to row delete  error=closed pool
//	18:28:17.853430  WARN ursm.v2: snapshot retention cleanup failed (non-fatal)  error=closed pool  mode=row-delete
//	18:28:17.853460  WARN trace: stage events retention cleanup failed (non-fatal)  error=closed pool
//	18:28:17.853705  INFO materialized_view_refresher stopped        ← 证明处在关停序列中
//
// ★ 中间那条 `mode=row-delete` 是**谎报**：表并没有真的走行删路径，
//
//	只是关停时没机会知道它是分区表。每部署一次就往日志里掺一条，
//	久了足以让读者与对账脚本以为生产实际在跑行删形态。
//
// # 这组判据的负向对照是它的成立前提
//
// TestSnapshotRetentionRealProbeErrorStillWarnsFallback 与
// TestSnapshotRetentionRealCleanupErrorStillWarns：
// **真错误必须继续 Warn**。没有它们，「把任何失败都归为关停」这种过度放宽能
// 同时骗过全部关停用例 —— 而那正是把真故障静音的形状。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/puddle/v2"
)

// shutdownLikeDB 造一个「池已关闭」的夹具：探针与批删除都返回哨兵。
func shutdownLikeDB() *fakeSnapshotRetentionDB {
	return &fakeSnapshotRetentionDB{
		partitioned: true,
		probeErr:    puddle.ErrClosedPool,
		execErr:     puddle.ErrClosedPool,
	}
}

// TestSnapshotRetentionShutdownIsNotLoggedAsDegrade 是本题眼：
// 关停态下**一条 WARN 都不许有**，尤其不许出现 "falling back to row delete"。
func TestSnapshotRetentionShutdownIsNotLoggedAsDegrade(t *testing.T) {
	now := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	db := shutdownLikeDB()
	w := partitionWorker(t, db, now, 7*24*time.Hour)
	logs := captureRetentionLogs(t)

	w.CleanupOnce(context.Background())

	for _, rec := range logs() {
		if rec["level"] == "WARN" {
			t.Fatalf("shutdown must not log at WARN; got level=%v msg=%v",
				rec["level"], rec["msg"])
		}
		// 双保险：即使有人把 level 改掉，谎报的那句话也不许出现。
		if msg, _ := rec["msg"].(string); strings.Contains(msg, "falling back to row delete") {
			t.Fatalf("shutdown must never claim a row-delete degrade: %q", msg)
		}
	}

	// ★ 断言的是「没有真的删掉任何东西」，**不是**「一条 DELETE 都没发过」。
	//   探针失败后 worker 会照常尝试批删（生产日志里那条 mode=row-delete
	//   正是这么来的），只是被已关闭的池挡在执行之前。
	//   我第一版断言写成「不得出现 DELETE 语句」，它红了 —— 那是我的断言
	//   描述错了代码，不是代码有问题：量具错了必须先承认量具错了。
	res, err := w.CleanupWithStats(context.Background())
	if err == nil {
		t.Fatal("a closed pool must surface the shutdown error to the caller")
	}
	if !isPoolClosed(err) {
		t.Fatalf("err = %v, want the closed-pool sentinel", err)
	}
	if res.RowsDeleted != 0 {
		t.Fatalf("RowsDeleted = %d, want 0 — a closed pool cannot have deleted anything", res.RowsDeleted)
	}
	if res.PartitionsDropped != 0 || res.BytesReclaimed != 0 {
		t.Fatalf("res = %+v, want zero counters", res)
	}
}

// TestSnapshotRetentionShutdownEmitsExplicitDebugMarker 钉住「不是因为
// 什么恰好没打」，而是**明确打了一行关停标记**。
//
// 这条很重要：只断言「没有 WARN」的话，把整个 CleanupOnce 删掉也能过。
func TestSnapshotRetentionShutdownEmitsExplicitDebugMarker(t *testing.T) {
	now := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	w := partitionWorker(t, shutdownLikeDB(), now, 7*24*time.Hour)
	logs := captureRetentionLogs(t) // 捕获器以 LevelDebug 建立

	w.CleanupOnce(context.Background())

	rec, ok := findRecord(logs(), "ursm.v2: snapshot retention skipped on shutdown; database pool already closed")
	if !ok {
		t.Fatalf("the shutdown marker must be emitted; got records %v", logs())
	}
	if rec["level"] != "DEBUG" {
		t.Fatalf("level = %v, want DEBUG", rec["level"])
	}
	if rec["retention"] != (7 * 24 * time.Hour).String() {
		t.Fatalf("retention = %v, want %s", rec["retention"], (7 * 24 * time.Hour).String())
	}
}

// TestSnapshotRetentionRealProbeErrorStillWarnsFallback 是**阴性对照**。
//
// 探针真失败（不是关停）时，那条降级 WARN 必须照旧出现 ——
// 否则「关停」就成了吞错的万能借口，留存静默停摆而无人知晓。
func TestSnapshotRetentionRealProbeErrorStillWarnsFallback(t *testing.T) {
	now := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	db := &fakeSnapshotRetentionDB{
		probeErr: errors.New("canceling statement due to statement timeout"),
		execErr:  errors.New("canceling statement due to statement timeout"),
	}
	w := partitionWorker(t, db, now, 7*24*time.Hour)
	logs := captureRetentionLogs(t)

	w.CleanupOnce(context.Background())

	var sawFallback, sawFailure bool
	for _, rec := range logs() {
		msg, _ := rec["msg"].(string)
		if rec["level"] == "WARN" && strings.Contains(msg, "falling back to row delete") {
			sawFallback = true
		}
		if rec["level"] == "WARN" && strings.Contains(msg, "cleanup failed") {
			sawFailure = true
		}
	}
	if !sawFallback {
		t.Fatalf("a genuine probe failure must still announce the row-delete fallback; got %v", logs())
	}
	if !sawFailure {
		t.Fatalf("a genuine cleanup failure must still WARN; got %v", logs())
	}
}

// TestSnapshotRetentionPartitionedProbeErrorStillWarnsScanFailure 覆盖
// 上面那条判据的**姊妹分支**：Scan 失败（不是关停）时仍要照记。
func TestSnapshotRetentionPartitionedProbeErrorStillWarnsScanFailure(t *testing.T) {
	now := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	db := &fakeSnapshotRetentionDB{
		partitioned:  true,
		probeScanErr: errors.New("unexpected EOF"),
		execErr:      errors.New("unexpected EOF"),
	}
	w := partitionWorker(t, db, now, 7*24*time.Hour)
	logs := captureRetentionLogs(t)

	w.CleanupOnce(context.Background())

	var saw bool
	for _, rec := range logs() {
		msg, _ := rec["msg"].(string)
		if rec["level"] == "WARN" && strings.Contains(msg, "probe scan failed; falling back") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("a genuine probe SCAN failure must still WARN; got %v", logs())
	}
}
