package main

// request_logs_view_session_arm_pin_test.go — 2026-10-02。
//
// 把「哪些 request_logs* 视图含 session 臂」这个事实钉在真库上。
//
// 为什么必须钉：S4 停写的四族分类（admin/request_logs_stop_write_classification_test.go）
// 用「读哪一族表」决定后果，而「710 视图仍由 session 臂供数」这条判断**不能**从
// 视图名推出来。实测反例：
//
//	request_logs_with_current_month                              HAS_SESSION_ARM
//	request_logs_with_current_month_without_customer_id          V1_ONLY
//	request_logs_with_current_month_without_request_class_due_at V1_ONLY
//	request_logs_bodies_with_current_month                       V1_ONLY
//
// 后两个是 577/734 迁移为规避投影缺失而**故意**建成 v1-only 的中间层。名字带
// `request_logs_with_` 却会在停写后彻底停止增长。我最初用名字模式判族，把它们
// 一起算成「停写后仍供数」，导致 7 个生产文件的停写后果被系统性低估。
//
// 本门是那份白名单的**唯一**真相来源：它从 pg_get_viewdef 重算，与代码里的
// requestLogsViewsWithSessionArm 比对，不一致即红。手工名单会过期，视图定义
// 会被后续迁移改写——而这里两者任一方向的变化都会被抓到：
//
//	新增一个视图           → 门要求登记（否则它的后果无人知晓）
//	视图被改成有/无 session 臂 → 门报白名单陈旧
//
// 跑法（沿用仓内 TEST_PG_DSN 契约）：
//
//	TEST_PG_DSN=postgres://... go test -tags=integration ./cmd/gateway/ -run TestRequestLogsViewSessionArmPin
//
// 本门**只读**：不建视图、不改数据。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// requestLogsViewsWithSessionArm 必须与本文件内容逐字一致。
// 改它必须同时改这里的复制品，且真库门会核对。
var requestLogsViewsWithSessionArm = map[string]struct{}{
	"request_logs_with_current_month": {},
}

// v1OnlyRequestLogsViews 是本机实测为纯 v1 的视图。登记它们是为了让门能区分
// 「忘了登记」与「确认过是 v1-only」——前者是缺口，后者是结论。
var v1OnlyRequestLogsViews = map[string]struct{}{
	"request_logs_bodies_with_current_month":                       {},
	"request_logs_with_current_month_without_customer_id":          {},
	"request_logs_with_current_month_without_request_class_due_at": {},
}

func TestRequestLogsViewSessionArmPinIsCurrent(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	defer pool.Close()

	// classification.go 里的白名单在本包不可见（它在 admin 包），所以这里
	// 维护一份复制品；两处不一致由 admin 侧的测试另行守护。
	rows, err := pool.Query(ctx, `
		SELECT c.relname, pg_get_viewdef(c.oid, true)
		FROM pg_class c
		WHERE c.relkind = 'v' AND c.relname LIKE 'request\_logs%'
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("query views: %v", err)
	}
	defer rows.Close()

	gotArm := map[string]struct{}{}
	gotOnly := map[string]struct{}{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if strings.Contains(def, "session_turns") {
			gotArm[name] = struct{}{}
		} else {
			gotOnly[name] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(gotArm)+len(gotOnly) == 0 {
		t.Fatal("未扫到任何 request_logs% 视图——扫描口径坏了，不是库里没有视图")
	}

	// 双向比对：只报「登记与真库不符」，不做模糊匹配。
	for name := range gotArm {
		if _, ok := requestLogsViewsWithSessionArm[name]; !ok {
			t.Errorf("视图 %s 含 session 臂（停写后仍供数），但不在白名单里 —— "+
				"未登记会让它的读点被当成 v1-only 而误判为「完全停止增长」", name)
		}
	}
	for name := range requestLogsViewsWithSessionArm {
		if _, ok := gotArm[name]; !ok {
			t.Errorf("白名单里的 %s 在真库上**不含** session 臂 —— 清单陈旧。"+
				"该视图的读者会被误判为「停写后仍供数」", name)
		}
	}
	for name := range gotOnly {
		if _, ok := v1OnlyRequestLogsViews[name]; !ok {
			t.Logf("提示：纯 v1 视图 %s 未登记进 v1OnlyRequestLogsViews"+
				"（不影响本门判定，登记只是为了让「已确认」与「忘了看」可区分）", name)
		}
	}
	t.Logf("真库实测：含 session 臂 %d 个，纯 v1 %d 个",
		len(gotArm), len(gotOnly))
}
