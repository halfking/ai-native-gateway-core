package maas

// market-price-overview 的 **NULL 安全契约门**（F12，2026-11-17）。
//
// # 它钉的是什么
//
// `handleMaasMarketPriceOverview` 的 SELECT 里有一列是**可空字符串**：
// `s_cur` 来自 `LEFT JOIN LATERAL`（该模型在 credential_model_bindings 里
// **没有带价绑定**时 ⇒ LATERAL 无行 ⇒ s_cur 为 NULL）。Go 侧把它扫进
// **裸 `*string`** ⇒ pgx 报 `cannot scan NULL into *string` ⇒ 端点对**任何
// 调用方**都 500（211 生产实证：375 个已解析模型里 353 个（94%）无带价
// 绑定；overview 前 50 行里 41 行 s_cur=NULL ⇒ 第一次扫描就炸）。
//
// ★ 这不是新洞：14bc988cb 曾对**同一列**修过（当时的 SQL 形态是
//   `min(b.currency)`）；08af89b1f 重写 D6 的 LATERAL 子查询（改成
//   `GROUP BY bb.currency` + `is_cny` 优先）时把那个 COALESCE 丢了 ⇒
//   **回归**。历史修复被后续重构吞掉，是本门存在的理由。
//
// # 为什么要有「真库腿」之外的这道源码门
//
// 真库腿（market_price_reference_realdb_test.go）跑生产函数本体，但它在
// TEST_DATABASE_URL 未设置时**静默 Skip** ⇒ 「没人凭一次 go test 说出它
// 跑没跑」。本门不需要真库 ⇒ 永远有东西在守。两者缺一，结论都不成立。
//
// # 本门是**不变量**门，不是形状锁
//
// 允许两种合法形态（任一即可）：
//   (a) SQL 侧 `COALESCE(s.s_cur, '')` ＋ Go 侧扫进裸 `*string`；
//   (b) SQL 侧原样 NULL ＋ Go 侧扫进 NULL 安全容器（sql.NullString 等）。
// 两者都**不能**是「SQL 原样 NULL ＋ Go 侧裸 *string」——那正是 500 的形状。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const overviewSQLFile = "market_price_reference.go"

// marketPriceOverviewSQLText 取出 SQL 常量文本。
func marketPriceOverviewSQLText(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(overviewSQLFile)
	if err != nil {
		t.Fatalf("读 %s：%v", overviewSQLFile, err)
	}
	text := string(src)
	re := regexp.MustCompile("(?s)const marketPriceOverviewSQL = `\n(.*?)\n`")
	m := re.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("marketPriceOverviewSQL 常量提取失败（SQL 被改写？）")
	}
	return text
}

// TestMarketPriceOverviewSupplierCurrencyIsNullSafe 钉住「扫进裸字符串的那一列
// 绝不能是 NULL」这个不变量（= 500 的直接成因）。
func TestMarketPriceOverviewSupplierCurrencyIsNullSafe(t *testing.T) {
	text := marketPriceOverviewSQLText(t)

	// ① SQL 侧：s_cur 是否 NULL 安全（COALESCE 成空串 = 币种未知，不是 0）。
	sqlNullSafe := strings.Contains(text, "COALESCE(s.s_cur, '')") ||
		regexp.MustCompile(`COALESCE\(\s*s\.s_cur\s*,`).MatchString(text)

	// ② Go 侧：SupplierCur 的扫描目的地是不是 NULL 安全容器。
	//    裸 `&r.SupplierCur`（*string）= 不安全；`&supplierCur`（sql.NullString）
	//    或 `&r.SupplierCur` 之前有 NULL 安全包装 = 安全。
	scanBlock := regexp.MustCompile(`(?s)rows\.Scan\((.*?)\); err != nil`).FindString(text)
	if scanBlock == "" {
		t.Fatalf("rows.Scan 调用块提取失败（扫描被重构？）")
	}
	bareScan := strings.Contains(scanBlock, "&r.SupplierCur")
	nullSafeScan := regexp.MustCompile(`&\w*[Ss]upplierCur\w*`).MatchString(scanBlock) && !bareScan

	if !sqlNullSafe && !nullSafeScan {
		t.Fatalf(
			"SupplierCur 列既没有 SQL 侧 NULL 保护（缺 COALESCE(s.s_cur, '')）" +
				"也没有 Go 侧 NULL 安全目的地（仍扫进裸 *string）⇒ " +
				"模型无带价绑定时 pgx 报 \"cannot scan NULL into *string\" ⇒ " +
				"market-price-overview 对所有调用方 500（211 生产实证：353/375 已解析模型无带价绑定）")
	}

	// ③ 列序不许被 COALESCE 改动：Scan 的 dest[22]=s_in / dest[23]=s_out /
	//    dest[24]=s_cur（顺序变 ⇒ 整行扫错位）。
	if !strings.Contains(text, "s.s_in, s.s_out, COALESCE(s.s_cur, '')") &&
		!strings.Contains(text, "s.s_in, s.s_out, s.s_cur") {
		t.Errorf("LATERAL 三列的顺序漂移：期望 s.s_in, s.s_out, <s_cur>（Scan dest[22..24] 对齐前提）")
	}
}

// TestMarketPriceOverviewIsCnyOrderingIsNullSafe 钉住同族的第二个 NULL 陷阱：
// `(bb.currency = 'CNY')` 在 currency 为 NULL 时结果是 **NULL 而不是 FALSE**，
// 而 Postgres 的 `ORDER BY is_cny DESC` 默认 **NULLS FIRST** ⇒ 「币种未知」的组
// 会排在真 CNY 组**前面** ⇒ 明明有 CNY 价却挑出未知币种组。
//
// 当前生产库 NULL 币种绑定实测为 0（所以这条今天不发作），但它与 500 同族
// （NULL 不当真值）⇒ 钉死，免得数据一变就复发。
func TestMarketPriceOverviewIsCnyOrderingIsNullSafe(t *testing.T) {
	text := marketPriceOverviewSQLText(t)

	ok := strings.Contains(text, "COALESCE((bb.currency = 'CNY'), FALSE)") ||
		regexp.MustCompile(`COALESCE\(\s*\(\s*bb\.currency\s*=\s*'CNY'\s*\)\s*,\s*FALSE\s*\)`).MatchString(text)
	if !ok {
		t.Errorf(
			"is_cny 仍是 `(bb.currency = 'CNY')` ⇒ currency 为 NULL 时结果是 NULL " +
				"⇒ ORDER BY is_cny DESC（NULLS FIRST）会让未知币种组压过真 CNY 组 " +
				"⇒ 修法：COALESCE((bb.currency = 'CNY'), FALSE)")
	}
}
