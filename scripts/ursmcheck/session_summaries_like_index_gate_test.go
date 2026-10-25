package ursm_test

// session_summaries_like_index_gate_test.go
//
// 守的是一条【生产实测】的慢 SQL（2026-10-27 R154 轮）：
//
//	admin/session_catalog_usage.go 的 catalogSearchSQL 用 likeContains(q)
//	（★ 生成 `%q%`，★ 中缀匹配）对 session_summaries 的三列做 OR 的 ILIKE。
//	★ 生产 252（868,711 行）实测：
//	  · EXPLAIN（不带 ANALYZE）⇒ Parallel Seq Scan on session_summaries
//	  · EXPLAIN ANALYZE ⇒ Execution Time = 40,415.968 ms（★ 40.4 秒）
//	    （Planning Time 仅 16.679 ms ⇒ ★ 不是规划问题，★ 是执行问题）
//	  · 对照「只查 session_key 一列」⇒ Index Only Scan using session_summaries_pkey
//	★ 而 mergeCatalogSearch 的 ctx 超时是 2 秒
//	⇒ ⇒ ★★★ 每次都超时 ⇒ ⇒ 静默降级成「没搜到」（用户看不到超时，只看到空结果）
//
// ⇒ ★★ 本门守的是【台账与源码的一致性】：
//	  ★ 源码里对 session_summaries 的每一个 LIKE/ILIKE 查询列，
//	    ★ 都必须出现在台账里（不多不少）⇒ ★ 新加一个 LIKE 列而忘了登记 ⇒ 门红。
//	  ★ 反向：台账里登记了但源码里已经没有的列 ⇒ 门红（★ 台账腐化）。
//	  ★★ 「不许新增」⇒ ★ 台账只许缩小（★ 加了索引的列从台账删掉）。
//
// ★★ 门必须【有牙】⇒ ★ 本文件自带阳性对照：
//	  ★ 断言源码里【确实】存在 catalogSearchSQL 这条 LIKE 查询
//	    ⇒ ★ 否则「源里把 LIKE 删了」会让第 6 条断言空转 ⇒ ★ 门假绿。
//	  ⇒ ★★ 判定一道对照「有没有牙」＝故意破坏它要守的东西，看它是否红。

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	ssLikeLedgerRel = "scripts/ursmcheck/session_summaries_like_index_ledger.tsv"
	ssLikeSourceRel = "admin/session_catalog_usage.go"
)

// ssLikeAllowedVerdicts 是台账 verdict 列的允许取值。
// ★ 「indexed」允许出现，但【不许】长期挂着（★ 加了索引就该从台账删掉）。
var ssLikeAllowedVerdicts = map[string]bool{
	"needs-index":   true,
	"indexed":       true,
	"accepted-risk": true,
}

// ssLikeLedgerRow 是一条台账数据行。
type ssLikeLedgerRow struct {
	File        string
	Line        int
	Occ         int
	Scope       string
	Kind        string
	Usage       string
	Volume      string
	Writer      string
	Verdict     string
	Reason      string
	Evidence    string
	Owner       string
	Fingerprint string
}

// ssLikeReadLedger 读台账并逐行校验形状。
func ssLikeReadLedger(t *testing.T) []ssLikeLedgerRow {
	t.Helper()
	p := filepath.Join("..", "..", ssLikeLedgerRel)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("★ 读不到台账 %s：%v", ssLikeLedgerRel, err)
	}
	var rows []ssLikeLedgerRow
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		// ★ 表头行（第一列字面量 "file"）不是数据行 ⇒ 跳过。
		//   ★ 照抄 scripts/ursmcheck/pipefail_race_ledger_test.go 的同一约定。
		if cols[0] == "file" {
			continue
		}
		if len(cols) != 13 {
			t.Fatalf("★ 台账行列数 = %d，不足 13（末列必须是 fingerprint）：%q", len(cols), line)
		}
		lineNo := atoiStrict(t, cols[1])
		occ := atoiStrict(t, cols[2])
		rows = append(rows, ssLikeLedgerRow{
			File: cols[0], Line: lineNo, Occ: occ, Scope: cols[3], Kind: cols[4],
			Usage: cols[5], Volume: cols[6], Writer: cols[7], Verdict: cols[8],
			Reason: cols[9], Evidence: cols[10], Owner: cols[11], Fingerprint: cols[12],
		})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读台账出错：%v", err)
	}
	// ★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论（照抄 pipefail 台账门的做法与理由）。
	if len(rows) == 0 {
		t.Fatalf("★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论（%s）", ssLikeLedgerRel)
	}
	return rows
}

func atoiStrict(t *testing.T, s string) int {
	t.Helper()
	if s == "" {
		t.Fatalf("★ 空字符串不是合法整数")
	}
	v := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("★ %q 不是合法整数", s)
		}
		v = v*10 + int(r-'0')
	}
	return v
}

