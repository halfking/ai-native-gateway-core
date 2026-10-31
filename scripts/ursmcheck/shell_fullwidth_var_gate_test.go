package ursm_test

// shell_fullwidth_var_gate_test.go — 2026-10-24 R147 轮（12h 审计）——
//
// # 这道门要证明的事：scripts/guards/check-shell-fullwidth-var.sh（**shell 门**）
//
//	① **自己会咬**：真站点（「裸 $VAR 紧跟非 ASCII 字节」）⇒ 退出码 1；
//	② **不乱咬**：安全形态 ⇒ 退出码 0；
//	③ **与 Go 门判据一致**：同一个合成语料喂两门 ⇒ 命中集合**逐行相同**。
//
// # 为什么要有这道门（★ 起因是「origin/main 上 `make guards` 就是红的」）
//
// 2026-10-24 12h 审计轮实测：shell 门在 **pristine origin/main 上退出码 = 1**
//   （报 5 处：scripts/mut-nginx-realip-patch.sh:47/56
//     + tests/nginx_llm_realip_patch_test.sh:143/144/147）。
// ⇒ 那 5 处**一处真站点都没有**（逐条用真实 bash 实测）：
//   · 2 处是**尾随注释**（`#` 在词首 ⇒ 后面整段不参与展开）；
//   · 3 处是**转义美元**（`"\$remote_addr（…）"` ⇒ `\$` 是字面 `$`）。
// ⇒ ⇒ 门**红得没有道理** ⇒ 危害不是「漏放缺陷」，而是
//   「**假红 ⇒ 噪音 ⇒ 被忽略 ⇒ 真红也看不见**」。
// ⇒ ⇒ 根因：**判据漂移** —— Go 门在 3c9e42d41（10-23 07:21）升级了扫描器
//   （加了尾随注释与 `\$` 两条口径），shell 门最后一次改是 aaa52091d（10-22 12:55）
//   ⇒ 两门不再是同一条判据。
// ⇒ ⇒ 修法：**对齐判据**（改门不改那 5 处代码 —— 那 5 处的写法是对的，
//   改代码会破坏断言语义），并**用这道 Go 门把「对齐」钉成可执行断言**。
//
// # 为什么不能只靠 shell 门自己的自检
//
// shell 门里确实有一张自检矩阵（11 条形态 × 期望命中/不命中），
// 但它**只存在于那个脚本里** ⇒ 谁都可以改它、改期望值、把它删掉
// ⇒ 「自证」不受任何外部约束。
// ⇒ 本门在**包外**再做一次同样的事：合成语料 ⇒ 两门都跑 ⇒ 比对结果。
// ⇒ ⇒ 于是「两门同源」从**文档里的一句话**变成**一条会红的断言**。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const shellFullwidthGatePath = "scripts/guards/check-shell-fullwidth-var.sh"

// shellGateRepoRoot 返回仓根（本包位于 scripts/ursmcheck/ ⇒ 上溯两层）。
func shellGateRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析仓根失败: %v", err)
	}
	return root
}

// cjkGateSyntaxCase 是「一条语法形态 + 期望是否命中」。
//
// ★ 每条期望值**不是拍脑袋写的** ⇒ 全部用真实 bash（LC_ALL=en_US.UTF-8、
//
//	`set -u`、变量先定义 v=X）实测过：只观察「变量名是否被那个非 ASCII 字节吞掉」
//	（签名 = 报错文案里出现该字节）。
type cjkGateSyntaxCase struct {
	name    string
	line    string
	wantHit bool
}

var cjkGateSyntaxCases = []cjkGateSyntaxCase{
	// ★ 真站点（必须命中）
	{"bare_var_cjk", `echo "X$v中Y"`, true},
	{"fullwidth_rparen", `echo "$v）"`, true},
	{"fullwidth_lparen", `echo "$v（"`, true},
	// ★ `#` 不在词首 ⇒ 不是注释 ⇒ 仍然是真站点
	{"hash_not_word_start", `echo a#b "$v中"`, true},
	// ★ `\\` ⇒ 一个字面反斜杠 ⇒ `$` 仍然展开 ⇒ 仍然是真站点
	{"even_backslash_expands", `echo "X\\$v中Y"`, true},
	// ★ 安全形态（必须放过）
	{"braced_var", `echo "X${v}中Y"`, false},
	{"escaped_dollar", `echo "X\$v中Y"`, false},
	{"whole_line_comment", `# 注释 $v中`, false},
	{"trailing_comment", `echo "X$v" # 尾随注释 $v中`, false},
	{"single_quoted", `echo '$v中'`, false},
	{"plain_ascii", `echo "X$v"`, false},
}

