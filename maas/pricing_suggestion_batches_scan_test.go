package maas

// pricing_suggestion_batches_scan_test.go —— 「价格变更历史」批次读面的
// **恒跑行为门**（2026-11-17）。
//
// # 它钉的是什么
//
// 批次留痕表 pricing_suggestion_batches（975 迁移）有一列**可空**：
// `markup_pct numeric(10,4)` —— **没有 NOT NULL**。这是设计如此：
// copy_platform / average / min 三个策略本来就没有「加成率」⇒ 写入的就是 NULL。
//
// 若扫描目的地退化成裸 `float64` ⇒ pgx 报
// `cannot scan NULL into *float64` ⇒ **批次读面整体 500**
// ⇒ 「价格变更历史」整页不可用（= F12/F13 同族：对所有调用方都炸）。
// 若退化成「NULL 当 0」⇒ 审计页把「无加成率」显示成「加成 0%」⇒ NULL≠0 破。
//
// ★ 判据是「目的地对 NULL 安全」，不是「字段名写没写对」——
//   所以本门**喂 NULL 并断言结果**，不靠读源码猜。
//
// # 为什么这道门不接数据库也有牙
//
// ★ 门里**不用 mock**：pgxmock 对 NULL 宽容（F13 的教训：mock 守不住
//   「不再 500」）⇒ 这里用 **pgx 自己的 pgtype codec**（`pgtype.Map.Scan`）
//   —— 这就是 pgx 生产路径的同一套扫描计划
//   （TryPointerPointerScanPlan / TryWrapBuiltinTypeScanPlan）。
// ★ 更关键：假件收到的**目的地列表来自生产扫描函数本身**
//   （`scanPricingSuggestionBatch`）⇒ 门测的是**生产代码**，不是复制品；
//   把生产目的地改回裸 float64 ⇒ 本门立刻红并复现生产同款报错原文。
//
// # 与真库门的关系
//
//   · 本门（恒跑，不需要真库）：钉「扫描目的地对 NULL 安全」。
//   · maas/pricing_suggestion_batches_realdb_test.go（真库）：钉「生产 SQL +
//     扫描」在真 schema 上真的跑得通（且零残留）。
//   两者缺一，结论都不成立（真库门在 TEST_DATABASE_URL 未设时静默 Skip）。

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// batchRowCodecFake —— 用 **pgx 真 codec** 扫描一行的假件。
//
// ★ 列的类型由「目的地 Go 类型」反推（int64→int8、int→int4、string→text、
//
//	bool→bool、float64→numeric、time.Time→timestamptz、[]byte→jsonb）。
//	这样列类型表不会随生产 SELECT 改列而腐化（改列 ⇒ 目的地类型变 ⇒ 自动跟随），
//	而「哪一列可空」是**已知契约**（975：只有 markup_pct 可空）⇒ 显式写在夹具里。
type batchRowCodecFake struct {
	values [][]byte // 每列的原始文本字节（nil = SQL NULL）
}

// Scan —— 假装自己是 pgx.Rows 的一行，用 pgx 的扫描计划把字节灌进目的地。
func (f *batchRowCodecFake) Scan(dest ...any) error {
	if len(dest) != len(f.values) {
		return fmt.Errorf("假件列数 %d ≠ 生产目的地数 %d（生产 SELECT 列集变了 ⇒ 门要更新）",
			len(f.values), len(dest))
	}
	m := pgtype.NewMap()
	for i, d := range dest {
		oid, err := batchColumnOID(d)
		if err != nil {
			return fmt.Errorf("列 %d：%w", i, err)
		}
		// ★ 文本格式：二进制/文本两条路径对 NULL 的判定完全相同
		//   （src==nil ⇒ 同一个 ScanFloat64(Float8{}) ⇒ 同一个报错原文）。
		if err := m.Scan(oid, pgtype.TextFormatCode, f.values[i], d); err != nil {
			return fmt.Errorf("列 %d（oid=%d）扫描失败：%w", i, oid, err)
		}
	}
	return nil
}

// batchColumnOID —— 由目的地 Go 类型反推列 OID。
func batchColumnOID(dest any) (uint32, error) {
	t := reflect.TypeOf(dest)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil {
		return 0, fmt.Errorf("目的地类型不可判定：%T", dest)
	}
	if t == reflect.TypeOf(time.Time{}) {
		return pgtype.TimestamptzOID, nil
	}
	if t == reflect.TypeOf([]byte{}) {
		return pgtype.JSONBOID, nil
	}
	switch t.Kind() {
	case reflect.Int64:
		return pgtype.Int8OID, nil
	case reflect.Int32:
		return pgtype.Int4OID, nil
	case reflect.Int:
		return pgtype.Int4OID, nil
	case reflect.String:
		return pgtype.TextOID, nil
	case reflect.Bool:
		return pgtype.BoolOID, nil
	case reflect.Float64:
		return pgtype.NumericOID, nil
	}
	return 0, fmt.Errorf("目的地类型 %s 没有对应的列 OID（门要更新）", t)
}

