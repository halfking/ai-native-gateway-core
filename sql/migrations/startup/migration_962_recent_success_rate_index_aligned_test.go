package startup

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/db"
)

// migration_962_recent_success_rate_index_aligned_test.go —— 962
// （recent_success_rate / recent_cache_hit_rate 谓词与既有索引同形）的契约门。
//
// # 它防的缺陷（2026-11-12 48h 审计第五轮，245 + 154 生产实证）
//
//	GET /api/credentials/monitor-summary ⇒ 500
//	{"error":{"detail":"internal error (see server logs)"}}
//	log: admin handler internal error  caller=credential_monitor.go:762
//	     err="rows iteration failed: timeout: context deadline exceeded"  duration_ms=15002
//
// 根因链（全部在真库实测过）：
//
//	① admin/credential_monitor.go:687 给该 handler 15s context ⇒ 慢于 15s 即 500；
//	② summary 模式对**每个 (凭据 × 模型) 行**跑一次
//	   `CROSS JOIN LATERAL recent_success_rate(...)`
//	   ⇒ 74 个活跃凭据 × 2015 条 model_offers = **1959 次函数调用**；
//	③ 函数体的过滤条件写成
//	       WHERE credential_id ~ '^[0-9]+$'
//	         AND credential_id::bigint = $1
//	   而**既有索引**（迁移 729 建）是
//	       idx_session_turns_hot_credential_ts
//	         ON session_turns_hot ((CASE WHEN credential_id ~ '^[0-9]+$'
//	                                   THEN credential_id::bigint END), ts DESC)
//	   ⇒ ★ **谓词与索引键不是同一个表达式** ⇒ planner 用不上该索引
//	   ⇒ 每次调用退化成「扫 3 小时窗全量再过滤」
//	   ⇒ EXPLAIN ANALYZE 实测 13184 ms、Rows Removed by Filter: 1876；
//	④ 改成与索引键同形后，同一查询 113 ms（Index Scan 命中该索引，116 倍）。
//
// # ★ 这轮最贵的教训：**同一个函数体有两份载体**
//
//	载体 ①：SQL 迁移文件（890 播种、962 改谓词）
//	载体 ②：Go 运行时 SSOT（db.recentSuccessRateEnsureSQL /
//	                              db.recentCacheHitRateEnsureSQL）
//
//	★ 网关 boot 的 ensure 链（db.Open → ApplyMigrations → applyMigrationsOnce
//	  → ensureRoutingRecentSuccessRate / ensureRecentCacheHitRate）
//	  用 `CREATE OR REPLACE` **在迁移之后**再写一遍函数体
//	  ⇒ **只改载体 ① 会被载体 ② 覆盖回去**。
//	⇒ 生产实测形态（最坏的形态）：迁移 962 已记账
//	  （schema_migrations + 迁移校验和台账都有行），
//	  **但库里的函数体仍是旧谓词** ⇒ 账本说修好了、运行面没修好。
//	⇒ 而且「只钉迁移文件的形状门」在这个形态下**恒绿** —— 它测的是载体 ①，
//	  而真正生效的是载体 ②。
//
// ⇒ 所以下面有两道门：
//
//	§A 形状门（载体 ① 内部）：索引表达式 == 迁移 962 的谓词；
//	§B 跨包等价门：载体 ① == 载体 ②（逐字等价 + 三方不变式）。
//
// # ★ 为什么判据是**结构化比对**而不是手写字符串钉
//
//	缺陷本体就是「谓词与索引键必须是**同一个表达式**」。
//	钉一个字面串只能保护那个串，保护不了这条不变量。
//	⇒ 索引表达式从**建索引的那条迁移（729）里抽**，
//	  再要求两个载体的谓词都携带它 ⇒ 谁把谓词改离索引形状，门就红。

