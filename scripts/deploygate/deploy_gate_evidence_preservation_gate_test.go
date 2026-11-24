package deploygate_test

// deploy_gate_evidence_preservation_gate_test.go —— 门：部署闸门**被中断时
// 必须留下它已经产出的读数**（★ 证据留存，★ 不是口径）。
//
// ── 为什么这道门存在（★ 2026-11-24 实测到的机制缺陷）────────────────────
//
//	闸门把读数写在 workdir 里（★ current.txt / base.txt / *.part）⇒
//	★ 而 workdir 由 EXIT trap 的 cleanup() **无条件删除** ⇒
//	★★ 被 SIGTERM / SIGKILL 掐断的那一轮，★ **它自己产出的读数被一起删掉**
//	★★ ⇒ 「基线红清单」永远是残缺的 ⇒ ⇒ 差集归因**不能做**
//	★★ ⇒ 只能另去隔离 worktree 上定向复跑（★ 本会话为 6 条红做过一次、★ 8.001s）。
//
//	★ 这不是「读数不准」、★ 是「**量具在被掐断时毁掉自己的证据**」★。
//
// ── 为什么这个修法**不动口径** ──────────────────────────────────────────
//
//	★ 它只做一件事：★ 把**已经算出来的**读数复制到另一个目录。
//	★ 「比哪些包」「红绿怎么判」「基线豁免认不认」★ **一个字都没改** ★
//	⇒ ⇒ 与「基线只跑有红的包」那个**口径**轴改动**完全无关**（★ 那个仍未擅自改）。
//
// ── 三条判据 ────────────────────────────────────────────────────────────
//
//	① 【静态】★ 闸门脚本必须 source 证据留存库；★ 且 `cleanup()` 里
//	   **必须**在 `rm -rf "$workdir"` **之前**调用它
//	   ⇒ 顺序反了 ⇒ 留存的是已经被删掉的东西 ⇒ 判空。
//	② 【行为】★ **真跑**证据留存库：★ 在临时目录里造 `current.txt` 与
//	   `base.txt.part` ⇒ 调用留存 ⇒ 断言两个文件都在、★ 且 README 里写着
//	   「基线可能不完整」⇒ ★ 那句是【正确用法】的载体 ⇒ 少了它 ⇒
//	   ★ 后人会拿残缺清单做差集 ⇒ ★★ **比「文件在不在」更要紧** ★★。
//	③ 【变异自证】★ 把闸门脚本里那句调用**删掉** ⇒ 同一场景必须报红
//	   ⇒ ⇒ 证明判据①**有牙齿**（★ 不是恒绿）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	evidenceGateScriptPath = "../../scripts/deploy-test-gate.sh"
	evidenceGateLibPath    = "../../scripts/lib/gate-evidence.sh"
)

// readEvidenceGateScript —— 读闸门脚本全文。
func readEvidenceGateScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(evidenceGateScriptPath)
	if err != nil {
		t.Fatalf("读闸门脚本 %s: %v", evidenceGateScriptPath, err)
	}
	return string(b)
}

// cleanupBody —— 抽出 `cleanup()` 的函数体（★ 到下一个顶格 `}` 为止）。
func cleanupBody(t *testing.T, script string) string {
	t.Helper()
	start := strings.Index(script, "cleanup() {")
	if start < 0 {
		t.Fatal("★ 闸门脚本里找不到 cleanup() ⇒ 本门恒红（★ 判据失效）")
	}
	rest := script[start:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatal("★ cleanup() 的函数体找不到结尾 ⇒ 本门恒红（★ 判据失效）")
	}
	return rest[:end]
}

