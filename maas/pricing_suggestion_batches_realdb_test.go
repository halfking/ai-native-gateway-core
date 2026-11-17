package maas

// pricing_suggestion_batches_realdb_test.go —— 「价格变更历史」批次读面的
// **真库行为门**（2026-11-17）。
//
// # 它证明什么
//
// ① 生产 SQL（pricingSuggestionBatchesSQL）+ 生产扫描函数
//    （scanPricingSuggestionBatch）在**真 schema**（975 建的
//    public.pricing_suggestion_batches）上跑得通；
// ② **可空性是真的**：真库 information_schema 说 markup_pct 可空
//    （975：`numeric(10,4)`，无 NOT NULL）⇒ 「该策略无加成率」会真的以
//    NULL 落库 ⇒ 读面必须能接住 NULL；
// ③ 接住 NULL 的含义是 **nil**（不是 0、不是整行失败）⇒ NULL≠0；
// ④ 读面**确实扫到了行**（不是空跑：门自己造的行必须被读出来）。
//
// ★ 零残留：夹具行只在**未提交事务**里存在 ⇒ ROLLBACK 后表里零行
//   （审计面无痕）。★ 门跑完还会**独立复核**残留行数 = 0。
// ★ 绝不在生产审计表留痕：pricing_suggestion_batches 是**审计记录**
//   （谁、何时、什么策略、改了几个）⇒ 写入测试数据会污染审计口径。
//   ⇒ 因此这里只用「事务 + 回滚」这一个安全形态，绝不 COMMIT。
//
// ★ 未设 TEST_DATABASE_URL 时**静默 Skip** —— Skip ≠ 通过：
//   配套的恒跑门（pricing_suggestion_batches_scan_test.go）不需要真库。

import (
	"context"
	"math"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pricingBatchesGateSentinel —— 夹具行的操作者标记（复核残留时按它查）。
const pricingBatchesGateSentinel = "__realdb_gate_pricing_suggestion_batches__"

func TestPricingSuggestionBatchesRealDBSurvivesNullMarkupPct(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — 需要真库（事务内造夹具 + 回滚）；Skip ≠ 通过（恒跑门在跑）")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连真库失败：%v", err)
	}
	defer pool.Close()

	svc := &Service{pool: pool}

	// ① 生产入口（连接池路径）：读面不许 500。
	rows, err := svc.ListPricingSuggestionBatches(ctx, 50)
	if err != nil {
		t.Fatalf("ListPricingSuggestionBatches 报错（价格变更历史整页 500）：%v", err)
	}
	t.Logf("真库读数（生产入口，生产库现存 %d 批）", len(rows))

	// ② 可空性取证：真库里 markup_pct 到底可不可空（门的前提）。
	var nullable string
	err = pool.QueryRow(ctx, `
		SELECT is_nullable
		  FROM information_schema.columns
		 WHERE table_schema = 'public'
		   AND table_name = 'pricing_suggestion_batches'
		   AND column_name = 'markup_pct'`).Scan(&nullable)
	if err != nil {
		t.Fatalf("查 information_schema 失败（975 没落库？）：%v", err)
	}
	if nullable != "YES" {
		t.Fatalf("markup_pct 的 is_nullable=%q（期望 YES）⇒ 本门的前提变了："+
			"要么迁移被改过，要么列不存在 ⇒ 需要重新评估读面", nullable)
	}

	// ③ 事务内造两行夹具（一个 markup_pct=NULL、一个 12.5）⇒ 用**生产读面**
	//    在**同一事务**里读（看得见未提交数据）⇒ 断言 ⇒ 回滚。
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("取连接失败：%v", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("开事务失败：%v", err)
	}
	rolledBack := false
	defer func() {
		if !rolledBack {
			_ = tx.Rollback(ctx)
		}
	}()

	// ★ 策略名必须过 975 的 CHECK（copy_platform/average/min/floor_markup）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.pricing_suggestion_batches
			(strategy, platform_id, dimension, markup_pct, operator, dry_run, requested)
		VALUES ('min', 'openrouter', 'standard', NULL, $1, true, 1),
		       ('floor_markup', '', 'standard', 12.5000, $1, true, 1)`,
		pricingBatchesGateSentinel); err != nil {
		t.Fatalf("插入夹具批次失败：%v", err)
	}

	batches, err := svc.listPricingSuggestionBatches(ctx, tx, 50)
	if err != nil {
		t.Fatalf("批次读面在真库上报错（价格变更历史整页 500）：%v", err)
	}

	var nullRow, valueRow *PricingSuggestionBatchRow
	for i := range batches {
		if batches[i].Operator != pricingBatchesGateSentinel {
			continue
		}
		switch batches[i].Strategy {
		case "min":
			nullRow = &batches[i]
		case "floor_markup":
			valueRow = &batches[i]
		}
	}

	// ★ 门不许空跑：两行夹具都必须被读出来。
	if nullRow == nil {
		t.Fatalf("事务内读不到 markup_pct=NULL 的夹具批次（共读到 %d 行）⇒ "+
			"读面把 NULL 行整行丢了，或读的不是同一事务的数据", len(batches))
	}
	if valueRow == nil {
		t.Fatalf("事务内读不到 markup_pct=12.5 的夹具批次（共读到 %d 行）", len(batches))
	}

	// ④ NULL≠0：NULL ⇒ nil（不是 0）；有值 ⇒ 值在。
	if nullRow.MarkupPct != nil {
		t.Fatalf("markup_pct=NULL 却读出 %v（期望 nil）⇒ NULL≠0 破："+
			"「该策略无加成率」被显示成「加成率 0」", *nullRow.MarkupPct)
	}
	if valueRow.MarkupPct == nil {
		t.Fatal("markup_pct=12.5 读出 nil ⇒ 有值被当成 NULL")
	}
	if math.Abs(*valueRow.MarkupPct-12.5) > 1e-9 {
		t.Fatalf("markup_pct 读数错：%v（期望 12.5）", *valueRow.MarkupPct)
	}

	// ⑤ 其余列必须原样回来（防止「为躲 NULL 而改坏别的列」）。
	if nullRow.Strategy != "min" || nullRow.PlatformID != "openrouter" ||
		nullRow.Dimension != "standard" {
		t.Fatalf("夹具批次其余字段扫错位：%+v", *nullRow)
	}
	if !nullRow.DryRun || nullRow.Requested != 1 {
		t.Fatalf("夹具批次的 dry_run/requested 扫错：dry_run=%v requested=%d",
			nullRow.DryRun, nullRow.Requested)
	}
	if nullRow.CreatedAt == "" {
		t.Fatal("created_at 扫空了（批次时间戳丢失 ⇒ 审计面无时间轴）")
	}

	// ⑥ 回滚 ⇒ 零残留。
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("回滚失败：%v", err)
	}
	rolledBack = true

	var leftover int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.pricing_suggestion_batches
		 WHERE operator = $1`, pricingBatchesGateSentinel).Scan(&leftover); err != nil {
		t.Fatalf("复核残留失败：%v", err)
	}
	if leftover != 0 {
		t.Fatalf("回滚后仍有 %d 行夹具残留 ⇒ 本门污染了生产审计表", leftover)
	}
	t.Logf("真库读数：夹具 2 行（NULL / 12.5）全部读出，NULL⇒nil 成立；回滚后残留 %d 行", leftover)
}
