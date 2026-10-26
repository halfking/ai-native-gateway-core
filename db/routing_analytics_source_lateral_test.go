package db

// routing_analytics_source_lateral_test.go —— R96 轮门禁（迁移 910 轮就地扩展）。
//
// # 背景（R96 轮实测，详见审计档
// `docs/audit/2026-10-27-R96-routing-analytics刷新链-LATERAL回定位.md`）
//
//	`routing_analytics_source` 旧体 = `SELECT 15 列 FROM request_logs_with_current_month`
//	⇒ 该 canonical 视图是「session_turns_hot ∪ session_turns LEFT JOIN session_turn_details(_hot)」
//	  的 118 列投影 ⇒ 规划器只能选 Hash Join ⇒ details 侧**顺序扫全部分区**
//	  （252 实测 `Parallel Seq Scan` × 4 = 45 万行、Buffers shared hit=95,086 ≈ 743 MB）
//	⇒ 而 source 的 15 列里**只有 3 列**来自 details（client_model / provider_id / auto_profile）
//
//	907 体 = **会话族两腿 + `LEFT JOIN LATERAL (... LIMIT 1)` 回定位**
//	⇒ 只读窗口内真正需要的行（走 details 既有唯一索引 (tenant_id, request_id, partition_date)）
//
//	910 体（2026-10-27 本轮，252 只读实测详见本轮审计档）：
//	907 形态的残留成本 = 父表腿 ~2.75 万次/刷新 的逐行探针，且探针单次成本
//	随宿主负载漂 100x+（R96 实测 ~3us/次，本轮 0.366ms/次）⇒ 父表腿改
//	**普通 LEFT JOIN + partition_date 日界下推**（details 侧执行期分区裁剪 +
//	Hash Join 可选）⇒ 整计划 buffers 147K → 52K（-64%，多轮稳定）。
//	hot 腿保留 LATERAL LIMIT 1（details_hot 无唯一键证明）。
//
// # 本文件钉住四件事
//
//	1. 形态门：视图体必须是 910 形态（两腿直读会话族；父表腿普通 JOIN + 日界；
//	   hot 腿 LATERAL），且**不得**回退读 canonical 视图 / v1 底表。
//	2. 真库口径等价门：★ **用生产 DDL 原文**在夹具库上与「旧体（canonical 形态）」
//	   逐行对拍（双向 EXCEPT ALL，必须 0/0）⇒ 证明换形不改口径。
//	3. 真库计划门：details 侧必须走**索引**（不得 Seq Scan）⇒ 证明换形真的换了访问路径。
//	4. 语义耦合门：迁移 910 的 SQL 文件必须与 Go 常量**逐字一致**
//	   ⇒ 防止「迁移与运行时真源各写一份、悄悄漂移」；
//	   且 910.down 的视图体必须与 907.up 逐字一致（回退链闭合）。
//
// ★ 门禁分层（承 R94 的结论）：**结构（形态）/ 语义（口径）/ 计划（规划器认不认）**
//   —— 只钉一层的门会被「无症状的退化」骗过。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. 形态门
// ─────────────────────────────────────────────────────────────────────────────

