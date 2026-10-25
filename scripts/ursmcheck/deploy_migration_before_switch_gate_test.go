package ursm_test

// R77 轮的门：**「切换前必须先应用 pending 迁移」这个顺序本身要有一道门**。
//
// # 这道门补的是哪块缺口
//
// R76 轮（handoff §19.21.11 遗留 2）登记：生产部署链按 `^[0-9]{3}_` 挑编号迁移，
// 而**编号迁移必须在「切流之前」应用** —— 否则新二进制会在**缺列/缺索引**的库上启动。
// 部署链 `scripts/deploy-seamless.sh` 的 [6.5/9] 步确实做了这件事（失败即 `exit 1`），
//
//	★ 但**全仓没有任何门钉住这个顺序** ⇒ 下次有人把这一步挪到切流之后，
//	  或者把 `|| { ...; exit 1; }` 改成 `|| true`，**没有任何东西会红**。
//
// ⇒ 这就是 R98 轮同族结论「关键顺序要钉住而不是钉住形态」的又一例：
// **顺序本身**是承重结构 ⇒ 顺序必须被测。
//
// # 本门钉什么
//
//	J1 顺序腿：迁移调用必须在 Nginx 原子切流步**之前**
//	J2 失败阻断腿：迁移失败的分支必须 `exit 1`（不得吞掉）
//	J3 安装器腿：891 在 installer 的 StartupFiles + embeddata 里（新装库拿得到）
//	J4 升级腿：891 在部署链的编号迁移通道 `files=()` 里（升级库拿得到）
//	J5 裸重启腿（★ **已知缺口**，本门把它钉成**显式事实**）
//
// # ★ J5 为什么是「事实」而不是「要求」
//
// 裸重启（不经部署链）只跑 `db.Open` 里的 Go ensure 链，**不跑编号 SQL 迁移**
// ⇒ 「891 先于新二进制」在裸重启路径上**不受保护**。
// 这**不是本门能修掉的**（把编号迁移塞进 ensure 链是产品级语义决策，属主项），
// ⇒ 本门只做两件事：① 把它**钉成显式事实**（若哪天 ensure 链真的吸收了它，
// 本门会红，提醒同步更新缺口登记）；② 明确 J3/J4 两条腿才是**编号迁移的投递保证**。
//
// # ★ 门自身的「有牙」证明
//
// `TestDeployMigrationOrderGateHasTeeth` 用**反向夹具**证明 J1 不是空转：
// 把「迁移调用」构造到切流步**之后** ⇒ 判据必须判红。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	r77DeployScript = "scripts/deploy-seamless.sh"
	r77RevSeqScript = "scripts/apply-db-revision-sequence.sh"
	r77RunnerGo     = "installer/internal/dbinit/runner.go"
	r77InstallerGo  = "installer/cmd/llm-gw-installer/main.go"

	r77Migration891 = "891_session_turns_cost_negative_partial_idx.sql"

	r77IdxParent = "idx_session_turns_cost_negative"
	r77IdxHot    = "idx_session_turns_hot_cost_negative"

	// r77SwitchAnchor 是 Nginx 原子切流那一步的日志锚点。
	r77SwitchAnchor = "[8/9] 候选预热 + Nginx 原子切流"
	// r77MigrateCall 是「应用 pending 迁移」那一步的调用锚点。
	r77MigrateCall = "deploy_apply_pending_migrations"
)

func repoRootR77(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	// file = <repo>/scripts/ursmcheck/<this>.go
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

func readR77(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", rel, err)
	}
	return string(b)
}

// r77LineOf 返回第一个包含 sub 的行号（1-based）；找不到返回 0。
func r77LineOf(lines []string, sub string) int {
	for i, l := range lines {
		if strings.Contains(l, sub) {
			return i + 1
		}
	}
	return 0
}

// r77BlockUntil 从 startIdx（0-based）起，取到某行 trim 后等于 terminator 为止的整块。
// 找不到 terminator ⇒ 返回 ("", false) ⇒ 门必须判红（不许默认当空块放过）。
func r77BlockUntil(lines []string, startIdx int, terminator string) (string, bool) {
	if startIdx < 0 || startIdx >= len(lines) {
		return "", false
	}
	for j := startIdx; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == terminator {
			return strings.Join(lines[startIdx:j+1], "\n"), true
		}
	}
	return "", false
}

