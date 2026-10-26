//go:build !integration

package main

// ═══════════════════════════════════════════════════════════════════════
// §143.99 会话正文族（session_bodies）读侧【长尾放大】门
//
// ★ ★ 为什么要有这道门：
//
//   §143.98 实测「读侧 3 条 SELECT 占 6,716 s DB 时间 = promote 的 3.1 倍」
//   ⇒ ⇒ §143.99 把那个累计数字拆成了可判定的算术（2026-10-24 于生产 252 只读实测）：
//
//     ① 这 3 条语句【每一次调用都返回 0 行】——★ 量具自检锚点验证过
//        pg_stat_statements.rows 的语义（恒空投影 ⇒ 0；count(*) ⇒ 1；
//        generate_series(1,3) ⇒ 3）⇒ ⇒ 「返回 0 行」是真的。
//     ② 但每次调用仍要拉 ~218 KB（shared_blks_hit+read × 8192 / calls）
//        ⇒ ⇒ 一次都没产出，纯浪费。
//     ③ 而【单次执行成本只有 5~9 ms】（★ 381 轮次的 session，A 形态中位数 9.1 ms）
//        ⇒ ⇒ 解释不了 182 ms 的平均。
//     ④ ⇒ ⇒ **均值是被长尾拉高的**：max_exec_time 10,200 ms ÷ mean 182.4 ms
//        = **55.9×**。
//     ⑤ 长尾的来源已定位：**L3 冷启动回退的 turns 腿是 `WHERE t.session_id = $1`，
//        【无轮次上限】** ⇒ ⇒ 一次调用要把该 session 的【全部】轮次过一遍
//        ⇒ ⇒ 命中 4 万轮次的探测型 session（sys:probe:*）时：
//        单次 **32.7 s（热）/ 155.2 s（冷）**，而纯 turns 腿本身就要 35.9 s。
//
//   ⇒ ⇒ **「读放大」不是「每次都慢」，而是「绝大多数很快 + 少数极慢」**。
//   ⇒ ⇒ 这道门把那条算术钉成【可执行判据】，并让「缺陷已登记」与
//        「缺陷已修好」两种状态都是绿的（★ 登记式门）。
// ═══════════════════════════════════════════════════════════════════════

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	section14399AuditSQL = "scripts/audit/audit_session_bodies_read_amplification.sql"
	section14399SchemaGo = "db/request_logs_view_schema.go"
	section14399Runbook  = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14399Lessons  = "LESSONS.md"
	section14399Agents   = "AGENTS.md"

	// ★ ★ 以下全部是 2026-10-24 于生产 252 只读实测的读数
	//   （★ 口径与 §143.98 一致：pg_stat_statements 的累计量 + 交错采样的中位数）。

	// ★ 三条读放大语句的合计（Q1/Q8 现场可复算）。
	section14399MeasuredCalls   = 37_383
	section14399MeasuredTotalMs = 6_819_000
	section14399MeasuredMeanMs  = 182.4
	section14399MeasuredMaxMs   = 10_200

	// ★ 每次调用平均拉取的字节数（≈ 恰好一个 outbound_body 的量级）。
	//   ★ 独立读数互证：hot 表 outbound_body 的 max = 227,885 B。
	section14399MeasuredBytesPerCall = 218_000
	section14399MeasuredMaxOutbound  = 227_885

	// ★ 单次执行成本的实测中位数（381 轮次的 session，A 形态交错采样）。
	section14399SingleCallMs = 9.1

	// ★ 长尾源：探测型 session 的累计轮次数（父表 09 月分区实测）。
	section14399LongTailTurns = 40_381
)

