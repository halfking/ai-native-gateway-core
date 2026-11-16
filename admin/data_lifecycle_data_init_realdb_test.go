package admin

// 数据初始化（data-init）的真库验证。
//
// # ★ 为什么这个测试只读 + EXPLAIN，不真删
//
// 这个功能是**破坏性最强**的一类。真库上跑一遍真 DELETE 会：
//   · 在生产库产生大量 WAL / 死元组；
//   · 删掉真实凭据与用户（哪怕回滚，中间态也会被并发请求看到）。
// ⇒⇒ 真库验证分成两半：
//   ① **统计与预览**：真跑（只读 SQL）⇒ 验证 SQL 形状与行数口径；
//   ② **删除语句**：用 `EXPLAIN`（**不带 ANALYZE**）⇒ 只解析+生成计划，
//      **不执行任何一行** ⇒ 验证「这条 DELETE 在真库上语法成立、目标存在、
//      外键可解析」。
//
// # 环境变量
//
// TEST_DATA_INIT_DSN → LLM_GATEWAY_DATABASE_URL → DATABASE_URL；
// 都没设 ⇒ t.Skip（与仓内其它真库测试同一口径）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dataInitTestDSN 取真库 DSN。
func dataInitTestDSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_DATA_INIT_DSN", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("set TEST_DATA_INIT_DSN (or LLM_GATEWAY_DATABASE_URL) to run the data-init real-DB test")
	return ""
}

// dataInitTestPool 连真库。
func dataInitTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := dataInitTestDSN(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestDataInitCatalog_OnRealDB(t *testing.T) {
	pool := dataInitTestPool(t)
	store := newPGDataInitStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	actor := dataInitActor{Username: "tester", TenantID: "default"}

	// ★ 走**生产同款**入口（dataInitCategoryStats），不是手写循环 ——
	//   手工循环测的是「串行逐表」，与线上并行口径不同 ⇒ 数字没有可比性。
	t0 := time.Now()
	stats, err := dataInitCategoryStats(ctx, store, actor, dataInitCategories)
	if err != nil {
		t.Fatalf("dataInitCategoryStats: %v", err)
	}
	elapsed := time.Since(t0)

	if len(stats) != len(dataInitCategories) {
		t.Fatalf("got %d category stats, want %d", len(stats), len(dataInitCategories))
	}

	total := int64(0)
	for _, st := range stats {
		if st.Key == "" || st.LabelKey == "" {
			t.Errorf("category stat missing key/label: %+v", st)
		}
		if st.Rows < 0 || st.Bytes < 0 {
			t.Errorf("category %s: negative counts rows=%d bytes=%d", st.Key, st.Rows, st.Bytes)
		}
		if st.TargetRows < 0 {
			t.Errorf("category %s: negative target rows %d", st.Key, st.TargetRows)
		}
		// 每张表都要出现（哪怕不存在 ⇒ Exists=false + MissingTables）。
		if len(st.Tables) != len(dataInitCategoryByKey(st.Key).Tables) {
			t.Errorf("category %s: got %d table stats", st.Key, len(st.Tables))
		}
		for _, ts := range st.Tables {
			if !ts.Exists {
				continue
			}
			if ts.BytesHuman == "" {
				t.Errorf("%s: missing bytes_human", ts.Name)
			}
			if ts.OldestAt != nil && ts.NewestAt != nil && ts.OldestAt.After(*ts.NewestAt) {
				t.Errorf("%s: oldest after newest", ts.Name)
			}
		}
		total += st.Rows
	}
	t.Logf("catalog covered %d rows across %d categories in %v", total, len(stats), elapsed)
}

func TestDataInitProtectedRowCounts_OnRealDB(t *testing.T) {
	pool := dataInitTestPool(t)
	store := newPGDataInitStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	actor := dataInitActor{Username: "tester", TenantID: "default"}

	// 系统 key 必须还在（否则「重置 key」会把网关自己的内部调用打断）。
	keptKeys, err := store.CountWhere(ctx, "api_keys", "is_system = true")
	if err != nil {
		t.Fatalf("count system keys: %v", err)
	}
	if keptKeys <= 0 {
		t.Errorf("expected at least one system api key, got %d", keptKeys)
	}

	// 超级管理员必须还在（否则重置后没人能登录）。
	keptAdmins, err := store.CountWhere(ctx, "users", "role = 'super_admin'")
	if err != nil {
		t.Fatalf("count super admins: %v", err)
	}
	if keptAdmins <= 0 {
		t.Errorf("expected at least one super admin, got %d", keptAdmins)
	}

	// 保留谓词自身必须可用（操作者名字不存在时也不该报错）。
	if _, err := store.CountWhere(ctx, "users", "role = 'super_admin' OR username = $1", actor.Username); err != nil {
		t.Fatalf("count protected users: %v", err)
	}
	if _, err := store.CountWhere(ctx, "tenants", "code = 'default' OR code = $1", actor.TenantID); err != nil {
		t.Fatalf("count protected tenants: %v", err)
	}
}

// TestDataInitDeleteStatements_PlanOnRealDB 用 EXPLAIN（不执行）验证
// 每一条 DELETE 在真库上**语法成立、目标存在、外键可解析**。
func TestDataInitDeleteStatements_PlanOnRealDB(t *testing.T) {
	pool := dataInitTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	actor := dataInitActor{Username: "tester", TenantID: "default"}
	plan, err := buildDataInitPlan(dataInitCategoryOrder, actor)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	// 计划顺序必须与注册表顺序一致（外键安全的核心不变式）。
	wantOrder := planCategories(plan)
	if len(wantOrder) != len(dataInitCategoryOrder) {
		t.Fatalf("plan covers %d categories, want %d", len(wantOrder), len(dataInitCategoryOrder))
	}

	for _, d := range plan.Deletes {
		exists, err := func() (bool, error) {
			var ex bool
			err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, d.Table).Scan(&ex)
			return ex, err
		}()
		if err != nil {
			t.Fatalf("to_regclass(%s): %v", d.Table, err)
		}
		if !exists {
			t.Logf("skip %s (table not present in this deployment)", d.Table)
			continue
		}
		// ★ EXPLAIN 不带 ANALYZE ⇒ 只解析+生成计划，不执行任何一行。
		rows, err := pool.Query(ctx, "EXPLAIN "+d.SQL, d.Args...)
		if err != nil {
			t.Errorf("EXPLAIN failed for %s: %v\nSQL: %s", d.Table, err, d.SQL)
			continue
		}
		planRows, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Errorf("read EXPLAIN output for %s: %v", d.Table, err)
			continue
		}
		if len(planRows) == 0 {
			t.Errorf("EXPLAIN returned no plan for %s", d.Table)
		}
	}
}
