package sales

// service_realdb_test.go —— 报价 MVP 的真库行为判据（2026-11-05 W1 验收门）。
//
// # 这道门钉什么
//
// 规划（docs/audit/2026-11-04-报价系统MVP-下一步方案与实施规划.md）W1-T1/T2
// 的验收门里，纯单测看不见的部分全部落在这里：
//
//   ① sellable 清单：trusted 门（suspect_symmetric/empty 排除）+ 整组
//      withhold（同名多价）+ suspended 登记排除 + 949 binding 身份 join；
//   ② 版本不可变：业务列 UPDATE 被 DB 触发器拒绝、superseded_by 可回填、
//      直删拒绝、头级联放行（945 触发器在真库里的实际行为）；
//   ③ 状态机：auto-approve（三维不触发）/ pending_approval（触发）/
//      decide / send / close（收口必须带原因）/ expired 派生态；
//   ④ 分享公开面：token 往返 + **响应体断言**（成本/毛利/供应商身份
//      物理不在 JSON 里）+ 过期/撤销 ⇒ 不可读 + 访问审计落 detail。
//
// # 跑法
//
//	TEST_DATABASE_URL=postgres://... go test ./sales/ -run RealDB -count=1
//
// 未设置该环境变量时整体 SKIP（仓内真库判据同款）。夹具的迁移与种子全部
// public. 限定（迁移字节即被测物，search_path 隔离对它们无效）——因此
// **必须指向专用 scratch 库**（salesFixture 有 fail-closed 守卫），清理由
// scratch 库内进行；共享/业务库会被守卫直接拒绝。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

