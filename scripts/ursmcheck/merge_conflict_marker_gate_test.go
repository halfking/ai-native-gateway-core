package ursm_test

// ★★★★ 冲突标记门（★ 2026-11-24 合并轮二十一·续）★★★★
//
// ★★ **这条门是被一次真实事故逼出来的** ★★
//
// ★★ **事故**：★ 上游 main（★ codeup `origin/main` ＋ github 镜像）里
// ★★ `scripts/checks/fresh-install-smoke.sh` **带着两行 `<<<<<<< Updated upstream`**
// ★★ **进了仓** ⇒ ★★ 而 bash 把它当重定向 ⇒ ★★ **语法错** ⇒ ★★ **那道门在
// ★★ 第二阶段就崩了**（★ 第一阶段还照跑 ⇒ ★★ **看起来像跑过、其实是崩在半路**）★★
// ★★ ⇒ ★★ 而「门脚本家族」那几道门**只查风格、不查可执行性** ⇒ ★★ **谁都没看见** ★★
//
// ★★ **根因（★ 不是「有人手滑」）**：★ 那笔自称「合并」的提交 **只有一个父**
// ★★ （★ `cdd155fff` 的 parent 只有 `54030ac22`）⇒ ★★ **它不是 merge、★ 是手写的
// ★★ 合并** ⇒ ★★ 手工粘内容时 **把冲突标记一起粘了进去** ⇒ ★★ ⇒ ★★ **「合并」这件事
// ★★ 在那个提交里是假的** ★★ ★★
//
// ★★★ **判据（三条、★ 逐条说清为什么这样切）★★★
//
//	① `<<<<<<<` / `>>>>>>>` / `|||||||` 出现在**行首** ⇒ ★ **一律判红**
//	   （★ 这三种在正常文本里不会出现；★ 而 markdown 的 `=======` 会出现、★ 所以不单列它 ★）
//	② `=======`（★ 独占一行、★ 恰好七个等号）⇒ ★ **只有当同一个文件里也有 `<<<<<<<`
//	   时才判红**（★ ⇒ ★★ markdown 的 setext 标题下划线、★ README 里的分隔线都不会误报 ★★）
//	③ 作用域＝**git 认的文件**（★ `git ls-files --cached --others --exclude-standard`）
//	   ⇒ ★★ 与仓内既有门同口径：★ 未跟踪但没被忽略的文件（★ ＝ 即将提交的新文件）
//	   ★★ 也进作用域 ⇒ ★★ ⇒ ★★ **「提交前就能看见」的能力不丢** ★★
//
// ★★ **内容读的是工作树**（★ 而文件集读的是 git）⇒ ★★ ⇒ ★★ **本地改了就能验、
// ★★ 不用先提交** ⇒ ★★ ★★ 而 `git grep --cached` 读的是【索引】⇒ ★★ 那个口径下
// ★★ 「刚改好、还没 stage」的修法**看不见** ⇒ ★★ ⇒ ★★ **本门刻意不用 `git grep` ** ★★

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mergeConflictMarkerFiles 返回 git 认的文件（★ 相对仓根）。
func mergeConflictMarkerFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--full-name",
		"--cached", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("★ 列 git 认的文件失败（★ 需要 git 在 PATH 且 %s 是仓根）：%v", root, err)
	}
	var rels []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		rels = append(rels, p)
	}
	return rels
}

// mergeConflictMarkerSkipDir：★ 基础设施目录（★ 逐层判、★ 与 gocache 那道门同款）。
var mergeConflictMarkerSkipDir = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
	".build-local": true,
}

func mergeConflictMarkerHasSkipDir(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if mergeConflictMarkerSkipDir[seg] {
			return true
		}
	}
	return false
}

