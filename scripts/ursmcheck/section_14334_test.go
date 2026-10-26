package ursm_test

// §143.34 的文档门：**边界缺口闭合** —— 154 / 245 上【没有】这个巡检
// ⇒ 全网只有 252 与 211 两台在跑 `ursm-snapshot-health.sh`。
//
// 背景（runbook §143.34）：
// ① §143.30.8 / §143.31.4 / §143.32.6 三处都留了同一个缺口：
//    「154 / 245 上有没有同名巡检，没查」；
// ② ⇒ §143.34 四路并查（cron.d 全量 + crontab 全量 + systemd timer + 按名字全盘 find）
//    ⇒ **两台连同名脚本都不存在** ⇒ 不只是「cron 里没有」；
// ③ ⇒ ⇒ 全网只有 252 与 211 在跑 ⇒ 「巡检是 I/O 头号大户（74 GB）」不会被第三个实例稀释；
// ④ ⇒ §143.32.3 那条「提示不是证明」的限定【继续有效】（机器差异没变）。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **「只有两台」是一个【排他性】结论** —— 它靠「查全了」支撑，
//    而「查全了」很容易在下次有人加装时被无声推翻 ⇒ 门钉住它。
// ② ★★★★ **必须钉住「连脚本都不存在」这一条** ——
//    只钉「cron 里没有」会放过「脚本在、调度没配」那种中间形态
//    ⇒ 而那正是会让人误判「这台没跑」的那一种。
// ③ ★★★★ **必须钉住四路侦察的形状** —— 只查 cron 是不够的。
// ④ ★★★ **必须钉住「提示不是证明」这条边界没有被动摇** ——
//    本节没有改变两台机器的差异 ⇒ 「252 慢是争用」仍然只是提示。

import (
	"os"
	"strings"
	"testing"
)

const runbook14334 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14334 取出指定小节的正文（从 "## <prefix>" 到下一个 "## " 之前）。
func section14334(t *testing.T, prefix string) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14334)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## "+prefix) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到小节 %q —— 这一节整节丢了", prefix)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// mustContain14334 断言给定文本里至少含 needle 之一。
func mustContain14334(t *testing.T, what string, body string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(body, n) {
			t.Fatalf("%s 里缺关键读数 %q —— 判据要求的关键证据不见了", what, n)
		}
	}
}

// TestRunbook14334SectionHeadingsPresent 钉住 §143.34 的子节骨架齐全。
func TestRunbook14334SectionHeadingsPresent(t *testing.T) {
	s := section14334(t, "§143.34 ")

	for _, h := range []string{
		"### §143.34.0 结论先行",
		"### §143.34.1 复核方法",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.34 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14334ReconIsFourWay 钉住「四路并查」这个侦察形状。
//
// ★ 只查 cron 是不够的 ⇒ 门钉住四路都写下来了。
func TestRunbook14334ReconIsFourWay(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"/etc/cron.d/",
		"crontab -l",
		"systemctl list-timers",
		"find",
		"按名字全盘",
	)
}

// TestRunbook14334Host154Recon 钉住 154 的侦察读数。
func TestRunbook14334Host154Recon(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"llm-gateway-feishu",
		"llm-gateway-prune",
		"sysstat",
		"cleanup-known-junk",
	)
}

// TestRunbook14334Host245Recon 钉住 245 的侦察读数。
func TestRunbook14334Host245Recon(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"raid-check",
		"update-motd",
	)
}

// TestRunbook14334ScriptAbsentNotJustCron 钉住「连脚本都不存在」这一条。
//
// ★★★ 这是本节最容易被后人弱化的地方：只钉「cron 里没有」会放过
//   「脚本已部署、只是调度没配」那种中间形态。
func TestRunbook14334ScriptAbsentNotJustCron(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"连「同名脚本」都不存在",
		"排除了「脚本在、调度没配」这种中间形态",
		"不存在",
	)
}

