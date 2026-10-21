package systemmonitor

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ★★★★ R85 轮（R80-B 收敛面之一）：本文件此前有**两个**「永久 SKIP 的绿门」，
// 而且它们**掩盖的正是本门自己**：
//
//	① `isSchemaShapeMismatch` 守卫把「夹具插入失败」变成 SKIP
//	   ⇒ 实测（隔离形态库 `itgate_r85_p5`）：夹具缺必填列 `task_id`
//	   ⇒ `ERROR: null value in column "task_id" of relation "system_probe_runs_default"
//	      violates not-null constraint (23502)`
//	   ⇒ ⇒ **被 SKIP 吞掉** ⇒ 门永远绿、什么都没证明。
//	② `TestMetricsCollector_IsReadyForMigration` 是一句 `t.Skip("Requires real DB…")`
//	   ⇒ **恒 SKIP ⇒ 断言为零** ⇒ 而产品侧 `IsReadyForMigration`（`metrics_collector.go:130`）
//	   **是有实现的** ⇒ ⇒ 这是一道**有实现却永远不测**的门。
//
// ⇒ 本轮按 R80 轮在 `admin/usage_enhanced_test.go` 上落地的**同款形态**收敛：
//
//	★★★ **两条边界互补，不是同一条**（这是并行会话 R83 回执里点名要守的那条）：
//	  - 「**关系不在** ⇒ 靶子不对 ⇒ **诚实 SKIP** 并点名缺哪个」
//	    （否则把「环境不是产品形态」报成产品缺陷）；
//	  - 「**关系在、但插入失败** ⇒ **t.Fatalf 红**」
//	    （否则把夹具漂移吞掉 ⇒ 门变成「永远绿」）。
//	⇒ ⇒ **删掉 `isSchemaShapeMismatch`（插入失败不再 SKIP）＋ 新增用例级前置守卫**
//	⇒ ⇒ 并且**把夹具修对**（补 `task_id`）⇒ ⇒ 门第一次真跑、真断言。
//
// ★ 诚实边界：R83 回执明确提醒「别把『删 isSchemaShapeMismatch』顺手扩成
//   『把 requireRelations 也去掉』」⇒ 本文件**保留**了「关系缺失 ⇒ 诚实 SKIP」这条。

// requireRelation 是**用例级**的前置守卫：关系不在 ⇒ 诚实 SKIP 并点名缺哪个。
//
// ★ 为什么不能用「插入失败 ⇒ SKIP」来代替它：
//
//	插入失败**不能**区分「靶子不对（环境不是产品形态）」与「夹具漂移（真缺陷）」，
//	而后者必须红 ⇒ ⇒ 所以前置检查必须**在插入之前**显式地问「关系在不在」。
func requireRelation(t *testing.T, db *pgxpool.Pool, rels ...string) {
	t.Helper()
	ctx := context.Background()
	for _, rel := range rels {
		var got string
		if err := db.QueryRow(ctx,
			`SELECT COALESCE(to_regclass('public.' || $1)::text, '')`, rel).Scan(&got); err != nil {
			t.Fatalf("探测关系 public.%s 失败: %v", rel, err)
		}
		if got == "" {
			t.Skipf("关系 public.%s 不存在 —— 本用例的取数源不在当前库里，"+
				"本次**没有**跑真库行为验证（不是通过）", rel)
		}
	}
}

// openMetricsDB 连库。★ 未设 DSN ⇒ 明确 SKIP 并说明「本门不构成证据」。
func openMetricsDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// ★ R70 轮：原先写死 `postgres://localhost/llm_gateway_test`（**无视 env**）⇒
	//   连不上 ⇒ 静默 SKIP 被记成绿 ⇒ 改成认事实源；未设 ⇒ 明确 SKIP 并说明。
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set —— 请 source scripts/audit/datasource_env.sh \"$DSN\"（未设时本门不构成证据）")
	}
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("Skip: no test DB available (%v)", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.Ping(context.Background()); err != nil {
		t.Skipf("Skip: cannot ping test DB (%v)", err)
	}
	return db
}

