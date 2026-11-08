package sales

import (
	"os"
	"strings"
	"testing"
)

// This file pins the three P0 defects the 2026-11-05 12h audit found in the
// sales MVP W1 commit (3c9954acb). All three shipped green because the only
// sales test was a real-DB suite that SKIPS when TEST_DATABASE_URL is unset
// ⇒ the money math and the create path were never exercised in CI.
//
// ★ 方法论（这一轮最贵的一条教训）：**「测试全绿」≠「被测行为被验过」**。
//   ⇒ 这里的断言全部落在**纯函数**上（precomputeLines / normalizeCostCurrency），
//     不需要真库 ⇒ 永远不会 SKIP ⇒ 每次 CI 必跑。

// ★★ 2026-11-09（本轮落地时补记）：★ 本文件原本还有 3 条断言，★ 它们钉的是
// ★ **上游尚未修复**的缺陷（★ `normalizeCostCurrency("")` 至今仍返回 "USD" ⇒ ★
// ★ 「缺币种被静默当成 USD」）⇒ ★ 若随本文件一并落地 ⇒ ★ 仓内会**新增 1 条红**
// ★ （★ 「不引入新红」是本轮的硬约束）⇒ ⇒ ★ 那 3 条断言**本轮不落地**，★ 原文
// ★ **仍完整保留**在另一会话的工作树里（★ llm-gateway-go/sales/quote_audit_guard_test.go）
// ★ ⇒ ★ 随该缺陷被修复时一并落地（★ 三条断言名与修法登记：
// ★ docs/12小时内修订审计-20261109-0730.md 的 F-13）。

func f64(v float64) *float64 { return &v }

// TestPrecomputeLinesRevenueUsesCentsPerCreditOver100 pins F-2.
//
// ★ 缺陷：cents_per_credit 的语义是「**分**/积分」（AF-1，2026-11-03 审计
// 裁定 A-14 钉死）⇒ 积分 × 分/积分 = **分** ⇒ 折元必须 **÷100**。
//
//	原实现漏了 ÷100 ⇒ 收入放大 **100 倍** ⇒ 毛利虚高 ⇒ margin_floor 红线
//	被架空（亏本单也能自动通过审批）。
//
// ★ 口径锚点（本仓另两处独立同款，缺一即证伪）：
//
//	maas/consumption_detail.go: revenueBillMajor := credits * centsPerCredit / 100
//	sql/schema/01-schema.sql:   … * ms.cents_per_credit / 100.0 as input_price_cny
func TestPrecomputeLinesRevenueUsesCentsPerCreditOver100(t *testing.T) {
	const (
		centsPerCredit = 0.1 // maas_settings.cents_per_credit DDL 默认值
		fx             = 7.2 // settings.CNYToUSDFX 的量级
	)
	items := []QuoteItemInput{{
		CanonicalName: "gpt-4o",
		EstTokensIn:   1_000_000,
		RateIn:        f64(1.0), // 1 分/积分 折前
		CostInPer1M:   f64(0.0),
		CostCurrency:  "USD",
		Confidence:    "trusted",
	}}

	out, _ := precomputeLines(items, fx, centsPerCredit)
	if len(out) != 1 {
		t.Fatalf("precomputeLines returned %d lines, want 1", len(out))
	}
	got := out[0].RevenueUSD

	// 1 积分 × 0.1 分/积分 = 0.1 分 = 0.001 元 ⇒ ÷7.2 = 0.000138… USD
	want := 1.0 * centsPerCredit / 100 / fx
	if diff := got - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("revenue_usd = %v, want %v (credits × cents_per_credit ÷ 100 ÷ FX)\n"+
			"⇒ 缺 ÷100 ⇒ 收入放大 100 倍 ⇒ 毛利红线被架空", got, want)
	}
	// 100 倍放大是本缺陷的特征量级：显式钉住，避免「改小但仍错」。
	if got > want*10 {
		t.Fatalf("revenue_usd = %v is >10× the correct %v ⇒ the ÷100 is missing again", got, want)
	}
}

