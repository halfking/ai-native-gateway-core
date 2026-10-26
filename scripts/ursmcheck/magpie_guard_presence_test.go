package ursm_test

// magpie_guard_presence_test.go —— ★ **「门在位自证」门**（★ 闭合 **F9**）。
//
// # 这道门守住哪块缺口
//
// ★★ 「计划项 → 门」完整性门（`scripts/ursmcheck/magpie_action_item_coverage_test.go`）
// ★★ 的判据④「§四 登记的每个门文件都必须真实存在」**住在它自己那个文件里**
// ⇒ ⇒ ★★ 把**它自己**删掉 ⇒ 判据④ **也不存在了** ⇒ ⇒ ★★ **无人抓**（★ 这就是 **F9**）。
// ⇒ ⇒ 本门住在**另一个文件**里 ⇒ ⇒ 它核「完整性门在不在、有没有断言」
// ⇒ ⇒ ⇒ ★★ **两侧互为对偶** ⇒ ★★ 删掉任意**一个** ⇒ 另一个会红。
//
// # 四条判据
//
//	① ★ **完整性门在位且有断言**：★ 文件在仓内存在 ＋ ★ **至少含一个 `func TestXxx`
//	   的函数声明**（★ AST 事实 ⇒ ★ 一行注释骗不过它）
//	   ⇒ ⇒ 完整性门被删 / 改名 / 掏空 ⇒ **红**。
//	② ★ **完整性门在 §四 有登记**（★ **「位置」列**级）：★ 审计档 §四 门禁表的
//	   **「位置」列**里必须出现它的路径（★ 别的列里顺口提一句**不算登记**）
//	   ⇒ ⇒ 「删文件 + 顺手把 §四 那行也删掉」这种**无痕**打法 ⇒ **红**。
//	③ ★ **§四 登记的每个门文件都存在**（★ 完整性门判据④ 的**对偶**）
//	   ⇒ ⇒ 即便完整性门的判据④ 被摘掉，别的门消失**仍有人抓**；★
//	   ⇒ ⇒ 也挡住「§四 登了个不存在的文件」。
//	④ ★ **本门自己也在 §四 有登记**（★ **「位置」列**级）：★ 否则「删掉本门」这条
//	   对偶就断了 ⇒ **红**。
//
// # ★★ 为什么本门**不复用**完整性门里的任何符号
//
// ★★ 若复用（★ `magpieHasTestFunc` / `magpieSectionScopes` / `magpieAuditGateRows` …）
// ★★ ⇒ ★★ 删掉完整性门 ⇒ 本门**编译不过** ⇒ ⇒ 拿到的是「**编译错误**」这种
// ★★ **无信息量**的红 ⇒ ⇒ 拿它当「F9 已闭」的证据是**无效的**
// ★★ （★ 变异读数纪律：★ 必须区分「**判定**抓到了」与「**编译**炸了」）
// ⇒ ⇒ 本门**自包含**（★ AST 检出 / §四 定位 / §四 行解析 / 仓根定位全部自己实现）。
// ★★ 唯一复用的是**与被审对象无关**的只读常量与读文件 helper
// ★★ （★ `auditDocRelPath` / `readRepoFile`，★ 住在计划书时效性门里）
// ⇒ ⇒ ★★ 删掉那个文件 ⇒ 整包**编译红** ⇒ ⇒ ★★ **不会**退化成假绿。
//
// # ★★ 残余（★ 登记在审计档 §五）
//
// ★★ **同时删掉完整性门与本门** ⇒ ⇒ **仍然无人抓** ⇒ ⇒ 这是「一道门永远抓不到
// ★★ 「自己被删」」这条通用式的**边界**（★ 再收窄就要**第三份**权威 ⇒ 属独立一轮）
// ⇒ ⇒ ⇒ ★★ 登记而不修。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mgpCompletenessGateRel 是**被核对象**：★ 完整性门在仓内的相对路径。
//
// ★ 路径**写死**是**故意的**：★ 本门核的就是「这个**具体**文件还在不在」
// ⇒ ⇒ 用 glob 反而不准（★ glob 命中 0 个 = 没了；★ 命中多个 = 复制品 ⇒ 都要红）。
const mgpCompletenessGateRel = "scripts/ursmcheck/magpie_action_item_coverage_test.go"