const (
	mig962UpName   = "962_recent_success_rate_index_aligned.sql"
	mig962DownName = "962_recent_success_rate_index_aligned.down.sql"
	mig729IdxName  = "729_sql_audit_session_turns_credential_ts_index.sql"
)

// casePredicateRe962 匹配「与索引键同形」的谓词形态。
//
// ⚠ ELSE 分支在源文件里可写可不写（PG 落库时统一补成 `ELSE NULL::bigint END`），
// ⇒ 两种拼法都要命中 ⇒ 正则里 ELSE 段可选。
var casePredicateRe962 = regexp.MustCompile(`(?is)CASE\s+WHEN\s+credential_id\s*~\s*'\^\[0-9\]\+\$'\s+THEN\s+credential_id::bigint(?:\s+ELSE\s+NULL(?:::bigint)?)?\s+END`)

// oldPredicateFormRe962 匹配**旧形态**（索引用不上的那种）：
// 正则守卫 + AND + 数值相等。
var oldPredicateFormRe962 = regexp.MustCompile(`(?is)credential_id\s*~\s*'\^\[0-9\]\+\$'\s*\n\s*AND\s+credential_id::bigint\s*=`)

// readFile962 读仓内文件（相对本包目录）。
func readFile962(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	require.NoError(t, err, "%s should exist", name)
	return string(b)
}

// normalizeWhitespace962 折叠空白（去行注释），让两份载体的**排版差异**不影响比较。
func normalizeWhitespace962(s string) string {
	lineComment := regexp.MustCompile(`--[^\n]*`)
	s = lineComment.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// normalizeExpr962 归一化索引/谓词表达式本身。
//
// ★ PG 会把没有 ELSE 的 CASE 改写成 `ELSE NULL::bigint END` ⇒ 源文件里
// 「带 ELSE」与「不带 ELSE」是同一个表达式 ⇒ 比较前先折掉 ELSE 分支。
func normalizeExpr962(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "ELSE NULL::bigint END", "END")
	s = strings.ReplaceAll(s, "ELSE NULL END", "END")
	return strings.Join(strings.Fields(s), " ")
}

// indexExpression962 从**建索引的那条迁移（729）**里抽出索引键表达式。
//
// ★ 锚点必须是「建索引的迁移」，不是「当前谓词」——
//
//	否则锚点与被检查对象同源 ⇒ 两者一起漂移时门恒绿（本仓反复踩过的自指失效）。
func indexExpression962(t *testing.T) string {
	t.Helper()
	idx := readFile962(t, mig729IdxName)
	idxRe := regexp.MustCompile(`(?is)CREATE\s+INDEX(?:\s+CONCURRENTLY)?(?:\s+IF\s+NOT\s+EXISTS)?\s+idx_session_turns_hot_credential_ts\s+ON\s+public\.session_turns_hot\s*\(\((.*?)\)\s*,\s*ts\s+DESC\)`)
	m := idxRe.FindStringSubmatch(idx)
	require.Len(t, m, 2, "migration 729 must still create idx_session_turns_hot_credential_ts with a (expression, ts DESC) key")
	return normalizeExpr962(m[1])
}

// functionBody962 从一段 SQL 文本里抽出指定函数的 `AS $$ … $$` 函数体。
//
// ⚠ 闭合标记必须用「**开标记之后**的第一个 `$$`」，不能用 LastIndex：
//
//	整份文件里后面还有别的 `$$` ⇒ 用 LastIndex 会把「函数体 + 文件尾巴」
//	当成函数体（失败信息读起来像「门坏了」，实际是门取错了范围）。
func functionBody962(t *testing.T, src string, fnMarker string) string {
	t.Helper()
	i := strings.Index(src, "CREATE OR REPLACE FUNCTION "+fnMarker)
	require.GreaterOrEqual(t, i, 0, "must define %s", fnMarker)
	seg := src[i:]
	o := strings.Index(seg, "AS $$")
	require.GreaterOrEqual(t, o, 0, "cannot locate the function body opener for %s", fnMarker)
	c := strings.Index(seg[o+len("AS $$"):], "$$")
	require.GreaterOrEqual(t, c, 0, "cannot locate the function body closer for %s", fnMarker)
	return seg[o+len("AS $$") : o+len("AS $$")+c]
}

