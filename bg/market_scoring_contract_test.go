package bg

// market_scoring_contract_test.go — 契约门：评分 job 读 SQL 的三个承重片段
// 必须与 959 视图（v_supplier_price_vs_baseline）**逐字同源**（归一化空白后
// 文本相等）。
//
// 为什么要有这扇门：C1 需要的 vendor_list_* / fx_rate 不在视图里（视图
// append-only 冻结），评分读 SQL 只能复刻视图的 mc/mpr LATERAL join 与
// C3 置信度 CASE。复刻体与被复刻体没有任何机器关系时必然漂移（§684
// 「手写清单 vs 代码输出」同款）——本门把两者钉死：改 959 的 join 谓词而
// 不同步评分读 SQL（或反过来）⇒ 红。
//
// 门自身的牙（守卫自己也要被审计，§675）：带一个**变异臂自测**——对片段
// 注入一个语义扰动后比较器必须判不等，防「比较器恒真」的空门。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var wsRe = regexp.MustCompile(`\s+`)

func normalizeSQL(s string) string {
	return strings.TrimSpace(wsRe.ReplaceAllString(s, " "))
}

// extractBetween 取 [start, end] 的闭区间文本（含两端锚；end 取 start 之后
// 第一次出现）。找不到 ⇒ 报错（fail-closed：读不到判据 ≠ 没发现问题）。
func extractBetween(t *testing.T, src, start, end, label string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("contract: anchor start %q not found in %s", start, label)
	}
	j := strings.Index(src[i:], end)
	if j < 0 {
		t.Fatalf("contract: anchor end %q not found in %s after %q", end, label, start)
	}
	return src[i : i+j+len(end)]
}

const (
	anchorMCStart  = "WHERE mc.id = pm.canonical_id"
	anchorMCEnd    = "LIMIT 1"
	anchorMPRStart = "WHERE mpr.dimension = 'standard'"
	anchorMPREnd   = "LIMIT 1"
	anchorC3Start  = "WHEN COALESCE(cmb.currency, 'USD') NOT IN ('USD', 'CNY')"
	anchorC3End    = "ELSE 'trusted'"
)

func loadMarketScoringSources(t *testing.T) (view959, jobSQL string) {
	t.Helper()
	vb, err := os.ReadFile("../sql/migrations/startup/959_market_dual_floor_a17.sql")
	if err != nil {
		t.Fatalf("read 959: %v", err)
	}
	jb, err := os.ReadFile("market_scoring.go")
	if err != nil {
		t.Fatalf("read market_scoring.go: %v", err)
	}
	return string(vb), string(jb)
}

func TestMarketScoringSQLFragmentsMatch959(t *testing.T) {
	view959, jobSQL := loadMarketScoringSources(t)

	cases := []struct {
		name       string
		start, end string
	}{
		{"mc LATERAL join 谓词（canonical 解析与 dedup 顺序）", anchorMCStart, anchorMCEnd},
		{"mpr LATERAL join 谓词（canonical_id 排除分支 + AF-8 dedup）", anchorMPRStart, anchorMPREnd},
		{"C3 置信度 CASE（四档门）", anchorC3Start, anchorC3End},
	}
	for _, c := range cases {
		want := normalizeSQL(extractBetween(t, view959, c.start, c.end, "959 view"))
		got := normalizeSQL(extractBetween(t, jobSQL, c.start, c.end, "market_scoring.go"))
		if got != want {
			t.Errorf("fragment %q drifted from 959 view:\n  job:  %s\n  959:  %s", c.name, got, want)
		}
	}
}

// TestMarketScoringContractComparatorHasTeeth — 守卫自己的牙：把 job 片段
// 注入语义扰动（mpr 排除分支被删 / C3 档位改写 / mc dedup 顺序翻转）后，
// 比较器必须判不等。比较器恒真 ⇒ 本门是空的。
func TestMarketScoringContractComparatorHasTeeth(t *testing.T) {
	view959, jobSQL := loadMarketScoringSources(t)
	got := normalizeSQL(extractBetween(t, jobSQL, anchorMPRStart, anchorMPREnd, "market_scoring.go"))
	want := normalizeSQL(extractBetween(t, view959, anchorMPRStart, anchorMPREnd, "959 view"))
	if got != want {
		t.Fatalf("premutation: fragments already unequal — fix the drift first")
	}

	mutants := []struct {
		name    string
		perturb func(string) string
	}{
		{"删掉 canonical_id 排除分支", func(s string) string {
			return strings.Replace(s,
				"AND (mpr.canonical_id = mc.id OR (mpr.canonical_id IS NULL AND lower(mpr.canonical_name) = lower(mc.canonical_name)))",
				"AND lower(mpr.canonical_name) = lower(mc.canonical_name)", 1)
		}},
		{"dedup 顺序翻转（名字优先于 id）", func(s string) string {
			return strings.Replace(s, "ORDER BY (mpr.canonical_id IS NOT NULL) DESC", "ORDER BY (mpr.canonical_id IS NOT NULL) ASC", 1)
		}},
	}
	for _, m := range mutants {
		mutated := m.perturb(got)
		if mutated == want {
			t.Errorf("mutant %q did not change the fragment — mutation is a no-op", m.name)
			continue
		}
		if mutated == got {
			t.Errorf("mutant %q produced the original text — perturb() is a no-op", m.name)
		}
	}
	// C3 片段的变异：档位词被改 ⇒ 必须与 959 侧不等。
	c3 := normalizeSQL(extractBetween(t, jobSQL, anchorC3Start, anchorC3End, "market_scoring.go"))
	c3Want := normalizeSQL(extractBetween(t, view959, anchorC3Start, anchorC3End, "959 view"))
	if c3 != c3Want {
		t.Fatalf("premutation: C3 fragments already unequal")
	}
	if strings.Replace(c3, "'suspect_symmetric'", "'suspect_symmetric_x'", 1) == c3Want {
		t.Errorf("C3 mutation did not diverge — comparator has no teeth")
	}
}
