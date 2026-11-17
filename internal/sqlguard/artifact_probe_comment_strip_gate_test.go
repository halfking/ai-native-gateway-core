package sqlguard

// artifact_probe_comment_strip_gate_test.go —— 产物探针的「**缺席型判据必须剥注释**」门。
//
// # 背景（本轮真抓到的自伤：977 的产物探针是**毒丸**）
//
//	2026-11-17，245 部署后验收轮把 977 的产物探针表达式**拿到活库上求值**
//	（不是读代码，是让 PG 真的算一遍）：
//
//	  to_regclass('public.v_error_analysis_stats') IS NOT NULL          → t
//	  EXISTS (… idx_error_analysis_results_queue_uniq … indisunique)  → t
//	  EXISTS (… position('RETURNING' in p.prosrc) = 0)                  → **f**
//
//	⇒ 整条探针求值为 **false** ⇒ 走本通道的部署会被**自己的产物探针打死**
//	（与本文件同目录的 migration_artifact_probe_scope_gate_test.go 记的 972
//	事故**同型**，只是方向相反：972 是「断言了运行时状态」，977 是
//	「断言某 token 缺席，而 token 出现在**注释**里」）。
//
// # 根因
//
//	`pg_proc.prosrc` 是**函数体原文**，**含注释**；而 977 的函数体注释里
//	恰好写着「不再 RETURNING ⇒ 不再有「多行 ⇒ 抛错」」⇒ 命中。
//
//	⚠ 关键：`pg_get_functiondef()` **同样保留 plpgsql 体的注释**
//	（实测 RETURNING 在其 646 处命中）⇒ 「改用 pg_get_functiondef」**并不能**躲开。
//	⇒ 唯一正确的做法是**先把行注释剥掉再判词**：
//
//	    position('RETURNING' in regexp_replace(p.prosrc, '--[^\n]*', '', 'g')) = 0
//
// # 本门钉住什么
//
//	对部署通道里**每一个** `ledger_artifact_probe`：
//	  若它含有「某 token **缺席**」型判据（`position('<tok>' in <expr>) = 0`），
//	  则 <expr> **必须**先剥注释（`regexp_replace(..., '--…', '', 'g')`）。
//
//	⇒ 这条门把「注释解释得越详细、守卫越致命」这个坑变成机器可判的规则。
//
// # 为什么不是「一律不许断言缺席」
//
//	缺席型判据本身是对的（它表达「这个缺陷已消除」）；问题只出在**判词的位置**。
//	⇒ 规则是「**剥了注释再判**」，不是「不许判缺席」。
//
// # 与既有门的关系
//
//	· migration_artifact_probe_scope_gate_test.go：管「探针不许断言运行时可变状态」。
//	· 本门：管「探针判缺席时必须剥注释」。
//	⇒ 两条门**互补**，互不重叠。

import (
	"os"
	"strings"
	"testing"
)

// commentStripRe ＝剥行注释的 regexp_replace 形态（探针里必须出现它）。
//
// ★ 判据：`regexp_replace(` ＋ 一个含 `--` 的模式 ⇒ 认为该探针剥了注释。
var commentStripMarkers = []string{
	"regexp_replace(",
	"--",
}

// absenceAssertionScan ＝在探针 SQL 里找「缺席型判据」。
//
// 形态：`position('<token>' in <expr>) = 0`
// ★ <expr> 里可能含括号（`regexp_replace(p.prosrc, '--[^\n]*', ”, 'g')`）⇒
// 不能用一条正则吃下 ⇒ 用**括号配对扫描**。
type absenceAssertion struct {
	token string
	expr  string
}

// findAbsenceAssertions ＝扫出探针 SQL 里的全部缺席型判据。
func findAbsenceAssertions(sql string) []absenceAssertion {
	var out []absenceAssertion
	lower := strings.ToLower(sql)
	needle := "position("
	for i := 0; ; {
		idx := strings.Index(lower[i:], needle)
		if idx < 0 {
			break
		}
		start := i + idx
		// 跳过 position( 里的空白后，取第一个引号（token 的起始）。
		j := start + len(needle)
		for j < len(sql) && sql[j] == ' ' {
			j++
		}
		if j >= len(sql) || sql[j] != '\'' {
			i = start + len(needle)
			continue
		}
		// token = 引号内的原文。
		k := j + 1
		for k < len(sql) && sql[k] != '\'' {
			k++
		}
		if k >= len(sql) {
			break
		}
		token := sql[j+1 : k]
		// 找 token 之后的 `in`。
		rest := sql[k+1:]
		rel := strings.Index(strings.ToLower(rest), " in ")
		if rel < 0 {
			i = k + 1
			continue
		}
		exprStart := k + 1 + rel + len(" in ")
		// 括号配对，找出 <expr> 的结束位置。
		depth := 0
		exprEnd := -1
		for p := exprStart; p < len(sql); p++ {
			switch sql[p] {
			case '(':
				depth++
			case ')':
				if depth == 0 {
					exprEnd = p
				} else {
					depth--
				}
			}
			if exprEnd >= 0 {
				break
			}
		}
		if exprEnd < 0 {
			i = k + 1
			continue
		}
		expr := strings.TrimSpace(sql[exprStart:exprEnd])
		// 判据尾巴：`) = 0`（缺席）。
		tail := strings.TrimSpace(sql[exprEnd+1:])
		if strings.HasPrefix(tail, "= 0") {
			out = append(out, absenceAssertion{token: token, expr: expr})
		}
		i = exprEnd + 1
	}
	return out
}