// mgpSelfRel 是**本门自己**在仓内的相对路径（★ 判据④ 用）。
const mgpSelfRel = "scripts/ursmcheck/magpie_guard_presence_test.go"

// mgpAuditScopeRe 定位审计档 §四 门禁清单（★ **行首**锚定 ⇒ 正文里的提及不算）。
var mgpAuditScopeRe = regexp.MustCompile(`(?m)^## 四、门禁清单`)

// mgpBacktickTokenRe 取出表格单元格里**反引号**包起来的 token。
var mgpBacktickTokenRe = regexp.MustCompile("`([^`]+)`")

// mgpRepoPathRe 识别「**看起来是仓内文件路径**」的 token。
//
// ★★ 与完整性门**同款口径**（★ 故意不共用实现 ⇒ ★ 编译独立性优先于 DRY）
// ★★ ⇒ ★ 于是 `make guards` 这类**非路径** token **不会**被误当成门文件 ⇒ ★ 不假红。
var mgpRepoPathRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*\.(go|sh|py)$`)

// mgpRepoRoot 返回仓根目录。
//
// ★ `go test` 的工作目录是**包目录**（`scripts/ursmcheck`）⇒ 仓根 = `../..`
// ★ ⇒ 再核一下 `go.mod` 在不在 ⇒ ⇒ 「定位失败」与「门没了」不混为一谈。
func mgpRepoRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("★ 仓根定位可疑：%s 下没有 go.mod ⇒ 读数无效", root)
	}
	return root
}

// mgpHasTestFunc 判断一个 Go 源文件里**是否真的有** `func TestXxx` 的**函数声明**。
//
// ★★★ 与完整性门里的同名函数**故意重复**：★ 那一份住在**被核对象**里
// ★★★ ⇒ 复用它 ⇒ 删掉被核对象 ⇒ 本门**编译不过** ⇒ ★★ 拿不到有信息量的判定。
// ★★★ ⇒ ⇒ 本门自带一份 ⇒ ⇒ 判据① 的删除实验得到的是**判定红**，不是**编译红**。
func mgpHasTestFunc(src []byte, rel string) (bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return false, err
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil {
			continue
		}
		if strings.HasPrefix(fd.Name.Name, "Test") {
			return true, nil
		}
	}
	return false, nil
}

// mgpAuditGateScope 取出审计档 §四 的正文。
//
// ★★ **逐个尝试所有命中**，取**第一个真能抽出表格行**的：
// ★★ ⇒ 顶部目录里**单独一行**的 `## 四、门禁清单`（★ 里面没有表格行）会被跳过
// ★★ ⇒ ⇒ 门不会因「有人加了行目录」而误报「锚点漂了」（★ 同 F6 那一族）。
func mgpAuditGateScope(text string) (string, bool) {
	for _, loc := range mgpAuditScopeRe.FindAllStringIndex(text, -1) {
		rest := text[loc[0]:]
		scope := rest
		// ★ 下一个「行首 `\n## `」= 下一个同级标题（★ `\n### ` 不会被误判）。
		if n := strings.Index(rest[1:], "\n## "); n >= 0 {
			scope = rest[:n+1]
		}
		paths, err := mgpAuditRowPaths(scope)
		if err == nil && len(paths) > 0 {
			return scope, true
		}
	}
	return "", false
}