func realdbDSN(t *testing.T) string {
	t.Helper()
	// A4（2026-11-05 审计）：只认显式 TEST_DATABASE_URL。此前回退读
	// LLM_GATEWAY_DATABASE_URL/DATABASE_URL —— 那两个是应用自身的 DSN，
	// 静默回落会把夹具（含清理 DELETE）打到业务库上。
	if v := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")); v != "" {
		return v
	}
	t.Skip("TEST_DATABASE_URL not set — real-DB gate skipped (unit gates still run)")
	return ""
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// salesFixture 建出 942–949 全链跑得动的最小生产形态：
//   - 基表走仓 SSOT（schemaobj.Table，不手抄 DDL）；
//   - 迁移 940–949 从仓内文件**原样执行**（迁移字节即被测物）。
func salesFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	// A4（2026-11-05 审计）fail-closed 守卫：本夹具的迁移与种子全部
	// public. 限定，且清理含对 credentials/credential_model_bindings 等
	// 共享表的 DELETE ⇒ 只允许专用 scratch 库（库名含 test/scratch/realdb），
	// 或显式 LLM_GATEWAY_REALDB_ALLOW_ANY_DSN=1 豁免（一次性容器才可用）。
	// ★ P-2（2026-11-06 审计）：守卫必须排在下面 DROP 循环**之前** ——
	// 该循环无条件 DROP 15 张 public 业务表（CASCADE），守卫若在其后，
	// DSN 误指业务库时表已先被删，t.Fatalf 为时已晚。
	var dbName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	low := strings.ToLower(dbName)
	if os.Getenv("LLM_GATEWAY_REALDB_ALLOW_ANY_DSN") != "1" &&
		!strings.Contains(low, "test") && !strings.Contains(low, "scratch") && !strings.Contains(low, "realdb") {
		t.Fatalf("TEST_DATABASE_URL must point at a dedicated scratch database (got %q) — "+
			"this fixture drops/truncates shared public tables; override only for "+
			"throwaway instances via LLM_GATEWAY_REALDB_ALLOW_ANY_DSN=1", dbName)
	}

	// 幂等重跑（2026-11-05 真库腿实测）：前一次失败会留下 public 表
	// （本夹具建表不带 IF NOT EXISTS、清理只删行）⇒ 先逆序 DROP 再建，
	// 失败重跑才可继续。CASCADE 顺带走 826/940/949 的从属对象。
	// 视图先于表 DROP（CASCADE 依赖）。
	if _, err := pool.Exec(ctx, `DROP VIEW IF EXISTS public.v_supplier_price_vs_baseline CASCADE`); err != nil {
		t.Fatalf("fixture drop view: %v", err)
	}
	for _, tbl := range []string{
		"maas_settings", "model_baseline_price_reconciliation",
		"sales_quote_shares", "sales_quote_approvals", "sales_quote_items",
		"sales_quote_versions", "sales_quote_status_log", "sales_quotes",
		"sales_demand_cards", "sales_supply_annotations",
		"credential_model_bindings", "provider_models", "credentials",
		"providers", "models_canonical",
	} {
		if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS public.`+tbl+` CASCADE`); err != nil {
			t.Fatalf("fixture drop %s: %v", tbl, err)
		}
	}

	// 隔离 schema：本测试独占，不碰 public。
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS sales_test_iso`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx := context.WithoutCancel(ctx)
		if _, err := pool.Exec(dropCtx, `DROP SCHEMA IF EXISTS sales_test_iso CASCADE`); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `SET search_path TO sales_test_iso, public`); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `SET search_path TO public`)
	}()

	// ① 基表（视图依赖的生产形态）
	ddl := "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n" +
		"CREATE SEQUENCE IF NOT EXISTS public.provider_models_id_seq;\n" +
		"CREATE SEQUENCE IF NOT EXISTS public.credential_model_bindings_id_seq;\n" +
		"CREATE SEQUENCE IF NOT EXISTS public.credentials_id_seq;\n" +
		"CREATE SEQUENCE IF NOT EXISTS public.providers_id_seq;\n" +
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
		) + "\n" +
		schemaobj.Table(t,
			"../sql/objects/tables/provider_models.sql",
			"../sql/objects/sequences/provider_models_id.sql",
		) + "\n" +
		schemaobj.Table(t,
			"../sql/objects/tables/credential_model_bindings.sql",
			"../sql/objects/sequences/credential_model_bindings_id.sql",
		) + "\n" +
		schemaobj.Table(t,
			"../sql/objects/tables/credentials.sql",
			"../sql/objects/sequences/credentials_id.sql",
		) + "\n" +
		schemaobj.Table(t,
			"../sql/objects/tables/providers.sql",
			"../sql/objects/sequences/providers_id.sql",
		)
	if _, err := pool.Exec(ctx, ddl); err != nil {
		t.Fatalf("fixture base tables: %v", err)
	}

	// ② maas_settings 最小行（centsPerCredit 读数面）
	if _, err := pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS public.maas_settings (
    id integer PRIMARY KEY,
    cents_per_credit numeric NOT NULL DEFAULT 0.01)`); err != nil {
		t.Fatalf("fixture maas_settings: %v", err)
	}

	// ③ 迁移 940 → 949 原样执行（schema_migrations 记账表先建）
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version text PRIMARY KEY, description text, applied_at timestamptz DEFAULT now())`); err != nil {
		t.Fatalf("fixture schema_migrations: %v", err)
	}
	for _, m := range []string{
		// 826 先于 949：949 的视图引用 mc.baseline_price_currency 等
		// baseline 列（826 引入；per-table canonical 不含它们——折叠基线
		// 才有）。2026-11-05 审计真库腿首跑实测 42703 后补。
		"826_model_baseline_price.sql",
		"940_market_price_reference_family.sql",
		"942_sales_supply_annotations.sql",
		"943_sales_demand_cards.sql",
		"944_sales_quotes.sql",
		"945_sales_quote_versions.sql",
		"946_sales_quote_items.sql",
		"947_sales_quote_approvals.sql",
		"948_sales_quote_shares.sql",
		"949_supplier_view_binding_id.sql",
	} {
		body := readRepoFile(t, filepath.Join("../sql/migrations/startup", m))
		if _, err := pool.Exec(ctx, body); err != nil {
			t.Fatalf("apply migration %s: %v", m, err)
		}
	}

	// ④ 量具自证：sales 表真库看得见
	for _, tbl := range []string{"sales_supply_annotations", "sales_demand_cards",
		"sales_quotes", "sales_quote_status_log", "sales_quote_versions",
		"sales_quote_items", "sales_quote_approvals", "sales_quote_shares"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c
			JOIN pg_namespace ns ON ns.oid = c.relnamespace
			WHERE ns.nspname='public' AND c.relname = $1`, tbl).Scan(&n); err != nil {
			t.Fatalf("self-check %s: %v", tbl, err)
		}
		if n != 1 {
			t.Fatalf("fixture self-check: public.%s is absent after building the fixture", tbl)
		}
	}
}

// seedSellable 铺一组每条断言各自可区分的样本：
//
//	model-a  (10,100) 1.0/2.0 USD trusted   —— 可售基线
//	model-b  (10,101) 0.5/1.5 USD trusted   ┐ 同名双价 ⇒ 整组 withhold
//	model-b  (11,103) 0.8/1.6 USD trusted   ┘
//	model-c  (11,102) 3.0/3.0 USD           —— suspect_symmetric ⇒ trusted 门排除
//	model-d  (10,104) 0/0   USD             —— empty ⇒ trusted 门排除
func seedSellable(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `
INSERT INTO public.providers (id, code, display_name, protocol, base_url)
VALUES (1, 'prov-test', '测试供应商', 'openai', 'https://provider.test/v1');
INSERT INTO public.credentials (id, provider_id, label, fp_slot_limit) VALUES (10, 1, 'cred-10', 0), (11, 1, 'cred-11', 0);
INSERT INTO public.provider_models (id, provider_id, raw_model_name, canonical_raw_name) VALUES
    (100, 1, 'model-a', 'model-a'),
    (101, 1, 'model-b', 'model-b'),
    (102, 1, 'model-c', 'model-c'),
    (103, 1, 'model-b', 'model-b'),
    (104, 1, 'model-d', 'model-d');
INSERT INTO public.credential_model_bindings
    (credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency) VALUES
    (10, 100, 1.0, 2.0, 'USD'),
    (10, 101, 0.5, 1.5, 'USD'),
    (11, 102, 3.0, 3.0, 'USD'),
    (11, 103, 0.8, 1.6, 'USD'),
    (10, 104, 0,   0,   'USD');`)
	if err != nil {
		t.Fatalf("seed sellable: %v", err)
	}
	t.Cleanup(func() {
		dropCtx := context.WithoutCancel(ctx)
		// A4：清理定界到夹具自建行（防 ALLOW_ANY=1 豁免下误清共享表）；
		// sales_* 与 schema_migrations 记账行是本夹具家族的私有面。
		for _, stmt := range []string{
			"DELETE FROM public.credential_model_bindings WHERE credential_id IN (10, 11)",
			"DELETE FROM public.provider_models WHERE id BETWEEN 100 AND 104",
			"DELETE FROM public.credentials WHERE id IN (10, 11)",
			"DELETE FROM public.providers WHERE id = 1",
			"DELETE FROM public.sales_supply_annotations",
			"DELETE FROM public.sales_demand_cards",
			"DELETE FROM public.sales_quotes",
			"DELETE FROM public.sales_quote_shares",
			"DELETE FROM public.maas_settings WHERE id = 1",
			"DELETE FROM public.schema_migrations WHERE version IN ('826','940','942','943','944','945','946','947','948','949')",
		} {
			if _, err := pool.Exec(dropCtx, stmt); err != nil {
				t.Errorf("cleanup seed %q: %v", stmt, err)
			}
		}
	})
}

// TestRealDBSellableList —— sellable 三断言 + withhold + 登记挂接。
func TestRealDBSellableList(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)

	svc := NewService(pool)
	list, err := svc.ListSellable(ctx, true)
	if err != nil {
		t.Fatalf("ListSellable: %v", err)
	}

	// ① trusted 门：model-c（对称正价）与 model-d（0/0）不在 items。
	byCanon := map[string]SellableRow{}
	for _, it := range list.Items {
		byCanon[it.CanonicalName] = it
	}
	if _, ok := byCanon["model-c"]; ok {
		t.Errorf("model-c (suspect_symmetric 3.0/3.0) must NOT be sellable, got %+v", byCanon["model-c"])
	}
	if _, ok := byCanon["model-d"]; ok {
		t.Errorf("model-d (empty 0/0) must NOT be sellable, got %+v", byCanon["model-d"])
	}
	// ② 整组 withhold：model-b 两个不同价 ⇒ 不进 items、进 withheld。
	if _, ok := byCanon["model-b"]; ok {
		t.Errorf("model-b (same-canonical multi-price) must be withheld from items")
	}
	foundB := false
	for _, wg := range list.Withheld {
		if wg.CanonicalName == "model-b" {
			foundB = true
			if len(wg.PriceVariants) != 2 {
				t.Errorf("model-b withheld group should carry 2 price variants, got %d", len(wg.PriceVariants))
			}
			if wg.Reason != "canonical_multi_price" {
				t.Errorf("model-b withhold reason = %q, want canonical_multi_price", wg.Reason)
			}
		}
	}
	if !foundB {
		t.Errorf("model-b missing from withheld groups: %+v", list.Withheld)
	}
	// ③ 可售基线：model-a 在 items 且带价格。
	a, ok := byCanon["model-a"]
	if !ok {
		t.Fatalf("model-a (trusted baseline) missing from items: %+v", list.Items)
	}
	if a.SupplierInPer1M == nil || *a.SupplierInPer1M != "1.0" {
		t.Errorf("model-a supplier_in = %v, want 1.0 (numeric text verbatim)", a.SupplierInPer1M)
	}
	// ④ 949 binding 身份 join：登记 (10,100) 后挂到 model-a 行。
	if _, err := pool.Exec(ctx, `INSERT INTO public.sales_supply_annotations
		(credential_id, provider_model_id, sla_tier, status, maintainer) VALUES (10, 100, 'gold', 'active', 'ops1')`); err != nil {
		t.Fatalf("insert annotation: %v", err)
	}
	list2, err := svc.ListSellable(ctx, true)
	if err != nil {
		t.Fatalf("ListSellable(2): %v", err)
	}
	var a2 *SellableRow
	for i := range list2.Items {
		if list2.Items[i].CanonicalName == "model-a" {
			a2 = &list2.Items[i]
		}
	}
	if a2 == nil || a2.Annotation == nil || a2.Annotation.SLATier != "gold" {
		t.Errorf("model-a annotation join failed (949 provider_model_id): %+v", a2)
	}
	// ⑤ suspended 登记排除（登记保留、状态排除）。
	if _, err := pool.Exec(ctx, `UPDATE public.sales_supply_annotations SET status='suspended' WHERE credential_id=10 AND provider_model_id=100`); err != nil {
		t.Fatalf("suspend annotation: %v", err)
	}
	list3, err := svc.ListSellable(ctx, true)
	if err != nil {
		t.Fatalf("ListSellable(3): %v", err)
	}
	for _, it := range list3.Items {
		if it.CanonicalName == "model-a" {
			t.Errorf("suspended model-a must not appear in items")
		}
	}
}

// seedQuote 铺工作台Lifecycle用的最小环境（trusted 成本 + 低折扣）。
func trustedQuoteInput(demandID *int64) QuoteInput {
	costIn := 1.0
	costOut := 2.0
	rateIn := 10.0
	rateOut := 20.0
	return QuoteInput{
		DemandCardID: demandID,
		CustomerName: "客户甲",
		Title:        "11月推理用量",
		Tier:         "standard",
		Items: []QuoteItemInput{{
			CanonicalName: "model-a",
			DisplayName:   "Model A",
			EstTokensIn:   100_000_000,
			EstTokensOut:  20_000_000,
			CostInPer1M:   &costIn,
			CostOutPer1M:  &costOut,
			CostCurrency:  "USD",
			RateIn:        &rateIn,
			RateOut:       &rateOut,
			DiscountPct:   0,
			Confidence:    "trusted",
		}},
	}
}

// TestRealDBQuoteLifecycle —— draft→(auto-approve)→sent→won + 状态审计行。
func TestRealDBQuoteLifecycle(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)

	svc := NewService(pool)

	// 充值锚点：cents_per_credit=20（分/积分，A-14 语义）⇒ 1 credit = 0.2 元
	// ≈ $0.0278 —— 让下面这组 rate 100/200 的样本落在「政策内」窗口。
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 20)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}

	// 成本 $140（1.0×100 + 2.0×20）。卖价（credits 100/200, 折 0）：
	// 100×100 + 200×20 = 14000 credits ⇒ ×20 分 = 2800 元 ⇒ ÷7.2 ≈ $388.9
	// ⇒ 毛利 64% ≥ 30%、金额 < $500、零折扣 ⇒ 三维全不触发（A1 修复后的
	// 正确算术；修复前 cents 被当元用，收入虚大 100×，本样本会错进 order_amount 触审）。
	in := trustedQuoteInput(nil)
	in.Items[0].RateIn = ptrF(100)
	in.Items[0].RateOut = ptrF(200)

	q, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if q.Status != StatusDraft {
		t.Fatalf("new quote status = %s, want draft", q.Status)
	}
	if q.Version == nil || q.Version.VersionNo != 1 {
		t.Fatalf("new quote should carry version 1, got %+v", q.Version)
	}
	if q.Version.PriceConfidence != "trusted" {
		t.Fatalf("version confidence = %s, want trusted", q.Version.PriceConfidence)
	}

	sub, err := svc.SubmitQuote(ctx, q.ID, "sales01")
	if err != nil {
		t.Fatalf("SubmitQuote: %v", err)
	}
	// 三维全不触发（毛利高、零折扣、金额 <$500）⇒ 政策内自动放行。
	if sub.Status != StatusApproved {
		t.Fatalf("in-policy submit status = %s, want approved (auto)", sub.Status)
	}
	sent, err := svc.SendQuote(ctx, q.ID, "sales01")
	if err != nil {
		t.Fatalf("SendQuote: %v", err)
	}
	if sent.Status != StatusSent {
		t.Fatalf("sent status = %s, want sent", sent.Status)
	}
	closed, err := svc.CloseQuote(ctx, q.ID, StatusWon, "客户确认签约", "sales01")
	if err != nil {
		t.Fatalf("CloseQuote: %v", err)
	}
	if closed.Status != StatusWon {
		t.Fatalf("closed status = %s, want won", closed.Status)
	}
	// 收口必须带原因：无原因 ⇒ 拒绝。
	q2, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote(2): %v", err)
	}
	if _, err := svc.SubmitQuote(ctx, q2.ID, "sales01"); err != nil {
		t.Fatalf("SubmitQuote(2): %v", err)
	}
	if _, err := svc.SendQuote(ctx, q2.ID, "sales01"); err != nil {
		t.Fatalf("SendQuote(2): %v", err)
	}
	if _, err := svc.CloseQuote(ctx, q2.ID, StatusLost, "", "sales01"); err == nil {
		t.Errorf("close without reason must be rejected (丢单原因强制回填)")
	}
	// 状态审计行存在且可复算。
	var logCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.sales_quote_status_log WHERE quote_id = $1`, q.ID).Scan(&logCount); err != nil {
		t.Fatalf("count status log: %v", err)
	}
	if logCount < 3 { // version1 materialize + auto-approve + sent + won
		t.Errorf("status log rows = %d, want >= 3 (转换表隐式定义状态机)", logCount)
	}
}

