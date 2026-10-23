//go:build !integration

package main

// §143.82 的门：把「★ 版本谱系」与「★ §143.81 的两处自我订正」钉成结构判据。
//
// ★★★★ 为什么这一轮要有这道门（★ §143.82 的起因）：
//
//	★ §143.77~§143.81 连续五轮都写着同一条诚实边界：
//	「生产仍跑 §143.56 之前代码 ⇒ ⇒ 需要授权部署 154/245」。
//	★ ★★ 而它一直是【推断出来的】⇒ ⇒ ★ 本轮实测把它【证伪】：
//	两台的 release 目录名里带 commit sha ⇒ ⇒ `bd0a27f7` ⇒ ⇒ 已含 §143.56。
//	★ ⇒ ⇒ ⇒ 「拍板项 ⑰」关闭 ⇒ ⇒ ★★ **⇒ 而「一条被反复引用的前提是推断出来的」
//	本身【就该有门】⇒ ⇒ 不然下一轮又会把它当事实用。**
//
// ★★ 这道门要守的不是「某个脚本存在」，而是【几件仍然为真的事实】：
//
//	① ★★★★ 取证脚本必须【枚举候选服务】⇒ ⇒ 不许写死一个服务名
//	   （★ 实测 154 = `llm-gateway-go-canary@8782`、245 = `llmgo-245-canary@8782`）
//	② ★★★★ 取证脚本必须用【release 目录名里的 sha】判版本
//	   ⇒ ⇒ 不许靠进程启动时间推断（★ 启动时间只能给【下限】）
//	③ ★★ 取证脚本必须覆盖【154 与 245】两台
//	④ ★★★ §143.82 段必须登记【bd0a27f7】与【它已含 §143.56】这条事实
//	⑤ ★★★ §143.81 段必须带「§143.82 已订正」标志（★ 两处自我订正）
//	⑥ ★★★ §143.80 必须带「订正横幅二（2026-10-23 §143.82 轮）」
//	⑦ ★★ LESSONS §272 与 AGENTS R124 必须存在（★ 交接面）

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14382LineageScript = "scripts/audit/verify_release_version_lineage.sh"
	section14382Runbook       = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14382Lessons       = "LESSONS.md"
	section14382Agents        = "AGENTS.md"
)

