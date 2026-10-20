package ursm_test

// §143.30 的文档门：**把 §143.29.6 的 P10 做掉了** —— 查清 ursm 快照健康巡检的
// 「两条调用路径」，**结论是「不存在」**；并据此**就地订正 §143.29**（订正横幅十 / 横幅十一）、
// **推翻「被砍在 30 s」这个从未被测量过的假设**。
//
// 背景（runbook §143.30）：
// §143.29.2.2 记下一个矛盾：「这条语句 2 天被 statement timeout 杀 17 次，
// 但 pg_stat_statements 里它有 26 次『成功』记录、均时 190 s ⇒ 两者不可能同时成立
// ⇒ 必有两条调用路径」。本节查清了：**那是同一个脚本的两个版本**（STMT_TIMEOUT 180s → 600s，
// 2026-10-20 00:31 切换），而「26」与「17~18」是**同一个脚本的成功数与失败数**。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **自我订正要留痕**：§143.29 把「30 s 的角色级超时 ⇒ 它一次也跑不完」与
//    「两条调用路径」写成了硬判词 ⇒ 若不订正，后人会拿它当已证实的根因 ⇒ 门钉住
//    **订正横幅十 / 横幅十一** + **原文保留**。
// ② ★★★★★ **「从未测量过的假设」是本节的核心教训**：PG 日志只写
//    `canceling statement due to statement timeout`，**不写是哪一个超时生效**
//    ⇒ 门钉住「124 次超时杀里最短的一次是 187 s」这个反证读数。
// ③ ★★★★ **两把尺子互证**：pgss（26 / 190,473 ms / 591,621 ms）与脚本自己的日志
//    （OK 23 / 202 s / 603 s / ABORT 18）必须并排钉住 ⇒ 门钉住这四个数。
// ④ ★★★★ **两个副产品发现**：超时被突破到 3,198 s（17.8×）、180→600 s 把占用放大 2.9×
////    ⇒ 它们直接决定「调超时是死路」这个方向性结论 ⇒ 门钉住。
// ⑤ ★★★ **诚实边界**：本节只覆盖 252、P9 只拿到一半、日志有 30 次缺收尾
//    ⇒ 门钉住这些边界。

import (
	"os"
	"strings"
	"testing"
)

const runbook14330 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14330 取出 §143.30 这一节的正文。
func section14330(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14330)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.30 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.30 的小节标题：§143.30 的结论整节丢了")
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