// TestRealDBApprovalPath —— 三维触发 ⇒ pending_approval + locked +
// trigger_reasons 可复算 + decide 双向。
func TestRealDBApprovalPath(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	svc := NewService(pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 20)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}

	// 折扣深度 25% > 默认阈值 10% ⇒ 触发人审。
	in := trustedQuoteInput(nil)
	in.Items[0].RateIn = ptrF(100)
	in.Items[0].RateOut = ptrF(200)
	in.Items[0].DiscountPct = 25
	q, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	sub, err := svc.SubmitQuote(ctx, q.ID, "sales01")
	if err != nil {
		t.Fatalf("SubmitQuote: %v", err)
	}
	if sub.Status != StatusPendingApproval {
		t.Fatalf("triggered submit status = %s, want pending_approval", sub.Status)
	}
	if !sub.Locked {
		t.Errorf("pending_approval must lock the head (Odoo 正交布尔)")
	}
	// 冻结：审批中修改内容被拒。
	if _, err := svc.ReviseQuote(ctx, q.ID, in, "sales01"); err == nil {
		t.Errorf("revise during pending_approval must be rejected (content frozen)")
	}
	// trigger_reasons 可复算（读数+阈值快照）。
	var reasons string
	if err := pool.QueryRow(ctx, `SELECT trigger_reasons::text FROM public.sales_quote_approvals WHERE quote_id = $1`, q.ID).Scan(&reasons); err != nil {
		t.Fatalf("read trigger_reasons: %v", err)
	}
	if !strings.Contains(reasons, "discount_depth") || !strings.Contains(reasons, "threshold") {
		t.Errorf("trigger_reasons missing discount_depth evidence: %s", reasons)
	}
	// 驳回 ⇒ 回 draft 解锁。
	rej, err := svc.DecideApproval(ctx, q.ID, "rejected", "折扣太深", "super01")
	if err != nil {
		t.Fatalf("DecideApproval(rejected): %v", err)
	}
	if rej.Status != StatusDraft || rej.Locked {
		t.Errorf("rejected should return to draft unlocked, got %s locked=%v", rej.Status, rej.Locked)
	}
	// 二次提交（同触发）→ 批准。
	if _, err := svc.SubmitQuote(ctx, q.ID, "sales01"); err != nil {
		t.Fatalf("SubmitQuote(2): %v", err)
	}
	ok, err := svc.DecideApproval(ctx, q.ID, "approved", "", "super01")
	if err != nil {
		t.Fatalf("DecideApproval(approved): %v", err)
	}
	if ok.Status != StatusApproved {
		t.Errorf("approved status = %s", ok.Status)
	}
	// 耗时预计算落库。
	var elapsed *int
	if err := pool.QueryRow(ctx, `SELECT elapsed_seconds FROM public.sales_quote_approvals WHERE quote_id = $1 AND decision='approved'`, q.ID).Scan(&elapsed); err != nil {
		t.Fatalf("read elapsed: %v", err)
	}
	if elapsed == nil || *elapsed < 0 {
		t.Errorf("elapsed_seconds should be precomputed, got %v", elapsed)
	}
}