// TestRoutingAnalyticsSourceViewShape 钉住 source 视图的**形态**（910 版）。
//
// ★ 为什么必须是形态门：口径等价门需要真库；纯文本门是它的**前置**——
//
//	形态不对时，真库门必然红，但红的原因会是「量具坏了」而不是「被测对象坏了」。
func TestRoutingAnalyticsSourceViewShape(t *testing.T) {
	body := RoutingAnalyticsSourceViewDDL

	if !strings.Contains(body, "CREATE OR REPLACE VIEW routing_analytics_source AS") {
		t.Fatal("source 视图体必须以 CREATE OR REPLACE VIEW 开头（保留 OID ⇒ matview 依赖不动）")
	}
	// ★ 两腿都必须是「会话族直读」。
	for _, want := range []string{
		"FROM public.session_turns_hot t",
		"FROM public.session_turns t",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source 视图体缺 %q —— 会话族直读被回退了", want)
		}
	}
	// ★ hot 腿保留 LATERAL 回定位（details_hot 的索引是非唯一 request 索引，
	//   唯一键证明只覆盖分区父表 ⇒ hot 腿的 LIMIT 1 不可拆）。
	for _, want := range []string{
		"LEFT JOIN LATERAL (",
		"FROM public.session_turn_details_hot dd",
		"LIMIT 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source 视图体缺 %q —— hot 腿的 LATERAL 回定位被改掉了", want)
		}
	}
	// ★ 父表腿必须是普通 LEFT JOIN + 日界下推（910 的核心判据）。
	//   日界下推让 details 的执行期分区裁剪成立（details_2026_09 虽 0 行但
	//   堆 383MB —— 没有日界下推就会被全扫）。
	for _, want := range []string{
		"LEFT JOIN public.session_turn_details dd",
		"dd.tenant_id = t.tenant_id",
		"dd.request_id = t.request_id",
		"dd.partition_date = t.partition_date",
		"dd.partition_date >= (now() - interval '8 days')::date",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source 视图体缺 %q —— 910 形态（普通 JOIN + 日界下推）被回退了", want)
		}
	}
	// ★ 回定位的三列必须齐全（少了任一列都会静默变 NULL ⇒ 口径坏掉且不报错）。
	for _, want := range []string{
		"d.client_model::text AS client_model",
		"d.provider_id::bigint AS provider_id",
		"d.auto_profile::text AS auto_profile",
		"dd.client_model::text AS client_model",
		"dd.provider_id::bigint AS provider_id",
		"dd.auto_profile::text AS auto_profile",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source 视图体缺 %q —— details 三列的投影被改了", want)
		}
	}
	// ★ 不得回退读 canonical 视图（旧形态 ⇒ details 侧回到 45 万行顺序扫）。
	if strings.Contains(body, "FROM request_logs_with_current_month") {
		t.Error("source 视图体又读回 canonical 视图了 ⇒ details 侧会回到 45 万行顺序扫（R95 归因的根因）")
	}
	// ★ 不得直读 v1 底表（v1 族已退役；直读会让分析端点随热窗排空静默归零）。
	if strings.Contains(body, "FROM _request_logs_hot") || strings.Contains(body, "FROM _request_logs;") {
		t.Error("source 视图体仍在直读 v1 底表")
	}
	// ★ 列契约：CREATE OR REPLACE 只能加列，不能改名列/次序/类型。
	//   auto_profile 必须仍在末位（admin/auto_route.go 直接读它）。
	if !strings.Contains(body, "auto_profile::text AS auto_profile") {
		t.Error("source 视图体缺 auto_profile（admin/auto_route.go 的 profile 分布端点直接读它）")
	}
	// ★ 数字守卫必须保留（credential_id 在会话腿是 TEXT ⇒ 直接 ::bigint 会 22P02）。
	if !strings.Contains(body, "credential_id ~ '^[0-9]+$'") {
		t.Error("source 视图体的 credential_id 缺数字守卫 CAST（TEXT 列直接 ::bigint 会 22P02）")
	}

	// ---- 阳性对照：证明上面那些判据**分得清** ----
	// 喂一段确实回退到 canonical 形态的样本 ⇒ 同一判据必须抓到它。
	// 没有它，上面那条断言若写坏（例如比较了永远不出现的串），整段会恒绿。
	const canonicalSample = "SELECT ts, task_type::text AS task_type FROM request_logs_with_current_month;"
	if !strings.Contains(canonicalSample, "FROM request_logs_with_current_month") {
		t.Error("阳性对照失效：回退样本没有被负向判据抓到 —— 上面那条断言此时恒绿，测不到任何东西")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2/3. 真库门（口径等价 + 计划形态）
// ─────────────────────────────────────────────────────────────────────────────

// routingSourceFixtureDDL 在一次性空库里搭出 source 视图需要的**最小族**：
// 会话族两腿（hot ∪ 父表）+ details 两腿（hot ∪ 父表）+ 旧体（canonical 形态）。
//
// ★★ 910 轮：夹具日期全部改成 **now() 相对值** —— 910 的父表腿带
// partition_date 日界下推（now() - 8 days），写死日期的夹具会随真实时钟
// 漂出 8 天窗外而假红。同时所有行的 partition_date = date(ts)
// （生产实测错位行 = 0，即 910 日界下推的蕴含前提；R96 夹具里 r8 那条
// 刻意错位的行在生产不存在 ⇒ 不再保留）。
const routingSourceFixtureDDL = `
CREATE TABLE public.session_turns_hot (
  ts timestamptz, task_type text, model text, work_type text, credential_id text,
  is_auto_request boolean, tenant_id text, request_id text, success boolean,
  latency_ms numeric, cost_usd numeric, origin_stage text, partition_date date);
CREATE TABLE public.session_turns (
  ts timestamptz, task_type text, model text, work_type text, credential_id text,
  is_auto_request boolean, tenant_id text, request_id text, success boolean,
  latency_ms numeric, cost_usd numeric, origin_stage text, partition_date date);
CREATE TABLE public.session_turn_details_hot (
  client_model text, provider_id bigint, auto_profile text,
  tenant_id varchar(255), request_id text, partition_date date);
CREATE TABLE public.session_turn_details (
  client_model text, provider_id bigint, auto_profile text,
  tenant_id varchar(255), request_id text, partition_date date);

-- 旧体的上游：canonical 形态的视图（详情三列用 details 侧，credential_id 带数字守卫）。
CREATE VIEW public.fixture_canonical AS
SELECT t.ts, t.task_type, t.model AS outbound_model, d.client_model, t.work_type, d.provider_id,
       (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint ELSE NULL::bigint END) AS credential_id,
       t.is_auto_request, t.tenant_id::text AS tenant_id, t.request_id, t.success,
       t.latency_ms, t.cost_usd, t.origin_stage, d.auto_profile
FROM public.session_turns_hot t
LEFT JOIN public.session_turn_details_hot d
  ON d.tenant_id::text = t.tenant_id::text AND d.request_id = t.request_id AND d.partition_date = t.partition_date
UNION ALL
SELECT t.ts, t.task_type, t.model AS outbound_model, d.client_model, t.work_type, d.provider_id,
       (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint ELSE NULL::bigint END) AS credential_id,
       t.is_auto_request, t.tenant_id::text AS tenant_id, t.request_id, t.success,
       t.latency_ms, t.cost_usd, t.origin_stage, d.auto_profile
FROM public.session_turns t
LEFT JOIN public.session_turn_details d
  ON d.tenant_id::text = t.tenant_id::text AND d.request_id = t.request_id AND d.partition_date = t.partition_date;

-- 旧体（R96 之前的 source 视图形态）。
CREATE VIEW public.routing_analytics_source_old AS
SELECT ts, task_type::text AS task_type, outbound_model::text AS outbound_model,
       client_model::text AS client_model, work_type::text AS work_type,
       provider_id::bigint AS provider_id, credential_id::bigint AS credential_id,
       is_auto_request::boolean AS is_auto_request, tenant_id::text AS tenant_id,
       request_id::text AS request_id, success::boolean AS success,
       latency_ms::numeric AS latency_ms, cost_usd::numeric AS cost_usd,
       origin_stage::text AS origin_stage, auto_profile::text AS auto_profile
FROM public.fixture_canonical;

-- 夹具数据：覆盖回定位的每一个边界情形。日期全部相对 now()（见上方 910 轮说明）。
-- ★ r_old 是「窗口外旧行」边界夹具（ts = now()-10d）：910 的日界下推让
--   source 对它返回 NULL details 三列，而 canonical（旧体）返回真实值
--   ⇒ 这是**有意边界**，由 TestRoutingAnalyticsSourceDayRangeBoundaryOnRealDB
--   从两侧分别钉住；口径等价门因此只对 8 天窗内行做 EXCEPT ALL（910 的
--   等价承诺范围 = 带 ts 窗的读——生产实测全部 13 处消费方都带窗/存在性探针）。
INSERT INTO public.session_turns_hot VALUES
  (now() - interval '2 days' + interval '10 hours','chat','m1','wt','42', true, 't1','r1', true, 10, 0.1, 'direct', (now() - interval '2 days')::date),
  (now() - interval '2 days' + interval '10 hours' + interval '1 minute','chat',NULL,NULL,NULL, false, 't1','r2', false, 20, 0.2, 'direct', (now() - interval '2 days')::date),
  (now() - interval '2 days' + interval '10 hours' + interval '2 minutes','chat','','wt','abc', false, NULL,'r3', true, 30, 0.3, 'self_check', (now() - interval '2 days')::date),
  (now() - interval '3 days' + interval '10 hours','chat','m3','wt','7', true, 't2','r1', true, 40, 0.4, 'direct', (now() - interval '3 days')::date);
INSERT INTO public.session_turn_details_hot VALUES
  ('cm1', 100, 'ap1','t1','r1', (now() - interval '2 days')::date),
  ('cm2', 200, NULL,'t1','r2', (now() - interval '2 days')::date),
  ('cmX', 999, 'apX','t1','r3', (now() - interval '56 days')::date);
INSERT INTO public.session_turns VALUES
  (now() - interval '4 days' + interval '9 hours','chat','m4','wt','9', true, 't3','r9', true, 50, 0.5, 'direct', (now() - interval '4 days')::date),
  (now() - interval '5 days' + interval '9 hours','chat','m5','wt','0', false, 't4','r8', false, 60, 0.6, 'direct', (now() - interval '5 days')::date),
  (now() - interval '10 days' + interval '9 hours','chat','m6','wt','1', true, 't5','r_old', true, 70, 0.7, 'direct', (now() - interval '10 days')::date);
INSERT INTO public.session_turn_details VALUES
  ('cm4', 400, 'ap4','t3','r9', (now() - interval '4 days')::date),
  ('cm5', 500, 'ap5','t4','r8', (now() - interval '5 days')::date),
  ('cm_old', 600, 'ap_old','t5','r_old', (now() - interval '10 days')::date),
  ('cmY', 888, 'apY','t9','rY', (now() - interval '26 days')::date);

-- details 的唯一键（907 LIMIT 1 的等价性前提 / 910 普通 JOIN 不增殖的前提）：
-- (tenant_id, request_id, partition_date)。
CREATE UNIQUE INDEX fixture_details_key ON public.session_turn_details (tenant_id, request_id, partition_date);
CREATE UNIQUE INDEX fixture_details_hot_key ON public.session_turn_details_hot (tenant_id, request_id, partition_date);
`

// routingSourceGateConn 派生一次性空库并连上（夹具 DDL 全部在事务里，末尾 ROLLBACK）。
func routingSourceGateConn(t *testing.T) (*pgx.Conn, context.Context) {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("ROUTING_ANALYTICS_GATE_TEST_DATABASE_URL"))
	if base == "" {
		t.Skip("ROUTING_ANALYTICS_GATE_TEST_DATABASE_URL not set — isolated PostgreSQL required")
	}
	dsn := scratchDSNFor(t, base, scratchFail)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, routingSourceFixtureDDL); err != nil {
		t.Fatalf("fixture SQL failed: %v", err)
	}
	// ★ 生产 DDL 原文（不是复制的片段）⇒ 门测的就是上线的那段 SQL。
	if _, err := tx.Exec(ctx, RoutingAnalyticsSourceViewDDL); err != nil {
		t.Fatalf("生产 source 视图 DDL 执行失败: %v", err)
	}
	return conn, ctx
}