// ssLikeSourceColumns 返回【源码里】对 session_summaries 做 LIKE/ILIKE 的列名集合。
// ★ 口径：取 catalogSearchSQL 这条常量里出现的 ILIKE 的目标表达式，
//   再把它归一成列名（COALESCE(title,'') ⇒ title）。
func ssLikeSourceColumns(t *testing.T) map[string]bool {
	t.Helper()
	p := filepath.Join("..", "..", ssLikeSourceRel)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("★ 读不到 %s：%v", ssLikeSourceRel, err)
	}
	src := string(raw)
	// ★ 阳性对照：这条 LIKE 查询必须【确实存在】。
	//   ⇒ 否则「源里把 LIKE 删了」会让集合比对空转 ⇒ 门假绿。
	if !strings.Contains(src, "catalogSearchSQL") {
		t.Fatalf("★ %s 里找不到 catalogSearchSQL ⇒ ★ 本门的比对基准没了（源被重构？）⇒ 门已失效", ssLikeSourceRel)
	}
	if !strings.Contains(src, "ILIKE") {
		t.Fatalf("★ %s 里找不到 ILIKE ⇒ ★ 本门守的 LIKE 查询已消失 ⇒ ★ 台账与门都应重新评估（★ 不是「门绿了」）", ssLikeSourceRel)
	}
	out := map[string]bool{}
	// 形如：`session_key ILIKE $1` / `OR COALESCE(title, '') ILIKE $1`
	re := regexp.MustCompile(`(?i)([a-z_][a-z0-9_]*)\s+ILIKE|COALESCE\(\s*([a-z_][a-z0-9_]*)\s*,\s*''\s*\)\s+ILIKE`)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		col := m[1]
		if col == "" {
			col = m[2]
		}
		if col == "" {
			continue
		}
		out[col] = true
	}
	if len(out) == 0 {
		t.Fatalf("★ 没能从 %s 解析出任何 LIKE 查询列 ⇒ 解析口径坏了 ⇒ 门已失效", ssLikeSourceRel)
	}
	return out
}

// TestSessionSummariesLikeIndexLedgerShape：台账的形状自洽（列数 / 主键 / 裁决列）。
func TestSessionSummariesLikeIndexLedgerShape(t *testing.T) {
	rows := ssLikeReadLedger(t)

	seen := map[string]bool{}
	for _, r := range rows {
		key := r.File + "\t" + r.Fingerprint + "\t" + ssLikeItoa(r.Occ)
		if seen[key] {
			t.Errorf("★ 台账主键重复：%q ⇒ 主键不唯一 ⇒ 同形行会互相冒用裁决", key)
		}
		seen[key] = true

		if !ssLikeAllowedVerdicts[r.Verdict] {
			t.Errorf("★ 台账 verdict=%q 不在允许集合 %v ⇒ 门不接受未知裁决（行 %s:%d）",
				r.Verdict, ssLikeAllowedVerdicts, r.File, r.Line)
		}
		if strings.TrimSpace(r.Reason) == "" {
			t.Errorf("★ 台账 reason 为空（%s:%d，列 %s）⇒ ★ 必须写清为什么这一列还没有可用索引",
				r.File, r.Line, r.Writer)
		}
		if strings.TrimSpace(r.Evidence) == "" {
			t.Errorf("★ 台账 evidence 为空（%s:%d，列 %s）⇒ ★ 必须留下可复核的读数",
				r.File, r.Line, r.Writer)
		}
		if strings.TrimSpace(r.Fingerprint) == "" {
			t.Errorf("★ 台账 fingerprint 为空（%s:%d）⇒ ★ 主键会退化", r.File, r.Line)
		}
	}
}

func ssLikeItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// TestSessionSummariesLikeIndexLedgerMatchesSource：★ 台账登记的列集合
// 必须与源码里实际存在的 LIKE 查询列集合【完全一致】（不多不少）。
func TestSessionSummariesLikeIndexLedgerMatchesSource(t *testing.T) {
	rows := ssLikeReadLedger(t)
	srcCols := ssLikeSourceColumns(t)

	ledgerCols := map[string]bool{}
	for _, r := range rows {
		ledgerCols[r.Writer] = true
	}

	var missing, extra []string
	for c := range srcCols {
		if !ledgerCols[c] {
			missing = append(missing, c)
		}
	}
	for c := range ledgerCols {
		if !srcCols[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("★ 源码里对 session_summaries 的 LIKE 查询列 %v 【没有登记进台账】⇒\n"+
			"    ★ 新加一个 LIKE 列而忘了登记 ⇒ 它会在生产上退化成 Seq Scan（★ 本轮实测 40.4 秒）\n"+
			"    ⇒ 请登记并写清 reason/evidence，或给它加 trigram 索引后从台账移除",
			missing)
	}
	if len(extra) > 0 {
		t.Errorf("★ 台账里登记了 %v，但源码里已经没有对应的 LIKE 查询了 ⇒ 台账腐化 ⇒ 请删除这些行", extra)
	}
}

// TestSessionSummariesLikeIndexLedgerOnlyShrinks：★ 台账只许缩小。
// ★ 「indexed」状态的行意味着「已加索引」⇒ ★ 它应当已被移除 ⇒ 门红。
func TestSessionSummariesLikeIndexLedgerOnlyShrinks(t *testing.T) {
	rows := ssLikeReadLedger(t)
	for _, r := range rows {
		if r.Verdict == "indexed" {
			t.Errorf("★ 台账里还有 verdict=indexed 的行（%s:%d，列 %s）⇒\n"+
				"    ★ 「已加索引」的行应当【从台账里删掉】⇒ ★ 台账只许缩小、不许长胖",
				r.File, r.Line, r.Writer)
		}
	}
}
