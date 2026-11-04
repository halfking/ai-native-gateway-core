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
// 未设置该环境变量时整体 SKIP（仓内真库判据同款）。夹具建在隔离 schema，
// 从不写 public 业务行。

import (
	"context"
	"encoding/json"
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
	for _, key := range []string{"TEST_DATABASE_URL", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
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
INSERT INTO public.providers (id) VALUES (1);
INSERT INTO public.credentials (id, provider_id) VALUES (10, 1), (11, 1);
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
		for _, stmt := range []string{
			"DELETE FROM public.credential_model_bindings",
			"DELETE FROM public.provider_models",
			"DELETE FROM public.credentials",
			"DELETE FROM public.providers",
			"DELETE FROM public.sales_supply_annotations",
			"DELETE FROM public.sales_demand_cards",
			"DELETE FROM public.sales_quotes",
			"DELETE FROM public.maas_settings",
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
	defer pool.Close()
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)

	svc := NewService(pool)
	list, err := svc.ListSellable(ctx)
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
	list2, err := svc.ListSellable(ctx)
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
	list3, err := svc.ListSellable(ctx)
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
	defer pool.Close()
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)

	svc := NewService(pool)

	// 充值锚点给正数，避免 cents 默认分歧（无所谓具体值，单源读它）。
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 0.01)
		ON CONFLICT (id) DO UPDATE SET cents_per_credit = EXCLUDED.cents_per_credit`); err != nil {
		t.Fatalf("seed cents: %v", err)
	}

	// 成本 $140（1.0×100 + 2.0×20），卖价（credits 10/20, 折 0）= 10×100+20×20 = 1400 credits
	// = 14 CNY = ~1.94 USD ⇒ 毛利为负且金额 < $500 且无折扣 ⇒ 但毛利下限会触发！
	// ⇒ 用更大的量让毛利为正：成本不变、tokens 加大只会同向……
	// ⇒ 真正的 in-policy 样本 = 卖价显著高于成本：rate 100/200 ⇒ 14000 credits
	//    = 140 CNY ≈ 19.4 USD… 仍低于 $500（order_amount 触审线），毛利 86%+。
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
	defer pool.Close()
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	svc := NewService(pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 0.01)
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
	defer pool.Close()
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	svc := NewService(pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 0.01)
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
	// sent 后修订 ⇒ v2，v1 被 superseded。
	in2 := in
	in2.Items = append([]QuoteItemInput(nil), in.Items...)
	in2.Items[0].EstTokensIn = 200_000_000
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

// TestRealDBSharePublicProjection —— token 往返 + 公开面响应体断言 +
// 过期/撤销不可读 + 访问审计。
func TestRealDBSharePublicProjection(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, realdbDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	salesFixture(t, ctx, pool)
	seedSellable(t, ctx, pool)
	svc := NewService(pool)
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_settings (id, cents_per_credit) VALUES (1, 0.01)
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
	// 卖价在场（折扣轴随基础价一起展示，决议 C）。
	if !strings.Contains(bs, "rate_in") || !strings.Contains(bs, "discount_pct") {
		t.Errorf("public body should carry the offer (rate_in/discount_pct): %s", bs)
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

	// 撤销 ⇒ 不可读。
	if err := svc.RevokeShare(ctx, link.ID); err != nil {
		t.Fatalf("RevokeShare: %v", err)
	}
	if _, err := svc.GetPublicQuote(ctx, link.Token, "1.2.3.4", "ua"); err == nil {
		t.Errorf("revoked token must not resolve")
	}
	// 过期 ⇒ 不可读。
	link2, err := svc.CreateShare(ctx, q.ID, "李四", "li@example.com", -time.Minute, "sales01")
	if err != nil {
		t.Fatalf("CreateShare(expired): %v", err)
	}
	if _, err := svc.GetPublicQuote(ctx, link2.Token, "1.2.3.4", "ua"); err == nil {
		t.Errorf("expired token must not resolve")
	}
}

func ptrF(f float64) *float64 { return &f }
