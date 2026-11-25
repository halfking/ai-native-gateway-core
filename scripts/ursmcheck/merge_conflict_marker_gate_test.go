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
// ★★★ **判据（四条、★ 逐条说清为什么这样切）★★★**
//
//	① `<<<<<<<` / `>>>>>>>` / `|||||||` 出现在**行首** ⇒ ★ **一律判红**
//	   （★ 这三种在正常文本里不会出现；★ 而 markdown 的 `=======` 会出现、★ 所以不单列它 ★）
//	② `=======`（★ 独占一行、★ 恰好七个等号）⇒ ★ **只有当同一个文件里也有 `<<<<<<<`
//	   时才判红**（★ ⇒ ★★ markdown 的 setext 标题下划线、★ README 里的分隔线都不会误报 ★★）
//	③ 作用域＝**git 认的文件**（★ `git ls-files --cached --others --exclude-standard`）
//	   ⇒ ★★ 与仓内既有门同口径：★ 未跟踪但没被忽略的文件（★ ＝ 即将提交的新文件）
//	   ★★ 也进作用域 ⇒ ★★ ⇒ ★★ **「提交前就能看见」的能力不丢** ★★
//	④ **markdown 里的【孤立分隔符】** ⇒ ★ **不看有没有 `<<<<<<<`、直接判红**
//	   （★ `.md` 文件里独占一行的 `=======`，★ 前一行**承载不了 setext 标题**
//	   ★★ ——空行／文件首行／ATX 标题／围栏／分隔线／只有引用标记的行——
//	   ★★ 且不在围栏内 ⇒ ★★ 它渲染出来就是一行字面的等号、★ 不可能是标题下划线 ★★）
//
// ★★★ **判据④是被第二次真实事故逼出来的（★ 2026-11-25 本会话系列第 29 轮）★★★**
//
//	★★ **事故**：★ 合并提交 `29b3dfbd5`（★ `AGENTS.md` 顶部两条车道撞号的解算）把两端的
//	★★ `<<<<<<<` / `>>>>>>>` 都删了、★ **唯独中间那行 `=======` 留在了主档第 9 行**
//	★★ ⇒ ★★ 判据②的前提（「同文件也有 `<<<<<<<`」）已经不成立 ⇒ ★★ **放行**
//	★★ ⇒ ★★ 全量 `ursmcheck` 绿着把它推上了两个远端 ⇒ ★★ **没有任何门会红** ★★
//	★★ **量过的边界**：★ 判据④落地前，全仓 git 认的文件里独占一行的 `=======`
//	★★ 恰好 1 处、★ 就是这一处 ⇒ ★★ 判据④在真实树上零误报 ★★
//	★★ **它覆盖不到的形态（诚实登记）**：★ 前一行**恰好是正文行**的孤立 `=======`
//	★★ 仍只受判据②管；★ 非 markdown 文件（日志、纯文本里的分隔线）不受判据④影响 ★★
//
// ★★ **内容读的是工作树**（★ 而文件集读的是 git）⇒ ★★ ⇒ ★★ **本地改了就能验、
// ★★ 不用先提交** ⇒ ★★ ★★ 而 `git grep --cached` 读的是【索引】⇒ ★★ 那个口径下
// ★★ 「刚改好、还没 stage」的修法**看不见** ⇒ ★★ ⇒ ★★ **本门刻意不用 `git grep` ** ★★

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
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

// mergeConflictMarkerFenceRE：★ 围栏行（★ 最多缩进 3 格；开栏与闭栏同形 ⇒ 逐行翻转）。
var mergeConflictMarkerFenceRE = regexp.MustCompile(`^\s{0,3}(` + "`{3,}" + `|~{3,})`)

// mergeConflictMarkerATXRE：★ ATX 标题行（★ 其后不能接 setext 下划线）。
var mergeConflictMarkerATXRE = regexp.MustCompile(`^\s{0,3}#{1,6}(\s|$)`)

// mergeConflictMarkerThematicBreakRE：★ 分隔线（`---`／`***`／`___`；★ 同一字符至少三个、
// 中间可夹空格；★ RE2 不支持反向引用 ⇒ 三个分支各写一遍）。
var mergeConflictMarkerThematicBreakRE = regexp.MustCompile(`^\s{0,3}(?:(?:-[\t ]*){3,}|(?:_[\t ]*){3,}|(?:\*[\t ]*){3,})$`)

// mergeConflictMarkerQuoteOnlyRE：★ 只有引用标记的行（`>`）。
var mergeConflictMarkerQuoteOnlyRE = regexp.MustCompile(`^\s{0,3}>\s*$`)

