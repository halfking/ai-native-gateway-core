//go:build !integration

package main

// ═══════════════════════════════════════════════════════════════════════
// §143.100 L3 冷启动回退 turns 腿的【索引缺口】门
//
// ★ ★ 为什么要有这道门：
//
//   §143.99 实测出读侧长尾：L3 回退的 turns 腿在 4 万轮次 session 上单次
//   要 24,763 ms。EXPLAIN 显示它走的是 `(tenant_id, ts)` 索引 +
//   **Filter: session_id** ⇒ ⇒ 同一个 session 的 42,289 行里过滤出
//   40,381 行 ⇒ ⇒ 成本 = 42,289 次回堆。
//
//   ★ ★★ 2026-10-24（§143.100）把「为什么 planner 不走精确索引」查清了：
//
//   ① 现有的 `(session_id, turn_no DESC)` 索引**能精确 Index Cond**，
//      但它**排序键不匹配**（ORDER BY ts ≠ 索引序）；
//   ② 现有的 `(tenant_id, ts)` 索引**能出 ts 序**，但 session_id 只能
//      **回堆过滤**；
//   ③ ⇒ ⇒ **planner 只能在「精确但乱序」与「有序但要过滤」之间二选一**
//      ⇒ ⇒ 它选了后者 ⇒ ⇒ **缺的正是一条 `(session_id, ts DESC)`。**
//   ⇒ ⇒ **⇒ 正确修法 = 加这条索引（零语义变更，只加索引不改 SQL 形态）。**
//
//   ★ ★★ 这道门要守的是【一个很容易被踩的坑】：
//
//   「把 ORDER BY ts DESC 换成 ORDER BY turn_no DESC」实测快 **4,762×**
//   （24,763 ms → 5.2 ms）⇒ ⇒ **极其诱人** ⇒ ⇒ 但它**不是语义等价**：
//   实测会话内 turn_no 与 ts 的秩不一致率 **93.9%**、最大秩差 **12,482**
//   ⇒ ⇒ ⇒ **它会取到【不同的那一轮】** ⇒ ⇒ ⇒ **不许那样改。**
//   ⇒ ⇒ **⇒ 判据 ③ 就是这条守卫**：★ 拦住「用性能换语义」的改法。
// ═══════════════════════════════════════════════════════════════════════

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	section143100AuditSQL = "scripts/audit/audit_l3_turns_leg_index_gap.sql"
	section143100WiringGo = "cmd/gateway/main_v3_wiring.go"
	section143100MigrateD = "sql/migrations/startup"
	section143100Runbook  = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section143100Lessons  = "LESSONS.md"
	section143100Agents   = "AGENTS.md"

	// ★ ★ 2026-10-24 于生产 252 只读实测的读数（★ 全部可由审计脚本复算）：
	//   ① 基线（ORDER BY ts DESC LIMIT 1）在 4 万轮次 session 上 = 24,763 ms
	//   ② 换排序键（ORDER BY turn_no DESC LIMIT 1）= 5.2 ms ⇒ 快 4,762×
	//   ③ ★ 但秩不一致率 93.9%、最大秩差 12,482 ⇒ ⇒ ②【不是等价优化】
	//   ④ planner 的回堆行数 = 42,289（09 月分区）
	section143100BaselineMs   = 24_763
	section143100TurnNoFormMs = 5.2
	section143100SpeedupX     = 4_762
	section143100MismatchPct  = 93.9
	section143100MaxRankDiff  = 12_482
	section143100RecheckRows  = 42_289
)

