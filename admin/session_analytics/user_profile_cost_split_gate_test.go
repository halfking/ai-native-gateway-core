package sessionanalytics

// user_profile_cost_split_gate_test.go —— admin/user_profile.go 的成本分列门
// （2026-11-24 L1 修订轮）。
//
// ★ 为什么这道门住在 sessionanalytics 包里：admin 包自己的测试文件不在本轮
//
//	可改范围（admin/** 由别的写者拥有）⇒ 门只能落在这里。判据是**源码形状**
//	（读 ../user_profile.go 的文本）+ **结构体的 JSON 形状**（反射 admin 包
//	导出的 UserProfileSummary）—— 两者都不需要 admin 包的非导出符号。
//
// ★ 守住的是什么（DEFECT 1 的另一半）：
//	① 列表 SQL 必须 SELECT 出 internal_cost_usd / external_credits 两列
//	   （此前只 SELECT 了混合值 total_cost_usd ⇒ 前端拿不到任何分列数字）；
//	② SELECT 的列数必须 == 该 handler 的 rows.Scan 实参个数（漏扫 ⇒ 恒 0）；
//	③ total_cost_usd 必须继续存在（前端依赖，不许改名/删除），且它**头上
//	   必须留着「混合单位」的披露注释** —— 披露被悄悄删掉也是回归。
import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// readUserProfileSource 读 admin/user_profile.go 源码（源码级契约判据）。
func readUserProfileSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../user_profile.go")
	if err != nil {
		t.Fatalf("读 ../user_profile.go 失败：%v", err)
	}
	return string(b)
}

// TestUserProfileListSQLSelectsSplitCost 钉测：列表 SQL 的成本三列与 Scan 对齐。
func TestUserProfileListSQLSelectsSplitCost(t *testing.T) {
	src := readUserProfileSource(t)

	listSQL := extractRawStringAfter(t, src, "listSQL := fmt.Sprintf(`")
	cols := sqlSelectColumns(t, listSQL)

	for _, alias := range []string{"total_cost_usd", "internal_cost_usd", "external_credits"} {
		require.True(t, hasSelectAlias(cols, alias),
			"列表 SELECT 里缺别名 %q（实际列清单：%v）—— 分列成本必须是真 SELECT "+
				"出来的，且 total_cost_usd（前端依赖）不许删", alias, cols)
	}

	// 分列两列的源列不得互换。
	internalExpr := selectAliasExpr(cols, "internal_cost_usd")
	externalExpr := selectAliasExpr(cols, "external_credits")
	assert.Contains(t, internalExpr, "internal_cost", "internal_cost_usd 必须聚合 internal_cost（实际：%q）", internalExpr)
	assert.NotContains(t, internalExpr, "external_cost", "internal_cost_usd 里混进了 external_cost（两列写反了？）")
	assert.Contains(t, externalExpr, "external_cost", "external_credits 必须聚合 external_cost（实际：%q）", externalExpr)
	assert.NotContains(t, externalExpr, "internal_cost", "external_credits 里混进了 internal_cost（两列写反了？）")

	// 列数与 Scan 目标数必须一致。
	want := len(cols)
	got := scanDestinationsOf(t, src, "func (h *Handler) handleUserAnalyticsList(")
	assert.Equal(t, want, got,
		"列表 SELECT 列数(%d) 与 rows.Scan 目标数(%d) 不一致 ⇒ 漏扫/多扫，分列字段会恒为 0 或读错列",
		want, got)
}

// TestUserProfileSummaryExposesSplitCostJSON 钉测：响应结构体的 JSON 形状。
func TestUserProfileSummaryExposesSplitCostJSON(t *testing.T) {
	rt := reflect.TypeOf(admin.UserProfileSummary{})
	tags := map[string]string{}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if idx := strings.Index(tag, ","); idx >= 0 {
			tag = tag[:idx]
		}
		tags[tag] = f.Name
	}

	for _, key := range []string{"total_cost_usd", "internal_cost_usd", "external_credits"} {
		require.Contains(t, tags, key,
			"UserProfileSummary 缺 json tag %q（前端消费面依赖这三个键）", key)
	}
}

// TestUserProfileMixedTotalCostIsDisclosed 钉测：混合单位的披露不许被删。
//
// ★ 为什么连注释都要钉：total_cost_usd 这个名字会把「USD + 积分」的跨单位
//
//	相加说成美元金额；字段名不能改（前端依赖）⇒ 只能靠**披露**纠偏。披露被
//	删掉 = 失实断言重新生效。
func TestUserProfileMixedTotalCostIsDisclosed(t *testing.T) {
	src := readUserProfileSource(t)

	block := extractStructBlock(t, src, "type UserProfileSummary struct")
	require.Contains(t, block, `json:"total_cost_usd"`,
		"UserProfileSummary 里的 total_cost_usd 字段被改名/删除了（前端依赖它）")
	assert.Contains(t, block, "混合单位",
		"total_cost_usd 字段上必须有「混合单位」的披露注释（它是 USD + 积分的跨单位相加，"+
			"不是美元金额）；披露被删 = 失实断言重新生效")
}

// extractRawStringAfter 取 `marker` 之后的第一个反引号原始字符串内容。
func extractRawStringAfter(t *testing.T, src, marker string) string {
	t.Helper()
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("源码里找不到 %q", marker)
	}
	rest := src[idx+len(marker):]
	end := strings.Index(rest, "`")
	if end < 0 {
		t.Fatalf("%q 之后的原始字符串没有闭合", marker)
	}
	return rest[:end]
}

// extractStructBlock 取一个结构体定义的花括号块（含其中的注释）。
func extractStructBlock(t *testing.T, src, decl string) string {
	t.Helper()
	idx := strings.Index(src, decl)
	if idx < 0 {
		t.Fatalf("源码里找不到 %q", decl)
	}
	open := strings.Index(src[idx:], "{")
	if open < 0 {
		t.Fatalf("%q 之后没有 '{'", decl)
	}
	start := idx + open
	depth := 0
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("%q 的结构体块没有闭合", decl)
	return ""
}
