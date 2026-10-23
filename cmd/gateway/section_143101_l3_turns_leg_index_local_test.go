//go:build !integration

package main

// ═══════════════════════════════════════════════════════════════════════
// §143.101 「加 (session_id, ts DESC) 索引」修法的【否证】门
//
// ★ ★ 为什么要有这道门：
//
//   §143.100 的结论是「读侧长尾 = planner 被排序键带偏 ⇒ ⇒ 缺一条
//   `(session_id, ts DESC)` ⇒ ⇒ 加这条索引（零语义变更）就是修法」。
//   ⇒ ⇒ 但那是**推算**（诚实边界③）。
//   ⇒ ⇒ §143.101 在**本地一次性库**上把它验了 ⇒ ⇒ ★★ **结果：计划【一行都没变】**：
//
//	阶段 1（无该索引）：Index Scan Backward using …_heap_tenant_id_ts_idx
//	                    Index Cond: (tenant_id = 'default')
//	                    Filter: (session_id = 'sys:probe:cred35:20260923')
//	阶段 2（★ 建了该索引）：★ **完全相同的计划、完全相同的 cost**
//
//   ⇒ ⇒ ★★ 为什么：★ 那两条路径要【碰的行数本来就一样】
//   （★ 生产实测：tenant_id 路径 42,289 行 vs session_id 路径 40,381 行，★ 只差 4.5%）
//   ⇒ ⇒ ⇒ planner 在生产上【同样没有理由换路】⇒ ⇒ ⇒ **㉔ 不是修法。**
//
//   ⇒ ⇒ ★★ 于是真正剩下两把钥匙：
//   ① 「换『最新』的定义」（= 改排序键为 turn_no DESC ⇒ 实测快 4,762×，★ 但语义变更）
//   ② 「不让这类 session 走到这条路径」（= 探测型 session 短路 ⇒ ★ 不改语义）
//
// ★ ★★ 这道门要守的是【一个已经被否证的结论不被偷偷复活】：
//   ★ 谁看到「加索引」这一招就以为修好了、于是提交了那条迁移 ⇒ ⇒ 门要红。
// ═══════════════════════════════════════════════════════════════════════

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	section143101LocalSQL = "scripts/local/143101_l3_turns_leg_index_local_experiment.sql"
	section143101MigrateD = "sql/migrations/startup"
	section143101Runbook  = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section143101Lessons  = "LESSONS.md"
	section143101Agents   = "AGENTS.md"
)

func repoRoot143101(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile143101(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot143101(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ①：本地实验脚本必须在场，且必须【自带结论自检】
// ═══════════════════════════════════════════════════════════════════════

func TestSection143101_LocalExperimentPresent(t *testing.T) {
	raw := readRepoFile143101(t, section143101LocalSQL)

	// ★ ★ 它【会写库】⇒ ⇒ 必须有醒目的「只能在本地一次性库跑」标记
	//   ⇒ ⇒ 否则有人会把它拿到生产上跑。
	for _, needle := range []string{
		"只能在本地一次性库跑",
		"绝对不要在生产上跑",
	} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 本地实验脚本缺警告 `%s` —— ★ 它会建表/建索引/插数据 ⇒ ⇒ 必须一眼看出「只能在本地跑」", needle)
		}
	}

	// ★ 五个阶段一个都不能少（★ 阶段 1 = 基线、阶段 2 = ★★ 否证的核心）。
	for _, needle := range []string{
		"阶段 0", "阶段 1", "阶段 2", "阶段 3", "阶段 4", "阶段 5",
	} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 本地实验脚本缺 `%s` —— ★ 少一段就少一环证据", needle)
		}
	}

	// ★★★ 【结论自检】必须在场：★ 三条断言是本脚本的产出。
	for _, needle := range []string{
		"plan_unchanged_after_new_index = t",
		"adding_limit_does_not_change_plan = t",
	} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 本地实验脚本缺结论自检 `%s` —— ★ 否证的结论必须能被门钉住", needle)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ②：★★ 「㉔ 加索引」这个修法【已被否证】⇒ ⇒ 必须已登记