// mgpAuditRegColIndex 定位 §四 门禁表里「**登记**」所在的列（★ 「位置」列）。
//
// ★★ 与完整性门**同款口径**（★ 故意不共用实现 ⇒ ★ 编译独立性优先于 DRY）
// ★★ ⇒ ★ §四 的登记单位**就是「位置」列** ⇒ ⇒ 别的列里顺口提一句路径**不算登记**
// ★★ ⇒ ⇒ 不假红（★ 第六轮 verifier 的 FR1 形态）。
//
// ★★ 找不到「位置」列 ⇒ ★★ 返回 false ⇒ ★★ 门**响亮失败**（★ 不许静默变成「零条登记」）。
func mgpAuditRegColIndex(scope string) (int, bool) {
	for _, line := range strings.Split(scope, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) < 2 || strings.TrimSpace(cells[1]) != "门" {
			continue
		}
		idx := -1
		for i, c := range cells {
			if strings.TrimSpace(c) == "位置" {
				if idx >= 0 {
					return 0, false // ★ 两列都叫「位置」⇒ 口径不明 ⇒ 响亮失败。
				}
				idx = i
			}
		}
		if idx < 0 {
			return 0, false
		}
		return idx, true
	}
	return 0, false
}

// mgpAuditRowPaths 取出 §四 **表格行**的「位置」列里登记的门文件路径
// （★ 散文里的、★ 别的列里的**都不算**）。
func mgpAuditRowPaths(scope string) ([]string, error) {
	regCol, ok := mgpAuditRegColIndex(scope)
	if !ok {
		return nil, fmt.Errorf("★ §四 表头里找不到唯一的「位置」列 ⇒ 登记口径不明")
	}
	var out []string
	for _, line := range strings.Split(scope, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if regCol >= len(cells) {
			continue
		}
		for _, tok := range mgpBacktickTokenRe.FindAllStringSubmatch(cells[regCol], -1) {
			if mgpRepoPathRe.MatchString(tok[1]) {
				out = append(out, tok[1])
			}
		}
	}
	return out, nil
}

