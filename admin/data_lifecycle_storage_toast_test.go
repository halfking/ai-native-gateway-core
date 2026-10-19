// Top-N 大表端点的 TOAST 列判据（2026-10-20 第四次性能修正）。
//
// 背景：`GET /api/admin/data-lifecycle/storage/tables` 在 2026-10-20 稳定
// 返回 500（"storage: table sizes query failed error=context deadline
// exceeded"，duration_ms=15006）。根因不是"大表多"，而是 TOAST 列的取值
// 表达式是一条**相关子查询**：
//
//	COALESCE(pg_total_relation_size((
//	  SELECT t.oid FROM pg_class t WHERE t.reltoastrelid = c.oid)), 0)
//
// 它同时坏在两处：
//
//	(1) 值恒为 0。子查询找的是「c 自己是某表的 TOAST 表」，而本查询限定
//	    c.relkind IN ('r','p')，TOAST 表的 relkind 是 't' ⇒ 永远匹配不上。
//	    实测 536 张 r/p 表可命中 0 张，而其中 435 张**确实有** TOAST 表。
//	    ⇒ toast_bytes 一直是 0，而 StorageOverview.vue:186 照着渲染它。
//
//	(2) 代价极高。planner 把它展开成 SubPlan → Seq Scan on pg_class，
//	    对 LIMIT 下的每一行重扫全表（≈4000 行）。Top-200 即 200×4000 ≈
//	    **80 万行重复扫描**，且每次还要过 relation_is_a_known_shard() 调用。
//
// 修法：直接用 `c.reltoastrelid` 这一列（零 join、零子扫描，且是真值）。
//
// 判据纪律（对齐 LESSONS）：
//   - 判据必须打在**被测函数所在的那一层**：这里用端点返回的行 + PG 真值
//     对账，而不是"源码里有没有某个字符串"（那是另一道门的职责）。
//   - 凡是"允许侧/命中侧"断言，都要配一条**前提仍在场**的断言：
//     夹具表必须真的落进 Top-N，否则 toast_bytes>0 这条会假绿。
//   - 计划形状那道门跑的是**线上同一条 SQL**（tableSizesSQL 常量），
//     不是测试里复制的副本 —— 复制的副本会让门在 SQL 漂移时静默失守。
//
// 运行：
//
//	source /tmp/lgw-env.sh   # 导出 LLM_GATEWAY_DATABASE_URL（真库判据）
//	go test ./admin -run 'TestTableSizes(ToastBytes|QueryPlan)' -count=1 -v
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"testing"
	"time"
)

// toastFixtureRows 夹具表的行数。
//
// 每行 4096 字节**不可压缩**随机十六进制（md5 串联），共约 4MB。
// 不可压缩是必须的：TOAST 会先做 pglz 压缩，若夹具用 repeat('x', 8192)
// 这类高度可压缩的内容，压缩后不足阈值就**根本不进 TOAST**，
// toast 大小恒为 0 —— 那样这道门会在"夹具没造出 TOAST"的情况下
// 变成"断言 0 == 0"的假绿。
//
// 4MB 远大于本机 Top-200 的门槛（实测第 200 名 48KB），
// 夹具表必然落进 Top-N；万一某天门槛涨过 4MB，用例会**明确失败**
// 并提示"前提不在场"，而不是悄悄跳过断言。
const toastFixtureRows = 1024