// r77OrderOK 判「迁移调用在切流步之前」。
// 返回 (通过, 诊断)。★ 任一锚点缺失都判红 —— 锚点缺失意味着**解析面变了**，
// 不能默认当作「通过」（那是最容易写出恒绿门的地方）。
func r77OrderOK(deploySrc string) (bool, string) {
	lines := strings.Split(deploySrc, "\n")
	mig := r77LineOf(lines, r77MigrateCall)
	sw := r77LineOf(lines, r77SwitchAnchor)
	if mig == 0 || sw == 0 {
		return false, fmt.Sprintf("锚点缺失：迁移调用行=%d 切流行=%d（任一为 0 ⇒ 解析面已变，判红）", mig, sw)
	}
	if mig >= sw {
		return false, fmt.Sprintf("迁移调用在第 %d 行、切流在第 %d 行 ⇒ 迁移没有先于切流", mig, sw)
	}
	return true, fmt.Sprintf("迁移第 %d 行 < 切流第 %d 行", mig, sw)
}

// r77AbortOK 判「迁移失败即 exit 1（失败被吞掉就是静默切流）」。
func r77AbortOK(deploySrc string) (bool, string) {
	lines := strings.Split(deploySrc, "\n")
	idx := -1
	for i, l := range lines {
		if strings.Contains(l, r77MigrateCall) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false, "锚点缺失：找不到 " + r77MigrateCall + " 调用"
	}
	// bash 的失败分支在调用之后的 3 行内（`\` 续行 + `|| { ...; }`）。
	end := idx + 4
	if end > len(lines) {
		end = len(lines)
	}
	blob := strings.Join(lines[idx:end], "\n")
	if !strings.Contains(blob, "exit 1") {
		return false, "迁移失败分支里没有 `exit 1` ⇒ 失败会被吞掉 ⇒ 可能带着未迁移的库继续切流"
	}
	return true, "失败分支含 exit 1"
}

// r77EnsureChainFiles 列出 db/ 下**非测试**的 .go 文件（ensure 链所在处）。
func r77EnsureChainFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	dbDir := filepath.Join(root, "db")
	err := filepath.WalkDir(dbDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 db/ 失败: %v", err)
	}
	return out
}

// TestDeployMigrationRunsBeforeTrafficSwitch —— J1 顺序腿。
func TestDeployMigrationRunsBeforeTrafficSwitch(t *testing.T) {
	root := repoRootR77(t)
	deploySrc := readR77(t, root, r77DeployScript)
	ok, diag := r77OrderOK(deploySrc)
	if !ok {
		t.Fatalf("J1 顺序腿红：%s\n（%s 的 %q 必须在 %q 之前）", diag, r77DeployScript, r77MigrateCall, r77SwitchAnchor)
	}
	t.Logf("J1 通过：%s", diag)
}

// TestDeployMigrationFailureAbortsBeforeSwitch —— J2 失败阻断腿。
func TestDeployMigrationFailureAbortsBeforeSwitch(t *testing.T) {
	root := repoRootR77(t)
	deploySrc := readR77(t, root, r77DeployScript)
	ok, diag := r77AbortOK(deploySrc)
	if !ok {
		t.Fatalf("J2 失败阻断腿红：%s", diag)
	}
	t.Logf("J2 通过：%s", diag)
}

// TestInstallerLegCarriesMigration891 —— J3 安装器腿。
func TestInstallerLegCarriesMigration891(t *testing.T) {
	root := repoRootR77(t)

	// ① StartupFiles 切片里必须有 891
	runnerSrc := readR77(t, root, r77RunnerGo)
	lines := strings.Split(runnerSrc, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "StartupFiles: []string{") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("J3 红：%s 里找不到 StartupFiles 切片 ⇒ 解析面已变", r77RunnerGo)
	}
	block, ok := r77BlockUntil(lines, start, "},")
	if !ok {
		t.Fatalf("J3 红：%s 的 StartupFiles 切片没有闭合 ⇒ 解析面已变", r77RunnerGo)
	}
	if !strings.Contains(block, r77Migration891) {
		t.Fatalf("J3 红：891 不在 installer 的 StartupFiles 里 ⇒ 新装库拿不到这条迁移")
	}

	// ② embeddata 必须有同名键 + go:embed 指令
	instSrc := readR77(t, root, r77InstallerGo)
	if !strings.Contains(instSrc, `//go:embed embeddata/startup/`+r77Migration891) {
		t.Fatalf("J3 红：installer 没有 go:embed %s ⇒ embeddata 里没有这份字节", r77Migration891)
	}
	if !strings.Contains(instSrc, `"startup/`+r77Migration891+`"`) {
		t.Fatalf("J3 红：installer 的 embeddata map 里没有 %s 的键", r77Migration891)
	}
	t.Log("J3 通过：891 在 StartupFiles + go:embed + embeddata map 三处齐备")
}

