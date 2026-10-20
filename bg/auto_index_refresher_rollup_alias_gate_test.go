// auto_index_refresher_rollup_alias_gate_test.go — 2026-10-20 退役轮：
// rollupCredentialModelIndexSQL 的**别名闭包**门（静态、不需要 PG）。
//
// ★ 缺陷本体（真库日志每 5s 一条 42703）：
//
//	890（v1 族真删）把 half-1 的 v1 腿整条删掉、换成会话腿。会话腿内层是
//	8 个**表达式**，外层却沿用 v1 腿的列名引用它们
//	（rl.credential_id / rl.model_key / rl.mo_canonical_id / rl.mo_billing_mode /
//	  rl.mo_price_in / rl.mo_price_out / rl.success / rl.latency_ms）。
//	表达式没有别名 ⇒ PG 列名变成 "?column?" ⇒ 外层 42703 ⇒ 整条 rollup 挂。
//
// ★ 为什么这道门与真库门并存：
//   - 真库门（auto_index_refresher_rollup_realdb_test.go）最强，但只在
//     TEST_DATABASE_URL 存在时跑 ⇒ CI 无 PG 时它恒绿（SKIP）。
//   - 这道门是**形状**判据：会话腿投影的每个项都必须带别名，且外层引用到的
//     每一个 rl.<col> 都必须在投影里被产出 ⇒ 无 PG 也能红。
//     两道门互为对照：真库门抓「PG 语义层」，本门抓「改写层」。
//
// ⚠ 判据纪律（R65 教训）：**先剥注释再断言**。本文件新增的注释里就写着
//
//	"rl.credential_id"（解释缺陷），不剥注释会把注释里的词当成真引用。
package bg

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// splitTopLevelCommas 按**顶层**逗号切分 SQL 片段（括号内/字面量内的逗号
// 不切）。深度只看圆括号。
func splitTopLevelCommas(s string) []string {
	var out []string
	depth, start := 0, 0
	inLiteral := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inLiteral:
			if c == '\'' {
				inLiteral = false
			}
		case c == '\'':
			inLiteral = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func TestRollupCredentialModelIndex_SessionLegProjectionIsAliased(t *testing.T) {
	clean := stripSQLCommentsAndLiterals(rollupCredentialModelIndexSQL)

	hotIdx := strings.Index(clean, "FROM session_turns_hot t")
	if hotIdx < 0 {
		t.Fatalf("会话腿不在（找不到 FROM session_turns_hot t）——rollup 语句形态变了，需复核；\n%s", clean)
	}
	openIdx := strings.LastIndex(clean[:hotIdx], "FROM (")
	if openIdx < 0 {
		t.Fatalf("找不到会话腿的 FROM ( 开头；\n%s", clean)
	}
	proj := clean[openIdx+len("FROM (") : hotIdx]

	aliasRe := regexp.MustCompile(`(?i)\bAS\s+([a-z_][a-z0-9_]*)\s*$`)
	produced := map[string]bool{}
	for _, item := range splitTopLevelCommas(proj) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		m := aliasRe.FindStringSubmatch(item)
		if m == nil {
			t.Fatalf("会话腿投影项没有别名 —— 外层按 v1 腿列名引用 rl.*，无别名 ⇒ PG 列名为 ?column? ⇒ 42703：\n  %s", item)
		}
		produced[strings.ToLower(m[1])] = true
	}

	refRe := regexp.MustCompile(`\brl\.([a-z_][a-z0-9_]*)`)
	refs := map[string]bool{}
	for _, m := range refRe.FindAllStringSubmatch(clean, -1) {
		refs[strings.ToLower(m[1])] = true
	}
	if len(refs) == 0 {
		t.Fatalf("外层没有 rl.* 引用 —— 语句形态变了，需复核；\n%s", clean)
	}

	var missing []string
	for ref := range refs {
		if !produced[ref] {
			missing = append(missing, ref)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("外层引用了会话腿未产出的列：%v\n"+
			"（会话腿投影必须为这 8 个列各给一个别名：credential_id / model_key / "+
			"mo_canonical_id / mo_billing_mode / mo_price_in / mo_price_out / success / latency_ms）",
			missing)
	}
}
