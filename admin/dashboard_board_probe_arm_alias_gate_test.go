package admin

import (
	"regexp"
	"strings"
	"testing"
)

// ★★★ 2026-11-25（12h 修订审计轮 P0 的防回归门）★★★
//
// ★★ 为什么这道门必须存在：★★
// ★★ 2026-11-25 02:09 的 44477c433「探测口径切换生效——UNION ALL 探测车道表」
// ★★ 把英雄区/趋势/饼图三条读链改成 UNION ALL 探测车道表，★ 但探测臂的 FROM 别名
// ★★ 是 `p`，而**共用谓词**是用业务臂的别名 `r` 构造的 ⇒ ★★ 被原样塞进探测臂
// ★★ ⇒ ★★ PostgreSQL 解析期就报：
// ★★     ERROR: missing FROM-clause entry for table "r"  (42P01)
// ★★ ⇒ ★★ **include_probes=1 时三条读链 100% 失败**。
// ★★ 而且因为 42P01 同时也是「缺表」的错误码，★ 故障被 IsMissingRelationError
// ★★ 误判成「视图没迁移」⇒ 英雄区连一条 Warn 都不打 ⇒ ★★ 载荷仍宣称
// ★★ `probe_scope: "included"` ⇒ ★★ **故障被静默掩盖**。
// ★★ 而当时的门（dashboard_board_truth_source_test.go 的 8 条 + 真库门
// ★★ dashboard_board_pie_sql_integration_test.go）**只渲染 includeProbes=false 的形态**
// ★★ ⇒ ★★ `*UnionSQL(..., true)` 这条路径**零渲染、零执行** ⇒ ★★ 这就是它能带着
// ★★ 「全绿」过关的结构原因。
//
// ★★ 本门的作用：★★
// ★★   ① **渲染** includeProbes=true 的三条 UNION 语句（之前零覆盖的那条路径）；
// ★★   ② 把每条语句按 UNION 臂切开，★ 断言**每条臂内部只出现自己那条臂的别名**
// ★★      （业务臂 `r` / 探测臂 `p`）⇒ ★★ 跨臂串别名 ⇒ 当场红；
// ★★   ③ 断言探测臂**确实带了**谓词（不是裸表扫描）⇒ 防「修别名时把谓词删了」。
var boardUnionArmSplit = regexp.MustCompile(`(?i)\bUNION\s+ALL\b`)

// boardAliasUseRE 匹配 SQL 里出现的**限定标识符**（`r.` / `p.` 形式）。
var boardAliasUseRE = regexp.MustCompile(`\b([a-zA-Z_][a-zA-Z0-9_]*)\.`)

// boardProbeArmAliases 抽取一条 UNION 语句里**每条臂**实际用到的别名集合。
//
// ★ 按臂切片后逐臂统计限定标识符 ⇒ 只关心「这条臂自己引用了谁」。
func boardProbeArmAliases(t *testing.T, sqlText string) []map[string]int {
	t.Helper()
	arms := boardUnionArmSplit.Split(sqlText, -1)
	out := make([]map[string]int, 0, len(arms))
	for _, arm := range arms {
		used := map[string]int{}
		for _, m := range boardAliasUseRE.FindAllStringSubmatch(arm, -1) {
			used[m[1]]++
		}
		out = append(out, used)
	}
	return out
}

// boardUnionArms 取出每条臂的文本（供逐条断言）。
func boardUnionArms(sqlText string) []string {
	return boardUnionArmSplit.Split(sqlText, -1)
}