// TestRunbook14334OnlyTwoHosts 钉住「全网只有两台」这个排他性结论。
func TestRunbook14334OnlyTwoHosts(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"全网只有 252 与 211 两台在跑",
		"【不会】被第三个实例稀释",
		"74 GB",
	)
}

// TestRunbook14334TwoRunningHostsIdentified 钉住两台在跑的主机各自的调度与脚本。
func TestRunbook14334TwoRunningHostsIdentified(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"/etc/cron.d/pg17",
		"/opt/scripts/ursm-snapshot-health.sh",
		"b5aaa384",
		"/etc/cron.d/llmgw-ursm-snapshot-health",
		"/opt/llm-gateway-go-211/scripts/ursm-snapshot-health.sh",
		"c6d25bde",
	)
}

// TestRunbook14334HintIsStillNotProof 钉住「提示不是证明」这条边界【没有被动摇】。
//
// ★★★ 本节没有改变两台机器的差异 ⇒ 这条限定必须继续有效。
func TestRunbook14334HintIsStillNotProof(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"提示不是证明",
		"继续有效",
		"仍然只能靠",
	)
}

// TestRunbook14334AbsentIsNotExempt 钉住「没有这个巡检 ≠ 不需要审计」。
//
// ★ 这是防止本节被读成「154/245 可以不管了」的那一条。
func TestRunbook14334AbsentIsNotExempt(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"没有这个巡检",
		"两件事",
		"仍然要按 §143.30/§143.31 的口径单独审计",
		"网关主机",
	)
}

// TestRunbook14334BannerLivesIn14332 钉住订正横幅就地钉在 §143.32。
func TestRunbook14334BannerLivesIn14332(t *testing.T) {
	s32 := section14334(t, "§143.32 ")
	s34 := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.32 正文", s32,
		"订正横幅十五",
		"订正横幅十五（2026-10-20，§143.34 就地订正本节 §143.32.6）",
		"没有查 154 / 245 上有没有同名巡检",
	)
	// ★ 横幅的本体不能在 §143.34 里（§143.34 只引用）。
	if strings.Contains(s34, "> **订正横幅十五（") {
		t.Fatalf("订正横幅十五的【本体】出现在 §143.34 里 ⇒ 它被搬走了 ⇒ 「就地订正」不成立")
	}
}

// TestRunbook14334ReconScriptIsInRepo 钉住侦察脚本入仓 + 四路都在脚本里。
//
// ★ 只钉「文档写了四路」不够 ⇒ 还要钉「脚本里真的有四路」，
//   否则「文档说四路、脚本只做两路」会长期共存而无人发现。
func TestRunbook14334ReconScriptIsInRepo(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"scripts/audit/recon_patrol_hosts.sh",
		"它实现了下面这四路",
	)

	raw, err := os.ReadFile("../../scripts/audit/recon_patrol_hosts.sh")
	if err != nil {
		t.Fatalf("仓内侦察脚本不存在：%v", err)
	}
	body := string(raw)
	for _, needle := range []string{
		"/etc/cron.d/",
		"crontab -l",
		"systemctl list-timers",
		"-name \"*ursm-snapshot*\"",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("仓内侦察脚本里缺 %q ⇒ 脚本没有实现文档承诺的四路", needle)
		}
	}
	// ★ 脚本必须只读：不得出现写入类命令。
	for _, forbidden := range []string{"rm -", "mv ", "tee ", ">>", "systemctl start", "systemctl restart"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("仓内侦察脚本里出现写操作 %q ⇒ 它必须是纯只读", forbidden)
		}
	}
}

// TestRunbook14334HonestBoundaries 钉住本节的诚实边界。
func TestRunbook14334HonestBoundaries(t *testing.T) {
	s := section14334(t, "§143.34 ")

	mustContain14334(t, "§143.34 正文", s,
		"对生产的写入 = 0",
		"可原样重跑",
		"TestRunbook14334",
	)
}
