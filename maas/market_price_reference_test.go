package maas

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestMarketPriceOverviewScanOrderMatchesSelect 钉 2026-11-12 48h 审计第四轮 P1：
// MarketPriceOverview 的 rows.Scan 参数序必须与 marketPriceOverviewSQL 的 SELECT
// 列序**逐位对齐**。缺陷形态（08af89b1f，D7，2026-11-10）：SELECT 列 26-29 =
// [credits_per_1m_in(BIGINT), credits_per_1m_out(BIGINT), COALESCE(manual_in,bool),
// COALESCE(manual_out,bool)]（值、值、旗、旗），Scan 却写成 [旗、旗、值、值] ⇒
// pgx 报 "cannot scan int8 (OID 20) in binary format into *bool" ⇒ 端点自 D7 起
// 每次调用必 500（845ba1d3b/14bc988cb 的线上冒烟都在 D7 之前 ⇒ 无人再冒烟过）。
//
// ★ 方法论：本测试是**源码级序钉**（解析 SQL 常量与 Scan 调用，不需要真库 ⇒
// 永不 SKIP）；列数/类型级的完整对拍由 pgx 在运行时 fail-loud，本测试只负责
// 「四元组尾序」这个曾经翻车过的具体形状不再漂。
func TestMarketPriceOverviewScanOrderMatchesSelect(t *testing.T) {
	src, err := os.ReadFile("market_price_reference.go")
	if err != nil {
		t.Fatalf("读源码：%v", err)
	}
	text := string(src)

	// ① SQL 侧：credits/manual 两段必须【值、值、旗、旗】序（与 007_maas_billing
	//    的 BIGINT 列 + 631 manual bool 列的类型面一致）。
	valIdx := strings.Index(text, "r.credits_per_1m_in, r.credits_per_1m_out,")
	flagIdx := strings.Index(text, "COALESCE(r.manual_in, FALSE), COALESCE(r.manual_out, FALSE),")
	if valIdx < 0 || flagIdx < 0 {
		t.Fatalf("marketPriceOverviewSQL 的 credits/manual 列段找不到（列序被改？）val=%d flag=%d", valIdx, flagIdx)
	}
	if valIdx > flagIdx {
		t.Errorf("SQL 列序漂移：credits（值）段出现在 manual（旗）段之后 ⇒ Scan 尾四元组的对齐前提不再成立")
	}

	// ② Scan 侧：尾四元组必须是【值、值、旗、旗】（manualIn/manualOut 在前）。
	//    旧缺陷形态 = &manualInFlag, &manualOutFlag, &manualIn, &manualOut。
	bad := regexp.MustCompile(`&manualInFlag,\s*&manualOutFlag,\s*&manualIn,\s*&manualOut`)
	if bad.MatchString(text) {
		t.Errorf("Scan 尾四元组仍是【旗、旗、值、值】旧缺陷序 ⇒ BIGINT 扫进 *bool 必炸（pgx 实证 cannot scan int8 into *bool）")
	}
	good := regexp.MustCompile(`&manualIn,\s*&manualOut,\s*&manualInFlag,\s*&manualOutFlag,`)
	if !good.MatchString(text) {
		t.Errorf("Scan 尾四元组找不到【值、值、旗、旗】序（&manualIn, &manualOut, &manualInFlag, &manualOutFlag）⇒ 序钉失效或被改写")
	}

	// ③ SQL 列数 == Scan 参数数（粗粒度防线：任何一列增删都必须两边同步）。
	sqlRe := regexp.MustCompile(`(?s)const marketPriceOverviewSQL = ` + "`\n(.*?)\n`")
	m := sqlRe.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("marketPriceOverviewSQL 常量提取失败")
	}
	selectList := strings.SplitN(m[1], "\n\tFROM", 2)[0]
	colCount := 0
	for _, line := range strings.Split(selectList, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "--"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.EqualFold(strings.TrimPrefix(line, "SELECT "), "") {
			continue
		}
		// 剥掉括号组（COALESCE(a, b) 的内逗号不是列分隔符）
		depth := 0
		stripped := make([]rune, 0, len(line))
		for _, r := range line {
			switch r {
			case '(':
				depth++
			case ')':
				depth--
			default:
				if depth == 0 {
					stripped = append(stripped, r)
				}
			}
		}
		clean := string(stripped)
		colCount += strings.Count(clean, ",")
		if !strings.HasSuffix(clean, ",") && !strings.HasPrefix(strings.ToUpper(clean), "SELECT") {
			colCount++
		}
	}
	scanRe := regexp.MustCompile(`(?s)rows\.Scan\(\n(.*?)\n\t\t\); err != nil`)
	sm := scanRe.FindStringSubmatch(text)
	if sm == nil {
		t.Fatalf("rows.Scan 参数块提取失败")
	}
	scanCount := 0
	for _, line := range strings.Split(sm[1], "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		scanCount += strings.Count(line, ",")
		if !strings.HasSuffix(line, ",") {
			scanCount++
		}
	}
	if colCount != scanCount {
		t.Errorf("SQL 列数=%d ≠ Scan 参数数=%d ⇒ 列序/列数漂移（MarketPriceOverview 全行必炸）", colCount, scanCount)
	}
}
