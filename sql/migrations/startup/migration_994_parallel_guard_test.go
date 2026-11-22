package startup

// migration_994_parallel_guard_test.go —— 994 迁移的**并行守卫**。
//
// # 这道门守什么
//
// 994 Part 3 的 model_usage_aggregates 重建是对
// request_logs_with_current_month 的大聚合 ⇒ PG 会选并行计划 ⇒ 并行 worker
// 要 resize DSM 共享内存段（8MB/worker）。
//
// 211 的 PG 容器 /dev/shm 只有 64MB，且长期滞留 DSM 孤儿段（实测常态水位
// 35~43MB）⇒ 并行 worker 一 resize 就报
// `could not resize shared memory segment … No space left on device`
// （SQLSTATE 53100）⇒ 迁移 FATAL。
//
// ★ 致命之处不在失败本身，而在**失败位置**：本迁移是 DROP + CREATE，
//   DROP 已执行、CREATE 失败 ⇒ 生产上 model_usage_aggregates 视图**缺失**
//   （请求级读面挂），且迁移整体不登记 ⇒ 每次启动重放 ⇒ 反复踩。
//
// ⇒ 修法 = 迁移里显式关并行（会话级 SET，与同文件既有的 lock_timeout /
//   statement_timeout 同族；docker 通道是非事务流式 psql，SET LOCAL 会静默失效）。
//
// ★ 为什么不写成「所有迁移都要关并行」的通用规则：关并行是**按迁移的语句形态**
//   决定的（这里是全量重建预聚合视图）。本门只钉 994 这一条语句的守卫不
//   被后人删掉——删掉它不会报错、只会让下一次启动把视图 DROP 掉。

import (
	"strings"
	"testing"
)

const mig994File = "994_session_analytics_owner_key_and_close_path.sql"

// stmt994 读 994 迁移的**语句部分**（去掉 -- 注释行与空行）。
func stmt994(t *testing.T) string {
	t.Helper()
	var kept []string
	for _, ln := range strings.Split(repoFile(t, "sql/migrations/startup/"+mig994File), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "--") || strings.TrimSpace(ln) == "" {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

func Test994DisablesParallelWorkersPerGather(t *testing.T) {
	sql := stmt994(t)
	normalized := strings.Join(strings.Fields(sql), " ")

	// 必须是会话级 SET（不是 SET LOCAL：docker 通道是非事务流式 psql，
	// SET LOCAL 在那里静默失效——981 已实测记录过这颗雷）。
	if !strings.Contains(normalized, "SET max_parallel_workers_per_gather = 0;") {
		t.Errorf("994 缺少 SET max_parallel_workers_per_gather = 0 —— " +
			"重建 model_usage_aggregates 时并行 worker 会 resize DSM 段，" +
			"撞 211 的 /dev/shm=64MB 上限 ⇒ SQLSTATE 53100 ⇒ 迁移 FATAL，" +
			"且 DROP 已执行 ⇒ model_usage_aggregates 视图缺失")
	}
	if strings.Contains(normalized, "SET LOCAL max_parallel_workers_per_gather") {
		t.Error("994 用的是 SET LOCAL max_parallel_workers_per_gather —— " +
			"docker 通道是非事务流式 psql，SET LOCAL 静默失效")
	}
}

func Test994ParallelGuardPrecedesViewRebuild(t *testing.T) {
	sql := stmt994(t)
	setIdx := strings.Index(sql, "SET max_parallel_workers_per_gather = 0;")
	if setIdx < 0 {
		t.Fatal("994 没有关并行 SET（先看 Test994DisablesParallelWorkersPerGather）")
	}
	// Part 3 的 model_usage_aggregates 重建必须落在 SET 之后。
	marker := "CREATE MATERIALIZED VIEW IF NOT EXISTS public.model_usage_aggregates"
	mvIdx := strings.Index(sql, marker)
	if mvIdx < 0 {
		t.Fatal("994 里找不到 model_usage_aggregates 的 CREATE")
	}
	if mvIdx < setIdx {
		t.Errorf("model_usage_aggregates 重建（偏移 %d）落在关并行 SET（偏移 %d）之前 ⇒ "+
			"并行计划仍会申请 DSM 段", mvIdx, setIdx)
	}
}