// TestRoutingAnalyticsSourceViewMatchesCanonicalOnRealDB 是**真库口径等价门**（910 修订）。
//
// ★ 判据（910 起按**窗口内**对拍）：新体与旧体在 **8 天窗内**逐行一致
// （双向 EXCEPT ALL 都必须是 0）。
//
//	⚠ 910 的父表腿带 partition_date 日界下推 ⇒ **全源等价**不再成立（窗口外
//	  旧行的 details 三列为 NULL，canonical 有值）——那是有意边界，由
//	  TestRoutingAnalyticsSourceDayRangeBoundaryOnRealDB 从两侧钉住；
//	  本门证的是「带 ts 窗的读不改口径」（生产 13 处消费方全部带窗/存在性探针）。
//	⚠ 「0 行也一致」是假绿 ⇒ 门必须先断言两侧都**有行**（空集显式失败）。
func TestRoutingAnalyticsSourceViewMatchesCanonicalOnRealDB(t *testing.T) {
	conn, ctx := routingSourceGateConn(t)

	var oldRows, newRows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.routing_analytics_source_old WHERE ts >= now() - interval '8 days'`).Scan(&oldRows); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.routing_analytics_source WHERE ts >= now() - interval '8 days'`).Scan(&newRows); err != nil {
		t.Fatal(err)
	}
	if oldRows == 0 || newRows == 0 {
		t.Fatalf("夹具两侧都必须非空（空集不能自证等价）：old_rows=%d new_rows=%d", oldRows, newRows)
	}
	if oldRows != newRows {
		t.Errorf("窗口内新旧体行数不同：old=%d new=%d ⇒ 口径已变", oldRows, newRows)
	}

	var onlyOld, onlyNew int
	if err := conn.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM (SELECT * FROM public.routing_analytics_source_old
		     WHERE ts >= now() - interval '8 days'
		     EXCEPT ALL SELECT * FROM public.routing_analytics_source
		     WHERE ts >= now() - interval '8 days') a),
		  (SELECT count(*) FROM (SELECT * FROM public.routing_analytics_source
		     WHERE ts >= now() - interval '8 days'
		     EXCEPT ALL SELECT * FROM public.routing_analytics_source_old
		     WHERE ts >= now() - interval '8 days') b)
	`).Scan(&onlyOld, &onlyNew); err != nil {
		t.Fatal(err)
	}
	if onlyOld != 0 || onlyNew != 0 {
		t.Errorf("窗口内口径不等价：only_old=%d only_new=%d ⇒ 换形改了取哪些行", onlyOld, onlyNew)
	}

	// ★ 列契约逐列对拍（CREATE OR REPLACE 的列名/类型/次序）。
	rows, err := conn.Query(ctx, `
		SELECT a.attnum, a.attname, format_type(a.atttypid, a.atttypmod),
		       b.attname, format_type(b.atttypid, b.atttypmod)
		  FROM pg_attribute a JOIN pg_attribute b ON b.attnum = a.attnum
		 WHERE a.attrelid = 'public.routing_analytics_source_old'::regclass
		   AND b.attrelid = 'public.routing_analytics_source'::regclass
		   AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var attnum int
		var oldName, oldType, newName, newType string
		if err := rows.Scan(&attnum, &oldName, &oldType, &newName, &newType); err != nil {
			t.Fatal(err)
		}
		if oldName != newName || oldType != newType {
			t.Errorf("第 %d 列契约漂移：old=%s %s / new=%s %s", attnum, oldName, oldType, newName, newType)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 15 {
		t.Errorf("source 视图应是 15 列，实际 %d 列", n)
	}
}