// TestRealDBReviseAndImmutability —— sent 后修订 = 新版本行 + 旧版
// superseded；不可变触发器三腿（业务列拒绝 / superseded_by 放行 / 直删拒绝）。
func TestRealDBReviseAndImmutability(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	svc := NewService(pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 20)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}

	in := trustedQuoteInput(nil)
	in.Items[0].RateIn = ptrF(100)
	in.Items[0].RateOut = ptrF(200)
	q, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	for _, step := range []func() error{
		func() error { _, err := svc.SubmitQuote(ctx, q.ID, "sales01"); return err },
		func() error { _, err := svc.SendQuote(ctx, q.ID, "sales01"); return err },
	} {
		if err := step(); err != nil {
			t.Fatalf("lifecycle step: %v", err)
		}
	}
	// sent 后修订 ⇒ v2，v1 被 superseded。v2 选**不触发**红线的量
	// （rate 60/120 × 200M/20M = 14400 credits = 2880 元 ≈ $400 < $500，
	// 毛利 65%）——触发重审的路径单独在 TestRealDBSentReviseReapproval 验。
	in2 := in
	in2.Items = append([]QuoteItemInput(nil), in.Items...)
	in2.Items[0].EstTokensIn = 200_000_000
	in2.Items[0].RateIn = ptrF(60)
	in2.Items[0].RateOut = ptrF(120)
	rev, err := svc.ReviseQuote(ctx, q.ID, in2, "sales01")
	if err != nil {
		t.Fatalf("ReviseQuote: %v", err)
	}
	if rev.Version == nil || rev.Version.VersionNo != 2 {
		t.Fatalf("revised version = %+v, want v2", rev.Version)
	}
	var sup *int64
	if err := pool.QueryRow(ctx, `SELECT superseded_by FROM public.sales_quote_versions WHERE quote_id = $1 AND version_no = 1`, q.ID).Scan(&sup); err != nil {
		t.Fatalf("read superseded_by: %v", err)
	}
	if sup == nil || *sup != rev.Version.ID {
		t.Errorf("v1.superseded_by = %v, want %d (Kill Bill 版本序列)", sup, rev.Version.ID)
	}
	// 不可变触发器三腿（真库行为，945）。
	v1ID := rev.Version.ID
	var v1Any int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.sales_quote_versions WHERE quote_id = $1 AND version_no = 1`, q.ID).Scan(&v1Any); err != nil {
		t.Fatalf("find v1: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.sales_quote_versions SET margin_usd = 99 WHERE id = $1`, v1Any); err == nil {
		t.Errorf("business-column UPDATE on immutable version must be rejected by DB trigger")
	}
	if _, err := pool.Exec(ctx, `UPDATE public.sales_quote_versions SET superseded_by = NULL WHERE id = $1`, v1Any); err != nil {
		t.Errorf("superseded_by UPDATE should be allowed: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM public.sales_quote_versions WHERE id = $1`, v1Any); err == nil {
		t.Errorf("direct DELETE on immutable version must be rejected by DB trigger")
	}
	// 头级联清理放行（v1 行随头删除消失）。
	if _, err := pool.Exec(ctx, `DELETE FROM public.sales_quotes WHERE id = $1`, q.ID); err != nil {
		t.Fatalf("cascade delete via head: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.sales_quote_versions WHERE quote_id = $1`, q.ID).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Errorf("cascade should remove all versions, got %d", remaining)
	}
	_ = v1ID
}

// TestRealDBSentReviseReapproval —— A3 修复：sent 单修订触发红线 ⇒ 回
// pending_approval + locked + 审批行（version 钉 v2）+ 状态审计行
// sent→pending_approval；人审通过后可再次 send（发出后的修订与首发同判）。
func TestRealDBSentReviseReapproval(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	svc := NewService(pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 20)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}

	in := trustedQuoteInput(nil)
	in.Items[0].RateIn = ptrF(100)
	in.Items[0].RateOut = ptrF(200)
	q, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if _, err := svc.SubmitQuote(ctx, q.ID, "sales01"); err != nil {
		t.Fatalf("SubmitQuote: %v", err)
	}
	if _, err := svc.SendQuote(ctx, q.ID, "sales01"); err != nil {
		t.Fatalf("SendQuote: %v", err)
	}
	// 触发修订：rate 200/400 ⇒ 28000 credits = 5600 元 ≈ $777.8 ≥ $500
	// ⇒ order_amount 维触发（修复前：sent 后修订完全旁路审批，A3）。
	in2 := in
	in2.Items = append([]QuoteItemInput(nil), in.Items...)
	in2.Items[0].RateIn = ptrF(200)
	in2.Items[0].RateOut = ptrF(400)
	rev, err := svc.ReviseQuote(ctx, q.ID, in2, "sales01")
	if err != nil {
		t.Fatalf("ReviseQuote: %v", err)
	}
	if rev.Status != StatusPendingApproval || !rev.Locked {
		t.Fatalf("triggered revise on sent = %s locked=%v, want pending_approval+locked", rev.Status, rev.Locked)
	}
	if rev.Version == nil || rev.Version.VersionNo != 2 {
		t.Fatalf("revised version = %+v, want v2", rev.Version)
	}
	// 审批行钉在 v2 且 trigger_reasons 可复算。
	var vID int64
	var reasons string
	if err := pool.QueryRow(ctx, `SELECT version_id, trigger_reasons::text FROM public.sales_quote_approvals WHERE quote_id = $1 ORDER BY created_at DESC LIMIT 1`, q.ID).Scan(&vID, &reasons); err != nil {
		t.Fatalf("read re-approval row: %v", err)
	}
	if vID != rev.Version.ID || !strings.Contains(reasons, "order_amount") {
		t.Errorf("re-approval pinned to version %d reasons=%s, want v2 + order_amount", vID, reasons)
	}
	// 状态审计行：sent → pending_approval（转换表隐式状态机的新转换）。
	var logCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.sales_quote_status_log
		WHERE quote_id = $1 AND from_status='sent' AND to_status='pending_approval'`, q.ID).Scan(&logCount); err != nil {
		t.Fatalf("count transition log: %v", err)
	}
	if logCount != 1 {
		t.Errorf("sent→pending_approval log rows = %d, want 1", logCount)
	}
	// 人审通过 → approved → 再次 send 回到 sent。
	if _, err := svc.DecideApproval(ctx, q.ID, "approved", "", "super01"); err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	sent, err := svc.SendQuote(ctx, q.ID, "sales01")
	if err != nil {
		t.Fatalf("SendQuote(after re-approval): %v", err)
	}
	if sent.Status != StatusSent {
		t.Errorf("re-sent status = %s, want sent", sent.Status)
	}
}

// TestRealDBSharePublicProjection —— token 往返 + 公开面响应体断言 +
// 过期/撤销不可读 + 访问审计。
func TestRealDBSharePublicProjection(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	svc := NewService(pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 20)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}

	in := trustedQuoteInput(nil)
	in.Items[0].RateIn = ptrF(100)
	in.Items[0].RateOut = ptrF(200)
	q, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if _, err := svc.SubmitQuote(ctx, q.ID, "sales01"); err != nil {
		t.Fatalf("SubmitQuote: %v", err)
	}
	if _, err := svc.SendQuote(ctx, q.ID, "sales01"); err != nil {
		t.Fatalf("SendQuote: %v", err)
	}
	link, err := svc.CreateShare(ctx, q.ID, "张三", "zhang@example.com", time.Hour, "sales01")
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if link.Token == "" {
		t.Fatalf("share token must be returned once")
	}
	// 明文不落库：表里只有 sha256。
	var stored string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM public.sales_quote_shares WHERE id = $1`, link.ID).Scan(&stored); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	if stored == link.Token || strings.Contains(stored, link.Token) {
		t.Errorf("plaintext token must never be stored")
	}

	pq, err := svc.GetPublicQuote(ctx, link.Token, "1.2.3.4", "test-agent")
	if err != nil {
		t.Fatalf("GetPublicQuote: %v", err)
	}
	// ★ 响应体断言（不是 CSS）：marshal 后禁止出现成本/毛利/供应商身份字段。
	body, err := json.Marshal(pq)
	if err != nil {
		t.Fatalf("marshal public quote: %v", err)
	}
	bs := string(body)
	for _, forbidden := range []string{
		"cost", "margin", "supplier", "credential", "provider_id",
		"fx_rate", "revenue", "confid",
	} {
		if strings.Contains(strings.ToLower(bs), forbidden) {
			t.Errorf("public body leaks %q: %s", forbidden, bs)
		}
	}
	// 卖价在场（折扣轴随基础价一起展示，决议 C）；合计位（W2 客户版页面）
	// = 客户自己的应付金额，不是敏感轴 —— 但成本/毛利轴必须仍缺席。
	if !strings.Contains(bs, "rate_in") || !strings.Contains(bs, "discount_pct") {
		t.Errorf("public body should carry the offer (rate_in/discount_pct): %s", bs)
	}
	if !strings.Contains(bs, "sale_total_usd") || !strings.Contains(bs, "subtotal_usd") {
		t.Errorf("public body should carry offer totals: %s", bs)
	}
	if pq.SaleTotalUSD <= 0 {
		t.Errorf("sale_total_usd must carry the precomputed offer total, got %v", pq.SaleTotalUSD)
	}
	// 三时点埋点 + view_count。
	var viewCount int
	var viewedAt, openedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT view_count, viewed_at, opened_at FROM public.sales_quote_shares WHERE id = $1`, link.ID).Scan(&viewCount, &viewedAt, &openedAt); err != nil {
		t.Fatalf("read tracking: %v", err)
	}
	if viewCount != 1 || viewedAt == nil || openedAt == nil {
		t.Errorf("first access should set viewed/opened and count=1, got %d %v %v", viewCount, viewedAt, openedAt)
	}
	// detail 访问审计落行。
	var detail string
	if err := pool.QueryRow(ctx, `SELECT detail::text FROM public.sales_quote_shares WHERE id = $1`, link.ID).Scan(&detail); err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if !strings.Contains(detail, "1.2.3.4") {
		t.Errorf("access audit should carry ip in detail: %s", detail)
	}

	// P-12（2026-11-06 审计）：A6 的 20 条上界回归钉——首访之后灌 25 次访问，
	// detail.access 必须恰好保留最近 20 条（1.2.3.4 + 10.0.0.0..4 共 6 条
	// 被挤出，最旧保留条 = 10.0.0.5；jsonb_set 的 LIMIT 20 写入路径语义）。
	for i := 0; i < 25; i++ {
		if _, err := svc.GetPublicQuote(ctx, link.Token, fmt.Sprintf("10.0.0.%d", i), "ua"); err != nil {
			t.Fatalf("bounded access %d: %v", i, err)
		}
	}
	var accessLen int
	var oldestKept string
	if err := pool.QueryRow(ctx, `
