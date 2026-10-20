package ursm_test

// §143.23 的文档门：**本地真库实测**（缺陷②复现 + 修法 A/C 验证 + 回归 + 幂等）、
// **落地待办**（7 处注册点）、**诚实边界**（本地无 columnar ⇒ 生产耗时仍未量化）。
//
// 背景（runbook §143.23）：
// §143.22 留下一个明确缺口：「11-03 会成功」是**推演，不是实测**（第 5 步耗时未量化）。
// 本节用**本地真库**（一次性库 itgate_14323，退出即 DROP，**对生产写入 = 0**）闭合它。
//
// # 为什么这些结论必须有门
//
// ① ★★★★★ **缺陷②必须从「DDL 推理」升级为「实测复现」** —— 门钉住**报错原文**
//    （`is not a partition of relation`）与**事务回滚的两个读数**
//    （dst 不存在 + 源行数仍在）⇒ 后人不用重新造环境。
// ② ★★★★★ **修法必须钉住「函数签名与返回形状不变 ⇒ Go 侧零改动」** —— 这是它
//    能被当成「零语义风险缺陷修复」的唯一依据 ⇒ 一旦有人改签名，门要红。
// ③ ★★★★ **「本节未写迁移、未改注册点 ⇒ 生产行为未变」必须钉住** ——
//    否则读者会误以为生产已经修好（§143.23.4 明确列了 7 处注册点）。
// ④ ★★ **诚实边界必须钉住**（本地无 columnar AM ⇒ 生产耗时仍未量化）——
//    这是「实测能闭合什么、不能闭合什么」的边界，不能被后人悄悄放大。

import (
	"os"
	"strings"
	"testing"
)

const runbook14323 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14323 取出 §143.23 这一节的正文。
func section14323(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14323)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.23 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.23 的小节标题：§143.23 的结论整节丢了")
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

// mustContain14323 断言 §143.23 正文里至少含 needle 之一。
func mustContain14323(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.23 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14323DefectReproducedOnRealDB 钉住「缺陷②实测复现 + 报错原文 + 事务回滚」。
func TestRunbook14323DefectReproducedOnRealDB(t *testing.T) {
	s := section14323(t)

	mustContain14323(t, s,
		"is not a partition of relation", // 报错原文
		"DETACH PARTITION credential_model_index_2026_09",
		"dst_2026_09_exists=0",  // 回滚：dst 不存在
		"src_orphan_rows=345,600", // 回滚：源行数仍在
		"345,600",
	)
}

// TestRunbook14323FixVerifiedAndSignatureUnchanged 钉住「修法有效 + 签名/形状不变 ⇒ Go 零改动」。
func TestRunbook14323FixVerifiedAndSignatureUnchanged(t *testing.T) {
	s := section14323(t)

	mustContain14323(t, s,
		"src_attached",
		"is detached from credential_model_index (orphan table)",
		"行数守恒", // 345,600 = 345,600
		"函数签名与返回形状逐字不变",
		"Go 侧 `runArchive` 零改动",
	)
}

// TestRunbook14323NotYetLanded 钉住「本节未写迁移、未改注册点 ⇒ 生产行为未变」。
//
// ★ 这是防误读的关键：读者若以为生产已修好，会跳过 §143.23.4 的 7 处注册点。
func TestRunbook14323NotYetLanded(t *testing.T) {
	s := section14323(t)

	mustContain14323(t, s,
		"生产上跑的还是 654",
		"生产行为未变",
		"installer/internal/dbinit/runner.go",
		// ⚠★ 这个 885 是**当时的**孤儿表修复号（§143.23 写作时它还没被上游占用），
		//   与本会话的 v1 族退役迁移（890）/ 配套索引（891）**无关** ⇒ 不做改号。
		"embeddata/startup/885",
		"installed_startup_migrations.tsv",
		"docs/db-changelog.md",
	)
}

// TestRunbook14323HonestBoundaries 钉住诚实边界：本地无 columnar ⇒ 生产耗时仍未量化。
func TestRunbook14323HonestBoundaries(t *testing.T) {
	s := section14323(t)

	mustContain14323(t, s,
		"不含 `columnar` access method",
		"仍未量化",
		"一次性库",
		"对生产的写入 = 0",
	)
}