func repoRoot14382(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14382(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14382(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// section14382Section14381 取出 runbook 里 §143.81 那一段（★ 到下一个 `### §143.` 为止）。
func section14382Section14381(t *testing.T, runbook string) string {
	t.Helper()
	i := strings.Index(runbook, "### §143.81")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.81 段")
	}
	seg := runbook[i:]
	if j := strings.Index(seg, "\n### §143."); j > 0 {
		seg = seg[:j]
	}
	return seg
}

// stripShellComments14382 去掉 shell 脚本的 `#` 行注释。
//
// ★ 用途：★ 判据「不许写死服务名」必须【分面】——
// ★ 代码面（剥注释后）不许写死；★ 但注释里【必须能】写明「实测两台服务名不同」
// ⇒ ⇒ 那是这段量具的教学价值所在 ⇒ ⇒ 不许被自己的守卫误伤。
func stripShellComments14382(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestSection14382LineageScriptMustEnumerateServices（★ 判据①③）
//
// ★★ 取证脚本必须【枚举候选服务】，不许写死一个服务名——
//
//	★ 实测两台的服务名【不同】：154 = `llm-gateway-go-canary@8782.service`，
//	245 = `llmgo-245-canary@8782.service`（★ `llm-gateway-go*` 在 245 上全部 inactive）
//	★ ⇒ ⇒ 写死一个 ⇒ ⇒ 另一台会被读成「没有服务」⇒ ⇒ ⇒ 被误读成「没部署」
func TestSection14382LineageScriptMustEnumerateServices(t *testing.T) {
	raw := readRepoFile14382(t, section14382LineageScript)

	// ①★ ★★★ 必须【按 PID 反查服务名】或【枚举服务】⇒ ⇒ ★★★ 【不许写死服务名】
	//   ★ 实测两台的服务名【不同】：154 = `llm-gateway-go-canary@8782.service`、
	//   ★ 245 = `llmgo-245-canary@8782.service`
	//   ★ ⇒ ⇒ 写死一个 ⇒ ⇒ 另一台会被读成「没服务」⇒ ⇒ ⇒ 那是【量具坏了】不是事实
	hasStatus := strings.Contains(raw, "systemctl status")
	hasList := strings.Contains(raw, "list-units")
	if !hasStatus && !hasList {
		t.Error("★ 版本谱系脚本既没有用 `systemctl status` 反查服务名、★ 也没有用 " +
			"`systemctl list-units` 枚举服务 —— " +
			"★ ★★ 两台的服务名不同（154 = `llm-gateway-go-canary@8782`、" +
			"★ 245 = `llmgo-245-canary@8782`）⇒ ⇒ 写死一个 ⇒ ⇒ 另一台会被读成「没服务」")
	}
	// ①★ ★★★ 反向判据：★ 脚本里【不许出现写死的服务名字面量】
	code := stripShellComments14382(raw)
	for _, svc := range []string{
		"llm-gateway-go-canary@8782.service",
		"llmgo-245-canary@8782.service",
	} {
		if strings.Contains(code, svc) {
			t.Errorf("★ 版本谱系脚本里写死了服务名 %q —— "+
				"★ ★★ 两台的服务名不同 ⇒ ⇒ 写死一个 ⇒ ⇒ 另一台会被读成「没在跑」"+
				"★ ⇒ ⇒ 脚本必须【按 PID 反查】或【枚举】，★ 不得写死", svc)
		}
	}
	// ①★ ★★★ 也不许写死二进制名（★ 实测 154 = `llm-gateway-go`、245 = `gateway`）
	for _, bin := range []string{"*/gateway)", "llm-gateway-go\""} {
		if strings.Contains(code, bin) {
			t.Errorf("★ 版本谱系脚本里疑似写死了二进制名 %q —— "+
				"★ ★★ 实测 154 的二进制叫 `llm-gateway-go`、245 的叫 `gateway`"+
				"★ ⇒ ⇒ 写死一个 ⇒ ⇒ 另一台会被读成「没在跑」", bin)
		}
	}
	// ② 必须覆盖两台
	for _, host := range []string{"154", "245"} {
		if !strings.Contains(raw, host) {
			t.Errorf("★ 版本谱系脚本里没有 %q 这台机 —— "+
				"★ 本轮实测【两台都跑着共享库的写入侧】⇒ ⇒ 少一台 ⇒ ⇒ 结论就不完整", host)
		}
	}
	// ③ 必须取到真实二进制路径（★ 而不是靠配置里的版本号）
	if !strings.Contains(raw, "/proc/") || !strings.Contains(raw, "readlink") {
		t.Error("★ 版本谱线脚本没有从 `/proc/<pid>/exe` 读真实二进制路径 —— " +
			"★ ★★ release 目录名才是【这台机器当前跑的那版】的事实源 ⇒ ⇒ " +
			"★ 配置里写的版本号可能与实际不符")
	}
}

// TestSection14382LineageScriptMustJudgeByReleaseSha（★ 判据②）
//
// ★★★ 判版本必须用【release 目录名里的 sha】⇒ ⇒ 不许靠进程启动时间推断。
//
//	★ 启动时间只能给出一个【下限】⇒ ⇒ 它【无法证明】「那个 commit 已经在里面」。
func TestSection14382LineageScriptMustJudgeByReleaseSha(t *testing.T) {
	raw := readRepoFile14382(t, section14382LineageScript)

	if !strings.Contains(raw, "RELEASE=") || !strings.Contains(raw, "releases") {
		t.Error("★ 版本谱系脚本没有读 `releases/` 目录下的 release 名 —— " +
			"★ ★★ release 目录名里带 commit sha ⇒ ⇒ 那是【当前跑的那版】的事实源")
	}
	if !strings.Contains(raw, "merge-base --is-ancestor") {
		t.Error("★ 版本谱系脚本没有用 `git merge-base --is-ancestor` 判祖先关系 —— " +
			"★ ⇒ ⇒ 「这台机器包不包含那个修复」就没有一条机械判定 ⇒ ⇒ " +
			"★ ⇒ 下轮又只能靠推断 ⇒ ⇒ 而本轮教训正是【推断出过错的前提】")
	}
	// ★ 判据必须能表达「否」⇒ ⇒ 脚本里不能只有一条 affirmative 分支
	for _, needle := range []string{"CONTAINS_TARGET=YES", "CONTAINS_TARGET=NO"} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 版本谱系脚本里没有 %q —— "+
				"★ ★★ 判据必须能同时表达【是】与【否】⇒ ⇒ 只有 affirmative 分支时，"+
				"★ 「读不到 sha」会被误读成「包含」", needle)
		}
	}
}