SELECT jsonb_array_length(detail->'access'),
       detail->'access'->0->>'ip'
  FROM public.sales_quote_shares WHERE id = $1`, link.ID).Scan(&accessLen, &oldestKept); err != nil {
		t.Fatalf("read bounded access: %v", err)
	}
	if accessLen != 20 {
		t.Errorf("detail.access must be capped at 20 entries, got %d", accessLen)
	}
	if oldestKept != "10.0.0.5" {
		t.Errorf("oldest kept access entry should be the 21st-newest (10.0.0.5), got %q", oldestKept)
	}

	// 撤销 ⇒ 不可读。
	if err := svc.RevokeShare(ctx, link.ID, "tester", true); err != nil {
		t.Fatalf("RevokeShare: %v", err)
	}
	if _, err := svc.GetPublicQuote(ctx, link.Token, "1.2.3.4", "ua"); err == nil {
		t.Errorf("revoked token must not resolve")
	}
	// 过期 ⇒ 不可读。CreateShare 对 ttl<=0 钳到 14 天（防呆语义，
	// 2026-11-05 真库腿实测钉出）⇒ 到期路径用直接改 expires_at 驱动。
	link2, err := svc.CreateShare(ctx, q.ID, "李四", "li@example.com", time.Hour, "sales01")
	if err != nil {
		t.Fatalf("CreateShare(expired): %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.sales_quote_shares SET expires_at = now() - interval '1 minute' WHERE id = $1`, link2.ID); err != nil {
		t.Fatalf("expire share: %v", err)
	}
	if _, err := svc.GetPublicQuote(ctx, link2.Token, "1.2.3.4", "ua"); err == nil {
		t.Errorf("expired token must not resolve")
	}
	// P3-12（2026-11-08 审计）：过期/撤销后的探测流量不得污染三时点埋点——
	// view_count 仍为 1、viewed/opened 不变（审计 detail 仍逐条落）。
	var vcAfter int
	var openedAfter *time.Time
	if err := pool.QueryRow(ctx, `SELECT view_count, opened_at FROM public.sales_quote_shares WHERE id = $1`, link2.ID).Scan(&vcAfter, &openedAfter); err != nil {
		t.Fatalf("read tracking after expired probe: %v", err)
	}
	if vcAfter != 0 || openedAfter != nil {
		t.Errorf("expired probe must not bump view_count/opened_at, got count=%d opened=%v", vcAfter, openedAfter)
	}
	var detailAfter string
	if err := pool.QueryRow(ctx, `SELECT detail::text FROM public.sales_quote_shares WHERE id = $1`, link2.ID).Scan(&detailAfter); err != nil {
		t.Fatalf("read detail after expired probe: %v", err)
	}
	if !strings.Contains(detailAfter, "expired") { // pg jsonb 序列化带空格，只断言词元
		t.Errorf("expired probe must still land in the access audit, detail: %s", detailAfter)
	}
}

