package ursm_test

// 「合并残留 ＋ 台账编号重号」的**形状门**（2026-11-24）
//
// ★★★ **这道门钉的是什么（★ 本轮实测到的一起真事故）★★★：
//
//　★ 2026-11-24，★ 我方做**第 5 次并上游**（★ `836ae5001` ＝「看板真源统一」★）★★
//　★ 合并**零冲突** ⇒ ★★ **但事后全仓扫冲突标记时、★ 在一份【已在仓内多轮的审计报告】里
//　★ 扫出 3 行字面冲突标记**（★ `<<<<<<< HEAD` / `=======` / `>>>>>>> origin/main` ★）★★
//　★ ⇒ ★★★ **它是被【更早的一轮合并】写进仓的**（★ 引入提交＝`18e6aab5b`、★ 「合并轮十九」★★
//　★ ⇒ ★★ **而那一轮自己报的是「零冲突」** ⇒ ★★ ⇒ ★★ **★ 「合并报零冲突」不等于「合并没留残迹」★★**
//　★ ⇒ ★★★ **★ 而全仓【当时是有现成扫描脚本的】**（★ `scripts/task-stop-audit.sh` 第 63 行
//　★ 就是 `rg -n '^(<<<<<<<|>>>>>>>)'` ★）★★ ｜★★ ★★ **但它是 shell 脚本 ⇒ 不在闸门的
//　★ 选包口径内（★ 选包只认 Go 包 ★）⇒ ★★ **⇒ 这道防线从来没有在闸门上生效过** ★★。
//
// ★★★ **所以这道门的作用是：★★
//
//　★ ① **把已有的扫描能力接进闸门链**（★ 从「散文 ＋ 孤儿脚本」变成「每次闸门都跑」★★）；
//　★ ② **外加一条【台账编号唯一性】判据**（★ 同一份表里同一个编号出现两次 ★）——
//　★ 　 ★★ 因为那一份报告的登记表**同时被两辆车维护**（★ 我方 ＋ 上游 ★）★★
//　★ 　 ★★ ⇒ ★★ **两车各自往后追加 ⇒ 编号空间撞车 ⇒ 表里出现两行同号** ★★
//　★ 　 ★★ 而这类撞号**没有任何门看得见**（★ 上游那一份当时是【零重号】的 ★★）。
//
// ★★★ **判据（★ 刻意做得很窄、★ 免得误伤）★★★：
//
//　★ **① 冲突标记**：★ **同一个文件内同时出现【行首】`<<<<<<<` 与【行首】`>>>>>>>`** ⇒ 判红 ★
//　★ 　 ★★ **为什么要求「同时」**：★ git 的冲突块**必然成对**（★ start + end ★）★★
//　★ 　 ★★ ⇒ ★★ **而 markdown 里的 `=======` 单独一行是【合法的 Setext 标题下划线】** ★★
//　★ 　 ★★ ⇒ ★★ **只判 `=======` 会误伤、★ 只判 start 或只判 end 也会漏** ★★
//　★ 　 ★★ ⇒ ★★ **判据＝「同一文件里 start 与 end 同时出现」⇒ 既不误伤、也不漏** ★★；
//　★ 　 ★★ **为什么要求【行首】**：★ 仓内**故意**写下冲突标记示例的地方（★
//　★ 　 ★★ `scripts/ursmcheck/process_record_guard_test.go` ／★
//　★ 　 ★★ `scripts/audit/verify-v1-retirement-red-gates.sh` ★）★★
//　★ 　 ★★ **标记都在【字符串字面量里、且不在行首】** ⇒ ★★ **行首判据天然排除它们** ★★
//　★ 　 ★★ ⇒ ★★ **不需要维护豁免清单**（★ 豁免清单＝恒绿的门 ★★）；
//　★ 　 ★★ **扫描面＝【已跟踪文件】**（★ `git ls-files` ★）⇒ ★★ **工作树里的临时文件不误伤** ★★。
//
//　★ **② 登记表编号唯一性**：★ `docs/audit/2026-11-20-会话分析系统前端实施报告.md` 的
//　★ 「遗留登记表」里 ★ **第一列（编号）不得出现两次** ★ ⇒ 判红 ★
//　★ 　 ★★ **为什么只钉这一份文件**：★ 它是**全仓唯一**一份「两车共编的编号登记表」★★
//　★ 　 ★★ ⇒ ★★ **泛化到「所有 md 的所有表」会误伤**（★ 很多表的第一列本来就允许重复 ★★）★★。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// conflictStartRE / conflictEndRE 匹配**行首**的 git 冲突标记。
//
// ★ ★ 只认行首（★ 允许前导空白 ★）：★ 仓内**故意**写下标记示例的地方都在字符串字面量里、
// ★ ★ 且不在行首 ⇒ ★★ 天然排除、★ 不需要豁免清单 ★★。
var (
	conflictStartRE = regexp.MustCompile(`^\s*<{7}(\s|$)`)
	conflictEndRE   = regexp.MustCompile(`^\s*>{7}(\s|$)`)
)