// foldIndexAlignedPredicate962 把「索引同形谓词」折叠回**等价的旧形态**，
// 使「旧迁移的体」与「新 SSOT 的体」可比。
//
// ★ 用途：890 / 915 那两道**逐字等价门**比较的是「那时的迁移体」与
//
//	「现在的 SSOT」。SSOT 因为 962 合法演进了 ⇒ 逐字比较必然红。
//	⇒ 折叠掉**这一条**被钉住的 delta 之后再比较：
//	  delta 之外的任何漂移仍然红（门的牙齿保留），
//	  而「合法演进」不再被误报（不产生假红）。
//	⚠ 折叠只许在**归一化之后**做（空白已折叠 ⇒ 文本形态唯一）。
func foldIndexAlignedPredicate962(normalized string) string {
	replaced := strings.ReplaceAll(normalized,
		"(CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint ELSE NULL::bigint END) = p_credential_id",
		"credential_id ~ '^[0-9]+$' AND credential_id::bigint = p_credential_id")
	// 折叠掉「不带 ELSE」的拼法（归一化后仍是合法同形表达式）。
	replaced = strings.ReplaceAll(replaced,
		"(CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END) = p_credential_id",
		"credential_id ~ '^[0-9]+$' AND credential_id::bigint = p_credential_id")
	return replaced
}

// TestMigration962_RecentSuccessRatePredicateIsIndexAligned —— §A 形状门
// （载体 ① 内部：迁移文件自身）。
//
// ★ down 文件**允许**携带旧形态：那就是「回退目标」本身。
//
//	所以旧形态断言只作用在 UP 文件上。
func TestMigration962_RecentSuccessRatePredicateIsIndexAligned(t *testing.T) {
	up := readFile962(t, mig962UpName)
	down := readFile962(t, mig962DownName)
	idxExpr := indexExpression962(t)

	// ── ① 两个函数的谓词都必须携带索引键表达式 ───────────────────────────
	for _, fn := range []string{"public.recent_success_rate", "public.recent_cache_hit_rate"} {
		body := functionBody962(t, up, fn)
		pred := casePredicateRe962.FindString(body)
		require.NotEmpty(t, pred,
			"%s's predicate must carry the index expression of idx_session_turns_hot_credential_ts — "+
				"the index key is ((%s), ts DESC); a differently-shaped predicate makes the planner "+
				"fall back to scanning the whole window per call (13.2s → 500 on 245/154)", fn, idxExpr)
		require.Equal(t, idxExpr, normalizeExpr962(pred),
			"%s's predicate expression must be the SAME as the index key expression", fn)
	}

	// ── ② UP 文件里旧形态必须消失 ───────────────────────────────────────
	require.NotRegexp(t, oldPredicateFormRe962, up,
		"migration 962 UP must not reintroduce the regex-guard + credential_id::bigint = $1 predicate "+
			"(it cannot match idx_session_turns_hot_credential_ts and degrades to a full-window scan per call)")
	require.Regexp(t, oldPredicateFormRe962, down,
		"migration 962 DOWN must restore the pre-962 predicate shape (rollback target)")

	// ── ③ CASE 分支必须保留正则守卫（否则非数字 credential_id 会开始匹配）──
	require.Contains(t, up, "CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint ELSE NULL::bigint END",
		"the CASE branch must keep the regex guard so non-numeric credential_id still cannot match")

	// ── ④ 两个函数都必须被重定义，且记账行必须在 ─────────────────────────
	require.Contains(t, up, "CREATE OR REPLACE FUNCTION public.recent_success_rate(")
	require.Contains(t, up, "CREATE OR REPLACE FUNCTION public.recent_cache_hit_rate(")
	require.Contains(t, up, "INSERT INTO public.schema_migrations (version, description)",
		"migrations >= 952 must self-register their ledger row")
	require.Contains(t, up, "'962'")
}