// exprStripsComments ＝<expr> 是否**先剥注释再判词**。
func exprStripsComments(expr string) bool {
	for _, marker := range commentStripMarkers {
		if !strings.Contains(expr, marker) {
			return false
		}
	}
	return true
}

// TestAbsenceArtifactProbesMustStripComments —— 本门核心。
func TestAbsenceArtifactProbesMustStripComments(t *testing.T) {
	raw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 %s：%v", revisionSequenceScriptPath, err)
	}
	probes := parseArtifactProbes(t, string(raw))
	if len(probes) == 0 {
		t.Fatalf("解析不到任何 ledger_artifact_probe ⇒ 判据形态坏了（解析不出来 ≠ 没有违规）")
	}

	checked := 0
	for _, p := range probes {
		for _, absent := range findAbsenceAssertions(p.sql) {
			checked++
			if exprStripsComments(absent.expr) {
				continue
			}
			t.Errorf("产物探针 %q（第 %d 行）含**缺席型判据**却没剥注释：\n"+
				"    position('%s' in %s) = 0\n"+
				"  ★ `pg_proc.prosrc` 是**函数体原文（含注释）**，`pg_get_functiondef()` **同样保留注释**\n"+
				"    ⇒ 注释里出现该 token 就会让探针**恒假** ⇒ 部署被**自己的产物探针打死**\n"+
				"    （2026-11-17 实测：977 的 `position('RETURNING' in p.prosrc) = 0` 在 245 活库上求值为 f）\n"+
				"  ⇒ 修法：先剥注释再判词，例如\n"+
				"    position('%s' in regexp_replace(p.prosrc, '--[^\\n]*', '', 'g')) = 0",
				p.name, p.line, absent.token, absent.expr, absent.token)
		}
	}
	if checked == 0 {
		// ★ 不是「没有违规」，而是「这条门在本仓当前形态下没有可判的对象」。
		//   此时**响亮失败**：要么形态变了（判据被改名/挪走），要么这批探针
		//   真的一个缺席型判据都没有（那本门此刻无意义 ⇒ 应显式说明）。
		t.Fatalf("在 %s 里找不到任何缺席型判据（`position('…' in …) = 0`）⇒ "+
			"本门此刻**没有可判对象**（探针形态可能变了）⇒ 请人工确认后调整本门",
			revisionSequenceScriptPath)
	}
	t.Logf("缺席型判据 %d 处，全部已剥注释", checked)
}

// TestAbsenceProbeRegression977 —— 防复发的**钉死**用例（点名单）。
//
// ★ 为什么要有「点名」这一条：通用规则（上面那条门）依赖**解析形态**；
//
//	一旦探针写法变了（换引号、拆行），通用门会「解析不出来」⇒ 静默失效。
//	⇒ 另设一条点名断言，把 977 这条**曾经真炸过**的探针钉死。
func TestAbsenceProbeRegression977(t *testing.T) {
	raw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 %s：%v", revisionSequenceScriptPath, err)
	}
	text := string(raw)
	idx := strings.Index(text, `ledger_artifact_probe "977_pi_error_analysis_semantics.sql"`)
	if idx < 0 {
		t.Fatalf("找不到 977 的 ledger_artifact_probe ⇒ 登记形态变了")
	}
	// 取该调用点之后的那一行（断言 SQL 行）。
	after := text[idx:]
	nl := strings.Index(after, "\n")
	if nl < 0 {
		t.Fatalf("977 探针调用点后没有断言 SQL 行 ⇒ 登记形态变了")
	}
	sqlLine := after[nl+1:]
	if end := strings.Index(sqlLine, "\n"); end >= 0 {
		sqlLine = sqlLine[:end]
	}
	if !strings.Contains(sqlLine, "regexp_replace(") {
		t.Errorf("977 的产物探针**没有剥注释**：\n  %s\n"+
			"  ★ 2026-11-17 实测：未剥注释的形态在 245 活库上求值为 false ⇒ 毒丸",
			sqlLine)
	}
	if strings.Contains(sqlLine, "position('RETURNING' in p.prosrc) = 0") {
		t.Errorf("977 的产物探针退回了**未剥注释**的判据 ⇒ 毒丸复活")
	}
	// 正形判据也要在（只判「没有 RETURNING」是弱判据：函数体被换成空壳也能过）。
	for _, shape := range []string{
		"GET DIAGNOSTICS v_deleted_results = ROW_COUNT",
		"GET DIAGNOSTICS v_deleted_queue = ROW_COUNT",
	} {
		if !strings.Contains(sqlLine, shape) {
			t.Errorf("977 的产物探针缺少**正形判据** %q ⇒ 弱判据（空壳函数体也能过）", shape)
		}
	}
}