// batchRowValues —— 一行批次的夹具值（列序与生产 SELECT 逐字对齐：
// id, created_at, strategy, platform_id, dimension, markup_pct,
// operator, dry_run, requested, applied, skipped, detail）。
func batchRowValues(markupPct []byte) [][]byte {
	return [][]byte{
		[]byte("42"),                   // id
		[]byte("2026-11-17 10:00:00Z"), // created_at（pgx 文本格式的 timestamptz 布局）
		[]byte("min"),                  // strategy
		[]byte("openrouter"),           // platform_id
		[]byte("standard"),             // dimension
		markupPct,                      // markup_pct（nil = SQL NULL）
		[]byte("super_admin"),          // operator
		[]byte("f"),                    // dry_run
		[]byte("3"),                    // requested
		[]byte("2"),                    // applied
		[]byte("1"),                    // skipped
		[]byte(`{"note":"x"}`),         // detail
	}
}

// TestPricingSuggestionBatchScanSurvivesNullMarkupPct —— 可空的 markup_pct
// 扫成 nil（不是 0、也不是整行失败）。
func TestPricingSuggestionBatchScanSurvivesNullMarkupPct(t *testing.T) {
	fake := &batchRowCodecFake{values: batchRowValues(nil)}

	row, err := scanPricingSuggestionBatch(fake)
	if err != nil {
		t.Fatalf("扫描 markup_pct=NULL 的批次行失败（生产同款 500：%v）", err)
	}
	if row.MarkupPct != nil {
		t.Fatalf("markup_pct=NULL 却读出 %v（期望 nil）⇒ NULL≠0 破："+
			"「无加成率」被当成「加成率 0」", *row.MarkupPct)
	}
	// 其余列必须原样回来（防止「为躲 NULL 而改坏别的列」）。
	if row.ID != 42 || row.Strategy != "min" || row.PlatformID != "openrouter" ||
		row.Dimension != "standard" || row.Operator != "super_admin" {
		t.Fatalf("批次行其余字段扫错位：%+v", row)
	}
	if row.DryRun || row.Requested != 3 || row.Applied != 2 || row.Skipped != 1 {
		t.Fatalf("批次行计数/布尔扫错位：%+v", row)
	}
	if row.CreatedAt != "2026-11-17T10:00:00Z" {
		t.Fatalf("created_at 扫错：%q", row.CreatedAt)
	}
}

// TestPricingSuggestionBatchScanKeepsMarkupPctValue —— 有加成率时不能被当成 nil。
func TestPricingSuggestionBatchScanKeepsMarkupPctValue(t *testing.T) {
	fake := &batchRowCodecFake{values: batchRowValues([]byte("12.5000"))}

	row, err := scanPricingSuggestionBatch(fake)
	if err != nil {
		t.Fatalf("扫描 markup_pct=12.5 的批次行失败：%v", err)
	}
	if row.MarkupPct == nil {
		t.Fatal("markup_pct=12.5 读出 nil ⇒ 有值被当成 NULL")
	}
	if math.Abs(*row.MarkupPct-12.5) > 1e-9 {
		t.Fatalf("markup_pct 读数错：%v（期望 12.5）", *row.MarkupPct)
	}
}

// TestPricingSuggestionBatchMarkupPctIsPointer —— 结构性钉：markup_pct 的
// 字段类型必须是**指针**（可空列 ⇒ 目的地是 **T 才能接 NULL）。
//
// ★ 这一条是给「结构简化型」回归兜底的：有人觉得「加成率就是个数」
//
//	而把它改成 float64 ⇒ 上面的行为门会红，但那时已经有人在生产上炸过一轮；
//	本条让它在**结构层**就被拦下。
func TestPricingSuggestionBatchMarkupPctIsPointer(t *testing.T) {
	f, ok := reflect.TypeOf(PricingSuggestionBatchRow{}).FieldByName("MarkupPct")
	if !ok {
		t.Fatal("PricingSuggestionBatchRow 没有 MarkupPct 字段（结构被改了？）")
	}
	if f.Type.Kind() != reflect.Ptr {
		t.Fatalf("MarkupPct 的类型是 %s（期望指针）⇒ 可空列会被裸类型接走 ⇒ "+
			"pgx 报 \"cannot scan NULL into *%s\"", f.Type, f.Type)
	}
}
