package main

// ExecuteRepair 端到端真库守卫（2026-10-05，§9.186）。
//
// # 这道门补的是哪条边界
//
// §9.183 修了 `ExecuteRepair` 只删 `session_bodies`（分区父表）、漏
// `session_bodies_hot` 的缺陷，并配了**静态源码**判据
//（repair_two_surface_test.go）。那道门守「这段 SQL 被写出来了」，
// **不守**「运行时真的删干净了」——§9.183 当时明确把这条边界留在外面。
// §9.184 试图跨它，撞上本机库的 catalog 异常（`v1BodyQuery` 恒失败），
// **两次都没跨过**。本门在**一套 schema 相同的新库**上跨过去了（§9.186）。
//
// # 夹具为什么**没有**沿用本仓的一次性数据库做法
//
// `domains/session/v2/session_request_status_backfill_test.go` 里早就有
// `statusBackfillFixtureDB`（建 throwaway database 跑逐字相同的生产 SQL），
// 且它的注释**已经记着我下面那个 `defer` / `t.Cleanup` 坑**
// （「a trap this project has already hit once」）。
// **那正是本门踩的坑之一**——先手工搭夹具、没先找现成 helper，教训见审计 §9.187.4。
//
// 本门**仍然**在共享库上跑、靠清理兜底，理由只有一个：
// `ExecuteRepair` 需要**生产那套完整 schema**——`request_logs`、
// `session_turns{,_hot}`、`session_bodies{,_hot}`、`sessions`、`session_turn_logs`、
// 两个合并视图（`session_bodies_unified` / `session_turns_with_current_month`）、
// 以及 `session_turns_advisory_lock_key` 函数。塞不进「最小内联 DDL」的形状。
// 对照：`domains/session/v2` 的 claim 门只要两张表，**已经**改用一次性数据库，
// 残留从「靠清理」变成「结构上不可能」（审计 §9.187）。
//
// ⇒ 若将来本门要求的表变少，**应当改造成一次性数据库**，而不是继续依赖清理。
//
// # 为什么必须有前置探针，而不是直接跑
//
// 本门依赖 `LoadV1Turns` → `v1BodyQuery`（`loader.go:115`）读 V1 源。
// 在 §9.184 认定有异常的那套库上，这一步必然失败。
//
// ⇒ 跑之前**先单独执行一次 `v1BodyQuery` 的形状**：
//   - 跑得通 ⇒ 继续本门；
//   - 跑不通 ⇒ `t.Skip`，并**指名**这是哪一类故障、指向哪条会报警的门。
//
// **这不是「把红变绿」。** 同一个条件下
// `admin/session_family_surface_readable_realdb_test.go`（§9.184）会**报红**——
// 两道门是**分工**不是重复：那条负责**让环境缺陷持续可见**，本条负责
// **在库可用时把修复验实**。若哪天有人删掉 §9.184 那条，本门的 Skip 就变成
// 了静默通过——**这一点写在这里，就是为了让那种联动被看见。**
//
// # 夹具：两个面各一行，且先断阳性对照
//
// 修复前必须先断言「父表 1 行 + hot 1 行」都真的存在。
// 否则「hot 侧删干净了」在单面夹具下是**恒真断言**，门看起来绿而什么也没验。
// §9.183 记过一次同族的坑（静态门看不到运行时），本门从一开始就带上这个对照。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// countFixtureRows 数一张表里属于本夹具会话的行数。表名取自本文件常量。
func countFixtureRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, tenant, session string) int {
	t.Helper()
	var n int
	q := fmt.Sprintf("SELECT count(*) FROM %s WHERE tenant_id = $1 AND session_id = $2", table)
	if err := pool.QueryRow(ctx, q, tenant, session).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestExecuteRepair_RealDB_BodiesLeaveNoRowOnEitherSurface(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 端到端门未运行（非通过）")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// ---- 前置探针：这套库能不能读 V1 源（v1BodyQuery 的形状）----
	//
	// 跑不通就指名跳到 §9.184 那条会报红的门。
	//
	// ⚠ 2026-10-04（审计 §9.196）：本探针原先逐字复制了**旧**形状
	//（「子查询内 UNION ALL」），而那条形状在 bodies 分区是 Citus `columnar` 时
	// 必然失败。**修好 loader.go 却留着旧探针，等于让修复被自己的测试遮住**
	// ——这正是本探针存在的目的（它本该是 loader 的可执行前提）。
	// 现改为**直接引用 loader 的两个常量**，从根上杜绝再次漂移：
	// 探针与被检验对象**同源**，形状一改两边一起改。
	//
	// 判定口径也变了：两条腿都跑一遍，**两条都**因非「no rows」失败才算环境坏。
	// 只跑 hot 腿会漏判「hot 通、母表不通」——而母表才是 columnar 的那一张。
	preErrs := []string{}
	for _, q := range []string{v1BodyQuery, v1BodyQueryParent} {
		if err := pool.QueryRow(ctx, q, "zz-preflight-no-such-id", time.Now().UTC()).
			Scan(new([]byte), new([]byte)); err != nil && !strings.Contains(err.Error(), "no rows") {
			preErrs = append(preErrs, err.Error())
		}
	}
	if len(preErrs) > 0 {
		t.Skipf("这套库读不了 V1 body 存储（%s）⇒ ExecuteRepair 在其上不可能执行。"+
			"\n  本门在此跳过**不是通过**。同一条件下"+
			"\n  admin.TestSessionFamilyTwoSurfaceUnionShapeIsExecutable 会**报红**"+
			"（§9.184），环境缺陷在那里持续可见。\n"+
			"  参见审计 §9.196：成因是 request_logs_bodies 的 RANGE 分区被 migration 765"+
			"\n  转成了 Citus `columnar`，与 D25-a「重建库」的结论**相反**——重建不会让它复发。",
			strings.Join(preErrs, " | "))
	}

	// 合成身份：绝不复用任何真实租户/会话，清理只按这两个值删。
	suffix := time.Now().UTC().Format("20060102t150405.000000")
	tenant := "zz-repair-e2e-" + suffix
	session := "zz-repair-e2e-sess-" + suffix
	ts := time.Now().UTC().Truncate(time.Second)

	t.Cleanup(func() {
		cctx := context.Background()
		// 清理**不吞错误**：清不掉就报红。一个污染测试库的测试，
		// 哪怕断言全绿也是负资产。
		//
		// 表清单里**不含** `request_logs_bodies{,_hot}`：这两张表
		// **没有 tenant_id 列**（第一版把它们列进来，清理直接报
		// "column tenant_id does not exist"，同样被 `_, _ =` 吞掉）。
		// 本测试不往那两张表写数据，故不列；真要写，得按 request_id 删。
		for _, tbl := range []string{
			"public.sessions", "public.session_bodies", "public.session_bodies_hot",
			"public.session_turns", "public.session_turns_hot", "public.session_turn_logs",
			"public.request_logs",
		} {
			if _, err := pool.Exec(cctx,
				fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", tbl), tenant); err != nil {
				t.Errorf("清理 %s 失败：%v —— 夹具将留在库里，后续运行的阳性对照会失真", tbl, err)
			}
		}
		// 池必须在清理**之后**才关：第一版写的是 `defer pool.Close()`，
		// 而 Go 里 defer 在函数体返回时执行、**早于** t.Cleanup 回调
		// ⇒ 清理时池已关闭，每条 DELETE 全部失败；我又用 `_, _ =` 吞掉
		// ⇒ **测试绿着把夹具留在库里**（实测残留 request_logs=6 / bodies=6 /
		// bodies_hot=1 / sessions=3 / turns=6，正好是三次运行的量）。
		// 与 §9.184.4 同族：**失败被静默吞掉 ⇒ 门看起来是好的。**
		pool.Close()
	})

	// ---- 夹具 1：V1 源（ExecuteRepair 的重建来源）----
	// provider_id / credential_id 在 request_logs 里是 **bigint**
	//（loader 里 `provider_id::text` 那个转换就是证据）——第一版夹具给了
	// 字符串，PG 直接以 22P02 拒掉。**那是夹具写错，不是被测对象的问题。**
	for i, rid := range []string{"rid-a-" + suffix, "rid-b-" + suffix} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.request_logs
				(request_id, ts, tenant_id, gw_session_id, success,
				 client_model, provider_id, credential_id, prompt_tokens, completion_tokens, cost_usd)
			VALUES ($1, $2, $3, $4, true, 'm-probe', 900001, 900002, 10, 20, 0.001)`,
			rid, ts.Add(time.Duration(i)*time.Second), tenant, session); err != nil {
			t.Fatalf("seed request_logs[%d]: %v", i, err)
		}
	}

	// ---- 夹具 2：待修复的 V2 状态，**两个面各一行** ----
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.session_bodies
			(session_id, turn_no, tenant_id, request_id, ts, request_delta, response_delta)
		VALUES ($1, 1, $2, $3, $4, '{"m":[{"o":"user","c":"parent"}]}'::jsonb, '{}'::jsonb)`,
		session, tenant, "rid-a-"+suffix, ts); err != nil {
		t.Fatalf("seed session_bodies(parent): %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.session_bodies_hot
			(session_id, turn_no, tenant_id, request_id, ts, request_delta, response_delta)
		VALUES ($1, 2, $2, $3, $4, '{"m":[{"o":"user","c":"hot"}]}'::jsonb, '{}'::jsonb)`,
		session, tenant, "rid-b-"+suffix, ts.Add(time.Second)); err != nil {
		t.Fatalf("seed session_bodies_hot: %v", err)
	}

	// ---- 阳性对照：先证明夹具真的两面都有行 ----
	if n := countFixtureRows(t, ctx, pool, "public.session_bodies", tenant, session); n != 1 {
		t.Fatalf("阳性对照失败：父表应恰有 1 行，实测 %d —— 夹具没搭起来，下面所有断言都不作数", n)
	}
	if n := countFixtureRows(t, ctx, pool, "public.session_bodies_hot", tenant, session); n != 1 {
		t.Fatalf("阳性对照失败：hot 应恰有 1 行，实测 %d —— 本门要的正是「hot 侧非空」这个前提，"+
			"单面夹具会让『hot 删干净』变成恒真断言（§9.186）", n)
	}

	// ---- 执行修复 ----
	r := NewSessionRepairer(pool, NewSessionLoader(pool), nil, nil, nil)
	res, err := r.ExecuteRepair(ctx, tenant, session)
	if err != nil {
		t.Fatalf("ExecuteRepair: %v (result=%+v)", err, res)
	}

	// ---- 断言 1：计数跨两面，与 PlanRepair（走合并视图、数两面）同口径 ----
	if got := res.DeletedRows["session_bodies"]; got != 2 {
		t.Errorf("DeletedRows[\"session_bodies\"] = %d，应为 2（父表 1 + hot 1）—— "+
			"§9.183 的 hot 腿要么没执行，要么只删了部分行", got)
	}

	// ---- 断言 2：hot 面必须归零 ----
	if n := countFixtureRows(t, ctx, pool, "public.session_bodies_hot", tenant, session); n != 0 {
		t.Errorf("修复后 session_bodies_hot 仍有 %d 行 —— 这正是 §9.183 的缺陷："+
			"残留行 + 重建写进父表 ⇒ 经 session_bodies_unified 读出来是重复的", n)
	}

	// ---- 断言 3：合并视图中每个 turn 恰一行（**身份**，不只是数量）----
	// 只断「共 2 行」会被「2 份 turn_no=1」满足——数量能被交换满足，身份不能。
	var total, distinctTurns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT turn_no)
		FROM public.session_bodies_unified
		WHERE tenant_id = $1 AND session_id = $2`, tenant, session).
		Scan(&total, &distinctTurns); err != nil {
		t.Fatalf("query session_bodies_unified: %v", err)
	}
	if total != 2 || distinctTurns != 2 {
		t.Errorf("合并视图实测 %d 行 / %d 个不同 turn_no，应为 2 / 2 —— "+
			"重建应按 V1 源的两个 turn 各产出一行；有重复则本门的修复无效", total, distinctTurns)
	}
}