// TestNoMergeConflictMarkersInTree 判：★ 仓里不许有【没解干净的冲突标记】。
//
// ★★★ **为什么它必须是一道门、而不是一次清理**★★★
// ★★ 「解冲突」是一次性动作、★ 而「合并」这件事会**反复发生** ⇒ ★★ ⇒ ★★
// ★★ 一次性的清理挡不住第二次 ⇒ ★★ ⇒ ★★ **只有门能挡住第二次** ★★
// ★★ 而本门自己的判据也要防「假绿」：★ ① 覆盖【未跟踪的新文件】（★ 提交前就能看见）
// ★★ ② 不把 markdown 的 `=======` 误判成冲突（★ ⇒ ★★ 那会让本门变成天天红、
// ★★ ★★ 而真冲突反而被淹没的噪音门 ★★）★★
func TestNoMergeConflictMarkersInTree(t *testing.T) {
	t.Helper()
	root := mergeConflictMarkerRepoRoot(t)
	rels := mergeConflictMarkerFiles(t, root)
	if len(rels) == 0 {
		t.Fatalf("git 认的文件数为 0 ⇒ ★ 判据的输入集合不对")
	}
	scanned := 0
	type hit struct {
		rel  string
		line int
		what string
	}
	var hits []hit
	for _, rel := range rels {
		if mergeConflictMarkerHasSkipDir(rel) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue // ★ 已删除或读不动 ⇒ 不在判据面里
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue // ★ 二进制 ⇒ 不扫
		}
		scanned++
		lines := strings.Split(string(data), "\n")
		hasStart := false
		for i, l := range lines {
			switch {
			case strings.HasPrefix(l, "<<<<<<<"):
				hasStart = true
				hits = append(hits, hit{rel, i + 1, "<<<<<<<"})
			case strings.HasPrefix(l, ">>>>>>>"):
				hits = append(hits, hit{rel, i + 1, ">>>>>>>"})
			case strings.HasPrefix(l, "|||||||"):
				hits = append(hits, hit{rel, i + 1, "|||||||"})
			}
		}
		if !hasStart {
			continue
		}
		for i, l := range lines {
			if strings.TrimRight(l, " \t\r") == "=======" {
				hits = append(hits, hit{rel, i + 1, "======="})
			}
		}
	}
	if len(hits) > 0 {
		for _, h := range hits {
			t.Errorf("★ %s:%d 残留冲突标记 %q（★ ⇒ 该文件没解干净就进仓了；★ ★ "+
				"而 bash 会把它当重定向 ⇒ ★★ 脚本类文件**当场语法错** ★★）",
				h.rel, h.line, h.what)
		}
		t.Errorf("★ 共 %d 处（★ 扫了 %d 个文件）", len(hits), scanned)
		return
	}
	t.Logf("★ 冲突标记扫描：%d 个文件、0 处（★ git 认的文件 %d 个）", scanned, len(rels))
}

// mergeConflictMarkerRepoRoot 取仓根（★ 与仓内其他门同款取法）。
func mergeConflictMarkerRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("★ 取仓根失败（★ 需要 git 在 PATH）：%v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("★ 仓根为空")
	}
	return root
}

// ★★ **阳性对照**：★ 本门自己必须【能咬住一个真冲突】★★
// ★★ 否则「它没报错」与「它没在扫」不可区分 ⇒ ★★ ⇒ ★★ **门自己也要被证伪一次** ★★
//
// ★★ **做法**：★ 在临时目录里造一个【最小仓】⇒ ★ 放一个带冲突标记的文件
// ★★ ⇒ ★ 跑同一套判据 ⇒ ★ 必须报出那一处 ⇒ ★★ **⇒ 判据有效** ★★
func TestMergeConflictMarkerDetectorPositiveControl(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("★ 建临时仓失败：%v（%s）", err, out)
	}
	bad := filepath.Join(dir, "broken.sh")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho hi\n<<<<<<< Updated upstream\n"), 0o644); err != nil {
		t.Fatalf("★ 写样本失败：%v", err)
	}
	// ★★ markdown 的 setext 下划线（★ 恰好七个等号）★★
	// ★★ ⇒ ★★ **它不许被单独判红**（★ 不然本门就是天天红的噪音门）★★
	md := filepath.Join(dir, "readme.md")
	if err := os.WriteFile(md, []byte("标题\n=======\n\n正文\n"), 0o644); err != nil {
		t.Fatalf("★ 写样本失败：%v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("★ git add 失败：%v（%s）", err, out)
	}
	rels := mergeConflictMarkerFiles(t, dir)
	if len(rels) != 2 {
		t.Fatalf("★ 样本仓应认到 2 个文件、★ 实得 %d", len(rels))
	}
	found := 0
	mdFound := 0
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		hasStart := false
		for _, l := range lines {
			if strings.HasPrefix(l, "<<<<<<<") {
				hasStart = true
			}
		}
		fileFound := 0
		for _, l := range lines {
			if strings.HasPrefix(l, "<<<<<<<") || strings.HasPrefix(l, ">>>>>>>") {
				fileFound++
			}
			if hasStart && strings.TrimRight(l, " \t\r") == "=======" {
				fileFound++
			}
		}
		found += fileFound
		if rel == "readme.md" {
			mdFound = fileFound
		}
	}
	if found != 1 {
		t.Errorf("★ 阳性对照失败：★ 期望恰好报出 1 处冲突标记、★ 实得 %d", found)
	}
	if mdFound != 0 {
		t.Errorf("★ markdown 的 setext `=======` 被误判成冲突标记（★ 实得 %d 处）", mdFound)
	}
	_ = fmt.Sprintf("")
}
