//go:build !integration

package main

// §143.91 的门：★ 本轮是【存储热点审计轮】⇒ ⇒ 产出是【一份三口径并列的存储全景】+【一个被证伪的候选】+【两个量具级陷阱】。
// ★★★★★★ 这个门要守的是【盘点这件事本身不许退化成单一口径】：
//
//	① ★★★★ 审计脚本存在，且【7 段】齐全
//	② ★★★★★★ 【三口径必须并列】—— ★ 表级体积 + 实际行数 + reltuples 估算
//	    ★ ★★ 这是本轮的第一个量具陷阱：`pg_stat_user_tables.n_live_tup` 是【采集快照】、★ 会过期
//	    ★ ★★ ⇒ ⇒ 「= 0」≠ 空表 ⇒ ⇒ 「空表占 600~850 MB」的候选形态被作废
//	③ ★★★★★★ 【三张「n_live_tup = 0」的表必须逐条独立 count】—— ★ 不许只留 reltuples
//	④ ★★★★★★ 【体积必须两口径并列】—— ★ 存储体积 + `length(::text)` 内容体积
//	    ★ ★★ `pg_column_size` 给的是【存储体积】（★ 压缩后）⇒ ⇒ 恒为下界
//	⑤ ★★★★★★ 【脚本必须只读】—— ★ 可执行面不许出现写 / DDL 关键字
//	⑥ ★★★★★★ 【runbook §143.91 段必须登记关键数字】
//	⑦ ★★★★★★ 【§143.90 的订正横幅七不许被删】（★ 原文保留 + 就地标注）
//	⑧ ★★★★ 【LESSONS §285 + AGENTS R135 必须在位】
//
// ★ ★★ 纪律提醒：★ 本门是【登记门 + 工具形态门】，★ **不是**端到端门。
// ★ ★★ 判据一律查【可执行面】（★ 剥掉 `--` 注释与 `\echo` 元命令）⇒ ⇒
// ★ ★★ 否则「在注释里写出那行」就会假绿（★ 变异 M8 专测这一条）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	section14391Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14391Lessons = "LESSONS.md"
	section14391Agents  = "AGENTS.md"
	section14391Script  = "scripts/audit/storage_hotspots.sql"
)