// mgpContains 判断路径列表里是否**含有**某个路径（★ 精确相等 ⇒ 不吃前缀）。
func mgpContains(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// TestMagpieGuardPresenceIsEnforced 是「**门在位自证**」门（★ 闭合 **F9**）。
//
// ★★ 它核的是**另一道门**在不在 ⇒ ⇒ 与完整性门**互为对偶**
// ★★ ⇒ ⇒ 完整性门被删 ⇒ 本门红；★ 本门被删 ⇒ 完整性门判据④ 红。
func TestMagpieGuardPresenceIsEnforced(t *testing.T) {
	root := mgpRepoRoot(t)

	// ① ★ 完整性门在位且**真的有断言**（★ AST 事实 ⇒ 注释骗不过）。
	abs := filepath.Join(root, mgpCompletenessGateRel)
	b, rerr := os.ReadFile(abs)
	switch {
	case rerr != nil:
		t.Errorf("★★ 完整性门 %s 在仓内**不存在** ⇒\n"+
			"★★ ⇒ 「计划项 → 门」完整性门**消失了** ⇒ ★★ **F9 重新打开**：\n"+
			"★★ ⇒ ★ 它的判据④「§四 登记的每个门文件都必须真实存在」**住在它自己那个文件里**\n"+
			"★★ ⇒ ★ 删掉它 ⇒ 判据④ 也不存在 ⇒ ★★ 那一族「门被删」就**再也没人抓**\n"+
			"★★ ⇒ 修法：恢复该门文件（★ `git log -- %s` 可查历史）",
			mgpCompletenessGateRel, mgpCompletenessGateRel)
	default:
		has, perr := mgpHasTestFunc(b, mgpCompletenessGateRel)
		switch {
		case perr != nil:
			t.Errorf("★★ 完整性门 %s **解析失败** ⇒ 无法核对它有没有断言 ⇒ 修法：修好这个文件",
				mgpCompletenessGateRel)
		case !has:
			t.Errorf("★★ 完整性门 %s **没有任何 `func TestXxx` 的函数声明** ⇒\n"+
				"★★ ⇒ 它在位但**一个测试函数都没有** ⇒ 本门**核不到**它到底断言了什么\n"+
				"★★ ⇒ ★ 判据是 **AST 事实**：★ 一行 `// func TestXxx` 的**注释**骗不过它\n"+
				"★★ ⇒ ★★ **口径提示**（★ 第六轮 verifier 的 S7）：★ 本判据只保证「**有测试函数声明**」\n"+
				"★★ ⇒ ★★ ⇒ **不**保证「函数体里有断言」⇒ ★★ 修法：把真正的断言写进那个测试函数",
				mgpCompletenessGateRel)
		}
	}

	// ② ③ ④ ★ §四 台账层的三件事。
	audit := readRepoFile(t, auditDocRelPath)
	scope, ok := mgpAuditGateScope(audit)
	if !ok {
		t.Fatalf("★ 审计档里找不到「## 四、门禁清单」小节（或它一行表格都没有）⇒\n" +
			"★ ⇒ 锚点漂了 ⇒ 门无法对账 ⇒ 读数无效（★ 不是「门没了」）")
	}
	paths, perr := mgpAuditRowPaths(scope)
	if perr != nil {
		t.Fatalf("★ 解析 §四 的**登记口径**失败 ⇒ %v ⇒ 门无法对账", perr)
	}

	// ② ★ 完整性门必须在 §四 **有登记**（★ **「位置」列**级 ⇒ ★ 别的列里提一句**不算登记**）。
	if !mgpContains(paths, mgpCompletenessGateRel) {
		t.Errorf("★★ 完整性门 %s **没有**登记在审计档 §四 门禁表里 ⇒\n"+
			"★★ ⇒ 「谁核它」这一条**没有台账** ⇒ ★★ 删掉它 + 顺手删掉 §四 那行 ⇒\n"+
			"★★ ⇒ 就**没有任何痕迹** ⇒ ★★ **F9 重新打开**\n"+
			"★★ ⇒ 修法：在 §四 表里补一行（★ 位置 = 门文件路径那一列）",
			mgpCompletenessGateRel)
	}

	// ③ ★ §四 登记的**每一个**门文件都必须真实存在（★ 完整性门判据④ 的对偶）。
	for _, rel := range paths {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("★★ 审计档 §四 门禁表登记的门文件 %s 在仓内**不存在** ⇒\n"+
				"★★ ⇒ 审计档登记的验收量具**已经消失** ⇒ 读者照着它去找会扑空\n"+
				"★★ ⇒ ★ 这条与完整性门的判据④ **同判**（★ 互为对偶 ⇒ 任一侧被摘掉都还有牙）\n"+
				"★★ ⇒ 修法：改成本仓真实存在的路径，或把这一行删掉",
				rel)
		}
	}

	// ④ ★ 本门自己必须在 §四 **有登记**（★ **「位置」列**级 ⇒ ★ 否则这条对偶是**断的**）。
	if !mgpContains(paths, mgpSelfRel) {
		t.Errorf("★★ 本门 %s **没有**登记在审计档 §四 门禁表里 ⇒\n"+
			"★★ ⇒ 「完整性门的对偶」这条线**没有台账** ⇒ ★★ 本门被删时没人知道它存在过\n"+
			"★★ ⇒ ★★ 判据④ 的作用（★ 完整性门判据④ 会抓「§四 登记的门文件不存在」）\n"+
			"★★ ⇒ ★★ 就会**随着本门一起消失** ⇒ ★★ ⇒ **两侧互为对偶**的前提被破坏\n"+
			"★★ ⇒ 修法：在 §四 表里补一行（★ 位置 = 门文件路径那一列）",
			mgpSelfRel)
	}
}

