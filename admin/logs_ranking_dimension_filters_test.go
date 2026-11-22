package admin

// logs_ranking_dimension_filters_test.go —— 排行榜六维度「点击跳转 → 日志过滤」的判据。
//
// # 这道门测什么
//
// 2026-11-23 实测发现的缺陷：`/admin/usage-trends` 的六张排行卡片都会带
// 维度参数跳到 `/admin/request-logs`，但**后端当时只实现了 `model` 一维**，
// 其余五维（client_model / end_user_id / api_key_prefix /
// application_code / task_type）被**原样忽略** ⇒ 跳过去看到的是
// **未筛选的全量请求**。
//
// 证据（可复现）：传一个根本不存在的 `end_user_id=ZZZ_NOT_A_REAL_USER_12345`
// 与传真值，`/api/logs` 返回的 100 条**完全一样**，首条都是 `anonymous`。
//
// 所以「点击进去可以看到详细的请求」此前只有 1/6 成立。本门钉住：
//
//  1. 五个维度的过滤分支**必须存在**，且谓词**必须真的作用在行上**
//     （不是「读了参数然后丢掉」——那种形态 queryString 断言照样绿）；
//  2. `end_user_id` 的谓词**必须与排行侧共用同一个身份表达式**
//     （否则卡片上的数字与点进去看到的条数对不上）；
//  3. `buildRankingDimensionFilterClauses` **必须真的被 listLogs 调用**
//     （不是死代码）；
//  4. 排行侧（admin/usage.go）**必须复用同一个常量**，不得自己抄一份；
//  5. 排行侧**不得**用 `::jsonb` 解析 end_user_id —— 该列混着
//     'anonymous'/NULL/纯 id 等**非法 JSON** 值，::jsonb 会对它们抛错。
//
// 判据纪律：每条断言都必须**能在被测对象坏掉时变红**。
// 行为层的真库判据见 logs_ranking_dimension_filters_realdb_test.go。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readRankingFilterSrc 读 admin 包里的源码（静态门的输入）。
//
// ★ 为什么静态门要读源码而不是调函数：★ 有些缺陷形态是「函数还在、但没人调」
//
//	或「参数读了、但没拼进 WHERE」⇒ 纯函数测试看不见 ⇒ 只能读源码。
func readRankingFilterSrc(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(".", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", p, err)
	}
	return string(b)
}

// TestRankingDimensionFilterClausesExist 钉住五个维度的过滤分支与谓词都在。
//
// ★ 每条断言都要能在**两种退化形态**下变红：
//
//	① 参数被整个删掉（后加的分支最容易在重构时被顺手删掉）；
//	② 参数还在、但**没拼进 WHERE**（读了不用 ⇒ 过滤恒不生效）。
//	⇒ 光断言 `queryString(r, "<key>")` 抓不住 ② ⇒ 必须同时断言谓词文本。
func TestRankingDimensionFilterClausesExist(t *testing.T) {
	src := readRankingFilterSrc(t, "logs.go")

	for _, tc := range []struct {
		key string
		// 谓词必须命中这个片段（缺它 ⇒ 该维度的过滤没作用在行上）
		predicate string
	}{
		{"client_model", "rl.client_model = $"},
		{"end_user_id", "buildEndUserFilterClause(v, nextArgIdx)"},
		{"api_key_prefix", "starts_with(rl.api_key_prefix, $"},
		{"application_code", "rl.application_code = $"},
		{"task_type", "rl.task_type = $"},
	} {
		pat := regexp.MustCompile(`queryString\(r,\s*"` + regexp.QuoteMeta(tc.key) + `"\)`)
		if !pat.MatchString(src) {
			t.Errorf("admin/logs.go 必须实现 %q 的过滤分支（排行卡片会带这个参数跳转）", tc.key)
		}
		if !strings.Contains(src, tc.predicate) {
			t.Errorf("admin/logs.go 的 %q 分支必须把谓词拼进 WHERE（实测：读了参数却没作用在行上 ⇒ 过滤恒不生效）；缺 %q", tc.key, tc.predicate)
		}
	}
}

// TestApiKeyPrefixFilterIsPrefixMatch 钉住 api_key_prefix 走**前缀**匹配。
//
// ★ 为什么不能用等值：★ 211 实测 —— 排行侧 name 取自 `ak.key_prefix`
//
//	（现网该列本身就可能是明文 `sk-pi-autollm-21`），而请求行里存的是
//	**掩码值** `sk-pi-autollm-21***`（掩码长度随前缀长度变）⇒
//	等值匹配对这类 key **命中 0 行**（实测 470 行 ⇒ 0）。
//	⇒ ⇒ 必须是前缀语义。
//
// ★ 为什么不用 LIKE：★ LIKE 把 '%'/'_' 当通配符，语义不是「前缀」
//
//	（传 `sk-%` 会匹配所有 key）⇒ 用 PG 的 starts_with。
func TestApiKeyPrefixFilterIsPrefixMatch(t *testing.T) {
	src := readRankingFilterSrc(t, "logs.go")
	if !strings.Contains(src, "starts_with(rl.api_key_prefix, $") {
		t.Error("api_key_prefix 过滤必须用 starts_with（前缀语义）；等值会命中 0 行，LIKE 会把 %/_ 当通配符")
	}
	if strings.Contains(src, "rl.api_key_prefix LIKE $") {
		t.Error("api_key_prefix 过滤不得用 LIKE（%/_ 会被当通配符，语义不是前缀）")
	}
}

