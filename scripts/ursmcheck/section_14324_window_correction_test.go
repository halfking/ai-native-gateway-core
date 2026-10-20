package ursm_test

// §143.24 的文档门：**订正「11-03 的 14 天决策窗口」**（它被证明是误报）、
// **孤儿表的来源是单一的**、**迁移 885 重新裁决为「防御性、无当前收益」**、
// 以及**11-03 真正需要的只剩观测点**。
//
// ⚠★ **号面辨析（改号轮补）**：本文件里出现的 **885** 指**§143.23/§143.24 写作当时**
//   的「CMI 归档孤儿表修复」迁移号（后来 §143.25.8 记了它被上游占用 ⇒ 改号 890，
//   而那个修复**最终没创建**）⇒ ★ 与本会话的 v1 族退役迁移（**890**）/
//   配套索引迁移（**891**）**不是同一个东西**，**一律不改号**：
//   改号会让 needle 与 runbook 正文脱钩 ⇒ 门红（2026-10-20 实测过一轮）。
//
// 背景（runbook §143.24）：
// §143.22 结论 3 写「A1′ 的效果会『自动达成』⇒ 属主的决策窗口因此变紧（14 天）」，
// §143.23.4 列出「落地还差 7 处注册点」。本节复核这条**紧迫性本身** ⇒ 它是误报。
//
// # 为什么这次订正必须有门
//
// ① ★★★★ **「紧迫」本身是最危险的结论** —— 若属主因为「14 天压力」而仓促上线一个
//    未在生产验证过的迁移，风险反而上升 ⇒ 必须钉住「11-03 不需要 885」这个事实。
// ② ★★★★ **订正要给出可复核的读数**（`attached = 1` / 301,255 行），
//    而不是一个判断 ⇒ 门钉住读数，不钉住措辞。
// ③ ★★★ **885 的重新裁决要写清「不是不修，是不急」** ⇒ 钉住「防御性」+「当前无收益」
//    +「真正的时限只剩观测点」，避免被读成「缺陷已消失、不用管」。
// ④ ★★ **观测口径要落成可执行的三条查法** ⇒ 否则 11-03 当天无从判断成败。

import (
	"os"
	"strings"
	"testing"
)

const runbook14324 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14324 取出 §143.24 这一节的正文。
func section14324(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14324)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.24 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.24 的小节标题：§143.24 的结论整节丢了")
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

// mustContain14324 断言 §143.24 正文里至少含 needle 之一。
func mustContain14324(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.24 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14324NovemberThirdDoesNotNeedMigration885 钉住「11-03 不需要 885」。
//
// ★ 这是本节最重要的一条：它解除的是**属主的时间压力**。
func TestRunbook14324NovemberThirdDoesNotNeedMigration885(t *testing.T) {
	s := section14324(t)

	mustContain14324(t, s,
		"attached = 1",
		"301,255",
		"不触发缺陷 ②",
		"不需要迁移 885",
		"没有 14 天的时间压力",
	)
}

// TestRunbook14324OrphanSourceIsSingle 钉住「孤儿表只有 337 一处来源，且 872 不留孤儿」。
func TestRunbook14324OrphanSourceIsSingle(t *testing.T) {
	s := section14324(t)

	mustContain14324(t, s,
		"337",
		"347",
		"872",
		"同边界重建",
		"不会再产生新的孤儿表",
	)
}

// TestRunbook14324Migration885Reclassified 钉住「885 = 防御性、当前无收益、不必赶 11-03」。
//
// ★ 关键：要钉住「不急」而不是「不用管」—— 后者会被误读成缺陷已消失。
func TestRunbook14324Migration885Reclassified(t *testing.T) {
	s := section14324(t)

	mustContain14324(t, s,
		"防御性",
		"当前无收益",
		"不为了赶 11-03 而上线",
		"真正的时限",
	)
}

// TestRunbook14324ObservationPlanIsActionable 钉住 11-03 的观测口径（可执行的三条查法）。
func TestRunbook14324ObservationPlanIsActionable(t *testing.T) {
	s := section14324(t)

	mustContain14324(t, s,
		"观测点",
		"credential_model_index_archive_2026_09",
		"回滚",
	)
}

// TestRunbook14324HonestBoundaries 钉住诚实边界：超时风险未被消除；本节未改生产。
func TestRunbook14324HonestBoundaries(t *testing.T) {
	s := section14324(t)

	mustContain14324(t, s,
		"对生产的写入 = 0",
		"仍是推演",
		"生产行为未变",
	)
}

// TestRunbook14324BannerFiveCorrects14322 钉住 §143.22 的「订正横幅五」+ **原文保留**。
//
// ★ 本仓纪律：错误结论**就地**加横幅 + **保留原文**（不静默改写）。
func TestRunbook14324BannerFiveCorrects14322(t *testing.T) {
	s14322 := section14322(t)

	if n := strings.Count(s14322, "订正横幅五（2026-10-20，§143.24.0 就地订正"); n != 1 {
		t.Fatalf("§143.22 里找到 %d 处「§143.24 的订正横幅五」，期望恰好 1 处", n)
	}

	for _, n := range []string{
		"它是误报",
		"不需要迁移 885",
		"防御性修复",
	} {
		if !strings.Contains(s14322, n) {
			t.Fatalf("订正横幅五里没有点名要订正的 %q —— 被订正的结论没被点名，后人读不到改了什么", n)
		}
	}

	// ★ 原文必须保留（§143.22.0 第 3 条的「14 天」表述仍在）。
	for _, n := range []string{
		"属主的决策窗口因此变紧",
		"### §143.22.4 ★★ 缺陷 ① 的一个反向问题：孤儿分区对读方不可见",
	} {
		if !strings.Contains(s14322, n) {
			t.Fatalf("§143.22 的原文 %q 不在了 —— 本仓纪律要求订正时**保留原文**（只加横幅，不静默改写）", n)
		}
	}
}
