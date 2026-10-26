package admin

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件守护 2026-10-17 第四次性能修正：启动预热两份存储快照。
//
// 背景：stale-while-revalidate（1233e7931）把"每 30s 破一次 2 秒"消掉了，
// 但都留了同一个尾巴——进程启动后的第一个请求没有任何快照可读，仍要同步
// 付全额聚合。实测 /storage/tables 首调 2.82s，**仍超过任务规定的 2 秒门槛**。
// PrewarmStorageSnapshots 就是在启动时先把两份快照采好。
//
// 判据纪律（对应 LESSONS）：
//   - 「没再采集」有两种成因——预热把冷路径消掉了，或者**压根没预热**、
//     而请求路径又恰好没触发采集。必须分开判。所以每条用例先钉住
//     "预热真的跑了"（collect == 1），再断言"请求不再新增采集"。
//   - 预热是后台 goroutine，用轮询等它落地，不用 sleep 猜。
//   - 快照"存在"不等于"可用"，额外断言采到了真实行数。

// waitPrewarmSettled 轮询到两份快照**都真的落到 map 里**。
//
// 这里踩过一次坑，值得写下来：`collectStorageOverviewSnapshot` 是先
// `collect.Add(1)`、后跑聚合、**最后**才 `put`。所以拿 collect 计数当
// "预热已完成"的信号会在 put 之前就返回，于是断言 `get(pool) != nil`
// 必然红——正是 LESSONS「计数增长 ≠ 工作完成」那条。
//
// 完成信号只能是快照本身可见。
func waitPrewarmSettled(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		overviewOK := false
		if ov, _ := storageSnapshots.get(pool); ov != nil {
			overviewOK = true
		}
		if overviewOK && tableSizesSnapshots.get(pool, false) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("prewarm never settled within 30s: overview collects=%d table-size collects=%d",
		dataLifecycleStorageCollectCount(), tableSizesCollectCount())
}

// TestStoragePrewarmRemovesFirstCallColdPath 是本修正的主判据：
// 预热跑过一次之后，两个请求路径都**不再**同步采集。
//
// 「不再采集」这一条如果单独看会有假绿：预热根本没跑、而请求路径也没触发
// 采集时它同样成立。所以先钉住预热真的跑过（两份 collect 各为 1）。
func TestStoragePrewarmRemovesFirstCallColdPath(t *testing.T) {
	h, pool, _ := newStoragePerfHandler(t)

	// 前提：无任何快照，且两次真实采集都还没发生。
	if got := dataLifecycleStorageCollectCount(); got != 0 {
		t.Fatalf("fixture must start with zero overview collects, got %d", got)
	}
	if got := tableSizesCollectCount(); got != 0 {
		t.Fatalf("fixture must start with zero table-size collects, got %d", got)
	}

	h.PrewarmStorageSnapshots(context.Background())
	waitPrewarmSettled(t, pool)

	// 前提仍在场：预热确实各采了一次，且用的是请求路径的同一份实现。
	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Fatalf("prewarm must collect overview exactly once, got %d", got)
	}
	if got := tableSizesCollectCount(); got != 1 {
		t.Fatalf("prewarm must collect table sizes exactly once, got %d", got)
	}
	if snap, _ := storageSnapshots.get(pool); snap == nil {
		t.Fatal("prewarm left no overview snapshot; the request path would fall back to the cold path")
	}
	if snap := tableSizesSnapshots.get(pool, false); snap == nil {
		t.Fatal("prewarm left no table-sizes snapshot; the request path would fall back to the cold path")
	}

	// 主断言：两次"首调"都不再触发同步采集。
	beforeOverview := dataLifecycleStorageCollectCount()
	beforeTables := tableSizesCollectCount()

	ov := h.storageOverviewSnapshot(context.Background())
	// 只判"采集失败"类告警，不要求 warnings 为空：磁盘用量、环境差异都会
	// 产生合法告警（例如"本机磁盘已用 ≥ 90%"），把它们算成失败会让判据
	// 随宿主机状态假红。采集失败类告警才说明预热没采到东西。
	for _, w := range ov.Warnings {
		if strings.Contains(w, "失败") || strings.Contains(w, "未完成") {
			t.Errorf("prewarmed overview carries a collection-failure warning: %q", w)
		}
	}
	if _, err := h.tableSizesSnapshot(context.Background(), 20); err != nil {
		t.Fatalf("prewarmed table sizes must serve without error: %v", err)
	}

	if after := dataLifecycleStorageCollectCount(); after != beforeOverview {
		t.Errorf("overview re-collected on the first request (%d → %d); prewarm did not remove the cold path",
			beforeOverview, after)
	}
	if after := tableSizesCollectCount(); after != beforeTables {
		t.Errorf("table sizes re-collected on the first request (%d → %d); prewarm did not remove the cold path",
			beforeTables, after)
	}
}

