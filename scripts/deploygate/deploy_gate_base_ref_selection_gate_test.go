package deploygate_test

// deploy_gate_base_ref_selection_gate_test.go —— 门：部署闸门的**选包判据**
// 必须纳入「相对 BASE_REF（现役基线）的**全部**改动」，★ 而不只是
// 「最后一笔提交」的改动。
//
// # 这道门守什么（★ 2026-11-23 L9 修复轮实测出来的机制缺陷）
//
// `scripts/deploy-test-gate.sh` 原先的选包判据是：
//
//	git diff HEAD            （工作区）
//	git diff --cached        （暂存区）
//	git diff HEAD~1 HEAD     （★ **只看最后一笔提交**）
//
// ⇒ ★★ **BASE_REF 完全不参与选包**（★ 它只被用于「基线对照」那一段）。
//
// ⇒ ★★ 实测事故（211 seq 3236 → 3237 那一轮）：
//   · 本轮的生产改动在 `044349a2c`（★ 改了 admin/ 4 个生产文件 + 3 个钉测）；
//   · 但那笔提交**不是最后一笔** —— 后面还叠了并行会话的 ratelimit 改动；
//   ⇒ ⇒ 选包只选出 `./cmd/gateway ./ratelimit` ⇒ ★★ **`./admin` 压根没进闸门**；
//   ⇒ ⇒ 而闸门当时报的是「★ 闸门通过」★ ⇒ ⇒ **新引入的 admin 改动被静默放过**。
//
// ⇒ ★★ 根因不止是「漏了一路」：★ **选包口径与基线对照口径是两套**——
//   · 选包   ＝ `HEAD~1..HEAD`（最后一笔）
//   · 基线对照＝ `merge-base(HEAD, $BASE_REF)`（★ 基线树）
//   ⇒ ⇒ 选出来的包，★ **基线树可能根本没跑过** ⇒ ⇒ 「新红 / 旧红」**不可判定**
//   ⇒ ⇒ 新红会被当成既有红放行。
//
// ⇒ ★ 本门要求：**选包与基线对照共用同一个 merge-base**，
//   且选包清单**必须包含**「基线树 → 当前树」的差异。
//
// # 判据（四条）
//
// ① 【行为】★ 在**真 git 仓库**里跑（★ 不用 mock）：
//    造三笔提交——commit1 建 `admin/x.go`＋`admin/x_test.go`（★ 当基线），
//    commit2 改 `admin/x.go`（★ **这一笔碰了 admin**），
//    commit3 改 `ratelimit/y.go`（★ **最后一笔不碰 admin**）。
//    ⇒ `HEAD~1..HEAD`（旧判据）**选不出** `./admin`；
//    ⇒ 「基线树 → 当前树」**必须**选出 `./admin`。
// ② 【变异自证】★ 把第④路（基线 diff）**注入掉** ⇒ 同一场景必须选不出 `./admin`
//    ⇒ ⇒ 证明这条判据**有牙齿**（★ 不是恒绿、★ 也不是恒选）。
// ③ 【退化安全】★ `BASE_REF` 指向**不存在的 ref** ⇒ 不得崩、不得 unbound
//    ⇒ ★ 兜底（`HEAD~1..HEAD`）仍须可用。
// ④ 【口径统一】★ 基线对照那一段**必须**走同一个 `dd_gate_base_commit`
//    ⇒ 且脚本里**不得**再出现「直接调 `git merge-base HEAD "$BASE_REF"`」的旁路
//    ⇒ ⇒ 两套口径不可能再漂移。★ 对照臂：把基线对照改回旧形态 ⇒ 本门必须红。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gateScriptPath —— 闸门脚本（★ 与选包门同一条脚本）。
const gateScriptPath = "../../scripts/deploy-test-gate.sh"

// gateBaselineDiffLine —— ★ 选包第④路的那一行（★ 变异自证要精确命中它）。
//
// ★ 形态刻意挑成「整条命令」而不是「某个关键字」：★ 对照臂要能**精确**把这一路
//   摘掉、且摘掉后 bash 语法仍合法（★ 保留 if 壳、只把命令换成 `true`）。
const gateBaselineDiffLine = `git diff --name-only -z "$base_commit" HEAD 2>/dev/null || true`

