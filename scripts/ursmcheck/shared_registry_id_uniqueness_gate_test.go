package ursm_test

// 「两车共编的编号登记表」的**编号唯一性门**（2026-11-24）
//
// ★★★ **这道门钉的是什么（★ 本轮实测到的一起真事故）★★★：
//
//　★ 2026-11-24，★ 我方在 `docs/audit/2026-11-20-会话分析系统前端实施报告.md` 的
//　★ 「遗留登记表」里，★ **同一个编号出现了两次**（★ 6 组：★ `L32` / `L38` / `L39` /
//　★ `L40` / `L41` / `L72` ★）★★
//
//　★ ★★ **而那份表是【两车共编】的**（★ 我方 ＋ 上游各往后追加 ★）★★
//　★ ⇒ ★★★ **⇒ 两车各自往后追加 ⇒ 编号空间必然撞车 ⇒ ★★ 而【没有任何门看得见】★★
//　★ ⇒ ★★★ **★ ⇒ 「编号唯一」必须变成机器判据 ⇒ ★★ 靠人记着「下一个号是多少」＝迟早撞 ★★**
//
// ★★★ **★ 为什么【冲突标记那半】不在这道门里 ★★★：
//
//　★ 仓里**已经有**一道更严的门管冲突标记（★ `scripts/ursmcheck/merge_conflict_marker_gate_test.go`
//　★ ——★ 它由并行车道在合并轮二十一·续二立下 ★）★★ ｜★★ ★★ **它的判据比我这一版更强**：★★
//　★ ① **行首** `<<<<<<<` / `>>>>>>>` / `|||||||` **一律判红**（★ 我那版只判【start 与 end
//　★ 　 同时出现】、★ 且不含 `|||||||` ★）｜★★ ★★ ② 作用域＝`git ls-files --cached --others
//　★ 　 --exclude-standard`（★ ★ **含未跟踪但未忽略的新文件** ⇒ ★★ **提交前就能看见** ★★
//　★ 　 ｜★ 我那版只扫**已跟踪**文件 ★）｜★★ ★★ ③ 额外覆盖 diff3 的基线标记 ★★。
//　★ ⇒ ★★★ **⇒ 两车道各管一段、★ 不重复造门、★ 也不互相覆盖（★ 两条候选推 main 时不会
//　★ 　 撞同一个文件 ★★）★★** ★★。
//
// ★★★ **判据（★ 只管这一件事）★★★：
//
//　★ **`docs/audit/2026-11-20-会话分析系统前端实施报告.md` 的遗留登记表里、★ 编号列
//　★ 不得出现两次** ⇒ 判红 ★
//　★ ★★ **为什么只钉这一份文件**：★ 它是**全仓唯一**一份「两车共编的编号登记表」★★
//　★ ★★ ⇒ ★★ **泛化到「所有 md 的所有表」会误伤**（★ 很多表的第一列本来就允许重复 ★★）★★。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// registryDocPath 是「两车共编的遗留登记表」所在文件（★ 相对仓根 ★）。
const registryDocPath = "docs/audit/2026-11-20-会话分析系统前端实施报告.md"

// registryIDRE 匹配登记表行的第一列（★ 形如 `| L38 |` ★）。
var registryIDRE = regexp.MustCompile(`^\|\s*(L\d+)\s*\|`)

// registryIDConflicts 返回「编号 → 出现行号（★ 重复时含全部出现位置 ★）」。
//
// ★ ★ 抽成函数是**为了让自证臂能喂合成样本**：★ 判据本体与自证臂必须走【同一条代码路径】★
// ★ ★ ⇒ ★★ 否则「自证臂测的是另一套实现」＝ 假自证 ★★。
func registryIDConflicts(lines []string) (map[string][]int, int) {
	seen := map[string]int{}
	dup := map[string][]int{}
	rows := 0
	for i, line := range lines {
		m := registryIDRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows++
		id := m[1]
		if prev, ok := seen[id]; ok {
			dup[id] = append(dup[id], prev, i+1)
			continue
		}
		seen[id] = i + 1
	}
	return dup, rows
}

// TestSharedRegistryIDsAreUnique —— ★★ 承重断言：★★
//
// ★★ **两车共编的遗留登记表里、★ 同一个编号不得出现两次** ★。
func TestSharedRegistryIDsAreUnique(t *testing.T) {
	root := repoRootForRegistryID(t)
	path := filepath.Join(root, registryDocPath)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("★ 读登记表失败（★ %s ★）: %v", registryDocPath, err)
	}
	dup, rows := registryIDConflicts(strings.Split(string(b), "\n"))
	if rows == 0 {
		t.Fatalf("★ 登记表里没扫到任何 `| Lxx |` 行（★ 判据会变成恒绿 ⇒ 拒绝继续 ★）")
	}
	for id, lines := range dup {
		t.Errorf("★ 登记表编号 %s 重号（★ 行 %v ★）⇒ ★★ 【两车共编同一张表】⇒ ★★ "+
			"★ 编号空间撞车时、★ 正确做法是【给新条目换一个空号】★ ★★ "+
			"★ 而**不是**把两行都留着（★ 上游那一份在上游修掉冲突标记时、★ 这条仍然存在 ★★）★★",
			id, lines)
	}
	if len(dup) == 0 {
		t.Logf("★ 登记表 %d 行、★ 编号全部唯一", rows)
	}
}

// TestRegistryIDConflictDetectorRecognisesSyntheticDup —— ★★ 自证臂：★★
//
// ★★ **判据自身必须能命中**（★ 否则上面那道门就是恒绿 ★★）。
//
// ★ ★ 这里用**合成样本**喂同一套解析函数 ⇒ ★★ 不依赖仓内当前状态（★ 仓内干净时也照样红 ★★）。
func TestRegistryIDConflictDetectorRecognisesSyntheticDup(t *testing.T) {
	synthetic := []string{
		"| 编号 | 事项 |",
		"| L1 | 甲 |",
		"| L2 | 乙 |",
		"| L1 | 丙 |", // ★ ★ 与第 2 行同号 ⇒ 必须被抓出来 ★★
		"| L3 | 丁 |",
	}
	dup, rows := registryIDConflicts(synthetic)
	if rows != 4 {
		t.Fatalf("★ 自证臂红：★ 合成样本应扫到 4 行，★ 实得 %d 行", rows)
	}
	if len(dup) != 1 {
		t.Fatalf("★ 自证臂红：★ 合成样本应命中 1 组重号，★ 实得 %d 组（★ %v ★）", len(dup), dup)
	}
	if got := dup["L1"]; len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Fatalf("★ 自证臂红：★ `L1` 的重号位置应是 [2 4]（★ 合成样本第 2 行与第 4 行 ★），★ 实得 %v", got)
	}
	// ★ ★ 反向自证：★ 没有重号的样本必须**不报** ★ ★
	clean := []string{"| L1 | 甲 |", "| L2 | 乙 |", "| L3 | 丙 |"}
	dup2, _ := registryIDConflicts(clean)
	if len(dup2) != 0 {
		t.Fatalf("★ 自证臂红：★ 无重号的样本被误报成 %v", dup2)
	}
}

// repoRootForRegistryID 定位仓根（★ 本门在 `scripts/ursmcheck` 下 ★）。
func repoRootForRegistryID(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("★ 找不到仓根（★ go.mod ★）")
	return ""
}