// newSyntheticShellRepo 造一个**一次性 git 仓**给 shell 门扫。
//
// ★ 为什么必须造真 git 仓：shell 门用 `git ls-files` / `git ls-files --others`
//
//	枚举扫描面 ⇒ 没有 git 就没法验证「已跟踪 ∪ 未跟踪」这条口径。
//
// ★ 为什么必须塞 >= 10 个 .sh：shell 门有一道「枚举面自检」
//
//	（只枚举到 < 10 个 .sh ⇒ ABORT exit 2，判「仓库根很可能算错了」）
//	⇒ 不满足的话门会在扫描前就退出，测试就测不到真正的东西了。
func newSyntheticShellRepo(t *testing.T, tracked, untracked map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("执行 %v 失败: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")

	// ① 门脚本本体（它自己就是一个 .sh，也算进枚举面）
	gateSrc := filepath.Join(shellGateRepoRoot(t), shellFullwidthGatePath)
	gateDst := filepath.Join(dir, shellFullwidthGatePath)
	if err := os.MkdirAll(filepath.Dir(gateDst), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	data, err := os.ReadFile(gateSrc)
	if err != nil {
		t.Fatalf("读门脚本失败: %v", err)
	}
	if err := os.WriteFile(gateDst, data, 0o755); err != nil {
		t.Fatalf("写门脚本失败: %v", err)
	}

	// ② 补足枚举面自检要的 10 个 .sh
	for i := 0; i < 10; i++ {
		name := filepath.Join("scripts", "filler", "filler"+strconv.Itoa(i)+".sh")
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\necho filler\n"), 0o755); err != nil {
			t.Fatalf("写填充文件失败: %v", err)
		}
	}

	// ③ 场景文件
	for name, content := range tracked {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("写场景文件失败: %v", err)
		}
	}
	for name, content := range untracked {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("写场景文件失败: %v", err)
		}
	}

	// ④ 已跟踪的入库（未跟踪的**故意**不入库 —— 那条口径也要验）
	run("git", "add", "-A")
	return dir
}

// runShellGate 在指定仓里跑 shell 门，返回退出码与 stdout。
func runShellGate(t *testing.T, dir string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(dir, shellFullwidthGatePath))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if ok := asExitError(err, &ee); ok {
		return ee.ExitCode(), string(out)
	}
	t.Fatalf("跑 shell 门失败: %v\n%s", err, out)
	return -1, ""
}

func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

// hitLineRe 解析 shell 门输出里的命中行（形如 `  path:lineno`）。
var hitLineRe = regexp.MustCompile(`(?m)^\s{2}(\S+):(\d+)\s*$`)

// shellGateHits 取出（相对路径, 行号）集合。
func shellGateHits(out string) map[string][]int {
	hits := map[string][]int{}
	for _, m := range hitLineRe.FindAllStringSubmatch(out, -1) {
		n, _ := strconv.Atoi(m[2])
		hits[m[1]] = append(hits[m[1]], n)
	}
	for k := range hits {
		sort.Ints(hits[k])
	}
	return hits
}