// readGateScript —— 读闸门脚本全文。
func readGateScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(gateScriptPath)
	if err != nil {
		t.Fatalf("读闸门脚本 %s: %v", gateScriptPath, err)
	}
	return string(b)
}

// gateSelectorBundle —— 把选包判据需要的全部块**原样**抽出来，凑成一个可执行脚本。
//
// ★ 抽的块：守卫域映射表 + 守卫域函数 + 基线 commit 函数 + 改动清单函数 + 选包函数。
//   ★ 「原样抽出来」是本仓既有做法（★ 见 deploy_gate_guard_domain_selection_gate_test.go）
//   ⇒ ⇒ 测的就是**脚本里那段真代码**，不是复制品。
func gateSelectorBundle(t *testing.T) string {
	t.Helper()
	return extractBlock(t, "DD_GUARDED_DOMAINS=(", ")") + "\n" +
		extractBlock(t, "dd_guard_domain_packages() {", "}") + "\n" +
		extractBlock(t, "dd_gate_base_commit() {", "}") + "\n" +
		extractBlock(t, "dd_gate_changed_files() {", "}") + "\n" +
		extractBlock(t, "dd_gate_packages_from_files() {", "}")
}

// gateRepo —— 造一个**真 git 仓库**，返回目录与三笔 commit 的 sha。
//
// ★ 三笔提交的场景是刻意设计的（★ 关键）：
//   · commit1（★ 当 BASE_REF）：建 `admin/x.go` + `admin/x_test.go` + `ratelimit/y.go`
//   · commit2（★ 碰了 admin，★ **但不是最后一笔**）：改 `admin/x.go`
//   · commit3（★ HEAD，★ **不碰 admin**）：改 `ratelimit/y.go`
// ⇒ ⇒ `HEAD~1..HEAD` ＝ commit2..commit3 ＝ **只有 ratelimit/y.go**
// ⇒ ⇒ 旧判据**必然**选不出 `./admin` ⇒ ⇒ 新判据的价值才测得到。
func gateRepo(t *testing.T) (dir string, baseCommit string, headCommit string) {
	t.Helper()
	dir = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=gate", "GIT_AUTHOR_EMAIL=gate@example.com",
			"GIT_COMMITTER_NAME=gate", "GIT_COMMITTER_EMAIL=gate@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s 失败: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建目录 %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("写 %s: %v", rel, err)
		}
	}

	git("init", "-q")
	git("config", "user.name", "gate")
	git("config", "user.email", "gate@example.com")

	// commit1：★ 基线。
	write("admin/x.go", "package admin\n")
	write("admin/x_test.go", "package admin\n")
	write("ratelimit/y.go", "package ratelimit\n")
	git("add", "-A")
	git("commit", "-qm", "c1")
	baseCommit = git("rev-parse", "HEAD")

	// commit2：★ 碰了 admin，★ 但**不是最后一笔**。
	write("admin/x.go", "package admin\n\n// touched by c2\n")
	git("add", "-A")
	git("commit", "-qm", "c2")

	// commit3：★ HEAD，★ 不碰 admin。
	write("ratelimit/y.go", "package ratelimit\n\n// touched by c3\n")
	git("add", "-A")
	git("commit", "-qm", "c3")
	headCommit = git("rev-parse", "HEAD")

	return dir, baseCommit, headCommit
}

// runGateSelector —— 在**给定目录**里跑抽出来的选包判据，返回包清单。
//
// ★ bundle：★ 抽出来的（★ 或**被变异过的**）判据块。
// ★ baseRef：★ BASE_REF 的取值（★ 可指向不存在的 ref ⇒ 退化场景）。
func runGateSelector(t *testing.T, dir, bundle, baseRef string) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "selector.sh")
	body := "#!/usr/bin/env bash\nset -uo pipefail\n" +
		"export BASE_REF=" + shellQuote(baseRef) + "\n" +
		bundle + "\n" +
		"changed=\"$(dd_gate_changed_files \"$(dd_gate_base_commit)\")\"\n" +
		"dd_gate_packages_from_files \"$changed\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("写临时脚本: %v", err)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("跑选包判据失败: %v\n%s", err, out)
		}
	}
	return strings.TrimSpace(string(out)), code
}

