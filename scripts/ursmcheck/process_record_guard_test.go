package ursm_test

// 过程记录文件的**体量守卫**（2026-10-20，§143.30 轮之后新增）
//
// # 背景：一次静默的「蒸发」
//
// 2026-10-20 17:37~17:38，一个并行会话的合并提交 `87952cc4a` 把 `LESSONS.md`
// 从 **429,302 bytes / 198 条**压成 **5,683 bytes / 4 条** ——
// **194 条经验条目在 origin/main 上一次性蒸发**。
//
// ★★ 那次合并的提交信息写着「LESSONS.md 两侧全保留」⇒ **提交信息不能替代文件内容核对**。
// ⇒ ⇒ 与其相信提交信息，不如**让门直接读文件**。
//
// # 这道门守什么
//
// ① `LESSONS.md` 的**条目数**与**字节数**不得低于地板；
// ② 主 runbook 的字节数不得低于地板；
// ③ 两者都不得含合并冲突标记。
//
// ★ 为什么用「地板」而不是「精确值」：这些文件**只增不减**（经验条目是追加的），
// ⇒ 地板既能抓住「被整份覆盖」，又不会在正常追加时误报。
//
// ★ 为什么要**同时**守条目数和字节数：条目数抓「条目被整段丢掉」，
// 字节数抓「文件被换成一份更短但条目数相近的版本」—— 两个尺子指向不同的失效形态。

import (
	"os"
	"strings"
	"testing"
)

const (
	lessonsFile  = "../../LESSONS.md"
	runbookFile  = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

	// 地板取自「事故前」的规模，并留出余量。
	//   LESSONS.md：事故前 198 条 / 429,302 bytes；恢复后 202 条 / 436,947 bytes
	//   runbook：§143.30 落库后 2,288,739 bytes
	lessonsMinEntries = 190
	lessonsMinBytes   = 400_000
	runbookMinBytes   = 2_000_000
)

// TestLessonsFileHasNotBeenClobbered 钉住 LESSONS.md 的体量地板。
//
// ★ 这是本门最直接的目标：**一次合并把 194 条经验静默蒸发**这件事不能再发生。
func TestLessonsFileHasNotBeenClobbered(t *testing.T) {
	raw, err := os.ReadFile(lessonsFile)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", lessonsFile, err)
	}

	if n := len(raw); n < lessonsMinBytes {
		t.Fatalf("LESSONS.md 只有 %d bytes，低于地板 %d —— "+
			"★ 它很可能被某次合并整份替换过（2026-10-20 的 87952cc4a 就是这么丢了 194 条）",
			n, lessonsMinBytes)
	}

	entries := 0
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(ln, "## ") {
			entries++
		}
	}
	if entries < lessonsMinEntries {
		t.Fatalf("LESSONS.md 只有 %d 条经验条目，低于地板 %d —— "+
			"★ 经验记录正在蒸发（§143.30 轮记录过一次：429KB/198 条 → 5.7KB/4 条）",
			entries, lessonsMinEntries)
	}
}

// TestLessonsFileHasNoConflictMarkers 钉住 LESSONS.md 里没有未解决的冲突标记。
//
// ★ 一次 rebase 冲突如果被「解决」成保留两侧，而另一边是整份替换 ⇒ 内容会静默变样。
func TestLessonsFileHasNoConflictMarkers(t *testing.T) {
	raw, err := os.ReadFile(lessonsFile)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", lessonsFile, err)
	}
	text := string(raw)
	for _, marker := range []string{"<<<<<<< ", ">>>>>>> ", "\n=======\n"} {
		if strings.Contains(text, marker) {
			t.Fatalf("LESSONS.md 里还有合并冲突标记 %q —— 冲突没有被真正解决", marker)
		}
	}
}

// TestRunbookHasNotBeenClobbered 钉住主 runbook 的体量地板。
//
// ★ 同一类风险：runbook 是本仓的过程记录主体（§143.x 全在里面），
//   它一旦被整份覆盖，历史读数与订正横幅会一起消失。
func TestRunbookHasNotBeenClobbered(t *testing.T) {
	raw, err := os.ReadFile(runbookFile)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", runbookFile, err)
	}
	if n := len(raw); n < runbookMinBytes {
		t.Fatalf("主 runbook 只有 %d bytes，低于地板 %d —— 它可能被某次合并整份替换过",
			n, runbookMinBytes)
	}
	text := string(raw)
	for _, marker := range []string{"<<<<<<< ", ">>>>>>> "} {
		if strings.Contains(text, marker) {
			t.Fatalf("主 runbook 里还有合并冲突标记 %q", marker)
		}
	}
}