// TestShellFullwidthVarGateHasTeeth 证明 shell 门**会咬**且**不乱咬**。
//
// ★ 断言是「集合相等」而不是「数量相等」：
//
//	数量相等时，「漏报 A + 多报 B」可以互相抵消 ⇒ 门仍有洞。
func TestShellFullwidthVarGateHasTeeth(t *testing.T) {
	// 语料：每条形态一行，行号即 case 序号（+1，因为第 1 行是 shebang）。
	var body strings.Builder
	body.WriteString("#!/usr/bin/env bash\n")
	wantHitLines := []int{}
	wantSafeLines := []int{}
	for i, c := range cjkGateSyntaxCases {
		body.WriteString(c.line + "\n")
		if c.wantHit {
			wantHitLines = append(wantHitLines, i+2)
		} else {
			wantSafeLines = append(wantSafeLines, i+2)
		}
	}

	// ① 全是安全形态 ⇒ 门必须绿
	safeOnly := strings.Builder{}
	safeOnly.WriteString("#!/usr/bin/env bash\n")
	for _, c := range cjkGateSyntaxCases {
		if !c.wantHit {
			safeOnly.WriteString(c.line + "\n")
		}
	}
	dir := newSyntheticShellRepo(t, map[string]string{
		"scripts/safe_only.sh": safeOnly.String(),
	}, nil)
	rc, out := runShellGate(t, dir)
	if rc != 0 {
		t.Fatalf("★ 安全形态语料下 shell 门红了（rc=%d）⇒ 判据过宽（乱咬）\n%s", rc, out)
	}
	if hits := shellGateHits(out); len(hits) != 0 {
		t.Fatalf("★ 安全形态语料下门报了命中 %v ⇒ 判据过宽\n%s", hits, out)
	}

	// ② 混合语料（真站点 + 安全形态）⇒ 门必须红，且**只**报真站点那一行
	dir = newSyntheticShellRepo(t, map[string]string{
		"scripts/mixed.sh": body.String(),
	}, nil)
	rc, out = runShellGate(t, dir)
	if rc != 1 {
		t.Fatalf("★ 含真站点的语料下 shell 门没红（rc=%d）⇒ 门没牙\n%s", rc, out)
	}
	hits := shellGateHits(out)
	got := hits["scripts/mixed.sh"]
	if !equalInts(got, wantHitLines) {
		t.Fatalf("★ 命中行集合不对：\n  期望 %v\n  实得 %v\n完整输出：\n%s",
			wantHitLines, got, out)
	}
	for _, ln := range wantSafeLines {
		for _, g := range got {
			if g == ln {
				t.Fatalf("★ 安全形态被误报（第 %d 行）⇒ 判据过宽\n完整输出：\n%s", ln, out)
			}
		}
	}
}

// TestShellFullwidthVarGateMatchesGoGate 证明**两门判据一致**。
//
// ★ 直接调用 Go 门的扫描器（scanCJKVarInText，同包可见），
//
//	把同一个合成语料喂给 shell 门 ⇒ 比对命中行号集合。
//
// ⇒ ⇒ 「两门同源」从文档里的一句话变成**一条会红的断言**。
func TestShellFullwidthVarGateMatchesGoGate(t *testing.T) {
	var body strings.Builder
	body.WriteString("#!/usr/bin/env bash\n")
	for _, c := range cjkGateSyntaxCases {
		body.WriteString(c.line + "\n")
	}
	content := body.String()

	// Go 门侧：逐行扫，记录命中的行号
	goLines := map[int]bool{}
	for i, line := range strings.Split(content, "\n") {
		if len(scanCJKVarInText(line)) > 0 {
			goLines[i+1] = true
		}
	}

	// shell 门侧：跑真实的脚本
	dir := newSyntheticShellRepo(t, map[string]string{
		"scripts/cmp.sh": content,
	}, nil)
	rc, out := runShellGate(t, dir)
	if rc != 1 {
		t.Fatalf("★ 含真站点的语料下 shell 门没红（rc=%d）\n%s", rc, out)
	}
	shellLines := map[int]bool{}
	for _, n := range shellGateHits(out)["scripts/cmp.sh"] {
		shellLines[n] = true
	}

	if !equalBoolSets(goLines, shellLines) {
		t.Fatalf("★ 两门判据不一致：\n  Go 门命中行 %v\n  shell 门命中行 %v\n完整输出：\n%s",
			sortedKeys(goLines), sortedKeys(shellLines), out)
	}
	// ★ 并且两门都必须命中「预期的那几条」，否则「一致」可能一致在「都没报」
	if len(shellLines) == 0 {
		t.Fatalf("★ 两门都没报 ⇒ 一致性是假的（语料失效）\n%s", out)
	}
}

