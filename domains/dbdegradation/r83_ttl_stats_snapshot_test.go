package dbdegradation

// R83 轮（2026-10-24）：TTL 统计腿的**快照 + 后台刷新**守卫。
//
// ★ 背景（154 生产实测）：`ttl_stats` 腿**每次请求**都全量 SCAN `session:*`
//
//	（生产 ~1M 键）⇒ 端点只有 5s 预算 ⇒ **扫描必然被打断**
//	⇒ 计数永远是部分值 ⇒ `degraded` **长期为 true**。
//
// ★ 本文件钉住：
//
//	① **快照命中 ⇒ 请求路径零 SCAN**（成本探针不得增长）
//	② **无快照 + ctx 取消** ⇒ 必须报错**且**返回已统计到的部分（R79 语义保留）
//	③ ★ **部分结果不得进快照**（R82 教训：坏结果会冒充好结果）
//	④ 快照**必须带年龄**（它是「某个已知时刻的计数」⇒ 运维要知道它有多旧）
//	⑤ **阳性对照**：探针计数确实会增长（否则 ① 可能因「计数器坏了」而恒绿）

import (
	"context"
	"testing"
	"time"
)

// r83Keys 是夹具用的键集（覆盖三个前缀 + 一个无关前缀）。
var r83Keys = []string{
	"session:a", "session:b",
	"session:key:k1", "session:key:k2",
	"session:apiKey:idx1",
	"ursm:u1", "other:o1",
}

// TestR83TTLStatsSnapshotMustAvoidRescanOnRequestPath（★ 不变量①）
//
// ★ 「有缓存」不等于「每次请求都命中缓存」（LESSONS §322）⇒ 这里用
// ★ **SCAN 探针**做**确定性**读数（计时断言天然 flaky ⇒ 不用计时）。
func TestR83TTLStatsSnapshotMustAvoidRescanOnRequestPath(t *testing.T) {
	tm, _, _ := newR79TTLManager(t, r83Keys)
	ctx := context.Background()

	// ① 第一次：必须真的扫（阳性对照 ⇒ 后面「没增长」才有意义）
	before := tm.TTLStatsScans()
	first, err := tm.GetTTLStats(ctx)
	if err != nil {
		t.Fatalf("★ 第一次取 TTL 统计不该报错：%v", err)
	}
	if tm.TTLStatsScans() == before {
		t.Fatal("★ 阳性对照失效：第一次调用必须真的 SCAN（否则「探针不增长」是空转）")
	}
	if got, _ := first["sessions_count"].(int); got != 5 {
		t.Errorf("★ 口径不变：session 键计数应为 5，实测 %d", got)
	}

	// ② 第二次：必须命中快照 ⇒ **零 SCAN**
	afterFirst := tm.TTLStatsScans()
	second, err := tm.GetTTLStats(ctx)
	if err != nil {
		t.Fatalf("★ 第二次取 TTL 统计不该报错（快照命中）：%v", err)
	}
	if got := tm.TTLStatsScans(); got != afterFirst {
		t.Errorf("★ 快照命中 ⇒ 请求路径不得再 SCAN（探针 %d → %d）—— "+
			"那正是生产上「每次请求全量扫 ~1M 键」的根源", afterFirst, got)
	}
	if second["sessions_count"] != first["sessions_count"] {
		t.Errorf("★ 命中快照时计数不得变化：首次=%v 二次=%v",
			first["sessions_count"], second["sessions_count"])
	}
}

// TestR83TTLStatsInterruptedScanMustErrorAndNotCachePartial（★ 不变量②③）
//
// ★ R79 语义保留：ctx 取消 ⇒ **必须报错**，且**仍返回已统计到的部分**。
// ★ ★ R83 加码：★ **部分结果不得进快照** —— 否则下一次请求会拿到
// 　「部分计数」却以为它是完整的（R82 教训：部分结果会冒充好结果）。
func TestR83TTLStatsInterruptedScanMustErrorAndNotCachePartial(t *testing.T) {
	tm, _, _ := newR79TTLManager(t, r83Keys)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stats, err := tm.GetTTLStats(ctx)
	if err == nil {
		t.Fatalf("★ ctx 已取消 ⇒ GetTTLStats 必须报错（否则部分计数会被当成完整计数），实测 stats=%v", stats)
	}
	if stats == nil {
		t.Fatal("★ 报错时仍应返回已统计到的部分数据（stats 不能为 nil）")
	}
	// ★ 部分结果**不得**进快照
	if snap, _ := tm.ttlStatsSnapshotNow(); snap != nil {
		t.Errorf("★ 被中断的部分结果不得进快照（它会冒充完整计数）—— 实测快照=%+v", snap)
	}
}

// TestR83TTLStatsSnapshotMustCarryAge（★ 不变量④）
//
// ★ 快照是「某个**已知时刻**的真实计数」⇒ **必须**让运维知道它有多旧
// 　⇒ 否则「会话数 70 万」会被当成「此刻的会话数」。
func TestR83TTLStatsSnapshotMustCarryAge(t *testing.T) {
	tm, _, _ := newR79TTLManager(t, r83Keys)
	ctx := context.Background()

	first, err := tm.GetTTLStats(ctx)
	if err != nil {
		t.Fatalf("★ 第一次取 TTL 统计不该报错：%v", err)
	}
	if _, ok := first["snapshot_at"]; !ok {
		t.Error("★ 快照必须带 snapshot_at（它来自 stat/计数时刻，必须可见）")
	}
	if _, ok := first["snapshot_age_seconds"]; !ok {
		t.Error("★ 快照必须带 snapshot_age_seconds（运维要知道它有多旧）")
	}

	// ★ 年龄必须会长大
	time.Sleep(1100 * time.Millisecond)
	second, err := tm.GetTTLStats(ctx)
	if err != nil {
		t.Fatalf("★ 第二次取 TTL 统计不该报错：%v", err)
	}
	age1, _ := first["snapshot_age_seconds"].(int64)
	age2, _ := second["snapshot_age_seconds"].(int64)
	if age2 <= age1 {
		t.Errorf("★ 年龄必须随时间增长：首次=%d 二次=%d", age1, age2)
	}
}

// TestR83TTLStatsStaleSnapshotReturnsOldValueWithoutError（★ 过期快照的处理）
//
// ★ 快照过期 ⇒ **返回旧快照**（真实但旧）⇒ **不报错**
// 　⇒ 端点不会因为「数字有点旧」就报降级；
// 　★ 而**年龄**字段让运维自己判断（这才是诚实的做法）。
func TestR83TTLStatsStaleSnapshotReturnsOldValueWithoutError(t *testing.T) {
	tm, _, _ := newR79TTLManager(t, r83Keys)
	ctx := context.Background()

	if _, err := tm.GetTTLStats(ctx); err != nil {
		t.Fatalf("★ 第一次取 TTL 统计不该报错：%v", err)
	}
	// ★ 把 TTL 调到 0 ⇒ 立刻过期
	tm.statsTTL = 0

	stats, err := tm.GetTTLStats(ctx)
	if err != nil {
		t.Errorf("★ 过期快照**仍有效**（只是旧）⇒ 不该报错：%v", err)
	}
	if got, _ := stats["sessions_count"].(int); got != 5 {
		t.Errorf("★ 过期快照的计数仍须照报（口径不变）：实测 %d", got)
	}
}