// evidenceStaticProblems —— 静态判据①（★ 返回问题清单、★ 空清单＝通过）。
//
// ★ ★ 关键点（★ 本门第一版栽在这里）：★ **不能**用「文本里出现过这个名字」
//
//	当判据 —— ★ 因为 `declare -F gate_evidence_preserve` 这类**提及**也会命中
//	⇒ ★ 删掉真正的【调用】后判据仍然绿 ⇒ ★ **判据恒绿** ★★
//	⇒ ⇒ 改为**数调用点**：★ 去掉空白后以 `gate_evidence_preserve ` 开头的行
//	★ 才算一次【调用】。
//
// evidenceCallRE —— ★ **调用点**的判据（★ 命令词形态、★ 不是「文本里出现过」）。
//
// ★ ★ 本门在这个锚点上栽过两次（★ 都已修、★ 记在这里免得复发）：
//
//	① 第一版用「行首必须是函数名」⇒ ★ 判据**恒绿**（★ `declare -F …` 那句
//	   【提及】也命中、★ 而删掉真正的调用后判据仍绿）★★
//	② 第二版把调用改成 `_gate_evidence_dir="$(gate_evidence_preserve …)"`
//	   形态后、★ 「行首」锚点又**太窄** ⇒ ★ 门当场红（★ 好在它是红的、★ 没放过）★★
//
// ⇒ ⇒ 正解＝**按命令词判定**（★ 正则 ＋ 排除注释行与 `declare -F` 提及）。
var evidenceCallRE = regexp.MustCompile(`(^|[^A-Za-z0-9_-])gate_evidence_preserve[[:space:]]`)

// evidenceIsInvocation —— 这一行是不是一次【调用】（★ 而不是提及、★ 不是注释）。
func evidenceIsInvocation(ln string) bool {
	trimmed := strings.TrimSpace(ln)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	if strings.Contains(ln, "declare -F gate_evidence_preserve") {
		return false // ★ 只是「函数在不在」的提及、★ 不是调用
	}
	return evidenceCallRE.MatchString(ln)
}

// evidenceStaticProblems —— 静态判据①（★ 返回问题清单、★ 空清单＝通过）。
func evidenceStaticProblems(script string) []string {
	var problems []string
	if !strings.Contains(script, "scripts/lib/gate-evidence.sh") {
		problems = append(problems, "★ 闸门脚本没有 source 证据留存库（scripts/lib/gate-evidence.sh）")
	}
	body := cleanupBodyFor(script)
	calls := 0
	callIdx := -1
	rmIdx := -1
	for i, ln := range strings.Split(body, "\n") {
		if evidenceIsInvocation(ln) {
			calls++
			if callIdx < 0 {
				callIdx = i
			}
		}
		if rmIdx < 0 && strings.Contains(ln, `rm -rf "$workdir"`) {
			rmIdx = i
		}
	}
	if calls == 0 {
		problems = append(problems, "★ cleanup() 里没有【调用】gate_evidence_preserve ⇒ 被中断时读数随 workdir 一起消失")
		return problems
	}
	switch {
	case rmIdx < 0:
		problems = append(problems, "★ cleanup() 里找不到删除 workdir 的那一行 ⇒ 判据失效")
	case callIdx > rmIdx:
		problems = append(problems, "★ cleanup() 里 gate_evidence_preserve 的调用出现在【删除 workdir】**之后** ⇒ 留存的是已经被删掉的东西")
	}
	return problems
}

// cleanupBodyFor —— 不带 testing.T 的版本（★ 供变异臂复用）。
func cleanupBodyFor(script string) string {
	start := strings.Index(script, "cleanup() {")
	if start < 0 {
		return ""
	}
	rest := script[start:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// TestGateEvidenceIsPreservedOnInterrupt —— 判据①【静态】。
func TestGateEvidenceIsPreservedOnInterrupt(t *testing.T) {
	script := readEvidenceGateScript(t)
	if _, err := os.Stat(evidenceGateLibPath); err != nil {
		t.Fatalf("★ 证据留存库 %s 不存在 ⇒ 判据失效：%v", evidenceGateLibPath, err)
	}
	if problems := evidenceStaticProblems(script); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("%s", p)
		}
		t.Errorf("★ ⇒ 修法：★ 在 cleanup() 里 `rm -rf \"$workdir\"` **之前**调用 gate_evidence_preserve")
	}
}