// TestSection14382RunbookMustRegisterLineageFact（★ 判据④）
//
// ★★★ 版本谱系这条【可复核的事实】必须落在仓内⇒ ⇒ 否则下一轮无从复核。
func TestSection14382RunbookMustRegisterLineageFact(t *testing.T) {
	runbook := readRepoFile14382(t, section14382Runbook)
	i := strings.Index(runbook, "### §143.82")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.82 段")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"bd0a27f7",         // ★ 两台当前 release 的 sha
		"llmgo-245-canary", // ★ 245 的服务名（★ 154 的那个到处都有 ⇒ ⇒ 少登记这条就查不出）
		"merge-base",       // ★ 判定方法
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.82 段里没有 %q —— "+
				"★ 登记面必须留下【可复核的关键词】⇒ ⇒ 否则下一轮无从复核", needle)
		}
	}
}

// TestSection14382PriorSectionsMustCarryCorrections（★ 判据⑤⑥）
//
// ★★★★ 【错误结论必须就地订正】⇒ ⇒ ★ 而且订正必须【真的落在那一段里】。
//
//	★ §143.81 里有两处本会话自己的错（「量级缩了三个数量级」与「jsonb 判据由 §143.56 引入」）
//	★ §143.80 里有「生产仍跑 §143.56 之前代码」⇒ ⇒ 三个都要就地订正。
func TestSection14382PriorSectionsMustCarryCorrections(t *testing.T) {
	runbook := readRepoFile14382(t, section14382Runbook)

	// ① §143.81 段必须带订正标志
	seg81 := section14382Section14381(t, runbook)
	if !strings.Contains(seg81, "§143.82 已订正") {
		t.Error("★ §143.81 段里没有「§143.82 已订正」标志 —— " +
			"★ ★★ 本轮推翻了 §143.81 的两个结论（「现时量级缩了三个数量级」" +
			"★ 与「jsonb 判据由 §143.56 引入」）⇒ ⇒ 必须就地订正（★ 原文保留）")
	}

	// ② §143.81 段里必须出现「分钟/天」的【已订正值】⇒ ⇒ 防止旧的外推数字留在原地
	for _, needle := range []string{"22.8", "25.2"} {
		if !strings.Contains(seg81, needle) {
			t.Errorf("★ §143.81 段里没有 %q —— "+
				"★ ★★ §143.82 已把现时成本折算成 22.8 ~ 25.2 分钟/天（★ 带独立交叉验证）"+
				"★ ⇒ ⇒ 旧段落里必须带上订正值 ⇒ ⇒ 否则会被读成「没有日均口径」", needle)
		}
	}

	// ③ §143.80 段必须带「订正横幅二（2026-10-23 §143.82 轮）」
	i80 := strings.Index(runbook, "### §143.80")
	if i80 < 0 {
		t.Fatal("★ runbook 里找不到 §143.80 段")
	}
	seg80 := runbook[i80:]
	if j := strings.Index(seg80, "\n### §143."); j > 0 {
		seg80 = seg80[:j]
	}
	if !strings.Contains(seg80, "订正横幅二（2026-10-23 §143.82 轮") {
		t.Error("★ §143.80 段里没有「订正横幅二（2026-10-23 §143.82 轮）」—— " +
			"★ ★★ §143.80 的诚实边界写着「生产仍跑 §143.56 之前代码」⇒ ⇒ " +
			"★ ★★ 本轮已证伪（★ 两台 release 都是 `bd0a27f7` ⇒ 已含）⇒ ⇒ " +
			"★ ⇒ 必须就地订正（★ 原文保留）")
	}
}