func TestMetricsCollector_CollectCoverage(t *testing.T) {
	db := openMetricsDB(t)

	// ★ 前置守卫：**关系不在 ⇒ 诚实 SKIP**（靶子不对 ≠ 产品缺陷）。
	requireRelation(t, db, "system_probe_runs")

	// Setup: insert test data
	//
	// ★ R85 轮修：补 `task_id` —— 它是 bigint NOT NULL 且**无默认值**（真库实测：
	//   `id` 是 GENERATED ALWAYS AS IDENTITY，但 `task_id` 靠调用方给）
	//   ⇒ 旧夹具没给 ⇒ 23502 ⇒ 被 `isSchemaShapeMismatch` 吞成 SKIP ⇒ 门永远绿。
	//   ⇒ 本表**无外键**（真库实测 pg_constraint 无 f）⇒ 填任意 bigint 即可。
	ctx := context.Background()
	setupSQL := `
		INSERT INTO system_probe_runs 
		  (task_id, task_type, automaticity, credential_id, raw_model, source, worker_id, status, started_at, finished_at)
		VALUES
		  (101, 'direct_ping', 'mandatory', 1, 'gpt-4', 'systemmonitor', 'test-worker-1', 'success', NOW() - INTERVAL '1 day', NOW() - INTERVAL '1 day'),
		  (102, 'direct_ping', 'mandatory', 2, 'gpt-4', 'systemmonitor', 'test-worker-2', 'success', NOW() - INTERVAL '2 days', NOW() - INTERVAL '2 days'),
		  (103, 'direct_ping', 'mandatory', 3, 'gpt-3.5', 'legacy_selfcheck', 'legacy-worker-1', 'success', NOW() - INTERVAL '3 days', NOW() - INTERVAL '3 days')
		ON CONFLICT DO NOTHING;
	`
	// ★★ 插入失败 ⇒ **红**（不再 SKIP）。
	//   关系已在上面查过了 ⇒ ⇒ 此时的任何失败都是**夹具漂移或真缺陷** ⇒ 必须红。
	if _, err := db.Exec(ctx, setupSQL); err != nil {
		t.Fatalf("setup test data: %v", err)
	}

	// Cleanup
	defer func() {
		db.Exec(ctx, "DELETE FROM system_probe_runs WHERE worker_id LIKE 'test-%' OR worker_id LIKE 'legacy-%'")
	}()

	// Test
	collector := NewMetricsCollector(db)
	metrics, err := collector.CollectCoverage(ctx, 7)
	if err != nil {
		t.Fatalf("CollectCoverage() error: %v", err)
	}

	// Assertions
	if metrics.TotalTasks < 3 {
		t.Errorf("Expected at least 3 total tasks, got %d", metrics.TotalTasks)
	}
	if metrics.SystemMonitorTasks < 2 {
		t.Errorf("Expected at least 2 systemmonitor tasks, got %d", metrics.SystemMonitorTasks)
	}
	if metrics.LegacyTasks < 1 {
		t.Errorf("Expected at least 1 legacy task, got %d", metrics.LegacyTasks)
	}
	if metrics.CoveragePercent <= 0 || metrics.CoveragePercent > 100 {
		t.Errorf("Expected coverage 0-100%%, got %.2f%%", metrics.CoveragePercent)
	}
	if len(metrics.BySource) == 0 {
		t.Error("Expected non-empty BySource map")
	}
	if len(metrics.ByTaskType) == 0 {
		t.Error("Expected non-empty ByTaskType map")
	}
	if metrics.CollectedAt.IsZero() {
		t.Error("Expected CollectedAt to be set")
	}

	// ★★ 新增（环境无关的**独立复算**）：collector 的三个计数必须等于
	//   「用同一窗口、同一分类口径**另写一遍**」得到的数。
	//   ⇒ 这一条把「分类逻辑」钉住：`source='systemmonitor'` 归 systemmonitor，
	//   `legacy_selfcheck` / `legacy_asset_health` 归 legacy，其余**不算 legacy**。
	//   ⇒ 不复算 ⇒ 只断言「≥ 3」这类下界 ⇒ 分类写错了门照样绿。
	var expTotal, expSysMon, expLegacy int64
	if err := db.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE source = 'systemmonitor'),
		       count(*) FILTER (WHERE source IN ('legacy_selfcheck','legacy_asset_health'))
		FROM system_probe_runs
		WHERE started_at > NOW() - INTERVAL '1 day' * 7`).Scan(&expTotal, &expSysMon, &expLegacy); err != nil {
		t.Fatalf("独立复算失败: %v", err)
	}
	if metrics.TotalTasks != expTotal {
		t.Errorf("TotalTasks=%d 与独立复算 %d 不一致", metrics.TotalTasks, expTotal)
	}
	if metrics.SystemMonitorTasks != expSysMon {
		t.Errorf("SystemMonitorTasks=%d 与独立复算 %d 不一致", metrics.SystemMonitorTasks, expSysMon)
	}
	if metrics.LegacyTasks != expLegacy {
		t.Errorf("LegacyTasks=%d 与独立复算 %d 不一致", metrics.LegacyTasks, expLegacy)
	}

	t.Logf("Metrics: Total=%d, SystemMonitor=%d, Legacy=%d, Coverage=%.2f%%",
		metrics.TotalTasks, metrics.SystemMonitorTasks, metrics.LegacyTasks, metrics.CoveragePercent)
}

// TestMetricsCollector_IsReadyForMigration —— ★★★★★ R85 轮：**从恒 SKIP 的空壳变成真门**。
//
// ★ 原形态：一句话 `t.Skip("Requires real DB with sufficient data")` ⇒
//
//	**断言为零** ⇒ 而产品侧 `IsReadyForMigration`（`metrics_collector.go:130`）**有实现**
//	⇒ ⇒ 这是一道「有实现却永远不测」的门。
//
// ★ 本门断言的是**契约**（环境无关，两个分支都能被覆盖）：
//
//	ready ⟺ coverage ≥ 80，且 message 必须**说清是哪一边**（≥ 80 还是 < 80 还要多久）。
//
// ★ 为什么不用「写死 coverage = 66.67%」来断言：collector 统计的是**窗口内全表**
//
//	⇒ 库的其它行会改变分母 ⇒ 写死值 = 绑定环境 ⇒ 在别的库上必然假红。
//	⇒ ⇒ 用「独立复算出的 coverage」来驱动期望值 ⇒ 任何库里都精确。
func TestMetricsCollector_IsReadyForMigration(t *testing.T) {
	db := openMetricsDB(t)

	requireRelation(t, db, "system_probe_runs")

	ctx := context.Background()
	// ★ 独立种一批**可辨认**的行（worker_id 前缀与本文件其它用例不同 ⇒ 互不误删）。
	//   2 条 systemmonitor + 1 条 legacy ⇒ 在空库上 coverage = 2/3 ≈ 66.67% ⇒ < 80 ⇒ not ready；
	//   在有数据的库上分母变大 ⇒ 期望值随之变 ⇒ 契约断言仍然精确。
	const seedPrefix = "r85mig-"
	_, err := db.Exec(ctx, `
		INSERT INTO system_probe_runs 
		  (task_id, task_type, automaticity, credential_id, raw_model, source, worker_id, status, started_at, finished_at)
		VALUES
		  (201, 'direct_ping', 'mandatory', 11, 'gpt-4', 'systemmonitor', '`+seedPrefix+`sm-1', 'success', NOW() - INTERVAL '1 day', NOW() - INTERVAL '1 day'),
		  (202, 'direct_ping', 'mandatory', 12, 'gpt-4', 'systemmonitor', '`+seedPrefix+`sm-2', 'success', NOW() - INTERVAL '2 days', NOW() - INTERVAL '2 days'),
		  (203, 'direct_ping', 'mandatory', 13, 'gpt-3.5', 'legacy_selfcheck', '`+seedPrefix+`legacy-1', 'success', NOW() - INTERVAL '3 days', NOW() - INTERVAL '3 days')
		ON CONFLICT DO NOTHING;
	`)
	if err != nil {
		t.Fatalf("插入迁移就绪度的种子失败: %v", err)
	}
	defer func() {
		db.Exec(ctx, "DELETE FROM system_probe_runs WHERE worker_id LIKE '"+seedPrefix+"%'")
	}()

	// 独立复算 coverage（同一窗口、同一分类口径）。
	var expTotal, expSysMon int64
	if err := db.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE source = 'systemmonitor')
		FROM system_probe_runs
		WHERE started_at > NOW() - INTERVAL '1 day' * 7`).Scan(&expTotal, &expSysMon); err != nil {
		t.Fatalf("独立复算失败: %v", err)
	}
	var expCoverage float64
	if expTotal > 0 {
		expCoverage = float64(expSysMon) / float64(expTotal) * 100.0
	}
	// ★ 非空断言：种子必须真的进了 collector 的视野 ⇒ 否则本门会**空转**
	//   （「没种上 ⇒ 没数据 ⇒ 断言恒真」是最常见的假绿）。
	if expSysMon < 2 {
		t.Fatalf("种子没进视野（systemmonitor 计数=%d）⇒ 本门会空转，先查夹具", expSysMon)
	}

	collector := NewMetricsCollector(db)
	ready, msg, err := collector.IsReadyForMigration(ctx)
	if err != nil {
		t.Fatalf("IsReadyForMigration() error: %v", err)
	}
	if msg == "" {
		t.Error("IsReadyForMigration 必须给出理由（空 message ⇒ 调用方无从判断该做什么）")
	}

	// ★ 契约：ready ⟺ coverage ≥ 80。
	const threshold = 80.0
	wantReady := expCoverage >= threshold
	if ready != wantReady {
		t.Errorf("IsReadyForMigration ready=%v，但独立复算的 coverage=%.2f%% ⇒ 期望 ready=%v（阈值 %.0f%%）\n  message=%q",
			ready, expCoverage, wantReady, threshold, msg)
	}
	// ★ message 必须**说清是哪一边**（两个分支都要有话说）。
	if wantReady {
		if !containsCoverage(msg, expCoverage) {
			t.Errorf("ready=true 时 message 必须含实测 coverage（%.2f%%），实际=%q", expCoverage, msg)
		}
	} else {
		if !containsCoverage(msg, expCoverage) {
			t.Errorf("ready=false 时 message 必须含实测 coverage（%.2f%%），实际=%q", expCoverage, msg)
		}
	}
	t.Logf("IsReadyForMigration: ready=%v coverage=%.2f%% (独立复算 total=%d systemmonitor=%d) message=%q",
		ready, expCoverage, expTotal, expSysMon, msg)
}

