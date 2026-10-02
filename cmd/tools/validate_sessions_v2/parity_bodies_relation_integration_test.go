//go:build integration

package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestParityGateBodiesRelationExists（§9.31，2026-10-02）
//
// 这道门存在的原因：**源码门看不见运行时形状**，而这一次缺陷恰恰是运行时的。
//
// `cmd/tools/validate_sessions_v2/loader.go` 此前把
// `public.session_bodies_with_current_month` 当成 canonical body 视图。
// 该名字在全仓**只作为 UNIQUE 约束名**出现在迁移 614/645，
// **从没有任何 CREATE VIEW**；真库 pg_class 核实它的 relkind 是 'i'（索引/约束），
// 而真正的合并视图是 `session_bodies_unified`（relkind = 'v'）。
//
// ⇒ parity 门每次运行都 `relation ... does not exist`，**一直在产出零证据**。
// 而 `loader_test.go` 那个源码契约串**照样是绿的**——它只证明「源码里写着这个名字」，
// 不证明「这个名字在库里是个能查的东西」。**那是一道喂饱的假保证。**
//
// 这道门跑真实的 parity 装载查询形状：关系不存在 ⇒ 立刻失败。
// 跑法：
//
//	TEST_PG_URL='postgres://llm_gateway:***@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./cmd/tools/validate_sessions_v2/ -run TestParityGateBodiesRelationExists -count=1
func TestParityGateBodiesRelationExists(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset —— 跳过**不构成**「parity 门的 bodies 关系存在」的证据")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 0) 门必须**读 loader 的真常量**，不能自己硬编码一个关系名。
	//
	//    第一版把 "session_bodies_unified" 直接写在门里 —— 变异验证立刻暴露：
	//    把 loader.go 的 CanonicalV2BodiesView 改回那个不存在的名字，**门照样绿**。
	//    那又是一道**喂饱的假保证**：它证明「session_bodies_unified 存在」，
	//    而门要回答的是「parity 门用的那个关系存不存在」——两者不是同一个问题。
	//    同包的代价是零，收益是不可能再漂移。
	rel := strings.TrimPrefix(strings.TrimSpace(CanonicalV2BodiesView), "public.")
	if rel == "" {
		t.Fatal("CanonicalV2BodiesView 为空")
	}
	if rel == "session_bodies_with_current_month" {
		t.Fatal("CanonicalV2BodiesView 又指回 session_bodies_with_current_month ——\n" +
			"该关系在真库 relkind='i'（UNIQUE 约束/索引，见迁移 614/645），**不是可查关系**，\n" +
			"parity 门会 `relation does not exist` ⇒ 一直在产出零证据。")
	}

	// 1) 关系必须存在且是**可查的表或视图**（不是索引/约束）。
	//    relkind: r=表 v=视图 m=物化视图 p=分区父表
	//    'i' 是索引、'I' 是分区索引 —— 正是本缺陷的形态。
	var relkind string
	err = pool.QueryRow(ctx,
		`SELECT c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relname = $1`, rel).Scan(&relkind)
	if err != nil {
		t.Fatalf("CanonicalV2BodiesView 指向的 %q 在真库不存在: %v\n"+
			"parity 门会 `relation does not exist` ⇒ 零证据。", rel, err)
	}
	switch relkind {
	case "r", "v", "m", "p":
	default:
		t.Fatalf("%s 的 relkind = %q，**不是可查关系**。\n"+
			"'i'/'I' 是索引/分区索引——这正是「约束名冒充视图名」的形态。", rel, relkind)
	}

	// 2) 装载查询要求的十个列必须齐备。少一列 ⇒ 工具在运行时报 undefined_column，
	//    而那和 relation-not-found 一样是「零证据」，不是「门通过」。
	for _, col := range []string{
		"session_id", "turn_no", "tenant_id", "request_id", "ts",
		"request_delta", "response_delta", "outbound_body",
		"request_attachments", "response_attachments",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_schema='public' AND table_name=$1
			                  AND column_name=$2)`, rel, col).Scan(&exists); err != nil {
			t.Fatalf("check column %s: %v", col, err)
		}
		if !exists {
			t.Errorf("%s 缺列 %q —— parity 装载查询会 undefined_column，零证据", rel, col)
		}
	}

	// 3) 真正跑一次装载查询的形状（用真库里的真实 tenant/session，不用编的参数）。
	//    只验证「能跑 + 列形状对」，不验证内容对错——内容对错由 parity 比对负责。
	var one, two string
	err = pool.QueryRow(ctx,
		`SELECT tenant_id, session_id FROM session_turns
		 WHERE session_id IS NOT NULL ORDER BY ts DESC LIMIT 1`).Scan(&one, &two)
	if err != nil {
		t.Skipf("库里没有可用的 session_turns 样本（%v）——本门不构成证据", err)
	}
	var n int
	err = pool.QueryRow(ctx,
		`SELECT count(*)::int
		 FROM public.session_bodies_unified b
		 WHERE b.tenant_id = $1 AND b.session_id = $2
		   AND EXISTS (SELECT 1 FROM public.session_turns_with_current_month t
		               WHERE t.tenant_id = b.tenant_id AND t.request_id = b.request_id)`,
		one, two).Scan(&n)
	if err != nil {
		t.Fatalf("parity 装载查询在真库上执行失败: %v\n"+
			"这正是本次缺陷的表现形式——**门跑不起来时不会红，只会没有输出**。", err)
	}
	t.Logf("parity 装载查询执行成功：tenant=%s session=%s 命中 %d 行", one, two, n)
}