// TestMigration962_RuntimeSSOTCarriesTheSamePredicate —— §B 跨包等价门
// （载体 ① ⇔ 载体 ②）。
//
// ★ 这道门就是**本轮那个缺陷本该被拦住的地方**：
//
//	上线前只有「迁移文件自身」的形状门（§A）⇒ 它在「只改了迁移、没改 SSOT」
//	的形态下**恒绿** ⇒ 缺陷直达生产（245/154 实测：迁移已记账、函数体仍旧）。
//
// ⇒ 这里把不变量写成**三方相等**：
//
//	既有索引表达式（从建索引的迁移 729 抽）
//	  == 迁移 962 的谓词
//	  == 运行时 SSOT 的谓词
//
//	并且额外要求：两份载体的**函数体逐字等价**（归一化空白后），
//	⇒ 任何一侧的漂移都被拦下，包括「改了两侧但改得不一样」。
func TestMigration962_RuntimeSSOTCarriesTheSamePredicate(t *testing.T) {
	idxExpr := indexExpression962(t)
	up := readFile962(t, mig962UpName)

	// 载体 ①：迁移文件。
	migRSR := functionBody962(t, up, "public.recent_success_rate")
	migCHR := functionBody962(t, up, "public.recent_cache_hit_rate")

	// 载体 ②：运行时 SSOT（网关 boot 时真正执行的那一份）。
	ssotRSR := functionBody962(t, db.RecentSuccessRateEnsureSQLForTest(), "public.recent_success_rate")
	ssotCHR := functionBody962(t, db.RecentCacheHitRateEnsureSQLForTest(), "public.recent_cache_hit_rate")

	// ── ① 三方不变式：索引表达式 == 迁移谓词 == SSOT 谓词 ─────────────────
	for _, tc := range []struct {
		name string
		body string
	}{
		{"迁移 962 的 recent_success_rate", migRSR},
		{"迁移 962 的 recent_cache_hit_rate", migCHR},
		{"运行时 SSOT 的 recent_success_rate", ssotRSR},
		{"运行时 SSOT 的 recent_cache_hit_rate", ssotCHR},
	} {
		pred := casePredicateRe962.FindString(tc.body)
		require.NotEmpty(t, pred,
			"%s 的谓词必须携带索引同形表达式（%s）—— 载体之间已漂移，或谓词被改离索引形状", tc.name, idxExpr)
		require.Equal(t, idxExpr, normalizeExpr962(pred),
			"%s 的谓词表达式必须与索引键表达式逐字同形", tc.name)
		require.NotRegexp(t, oldPredicateFormRe962, tc.body,
			"%s 仍带旧形态谓词（正则守卫 + AND + 数值相等）⇒ planner 用不上 idx_session_turns_hot_credential_ts", tc.name)
	}

	// ── ② 两份载体的函数体必须逐字等价（归一化空白后）────────────────────
	require.Equal(t, normalizeWhitespace962(migRSR), normalizeWhitespace962(ssotRSR),
		"迁移 962 的 recent_success_rate 函数体与运行时 SSOT 漂移了。\n"+
			"后果：网关 boot 的 CREATE OR REPLACE 会用 SSOT 覆盖迁移刚写的函数体 ⇒ "+
			"「迁移已记账、函数体没换」⇒ 修复在账面上成立、在运行面上没生效。\n"+
			"★ 改函数体必须同时改两份载体（SQL 迁移文件 + Go 运行时 SSOT）。")
	require.Equal(t, normalizeWhitespace962(migCHR), normalizeWhitespace962(ssotCHR),
		"迁移 962 的 recent_cache_hit_rate 函数体与运行时 SSOT 漂移了（路由打分维度，同款后果）。")
}