// containsCoverage 判断 message 里是否出现了实测 coverage（允许两位小数的舍入差）。
func containsCoverage(msg string, coverage float64) bool {
	// 消息里的百分比形如 "Coverage 66.67% >= 80%" ⇒ 取 "Coverage " 与 "%" 之间的数。
	const marker = "Coverage "
	i := strings.Index(msg, marker)
	if i < 0 {
		return false
	}
	rest := msg[i+len(marker):]
	j := strings.Index(rest, "%")
	if j < 0 {
		return false
	}
	got, err := strconv.ParseFloat(strings.TrimSpace(rest[:j]), 64)
	if err != nil {
		return false
	}
	diff := got - coverage
	if diff < 0 {
		diff = -diff
	}
	return diff < 0.01
}

func TestCoverageMetrics_Calculation(t *testing.T) {
	// Unit test for calculation logic
	metrics := &CoverageMetrics{
		WindowDays:         7,
		TotalTasks:         100,
		SystemMonitorTasks: 85,
		LegacyTasks:        15,
	}

	// Calculate coverage
	metrics.CoveragePercent = float64(metrics.SystemMonitorTasks) / float64(metrics.TotalTasks) * 100.0

	if metrics.CoveragePercent != 85.0 {
		t.Errorf("Expected 85%%, got %.2f%%", metrics.CoveragePercent)
	}
}