// shellQuote —— 单引号包裹（★ 防注入、★ 保持简单）。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hasLine —— 清单里是否含某一行。
func hasLine(out, want string) bool {
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimSpace(ln) == want {
			return true
		}
	}
	return false
}

// TestDeployGateSelectsPackagesChangedSinceBaseRef —— 判据①【行为】。
//
// ★ 场景见 gateRepo 的注释：★ 改动在**倒数第二笔**、最后一笔不碰 admin。
// ⇒ ⇒ 「基线树 → 当前树」这一路**必须**把 `./admin` 选出来。
func TestDeployGateSelectsPackagesChangedSinceBaseRef(t *testing.T) {
	dir, base, head := gateRepo(t)
	if base == "" || head == "" {
		t.Fatal("临时仓库没造出 commit")
	}
	out, code := runGateSelector(t, dir, gateSelectorBundle(t), base)
	if code != 0 {
		t.Fatalf("选包判据退出码 %d（★ 期望 0）\n%s", code, out)
	}
	if !hasLine(out, "./admin") {
		t.Errorf("选包判据没有选出 ./admin（★ 改动在基线之后、但不在最后一笔 ⇒ 必须由基线 diff 那一路选出来）\n实际输出：\n%s", out)
	}
	// ★ 最后一笔碰的 ratelimit 目录**没有** _test.go ⇒ 不该被选（★ 不许误伤）。
	if hasLine(out, "./ratelimit") {
		t.Errorf("选包判据选出了 ./ratelimit（★ 该目录没有 _test.go ⇒ 不该选）\n实际输出：\n%s", out)
	}
}

// TestDeployGateBaseRefSelectionHasTeeth —— 判据②【变异自证】。
//
// ★ 变异：★ 把第④路（基线 diff）替换成 `true` ⇒ 该路不再产出任何文件。
// ⇒ ⇒ 同一场景必须**选不出** `./admin` ⇒ ⇒ 证明判据①的那条 PASS 不是恒绿。
//
// ★ ★ 同时做**反向确认**：★ 原脚本对同一条输入必须选得出（★ 否则上面那条 PASS 是假的）。
func TestDeployGateBaseRefSelectionHasTeeth(t *testing.T) {
	dir, base, _ := gateRepo(t)
	bundle := gateSelectorBundle(t)

	// ★ 正面：原判据必须选得出。
	real, code := runGateSelector(t, dir, bundle, base)
	if code != 0 {
		t.Fatalf("原判据退出码 %d（★ 期望 0）\n%s", code, real)
	}
	if !hasLine(real, "./admin") {
		t.Fatalf("原判据都没选出 ./admin ⇒ 判据①的 PASS 是假的 ⇒ 本门自证不成立\n实际输出：\n%s", real)
	}

	// ★ 变异臂：★ 精确摘掉第④路。
	if !strings.Contains(bundle, gateBaselineDiffLine) {
		t.Fatalf("闸门脚本里找不到选包第④路（★ 形态变了？★ 请同步更新本测试）：%q", gateBaselineDiffLine)
	}
	broken := strings.Replace(bundle, gateBaselineDiffLine, "true", 1)
	if broken == bundle {
		t.Fatalf("变异臂没改到任何东西（★ 判据形态变了？★ 本门会恒绿）")
	}
	got, code := runGateSelector(t, dir, broken, base)
	if code != 0 {
		t.Fatalf("变异臂退出码 %d（★ 期望 0 ⇒ 变异不该让脚本崩）\n%s", code, got)
	}
	if hasLine(got, "./admin") {
		t.Errorf("变异臂仍选出 ./admin ⇒ 这道门恒绿 ⇒ 第④路被删也发现不了\n实际输出：\n%s", got)
	}
}