// TestShellFullwidthVarGateSeesUntrackedFiles 验「已跟踪 ∪ 未跟踪」这条口径。
//
// ★ 这条口径是 shell 门**故意**比 Go 门宽的（Go 门只扫 git 追踪的文件，
//
//	因为它要拿读数对棘轮登记册；本门没有登记册 ⇒ 宽面不引入不确定性）。
//
// ⇒ ⇒ 门必须看得见**还没 git add** 的脚本 —— 那恰恰是风险最高的窗口。
func TestShellFullwidthVarGateSeesUntrackedFiles(t *testing.T) {
	dir := newSyntheticShellRepo(t, map[string]string{
		"scripts/tracked.sh": "#!/usr/bin/env bash\necho tracked\n",
	}, map[string]string{
		"scripts/untracked_new.sh": "#!/usr/bin/env bash\necho \"X$v中Y\"\n",
	})
	rc, out := runShellGate(t, dir)
	if rc != 1 {
		t.Fatalf("★ 未跟踪脚本里的真站点没被抓到（rc=%d）⇒ 扫描面退化了\n%s", rc, out)
	}
	hits := shellGateHits(out)
	if len(hits["scripts/untracked_new.sh"]) != 1 {
		t.Fatalf("★ 未跟踪脚本的命中行不对：%v\n%s", hits, out)
	}
	if len(hits["scripts/tracked.sh"]) != 0 {
		t.Fatalf("★ 已跟踪的干净脚本被误报：%v\n%s", hits, out)
	}
}

// TestShellFullwidthVarGateIsWired 验「门还在、还接在线上」。
//
// ★ 为什么把这条从「真实仓全扫」换成「接线断言」：
//
//	真实仓全扫要 8.7s，而 `make guards-ursmcheck` 的整体预算在**当时**只剩 ~60s
//	（★ 实测 840.111s / timeout 900s ⇒ ★ 这是 2026-10-28 r1044 轮把预算提到
//	★ 1800s **之前**的数）⇒ ★ 花掉 15% 的余量去重复
//	`make guards-shell` 已经做过的事（它跑的就是同一个脚本、同一个仓）
//	⇒ 性价比不对。
//	★ r1044 轮实测该包 **1011.711s**（load 26.8/30.2/36.4 ⇒ 上界）⇒ ★ 提预算后
//	★ 余量更宽 ⇒ ★★ 但「重复全扫的性价比不对」这个**结论不变**
//	★ （★ 它与预算无关，★ 取决于「同一件事被做两遍」本身）。
//	★★★ 2026-10-30 r1050 轮回填当前实测：该包 **1122.817s**（load 6.32/5.12/3.47
//	★ ⇒ ★ 负载远低于 1011.711s 那次却更慢 ⇒ ★★ **不是负载上界，是包真的变重了**）
//	★ ⇒ 余量 **1800/1122.817 ≈ 1.60x**（★ 仍够用，但 ★ 已非「宽松」）
//	★ ⇒ ★★ **「余量只剩 ~60s」是 2026-10-28 之前的历史读数，不是当前值**
//	★ ⇒ ★★ **注释里的余量不会自己失效** ⇒ 它会变成一句**指向相反结论的可信假话**
//	★ ⇒ ★★ 故本条在 r1050 轮显式补上当前读数与时点标注。
//
//	⇒ 而「**门被摘掉 / 接线被断开**」这个场景，用**零成本**的断言就能覆盖：
//	  ① 脚本文件还在；
//	  ② Makefile 的 guards-shell 目标还引用它；
//	  ③ CI workflow 还跑 `make guards`。
//	⇒ 「真实仓是干净的」由 `make guards` 的 guards-shell 阶段负责（不重复）。
func TestShellFullwidthVarGateIsWired(t *testing.T) {
	root := shellGateRepoRoot(t)

	// ① 门脚本还在（被摘掉 ⇒ 下面两个合成仓测试也会因读不到脚本而红）
	script := filepath.Join(root, shellFullwidthGatePath)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("★ shell 门不存在：%v（门被摘掉了 ⇒ 流程闭环破了）", err)
	}

	// ② Makefile 的 guards-shell 目标还引用它
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("读 Makefile 失败: %v", err)
	}
	if !strings.Contains(string(mk), shellFullwidthGatePath) {
		t.Fatalf("★ Makefile 里找不到 %s ⇒ guards-shell 目标被摘了", shellFullwidthGatePath)
	}
	if !strings.Contains(string(mk), "guards-shell") {
		t.Fatalf("★ Makefile 里找不到 guards-shell 目标 ⇒ 接线断了")
	}

	// ③ CI workflow 还跑 `make guards`
	wf := filepath.Join(root, ".github", "workflows", "audit-guards-ci.yml")
	wfData, err := os.ReadFile(wf)
	if err != nil {
		t.Fatalf("读 CI workflow 失败: %v", err)
	}
	if !strings.Contains(string(wfData), "make guards") {
		t.Fatalf("★ CI workflow 里找不到 `make guards` ⇒ 这道门在 CI 上不会被执行")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalBoolSets(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