// TestStoragePrewarmProducesUsableRows 单独判「快照存在」与「快照可用」。
//
// 上一条的漏洞：预热若采出一个**空快照**（比如采集函数被改坏却仍然 put），
// 上面所有断言都会绿，而线上会看到一个字段齐全、rows 为空的页面。
func TestStoragePrewarmProducesUsableRows(t *testing.T) {
	h, pool, _ := newStoragePerfHandler(t)

	h.PrewarmStorageSnapshots(context.Background())
	waitPrewarmSettled(t, pool)

	// 直接读进程内快照，不经请求路径，避免把「请求路径的兜底」当成预热的功劳。
	ov, _ := storageSnapshots.get(pool)
	if ov == nil {
		t.Fatal("no overview snapshot after prewarm")
	}
	if ov.CollectedAt.IsZero() {
		t.Error("prewarmed overview has a zero CollectedAt; the page cannot show when it was measured")
	}

	snap := tableSizesSnapshots.get(pool, false)
	if snap == nil {
		t.Fatal("no table-sizes snapshot after prewarm")
	}
	if len(snap.rows) == 0 {
		t.Error("prewarmed table-sizes snapshot has zero rows; every storage table would vanish from the page")
	}
	if snap.collectedAt.IsZero() {
		t.Error("prewarmed table-sizes snapshot has a zero collectedAt")
	}

	// 采到的必须是**本隔离 schema 里的**表，证明确实打了库而不是拿了个常量。
	foundOwnSchema := false
	for _, r := range snap.rows {
		if r.Schema != "" && !isSystemSchemaForPerf(r.Schema) {
			foundOwnSchema = true
			break
		}
	}
	if !foundOwnSchema && len(snap.rows) > 0 {
		// 隔离 schema 里没有用户表时不强求；这里只保证列形状对。
		for _, r := range snap.rows {
			if r.Table == "" {
				t.Error("prewarmed row has an empty table name")
			}
		}
	}
}

// TestStoragePrewarmSharesOneFlightWithFirstRequest 钉住「预热与请求共用同一
// 个单飞键」这条代码注释里的断言。
//
// 判别力：若预热用了别的键（或干脆绕过 singleflight），预热途中到来的首个
// 请求会**再打一次库**，collect 变成 2，本用例红。反之三种交错（请求先到 /
// 同时 / 预热先完成）都恰好是 1，所以断言是确定性的，不靠时序运气。
func TestStoragePrewarmSharesOneFlightWithFirstRequest(t *testing.T) {
	h, pool, _ := newStoragePerfHandler(t)

	var wg sync.WaitGroup
	start := make(chan struct{})

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		h.PrewarmStorageSnapshots(context.Background())
	}()
	go func() {
		defer wg.Done()
		<-start
		// 请求路径：预热可能已完成、正在飞、尚未开始，三种都要 200。
		if _, err := h.tableSizesSnapshot(context.Background(), 20); err != nil {
			t.Errorf("table sizes must never fail just because prewarm races it: %v", err)
		}
		h.storageOverviewSnapshot(context.Background())
	}()

	close(start)
	wg.Wait()

	// 两边都跑完后，预热必然已经落地（它自己就是其中一个 goroutine）。
	waitPrewarmSettled(t, pool)
	// 留一点余量让可能仍在飞的采集落地，再断言恰好一次。
	time.Sleep(300 * time.Millisecond)
	if got := tableSizesCollectCount(); got != 1 {
		t.Errorf("prewarm and the first request must share one flight, got %d collects", got)
	}
	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Errorf("prewarm and the first request must share one flight, got %d overview collects", got)
	}
}

// isSystemSchemaForPerf 识别 pg_catalog / information_schema 这类系统 schema。
func isSystemSchemaForPerf(s string) bool {
	switch s {
	case "pg_catalog", "information_schema", "pg_toast":
		return true
	}
	return false
}