func repoRoot143100(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile143100(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot143100(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ①：只读审计脚本必须在场、必须只读、必须自带【量具自检】与【计划对照】
// ═══════════════════════════════════════════════════════════════════════

func TestSection143100_AuditScriptPresentAndReadOnly(t *testing.T) {
	raw := readRepoFile143100(t, section143100AuditSQL)

	// ★ Q1~Q7 七个探针一个都不能少（★ 少一项读数就有一项没人负责）。
	for _, q := range []string{"Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Q7"} {
		if !strings.Contains(raw, "=== "+q+" ") {
			t.Errorf("★ 只读审计脚本缺 %s 探针 —— ★ 少一项读数就有一项没人负责", q)
		}
	}

	// ★ 必须是【只读】的：★ 不许出现行首写/DDL 关键字。
	// ★ 这条判据的价值：★ 审计脚本会被人在生产上直接跑 ⇒ ⇒ 它自己绝不能写。
	writeRE := regexp.MustCompile(`(?m)^[[:space:]]*(INSERT|UPDATE|DELETE|DROP|CREATE|ALTER|TRUNCATE|REINDEX|VACUUM|GRANT|REVOKE|COPY)\b`)
	if m := writeRE.FindString(raw); m != "" {
		t.Errorf("★ 只读审计脚本里出现写/DDL 关键字 %q —— ★ 它会在生产上被执行 ⇒ 绝不能写", strings.TrimSpace(m))
	}

	// ★ 【量具自检】必须在场：★ 没有它，Q7 的 rows 列不可信。
	for _, needle := range []string{
		"anchor_zero_rows", "anchor_one_row", "anchor_three_rows",
	} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 审计脚本缺 `%s` —— ★ 量具自检必须在脚本里被现场跑出来", needle)
		}
	}

	// ★ ★★ 【计划对照的四个形态】必须在场：★ 这是本轮的核心证据链
	//   （基线 / 换排序键 / 只加上限 / 对照）⇒ ⇒ 少了任一个都无法判定。
	for _, needle := range []string{
		"Q4-A:", "Q4-B:", "Q4-C:", "Q4-D:",
	} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 审计脚本缺计划对照形态 `%s` —— ★ 四个形态缺一个就判不出「换排序键 vs 只加上限」", needle)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ②：★★ 索引缺口必须带数字登记（★ 登记式：修好了仍绿）
// ═══════════════════════════════════════════════════════════════════════

func TestSection143100_IndexGapRegistered(t *testing.T) {
	// ★ 仓内是否已有「turns 族 (session_id, ts DESC)」的迁移？
	//   ★ 有 ⇒ ⇒ 缺口已补 ⇒ ⇒ 门不再要求登记（★ 但门仍绿）。
	//   ★ 没有 ⇒ ⇒ 缺口仍在 ⇒ ⇒ 必须已在 runbook 里带可复算的数字登记。
	//
	// ★★★ ★★★ 这里的第一版判据**太宽、恒真、形同虚设**：
	//   第一版写的是「文件里同时出现 `session_id` 与 `ts DESC`」⇒ ⇒ 实测**17 个
	//   startup 迁移都命中**（000_base_tables / 430_sessions_v2_schema / 526 /
	//   640 / 876 / 890 ……）⇒ ⇒ `hasGapFix` **永远是 true** ⇒ ⇒
	//   ⇒ **判据②的「必须登记」那一半从未生效** ⇒ ⇒ 门真的没牙
	//   ⇒ ⇒ 是【变异台 M04（删掉关键数字 4,762 门仍绿）】把它抓出来的。
	// ⇒ ⇒ 现在用【精确形态】匹配：必须是给 public.session_turns 建的
	//    btree (session_id, ts DESC) ⇒ ⇒ session_turns_hot / session_bodies
	//    都不会命中（`\b` 在 `_` 前不成立）。
	// ⚠ 动态生成形态（如 888 用 format(...) 拼 DDL）静态匹配不到 ⇒ ⇒
	//   那种情况会【保守地】判成「缺口仍在」⇒ ⇒ 要求登记 ⇒ ⇒ 宁多不少。
	idxGapRE := regexp.MustCompile(
		`(?is)CREATE\s+(?:UNIQUE\s+)?INDEX\s+[^;]*?ON\s+(?:ONLY\s+)?public\.session_turns\b` +
			`[^;]*?USING\s+btree\s*\(\s*session_id\s*,\s*ts\s+DESC`)
	hasGapFix := false
	migDir := filepath.Join(repoRoot143100(t), section143100MigrateD)
	entries, err := os.ReadDir(migDir)
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", section143100MigrateD, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body := readRepoFile143100(t, filepath.Join(section143100MigrateD, e.Name()))
		if idxGapRE.MatchString(body) {
			hasGapFix = true
			break
		}
	}

	runbook := readRepoFile143100(t, section143100Runbook)

	if !hasGapFix {
		if !strings.Contains(runbook, "§143.100") {
			t.Errorf("★ turns 族【没有 (session_id, ts DESC) 索引】⇒ ⇒ L3 回退的 turns 腿在多轮次 session 上"+
				"仍会退化成「回堆 %d 行再过滤」⇒ ⇒ 单次可达 %d ms "+
				"⇒ ⇒ ★ 但 runbook 里没有 `§143.100` 登记这个缺陷",
				section143100RecheckRows, section143100BaselineMs)
		}
		// ★ 登记必须带可复算的数字。
		for _, needle := range []string{"24,763", "4,762", "93.9", "42,289"} {
			if !strings.Contains(runbook, needle) {
				t.Errorf("★ runbook 的 §143.100 缺 `%s` —— ★ 缺陷登记必须带可复算的数字", needle)
			}
		}
	}
	// ★ 缺口已补 ⇒ 门仍绿（★ 登记式门的定义：★ 缺陷修好不得让门变红）。
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ③：★★★ 排序键守卫 —— ★ 不许用「换排序键」换性能
//
//	★ 那条路快 4,762×，但秩不一致率 93.9% ⇒ ⇒ 它会取到【不同的那一轮】。
// ═══════════════════════════════════════════════════════════════════════

func TestSection143100_TurnsLegSortKeyStaysTs(t *testing.T) {
	raw := readRepoFile143100(t, section143100WiringGo)

	// ★ 必须仍是 `ORDER BY rl.ts DESC`（★ §143.77 的合同：
	//   「取该会话最近一个【真的有正文】的轮次」）。
	if !strings.Contains(raw, "ORDER BY rl.ts DESC") {
		t.Errorf("★ %s 的 L3 回退顶层排序键不再是 `ORDER BY rl.ts DESC` —— "+
			"★ §143.56 的合同是「取该会话最近一个【真的有正文】的轮次」"+
			"⇒ ⇒ 换排序键会取到【不同的那一轮】（★ 实测秩不一致率 %.1f%%、最大秩差 %d）"+
			"⇒ ⇒ ⇒ 要提性能请走【加 (session_id, ts DESC) 索引】，不要走换排序键",
			section143100WiringGo, section143100MismatchPct, section143100MaxRankDiff)
	}

	// ★ ★★ 显式拦住「用 turn_no 换 ts」这个具体误改（★ 它快 4,762×，最容易被下一个人顺手改掉）。
	for _, bad := range []string{
		"ORDER BY rl.turn_no DESC",
		"ORDER BY rl.ts DESC, rl.turn_no DESC",
	} {
		if strings.Contains(raw, bad) {
			t.Errorf("★ %s 出现了 `%s` —— ★ 实测它快 %d×（%d ms → %.1f ms），"+
				"★ 但会话内 turn_no 与 ts 的秩不一致率 %.1f%%、最大秩差 %d ⇒ ⇒ 它【不是等价优化】"+
				"⇒ ⇒ ⇒ 请改用【给 turns 族加 (session_id, ts DESC) 索引】",
				section143100WiringGo, bad, section143100SpeedupX,
				section143100BaselineMs, section143100TurnNoFormMs,
				section143100MismatchPct, section143100MaxRankDiff)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ④：文档登记（★ 过程可查）
// ═══════════════════════════════════════════════════════════════════════

func TestSection143100_DocsRegistered(t *testing.T) {
	runbook := readRepoFile143100(t, section143100Runbook)
	if !strings.Contains(runbook, "§143.100") {
		t.Errorf("runbook 里没有 `§143.100` —— ★ 过程不可查")
	}
	// ★ 必须带上「换排序键不是等价优化」这个结论（★ 它是本轮最容易被踩的坑）。
	for _, needle := range []string{"turn_no", "93.9", "session_id, ts DESC"} {
		if !strings.Contains(runbook, needle) {
			t.Errorf("★ runbook 的 §143.100 缺 `%s` —— ★ 「换排序键 ≠ 等价优化」必须登记在册", needle)
		}
	}
	lessons := readRepoFile143100(t, section143100Lessons)
	if !strings.Contains(lessons, "§296") {
		t.Errorf("LESSONS.md 里没有 `§296` —— ★ 通用式没沉淀")
	}
	agents := readRepoFile143100(t, section143100Agents)
	if !strings.Contains(agents, "R145") {
		t.Errorf("AGENTS.md 里没有 `R145` —— ★ 交接没更新")
	}
}