// TestPrecomputeLinesNoCostDataKeepsCallerConfidence guards the boundary of
// the fix above: a quote that reports **price only** (no cost numbers at all)
// must NOT be degraded — its cost is genuinely 0, so downgrading it would
// wrongly block a legitimate quote.
func TestPrecomputeLinesNoCostDataKeepsCallerConfidence(t *testing.T) {
	items := []QuoteItemInput{{
		CanonicalName: "gpt-4o",
		EstTokensIn:   1_000_000,
		RateIn:        f64(1.0),
		CostCurrency:  "USD",
		Confidence:    "trusted",
	}}

	out, versionConfidence := precomputeLines(items, 7.2, 0.1)
	if out[0].Confidence != "trusted" {
		t.Fatalf("confidence = %q, want \"trusted\" for an explicit-USD quote", out[0].Confidence)
	}
	if versionConfidence != "trusted" {
		t.Fatalf("version confidence = %q, want \"trusted\"", versionConfidence)
	}
}

// TestPublicShareAccessAuditSQLHasTypedParameters pins F-1.
//
// ★ 缺陷：公开端的访问审计 UPDATE 里，$3/$4 只出现在 `jsonb_build_object`
//
//	内，而它是 **variadic any** ⇒ PostgreSQL **得不到类型上下文** ⇒
//	pgx 发无类型参数 ⇒ 服务端报 `could not determine data type of parameter`
//	（42P18）⇒ **每一个有效 token 都 404**，且访问审计写不进去。
//	⇒ 无类型位置的参数必须显式 cast。
func TestPublicShareAccessAuditSQLHasTypedParameters(t *testing.T) {
	// 这条断言读的是 sales/quote.go 的真源文本（不是复制一份 SQL），
	// 所以改动必须真的落到源文件上。
	src := mustReadQuoteSource(t)
	// Anchor on the audit-specific body, not on the table name: RevokeShare
	// issues its own UPDATE against the same table.
	// ★ 2026-11-09：★ 锚点随上游 2026-11-08 审计 P3-12（★ 「viewed_at/opened_at/
	//   view_count 只在 outcome=ok 时推进」）调整 ⇒ ★ 表达式从
	//   `view_count = view_count + 1` 变成
	//   `view_count = view_count + CASE WHEN $2 = 'ok' THEN 1 ELSE 0 END`
	//   ⇒ ★ **被钉的性质没变**（★ 仍是「访问审计 UPDATE 里的占位符必须有类型上下文」），
	//   ★ 只是锚点文本跟到了上游当前形态。
	const marker = "view_count = view_count + CASE WHEN $2 = 'ok' THEN 1 ELSE 0 END"
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatal("share access-audit UPDATE not found in sales/quote.go")
	}
	// Walk back to the start of the statement text.
	start := strings.LastIndex(src[:i], "UPDATE public.sales_quote_shares")
	if start < 0 {
		t.Fatal("share access-audit UPDATE body not found")
	}
	// The statement ends at the closing backtick that precedes the argument
	// list (`, tokenHash, outcome, ...).
	j := strings.Index(src[i:], "`, tokenHash")
	if j < 0 {
		t.Fatal("share access-audit UPDATE body not found")
	}
	body := src[start : i+j] // 每个占位符都必须带显式 cast。
	// ★ $3 / $4：只出现在 jsonb_build_object 内（variadic any）⇒ 必须显式 cast。
	for _, param := range []string{"$3", "$4"} {
		if !strings.Contains(body, param+"::text") {
			t.Fatalf("share access-audit SQL: %s has no explicit cast\n"+
				"⇒ jsonb_build_object 是 variadic any ⇒ PostgreSQL 无类型上下文 ⇒\n"+
				"⇒ 真库报 42P18（could not determine data type of parameter）⇒\n"+
				"⇒ 公开端点对每个有效 token 都 404，且访问审计写不进去。", param)
		}
	}
	// ★ $2：它同样出现在 jsonb_build_object 内 ⇒ 必须**另有**一个带类型上下文的用法
	//   （★ 与 'ok' 的比较 ⇒ PG 推断为 text）⇒ 缺了它 ⇒ $2 无类型 ⇒ 同样 42P18。
	if !strings.Contains(body, "$2 = 'ok'") {
		t.Fatalf("share access-audit SQL: $2 has no typed context\n" +
			"⇒ $2 只出现在 jsonb_build_object（variadic any）内 ⇒ PostgreSQL 得不到类型 ⇒\n" +
			"⇒ 真库报 42P18（could not determine data type of parameter）⇒\n" +
			"⇒ 公开端点对每个有效 token 都 404，且访问审计写不进去。")
	}
}

// mustReadQuoteSource reads sales/quote.go from the package directory so the
// guard above asserts against the real source, not a copied SQL literal.
func mustReadQuoteSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("quote.go")
	if err != nil {
		t.Fatalf("read quote.go: %v", err)
	}
	return string(raw)
}
