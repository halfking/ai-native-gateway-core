//go:build !integration

package main

// §143.94 的门：★ 本轮是【session_turns 族逐列拆解轮】⇒ ⇒ 产出是
// 【两个大 jsonb 列的定性】+【一个是结构性重复、一个是设计使然】+【一个孤儿热分区】。
//
// ★★★★★★ 这个门要守的是【三件事不许被无声改掉】：
//
//	① ★★★★★★ 【逐列分解不许退化成单一口径】—— ★ `compression_meta` 与 `digest`
//	    ★ 必须【同一条 select 里并列】⇒ ⇒ 否则又退回「只挑一列代表整行」
//	② ★★★★★★ 【成因判据必须钉住】—— ★ 两条尺子：
//	    ★ ① 「逐轮重写」（★ `lag(...) OVER (PARTITION BY session_id ORDER BY turn_no)`）
//	    ★ ② 「大对象的规模」（★ `pg_column_size(...) > 8192` 的会话数与字节）
//	    ★ ⇒ ⇒ 少了任一条 ⇒ ⇒ 「每轮重写同一份」就退回成一个观察
//	③ ★★★★★★ 【孤儿表判据必须钉住】—— ★ `pg_inherits` 的父子列名是
//	    ★ `inhrelid` / `inhparent`（★ 不是 `relid` / `parent`）
//	    ★ ⇒ ⇒ 且必须能算出「留存候选集里有没有它」
//	④ ★★★★★★ 【脚本必须只读】
//	⑤ ★★★★ 【runbook §143.94 段必须登记关键数字 + LESSONS §288 / AGENTS R138 在位】
//
// ★ ★★ 纪律提醒：★ 本门是【登记门 + 工具形态门】，★ **不是**端到端门。
// ★ ★★ 判据一律查【可执行面】（★ 剥掉 `--` 注释与 `\echo` 元命令）⇒ ⇒
// ★ ★★ 否则「在注释里写出那行」就会假绿。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	section14394Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14394Lessons = "LESSONS.md"
	section14394Agents  = "AGENTS.md"
	section14394Script  = "scripts/audit/verify_session_turns_column_decomposition.sql"
)