func repoRoot14399(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14399(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14399(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ①：只读审计脚本必须在场、必须只读、必须自带【量具自检】
// ═══════════════════════════════════════════════════════════════════════

func TestSection14399_AuditScriptPresentAndReadOnly(t *testing.T) {
	raw := readRepoFile14399(t, section14399AuditSQL)

	// ★ Q1~Q9 九个探针一个都不能少（★ 少一项读数就有一项没人负责）。
	for _, q := range []string{"Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Q7", "Q8", "Q9"} {
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

	// ★ 【量具自检】必须在场：★ 没有它，Q1 的「rows=0」会被误读。
	// ★   anchor_zero_rows ⇒ 验证 rows 语义；tail_ratio ⇒ 验证长尾。
	for _, needle := range []string{
		"anchor_zero_rows", "anchor_one_row", "anchor_three_rows",
		"tail_ratio", "max_exec_ms", "bytes_per_call",
	} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 审计脚本缺 `%s` —— ★ 量具自检与长尾算术必须在脚本里被现场算出来", needle)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ②：★★ 长尾算术（本门的心脏）—— 「读放大」必须能被四条恒等式解释
// ═══════════════════════════════════════════════════════════════════════

func TestSection14399_TailArithmetic(t *testing.T) {
	calls := float64(section14399MeasuredCalls)
	total := float64(section14399MeasuredTotalMs)
	mean := section14399MeasuredMeanMs
	maxMs := float64(section14399MeasuredMaxMs)

	// ★ ① 恒等式：calls × mean ≈ total。
	//   ★ 若不成立 ⇒ ⇒ 说明「累计时间」与「平均/次数」取自不同口径
	//   ★   ⇒ ⇒ §143.99 的全部结论都要重算。
	recomputed := calls * mean
	if diff := recomputed - total; diff > total*0.05 || diff < -total*0.05 {
		t.Errorf("★ 恒等式不成立：calls(%d) × mean(%.1f ms) = %.0f ms，与实测 total(%d ms) 差 %.1f%% "+
			"⇒ ⇒ 「累计时间」与「平均/次数」不是同一口径 ⇒ ⇒ §143.99 的结论要重算",
			section14399MeasuredCalls, mean, recomputed, section14399MeasuredTotalMs,
			diff/total*100)
	}

	// ★ ② 「均值被长尾拉高」：max / mean 必须远大于 1。
	//   ★ 这是 §143.99 的中心结论：★ 若 mean ≈ max ⇒ ⇒ 「每次都慢」成立
	//   ★   ⇒ ⇒ 「长尾」这个解释就错了 ⇒ ⇒ 门必须红。
	tailRatio := maxMs / mean
	if tailRatio < 10 {
		t.Errorf("★ max/mean = %.1f×（max %.0f ms / mean %.1f ms）< 10 ⇒ ⇒ "+
			"★ 「读放大是少数超长调用拉高均值」这个结论不成立 "+
			"⇒ ⇒ §143.99 的机制解释错了，必须重新核对",
			tailRatio, maxMs, mean)
	}

	// ★ ③ 「总耗时可由少数满速调用解释」：total / max 必须远小于 calls。
	//   ★   ⇒ ⇒ 若这一条不成立 ⇒ ⇒ 总耗时是被【所有】调用均匀摊的
	//   ★   ⇒ ⇒ 「长尾」与「均匀」互相排斥 ⇒ ⇒ 只能有一个成立。
	impliedFullSpeed := total / maxMs
	if impliedFullSpeed >= calls {
		t.Errorf("★ total/max = %.0f 次（满速调用数）≥ 总调用数 %d ⇒ ⇒ "+
			"★ 总耗时不可能由少数超长调用解释 ⇒ ⇒ 「长尾」结论与算术互相矛盾",
			impliedFullSpeed, section14399MeasuredCalls)
	}

	// ★ ④ 「单次执行成本解释不了均值」：mean 必须远大于单次实测。
	//   ★   ⇒ ⇒ 若 mean ≈ 单次实测 ⇒ ⇒ 不存在长尾问题（本轮整个机制就没有对象）。
	meanVsSingle := mean / section14399SingleCallMs
	if meanVsSingle < 5 {
		t.Errorf("★ mean(%.1f ms) / 单次实测(%.1f ms) = %.1f× < 5 ⇒ ⇒ "+
			"★ 单次执行成本足以解释均值 ⇒ ⇒ 「读放大」不需要长尾来解释",
			mean, section14399SingleCallMs, meanVsSingle)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ③：★ turns 腿「无轮次上限」是现状缺陷 ⇒ 必须已登记；
//
//	★ 将来有人给它加上上限（= 属主授权后的修复）⇒ 门必须【仍绿】。
// ═══════════════════════════════════════════════════════════════════════

func TestSection14399_TurnsLegLimitRegistered(t *testing.T) {
	raw := readRepoFile14399(t, section14399SchemaGo)

	// ★ 取 SessionFamilyTurnsForSessionSQL 的函数体（★ 只取这个函数，别把整个文件算进来）。
	// ★   —— 文件里别处出现 "LIMIT" 与本缺陷无关。
	i := strings.Index(raw, "func SessionFamilyTurnsForSessionSQL() string {")
	if i < 0 {
		t.Fatalf("★ %s 里找不到 `func SessionFamilyTurnsForSessionSQL() string {` —— ★ 判据失效", section14399SchemaGo)
	}
	j := strings.Index(raw[i:], "\n}\n")
	if j < 0 {
		t.Fatalf("★ 找不到 SessionFamilyTurnsForSessionSQL 的函数体结尾 —— ★ 判据失效")
	}
	body := raw[i : i+j]

	// ★ 词边界判据：★ 「LIMIT」必须作为独立 token 出现（★ 免得 "LIMITING" 之类逃过）。
	limitRE := regexp.MustCompile(`(?m)\bLIMIT\b`)
	hasLimit := limitRE.MatchString(body)

	runbook := readRepoFile14399(t, section14399Runbook)

	if !hasLimit {
		// ★ 现状：turns 腿按 session_id 取【全部】轮次 ⇒ ⇒ 缺陷仍在
		// ★   ⇒ ⇒ 必须已在 runbook 里带可复算的数字登记。
		if !strings.Contains(runbook, "§143.99") {
			t.Errorf("★ SessionFamilyTurnsForSessionSQL 的 turns 腿仍无轮次上限 " +
				"⇒ ⇒ 命中多轮次 session 时单次调用可达数十秒（★ 实测 40,381 轮次 ⇒ 32.7~155.2 s）" +
				"⇒ ⇒ ★ 但 runbook 里没有 `§143.99` 登记这个缺陷")
		}
		for _, needle := range []string{"40,381", "10,200", "37,383", "6,819"} {
			if !strings.Contains(runbook, needle) {
				t.Errorf("★ runbook 的 §143.99 缺 `%s` —— ★ 缺陷登记必须带可复算的数字", needle)
			}
		}
	}
	// ★ 有上限 ⇒ 门仍绿（★ 登记式门的定义：★ 缺陷修好不得让门变红）。
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ④：文档登记（★ 过程可查）
// ═══════════════════════════════════════════════════════════════════════

func TestSection14399_DocsRegistered(t *testing.T) {
	runbook := readRepoFile14399(t, section14399Runbook)
	if !strings.Contains(runbook, "§143.99") {
		t.Errorf("runbook 里没有 `§143.99` —— ★ 过程不可查")
	}
	// ★ 必须带上「读放大 = 长尾」这个结论（★ 它改变了修法的方向）。
	for _, needle := range []string{"outbound_body", "tail_ratio", "218"} {
		if !strings.Contains(runbook, needle) {
			t.Errorf("★ runbook 的 §143.99 缺 `%s` —— ★ 长尾结论是本轮的核心，必须在册", needle)
		}
	}
	lessons := readRepoFile14399(t, section14399Lessons)
	if !strings.Contains(lessons, "§295") {
		t.Errorf("LESSONS.md 里没有 `§295` —— ★ 通用式没沉淀")
	}
	agents := readRepoFile14399(t, section14399Agents)
	if !strings.Contains(agents, "R144") {
		t.Errorf("AGENTS.md 里没有 `R144` —— ★ 交接没更新")
	}
}
