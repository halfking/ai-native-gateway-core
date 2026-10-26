//go:build !integration

package main

// §143.93 的门：★ 本轮是【ursm 快照族存储审计轮】⇒ ⇒ 产出是
// 【双写的机制定位】+【孤儿表的定位】+【一个零迁移修法的证据链】。
//
// ★★★★★★ 这个门要守的是【这三件事不许被无声改掉】：
//
//	① ★★★★★★ 【双写的机制判据必须钉住】—— ★ 脚本必须能复现：
//	    ★ 批间隔分布（★ 相位错开 ⇒ 间隔是两个小间隔而非 60）
//	    ★ 内容级指纹对照（★ 相邻两批 IDENTICAL）
//	    ★ 每小时批次数（★ 120 = 2 实例 × 60 tick）
//	② ★★★★★★ 【孤儿表判据必须钉住】—— ★ `pg_inherits` 的父子列名是
//	    ★ `inhrelid` / `inhparent`（★ 不是 `relid` / `parent`）
//	    ★ ⇒ 且必须能算出「留存候选集里有没有它」
//	③ ★★★★★★ 【零迁移修法的开关必须在仓内】—— ★ `URSM_V2_SHADOW_PERSIST`
//	    ★ ⇒ ⇒ 显式负值语义（★ 不设 = 保持现状）⇒ ⇒ 改它前必须先读到这条
//	④ ★★★★★★ 【脚本必须只读】
//	⑤ ★★★★ 【runbook §143.93 段必须登记关键数字 + LESSONS §287 + AGENTS R137 在位】
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
	section14393Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14393Lessons = "LESSONS.md"
	section14393Agents  = "AGENTS.md"
	section14393Script  = "scripts/audit/verify_ursm_snapshot_dualwrite.sql"
)