// TestTableSizesToastBytesReportsRealValue 端点给出的 toast_bytes 必须是真值。
//
// 三段断言：
//
//	① 前提仍在场：夹具表必须出现在端点返回的 Top-N 里（否则下面的
//	   toast_bytes>0 无从观察，会假绿）。
//	② toast_bytes > 0 —— 旧实现恒为 0，这条会红。
//	③ toast_bytes == PG 真值（pg_total_relation_size(reltoastrelid)）。
func TestTableSizesToastBytesReportsRealValue(t *testing.T) {
	h, pool, schema := newStoragePerfHandler(t)
	ctx := context.Background()

	// 夹具：不可压缩随机数据 ⇒ 必然进 TOAST。
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE %s.t_toastfix (id bigint, pad text)", schema)); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s.t_toastfix SELECT g, (SELECT string_agg(md5(random()::text),'') FROM generate_series(1,128)) FROM generate_series(1,$1) g",
		schema), toastFixtureRows); err != nil {
		t.Fatalf("fill fixture table: %v", err)
	}

	// PG 真值：夹具表的 TOAST 大小。
	var truth int64
	if err := pool.QueryRow(ctx, `
		SELECT pg_total_relation_size(c.reltoastrelid)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = 't_toastfix'`, schema).Scan(&truth); err != nil {
		t.Fatalf("probe ground truth: %v", err)
	}
	// 前提仍在场：PG 侧确实有 TOAST。
	if truth <= 0 {
		t.Fatalf("前提不在场：夹具表没有 TOAST（pg_total_relation_size(reltoastrelid)=%d）—— 夹具造法失效，用例无从观察", truth)
	}

	// 取值走端点的核心路径 tableSizesSnapshot（handler 只是在外面套了个
	// 15s 请求预算 + JSON 编码）。这里给足 ctx：这道门断言的是**值**，
	// 不是延迟；延迟由部署后的真实端点复验（见审计文档）。
	// 用 handler 的 15s 预算来取数据，会让这道门在"冷页缓存 + 机器有并发
	// 负载"时因**超时**变红 —— 那是环境噪声，不是被测逻辑的缺陷。
	snapCtx, snapCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer snapCancel()
	resp, err := h.tableSizesSnapshot(snapCtx, tableSizesMaxLimit)
	if err != nil {
		t.Fatalf("tableSizesSnapshot: %v", err)
	}

	var found *tableSizeInfo
	for i := range resp.Tables {
		if resp.Tables[i].Table == "t_toastfix" {
			found = &resp.Tables[i]
			break
		}
	}
	// ① 前提不在场
	if found == nil {
		t.Fatalf("前提不在场：夹具表 t_toastfix 不在 Top-N（返回 %d 行）—— 无法观察 toast_bytes", len(resp.Tables))
	}

	// ② 值必须非零（旧实现恒为 0）
	if found.ToastBytes <= 0 {
		t.Errorf("ToastBytes = %d，want > 0 —— TOAST 大小恒为 0（旧的相关子查询对 relkind IN ('r','p') 永远匹配不上）", found.ToastBytes)
	}
	// ③ 与 PG 真值一致
	if found.ToastBytes != truth {
		t.Errorf("ToastBytes = %d，PG 真值 = %d —— 端点给的不是真值", found.ToastBytes, truth)
	}
	t.Logf("toast_bytes=%d pg_truth=%d total=%d", found.ToastBytes, truth, found.TotalBytes)
}