// TestDeployRevisionChannelCarriesMigration891 —— J4 升级腿。
func TestDeployRevisionChannelCarriesMigration891(t *testing.T) {
	root := repoRootR77(t)
	src := readR77(t, root, r77RevSeqScript)
	lines := strings.Split(src, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "files=(" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("J4 红：%s 里找不到 files=( ⇒ 解析面已变", r77RevSeqScript)
	}
	block, ok := r77BlockUntil(lines, start, ")")
	if !ok {
		t.Fatalf("J4 红：%s 的 files=( 没有闭合 ⇒ 解析面已变", r77RevSeqScript)
	}
	if !strings.Contains(block, r77Migration891) {
		t.Fatalf("J4 红：891 不在部署链的编号迁移通道 files=() 里 ⇒ 升级库拿不到这条迁移")
	}
	t.Log("J4 通过：891 在部署链 files=() 里")
}

// r77CodeMentions 判「这个 .go 文件的**代码**（不是注释）里出现了 needle」。
//
// ★★ 为什么用 AST 而不是手写剥注释：手写剥注释会被**字符串字面量里的 `/*` 或 `//`** 骗掉
// （仓内 db/ 下有大量内嵌 SQL DDL，里面就有 `/* ... */`）⇒ 整段代码被误吞
// ⇒ **门会恒绿**（本轮真踩到：手写版把注入的索引名整行吃掉了 ⇒ M5 变异跑成绿
// ⇒ 差点把一道**恒绿门**交出去）。
// ⇒ AST 判据天然排除注释（**注释不是 AST 节点**）⇒ 也不会被字符串里的 `/*` 骗。
func r77CodeMentions(src, needle string) (bool, string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		return false, "", err
	}
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			if strings.Contains(v.Value, needle) {
				hits = append(hits, v.Value)
			}
		case *ast.Ident:
			if v.Name == needle {
				hits = append(hits, v.Name)
			}
		}
		return true
	})
	return len(hits) > 0, strings.Join(hits, " | "), nil
}

// TestBareRestartLegIsPinnedAsKnownGap —— J5 裸重启腿（显式事实）。
//
// ★ 本门**不要求** ensure 链包含 891 —— 那会与产品语义冲突。
// 本门要求的是：ensure 链**当前不**创建 891 的两条索引，
// 即「编号迁移不归 ensure 链管」是**被显式登记的现状**。
// ⇒ 哪天有人把编号迁移塞进 ensure 链，本门会红并要求同步更新缺口登记。
func TestBareRestartLegIsPinnedAsKnownGap(t *testing.T) {
	root := repoRootR77(t)
	files := r77EnsureChainFiles(t, root)
	if len(files) == 0 {
		t.Fatalf("J5 红：db/ 下没扫到任何非测试 .go 文件 ⇒ 解析面已变（不许当空通过）")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", f, err)
		}
		// ★ AST 判据：命中只可能来自**真正的代码**（字面量/标识符），不是文档里的提及。
		for _, needle := range []string{r77IdxParent, r77IdxHot} {
			hit, where, perr := r77CodeMentions(string(b), needle)
			if perr != nil {
				rel, _ := filepath.Rel(root, f)
				t.Fatalf("J5 红：解析 %s 失败 ⇒ 解析面已变（不许当空通过）: %v", rel, perr)
			}
			if hit {
				rel, _ := filepath.Rel(root, f)
				t.Fatalf("J5 红：Go ensure 链（%s）的**代码**里出现了 %s（命中：%s）⇒ 「编号迁移不归 ensure 链管」这条现状变了\n"+
					"⇒ 裸重启路径的保护范围**扩大了** ⇒ 请同步更新 handoff 的缺口登记（§19.26）", rel, needle, where)
			}
		}
	}
	t.Logf("J5 通过：%d 个 ensure 链文件的**代码**里都不含 891 的索引名 ⇒ 裸重启缺口维持显式登记", len(files))
}

