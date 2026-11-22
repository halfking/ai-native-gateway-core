package admin

// usage_top_rankings_filters_test.go —— top-rankings 端点的维度过滤契约
// （2026-11-23 深链钉住轮）。
//
// ★ 为什么需要这条：深链钉住某个维度后（从会话分析「Top5 供应商/模型」点进来），
//   用量趋势页**下方**的「用量排行榜」也必须跟着那个维度走 —— 否则同一屏里
//   上半段是那个供应商的数据、下半段还是全站的 ⇒ 口径打架。
// ⇒ 端点必须接受 provider_id / model / api_key_id 三个可选过滤参数。
//
// ★ 钉法：源码级契约（本端点要真库才有意义，仓内既有用法同款）
//   ① 参数解析走**趋势序列同一套** usageTrendFiltersFromRequest（口径不许分叉）；
//   ② 六个维度里共用 queryTopN 的 5 个 + API Key 自带 JOIN 的 1 个，
//      **都要**带上过滤谓词（漏一个维度 ⇒ 那一块还是全站数据）；
//   ③ 参数缺省 = 不过滤（与改动前逐字等价 ⇒ 其它调用方不受影响）。
import (
	"os"
	"strings"
	"testing"
)

func usageSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("usage.go")
	if err != nil {
		t.Fatalf("read usage.go: %v", err)
	}
	return string(b)
}

// TestUsageTopRankingsHonorsDimensionFilters 钉测①：端点解析维度过滤参数。
func TestUsageTopRankingsHonorsDimensionFilters(t *testing.T) {
	src := usageSource(t)
	start := strings.Index(src, "func (h *Handler) usageTopRankings(")
	if start == -1 {
		t.Fatal("usageTopRankings not found")
	}
	end := strings.Index(src[start:], "\n}\n")
	if end == -1 {
		t.Fatal("usageTopRankings body end not found")
	}
	fn := src[start : start+end+2]

	// 走趋势序列同一套解析器（口径不许分叉）
	if !strings.Contains(fn, "usageTrendFiltersFromRequest(r)") {
		t.Error("top-rankings 必须复用 usageTrendFiltersFromRequest —— 另写一份解析就是口径分叉的起点")
	}
	// 三个维度过滤谓词都要出现（queryTopN 与 API Key 自带 JOIN 各一份）
	for _, pred := range []string{
		"AND rl.provider_id = $",
		"AND rl.api_key_id = $",
		"AND %s = ANY($",
	} {
		if !strings.Contains(fn, pred) {
			t.Errorf("top-rankings 缺少过滤谓词 %q", pred)
		}
	}
	// 模型口径必须与趋势序列同源（共用模板的两个别名版本）
	if !strings.Contains(fn, "usageTrendModelExprRL") {
		t.Error("top-rankings 的模型过滤必须用 usageTrendModelExprRL（与趋势序列同口径）")
	}
}

// TestUsageTopRankingsFilterCoversAllDimensions 钉测②：六个维度都得被过滤覆盖。
//
// ★ 漏一个维度 ⇒ 那一块卡片还是全站数据 ⇒ 同屏口径打架（正是本轮要消灭的形态）。
func TestUsageTopRankingsFilterCoversAllDimensions(t *testing.T) {
	src := usageSource(t)
	start := strings.Index(src, "func (h *Handler) usageTopRankings(")
	end := strings.Index(src[start:], "\n}\n")
	fn := src[start : start+end+2]

	// queryTopN 承载的 5 个维度
	for _, dim := range []string{"models", "clients", "users", "projects", "tasks"} {
		if !strings.Contains(fn, `queryTopN("`+dim+`"`) {
			t.Errorf("维度 %q 不在 queryTopN 列表里（改动后维度清单变了？）", dim)
		}
	}
	// API Key 维度自带 JOIN（不走 queryTopN）⇒ 过滤谓词要单有一份
	if strings.Count(fn, "AND rl.provider_id = $") < 2 {
		t.Error("API Key 维度（自带 JOIN 的那条查询）也得带 provider 过滤 ⇒ 过滤谓词应出现两次")
	}
	if strings.Count(fn, "AND rl.api_key_id = $") < 2 {
		t.Error("API Key 维度那条查询也得带 api_key 过滤 ⇒ 过滤谓词应出现两次")
	}
}

// TestUsageTrendModelExprAliasesShareTemplate 钉测③：两处模型口径表达式同源。
//
// ★ 趋势序列用 `r` 别名、top-rankings 用 `rl` 别名 ⇒ 表达式必须由同一份模板生成；
// 手抄两份就是「图表里的模型」与「排行榜里的模型」对不上的起点。
func TestUsageTrendModelExprAliasesShareTemplate(t *testing.T) {
	b, err := os.ReadFile("usage_trend_series.go")
	if err != nil {
		t.Fatalf("read usage_trend_series.go: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "usageTrendModelExprTemplate") {
		t.Fatal("模型口径表达式必须由模板生成（usageTrendModelExprTemplate）")
	}
	if !strings.Contains(src, `usageTrendModelExpr = fmt.Sprintf(usageTrendModelExprTemplate, "r", "r")`) {
		t.Error("usageTrendModelExpr 必须由模板 + r 别名生成")
	}
	if !strings.Contains(src, `usageTrendModelExprRL = fmt.Sprintf(usageTrendModelExprTemplate, "rl", "rl")`) {
		t.Error("usageTrendModelExprRL 必须由同一模板 + rl 别名生成")
	}
}