func ptrF(f float64) *float64 { return &f }

// ── 2026-11-08 风暴档审计轮新增腿 ──────────────────────────────────────────
//
//   ⑤ 服务端派生：建单不带 confidence/cost_currency/cost 也成功（P1 钉：
//      此前版本置信 '' 违反 945 CHECK ⇒ 前端建单必 23514）+ 客户端声明
//      成本不采信（P6）+ price_axes 五价快照非空（P5）；
//   ⑥ 需求→报价联动（P3-13：'quoted' 死值激活）；
//   ⑦ 成本漂移（W2-T2：改绑定价 ⇒ 活单 drifted，won 单不漂）；
//   ⑧ 看板读模型（W2-T2：漏斗/触发率/丢单原因/毛利桶）；
//   ⑨ owner 语义（P2-3：清单收窄/非本人 404/撤销越权拒绝）；
//   ⑩ 联系人最小化（P2-4：reveal=false ⇒ vendor_contact 不下发）。

// TestRealDBCreateQuoteServerDerivedCost —— ⑤。
func TestRealDBCreateQuoteServerDerivedCost(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 20)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}
	svc := NewService(pool)

	// 前端真实形状（2026-11-08 审计 P1 的 payload）：无 confidence、无
	// cost_currency、无 cost_*；甚至故意带上一个错误成本（服务端必须不采信）。
	wrongCost := 99.0
	in := QuoteInput{
		CustomerName: "客户乙",
		Title:        "服务端派生钉",
		Tier:         "standard",
		Items: []QuoteItemInput{{
			CanonicalName: "model-a",
			DisplayName:   "Model A",
			EstTokensIn:   100_000_000,
			EstTokensOut:  20_000_000,
			CostInPer1M:   &wrongCost, // 客户端断言：必须被 binding 价覆盖
			RateIn:        ptrF(100),
			RateOut:       ptrF(200),
		}},
	}
	q, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote without client cost/confidence: %v（P1 修复前的 23514 链）", err)
	}
	if q.Version == nil || q.Version.PriceConfidence != "trusted" {
		t.Fatalf("derived version confidence = %v, want trusted", q.Version)
	}
	var it QuoteItemOutput
	for _, v := range q.Version.Items {
		if v.CanonicalName == "model-a" {
			it = v
		}
	}
	if it.CostInPer1M == nil || *it.CostInPer1M != 1.0 || it.CostOutPer1M == nil || *it.CostOutPer1M != 2.0 {
		t.Errorf("derived cost = %v/%v, want binding price 1.0/2.0 (客户端断言 99 必须被覆盖)", it.CostInPer1M, it.CostOutPer1M)
	}
	if it.CostCurrency != "USD" {
		t.Errorf("derived currency = %q, want USD", it.CostCurrency)
	}
	if it.Confidence != "trusted" {
		t.Errorf("derived item confidence = %q, want trusted", it.Confidence)
	}
	// P5：price_axes 五价快照非空（此前恒 '{}'）。
	if q.Version.PriceAxes == nil || len(q.Version.PriceAxes) == 0 {
		t.Errorf("price_axes must be materialized, got %v", q.Version.PriceAxes)
	}
	if axesItems, ok := q.Version.PriceAxes["items"].(map[string]any); !ok || axesItems["model-a"] == nil {
		t.Errorf("price_axes.items[model-a] missing: %v", q.Version.PriceAxes)
	}
	// 提交门仍然工作：trusted ⇒ submit 通（链路全绿语义）。
	if _, err := svc.SubmitQuote(ctx, q.ID, "sales01"); err != nil {
		t.Errorf("SubmitQuote after derivation: %v", err)
	}
	// 视图外模型拒绝（无成本依据不进报价）。
	bad := in
	bad.Items = []QuoteItemInput{{CanonicalName: "not-in-view", EstTokensIn: 1, RateIn: ptrF(1)}}
	if _, err := svc.CreateQuote(ctx, bad, "sales01"); err == nil {
		t.Errorf("canonical outside supply view must be rejected")
	}
	// 负数输入拒绝（P3-9）。
	neg := in
	neg.Items = []QuoteItemInput{{CanonicalName: "model-a", EstTokensIn: -1, RateIn: ptrF(1)}}
	if _, err := svc.CreateQuote(ctx, neg, "sales01"); err == nil {
		t.Errorf("negative est_tokens must be rejected")
	}
}