// TestDeployGateBaseRefDegradesSafely —— 判据③【退化安全】。
//
// ★ `BASE_REF` 指向**不存在的 ref** ⇒ `git merge-base` 失败 ⇒
//   ★ 第④路必须**安静跳过**（★ 不得 unbound、不得让整个闸门崩），
//   ★ 且兜底（`HEAD~1..HEAD`）仍须可用（★ 脚本整体退出码 0）。
func TestDeployGateBaseRefDegradesSafely(t *testing.T) {
	dir, _, _ := gateRepo(t)
	out, code := runGateSelector(t, dir, gateSelectorBundle(t), "refs/heads/__no_such_ref__")
	if code != 0 {
		t.Errorf("BASE_REF 指向不存在的 ref 时退出码 %d（★ 期望 0 ⇒ 必须安静退化）\n%s", code, out)
	}
	if strings.Contains(out, "unbound variable") || strings.Contains(out, "command not found") {
		t.Errorf("退化路径报出了脚本错误（★ 必须安静跳过）\n%s", out)
	}
}

// gateCodeOnly —— ★ 去掉**注释行**后的脚本正文。
//
// ★ 为什么必须去注释：★ 脚本的说明注释里**引用过** `git merge-base HEAD "$BASE_REF"`
//   这个形态（★ 用来讲历史缺陷）⇒ ★ 若按全文计数，那一处**注释**会被当成
//   「第二个旁路」⇒ ⇒ 门会误报 ⇒ ⇒ ★ 门自己的误报比没门更坏。
func gateCodeOnly(src string) string {
	var b strings.Builder
	for _, ln := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return b.String()
}

// TestDeployGateBaselineUsesSameBaseCommit —— 判据④【口径统一】。
//
// ★ ★ 判据不是「某处调用了 dd_gate_base_commit」（★ 那样只存在一处就放行、
//   ★ 基线对照那一处单独被改回旧形态仍会绿 ⇒ ★ 门没牙齿），
//   ★ 而是**唯一入口约束**（★ 三条同时成立）：
//   (a) `git merge-base HEAD "$BASE_REF"` 这个**直接调用形态**在**可执行代码**里
//       **恰好出现一次** ⇒ ★ 出现两次＝有一处旁路 ⇒ 两套口径会漂移 ⇒ 门红；
//   (b) `$(dd_gate_base_commit)`（★ **调用**形态、★ 不含定义行）**至少出现两次**
//       ⇒ ★ 选包 + 基线对照**两处都走它** ⇒ 口径不可能分叉；
//   (c) (a) 那唯一的一次**落在 dd_gate_base_commit 函数体内**（★ 不是旁路）。
//
// ★ ★ 计数一律在**去注释**后的正文上做（★ 见 gateCodeOnly 的理由）。
func TestDeployGateBaselineUsesSameBaseCommit(t *testing.T) {
	src := readGateScript(t)
	code := gateCodeOnly(src)

	// (a) 直接调用形态的唯一性（★ 代码正文口径）。
	direct := strings.Count(code, `git merge-base HEAD "$BASE_REF"`)
	if direct != 1 {
		t.Errorf("可执行代码里 `git merge-base HEAD \"$BASE_REF\"` 出现 %d 次（★ 期望恰好 1 次）⇒ "+
			"要么有旁路（★ 两套口径会漂移 ⇒ 新红会被当成既有红放行）、要么基线 commit 的入口被拆散了",
			direct)
	}

	// (b) 调用形态的次数（★ 定义行不算 ⇒ 统计 `$(dd_gate_base_commit)`）。
	calls := strings.Count(code, "$(dd_gate_base_commit)")
	if calls < 2 {
		t.Errorf("`$(dd_gate_base_commit)` 只出现 %d 次（★ 期望 ≥2：★ 选包 + 基线对照**两处**都该走它）⇒ "+
			"基线对照那一处多半被改回旧形态了 ⇒ 两套口径会漂移", calls)
	}

	// (c) 唯一的那次直接调用必须落在 dd_gate_base_commit 函数体内。
	if direct == 1 {
		idx := strings.Index(code, "dd_gate_base_commit() {")
		if idx < 0 {
			t.Errorf("脚本里找不到 dd_gate_base_commit 定义")
			return
		}
		rest := code[idx:]
		end := strings.Index(rest, "\n}\n")
		if end < 0 {
			t.Errorf("dd_gate_base_commit 函数体没有正常闭合")
			return
		}
		if !strings.Contains(rest[:end], `git merge-base HEAD "$BASE_REF"`) {
			t.Errorf("唯一的那次 `git merge-base HEAD \"$BASE_REF\"` 不在 dd_gate_base_commit 函数体内 ⇒ 是旁路")
		}
	}
}

