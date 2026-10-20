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
//
// ⚠⚠ 2026-10-21 §143.35 收尾轮：**检测口径从【子串】改成【行首锚定】**。
// ★★ 原口径 `strings.Contains(text, "<<<<<<< ")` 会把**正文里引用这些标记的散文**
//   也判成冲突 —— 实测踩到：上游自己在 LESSONS 里记「为了两侧都保留，写了个脚本
//   按 `<<<<<<< HEAD` / `=======` / `>>>>>>> <sha>` 三个标记切片拼接」，
//   那段散文里的标记写在【反引号里、行中】⇒ 被子串检查误判
//   ⇒ ⇒ **origin/main 因此判红**（已在干净副本上复核）。
// ⇒ ⇒ 而 git 写出的真冲突标记**必在第 0 列**（opener/closer 行首、separator 独占一行）
// ⇒ ⇒ **行首锚定既不漏真冲突、又不再误伤散文**。
// ⇒ ★ 改口径后必须双向验：行首的真标记仍判红（阳性对照），行中的散文引用仍判绿。
func TestLessonsFileHasNoConflictMarkers(t *testing.T) {
	raw, err := os.ReadFile(lessonsFile)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", lessonsFile, err)
	}
	text := string(raw)
	// ★ 阳性对照：行首锚定的真冲突标记必须被抓到（否则这条门会变成恒绿门）。
	for _, marker := range []string{"<<<<<<< ", ">>>>>>> "} {
		for i, ln := range strings.Split(text, "\n") {
			if strings.HasPrefix(ln, marker) {
				t.Fatalf("LESSONS.md 第 %d 行还有合并冲突标记 %q —— 冲突没有被真正解决",
					i+1, marker)
			}
		}
	}
	// ★ 刻意【不】单查「独占一行的分隔标记」：
	//   git 的冲突区**必然同时**有 opener 与 closer ⇒ 只看 opener/closer 就够；
	//   而「一行正好是 7 个等号」与 markdown 的 setext 标题下划线**不可区分**
	//   ⇒ ★ 过程文档里引用/展示这个形态是合法的（runbook 就在解释它）
	//   ⇒ 单查它会把**文档在讲这件事**判成冲突（实测踩到）。
}

// TestRunbookHasNotBeenClobbered 钉住主 runbook 的体量地板。
//
// ★ 同一类风险：runbook 是本仓的过程记录主体（§143.x 全在里面），
//   它一旦被整份覆盖，历史读数与订正横幅会一起消失。
//
// ⚠⚠ 2026-10-21 §143.35 收尾轮：**冲突标记检测同样改成【行首锚定】**。
// ★★ 实测踩到：runbook 是**过程日志**，它**本来就会记录冲突标记长什么样**
//   （本轮 §143.35.9 就在解释「子串口径为什么误伤」）⇒
//   ⇒ 子串检查会把**它自己写的说明**判成冲突 ⇒ 门把自己写红了。
// ⇒ ⇒ 而 git 写出的真冲突标记**必在第 0 列** ⇒ 行首锚定既不漏真冲突、
//   又允许正文正常引用这些标记。
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
	// ★ 行首锚定：git 的冲突 opener/closer 必在第 0 列。
	for _, marker := range []string{"<<<<<<< ", ">>>>>>> "} {
		for i, ln := range strings.Split(text, "\n") {
			if strings.HasPrefix(ln, marker) {
				t.Fatalf("主 runbook 第 %d 行还有合并冲突标记 %q", i+1, marker)
			}
		}
	}
	// ★ 同样刻意【不】单查「独占一行的分隔标记」—— 理由见上面那道门：
	//   七等号独占一行与 markdown setext 下划线不可区分，而 runbook 作为过程日志
	//   本来就会讲到冲突标记长什么样。
}
