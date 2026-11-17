package maas

// `MarketPriceOverview` 的**真库行为门**（F12，2026-11-17）。
//
// # 它证明什么
//
// 生产事故：`GET /api/admin/maas/market-price-overview` 对**任何调用方**都 500，
// 报错 `can't scan into dest[24] (col: s_cur): cannot scan NULL into *string`
// ⇒ `/operations/market-prices` 页顶部的摘要区常驻「加载失败」，`/operations/
// price-compare` 的「定价参考」页签同源同病。
//
// 根因：SELECT 的 `s_cur` 来自 `LEFT JOIN LATERAL` ⇒ **模型在
// credential_model_bindings 里没有带价绑定时该列为 NULL** ⇒ 扫进裸 `*string` 炸。
// 211 生产实测：375 个已解析模型里 **353 个（94%）** 无带价绑定；overview 前
// 50 行里 **41 行** s_cur=NULL ⇒ 第一次扫描就整体失败。
//
// # 为什么是真库门而不是源码门
//
// 源码门（market_price_reference_null_safety_test.go）只能证明「形状对了」；
// 本门**跑生产函数本体**（`Service.MarketPriceOverview`）打真库 ⇒ 证明
// ① 端点不再 500；② 「模型无带价绑定」这条路径**真的被走到了**（否则门空跑）；
// ③ 币种未知 ⇒ 不产出位势比（NULL≠0）。
//
// ★ 只读：本门只发 SELECT（MarketPriceOverview / GetSettings 皆只读）⇒
//   可以在生产库上跑，**不写任何一行**。
//
// ★ 未设 TEST_DATABASE_URL 时**静默 Skip** —— Skip 不等于通过：
//   配套的源码门不需要真库、永远在跑。两者缺一，结论都不成立。

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMarketPriceOverviewSurvivesModelsWithoutSupplierBindings(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — 需要真库（只读 SELECT）；Skip ≠ 通过（源码门在跑）")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连真库失败：%v", err)
	}
	defer pool.Close()

	svc := &Service{pool: pool}
	rows, err := svc.MarketPriceOverview(ctx, "", false, 200)
	if err != nil {
		t.Fatalf("MarketPriceOverview 报错（生产 500 的复现路径）：%v", err)
	}
	if len(rows) == 0 {
		t.Fatal("空结果 ⇒ 门空跑（真库里 market_price_reference 无已解析行？）")
	}

	unknownCur, knownCur := 0, 0
	for _, r := range rows {
		if r.SupplierCur == "" {
			unknownCur++
			// 币种未知 ⇒ 实付价不可比 ⇒ 不得产出位势比（NULL≠0，不猜）。
			if r.MarketPosition != nil {
				t.Errorf("模型 %s 币种未知却产出了 market_position=%v ⇒ 无可比性却出结论",
					r.CanonicalName, *r.MarketPosition)
			}
			continue
		}
		knownCur++
		// 有币种但不是 CNY ⇒ 同样不产出位势比（实付非 CNY 不默认折算）。
		if r.SupplierCur != "CNY" && r.MarketPosition != nil {
			t.Errorf("模型 %s 实付币种 %s（非 CNY）却产出了 market_position=%v",
				r.CanonicalName, r.SupplierCur, *r.MarketPosition)
		}
	}

	// ★ 量具不许腐化：必须有行真的走到「币种未知」这条路径，否则本门
	//   退化成「端点没炸」的弱断言（数据一变就会空跑）。
	if unknownCur == 0 {
		t.Fatalf("200 行里没有任何一行币种未知 ⇒ 「模型无带价绑定」路径未被覆盖 ⇒ " +
			"门腐化（真库数据变了？）——本门必须继续钉住该路径")
	}
	if knownCur == 0 {
		t.Errorf("200 行里没有任何一行带币种 ⇒ 取数口径异常（COALESCE 是否被过度应用？）")
	}
	t.Logf("真库读数：%d 行，其中币种未知 %d 行、带币种 %d 行", len(rows), unknownCur, knownCur)
}