// TestDeployMigrationOrderGateHasTeeth —— 门自身的「有牙」证明。
func TestDeployMigrationOrderGateHasTeeth(t *testing.T) {
	root := repoRootR77(t)
	deploySrc := readR77(t, root, r77DeployScript)

	// ① 真实文件：J1 必须绿（否则门自己就红了，不是有牙）
	if ok, diag := r77OrderOK(deploySrc); !ok {
		t.Fatalf("真实文件上 J1 就是红的 ⇒ 门自己坏了: %s", diag)
	}

	// ② 反向夹具：把迁移调用挪到切流步**之后** ⇒ J1 必须判红。
	lines := strings.Split(deploySrc, "\n")
	var migIdx, swIdx = -1, -1
	for i, l := range lines {
		if migIdx < 0 && strings.Contains(l, r77MigrateCall) {
			migIdx = i
		}
		if swIdx < 0 && strings.Contains(l, r77SwitchAnchor) {
			swIdx = i
		}
	}
	if migIdx < 0 || swIdx < 0 {
		t.Fatalf("反向夹具无法构造：找不到迁移调用(%d)或切流锚点(%d)", migIdx, swIdx)
	}
	mutated := make([]string, len(lines))
	copy(mutated, lines)
	// 把迁移那一行搬到切流行之后
	migLine := mutated[migIdx]
	mutated = append(mutated[:migIdx], mutated[migIdx+1:]...)
	swIdxAfter := swIdx
	if swIdxAfter > migIdx {
		swIdxAfter--
	}
	mutated = append(mutated[:swIdxAfter+1], append([]string{migLine}, mutated[swIdxAfter+1:]...)...)

	if ok, diag := r77OrderOK(strings.Join(mutated, "\n")); ok {
		t.Fatalf("反向夹具没被抓住：把迁移挪到切流之后，J1 仍然判绿 ⇒ J1 是恒绿门")
	} else {
		t.Logf("反向夹具被抓住：%s", diag)
	}

	// ③ 反向夹具：把**迁移那一条**失败分支的 exit 1 去掉 ⇒ J2 必须判红。
	//    ⚠ 只能在**迁移调用自己的窗口内**替换：全文件 `strings.Replace(..., 1)`
	//    命中的可能是**更早的**另一处 `; exit 1; }` ⇒ 变异落在别处 ⇒ **空跑**
	//    （本轮真踩到：夹具第一次版本改的是文件里第一处 ⇒ J2 仍绿 ⇒ 门自己差点被放过）。
	//    ⇒ 下面用「窗口内替换 + 断言替换确实发生」把它钉死。
	migLineIdx := -1
	for i, l := range lines {
		if strings.Contains(l, r77MigrateCall) {
			migLineIdx = i
			break
		}
	}
	if migLineIdx < 0 {
		t.Fatalf("反向夹具无法构造：找不到迁移调用")
	}
	winEnd := migLineIdx + 4
	if winEnd > len(lines) {
		winEnd = len(lines)
	}
	window := strings.Join(lines[migLineIdx:winEnd], "\n")
	if !strings.Contains(window, "exit 1") {
		t.Fatalf("反向夹具无法构造：迁移调用窗口里没有 exit 1（窗口=%q）", window)
	}
	mutWindow := strings.Replace(window, "exit 1", "true", 1)
	if mutWindow == window {
		t.Fatalf("反向夹具无法构造：窗口内替换没有生效 ⇒ 变异会空跑")
	}
	noAbort := strings.Join(lines[:migLineIdx], "\n") + "\n" + mutWindow +
		"\n" + strings.Join(lines[winEnd:], "\n")
	if ok, diag := r77AbortOK(noAbort); ok {
		t.Fatalf("反向夹具没被抓住：去掉迁移分支的 exit 1 后 J2 仍然判绿 ⇒ J2 是恒绿门")
	} else {
		t.Logf("反向夹具被抓住：%s", diag)
	}
}