//
//	★ 若仓内已有人加了那条索引 ⇒ ⇒ 必须登记「本地实测计划不变」
//	★（★ 否则就等于把一个【被否证】的修法当成了修好的事实）。
// ═══════════════════════════════════════════════════════════════════════

func TestSection143101_GapFixClaimRegistered(t *testing.T) {
	// ★ ★ 与 §143.100 同款精确正则（★ 第一版「关键词共现」恒真 ⇒ ⇒ 已修）。
	idxGapRE := regexp.MustCompile(
		`(?is)CREATE\s+(?:UNIQUE\s+)?INDEX\s+[^;]*?ON\s+(?:ONLY\s+)?public\.session_turns\b` +
			`[^;]*?USING\s+btree\s*\(\s*session_id\s*,\s*ts\s+DESC`)
	hasGapFix := false
	migDir := filepath.Join(repoRoot143101(t), section143101MigrateD)
	entries, err := os.ReadDir(migDir)
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", section143101MigrateD, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body := readRepoFile143101(t, filepath.Join(section143101MigrateD, e.Name()))
		if idxGapRE.MatchString(body) {
			hasGapFix = true
			break
		}
	}

	runbook := readRepoFile143101(t, section143101Runbook)

	if !strings.Contains(runbook, "§143.101") {
		t.Errorf("★ runbook 里没有 `§143.101` —— ★ 「加索引改不了计划」这个否证必须登记在册")
	}
	// ★ 两种状态都必须带可复算的数字。
	for _, needle := range []string{"42,289", "40,381", "4,762"} {
		if !strings.Contains(runbook, needle) {
			t.Errorf("★ runbook 缺 `%s` —— ★ 否证登记必须带可复算的数字", needle)
		}
	}
	if hasGapFix {
		// ★ 有人加了那条索引 ⇒ ⇒ 必须登记「它被本地实验否证了」。
		for _, needle := range []string{"作废", "计划"} {
			if !strings.Contains(runbook, needle) {
				t.Errorf("★ turns 族【已加了 (session_id, ts DESC) 索引】⇒ ⇒ runbook 必须登记 `%s` —— "+
					"★ 本地实验实测它【改不了计划】⇒ ⇒ 谁把它当成修好的事实，谁就踩坑了",
					needle)
			}
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ③：★★★ 「作废」必须落在 §143.100 的段里（★ 防止只在新段里说、旧的推荐还挂着）
// ═══════════════════════════════════════════════════════════════════════

func TestSection143101_Section143100GapFixRetracted(t *testing.T) {
	runbook := readRepoFile143101(t, section143101Runbook)

	// ★ 定位 §143.100 段的起点与下一段起点（★ 只在 §143.100 这一段里查）。
	i := strings.Index(runbook, "### §143.100 ")
	if i < 0 {
		t.Fatalf("★ runbook 里找不到 §143.100 段")
	}
	j := strings.Index(runbook[i+10:], "\n### ")
	if j < 0 {
		j = len(runbook) - i
	}
	seg := runbook[i : i+j]

	// ★ §143.100 段里必须出现「作废」或「否证」⇒ ⇒ 旧的推荐不能还挂着。
	found := false
	for _, needle := range []string{"作废", "否证"} {
		if strings.Contains(seg, needle) {
			found = true
		}
	}
	if !found {
		t.Errorf("★ §143.100 段里既没有「作废」也没有「否证」—— ★ 该段仍把 "+
			"「加 (session_id, ts DESC) 索引」推荐为修法 ⇒ ⇒ §143.101 已用本地实验否证它 "+
			"⇒ ⇒ 必须就地订正（★ 保留原文 + 加订正横幅）")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ④：文档登记（★ 过程可查）
// ═══════════════════════════════════════════════════════════════════════

func TestSection143101_DocsRegistered(t *testing.T) {
	lessons := readRepoFile143101(t, section143101Lessons)
	if !strings.Contains(lessons, "§300") {
		t.Errorf("LESSONS.md 里没有 `§300` —— ★ 通用式没沉淀")
	}
	agents := readRepoFile143101(t, section143101Agents)
	if !strings.Contains(agents, "R146") {
		t.Errorf("AGENTS.md 里没有 `R146` —— ★ 交接没更新")
	}
}