// mergeConflictMarkerIsMarkdown：★ 判据④只对 markdown 生效。
func mergeConflictMarkerIsMarkdown(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// mergeConflictMarkerCantCarrySetext 报告前一行是否**承载不了** setext 标题下划线。
//
// prev 传空串表示「这一行是文件的第一行」。
func mergeConflictMarkerCantCarrySetext(prev string) bool {
	if strings.TrimSpace(prev) == "" {
		return true // ★ 空行（含文件首行）
	}
	return mergeConflictMarkerFenceRE.MatchString(prev) ||
		mergeConflictMarkerATXRE.MatchString(prev) ||
		mergeConflictMarkerThematicBreakRE.MatchString(prev) ||
		mergeConflictMarkerQuoteOnlyRE.MatchString(prev)
}

// mergeConflictMarkerHit 一处残留冲突标记（★ Line 从 1 起）。
type mergeConflictMarkerHit struct {
	Line int
	What string
	// Orphan 标出这一处是判据④的【孤立分隔符】（★ 不是靠 `<<<<<<<` 认出来的）。
	Orphan bool
}

// mergeConflictMarkerScan 返回一个文本文件里的残留冲突标记。
//
// ★★ **真门与阳性对照共用这一份判据**（★ 2026-11-25 之前对照测试里是内联的另一份 ⇒
// ★★ 「同一套判据」只是口头上的 ⇒ ★★ 现在两边调同一个函数 ★★）。
func mergeConflictMarkerScan(data []byte, isMarkdown bool) []mergeConflictMarkerHit {
	lines := strings.Split(string(data), "\n")
	var hits []mergeConflictMarkerHit
	hasStart := false
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "<<<<<<<"):
			hasStart = true
			hits = append(hits, mergeConflictMarkerHit{Line: i + 1, What: "<<<<<<<"})
		case strings.HasPrefix(l, ">>>>>>>"):
			hits = append(hits, mergeConflictMarkerHit{Line: i + 1, What: ">>>>>>>"})
		case strings.HasPrefix(l, "|||||||"):
			hits = append(hits, mergeConflictMarkerHit{Line: i + 1, What: "|||||||"})
		}
	}
	if !hasStart && !isMarkdown {
		return hits
	}
	inFence := false
	for i, l := range lines {
		if mergeConflictMarkerFenceRE.MatchString(l) {
			inFence = !inFence
		}
		if strings.TrimRight(l, " \t\r") != "=======" {
			continue
		}
		if hasStart { // ★ 判据②
			hits = append(hits, mergeConflictMarkerHit{Line: i + 1, What: "======="})
			continue
		}
		prev := "" // ★ 文件首行 ⇒ 没有前一行
		if i > 0 {
			prev = strings.TrimRight(lines[i-1], "\r") // ★ CRLF 文件：去掉行尾的 CR 再判
		}
		if !inFence && mergeConflictMarkerCantCarrySetext(prev) { // ★ 判据④
			hits = append(hits, mergeConflictMarkerHit{Line: i + 1, What: "=======", Orphan: true})
		}
	}
	// ★ 判据①与判据②④分两遍扫 ⇒ 按行号排回去（★ 报错行按文件顺序出现，对照臂也按文件顺序写期望）。
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Line < hits[j].Line })
	return hits
}