// TestTableSizesQueryPlanHasNoCorrelatedSubplan 线上同一条 SQL 的计划里
// **不得靠「在 pg_class 里反查」来取 TOAST**。
//
// 这道门守的是那个逐行重复扫描的形状本身。相关子查询
//
//	(SELECT t.oid FROM pg_class t WHERE t.reltoastrelid = c.oid)
//
// 会被 planner 用两种形态落地，**两者都要拦**：
//
//	(a) SubPlan：对每一行重扫一遍 pg_class（loops=N）。
//	(b) 半连接拉起：变成 Nested Loop 的内层，
//	    `Seq Scan on pg_class Filter=(... AND (reltoastrelid = c.oid))`，
//	    同样对外层每行扫一遍全表。
//
// 第一版判据只查「计划里有没有 SubPlan 节点」，被 (b) 形态**假绿**了：
// 变异（把表达式改回相关子查询）时计划形状恰好落到 (b)，门全绿。
// ⇒ 判据改成钉**不变量**而不是节点名字：
//
//	计划里任何「条件」（Filter / Hash Cond / Merge Cond / Join Filter /
//	Recheck Cond / Index Cond）都不得出现 reltoastrelid。
//
// 这条不变量与 planner 选哪种落地形态无关：坏写法一定把 reltoastrelid
// 放进某个条件里；好写法的 reltoastrelid 只作为
// pg_total_relation_size(c.reltoastrelid) 的实参出现，不进任何条件
// （实测好计划的 JSON 里 reltoastrelid **一次都不出现**）。
//
// 变异验证：把表达式改回
//
//	COALESCE(pg_total_relation_size((
//	  SELECT t.oid FROM pg_class t WHERE t.reltoastrelid = c.oid)), 0)
//
// 这道门会红（计划里出现 reltoastrelid 条件）。
func TestTableSizesQueryPlanHasNoCorrelatedSubplan(t *testing.T) {
	_, pool, _ := newStoragePerfHandler(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// EXPLAIN 线上同一条 SQL。$1 走扩展协议绑定，PG 允许 EXPLAIN 带参数。
	raw, err := pool.Query(ctx, "EXPLAIN (FORMAT JSON) "+tableSizesSQL, tableSizesMaxLimit)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer raw.Close()
	var explainJSON []byte
	if raw.Next() {
		if err := raw.Scan(&explainJSON); err != nil {
			t.Fatalf("scan explain: %v", err)
		}
	} else {
		t.Fatalf("explain 无返回行: %v", raw.Err())
	}

	var payload []struct {
		Plan json.RawMessage `json:"Plan"`
	}
	if err := json.Unmarshal(explainJSON, &payload); err != nil {
		t.Fatalf("decode explain: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("explain 返回空")
	}

	var root map[string]any
	if err := json.Unmarshal(payload[0].Plan, &root); err != nil {
		t.Fatalf("decode plan: %v", err)
	}

	// 收集计划里所有「条件」字段。
	conditionFields := []string{
		"Filter", "Hash Cond", "Merge Cond", "Join Filter",
		"Recheck Cond", "Index Cond", "Conflict Filter",
	}
	var offenders []string
	var walk func(node map[string]any, path string)
	walk = func(node map[string]any, path string) {
		nt, _ := node["Node Type"].(string)
		for _, f := range conditionFields {
			v, ok := node[f].(string)
			if !ok {
				continue
			}
			if strings.Contains(v, "reltoastrelid") {
				offenders = append(offenders, fmt.Sprintf("%s/%s: %s", path, nt, v))
			}
		}
		kids, _ := node["Plans"].([]any)
		for i, kid := range kids {
			m, ok := kid.(map[string]any)
			if !ok {
				continue
			}
			walk(m, fmt.Sprintf("%s/%s[%d]", path, nt, i))
		}
	}
	walk(root, "Plan")

	if len(offenders) > 0 {
		t.Errorf("计划里有 %d 处条件引用了 reltoastrelid（%v）—— "+
			"说明 TOAST 大小是靠「在 pg_class 里反查谁指向 c」得到的，"+
			"那会对结果集每一行重扫一遍全表；必须直接用 c.reltoastrelid 取列",
			len(offenders), offenders)
	}
}

// TestStorageCollectBudgetOutlivesRequestBudget 采集预算必须长于请求预算。
//
// 这条是「/storage/tables 稳定 500」那个故障的**结构前提**：
// 采集的 ctx 与请求 ctx 是解耦的（context.WithoutCancel），所以请求超时
// 不会掐死采集 —— 真正会掐死采集的只有 storageCollectTimeout。
// 若采集预算 ≤ 请求预算，冷页缓存下一次采集（实测 11–17s）就会在跑完
// 之前被掐掉 ⇒ 快照永远建不出来 ⇒ 每次请求都重试、都失败。
func TestStorageCollectBudgetOutlivesRequestBudget(t *testing.T) {
	if storageCollectTimeout <= storageRequestTimeout {
		t.Errorf("storageCollectTimeout = %v，storageRequestTimeout = %v —— "+
			"采集预算必须大于请求预算，否则采集会在跑完前被自己掐掉，快照永远建不出来",
			storageCollectTimeout, storageRequestTimeout)
	}
}

// TestTableSizesCollectSurvivesCallerCancellation 调用方取消后，采集仍须跑完
// 并把快照写进去。
//
// 这道门是行为门，验的是 2026-10-20 那个 500 的真正机制：采集 ctx 必须
// 从 context.WithoutCancel 派生。少了它，请求一断开就把共享采集连坐取消，
// 于是**谁也没等到快照**，而表面上看只是"这次请求失败了"。
//
// 判据纪律：这里不断言"调用方拿到了结果"（调用方确实会拿到 ctx.Err()），
// 断言的是**快照最终存在** —— 那才是"下次请求能命中"的真正前提。
func TestTableSizesCollectSurvivesCallerCancellation(t *testing.T) {
	h, pool, schema := newStoragePerfHandler(t)

	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("CREATE TABLE %s.t_cancel (id bigint)", schema)); err != nil {
		t.Fatalf("create table: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// 调用方在采集过程中被取消：这里拿不到结果是预期的（返回 ctx.Err()）。
		_, _ = h.tableSizesSnapshot(ctx, tableSizesMaxLimit)
	}()

	// 等采集真的开始（计数器在采集入口自增）。
	startDeadline := time.Now().Add(15 * time.Second)
	for tableSizesCollectCount() == 0 {
		if time.Now().After(startDeadline) {
			t.Fatal("采集没有开始 —— 用例无从观察")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 掐掉调用方。
	cancel()

	// 采集必须仍然跑完，并把快照写进去。
	deadline := time.Now().Add(90 * time.Second)
	for {
		if snap := tableSizesSnapshots.get(h.db, false); snap != nil {
			if len(snap.rows) == 0 {
				t.Fatal("快照存在但没有数据行")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("调用方 ctx 取消后，采集没有跑完 —— " +
				"共享采集被连坐取消 ⇒ 快照永远建不出来 ⇒ 端点稳定 500")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