func repoRoot14391(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14391(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14391(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// sqlNonComment14391 返回去掉【整行 `--` 注释】后的行（★ 保留 `\echo` 元命令）。
//
// ★ ★★ 为什么要它：★ 判据① 数的是【段数】⇒ ⇒ 而「被注释掉的 `\echo`」不该算一段
// ★ ★★ （★ 变异 M11 的实测：★ 首版判据数【原文里的 `\echo`】⇒ ⇒ 注释掉仍然绿
// ★ ★★ ⇒ ⇒ ⇒ 这是本门自己的一个洞 ⇒ ⇒ 修法是数【非注释行里的 `\echo`】）。
func sqlNonComment14391(t *testing.T, rel string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14391(t, rel), "\n") {
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

// sqlExecutable14391 返回 SQL 文件的【可执行面】。
//
// ★ ★★ 为什么要剥：★ 本轮的判据（★ 尤其是③⑤）查的是【真正会被 PG 执行的那部分】⇒ ⇒
// ★ ★★ 若不剥注释，★ 「在 `--` 注释里写出 count(*) / DELETE」就会假绿。
// ★ ★★ 同时剥掉 `\echo` 元命令（★ psql 指令、★ 不是 SQL）。
func sqlExecutable14391(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range sqlNonComment14391(t, rel) {
		if strings.HasPrefix(line, `\`) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestSection14391ScriptExistsAndHasSevenStages（★ 判据①）
func TestSection14391ScriptExistsAndHasSevenStages(t *testing.T) {
	n := 0
	for _, line := range sqlNonComment14391(t, section14391Script) {
		if strings.HasPrefix(line, `\echo `) {
			n++
		}
	}
	if n != 7 {
		t.Errorf("★ %s 里有 %d 个（非注释的）`\\echo` 段（期望 7）—— "+
			"★ ★★ 本轮的可复现审计是【7 段】：① 表级 top 15 ② reltuples 对账 ③ 三张「n_live_tup=0」的独立 count "+
			"④ 重点表 relkind/reltuples ⑤ 正文族按 kind 拆解 ⑥ final_full 分布 ⑦ 长驻会话画像"+
			"★ ⇒ ⇒ ★★ 少一段 ⇒ ⇒ 那个坑就没有被钉住"+
			"★ ⇒ ⇒ ★★ 且【被注释掉的 `\\echo` 不算一段】（★ 变异 M11 的实测）",
			section14391Script, n)
	}
}

// TestSection14391ThreeMeasuresAreParallel（★ 判据②）—— ★★ 本轮的第一个量具陷阱的守卫
//
// ★★★★★★ 【存储类结论必须三口径并列：★ 表级体积 + 实际行数 + reltuples 估算】—— ★★★
// ★ §143.91.3 实测：★ 三张表的 `n_live_tup = 0`，★ 而独立 `count(*)` 分别是
// ★ 786,036 / 1,416,772 / 2,836,620 ⇒ ⇒ 「= 0」≠ 空表。
func TestSection14391ThreeMeasuresAreParallel(t *testing.T) {
	exe := sqlExecutable14391(t, section14391Script)
	for _, needle := range []string{
		"n_live_tup",             // ★ 口径①：采集快照（★ 会过期）
		"pg_total_relation_size", // ★ 口径②：表级体积
		"reltuples",              // ★ 口径③：估算行数（★ 比 n_live_tup 可靠）
		"count(*)",               // ★ 口径④：实际行数（★ 唯一不骗人的）
		"pg_class",               // ★ reltuples 的来源表
		"pg_stat_user_tables",    // ★ n_live_tup 的来源表
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有 %q —— "+
				"★ ★★★★ 存储类结论必须三口径并列（★ 表级体积 + 实际行数 + reltuples 估算）"+
				"★ ⇒ ⇒ ★★ `n_live_tup` 是【采集快照】会过期 ⇒ ⇒ 「= 0」≠ 空表"+
				"★ ⇒ ⇒ ★★ 少了任一口径 ⇒ ⇒ 「空表占 600~850 MB」的候选就会复活",
				section14391Script, needle)
		}
	}
}

// TestSection14391ZeroLiveTupTablesAreCountedIndependently（★ 判据③）
//
// ★★★★★★ 【三张「n_live_tup = 0」的表必须【逐条独立 count】】—— ★★★
// ★ ★★ 逐条（★ 不写 UNION ALL）⇒ ⇒ 大表 count 放在同一分支里会互相拖累。
func TestSection14391ZeroLiveTupTablesAreCountedIndependently(t *testing.T) {
	exe := sqlExecutable14391(t, section14391Script)
	for _, tbl := range []string{
		"public.session_turn_details_2026_09",
		"public.usage_ledger_2026_09",
		"public.ursm_node_snapshot_min_legacy",
	} {
		needle := "count(*) AS real_rows FROM " + tbl
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 本轮实测这三张表的 `n_live_tup` 全是 0，★ 而实际行数是 786,036 / 1,416,772 / 2,836,620"+
				"★ ⇒ ⇒ ★★ 少了独立 count ⇒ ⇒ 「空表」这个读法就会回来",
				section14391Script, needle)
		}
	}
}

// TestSection14391VolumeHasTwoMeasures（★ 判据④）—— ★★ 第二个量具陷阱的守卫
//
// ★★★★★★ 【体积必须两口径并列：★ 存储体积 + `length(::text)` 内容体积】—— ★★★
// ★ §143.91.4 本地实测：★ jsonb 内容 128,894 B ⇒ `pg_column_size` = 67,766（★ 压缩后）
// ★ ⇒ ⇒ ⇒ `pg_column_size` 【恒为内容体积的下界】⇒ ⇒ 只报它就是低报。
func TestSection14391VolumeHasTwoMeasures(t *testing.T) {
	exe := sqlExecutable14391(t, section14391Script)
	if !strings.Contains(exe, "length(outbound_body::text)") {
		t.Errorf("★ %s 的【可执行面】里没有逐字的 `length(outbound_body::text)` —— "+
			"★ ★★★★ `pg_column_size` 给的是【存储体积】（★ 压缩后）⇒ ⇒ 【恒为下界】"+
			"★ ⇒ ⇒ ★★ 只报存储体积 ⇒ ⇒ 就是低报内容体积"+
			"★ ⇒ ⇒ ★★ 必须两口径并列（★ 且【方向】与【倍数】要分开说）",
			section14391Script)
	}
	if !strings.Contains(exe, "pg_column_size(outbound_body)") {
		t.Errorf("★ %s 的【可执行面】里没有 `pg_column_size(outbound_body)` —— "+
			"★ ★★ 两口径是【并列】的：★ 存储体积 + 内容体积 ⇒ ⇒ 少一个就不叫并列",
			section14391Script)
	}
}

// TestSection14391ScriptIsReadOnly（★ 判据⑤）
//
// ★★★★★★ 【脚本必须只读】—— ★ 不许出现写 / DDL 关键字。
var forbidden14391 = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|truncate|vacuum|analyze|reindex|cluster|refresh|merge|grant|revoke|copy|create|call|do)\b`)

func TestSection14391ScriptIsReadOnly(t *testing.T) {
	exe := sqlExecutable14391(t, section14391Script)
	for i, line := range strings.Split(exe, "\n") {
		if forbidden14391.MatchString(line) {
			t.Errorf("★ %s 的可执行面第 %d 行出现了写 / DDL 关键字：%q —— "+
				"★ ★★★★ 本脚本是【只读审计】⇒ ⇒ 生产上重复跑它必须绝对安全"+
				"★ ⇒ ⇒ ★★ 只允许 SELECT 与 `SET statement_timeout`",
				section14391Script, i+1, line)
		}
	}
	// ★ SET 只允许 statement_timeout（★ 其它 SET 会改会话行为）。
	for _, line := range strings.Split(exe, "\n") {
		if strings.HasPrefix(strings.ToUpper(line), "SET ") &&
			!strings.HasPrefix(strings.ToUpper(line), "SET STATEMENT_TIMEOUT") {
			t.Errorf("★ %s 的可执行面出现了非 `SET statement_timeout` 的 SET：%q —— "+
				"★ ★★ 只读脚本的 SET 面要收敛", section14391Script, line)
		}
	}
}

// TestSection14391RunbookRegistersNumbers（★ 判据⑥）
func TestSection14391RunbookRegistersNumbers(t *testing.T) {
	runbook := readRepoFile14391(t, section14391Runbook)
	i := strings.Index(runbook, "### §143.91")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.91 段 —— ★ 本轮的存储全景必须落在仓内（★ 过程可查）")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"5,228",     // ★ session_bodies_2026_10 的 MB
		"11,117",    // ★ 会话族合计 MB
		"9,477",     // ★ ursm 快照族合计 MB
		"2,836,620", // ★ legacy 的实际行数（★ 不是 0）
		"7,507",     // ★ final_full 行数
		"294,934",   // ★ turn_delta 行数
		"1.33%",     // ★ final_full 的占比上界
		"519,943",   // ★ 长驻会话的内容体积
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.91 段里没有 %q —— "+
				"★ ★★ 本轮的读数是【数字级】的 ⇒ ⇒ 新数字必须登记"+
				"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 旧的存储结论就会继续被引用",
				needle)
		}
	}
}

// TestSection14391CorrectionBannerIsRegistered（★ 判据⑦）
//
// ★ ★★ 订正必须【就地留在被订正的段上】⇒ ⇒ 否则读旧段的人看不到它已被改。
func TestSection14391CorrectionBannerIsRegistered(t *testing.T) {
	runbook := readRepoFile14391(t, section14391Runbook)
	i := strings.Index(runbook, "### §143.90")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.90 段")
	}
	b := strings.Index(runbook[i+1:], "\n### ")
	if b < 0 {
		b = len(runbook) - i
	}
	part := runbook[i : i+b]
	if !strings.Contains(part, "订正横幅七（2026-10-23 §143.91 轮）") {
		t.Errorf("★ %s 段里没有「订正横幅七（2026-10-23 §143.91 轮）」 —— "+
			"★ ★★ §143.90.4 把长驻会话登记为「上下文体积的大头」⇒ ⇒ §143.91 实测它只有 508 KiB"+
			"★ ⇒ ⇒ ★★ 订正必须【就地挂在被订正的段上】（★ 原文保留）",
			"§143.90")
	}
	// ★★ 横幅必须真的落在 §143.90 段【之内】（★ 不许只是全文里有这个串）。
	if !strings.Contains(part, "519,943") {
		t.Errorf("★ %s 段的订正横幅七里没有 519,943 —— "+
			"★ ★★ 横幅必须自带【订正后的数】⇒ ⇒ 否则读者看不到结论改成了什么",
			"§143.90")
	}
}

// TestSection14391LessonsAndAgentsAreRegistered（★ 判据⑧）
func TestSection14391LessonsAndAgentsAreRegistered(t *testing.T) {
	if !strings.Contains(readRepoFile14391(t, section14391Lessons), "## §285") {
		t.Error("★ LESSONS.md 里没有 §285 —— " +
			"★ ⇒ ⇒ 「存储类结论必须三口径并列」这条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14391(t, section14391Agents), "R135 轮") {
		t.Error("★ AGENTS.md 里没有 R135 轮 —— " +
			"★ ⇒ ⇒ 本轮「阴性结论也要当缺陷一样复核」必须落在交接段")
	}
}