func repoRoot14393(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14393(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14393(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

func sqlNonComment14393(t *testing.T, rel string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14393(t, rel), "\n") {
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

func sqlExecutable14393(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range sqlNonComment14393(t, rel) {
		if strings.HasPrefix(line, `\`) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestSection14393ScriptExistsAndHasEightStages（★ 判据①的前置）
func TestSection14393ScriptExistsAndHasEightStages(t *testing.T) {
	n := 0
	for _, line := range sqlNonComment14393(t, section14393Script) {
		if strings.HasPrefix(line, `\echo `) {
			n++
		}
	}
	if n != 8 {
		t.Errorf("★ %s 里有 %d 个（非注释的）`\\echo` 段（期望 8）—— "+
			"★ ★★ 本轮的可复现复算是【8 段】：① 族清单+留存判据 ② 存储账 ③ 批间隔分布 "+
			"④ 内容级指纹对照 ⑤ 每小时批次数 ⑥ legacy 时间跨度 ⑦ 每批租户分布 ⑧ 父表下分区数"+
			"★ ⇒ ⇒ ★★ 少一段 ⇒ ⇒ 那个坑就没有被钉住"+
			"★ ⇒ ⇒ ★★ 且【被注释掉的 `\\echo` 不算一段】",
			section14393Script, n)
	}
}

// TestSection14393DualWriteRulersArePresent（★ 判据①）—— ★★ 双写机制判据的守卫
//
// ★★★★★★ 【双写必须能被三条尺子同时复现】—— ★★★
// ★ 本轮实测（★ 252 共享库，★ 2026-10-23）：★ 相邻批间隔 7 / 53 / 60 秒
// ★ ⇒ ⇒ 两实例 60s tick 相位错开；★ 内容指纹 IDENTICAL；★ 每小时 120 批。
func TestSection14393DualWriteRulersArePresent(t *testing.T) {
	exe := sqlExecutable14393(t, section14393Script)
	for _, needle := range []string{
		// ★ 尺子①：批间隔分布
		// ★ ★★★ 尾部必须带【逗号】⇒ ⇒ 否则改名成 `gap_seconds_x` 时仍会命中 ⇒ ⇒ 门没有牙。
		"EXTRACT(EPOCH FROM (snapshot_ts - prev_ts))::int AS gap_seconds,",
		// ★ 尺子②：内容级指纹（★ 逐条拼 credential/model/tenant/fail_streak/available/health）
		// ★ ★★★ 必须钉【行内唯一片段】⇒ ⇒ 因为这个表达式在 SQL 里【跨行】、
		// ★ ★★ 而判据查的是【逐行 trim 后重新拼接】的可执行面 ⇒ ⇒ 跨行 needle 永远打不中。
		// ★ ★★ 同时尾部要带【下一段拼接】⇒ ⇒ 否则改名成 `tenant_id_x` 时仍会命中 ⇒ ⇒ 门没有牙。
		"|| tenant_id || '|' ||",
		"'IDENTICAL' ELSE 'DIFFERENT' END AS content_cmp",
		// ★ 尺子③：每小时批次数
		// ★ ★★★ 尾部必须带【逗号+换行】⇒ ⇒ 否则改名成 `batches_x` 时仍会命中 ⇒ ⇒ 门没有牙。
		"count(DISTINCT snapshot_ts) AS batches,\n",
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 本轮的机制结论是【双写】：★ 两实例 60s tick 相位错开 ⇒ 互斥锁不冲突"+
				"★ ⇒ ⇒ ★★ 三条尺子（★ 批间隔 / 内容指纹 / 批次数）必须【都能复算】"+
				"★ ⇒ ⇒ ★★ 少一条 ⇒ ⇒ 「双写」就退回成一个观察、而不是一个可复现的机制",
				section14393Script, needle)
		}
	}
}

// TestSection14393OrphanTableRulerIsPresent（★ 判据②）—— ★★ 孤儿表判据的守卫
//
// ★★★★★★ 【孤儿表判定必须用【正确的父子列名】+【必须能算留存候选集】】—— ★★★
// ★ `pg_inherits` 的列是 `inhrelid` / `inhparent`（★ 不是 `relid` / `parent`）
// ★ ⇒ ⇒ DROP 型留存的候选集 = `i.inhparent = 'public.ursm_node_snapshot_min'::regclass`
// ★ ⇒ ⇒ ★★ 【不在这个集合里的表 ⇒ 永远进不了候选集 ⇒ 永远不会被 DROP】
func TestSection14393OrphanTableRulerIsPresent(t *testing.T) {
	exe := sqlExecutable14393(t, section14393Script)
	for _, needle := range []string{
		"LEFT JOIN pg_inherits i ON i.inhrelid = c.oid",           // ★ 子表列
		"i.inhparent = 'public.ursm_node_snapshot_min'::regclass", // ★ 父表列
		"'_([0-9]{8})$'", // ★ 分区名契约（★ legacy 匹配不到）
		"NO_DAY_END-never-dropped-by-partition-retention", // ★ 孤儿结论的字面量
	} {
		if !strings.Contains(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 实测 `ursm_node_snapshot_min_legacy` 是【孤儿表】（★ 不在父表下）"+
				"★ ⇒ ⇒ ★★ 852 MB 永远进不了 DROP 型留存的候选集 ⇒ ⇒ 永久滞留"+
				"★ ⇒ ⇒ ★★ 这条判据必须能复算（★ 列名 / 正则 / 结论字面量都要钉住）",
				section14393Script, needle)
		}
	}
	// ★ ★★ 反向禁：不得出现错误的父子列名（★ `relid` / `parent` 在 pg_inherits 里不存在）。
	for _, bad := range []string{"i.relid", "i.parent", "pg_inherits i ON i.relid"} {
		if strings.Contains(exe, bad) {
			t.Errorf("★ %s 的【可执行面】里出现了 `%s` —— "+
				"★ ★★★★ `pg_inherits` 的列名是【`inhrelid` / `inhparent`】"+
				"★ ⇒ ⇒ ★★ 写成 `relid` / `parent` 会直接 42703 报错"+
				"★ ⇒ ⇒ ★★ 这类错列名在别处已踩过（★ `pg_class` 没有 `relid`）",
				section14393Script, bad)
		}
	}
}

// TestSection14393ZeroMigrationSwitchIsRegistered（★ 判据③）—— ★★ 零迁移修法的守卫
//
// ★★★★★★ 【零迁移修法的开关必须在仓内、且【显式负值语义】必须被钉住】—— ★★★
// ★ 修法 = 给 245（★ `URSM_V2_MODE=shadow`）设 `URSM_V2_SHADOW_PERSIST=0`
// ★ ⇒ ⇒ 154（★ authoritative ⇒ `ResolvePersistEnabled()` 恒 true）继续写
// ★ ⇒ ⇒ ★★ 双写变单写 ⇒ ⇒ 零迁移、零代码变更、只需一行 env + 一次重启
func TestSection14393ZeroMigrationSwitchIsRegistered(t *testing.T) {
	cfg := readRepoFile14393(t, "domains/ursm/v2/config.go")
	// ★ ★★★ 必须钉【带边界的完整形态】⇒ ⇒ 因为裸子串「URSM_V2_SHADOW_PERSIST」
	// ★ ★★ 会被改名成「URSM_V2_SHADOW_PERSIST_X」逃过 ⇒ ⇒ 门没有牙（★ 变异 M9 的实测）。
	for _, needle := range []string{
		`os.Getenv("URSM_V2_SHADOW_PERSIST")`,
		`c.ShadowPersist = !(v == "0" || v == "false" || v == "no")`,
	} {
		if !strings.Contains(cfg, needle) {
			t.Errorf("★ domains/ursm/v2/config.go 里没有逐字的 `%s` —— "+
				"★ ⇒ ⇒ 「只停 shadow 实例的快照写、保留 sidecar 漂移证据」这个零迁移修法"+
				"★ ⇒ ⇒ 必须有一个可用的开关；★ 它不在 ⇒ ⇒ 修法不存在"+
				"★ ⇒ ⇒ ★★ 且必须钉【带边界的完整形态】（★ 裸子串会被改名逃过）",
				needle)
		}
	}
	// ★ ★★ 显式负值语义：★ 不设 = 保持现状（★ ⇒ ⇒ 改它【不会】意外改变未设它的部署）。
	if !strings.Contains(cfg, "c.ShadowPersistSet = true") {
		t.Error("★ domains/ursm/v2/config.go 里没有 `c.ShadowPersistSet = true` —— " +
			"★ ⇒ ⇒ 「false 既是『运维说不要』又是『运维什么都没说』」必须被区分开" +
			"★ ⇒ ⇒ ★★ 否则这个开关【不是】显式负值 ⇒ ⇒ 给存量部署塞上它就会改变行为")
	}
	// ★ ★★ 回落路径必须还在（★ 兼容既有部署）。
	if !strings.Contains(cfg, "return c.ShadowDoubleWrite") {
		t.Error("★ domains/ursm/v2/config.go 里没有 `return c.ShadowDoubleWrite` —— " +
			"★ ⇒ ⇒ ★★ 「未设该变量时 persist 跟随 ShadowDoubleWrite」这条【向后兼容】路径必须还在")
	}
}

// TestSection14393ScriptIsReadOnly（★ 判据④）
//
// ★★★★★★ 【脚本必须只读】—— ★ 不许出现写 / DDL 关键字。
var forbidden14393 = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|truncate|vacuum|analyze|reindex|cluster|refresh|merge|grant|revoke|copy|create|call|do)\b`)

func TestSection14393ScriptIsReadOnly(t *testing.T) {
	exe := sqlExecutable14393(t, section14393Script)
	for i, line := range strings.Split(exe, "\n") {
		if forbidden14393.MatchString(line) {
			t.Errorf("★ %s 的可执行面第 %d 行出现了写 / DDL 关键字：%q —— "+
				"★ ★★★★ 本脚本是【只读复算】⇒ ⇒ 生产上重复跑它必须绝对安全"+
				"★ ⇒ ⇒ ★★ 只允许 SELECT 与 `SET statement_timeout`",
				section14393Script, i+1, line)
		}
	}
	for _, line := range strings.Split(exe, "\n") {
		if strings.HasPrefix(strings.ToUpper(line), "SET ") &&
			!strings.HasPrefix(strings.ToUpper(line), "SET STATEMENT_TIMEOUT") {
			t.Errorf("★ %s 的可执行面出现了非 `SET statement_timeout` 的 SET：%q —— "+
				"★ ★★ 只读脚本的 SET 面要收敛", section14393Script, line)
		}
	}
}

// TestSection14393RunbookRegistersNumbers（★ 判据⑤）
func TestSection14393RunbookRegistersNumbers(t *testing.T) {
	runbook := readRepoFile14393(t, section14393Runbook)
	i := strings.Index(runbook, "### §143.93")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.93 段 —— ★ 本轮的机制定位必须落在仓内（★ 过程可查）")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"9,852",                  // ★ 族总 MB
		"852",                    // ★ legacy 孤儿表 MB
		"2,836,620",              // ★ legacy 行数
		"120",                    // ★ 每小时批次数（★ 2 实例 × 60 tick）
		"1,152",                  // ★ 今天每批行数
		"IDENTICAL",              // ★ 内容级对照结论
		"URSM_V2_SHADOW_PERSIST", // ★ 零迁移修法的开关
		"2,836,620",
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.93 段里没有 %q —— "+
				"★ ★★ 本轮的读数是【数字级】的 ⇒ ⇒ 新数字必须登记"+
				"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 旧的「12 GB / 37.4%%」就会继续被引用（★ 而它已被本轮复算更新）",
				needle)
		}
	}
}

// TestSection14393LessonsAndAgentsAreRegistered（★ 判据⑤的配套）
func TestSection14393LessonsAndAgentsAreRegistered(t *testing.T) {
	if !strings.Contains(readRepoFile14393(t, section14393Lessons), "## §287") {
		t.Error("★ LESSONS.md 里没有 §287 —— " +
			"★ ⇒ ⇒ 「互斥锁不是去重锁」与「孤儿表永远进不了 DROP 型留存的候选集」" +
			"★ ⇒ ⇒ 这两条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14393(t, section14393Agents), "R137 轮") {
		t.Error("★ AGENTS.md 里没有 R137 轮 —— " +
			"★ ⇒ ⇒ 本轮「双写的机制定位 + 零迁移修法」必须落在交接段")
	}
}