// mergeResidueSkipDirs 是扫描时跳过的目录（★ 第三方与构建产物 ★）。
var mergeResidueSkipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
}

// mergeResidueBinarySuffix 是按后缀直接跳过的二进制（★ 省得读进内存 ★）。
var mergeResidueBinarySuffix = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".ico": true, ".pdf": true, ".zip": true, ".gz": true, ".tar": true,
	".jar": true, ".so": true, ".dylib": true, ".dll": true, ".exe": true,
	".bin": true, ".woff": true, ".woff2": true, ".ttf": true, ".mp4": true,
	".mp3": true, ".wasm": true, ".pyc": true, ".class": true,
}

// registryDocPath 是「两车共编的遗留登记表」所在文件（★ 相对仓根 ★）。
const registryDocPath = "docs/audit/2026-11-20-会话分析系统前端实施报告.md"

// registryIDRE 匹配登记表行的第一列（★ 形如 `| L38 |` ★）。
var registryIDRE = regexp.MustCompile(`^\|\s*(L\d+)\s*\|`)

// TestNoTrackedFileCarriesConflictMarkers —— ★★ 承重断言：★★
//
// ★★ **已跟踪文件里不得残留 git 冲突标记**（★ start 与 end 同行成对出现 ★）。
func TestNoTrackedFileCarriesConflictMarkers(t *testing.T) {
	root := repoRootForMergeResidue(t)

	files, err := gitTrackedFiles(root)
	if err != nil {
		t.Fatalf("★ 取已跟踪文件列表失败: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("★ 已跟踪文件列表为空（★ 判据会变成恒绿 ⇒ 拒绝继续 ★）")
	}

	var offenders []string
	scanned := 0
	for _, rel := range files {
		if skipTrackedPath(rel) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		if len(b) == 0 || isBinaryBlob(b) {
			continue
		}
		scanned++
		starts, ends := []int{}, []int{}
		for i, line := range strings.Split(string(b), "\n") {
			if conflictStartRE.MatchString(line) {
				starts = append(starts, i+1)
			}
			if conflictEndRE.MatchString(line) {
				ends = append(ends, i+1)
			}
		}
		// ★ ★ 要求【成对】才报：★ 单独的 `=======` 是 markdown 的合法下划线 ★★
		if len(starts) == 0 || len(ends) == 0 {
			continue
		}
		offenders = append(offenders, fmt.Sprintf(
			"%s: ★★ 残留 git 冲突标记 ★★（★ start 行 %v ／★ end 行 %v ★）⇒ ★★ "+
				"★ 这是【某一次合并的解算没做完】留下的残迹（★ 当轮自己报的是「零冲突」★）★★ "+
				"★ ⇒ 请解算成【干净内容】再提交、★ 别把标记本身推上去 ★★",
			rel, starts, ends))
	}
	if len(offenders) > 0 {
		for _, o := range offenders {
			t.Errorf("★ %s", o)
		}
		return
	}
	// ★ ★ 只在【没有 offender】时才打成功日志：★ 否则【门红的那一轮】也会打出
	// ★ ★ 「零残留」这种与结论相反的字样（★ 上一轮踩过「门输出互相否证的结论」★★）
	t.Logf("★ 已扫描 %d 个已跟踪文本文件、★ 冲突标记零残留（★ 共 %d 个已跟踪文件 ★）",
		scanned, len(files))
}

// TestSharedRegistryIDsAreUnique —— ★★ 承重断言：★★
//
// ★★ **两车共编的遗留登记表里、★ 同一个编号不得出现两次** ★。
func TestSharedRegistryIDsAreUnique(t *testing.T) {
	root := repoRootForMergeResidue(t)
	path := filepath.Join(root, registryDocPath)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("★ 读登记表失败（★ %s ★）: %v", registryDocPath, err)
	}
	seen := map[string]int{}
	dup := map[string][]int{}
	rows := 0
	for i, line := range strings.Split(string(b), "\n") {
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
	if rows == 0 {
		t.Fatalf("★ 登记表里没扫到任何 `| Lxx |` 行（★ 判据会变成恒绿 ⇒ 拒绝继续 ★）")
	}
	for id, lines := range dup {
		t.Errorf("★ 登记表编号 %s 重号（★ 行 %v ★）⇒ ★★ 【两车共编同一张表】⇒ ★★ "+
			"★ 编号空间撞车时、★ 正确做法是【给新条目换一个空号】★ ★★ "+
			"★ 而**不是**把两行都留着（★ 上游那一份当时是零重号的 ★★）★★",
			id, lines)
	}
	if len(dup) == 0 {
		t.Logf("★ 登记表 %d 行、★ 编号全部唯一（★ 覆盖 L1…L77 ★）", rows)
	}
}

// TestConflictMarkerDetectorRecognisesSyntheticResidue —— ★★ 自证臂：★★
//
// ★★ **判据自身必须能命中**（★ 否则上面两道门就是恒绿 ★★）。
//
// ★ ★ 这里用**合成字符串**喂给同一套正则 ⇒ ★★ 不依赖仓内当前状态（★ 仓内干净时也照样红 ★★）。
func TestConflictMarkerDetectorRecognisesSyntheticResidue(t *testing.T) {
	synthetic := "正文一行\n<<<<<<< HEAD\n我方内容\n=======\n对方内容\n>>>>>>> origin/main\n正文末行\n"
	starts, ends := []int{}, []int{}
	for i, line := range strings.Split(synthetic, "\n") {
		if conflictStartRE.MatchString(line) {
			starts = append(starts, i+1)
		}
		if conflictEndRE.MatchString(line) {
			ends = append(ends, i+1)
		}
	}
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("★ 自证臂红：★ 合成样本应各命中 1 个 start/end，★ 实得 start=%v end=%v ★",
			starts, ends)
	}
	// ★ ★ 反向自证：★ markdown 的 Setext 下划线**不得**被判成冲突 ★★
	md := "标题\n=======\n正文\n"
	mdStarts, mdEnds := 0, 0
	for _, line := range strings.Split(md, "\n") {
		if conflictStartRE.MatchString(line) {
			mdStarts++
		}
		if conflictEndRE.MatchString(line) {
			mdEnds++
		}
	}
	if mdStarts != 0 || mdEnds != 0 {
		t.Fatalf("★ 自证臂红：★ markdown 的 `=======` 下划线被误判成冲突标记 ★")
	}
	// ★ ★ 反向自证：★ 字符串字面量里（★ 不在行首 ★）的标记不得被命中 ★★
	literal := "\t\t\"<<<<<<< HEAD\\n正文\\n=======\\n对方\\n>>>>>>> abc1234 (commit msg)\\n\",\n"
	litStarts, litEnds := 0, 0
	for _, line := range strings.Split(literal, "\n") {
		if conflictStartRE.MatchString(line) {
			litStarts++
		}
		if conflictEndRE.MatchString(line) {
			litEnds++
		}
	}
	if litStarts != 0 || litEnds != 0 {
		t.Fatalf("★ 自证臂红：★ 行内的标记示例被误判成冲突标记（★ 行首判据失效 ★）")
	}
}

// gitTrackedFiles 取仓内**已跟踪**文件（★ 相对仓根 ★）。
func gitTrackedFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	raw := strings.Split(string(out), "\x00")
	files := make([]string, 0, len(raw))
	for _, f := range raw {
		if f == "" {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

// skipTrackedPath 判断该路径是否跳过扫描。
func skipTrackedPath(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if mergeResidueSkipDirs[p] {
			return true
		}
	}
	if mergeResidueBinarySuffix[strings.ToLower(filepath.Ext(rel))] {
		return true
	}
	return false
}

// isBinaryBlob 判断字节串是否为二进制（★ 含 NUL 即判二进制 ★）。
func isBinaryBlob(b []byte) bool {
	limit := len(b)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

// repoRootForMergeResidue 定位仓根（★ 本门在 `scripts/ursmcheck` 下 ★）。
func repoRootForMergeResidue(t *testing.T) string {
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
