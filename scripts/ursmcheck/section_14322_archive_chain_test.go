package ursm_test

// §143.22 的文档门：**archive 链路的根因链**、**「11-03 第一次真归档」预警**、
// **缺陷 ② 的可修形态**，以及**对 §143.21.3 的就地订正**。
//
// 背景（runbook §143.22）：
// §143.21.3 判「archive 路径几乎零产出 ⇒ 冷族回收完全押在 archive 上，而它基本没跑」，
// 并把 A3′ 列为「要么让它真跑，要么把保留期交给 A2′」。本节查清后**改判**：
//   ① 归档机制**本身是通的**（RDL 族的 dst `_2026_08/_2026_09/_2026_10` 都存在，
//      而源 `_2026_08` 已不在父表下 ⇒ 归档完整走完了「建 dst → 搬 → DETACH → DROP」）；
//   ② 零产出有**三个不同原因**（目标月为空 / 目标月是孤儿表 / skipped）；
//   ③ ★★ **2026-11-03 是这个库上第一次「真归档」**（目标 = 2026_09，301,255 行）
//      ⇒ A1′ 的效果会**自动达成**，且历史完整保留在 archive 表里。
//
// # 为什么这些结论必须有门
//
// ① ★★★★ **「archive 坏了」与「archive 还没到能搬的月份」是两个完全不同的处置**：
//    前者要先修链路，后者只需要**观测 11-03 那一刻** ⇒ 钉住「机制是通的」这个判词。
// ② ★★★★ **机制性缺陷可从 DDL 直接推出**（654 的 `DETACH PARTITION` 对孤儿表必然报错）
//    ⇒ 不必等复现就能钉住 ⇒ 同时钉住「静默」这个关键属性
//    （`pg_stat_statements` 只记成功语句 ⇒ 失败在量具上完全不可见）。
// ③ ★★ **11-03 的预警有明确时限**（14 天）⇒ 钉住「目标 = 2026_09 / 301,255 行 /
//    在父表下」这三个事实，避免被误读成「11-03 会搬 08 月（已不存在 ⇒ skipped）」。
// ④ ★★ **修法 A/C 是「缺陷修复」不是「策略变更」** ⇒ 不撞属主的语义决策
//    ⇒ 钉住这个性质，避免下一个人把它当成需要重新拍板的策略题。

import (
	"os"
	"strings"
	"testing"
)

const runbook14322 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14322 取出 §143.22 这一节的正文。
//
// ★ 同款纪律（§143.17~§143.21 一致）：必须**按节切**再断言
// —— 本 runbook 已 36,000+ 行，同一个量会出现十几次 ⇒ 整篇断言等于断言不到东西。
func section14322(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14322)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.22 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.22 的小节标题：§143.22 的结论整节丢了")
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

// mustContain14322 断言 §143.22 正文里至少含 needle 之一。
func mustContain14322(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.22 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14322ArchiveMechanismIsNotBroken 钉住「机制是通的，零产出有三个原因」。
func TestRunbook14322ArchiveMechanismIsNotBroken(t *testing.T) {
	s := section14322(t)

	// 机制通的证据：RDL 族的三个 dst 分区 + 源已不在父表下。
	mustContain14322(t, s,
		"routing_decision_log_archive_2026_08",
		"routing_decision_log_archive_2026_09",
		"routing_decision_log_archive_2026_10",
		"搬了 0 行",
	)

	// 三个原因都要在（不能只说一个）。
	mustContain14322(t, s,
		"目标月分区是空的",
		"孤儿表",
		"skipped",
	)
}

// TestRunbook14322DetachOnOrphanTableFailsSilently 钉住机制性缺陷 ②。
//
// ★ 这条是**可从 DDL 直接推出**的：654 收尾的
// `ALTER TABLE credential_model_index DETACH PARTITION src_part`
// 对「已 DETACH 出父表的同名表」必然报错。
func TestRunbook14322DetachOnOrphanTableFailsSilently(t *testing.T) {
	s := section14322(t)

	mustContain14322(t, s,
		"337_detach_current_future_partitions.sql",
		"DETACH PARTITION",
		"静默",
		"只记成功语句",
		// ★ 「孤儿表」是缺陷 ② 的**核心概念**（= 已被 DETACH 出父表的同名表）。
		//   缺这条 needle 时，变异 M2（把它改成「普通表」）会让本门全绿
		//   ⇒ **门就没有守卫力** ⇒ 必须钉住。
		"孤儿表",
	)
}

// TestRunbook14322NovemberThirdIsFirstRealArchive 钉住 11-03 预警的三个关键事实。
//
// ★ 尤其要钉住「目标 = 2026_09」—— 若被误读成「11-03 搬 08 月」，
// 结论会变成「skipped ⇒ 无事发生」，与事实相反。
func TestRunbook14322NovemberThirdIsFirstRealArchive(t *testing.T) {
	s := section14322(t)

	mustContain14322(t, s,
		"2026-11-03",
		"2026_09",
		"301,255",
		"84 MB",
		"在父表下",
		"自动达成",
	)
}

// TestRunbook14322FixIsDefectRepairNotPolicy 钉住「修法不撞属主语义决策」。
func TestRunbook14322FixIsDefectRepairNotPolicy(t *testing.T) {
	s := section14322(t)

	mustContain14322(t, s,
		"缺陷修复",
		"不是「策略变更」",
		"不撞属主的语义决策",
	)
}

// TestRunbook14322BannerFourCorrects14321 钉住 §143.21.3 的「订正横幅四」+ **原文保留**。
//
// ★ 本仓纪律：错误结论**就地**加横幅 + **保留原文**（不静默改写）。
func TestRunbook14322BannerFourCorrects14321(t *testing.T) {
	s14321 := section14321(t)

	if n := strings.Count(s14321, "订正横幅四（2026-10-20，§143.22.1 就地订正"); n != 1 {
		t.Fatalf("§143.21 里找到 %d 处「§143.22 的订正横幅四」，期望恰好 1 处", n)
	}

	// 横幅必须点名被改判的那几点。
	for _, n := range []string{
		"机制本身是通的",
		"三个不同原因",
		"第一次「真归档」",
	} {
		if !strings.Contains(s14321, n) {
			t.Fatalf("订正横幅四里没有点名要改判的 %q —— 被改判的结论没被点名，后人读不到改了什么", n)
		}
	}

	// ★ 原文必须保留（§143.21.3 的原始表头与「几乎零产出」判词仍在）。
	for _, n := range []string{
		"### §143.21.3 ★★★ archive 路径几乎零产出",
		"`archive_credential_model_index` 在 `pg_stat_statements` 的调用",
	} {
		if !strings.Contains(s14321, n) {
			t.Fatalf("§143.21.3 的原文 %q 不在了 —— 本仓纪律要求订正时**保留原文**（只加横幅，不静默改写）", n)
		}
	}
}

// TestRunbook14322HonestBoundaries 钉住诚实边界：本节仍为 0 生产写，且不承诺「11-03 一定成功」。
func TestRunbook14322HonestBoundaries(t *testing.T) {
	s := section14322(t)

	mustContain14322(t, s,
		"对生产的写入 = 0",
		"是**推演**，不是实测",
		"未闭合",
	)
}