// TestDeployGateBaselineSameBaseCommitHasTeeth —— 判据④的【变异自证】。
//
// ★ 把**基线对照那一处**换回旧形态（★ 直接调 git merge-base）⇒
//   ★ (a) 直接调用次数 1→2、(b) 调用次数 2→1 ⇒ ★ 契约检查必须**失败**
//   ⇒ ⇒ 证明判据④有牙齿（★ 不是恒绿）。
func TestDeployGateBaselineSameBaseCommitHasTeeth(t *testing.T) {
	src := readGateScript(t)

	// ★ 精确定位**基线对照那一处**（★ 不是选包段那处、★ 也不是函数里的局部变量）。
	//
	// ★ ★ 为什么不能用「锚点 + 第一个匹配」那种定位（★ 本轮实测踩过）：
	//   ★ 锚点「基线树」在**别的注释**里也出现 ⇒ 锚点落错 ⇒
	//   ★ 第一个 `base_commit="` 命中的是 `dd_gate_changed_files` 里的
	//   ★ **局部变量声明** `local base_commit="${1:-}"` ⇒ 变异改了个不相干的地方
	//   ★ ⇒ ⇒ 断言的数对不上 ⇒ ★ 门自己先红 ⇒ ★ **门自己的误报比没门更坏**。
	//
	// ★ 确定性做法：★ 全文里 `base_commit="$(dd_gate_base_commit)"` 恰好两处
	//   （★ 选包段 + 基线对照段）⇒ ★ **第二处**就是基线对照那处。
	const callForm = `base_commit="$(dd_gate_base_commit)"`
	if got := strings.Count(src, callForm); got != 2 {
		t.Skipf("★ 全文里 `%s` 出现 %d 次（★ 期望恰好 2 次：★ 选包段 + 基线对照段）⇒ "+
			"★ 形态变了 ⇒ 本测试的变异点需要同步更新", callForm, got)
	}
	first := strings.Index(src, callForm)
	rel := strings.Index(src[first+len(callForm):], callForm)
	second := first + len(callForm) + rel

	// ★ 变异：★ 把**基线对照那一处**换回「直接调 git merge-base」的旧形态。
	mutated := src[:second] +
		`base_commit="$(git merge-base HEAD "$BASE_REF" 2>/dev/null || true)"` +
		src[second+len(callForm):]
	if mutated == src {
		t.Skip("★ 变异点没命中（★ 形态变了？★ 本测试需要同步更新）")
	}

	before := gateCodeOnly(src)
	after := gateCodeOnly(mutated)

	// ★ 变异后直接调用必须从 1 变成 2（★ 证明「唯一入口」这条约束真的能咬住）。
	if got := strings.Count(after, `git merge-base HEAD "$BASE_REF"`); got != 2 {
		t.Errorf("变异后直接调用出现 %d 次（★ 期望 2 ⇒ 证明「唯一入口」这条约束能抓住「基线对照改回旧形态」）", got)
	}
	// ★ 变异后调用形态次数必须从 2 降到 1（★ 证明「两处都走它」这条约束能咬住）。
	beforeCalls := strings.Count(before, "$(dd_gate_base_commit)")
	afterCalls := strings.Count(after, "$(dd_gate_base_commit)")
	if afterCalls != beforeCalls-1 {
		t.Errorf("变异后 `$(dd_gate_base_commit)` 次数 %d → %d（★ 期望恰好 -1 ⇒ 证明「两处都走它」这条约束能咬住）",
			beforeCalls, afterCalls)
	}
}