func repoRoot14394(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14394(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14394(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

func sqlNonComment14394(t *testing.T, rel string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14394(t, rel), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func sqlExecutable14394(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range sqlNonComment14394(t, rel) {
		if strings.HasPrefix(line, `\`) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestSection14394ScriptExistsAndHasEightStages（★ 判据①的前置）
func TestSection14394ScriptExistsAndHasEightStages(t *testing.T) {
	n := 0
	for _, line := range sqlNonComment14394(t, section14394Script) {
		if strings.HasPrefix(line, `\echo `) {
			n++
		}
	}
	if n != 8 {
		t.Errorf("★ %s 里有 %d 个（非注释的）`\\echo` 段（期望 8）—— "+
			"★ ★★ 本轮的可复现复算是【8 段】：① 族清单 ② 9 月逐列分解 ③ compression_meta 直方图 "+
			"④ 按 strategy 拆 ⑤ 大对象规模 ⑥ 逐轮重写 ⑦ digest 直方图 ⑧ 10 月采样"+
			"★ ⇒ ⇒ ★★ 少一段 ⇒ ⇒ 那个坑就没有被钉住"+
			"★ ⇒ ⇒ ★★ 且【被注释掉的 `\\echo` 不算一段】",
			section14394Script, n)
	}
}

// TestSection14394ColumnDecompositionIsParallel（★ 判据①）—— ★★ 逐列不许退化的守卫
//
// ★★★★★★ 【两个大 jsonb 列必须【同一条 select 里并列】】—— ★★★
// ★ §143.94 实测：★ `compression_meta` ＝ 569,396,910 B（19.9%）、★ `digest` ＝ 533,160,775 B（18.6%）
// ★ ⇒ ⇒ 两者合计 38.5%；★ 而 `tools` 只有 3,991,390 B ⇒ ⇒ 它【不是】问题。
func TestSection14394ColumnDecompositionIsParallel(t *testing.T) {
	exe := sqlExecutable14394(t, section14394Script)
	for _, needle := range []string{
		// ★ ★★★ 尾部必须带【逗号】⇒ ⇒ 否则改名成 `..._bytes_x` 时仍会命中 ⇒ ⇒ 门没有牙。
		"sum(pg_column_size(compression_meta)) AS compression_meta_bytes,",
		"sum(pg_column_size(digest))           AS digest_bytes,",
		"sum(pg_column_size(tools))            AS tools_bytes,",
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 本轮实测：★ `compression_meta` ＝ 569,396,910 B（19.9%%）、"+
				"★ `digest` ＝ 533,160,775 B（18.6%%）⇒ ⇒ 两者合计 38.5%%"+
				"★ ⇒ ⇒ ★★ 而 `tools` 只有 3,991,390 B（★ 0.14%%）⇒ ⇒ 它【不是】问题"+
				"★ ⇒ ⇒ ★★ 少一列 ⇒ ⇒ 又退回【只挑一列代表整行】（★ §143.92 的同款洞）",
				section14394Script, needle)
		}
	}
}

// TestSection14394CauseRulersArePresent（★ 判据②）—— ★★ 成因判据的守卫
//
// ★★★★★★ 【「每轮重写同一份」必须能被两条尺子复现】—— ★★★
// ★ 尺子①：逐轮体积（★ `lag(...) OVER (PARTITION BY session_id ORDER BY turn_no)`）
// ★ 尺子②：大对象的规模（★ 多少会话、多少行、多少字节）
func TestSection14394CauseRulersArePresent(t *testing.T) {
	exe := sqlExecutable14394(t, section14394Script)
	for _, needle := range []string{
		// ★ 尺子①：逐轮重写
		"lag(pg_column_size(compression_meta)) OVER (PARTITION BY session_id ORDER BY turn_no)",
		// ★ 尺子②：大对象的规模
		// ★ ★★★ 尾部必须带【逗号】⇒ ⇒ 否则改名成 `..._x` 时仍会命中 ⇒ ⇒ 门没有牙。
		"count(*) AS big_sessions,",
		"sum(meta_bytes) AS big_meta_bytes,",
		// ★ 大对象的判据（★ 阈值 8192）—— ★ 尾部带【换行】⇒ ⇒ 改名即逃
		"pg_column_size(compression_meta) > 8192\n",
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 本轮的机制结论是【每轮被整体重写】：★ 同一会话连续 30 轮、"+
				"★ 每轮 `compression_meta` 都是约 23,000 B（★ 波动仅几十字节）"+
				"★ ⇒ ⇒ ★★ 大对象只属于 260 个会话 / 22,003 行 / 483,696,246 B"+
				"★ ⇒ ⇒ ★★ 少了尺子 ⇒ ⇒ 「每轮重写同一份」就退回成一个观察",
				section14394Script, needle)
		}
	}
}

// TestSection14394OrphanTableRulerIsPresent（★ 判据③）—— ★★ 孤儿表判据的守卫
//
// ★★★★★★ 【孤儿表判定必须用【正确的父子列名】+【必须能算留存候选集】】—— ★★★
// ★ `pg_inherits` 的列是 `inhrelid` / `inhparent`（★ 不是 `relid` / `parent`）
func TestSection14394OrphanTableRulerIsPresent(t *testing.T) {
	exe := sqlExecutable14394(t, section14394Script)
	for _, needle := range []string{
		"LEFT JOIN pg_inherits i ON i.inhrelid = c.oid",
		// ★ ★★★ 字面量尾部必须带【换行】⇒ ⇒ 否则改名成 `...-x` 时仍会命中 ⇒ ⇒ 门没有牙。
		"'ORPHAN-not-under-parent'\n",
		"'parent-table'\n",
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 实测 `session_turns_hot` 是【孤儿表】（★ 167 MB / 8,837 行 / 跨度 10-22 23:46 → 10-23 10:49）"+
				"★ ⇒ ⇒ ★★ 它是【当月热分区】、★ 不是垃圾；但它【不在父表下】"+
				"★ ⇒ ⇒ ★★ ⇒ 【按父表过滤的清理机制看不到它】"+
				"★ ⇒ ⇒ ★★ 这条判据必须能复算（★ 列名 / 结论字面量都要钉住）",
				section14394Script, needle)
		}
	}
	// ★ ★★ 反向禁：不得出现错误的父子列名。
	for _, bad := range []string{"i.relid", "i.parent", "pg_inherits i ON i.relid"} {
		if strings.Contains(exe, bad) {
			t.Errorf("★ %s 的【可执行面】里出现了 `%s` —— "+
				"★ ★★★★ `pg_inherits` 的列名是【`inhrelid` / `inhparent`】"+
				"★ ⇒ ⇒ ★★ 写成 `relid` / `parent` 会直接 42703 报错"+
				"★ ⇒ ⇒ ★★ 这类错列名在别处已踩过（★ `pg_class` 没有 `relid`）",
				section14394Script, bad)
		}
	}
}

// TestSection14394ScriptIsReadOnly（★ 判据④）
//
// ★★★★★★ 【脚本必须只读】—— ★ 不许出现写 / DDL 关键字。
var forbidden14394 = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|truncate|vacuum|analyze|reindex|cluster|refresh|merge|grant|revoke|copy|create|call|do)\b`)

func TestSection14394ScriptIsReadOnly(t *testing.T) {
	exe := sqlExecutable14394(t, section14394Script)
	for i, line := range strings.Split(exe, "\n") {
		if forbidden14394.MatchString(line) {
			t.Errorf("★ %s 的可执行面第 %d 行出现了写 / DDL 关键字：%q —— "+
				"★ ★★★★ 本脚本是【只读复算】⇒ ⇒ 生产上重复跑它必须绝对安全"+
				"★ ⇒ ⇒ ★★ 只允许 SELECT 与 `SET statement_timeout`",
				section14394Script, i+1, line)
		}
	}
	for _, line := range strings.Split(exe, "\n") {
		if strings.HasPrefix(strings.ToUpper(line), "SET ") &&
			!strings.HasPrefix(strings.ToUpper(line), "SET STATEMENT_TIMEOUT") {
			t.Errorf("★ %s 的可执行面出现了非 `SET statement_timeout` 的 SET：%q —— "+
				"★ ★★ 只读脚本的 SET 面要收敛", section14394Script, line)
		}
	}
}

// TestSection14394RunbookRegistersNumbers（★ 判据⑤）
func TestSection14394RunbookRegistersNumbers(t *testing.T) {
	runbook := readRepoFile14394(t, section14394Runbook)
	i := strings.Index(runbook, "### §143.94")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.94 段 —— ★ 本轮的逐列分解必须落在仓内（★ 过程可查）")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"569,396,910",       // ★ compression_meta 的体积
		"533,160,775",       // ★ digest 的体积
		"2,867,789,824",     // ★ 9 月分区总字节
		"483,696,246",       // ★ 大对象的字节
		"22,003",            // ★ 大对象的行数
		"260",               // ★ 大对象的会话数
		"25,830",            // ★ 10 月单行平均
		"6,173",             // ★ 9 月单行平均
		"session_turns_hot", // ★ 孤儿热分区
		"maxRecords = 256",  // ★ 成因的代码锚点
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.94 段里没有 %q —— "+
				"★ ★★ 本轮的读数是【数字级】的 ⇒ ⇒ 新数字必须登记"+
				"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 「4,446 MB」这个笼统数字就会继续被引用"+
				"★ ⇒ ⇒ ★★ 而【它是什么】只有逐列才答得出来",
				needle)
		}
	}
}

// TestSection14394LessonsAndAgentsAreRegistered（★ 判据⑤的配套）
func TestSection14394LessonsAndAgentsAreRegistered(t *testing.T) {
	if !strings.Contains(readRepoFile14394(t, section14394Lessons), "## §288") {
		t.Error("★ LESSONS.md 里没有 §288 —— " +
			"★ ⇒ ⇒ 「两个大列的成因可以完全不同」与「读不出来本身就是结论」" +
			"★ ⇒ ⇒ 这两条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14394(t, section14394Agents), "R138 轮") {
		t.Error("★ AGENTS.md 里没有 R138 轮 —— " +
			"★ ⇒ ⇒ 本轮「逐列拆解 + 逐列定性」必须落在交接段")
	}
}