// TestSection14382HandoffSurfacesExist（★ 判据⑦）
//
// ★★★ 交接面必须齐（★ 冷启动的下一轮就靠它们）。
func TestSection14382HandoffSurfacesExist(t *testing.T) {
	lessons := readRepoFile14382(t, section14382Lessons)
	if !strings.Contains(lessons, "## §272") {
		t.Error("★ LESSONS.md 里没有 §272 —— " +
			"★ ⇒ ⇒ 「版本谱系能把推断变成机械判定」与「差分语义要先算清」这两条通用式必须落进去")
	}
	agents := readRepoFile14382(t, section14382Agents)
	if !strings.Contains(agents, "R124") {
		t.Error("★ AGENTS.md 里没有 R124 —— " +
			"★ ⇒ ⇒ 本轮的结论（★ ⑰ 关闭 / 两处自我订正 / 速率突变待查）必须落在交接段")
	}
}

// TestSection14382BRateSpikeMustBeCorrected（★ 判据⑧）
//
// ★★★★ 【一条被写进仓的【错结论】必须被【就地作废】⇒ ⇒ 否则它会被后人当事实用】。
//
//	★ §143.82.5 曾写「部署后 L3 调用速率涨约 20 倍」⇒ ★ §143.82-B 实测作废
//	★ （★ 真相是【流量恢复】：★ 245 的日志有 22 小时空档 ⇒ ⇒ 与部署无关）。
//
// ★★ 判据要钉的不是「文档里没有『20 倍』这几个字」（★ 原文保留是纪律），
// ★★ 而是【那段必须带着作废标志】⇒ ⇒ 否则「原文保留」会变成「错结论继续流传」。
func TestSection14382BRateSpikeMustBeCorrected(t *testing.T) {
	runbook := readRepoFile14382(t, section14382Runbook)

	// ① §143.82-B 段必须存在
	i := strings.Index(runbook, "### §143.82-B")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.82-B 段 —— ★ 本轮的【作废 + 补齐 245 样本】必须落在仓内")
	}
	segB := runbook[i:]

	// ② §143.82.5 那一段必须带【作废】标志（★ 原文保留，但不许无标志）
	i5 := strings.Index(runbook, "#### §143.82.5 ")
	if i5 < 0 {
		t.Fatal("★ runbook 里找不到 §143.82.5 段")
	}
	seg5 := runbook[i5:]
	if j := strings.Index(seg5, "\n#### §143.82."); j > 0 {
		seg5 = seg5[:j]
	}
	if !strings.Contains(seg5, "已作废") {
		t.Error("★ §143.82.5 段没有「已作废」标志 —— " +
			"★ ★★ 该段写的「部署后 L3 调用速率涨约 20 倍」已被 §143.82-B 实测推翻" +
			"★ ⇒ ⇒ 「原文保留」必须配「作废标志」⇒ ⇒ 否则错结论会被当事实用")
	}

	// ③ §143.82-B 段必须带【真正���】与【两条关键读数】
	for _, needle := range []string{
		"22 小时", // ★★ 245 的日志空档（★ 「是流量恢复」的直接证据）
		"1,361", // ★★ 245 服务侧的冷启动条数（★ 跨 3 天 body_size 全 0）
		"流量恢复",  // ★★ 真正的归因
	} {
		if !strings.Contains(segB, needle) {
			t.Errorf("★ §143.82-B 段里没有 %q —— "+
				"★ 订正面必须留下【可复核的关键读数】⇒ ⇒ 否则「作废」变成了「没有替代说法」", needle)
		}
	}

	// ④ 交接面
	lessons := readRepoFile14382(t, section14382Lessons)
	if !strings.Contains(lessons, "## §273") {
		t.Error("★ LESSONS.md 里没有 §273 —— " +
			"★ ⇒ ⇒ 「速率变化先分清行为与流量」这条通用式必须落进去")
	}
	agents := readRepoFile14382(t, section14382Agents)
	if !strings.Contains(agents, "R125") {
		t.Error("★ AGENTS.md 里没有 R125 —— " +
			"★ ⇒ ⇒ 本轮的作废与补齐必须落在交接段（★ 否则下一轮会重走一遍并再写一次「20 倍」）")
	}
}