// mergeConflictMarkerScanFile 按「相对仓根的路径」扫一个文件。
//
// 第二个返回值报告这个文件**是否进了判据面**（★ 已删除／读不动／二进制 ⇒ false）。
func mergeConflictMarkerScanFile(root, rel string) ([]mergeConflictMarkerHit, bool) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil, false // ★ 已删除或读不动 ⇒ 不在判据面里
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, false // ★ 二进制 ⇒ 不扫
	}
	return mergeConflictMarkerScan(data, mergeConflictMarkerIsMarkdown(rel)), true
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
		rel    string
		line   int
		what   string
		orphan bool
	}
	var hits []hit
	for _, rel := range rels {
		if mergeConflictMarkerHasSkipDir(rel) {
			continue
		}
		fileHits, inScope := mergeConflictMarkerScanFile(root, rel)
		if !inScope {
			continue // ★ 已删除／读不动／二进制 ⇒ 不在判据面里
		}
		scanned++
		for _, h := range fileHits {
			hits = append(hits, hit{rel, h.Line, h.What, h.Orphan})
		}
	}
	for _, h := range hits {
		if h.orphan {
			t.Errorf("★ %s:%d 孤立分隔符 %q（★ 前一行承载不了 setext 标题 ⇒ ★ 它不是 markdown 的标题下划线 ⇒ "+
				"★ ★ 多半是解冲突时删了两端、漏了中间 ⇒ ★★ 取证：比对该文件这次合并的两个父提交里有没有这一行）",
				h.rel, h.line, h.what)
			continue
		}
		t.Errorf("★ %s:%d 残留冲突标记 %q（★ ⇒ 该文件没解干净就进仓了；★ ★ "+
			"而 bash 会把它当重定向 ⇒ ★★ 脚本类文件**当场语法错** ★★）",
			h.rel, h.line, h.what)
	}
	if len(hits) > 0 {
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
// ★★ **做法**：★ 每一臂都是一段最小样本 ⇒ ★ 跑**真门用的同一个** mergeConflictMarkerScan
// ★★ ⇒ ★ 逐处比对行号与形态 ⇒ ★★ **⇒ 判据有效** ★★
func TestMergeConflictMarkerDetectorPositiveControl(t *testing.T) {
	t.Helper()
	cases := []struct {
		name    string
		file    string // ★ 扩展名决定判据④是否生效
		content string
		want    []mergeConflictMarkerHit
	}{
		{
			name: "A 起始标记（行首 ⇒ 一律红）",
			file: "broken.sh", content: "#!/bin/sh\necho hi\n<<<<<<< Updated upstream\n",
			want: []mergeConflictMarkerHit{{Line: 3, What: "<<<<<<<"}},
		},
		{
			// ★★ 它不许被单独判红（★ 不然本门就是天天红的噪音门）★★
			name: "B markdown 的 setext 下划线（恰好七个等号）",
			file: "readme.md", content: "标题\n=======\n\n正文\n",
			want: nil,
		},
		{
			// ★★ 2026-11-25 的真实事故形态：两端的标记都删了、只有中间那行留了下来 ★★
			name: "C 孤立分隔符（前一行是空行、没有 `<<<<<<<`）",
			file: "handover.md", content: "> ★ 甲块\n\n=======\n> ★ 乙块\n",
			want: []mergeConflictMarkerHit{{Line: 3, What: "=======", Orphan: true}},
		},
		{
			name: "D 文件首行就是分隔符",
			file: "top.md", content: "=======\n正文\n",
			want: []mergeConflictMarkerHit{{Line: 1, What: "=======", Orphan: true}},
		},
		{
			name: "E 围栏内的是引文（不在判据面里）",
			file: "doc.md", content: "说明\n\n```text\n=======\n```\n",
			want: nil,
		},
		{
			name: "F 非 markdown 沿用判据②（没有 `<<<<<<<` ⇒ 不报）",
			file: "notes.txt", content: "甲\n\n=======\n乙\n",
			want: nil,
		},
		{
			name: "G 完整冲突（三个标记逐处报出、分隔符不算孤立）",
			file: "conflict.md", content: "<<<<<<< ours\n甲\n=======\n乙\n>>>>>>> theirs\n",
			want: []mergeConflictMarkerHit{
				{Line: 1, What: "<<<<<<<"},
				{Line: 3, What: "======="},
				{Line: 5, What: ">>>>>>>"},
			},
		},
	}
	for _, tc := range cases {
		got := mergeConflictMarkerScan([]byte(tc.content), mergeConflictMarkerIsMarkdown(tc.file))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("★ 阳性对照失败【%s】：★ 期望 %v、★ 实得 %v", tc.name, tc.want, got)
		}
	}

	// ★★ 文件集口径自检：真门的输入集合必须【包含未跟踪但没被忽略的新文件】★★
	// ★★ （★ 那正是「提交前就能看见」的全部意义 ★★）。
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("★ 建临时仓失败：%v（%s）", err, out)
	}
	for i, tc := range cases {
		if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.content), 0o644); err != nil {
			t.Fatalf("★ 写样本失败：%v", err)
		}
		if i < 2 { // ★ 只 stage 前两个 ⇒ 其余保持「未跟踪、未被忽略」
			if out, err := exec.Command("git", "-C", dir, "add", tc.file).CombinedOutput(); err != nil {
				t.Fatalf("★ git add 失败：%v（%s）", err, out)
			}
		}
	}
	rels := mergeConflictMarkerFiles(t, dir)
	if len(rels) != len(cases) {
		t.Fatalf("★ 样本仓应认到 %d 个文件（含未跟踪的）、★ 实得 %d：%v", len(cases), len(rels), rels)
	}
	for _, tc := range cases {
		got, inScope := mergeConflictMarkerScanFile(dir, tc.file)
		if !inScope || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("★ 按路径扫描与按内容扫描结论不一致【%s】：★ 期望进判据面且为 %v、★ 实得 inScope=%v %v",
				tc.name, tc.want, inScope, got)
		}
	}
	// ★★ 读不动／二进制的文件必须落在判据面**之外**（★ 不然「扫了 N 个文件」这个数就是假的）★★
	if _, inScope := mergeConflictMarkerScanFile(dir, "does-not-exist.md"); inScope {
		t.Errorf("★ 不存在的文件被判成进了判据面")
	}
	bin := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(bin, []byte("a\x00\n=======\n"), 0o644); err != nil {
		t.Fatalf("★ 写二进制样本失败：%v", err)
	}
	if _, inScope := mergeConflictMarkerScanFile(dir, "blob.bin"); inScope {
		t.Errorf("★ 含 NUL 的二进制文件被判成进了判据面")
	}
}