// TestMagpieGuardPresenceChecksHaveTeeth 是本门**判据本身**的自检。
//
// ★★ 为什么需要它：★ 判据①（AST 检出）、判据②④（§四 登记判定）、
// ★★ 判据③（§四 行解析）一旦**静默失效** ⇒ ⇒ 门会**恒绿**
// ★★ ⇒ ⇒ 必须有**独立**的自检证明它们**取对了**、且**只取该取的**。
//
// ★★ 形态照抄本仓库既有纪律（★ `TestMagpiePlanItemExtractorHasTeeth` /
// ★★ `TestMagpieAuditGateRowExtractorHasTeeth` / `TestMagpieP2CorpusFacesAreSeparated`）。
func TestMagpieGuardPresenceChecksHaveTeeth(t *testing.T) {
	// ① ★ AST 检出：★ 真有 `func TestXxx` ⇒ 必须为 true。
	if ok, err := mgpHasTestFunc([]byte("package p\n\nfunc TestReal(t *testing.T) {}\n"), "x.go"); err != nil || !ok {
		t.Errorf("★ AST 检出漏掉了真的 `func TestXxx`（err=%v ok=%v）⇒ 判据① 会假红", err, ok)
	}
	// ② ★ AST 检出：★ **只有一行注释** ⇒ 必须为 false（★ 注释骗不过它）。
	if ok, err := mgpHasTestFunc([]byte("package p\n\n// func TestFake(t *testing.T) {}\n"), "x.go"); err != nil || ok {
		t.Errorf("★ AST 检出被一行**注释**骗过了（err=%v ok=%v）⇒ 判据① 会假绿", err, ok)
	}

	// ③ ★ §四 行解析：★ **「位置」列**里的路径要抽到；★ **散文里**的、★ **别的列**里的**不**抽。
	const scope = "| 门 | 位置 | 条数 | CI | 变异 |\n|---|---|---|---|---|\n" +
		"| P0-1 裁决表 | `a/gate_test.go` | 6 | ✅ | M1 全红（另见 `e/other_col_test.go`） |\n" +
		"\n散文里也提到 `b/prose_test.go`，但它**不在表格行**里 ⇒ 不该被算作登记。\n"
	got, err := mgpAuditRowPaths(scope)
	if err != nil {
		t.Fatalf("★ 解析 §四 的登记口径失败 ⇒ %v", err)
	}
	if !mgpContains(got, "a/gate_test.go") {
		t.Errorf("★ §四 行解析漏掉了「位置」列里的路径（实际 %v）", got)
	}
	if mgpContains(got, "b/prose_test.go") {
		t.Errorf("★ §四 行解析把**散文里**的路径当成了登记（实际 %v）⇒ 判据② 会假绿", got)
	}
	if mgpContains(got, "e/other_col_test.go") {
		t.Errorf("★ §四 行解析把**别的列**（变异列）里的路径当成了登记（实际 %v）⇒ 判据②/③ 会**假红**", got)
	}

	// ④ ★ §四 定位：★ 顶部目录里**单独一行**的 `## 四、门禁清单`（★ 没有表格行）
	//    ⇒ 必须跳过它、继续找真 §四（★ 同 F6 那一族 ⇒ 否则会误报「锚点漂了」）。
	const toc = "## 四、门禁清单\n\n（目录项）\n\n## 四、门禁清单与变异验证\n\n" +
		"| 门 | 位置 |\n|---|---|\n| P0-1 裁决表 | `a/gate_test.go` |\n"
	sc, ok := mgpAuditGateScope(toc)
	if !ok || !strings.Contains(sc, "a/gate_test.go") {
		t.Errorf("★ §四 定位被顶部目录骗了（ok=%v scope=%q）⇒ 判据②③④ 会假红", ok, sc)
	}

	// ⑤ ★ §四 定位失败必须**如实**返回 false（★ 不许瞎猜一个空 scope）。
	if _, ok := mgpAuditGateScope("# 别的文档\n\n正文里有 `x/gate_test.go`\n"); ok {
		t.Errorf("★ §四 定位对没有 §四 的文本返回了 true ⇒ 判据面会漂")
	}

	// ⑥ ★★ 登记列解析不出来 ⇒ ★★ 必须**响亮失败**（★ 不许静默变成「零条登记」⇒ 那是假绿）。
	if _, err := mgpAuditRowPaths("| 门 | 说明 |\n|---|---|\n| P0-1 | `a/gate_test.go` |\n"); err == nil {
		t.Errorf("★ 表头里没有「位置」列时**没有**报错 ⇒ 口径漂了会静默退化成假绿")
	}
}