// TestEndUserFilterClauseUsesSharedIdentityExpr 钉住过滤侧与排行侧
// **共用同一个身份表达式**。
//
// ★ 这是「卡片数字 == 点进去条数」的唯一保证：★ 两边各写一份表达式 ⇒
//
//	改了一边忘了另一边就是「卡片 14,674 / 点进去 0 条」。
func TestEndUserFilterClauseUsesSharedIdentityExpr(t *testing.T) {
	clause, args := buildEndUserFilterClause("dev-1", 3)
	if len(args) != 1 {
		t.Errorf("两条腿共用同一个占位符 ⇒ 只应绑 1 个参数，实得 %d", len(args))
	}
	if !strings.Contains(clause, userIdentityExpr) {
		t.Errorf("end_user_id 谓词必须内嵌共享的 userIdentityExpr（否则与排行口径对不上），实际：%s", clause)
	}
	// 腿①：原值精确命中（为旧链接/直链保留）
	if !strings.Contains(clause, "rl.end_user_id = $3") {
		t.Errorf("谓词必须有「原值精确命中」这条腿（修复前界面传的就是整段 JSON），实际：%s", clause)
	}
	// 两条腿必须用同一个 $n（否则参数个数与占位符对不上）
	if strings.Count(clause, "$3") != 2 {
		t.Errorf("两条腿必须共用同一个占位符 $3，实际：%s", clause)
	}
}

// TestUserIdentityExprIsSafeOnNonJSONRows 钉住身份表达式不会被非法 JSON 炸掉。
//
// ★ 该列现网混着 'anonymous' / NULL / 纯 id 等**非法 JSON** 值
//
//	（211 实测 379,142 / 38,657 / 49 行）⇒ `::jsonb` 会对它们抛错
//	⇒ ⇒ 整条排行查询 500 ⇒ ⇒ 表达式必须用 substring 的正则形式。
func TestUserIdentityExprIsSafeOnNonJSONRows(t *testing.T) {
	if strings.Contains(userIdentityExpr, "::jsonb") {
		t.Error("userIdentityExpr 不得用 ::jsonb（该列混着 'anonymous'/NULL/纯 id 等非法 JSON 值，::jsonb 会抛错使整条查询 500）")
	}
	if !strings.Contains(userIdentityExpr, "substring") {
		t.Error("userIdentityExpr 必须用 substring 提取 device_id")
	}
	if !strings.Contains(userIdentityExpr, `"device_id"`) {
		t.Error("userIdentityExpr 必须从 JSON 里提取 device_id（否则界面显示的是一坨原始 JSON）")
	}
	// 非法 JSON 值必须落到 'unknown' 兜底（不能落回原始 JSON 串）
	if !strings.Contains(userIdentityExpr, "'unknown'") {
		t.Error("userIdentityExpr 必须有 'unknown' 兜底（NULL/空值/无 device_id 的 JSON 都要归组）")
	}
	// ★ 表达式里**含 '%'**（LIKE 模式 '{%' 的通配符）⇒ 它绝不能被当成
	//   fmt.Sprintf 的**格式串**（否则 % 会被吃掉）⇒ 断言它**逐字**出现在
	//   渲染后的谓词里：若被当格式串用过，这里立刻红。
	clause, _ := buildEndUserFilterClause("dev-1", 3)
	if !strings.Contains(clause, userIdentityExpr) {
		t.Error("userIdentityExpr 必须逐字出现在渲染后的谓词里（它含 '%'，一旦被当成 Sprintf 的格式串就会被吃掉）")
	}
}

// TestRankingDimensionFilterClausesAreWiredToListLogs 钉住纯函数**真的被端点调用**
// （不是死代码 —— 单元里存在但端点没接，是这类修复最常见的假绿）。
func TestRankingDimensionFilterClausesAreWiredToListLogs(t *testing.T) {
	src := readRankingFilterSrc(t, "logs.go")
	if !strings.Contains(src, "buildRankingDimensionFilterClauses(r, argIdx)") {
		t.Error("admin/logs.go 的 listLogs 必须调用 buildRankingDimensionFilterClauses（否则谓词是死代码，过滤恒不生效）")
	}
}

// TestUsageTopUsersSharesIdentityExprWithFilter 钉住排行侧复用**同一个**常量。
//
// ★ 排行侧必须用共享的 userIdentityExpr，不得自己抄一份表达式
//
//	（抄一份 = 两边漂移 = 卡片数字与点进去的条数对不上）。
func TestUsageTopUsersSharesIdentityExprWithFilter(t *testing.T) {
	src := readRankingFilterSrc(t, "usage.go")
	if !strings.Contains(src, `queryTopN("users", userIdentityExpr, usersLimit)`) {
		t.Error("admin/usage.go 的 Top 用户查询必须使用共享的 userIdentityExpr 作为分组键")
	}
	// ★ 不得在 usage.go 里另抄一份 device_id 提取表达式
	if strings.Contains(src, "substring(rl.end_user_id") {
		t.Error("admin/usage.go 不得自己抄一份 end_user_id 的 device_id 提取表达式（必须复用 logs.go 的 userIdentityExpr）")
	}
	if strings.Contains(src, "end_user_id::jsonb") {
		t.Error("admin/usage.go 不得用 end_user_id::jsonb（该列混着 'anonymous'/NULL/纯 id 等非法 JSON 值）")
	}
}