// TestRealDBDemandQuotedLinkage —— ⑥。
func TestRealDBDemandQuotedLinkage(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	svc := NewService(pool)

	card, err := svc.CreateDemandCard(ctx, DemandCard{CustomerName: "客户丙"}, "sales01")
	if err != nil {
		t.Fatalf("CreateDemandCard: %v", err)
	}
	in := trustedQuoteInput(&card.ID)
	if _, err := svc.CreateQuote(ctx, in, "sales01"); err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	got, err := svc.GetDemandCard(ctx, card.ID)
	if err != nil {
		t.Fatalf("GetDemandCard: %v", err)
	}
	if got.Status != "quoted" {
		t.Errorf("demand card status = %q, want quoted（P3-13 死值激活）", got.Status)
	}
}

// TestRealDBCostDrift —— ⑦。
func TestRealDBCostDrift(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 20)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}
	svc := NewService(pool)

	qIn := trustedQuoteInput(nil)
	qIn.Items[0].RateIn = ptrF(100)
	qIn.Items[0].RateOut = ptrF(200)
	q, err := svc.CreateQuote(ctx, qIn, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	qOld, err := svc.CreateQuote(ctx, qIn, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote(2): %v", err)
	}
	if _, err := svc.SubmitQuote(ctx, qOld.ID, "sales01"); err != nil {
		t.Fatalf("SubmitQuote(2): %v", err)
	}
	if _, err := svc.SendQuote(ctx, qOld.ID, "sales01"); err != nil {
		t.Fatalf("SendQuote(2): %v", err)
	}
	if _, err := svc.CloseQuote(ctx, qOld.ID, StatusWon, "签了", "sales01"); err != nil {
		t.Fatalf("CloseQuote(2): %v", err)
	}
	// 改绑定价 +30% ⇒ 活单漂移 ≥10%（默认阈值）⇒ drifted；won 单不漂。
	if _, err := pool.Exec(ctx, `UPDATE public.credential_model_bindings
	     SET unit_price_in_per_1m = 1.3, unit_price_out_per_1m = 2.6
	   WHERE credential_id = 10 AND provider_model_id = 100`); err != nil {
		t.Fatalf("drift binding price: %v", err)
	}
	quotes, err := svc.ListQuotes(ctx, "", "sales01", true)
	if err != nil {
		t.Fatalf("ListQuotes: %v", err)
	}
	byID := map[int64]Quote{}
	for _, qq := range quotes {
		byID[qq.ID] = qq
	}
	drifted, ok := byID[q.ID]
	if !ok {
		t.Fatalf("active quote %d missing from list", q.ID)
	}
	if !drifted.CostDrifted || drifted.CostDriftPct == nil || *drifted.CostDriftPct < 10 {
		t.Errorf("active quote drift = %v/%v, want drifted ≥10%%（成本 1.0→1.3）", drifted.CostDrifted, drifted.CostDriftPct)
	}
	won := byID[qOld.ID]
	if won.CostDriftPct != nil || won.CostDrifted {
		t.Errorf("won quote must not carry drift markers（已收口单无收件人）")
	}
}

// TestRealDBDashboard —— ⑧。
func TestRealDBDashboard(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 20)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}
	svc := NewService(pool)

	in := trustedQuoteInput(nil)
	in.Items[0].RateIn = ptrF(100)
	in.Items[0].RateOut = ptrF(200)
	q, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	// 深折扣触发人审 ⇒ status_log pending_approval（触发率分子/分母）。
	inTrig := trustedQuoteInput(nil)
	inTrig.Items[0].RateIn = ptrF(100)
	inTrig.Items[0].RateOut = ptrF(200)
	inTrig.Items[0].DiscountPct = 50
	qTrig, err := svc.CreateQuote(ctx, inTrig, "sales02")
	if err != nil {
		t.Fatalf("CreateQuote(trigger): %v", err)
	}
	if _, err := svc.SubmitQuote(ctx, qTrig.ID, "sales02"); err != nil {
		t.Fatalf("SubmitQuote(trigger): %v", err)
	}
	if _, err := svc.SubmitQuote(ctx, q.ID, "sales01"); err != nil {
		t.Fatalf("SubmitQuote: %v", err)
	}
	if _, err := svc.SendQuote(ctx, q.ID, "sales01"); err != nil {
		t.Fatalf("SendQuote: %v", err)
	}
	if _, err := svc.CloseQuote(ctx, q.ID, StatusLost, "价格太高", "sales01"); err != nil {
		t.Fatalf("CloseQuote: %v", err)
	}
	dash, err := svc.FunnelDashboard(ctx)
	if err != nil {
		t.Fatalf("FunnelDashboard: %v", err)
	}
	if dash.Funnel["lost"] != 1 || dash.Funnel["pending_approval"] != 1 {
		t.Errorf("funnel = %v, want lost=1 pending_approval=1", dash.Funnel)
	}
	if dash.SubmittedQuotes != 2 || dash.TriggeredQuotes != 1 {
		t.Errorf("trigger rate base = %d/%d, want 1/2", dash.TriggeredQuotes, dash.SubmittedQuotes)
	}
	if dash.TriggerRatePct != 50 {
		t.Errorf("trigger rate = %v, want 50", dash.TriggerRatePct)
	}
	if !dash.TriggerRateAlert {
		t.Errorf("50%% rate must exceed the 40%% alert line ⇒ TriggerRateAlert=true")
	}
	if len(dash.LostReasons) == 0 || dash.LostReasons[0].Reason != "价格太高" {
		t.Errorf("lost reasons = %v, want [价格太高]", dash.LostReasons)
	}
}