// mustContain14330 断言 §143.30 正文里至少含 needle 之一。
func mustContain14330(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.30 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14330SectionHeadingsPresent 钉住 §143.30 的子节骨架齐全。
func TestRunbook14330SectionHeadingsPresent(t *testing.T) {
	s := section14330(t)

	for _, h := range []string{
		"### §143.30.0 结论先行",
		"### §143.30.1 ",
		"### §143.30.2 ",
		"### §143.30.3 ",
		"### §143.30.4 ",
		"### §143.30.5 ",
		"### §143.30.6 ",
		"### §143.30.7 ",
		"### §143.30.8 ",
		"### §143.30.9 ",
		"### §143.30.10 ",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.30 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14330CorrectionBannersLiveIn14329 钉住订正横幅十 / 横幅十一 **就地**在 §143.29 里。
//
// ★ 本仓纪律：错误结论**就地**加横幅 + **保留原文**。
// ★ ★★ **判据必须钉在横幅的「老家」§143.29 里**（不是全文件）—— 否则删掉横幅正文也照样绿。
func TestRunbook14330CorrectionBannersLiveIn14329(t *testing.T) {
	home := section14329(t)

	if !strings.Contains(home, "订正横幅十") {
		t.Fatalf("§143.29 里找不到「订正横幅十」—— §143.29.2.1 的「30 s 角色级超时」误判没有就地留痕")
	}
	if !strings.Contains(home, "订正横幅十一") {
		t.Fatalf("§143.29 里找不到「订正横幅十一」—— §143.29.2.2 的「两条调用路径」误判没有就地留痕")
	}
	// ★ 被订正的原文必须仍在（保留原文）。
	for _, n := range []string{
		"30 s 的角色级超时 ⇒ 它【一次也跑不完】",
		"同一个查询有【两条调用路径】",
		"本节未查 ⇒ 诚实边界",
	} {
		if !strings.Contains(home, n) {
			t.Fatalf("§143.29 里被订正的原文 %q 不见了 —— 本仓纪律要求订正时**保留原文**", n)
		}
	}
	// ★ 订正后的判词必须在横幅里。
	mustContain14330(t, home,
		"假矛盾",
		"没有任何一次 < 180 s",
		"P10 关闭",
	)
}

// TestRunbook14330The30sAssumptionIsRefuted 钉住「被砍在 30 s」被实测推翻。
//
// ★ 这是本节最重要的证据：**124 次超时杀里最短的一次是 187 s**。
func TestRunbook14330The30sAssumptionIsRefuted(t *testing.T) {
	s := section14330(t)

	mustContain14330(t, s,
		"124 次",
		"187 s",
		"没有任何一次 < 180 s",
		"从未作用于此查询",
		"PGOPTIONS",
	)
}

// TestRunbook14330SingleCallPathIsProven 钉住「只有一条调用路径」的取证链。
func TestRunbook14330SingleCallPathIsProven(t *testing.T) {
	s := section14330(t)

	mustContain14330(t, s,
		"/etc/cron.d/pg17:106",
		"17 * * * * root llmgw-source",
		"b5aaa384104fb5f45478cae50cd4ef5e",
		"只有一条调用路径",
		"STMT_TIMEOUT",
	)
}

// TestRunbook14330VersionSwitchIsPinned 钉住「180s → 600s」这个版本切换点。
func TestRunbook14330VersionSwitchIsPinned(t *testing.T) {
	s := section14330(t)

	mustContain14330(t, s,
		"2026-10-20 00:31",
		"600s",
		"180s",
		"71c5bc1de",
		"ec3acdc73",
	)
}

// TestRunbook14330TwoRulersReconcile 钉住两把尺子互证的对照表。
//
// ★ 这是「矛盾是假的」的核心证据：pgss 的 26 / 190,473 ms / 591,621 ms
//   与脚本日志的 OK 23 / 202 s / 603 s / ABORT 18 必须并排出现。
func TestRunbook14330TwoRulersReconcile(t *testing.T) {
	s := section14330(t)

	// ★ 对照表**逐行逐字**断言 —— 这四行就是「矛盾是假的」的全部证据。
	mustContain14330(t, s,
		"| 成功（OK） | **calls = 26** | **23** |",
		"| 失败（ABORT） | 「被杀 17 次」（§143.26.3.1） | **18** |",
		"| 成功执行的平均耗时 | **mean_exec_time = 190,473 ms（190.5 s）** | **202 s** |",
		"| 成功执行的最长耗时 | **max_exec_time = 591,621 ms（591.6 s）** | **603 s**（客户端侧） |",
		"pgss 只记成功",
	)
}

// TestRunbook14330TimeoutIsACeilingNotAClock 钉住「超时是上限不是墙钟」。
//
// ★ 180 s 预算下实际跑到 3,198 s（17.8×）⇒ 这条纪律会直接改变别处的判据写法。
func TestRunbook14330TimeoutIsACeilingNotAClock(t *testing.T) {
	s := section14330(t)

	mustContain14330(t, s,
		"3,198 s",
		"17.8×",
		"不是墙钟上限",
		"实测 elapsed",
		"609 / 613 / 618 / 639 s",
	)
}

// TestRunbook14330RaisingTimeoutCostedUsers 钉住「抬超时有可测量的价格」。
func TestRunbook14330RaisingTimeoutCostedUsers(t *testing.T) {
	s := section14330(t)

	mustContain14330(t, s,
		"2.9×",
		"用成本换结论",
		"没有解决成本",
	)
}

// TestRunbook14330SuccessRateIsRecorded 钉住巡检成功率 56%。
func TestRunbook14330SuccessRateIsRecorded(t *testing.T) {
	s := section14330(t)

	mustContain14330(t, s,
		"56%",
		"量具不可用",
		"OK=0 / ABORT=24",
	)
}

// TestRunbook14330NewProposalsAreRegistered 钉住新增的 P14 / P15 与 P10 关闭。
func TestRunbook14330NewProposalsAreRegistered(t *testing.T) {
	s := section14330(t)

	mustContain14330(t, s,
		"**P14（新增）**",
		"**P15（新增）**",
		"**P10**",
		"关闭",
		"需属主显式授权",
	)
}

// TestRunbook14330HonestBoundaries 钉住诚实边界。
func TestRunbook14330HonestBoundaries(t *testing.T) {
	s := section14330(t)

	mustContain14330(t, s,
		"对生产的写入 = 0",
		"生产行为未变",
		"只覆盖 252",
		"30 次没有收尾",
		"没有重跑 §143.29 的排名",
	)
}