// TestBoardProbeArmDoesNotLeakBusinessAlias 是本轮 P0 的**直接防回归门**。
//
// ★ 判据：★ 探测臂（FROM session_turns_probe_hot 的那条臂）里**不得**出现业务臂的
// ★ 别名 `r.`；★ 且探测臂**必须**带时间窗谓词（`p.ts`）。
func TestBoardProbeArmDoesNotLeakBusinessAlias(t *testing.T) {
	tr := boardTimeRange{}
	where, args := boardLogsWhereTerminal(tr, "tenant-a", true)
	if len(args) != 3 {
		t.Fatalf("expected 3 args for tenant-scoped window, got %d", len(args))
	}
	providerWhere := where + " AND r.provider_id = $4"
	credits := boardCreditsExpr("tenant-a")

	cases := []struct {
		name string
		sql  string
	}{
		{"summary", boardSummaryLogsUnionSQL(where, credits, true)},
		{"trend", boardTrendLogsUnionSQL(sqlTrendBucket("r.ts", 60), providerWhere, credits, true)},
		{"pie", boardPieFactsUnionSQL(where, credits, true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arms := boardUnionArms(tc.sql)
			if len(arms) < 2 {
				t.Fatalf("expected a UNION ALL with a probe arm, got %d arm(s)", len(arms))
			}
			// 定位探测臂：含 session_turns_probe_hot 的那条。
			probeIdx := -1
			for i, arm := range arms {
				if strings.Contains(arm, "session_turns_probe_hot") {
					probeIdx = i
					break
				}
			}
			if probeIdx < 0 {
				t.Fatalf("no probe arm found in the UNION statement")
			}
			probeArm := arms[probeIdx]
			// ① 探测臂不得引用业务臂别名 `r.`。
			if strings.Contains(probeArm, "r.") {
				// 只允许 `p.` / `d.` / 其它合法限定；把命中的 r. 摘出来报错。
				bad := regexp.MustCompile(`\br\.[a-zA-Z_][a-zA-Z0-9_]*`).FindAllString(probeArm, -1)
				t.Fatalf("probe arm leaks the business alias: %v\n--- probe arm ---\n%s", bad, probeArm)
			}
			// ② 探测臂必须带时间窗谓词（p.ts）。
			if !strings.Contains(probeArm, "p.ts") {
				t.Fatalf("probe arm lost its time-window predicate (expected p.ts)\n--- probe arm ---\n%s", probeArm)
			}
			// ③ 探测臂必须仍带终态集谓词（与业务臂同口径）。
			if !strings.Contains(probeArm, "p.request_status IN ('success','failure','rate_limited')") {
				t.Fatalf("probe arm lost the terminal-status set predicate\n--- probe arm ---\n%s", probeArm)
			}
		})
	}
}

// TestBoardProbeArmPredicateMatchesBusinessArm 断言两条臂的谓词**逐字同构**
// （只差别名）⇒ 这是「同一宇宙」不变量的最强形式。
//
// ★ 判据用「包含」而不是文本抽取后比较：★ 业务臂里嵌着积分表达式的子查询
// ★ （它自己带一个 WHERE ms.id = 1）⇒ ★ 按「第一条 WHERE」抽取会抽错。
func TestBoardProbeArmPredicateMatchesBusinessArm(t *testing.T) {
	tr := boardTimeRange{}
	where, _ := boardLogsWhereTerminal(tr, "tenant-a", true)
	wantProbeWhere := boardRewriteAlias(where, "p")

	sqlText := boardSummaryLogsUnionSQL(where, boardCreditsExpr("tenant-a"), true)
	arms := boardUnionArms(sqlText)
	var businessArm, probeArm string
	for _, arm := range arms {
		if strings.Contains(arm, "session_turns_probe_hot") {
			probeArm = arm
			continue
		}
		if strings.Contains(arm, "request_logs_with_current_month") {
			businessArm = arm
		}
	}
	if businessArm == "" || probeArm == "" {
		t.Fatalf("expected both a business arm and a probe arm")
	}
	// 业务臂原样带着它自己的谓词。
	if !strings.Contains(businessArm, where) {
		t.Fatalf("business arm does not carry the shared predicate verbatim:\n%s", businessArm)
	}
	// 探测臂带的是**同一条谓词的别名改写版**（逐字）。
	if !strings.Contains(probeArm, wantProbeWhere) {
		t.Fatalf("probe arm does not carry the alias-rewritten shared predicate\nwant substring: %s\n--- probe arm ---\n%s", wantProbeWhere, probeArm)
	}
	// 探测臂**不得**携带业务臂的原始谓词（带 r. 的那条）。
	if strings.Contains(probeArm, where) {
		t.Fatalf("probe arm still carries the business-arm predicate (alias %q)\n--- probe arm ---\n%s", where, probeArm)
	}
}

// TestBoardRewriteAliasDoesNotTouchSimilarIdentifiers 钉住别名改写的**边界**：
// ★ 只改**独立的** `r.`；★ `error.` 这类「以 r 结尾的标识符」不得被误改。
func TestBoardRewriteAliasDoesNotTouchSimilarIdentifiers(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"r.ts >= $1 AND r.tenant_id = $3", "p.ts >= $1 AND p.tenant_id = $3"},
		{"r.request_status IN ('success')", "p.request_status IN ('success')"},
		{"error.kind = 'x' AND r.ts > $1", "error.kind = 'x' AND p.ts > $1"},
		{"dir.r.ts", "dir.p.ts"}, // 前缀是标识符的一部分 ⇒ 不是别名
	}
	for _, tc := range cases {
		if got := boardRewriteAlias(tc.in, "p"); got != tc.want {
			t.Errorf("boardRewriteAlias(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// 同别名 ⇒ 原样返回。
	if got := boardRewriteAlias("r.ts >= $1", "r"); got != "r.ts >= $1" {
		t.Errorf("alias rewrite to the same alias must be a no-op, got %q", got)
	}
}