// TestRealDBOwnerSemantics —— ⑨。
func TestRealDBOwnerSemantics(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 20)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}
	svc := NewService(pool)

	in := trustedQuoteInput(nil)
	in.Items[0].RateIn = ptrF(100)
	in.Items[0].RateOut = ptrF(200)
	q, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if err := svc.EnsureQuoteOwner(ctx, q.ID, "sales01", false); err != nil {
		t.Errorf("owner must pass: %v", err)
	}
	if err := svc.EnsureQuoteOwner(ctx, q.ID, "sales02", false); !errorsIsNotFound(err) {
		t.Errorf("non-owner must get ErrNotFound (不泄露存在性), got %v", err)
	}
	if err := svc.EnsureQuoteOwner(ctx, q.ID, "sales02", true); err != nil {
		t.Errorf("unrestricted must pass: %v", err)
	}
	// 清单收窄：sales02 只看自己。
	if _, err := svc.CreateQuote(ctx, in, "sales02"); err != nil {
		t.Fatalf("CreateQuote(sales02): %v", err)
	}
	mine, err := svc.ListQuotes(ctx, "", "sales02", false)
	if err != nil {
		t.Fatalf("ListQuotes(sales02): %v", err)
	}
	if len(mine) != 1 || mine[0].CreatedBy != "sales02" {
		t.Errorf("owner-scoped list = %d rows, want 1 own row", len(mine))
	}
	// 撤销越权拒绝（RevokeShare owner 语义）。分享要求 sent/approved ⇒ 先推进。
	if _, err := svc.SubmitQuote(ctx, q.ID, "sales01"); err != nil {
		t.Fatalf("SubmitQuote: %v", err)
	}
	link, err := svc.CreateShare(ctx, q.ID, "李四", "li@example.com", time.Hour, "sales01")
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if err := svc.RevokeShare(ctx, link.ID, "sales02", false); !errorsIsNotFound(err) {
		t.Errorf("non-owner revoke must be ErrNotFound, got %v", err)
	}
	if err := svc.RevokeShare(ctx, link.ID, "sales02", true); err != nil {
		t.Errorf("unrestricted revoke must pass: %v", err)
	}
}

func errorsIsNotFound(err error) bool { return err != nil && errors.Is(err, ErrNotFound) }

// TestRealDBContactMinimization —— ⑩。
func TestRealDBContactMinimization(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.sales_supply_annotations
	    (credential_id, provider_model_id, vendor_contact, status)
	  VALUES (10, 100, '王厂商 13800000000', 'active')
	  ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET vendor_contact = EXCLUDED.vendor_contact`); err != nil {
		t.Fatalf("seed annotation: %v", err)
	}
	svc := NewService(pool)
	hidden, err := svc.ListSellable(ctx, false)
	if err != nil {
		t.Fatalf("ListSellable(hidden): %v", err)
	}
	for _, row := range hidden.Items {
		if row.Annotation != nil && row.Annotation.VendorContact != "" {
			t.Errorf("non-super view must strip vendor_contact, got %q", row.Annotation.VendorContact)
		}
	}
	shown, err := svc.ListSellable(ctx, true)
	if err != nil {
		t.Fatalf("ListSellable(shown): %v", err)
	}
	found := false
	for _, row := range shown.Items {
		if row.Annotation != nil && row.Annotation.VendorContact == "王厂商 13800000000" {
			found = true
		}
	}
	if !found {
		t.Errorf("super view must carry vendor_contact")
	}
}

// TestRealDBWithholdUniverseTrustedPlusEmpty —— 2026-11-12 复审审计 P1-1 钉：
// 「trusted 行 + empty 档 NULL 价行」的同名组**不得**判 withhold（NULL 价不是
// 价格变体，NULL≠0）。修复前派生宇宙把 NULL 当 (0,0) 变体 ⇒ 211 实测 12 只
// 可售模型 8 只建单必拒（与 W1 的 23514 同构，方向 fail-closed 但可用性坏）。
// 真库腿用「同名：2 行 trusted 同价 + 2 行 empty NULL 价」驱动。
func TestRealDBWithholdUniverseTrustedPlusEmpty(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	svc := NewService(pool)

	// model-e：2 行 trusted 同价 (2.0,4.0) + 2 行 empty（NULL 价 ⇒ 视图判 empty 档）。
	// NULL 价行的 cost 基础列不落数值（保持 NULL）。
	_, err = pool.Exec(ctx, `
INSERT INTO public.provider_models (id, provider_id, raw_model_name, canonical_raw_name) VALUES
    (120, 1, 'model-e', 'model-e'),
    (121, 1, 'model-e', 'model-e'),
    (122, 1, 'model-e', 'model-e'),
    (123, 1, 'model-e', 'model-e');
INSERT INTO public.credential_model_bindings
    (credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency) VALUES
    (10, 120, 2.0, 4.0, 'USD'),
    (11, 121, 2.0, 4.0, 'USD'),
    (10, 122, NULL, NULL, 'USD'),
    (11, 123, NULL, NULL, 'USD');`)
	if err != nil {
		t.Fatalf("seed model-e: %v", err)
	}
	t.Cleanup(func() {
		dropCtx := context.WithoutCancel(ctx)
		for _, stmt := range []string{
			"DELETE FROM public.credential_model_bindings WHERE provider_model_id BETWEEN 120 AND 123",
			"DELETE FROM public.provider_models WHERE id BETWEEN 120 AND 123",
		} {
			if _, err := pool.Exec(dropCtx, stmt); err != nil {
				t.Errorf("cleanup model-e: %v", stmt)
			}
		}
	})

	// sellable 清单必须含 model-e（trusted 同价重复不是冲突）。
	list, err := svc.ListSellable(ctx, false)
	if err != nil {
		t.Fatalf("ListSellable: %v", err)
	}
	found := false
	for _, r := range list.Items {
		if r.CanonicalName == "model-e" {
			found = true
		}
	}
	if !found {
		t.Fatalf("model-e missing from sellable items（trusted 同价 + empty NULL 价不得整组扣下）")
	}

	// 建单必须成功（修复前：派生宇宙 NULL→(0,0) 计为第二变体 ⇒ withheld 拒绝）。
	in := trustedQuoteInput(nil)
	in.Items = []QuoteItemInput{{
		CanonicalName: "model-e",
		DisplayName:   "Model E",
		EstTokensIn:   100_000_000,
		RateIn:        ptrF(100),
		RateOut:       ptrF(200),
	}}
	q, err := svc.CreateQuote(ctx, in, "sales01")
	if err != nil {
		t.Fatalf("CreateQuote on trusted+empty same-name group: %v（P1-1 修复前必拒）", err)
	}
	if q.Version == nil || q.Version.PriceConfidence != "trusted" {
		t.Fatalf("derived confidence = %v, want trusted（pick 应落 trusted 行）", q.Version)
	}
	for _, it := range q.Version.Items {
		if it.CanonicalName == "model-e" && (it.CostInPer1M == nil || *it.CostInPer1M != 2.0) {
			t.Errorf("derived cost = %v, want trusted 行的 2.0（pick 不得落 NULL 价行）", it.CostInPer1M)
		}
	}

	// 同名两行 trusted **不同价** ⇒ 仍然整组扣下（红线本体不得被本修复放松）。
	if _, err := pool.Exec(ctx, `UPDATE public.credential_model_bindings
	     SET unit_price_in_per_1m = 9.0, unit_price_out_per_1m = 9.0
	   WHERE credential_id = 11 AND provider_model_id = 121`); err != nil {
		t.Fatalf("diverge trusted price: %v", err)
	}
	in.Items[0].CanonicalName = "model-e"
	if _, err := svc.CreateQuote(ctx, in, "sales01"); err == nil {
		t.Errorf("genuinely multi-priced trusted group must stay withheld")
	}
}
