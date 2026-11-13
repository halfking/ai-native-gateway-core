package bg

// market_health_checks_test.go — A-17 市场面四条健康检查（定稿 M2-T4）的
// 形状门 + 变异臂。
//
// 四条：supplier_price_above_market / market_ref_thin_coverage /
//       price_confidence_degraded / platform_region_unmapped。
//
// 判据载体是 SQL（routing_health_checks.go 的 HealthCheckDef.Query），形状门
// 把每条的**承重谓词**钉成文本断言；变异臂对**同一份判据**注入扰动 ⇒ 门必须
// 点名（守卫自己也要被审计，§675：变异要用语义变异，不是改判据字面量自身）。

import (
	"os"
	"strings"
	"testing"
)

type marketAlertSpec struct {
	checkID string
	// loadBearing 是判据的承重谓词（缺失 = 告警变成空转或恒红）。
	loadBearing []string
	// mutant 把承重谓词从 Query 里挖掉的扰动（语义变异，不是改判据常量）。
	mutant func(string) string
}

var marketAlertSpecs = []marketAlertSpec{
	{
		checkID: "supplier_price_above_market",
		loadBearing: []string{
			"public.supplier_market_position",           // 读评分 job 的落库面（不是视图重算）
			"market_position < 1.0/1.5",                 // 定稿 §4.6 种子判据（贵超 50%）
			"market_ref_platform_count >= 3",            // <3 = 降级参考，不作硬判据
			"price_confidence = 'trusted'",              // 占位价不评「贵不贵」（防噪声）
			"computed_at > now() - interval '48 hours'", // 陈旧快照不告警
		},
		mutant: func(q string) string {
			return strings.Replace(q, "AND s.market_position < 1.0/1.5", "", 1)
		},
	},
	{
		checkID: "market_ref_thin_coverage",
		loadBearing: []string{
			"public.market_price_reference",
			"overseas_min_platform_count < 3",
			"domestic_min_platform_count < 3",
			"IS NOT NULL", // 只报「有价但薄」，缺地板不报（防全量噪声，见检查注释）
		},
		mutant: func(q string) string {
			return strings.Replace(q, "AND overseas_min_platform_count < 3", "", 1)
		},
	},
	{
		checkID: "price_confidence_degraded",
		loadBearing: []string{
			"public.market_scoring_runs",                        // 961 台账（环比源）
			"latest.non_trusted_count > prev.non_trusted_count", // 环比上升判据
			"latest.run_date >= current_date - 1",               // 台账新鲜度（停跑 ⇒ 沉默）
		},
		mutant: func(q string) string {
			return strings.Replace(q,
				"AND latest.non_trusted_count > prev.non_trusted_count", "", 1)
		},
	},
	{
		checkID: "platform_region_unmapped",
		loadBearing: []string{
			"public.market_price_reference_sync", // 同步台账（syncer 写面）
			// ★ 承重标记用**带引号的 JSON 键访问形态**：裸键还会出现在 detail
			//   展示文本里（'同步台账 a17_unmapped_rows>0：…'），Contains 判据
			//   分不开「判据在」与「文案提到」（960 轮前缀误匹配同族）。
			`->> 'a17_unmapped_rows'`,
			// 48h 新鲜度门（2026-11-13 12h 修订审计轮补钉：52d444191 加了
			// `WHERE created_at > now() - interval '48 hours'` 但漏进承重清单
			// ⇒ 未来编辑可静默删掉该门而形状门不红——与 above_market 35 行
			// 的 computed_at 48h 门同纪律补齐）。
			"created_at > now() - interval '48 hours'",
		},
		mutant: func(q string) string {
			return strings.ReplaceAll(q, "'a17_unmapped_rows'", "'a17_sync_unmapped_total'")
		},
	},
}

// marketAlertQuery 按 checkID 取 Query（找不到 = fail）。
func marketAlertQuery(t *testing.T, checkID string) string {
	t.Helper()
	for _, chk := range AllHealthChecks() {
		if chk.CheckID == checkID {
			return chk.Query
		}
	}
	t.Fatalf("check %q missing from AllHealthChecks()", checkID)
	return ""
}

// marketAlertViolations 是形状门的可测内核：返回违例清单（空 = 通过）。
// 变异臂对同一份内核注入扰动 ⇒ 必须点名。
func marketAlertViolations(spec marketAlertSpec, query string) []string {
	var bad []string
	for _, lb := range spec.loadBearing {
		if !strings.Contains(query, lb) {
			bad = append(bad, "load-bearing predicate missing: "+lb)
		}
	}
	return bad
}

func TestMarketAlertChecksRegistered(t *testing.T) {
	for _, spec := range marketAlertSpecs {
		q := marketAlertQuery(t, spec.checkID)
		if bad := marketAlertViolations(spec, q); len(bad) > 0 {
			t.Errorf("check %q violations: %v\n%s", spec.checkID, bad, q)
		}
		// 健康门形状：warning + Optional（对象缺失的环境跳过而不是中止整轮）。
		for _, chk := range AllHealthChecks() {
			if chk.CheckID == spec.checkID {
				if chk.Severity != "warning" {
					t.Errorf("check %q severity = %q, want warning", spec.checkID, chk.Severity)
				}
				if !chk.Optional {
					t.Errorf("check %q must be Optional (940/961 未应用环境 42P01 跳过)", spec.checkID)
				}
			}
		}
	}
}

func TestMarketAlertChecksMutationArms(t *testing.T) {
	for _, spec := range marketAlertSpecs {
		q := marketAlertQuery(t, spec.checkID)
		if bad := marketAlertViolations(spec, q); len(bad) != 0 {
			t.Fatalf("premutation: %q already violated: %v — fix the drift first", spec.checkID, bad)
		}
		mutated := spec.mutant(q)
		if mutated == q {
			t.Fatalf("mutant for %q is a no-op (anchor missing) — mutation must really change the SQL", spec.checkID)
		}
		if bad := marketAlertViolations(spec, mutated); len(bad) == 0 {
			t.Fatalf("mutant for %q passed the shape gate — gate has no teeth", spec.checkID)
		}
	}
}

// TestMarketAlertUnmappedKeyCrossPin — platform_region_unmapped 的台账键必须与
// 同步器（scripts/market-price-sync.py）逐字同源：a17_unmapped_rows /
// a17_unmapped_by_platform。同步器改键而告警不跟 ⇒ 告警静默失明。
func TestMarketAlertUnmappedKeyCrossPin(t *testing.T) {
	py, err := os.ReadFile("../scripts/market-price-sync.py")
	if err != nil {
		t.Fatalf("read syncer: %v", err)
	}
	pySrc := string(py)
	for _, key := range []string{"a17_unmapped_rows", "a17_unmapped_by_platform"} {
		if !strings.Contains(pySrc, `"`+key+`"`) {
			t.Fatalf("syncer no longer writes %q — platform_region_unmapped check must follow", key)
		}
		q := marketAlertQuery(t, "platform_region_unmapped")
		if !strings.Contains(q, key) {
			t.Fatalf("platform_region_unmapped query lost key %q", key)
		}
	}
}
