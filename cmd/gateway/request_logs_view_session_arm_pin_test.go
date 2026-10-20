package main

// request_logs_view_session_arm_pin_test.go — 2026-10-02。
//
// 把「哪些 _request_logs* 视图含 session 臂」这个事实钉在真库上。
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

// v1OnlyRequestLogsViews 是**曾经**为纯 v1 的视图。
//
// ★ 890（v1 族退役）：这 3 张视图已随 v1 族**一起被删掉**（迁移 890 的步骤 4）
// ⇒ 登记表随之清空。
//
// ⇒ 本表从"v1-only 视图的名单"变成了"**曾经**是 v1-only、现在应当已被删掉"的
// 负向名单？—— **不是**。清空它，是因为这张表的语义是"真库上现在存在的纯 v1
// 视图"，而它们已经不存在了。留在表里会让人以为"还有 3 张纯 v1 视图在服役"。
//
// ⇒ 但"删掉了"这件事必须**有门看着**，否则将来有人重建一张同名视图，门不会响。
// ⇒ 新的负向断言在本文件下方：v1OnlyRequestLogsViews 里的名字**不允许**重新
// 出现在真库上（要么真不存在，要么已被改造成含 session 臂）。
var v1OnlyRequestLogsViews = map[string]struct{}{}

// retiredV1OnlyRequestLogsViews 是 890 之后**被删掉**的纯 v1 视图名单。
//
// 它的作用是**负向**的：这三张名字在真库上**不允许**再以"纯 v1"形态出现。
//   · 重新出现（纯 v1）⇒ 有人在重建它们 ⇒ 违反"v1 族已退役"的决定；
//   · 以含 session 臂的形态出现 ⇒ 那是一次**合法**的重建（读面改读会话族），
//     不算违规，但**必须**登记进 requestLogsViewsWithSessionArm。
//
// 为什么需要这张表：v1 族退役是**终态**，不是"暂时没有数据"。让"又有一张纯 v1
// 视图"重新出现而无门可拦，等于把终态悄悄退回中间态。
var retiredV1OnlyRequestLogsViews = map[string]struct{}{
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
		t.Fatal("未扫到任何 _request_logs% 视图——扫描口径坏了，不是库里没有视图")
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
	// ★ 890：退役名单的**负向**断言。
	//
	// ⚠ 这里必须用 `strpos` 类的**精确**判定，而不是 `LIKE` 配 `_`：
	// SQL 的 LIKE 里 `_` 是**单字符通配符**（审计 §10.8 的坏尺子），
	// 用它判"这个名字出现了没有"会把别的名字误判成命中。
	//
	// 语义：retiredV1OnlyRequestLogsViews 里的名字在真库上**不允许**以
	// "纯 v1"形态存在（要么不存在，要么已被改造成含 session 臂）。
	for name := range retiredV1OnlyRequestLogsViews {
		if _, stillV1 := gotOnly[name]; stillV1 {
			t.Errorf("视图 %s 已在 890 随 v1 族一并退役，却仍以**纯 v1**形态存在 —— "+
				"v1 族退役是终态；要么它不存在，要么它已被改造成含 session 臂", name)
		}
	}
	// 反向：v1OnlyRequestLogsViews 登记的名字必须**真的不存在**（登记的是
	//"现在存在的纯 v1 视图"，而 890 之后这张表应当是空的）。
	for name := range v1OnlyRequestLogsViews {
		if _, exists := gotOnly[name]; !exists {
			t.Errorf("v1OnlyRequestLogsViews 里的 %s 在真库上已不存在（890 已删）—— "+
				"登记表陈旧：它是「现在存在的纯 v1 视图」名单", name)
		}
	}
	t.Logf("真库实测：含 session 臂 %d 个，纯 v1 %d 个，退役名单 %d 个",
		len(gotArm), len(gotOnly), len(retiredV1OnlyRequestLogsViews))
}