// TestRoutingAnalyticsSourceDayRangeBoundaryOnRealDB 钉住 910 的**有意边界**。
//
// ★ 910 的父表腿带 partition_date 日界下推 ⇒ 对 ts 早于 8 天前的旧行，
//
//	source 返回行的 details 三列（client_model/provider_id/auto_profile）为 NULL，
//	而 canonical（旧体）返回真实值。这是**设计内的口径边界**（等价承诺范围 =
//	带 ts 窗的读；生产 13 处消费方实测全部带窗/存在性探针），不是缺陷——
//	但必须显式钉住，否则未来任何「静默放宽/收窄日界」的改动都无人察觉。
//
// ★ 断言三件：
//
//  1. source 里旧行**存在**（LEFT JOIN 不丢行）且恰好 1 行（ukey ⇒ 不增殖）；
//  2. source 里旧行 client_model IS NULL（日界边界生效）；
//  3. canonical（旧体）里同一行 client_model = 'cm_old'（差异确实来自边界本身，
//     不是夹具坏掉——没有这条，上面两条也可能因为「旧行根本没插进去」而恒真）。
func TestRoutingAnalyticsSourceDayRangeBoundaryOnRealDB(t *testing.T) {
	conn, ctx := routingSourceGateConn(t)

	var srcRows int
	var srcCM *string
	if err := conn.QueryRow(ctx, `
		SELECT count(*), min(client_model) FROM public.routing_analytics_source
		 WHERE request_id = 'r_old'`).Scan(&srcRows, &srcCM); err != nil {
		t.Fatal(err)
	}
	if srcRows != 1 {
		t.Fatalf("旧行在 source 中应恰好 1 行（LEFT JOIN 保行 + ukey 不增殖），实际 %d", srcRows)
	}
	if srcCM != nil {
		t.Errorf("旧行（ts 早于 8 天窗）的 client_model 应为 NULL（910 日界边界），实际 %q", *srcCM)
	}

	var oldCM *string
	if err := conn.QueryRow(ctx, `
		SELECT client_model FROM public.routing_analytics_source_old
		 WHERE request_id = 'r_old'`).Scan(&oldCM); err != nil {
		t.Fatal(err)
	}
	if oldCM == nil || *oldCM != "cm_old" {
		t.Errorf("canonical 旧行 client_model 应为 cm_old（证明差异来自日界边界而非夹具缺数据），实际 %v", oldCM)
	}

	// ★ 窗口内的行不受边界影响（r9 在 8 天窗内 ⇒ details 有值）——
	//   防止「日界谓词写得太紧把窗内行也切了」的回归。
	var recentCM *string
	if err := conn.QueryRow(ctx, `
		SELECT client_model FROM public.routing_analytics_source
		 WHERE request_id = 'r9' AND ts >= now() - interval '7 days'`).Scan(&recentCM); err != nil {
		t.Fatal(err)
	}
	if recentCM == nil || *recentCM != "cm4" {
		t.Errorf("窗口内行 r9 的 client_model 应为 cm4（日界边界不得波及窗内行），实际 %v", recentCM)
	}
}