// TestGateEvidencePreservationMutantIsDetected —— 判据③【变异自证】。
func TestGateEvidencePreservationMutantIsDetected(t *testing.T) {
	script := readEvidenceGateScript(t)
	// ★ 变异＝**把 cleanup() 里那一句【调用】摘掉**（★ 连同 `declare -F` 那句
	//   【提及】一起摘 ⇒ ★ 否则「文本出现过这个名字」仍会命中 ⇒ ★ 判据恒绿）★★。
	mutant := ""
	removed := 0
	for _, ln := range strings.Split(script, "\n") {
		if evidenceIsInvocation(ln) ||
			(strings.Contains(ln, "declare -F gate_evidence_preserve") && !strings.HasPrefix(strings.TrimSpace(ln), "#")) {
			removed++
			continue
		}
		mutant += ln + "\n"
	}
	if removed == 0 {
		t.Fatal("★ 变异没有命中任何一行 ⇒ 变异臂失效（★ 判据可能恒绿）")
	}
	problems := evidenceStaticProblems(mutant)
	if len(problems) == 0 {
		t.Fatal("★★ 删掉 gate_evidence_preserve 调用后判据仍然通过 ⇒ ★★ 判据恒绿（★ 没有牙齿）★★")
	}
	t.Logf("★ 变异臂按预期报红：%s", strings.Join(problems, " / "))
}

// TestGateEvidenceLibraryActuallyPreserves —— 判据②【行为】。
//
// ★ 真跑证据留存库（★ bash 3.2 兼容、★ 不依赖任何外部依赖）。
func TestGateEvidenceLibraryActuallyPreserves(t *testing.T) {
	if _, err := os.Stat(evidenceGateLibPath); err != nil {
		t.Fatalf("★ 证据留存库 %s 不存在 ⇒ 判据失效：%v", evidenceGateLibPath, err)
	}
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("造 workdir: %v", err)
	}
	// ★ 造两份读数：★ current.txt（当前树）＋ base.txt.part（★ 基线的分箱中间输出）
	if err := os.WriteFile(filepath.Join(work, "current.txt"), []byte("ok  \tpkg-a\t1.0s\n"), 0o644); err != nil {
		t.Fatalf("造 current.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "base.txt.part"), []byte("--- FAIL: TestX\n"), 0o644); err != nil {
		t.Fatalf("造 base.txt.part: %v", err)
	}
	root := filepath.Join(tmp, "evidence")

	script := ". \"" + evidenceGateLibPath + "\"\n" +
		"gate_evidence_preserve \"" + work + "\" \"" + root + "\" \"gate-interrupted\"\n"
	cmd := exec.Command("bash", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("★ 证据留存库跑不起来：%v\n%s", err, out)
	}
	dest := strings.TrimSpace(string(out))
	if dest == "" {
		t.Fatal("★ 留存函数没有打印留存目录 ⇒ 判据②失效")
	}
	if !strings.HasPrefix(dest, root) {
		t.Errorf("★ 留存目录 %q 不在指定的证据根 %q 下 ⇒ 判据②失效", dest, root)
	}
	for _, name := range []string{"current.txt", "base.txt.part"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("★ 留存目录里没有 %s ⇒ 读数仍然会丢：%v", name, err)
		}
	}
	readme, err := os.ReadFile(filepath.Join(dest, "README.md"))
	if err != nil {
		t.Fatalf("★ 留存目录没有 README.md ⇒ 使用者无从知道【基线可能不完整】：%v", err)
	}
	// ★ ★ 那句是【正确用法】的载体 ⇒ 少了它 ⇒ 后人会拿残缺清单做差集。
	if !strings.Contains(string(readme), "不完整") {
		t.Errorf("★ README 里没有写明「基线可能不完整」⇒ ★ 使用者可能拿残缺清单做差集 ★\n%s", readme)
	}
}