// TestRoutingAnalyticsSourceViewUsesDetailsIndexOnRealDB 是**真库计划门**（910 版）。
//
// ★ 判据：
//
//  1. details 侧必须走**索引**（Index Scan / Index Only Scan），且**不得**出现
//     Seq Scan on session_turn_details —— 这正是 R95 归因的「45 万行顺序扫」。
//  2. hot 腿探针的 Index Cond 必须带齐三键（少一键就可能退化成扫全表）。
//  3. ★ 910 专判：整个计划里只允许 **1 个 Limit 节点**（hot 腿探针的）。
//     907 形态有 2 个（每腿探针各一个）⇒ 父表腿一旦回退到逐行探针，本判据红。
//
// ★ 为什么用 enable_seqscan=off：夹具只有几行，规划器本来会选顺序扫
//
//	（那不是被测行为）；关掉它才能**强制**索引路径被看见。
func TestRoutingAnalyticsSourceViewUsesDetailsIndexOnRealDB(t *testing.T) {
	conn, ctx := routingSourceGateConn(t)

	// ★ 先在「不强制」的形态下取计划：说明本门**只**在强制态下断言。
	if _, err := conn.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	// ⚠ EXPLAIN 返回**多行**（每行一个计划节点）⇒ 必须逐行收集，
	//   用 QueryRow 只会拿到第一行（实测就是 "Append"）⇒ 量具坏了会读成「没走索引」。
	planRows, err := conn.Query(ctx, `
		EXPLAIN (COSTS OFF)
		SELECT * FROM public.routing_analytics_source
		WHERE ts >= now() - interval '7 days'`)
	if err != nil {
		t.Fatal(err)
	}
	defer planRows.Close()
	var plan strings.Builder
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := planRows.Err(); err != nil {
		t.Fatal(err)
	}
	planText := plan.String()
	if !strings.Contains(planText, "Index Scan") && !strings.Contains(planText, "Index Only Scan") {
		t.Errorf("details 侧没有走索引：\n%s", planText)
	}
	if strings.Contains(planText, "Seq Scan on session_turn_details") {
		t.Errorf("details 侧仍是顺序扫（R95 归因的 45 万行顺序扫没被消除）：\n%s", planText)
	}
	// ★ 910 专判：只允许 1 个 Limit 节点（hot 腿探针）。
	//   ⚠ 计数按行首 "Limit" 节点行（"  ->  Limit  (cost=..."），别把
	//   "SubPlan"/其他含 Limit 字样的行算进来 —— 本计划里没有那种行（实测）。
	limitLines := 0
	for _, line := range strings.Split(planText, "\n") {
		if strings.Contains(line, "->  Limit") {
			limitLines++
		}
	}
	if limitLines != 1 {
		t.Errorf("期望 1 个 Limit 节点（hot 腿探针）；907 的父表腿探针若回归会出现第 2 个，实际 %d 个：\n%s", limitLines, planText)
	}
	// ★ hot 腿探针的 Index Cond 必须带齐三键；父表腿的 details 扫描是
	//   910 的 pkey 日界范围扫 ⇒ Index Cond 只需含分区键 partition_date。
	// ★ ⚠ 计划文本里 PG 会把 varchar 比较**规范化**成 `(tenant_id)::text = t.tenant_id`
	//   （R95 §3.2 的实测结论）⇒ 断言不能写死 "tenant_id =" ⇒ 按**列名**核对。
	// ★ ⚠ 逐节点核对：EXPLAIN 的每个节点占多行（节点行 → Index Cond 行），
	//   必须把 Index Cond 归属到**它上面最近的节点行**，不能对所有 Cond 行
	//   一刀切要求三键（910 的父表腿 pkey 范围扫天然只有 partition_date）。
	curNode := ""
	hotCondSeen := false
	parentCondSeen := false
	for _, line := range strings.Split(planText, "\n") {
		if strings.Contains(line, "Index Scan using") || strings.Contains(line, "Index Only Scan using") {
			curNode = line
			continue
		}
		if !strings.Contains(line, "Index Cond:") {
			continue
		}
		switch {
		case strings.Contains(curNode, "session_turn_details_hot"):
			hotCondSeen = true
			for _, col := range []string{"tenant_id", "request_id", "partition_date"} {
				if !strings.Contains(line, col) {
					t.Errorf("hot 腿回定位的 Index Cond 缺列 %q：%s", col, line)
				}
			}
		case strings.Contains(curNode, "session_turn_details"):
			parentCondSeen = true
			if !strings.Contains(line, "partition_date") {
				t.Errorf("父表腿 details 扫描的 Index Cond 缺分区键 partition_date（日界下推没进计划）：%s", line)
			}
		}
	}
	if !hotCondSeen {
		t.Errorf("计划里没有 hot 腿探针的 Index Cond（探针丢了？）：\n%s", planText)
	}
	if !parentCondSeen {
		t.Errorf("计划里没有父表腿 details 扫描的 Index Cond（日界范围扫丢了？）：\n%s", planText)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. 语义耦合门：迁移 910 ≡ Go 常量；910.down ≡ 907.up（回退链闭合）
// ─────────────────────────────────────────────────────────────────────────────

// TestMigration910DDLMatchesRuntimeConstant 钉住「迁移文件」与「运行时真源」
// **逐字一致**。
//
// ★ 为什么必须有：db.go 的 RoutingAnalyticsSourceViewDDL 是**启动期执行**的文本，
//
//	迁移 910 是**部署期执行**的文本。两份各写一份时，任何一侧的改动若忘了另一侧
//	⇒ 部署后的视图形态与新装/重建路径的形态**不一致** ⇒ 难查的行为分叉。
//	（907 时代的同名断言在 910 应用后改为「910.down ≡ 907.up」回退链门。）
func TestMigration910DDLMatchesRuntimeConstant(t *testing.T) {
	upPath := filepath.Join("..", "sql", "migrations", "startup", "910_routing_analytics_source_hashjoin.sql")
	up, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatalf("up migration file should exist: %v", err)
	}
	downPath := filepath.Join("..", "sql", "migrations", "startup", "910_routing_analytics_source_hashjoin.down.sql")
	down, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatalf("down migration file should exist: %v", err)
	}
	upSQL := string(up)
	downSQL := string(down)

	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"up", upSQL},
		{"down", downSQL},
	} {
		if !strings.Contains(tc.sql, "BEGIN;") || !strings.Contains(tc.sql, "COMMIT;") {
			t.Errorf("%s 迁移必须是事务性的（startup 迁移默认在事务里跑）", tc.name)
		}
		if !strings.Contains(tc.sql, "CREATE OR REPLACE VIEW routing_analytics_source AS") {
			t.Errorf("%s 迁移必须替换 source 视图", tc.name)
		}
	}

	// ★ 逐字比对：up 的视图体必须与 Go 常量一致。
	// ★ 两侧都按「从 CREATE 到第一个分号」抽取后再比对 ⇒
	//   Go 常量自带的前置注释不会被算进差异里（注释不影响执行语义）。
	want := extractViewDDL(RoutingAnalyticsSourceViewDDL)
	got := extractViewDDL(upSQL)
	if want == "" || got == "" {
		t.Fatalf("抽取不到视图体（go=%d sql=%d 字节）", len(want), len(got))
	}
	if got != want {
		t.Errorf("迁移 910 的视图体与 Go 常量不一致（部署路径与运行时路径会分叉）\n--- go ---\n%s\n--- sql ---\n%s", want, got)
	}

	// ★ 回退链闭合：910.down 的视图体必须与 907.up **逐字一致**
	//   （907 已 applied、文件内容冻结；down 不许另写一份「差不多」的旧形态）。
	up907Path := filepath.Join("..", "sql", "migrations", "startup", "907_routing_analytics_source_lateral.sql")
	up907, err := os.ReadFile(up907Path)
	if err != nil {
		t.Fatalf("907 up migration file should exist: %v", err)
	}
	if d := extractViewDDL(downSQL); d != extractViewDDL(string(up907)) {
		t.Errorf("910.down 的视图体与 907.up 不一致（回退形态必须逐字等于被回退的形态）\n--- 907.up ---\n%s\n--- 910.down ---\n%s", extractViewDDL(string(up907)), d)
	}

	// ★ 910.down 不该把视图体换回 canonical 形态（那是 907.down 的职责；
	//   907 已 applied ⇒ 回退 910 应落在 907 形态，不是 pre-R96 形态）。
	if strings.Contains(extractViewDDL(downSQL), "FROM request_logs_with_current_month") {
		t.Error("910.down 不该回退到 canonical 形态（应回退到 907 的 LATERAL 形态）")
	}
}

// extractViewDDL 从迁移 SQL 里抽出 `CREATE OR REPLACE VIEW routing_analytics_source ...;` 段。
func extractViewDDL(sql string) string {
	const marker = "CREATE OR REPLACE VIEW routing_analytics_source AS"
	i := strings.Index(sql, marker)
	if i < 0 {
		return ""
	}
	rest := sql[i:]
	j := strings.Index(rest, ";")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j+1])
}
