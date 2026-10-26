package ursm_test

// magpie_audit_count_column_test.go —— ★★ **§四 门禁表「条数」列的计数对账门**（2026-10-25 §3.19.16）
//
// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// # 这道门补的是哪块缺口（★ 本轮实测撞出来的）
//
// 审计档 §四 的「条数」列是**计数类声明**（★「这个门有 N 条判据 / N 道自检 / N 个测试」），
// ⇒ 仓内的规矩是「**计数类结论：口径必须和数字一起发布**」＋「计数类声明必须实测数一遍」
// ⇒ ★★ 但**没有任何门**在核它 ⇒ ⇒ 计数会**静默漂移**。
//
// ★★ 本轮实测一次就抓到 **9 处漂移**（★ 判据数 3 处 / 测试数 2 处 / 过期订正 2 处 / 分项 1 处 / 自检数 1 处），
// ⇒ ⇒ 而「这个门有 N 条判据」正是**别人判断「它覆盖了什么」的唯一依据**
// ⇒ ⇒ ⇒ 漂了 = 把覆盖面说小了或说大了 ⇒ ⇒ ★★ **比「门失效」更隐蔽**（门照样绿，只是**说明书**错了）。
//
// # ★★ 为什么这道门是「**对账**」而不是「**扫描**」
//
// ★ 计数列的**口径是异质的**：★ 裸整数（★ 同一个文件两行写 6 与 1 ⇒ 那是**判据数**不是测试数）
// ★ 「N 道自检」/「N 自检」/「N 条断言」/「N 项对拍表」/「N 条判据」/「—」…
// ⇒ ⇒ ★★ **不能**用一个统一正则去核 ⇒ ⇒ ★★ 否则必然**假红**（★ 假红比假绿更费）。
// ⇒ ⇒ ★★ 所以本门：**只核口径唯一的那些形态**，其余**明确不核**并写进下面的「登记残余」。
//
// # ★★ 计数怎么读（★ **只读「当前声明段」** ⇒ ★★ 订正横幅里的**历史数**不参与核验）
//
// ⇒ ★★ 单元里除了当前计数，常有「★ ★ **订正（…）**：★ 原写「5 测试」⇒ …」这样的**历史数引用**
// ⇒ ⇒ ★★ 若对**整个单元**跑正则 ⇒ ★★ 会把**历史数**当成当前计数 ⇒ ★★ 读数随**横幅措辞**漂 ⇒ ★★ 假红
// ⇒ ⇒ ★★ 正解：★ 只读「**订正」横幅**之前**的那一段」⇒ ★★ 当前数与历史数**永不相撞**
// ⇒ ⇒ ★★ **约定**：★ **当前计数写在单元最前**，★ 订正横幅写在后面（★ 本门有自检钉住这一条）。
//
// # ★★ 六条判据
//
//	C1 ★ **测试数**：★ 门侧钉住 (行名 → 文件, 期望 N)
//	   ⇒ 文档该行「条数」单元里「**N 测试**」形态的 N
//	   **必须** == 门侧期望 N **==** 该文件里 `func TestXxx` 的**真实个数**（★ AST 事实）
//	   ⇒ ⇒ ★★ **三方相等**；任一方单独变 ⇒ 红：
//	   ★ 文档单方面改 N ⇒ 红；★ 代码单方面加/删一个测试 ⇒ 红；★ 门侧期望忘了改 ⇒ 红。
//	C2 ★ **自检数**：★ 门侧钉住 (行名 → 文件, [自检函数名…])
//	   ⇒ 文档该行「N 道自检」/「N 自检」的 N == 钉住的名字个数
//	   ⇒ ⇒ 且每个钉住的名字**真实存在**于该文件（★ 名字写错 ⇒ 红；★ 自检被删 ⇒ 红）。
//	C3 ★ **断言数**：★ 门侧钉住 (行名 → 自检函数, 期望 M)
//	   ⇒ 文档该行「M 条断言」的 M == 那个自检函数里 `t.Error*` / `t.Fatal*` 的**调用数**（★ AST 事实）
//	   ⇒ ⇒ ★ 口径写在门里：`t.Error` / `t.Errorf` / `t.Fatal` / `t.Fatalf` **四种**都算。
//	C4 ★ **提取器自检**：★ 喂合成 §四 片段 ⇒ 必须挑对行列，
//	   且★ 必须**跳过**「多文件行」/「非 .go 行」/「无计数形态行」⇒ ⇒ 防假红、也防恒绿。
//	C5 ★ **反向**：★ 门侧钉住的每个行名必须在 §四 里**存在**（★ 门侧清单写了一个不存在的行 ⇒ 红）。
//	C6 ★ **自注册**：★ 本门自身必须在 §四 的「位置」列被登记
//	   ⇒ ⇒ 与完整性门判据④、对偶门**互锁**（★ 删掉本门 ⇒ 那两道门会红）。
//
// # ★★ 登记残余（本门**不**核的部分 ⇒ ★★ 不假装已闭）
//
//	① **裸整数形态**（如「9」「14」）⇒ ★★ 口径**不唯一**（★ 同一个文件两行写 6 与 1 ⇒ 是**判据数**不是测试数）
//	   ⇒ ⇒ ★★ **不核** ⇒ ★★ 只能靠人读 ⇒ 登记。
//	② **门文件头里的判据数**（如「★★ 六条判据」）⇒ ★★ 那是**散文**、且「判据」在本仓是
//	   **跨门共用的编号**（★ C7 归横幅门）⇒ ★★ 机读需要一个**跨门映射表**⇒ ★★ 本轮**不**引入
//	   ⇒ ⇒ ★★ 靠**手工订正 + 本轮的实测**（★ §3.19.16 记了全部 9 处）⇒ 登记。
//	③ **「N 项对拍表」/「N 项登记 certRef」** ⇒ ★★ 它们是**表内元素数**（★ 可机读）
//	   ⇒ ★★ 但取值口径是「**那一张特定的表**」⇒ ★★ 要机读就得把「表在哪个文件的哪个变量」也钉住
//	   ⇒ ⇒ ★★ 本轮**先不核** ⇒ 登记（★ **不**与 C1 混为一谈 ⇒ 两者口径不同）。
//
// # ★★ 「被测集合」的设计（★ 与本仓其它门同款）
//
// ⇒ ★★ 门侧钉住一份**期望表**（★ 独立于被审对象）⇒ ★ 改文档 ⇒ 期望表不变 ⇒ 门红（★ 正确）
// ⇒ ★ 改代码 ⇒ 期望表不变 ⇒ 门红（★ 正确）⇒ ★★ ⇒ 「三方同时改」才是**显式动作**（★ 要动门文件）。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	// macdcAuditDocRelPath 是被审的审计档。
	macdcAuditDocRelPath = "docs/audit/2026-10-24-magpie-v0.1.1367-改造落地审计.md"
	// macdcSection4Header 是 §四 表的表头（★ 「条数」列的**位置**由它定位 ⇒ 找不到 ⇒ 响亮失败）。
	macdcSection4Header = "| 门 | 位置 | 条数 | CI | 变异 |"
)

// macdcPin 是一条**门侧钉住**的期望（★ 独立于被审对象）。
type macdcPin struct {
	// rowKey 是 §四「门」列里的**判别子串**（★ 与 file 一起定位唯一一行）。
	rowKey string
	// file 是被点的门文件（相对仓根）。
	file string
	// wantTests 是 C1 的期望测试函数个数；★ nil = 该行**不要求**带「N 测试」形态。
	//
	// ★★ 用**指针**而不是 int：★ int 的零值 0 与「不适用」分不开
	// ⇒ ⇒ 曾把「不适用」误当成「钉着 0 个」⇒ ⇒ **自己制造了一处假红**（★ 已修，见 §3.19.16）。
	wantTests *int
	// selfChecks 是 C2 钉住的自检函数名（★ nil = C2 不适用）。
	selfChecks []string
	// wantAssertions 是 C3 的期望断言数；★ nil = C3 不适用（★ 同上 ⇒ 用指针）。
	wantAssertions *int
	// assertFunc 是 C3 的目标自检函数（★ 必须在 selfChecks 里）。
	assertFunc string
}

// macdcInt 造一个「期望值」指针（★ 用来避开 int 零值陷阱）。
func macdcInt(n int) *int { return &n }

// macdcPins 是门侧的期望表（★ **独立于**被审对象 ⇒ 改文档/改代码都躲不过它）。
//
// ★★ 每一行都注明了「口径」——★ 数字必须与口径**一起**发布（★ 否则读者无从判断它指什么）。
var macdcPins = []macdcPin{
	{
		rowKey: "设计页漂移门", file: "scripts/ursmcheck/request_surface_doc_currency_test.go",
		wantTests: macdcInt(6),
	},
	{
		rowKey: "D09 覆盖测试", file: "cmd/gateway/main_pipeline_requestid_test.go",
		wantTests: macdcInt(2),
	},
	{
		rowKey: "P2-1 门升级", file: "scripts/ursmcheck/magpie_p2_v2_criteria_test.go",
		selfChecks: []string{
			"TestMagpieP2FuncCorpusIsPerFunction",
			"TestMagpieP2DeadBranchesArePruned",
			"TestMagpieP2MissingAssertionsHasTeeth",
			"TestMagpieP2CorpusFacesAreSeparated",
			"TestMagpieP2LivenessIsEnforced",
		},
	},
	{
		rowKey: "完整性门", file: "scripts/ursmcheck/magpie_action_item_coverage_test.go",
		selfChecks: []string{
			"TestMagpiePlanItemExtractorHasTeeth",
			"TestMagpiePlanBroadCandidatesHasTeeth",
			"TestMagpiePlanScopeIgnoresTopDirectory",
			"TestMagpieAuditGateRowExtractorHasTeeth",
		},
	},
	{
		rowKey: "对偶门", file: "scripts/ursmcheck/magpie_guard_presence_test.go",
		selfChecks: []string{
			"TestMagpieGuardPresenceChecksHaveTeeth",
		},
		wantAssertions: macdcInt(9),
		assertFunc:     "TestMagpieGuardPresenceChecksHaveTeeth",
	},
	{
		rowKey: "条数", file: "scripts/ursmcheck/magpie_audit_count_column_test.go",
		wantTests: macdcInt(3),
	},
}

// macdcCountClaim 是一条从「条数」单元里解析出来的**计数声明**。
type macdcCountClaim struct {
	// tests 是「N 测试」形态的 N；★ -1 = 该单元没有这个形态。
	tests int
	// selfChecks 是「N 道自检」/「N 自检」形态的 N；★ -1 = 没有。
	selfChecks int
	// assertions 是「M 条断言」形态的 M；★ -1 = 没有。
	assertions int
}

// macdcTestCountRe 匹配「N 测试」形态。
var macdcTestCountRe = regexp.MustCompile(`(\d+)\s*测试`)

// macdcSelfCheckCountRe 匹配「N 道自检」/「N 自检」形态（★ 「道」可有可无）。
var macdcSelfCheckCountRe = regexp.MustCompile(`(\d+)\s*(?:道)?\s*自检`)

// macdcAssertionCountRe 匹配「M 条断言」形态。
var macdcAssertionCountRe = regexp.MustCompile(`(\d+)\s*条断言`)

// macdcClaimSegmentMarker 是「**订正」横幅的**起始标记**（★ 仓内订正横幅一律用它开头）。
const macdcClaimSegmentMarker = "**订正"

// macdcCurrentClaimSegment 取出「条数」单元里的**当前声明段**。
//
// ★★ 为什么要有这一步（★ 本轮实测撞出来的）：★ 单元里除了当前计数，还常有
// 「★ ★ **订正（…）**：★ 原写「5 测试」⇒ …」这样的**历史数引用**
// ⇒ ⇒ ★★ 若直接对**整个单元**跑正则 ⇒ ★★ 会把**历史数**当成当前计数
// ⇒ ⇒ ★★ ⇒ 读数随**横幅的措辞**漂 ⇒ ⇒ ★★ 门红得没有道理（★ 假红）。
// ⇒ ⇒ ★★ 正解：★ 只读「**订正**横幅**之前**的那一段」⇒ ★★ 当前数与历史数**永不相撞**。
// ⇒ ⇒ ★★ 约定（★ 写在这里，便于文档引用）：★ **当前计数写在单元最前**，★ 订正横幅写在后面。
func macdcCurrentClaimSegment(cell string) string {
	if i := strings.Index(cell, macdcClaimSegmentMarker); i >= 0 {
		return cell[:i]
	}
	return cell
}

// macdcParseCountClaim 从「条数」单元解析出所有**口径唯一**的计数声明。
//
// ★ 只认三种形态（★ 其余形态**不认** ⇒ 不核 ⇒ 见「登记残余」①③）。
func macdcParseCountClaim(cell string) macdcCountClaim {
	c := macdcCountClaim{tests: -1, selfChecks: -1, assertions: -1}
	seg := macdcCurrentClaimSegment(cell)
	if m := macdcTestCountRe.FindStringSubmatch(seg); m != nil {
		c.tests, _ = strconv.Atoi(m[1])
	}
	if m := macdcSelfCheckCountRe.FindStringSubmatch(seg); m != nil {
		c.selfChecks, _ = strconv.Atoi(m[1])
	}
	if m := macdcAssertionCountRe.FindStringSubmatch(seg); m != nil {
		c.assertions, _ = strconv.Atoi(m[1])
	}
	return c
}

// macdcAuditRow 是一条 §四 表格行。
type macdcAuditRow struct {
	name     string
	location string
	count    string
}

// macdcParseSection4 从审计档文本里解析出 §四 门禁表的所有行。
//
// ★ 表头必须**逐字**命中（★ 找不到 ⇒ 响亮失败 ⇒ 不许静默变成「零行」⇒ 那是假绿）。
func macdcParseSection4(doc string) ([]macdcAuditRow, error) {
	scope, ok := macdcSection4Scope(doc)
	if !ok {
		return nil, fmt.Errorf("审计档里找不到 §四 小节 ⇒ 口径漂了 ⇒ 响亮失败（★ 不许静默零行）")
	}
	lines := strings.Split(scope, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == macdcSection4Header {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("§四 小节里找不到表头 %q ⇒ 口径漂了 ⇒ 响亮失败（★ 不许静默零行）", macdcSection4Header)
	}
	var rows []macdcAuditRow
	for _, l := range lines[start+1:] {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "|") {
			continue // ★ 跳过空行/散文（★ 与完整性门同口径 ⇒ 两门看的必须是同一张表）
		}
		if strings.HasPrefix(t, "|---") {
			continue
		}
		f := strings.Split(strings.Trim(t, "|"), "|")
		if len(f) < 3 {
			continue
		}
		rows = append(rows, macdcAuditRow{
			name:     strings.TrimSpace(f[0]),
			location: strings.TrimSpace(f[1]),
			count:    strings.TrimSpace(f[2]),
		})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("§四 表头在，但**零行** ⇒ 解析器坏了（★ 不许静默零行 ⇒ 那是假绿）")
	}
	return rows, nil
}

// macdcSection4Scope 取出 §四 小节的文本（★ 「## 四、门禁清单与变异验证」到下一个「## 」为止）。
func macdcSection4Scope(doc string) (string, bool) {
	const heading = "## 四、门禁清单与变异验证"
	lines := strings.Split(doc, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == heading {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
}

// macdcRepoRoot 返回仓根（★ 不用别的门的符号 ⇒ 删掉别的门不会让本门只得到「编译红」）。
func macdcRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败：%v", err)
	}
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("从 %s 往上找不到 go.mod ⇒ 仓根定位失败", wd)
	return ""
}

// macdcCountTestFuncs 数一个 .go 文件里**顶层**的 `func TestXxx` 声明个数（★ AST 事实）。
func macdcCountTestFuncs(t *testing.T, path string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	n := 0
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if strings.HasPrefix(fn.Name.Name, "Test") {
			n++
		}
	}
	return n
}

// macdcCountAssertionCalls 数一个函数体里 `t.Error*` / `t.Fatal*` 的**调用数**。
//
// ★ 口径（★ 写在这里，便于文档引用）：★ `t.Error` / `t.Errorf` / `t.Fatal` / `t.Fatalf` **四种**都算。
func macdcCountAssertionCalls(t *testing.T, path, funcName string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != funcName {
			continue
		}
		n := 0
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			base, ok := sel.X.(*ast.Ident)
			if !ok || base.Name != "t" {
				return true
			}
			switch sel.Sel.Name {
			case "Error", "Errorf", "Fatal", "Fatalf":
				n++
			}
			return true
		})
		return n
	}
	t.Fatalf("在 %s 里找不到函数 %s ⇒ 门侧钉住的名字与现实不符", path, funcName)
	return 0
}

// macdcHasFunc 报告某个文件里是否有**顶层**的该函数声明（★ AST 事实 ⇒ 注释骗不过）。
func macdcHasFunc(t *testing.T, path, funcName string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == funcName {
			return true
		}
	}
	return false
}

// TestMagpieAuditCountColumnMatchesRealCounts 是 C1 / C2 / C3 / C5 / C6。
func TestMagpieAuditCountColumnMatchesRealCounts(t *testing.T) {
	root := macdcRepoRoot(t)
	docPath := filepath.Join(root, macdcAuditDocRelPath)
	b, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("读审计档 %s 失败：%v", macdcAuditDocRelPath, err)
	}
	rows, err := macdcParseSection4(string(b))
	if err != nil {
		t.Fatalf("★ %v", err)
	}

	// C6：★ 本门自身必须在 §四 的「位置」列被登记（⇒ 与完整性门判据④、对偶门互锁）。
	selfRel := "scripts/ursmcheck/magpie_audit_count_column_test.go"
	selfRegistered := false
	for _, r := range rows {
		if strings.Contains(r.location, selfRel) {
			selfRegistered = true
			break
		}
	}
	if !selfRegistered {
		t.Errorf("★ 本门（%s）没有在审计档 §四 的「位置」列登记 ⇒ 删掉本门时没人会红\n"+
			"★ 请在 §四 加一行：位置写 %s", selfRel, selfRel)
	}

	for _, pin := range macdcPins {
		// 定位行：★ 门名子串 **且** 位置列含钉住的文件 ⇒ 两键定位 ⇒ 唯一。
		var hit *macdcAuditRow
		for i := range rows {
			if strings.Contains(rows[i].name, pin.rowKey) && strings.Contains(rows[i].location, pin.file) {
				hit = &rows[i]
				break
			}
		}
		if hit == nil {
			// C5：★ 门侧钉住的每个行名必须在 §四 里存在。
			t.Errorf("★ 门侧钉住的行「%s」（文件 %s）在 §四 里**不存在** ⇒ 期望表与现实脱节\n"+
				"★ 请要么把该行补回 §四，要么把这条钉住删掉（★ 两者都改是**显式动作**）",
				pin.rowKey, pin.file)
			continue
		}
		claim := macdcParseCountClaim(hit.count)
		abs := filepath.Join(root, pin.file)

		// C1：★ 测试数三方相等。
		if pin.wantTests != nil {
			want := *pin.wantTests
			if claim.tests < 0 {
				t.Errorf("★ §四 的「%s」行没有「N 测试」形态的计数声明 ⇒ 门侧钉着 %d 个测试却核不到\n"+
					"★ 请把该行的「条数」列写成「%d 测试 …」，或把门侧的 wantTests 置 nil（★ 明说不核）",
					pin.rowKey, want, want)
			} else {
				real := macdcCountTestFuncs(t, abs)
				if claim.tests != real {
					t.Errorf("★ §四 的「%s」行写「%d 测试」，但 %s 里真实有 **%d** 个 `func TestXxx` ⇒ 计数漂了\n"+
						"★ 请把文档改成 %d，或把门侧 wantTests 改成 %d（★ 两者都改是**显式动作**）",
						pin.rowKey, claim.tests, pin.file, real, real, real)
				}
				if claim.tests != want {
					t.Errorf("★ §四 的「%s」行写「%d 测试」，但门侧钉着 **%d** ⇒ 两份声明不一致\n"+
						"★ 这是「被审对象可削弱声明」的形态 ⇒ 门侧必须留第二份权威（★ 就是这张表）",
						pin.rowKey, claim.tests, want)
				}
			}
		}

		// C2：★ 自检数 + 名字真实性。
		if len(pin.selfChecks) > 0 {
			if claim.selfChecks < 0 {
				t.Errorf("★ §四 的「%s」行没有「N 道自检」形态的计数声明 ⇒ 门侧钉着 %d 道却核不到",
					pin.rowKey, len(pin.selfChecks))
			} else if claim.selfChecks != len(pin.selfChecks) {
				t.Errorf("★ §四 的「%s」行写「%d 道自检」，但门侧钉着 **%d** 道 ⇒ 计数漂了\n"+
					"★ 钉住的名字：%s", pin.rowKey, claim.selfChecks, len(pin.selfChecks),
					strings.Join(pin.selfChecks, " / "))
			}
			for _, name := range pin.selfChecks {
				if !macdcHasFunc(t, abs, name) {
					t.Errorf("★ 门侧钉住的自检 `%s` 在 %s 里**不存在** ⇒ 自检被删/被改名 ⇒ 计数已漂",
						name, pin.file)
				}
			}
		}

		// C3：★ 断言数。
		if pin.wantAssertions != nil {
			want := *pin.wantAssertions
			if claim.assertions < 0 {
				t.Errorf("★ §四 的「%s」行没有「M 条断言」形态的计数声明 ⇒ 门侧钉着 %d 条却核不到",
					pin.rowKey, want)
			} else {
				real := macdcCountAssertionCalls(t, abs, pin.assertFunc)
				if claim.assertions != real {
					t.Errorf("★ §四 的「%s」行写「%d 条断言」，但 `%s` 里真实有 **%d** 处 `t.Error*`/`t.Fatal*` 调用 ⇒ 计数漂了\n"+
						"★ 口径：`t.Error` / `t.Errorf` / `t.Fatal` / `t.Fatalf` **四种**都算",
						pin.rowKey, claim.assertions, pin.assertFunc, real)
				}
				if claim.assertions != want {
					t.Errorf("★ §四 的「%s」行写「%d 条断言」，但门侧钉着 **%d** ⇒ 两份声明不一致",
						pin.rowKey, claim.assertions, want)
				}
			}
		}
	}
}

// TestMagpieAuditCountColumnExtractorHasTeeth 是 C4：★ 提取器自检。
func TestMagpieAuditCountColumnExtractorHasTeeth(t *testing.T) {
	// ① ★ §四 小节不在 ⇒ 必须报错（★ 不许静默零行）。
	if _, err := macdcParseSection4("# 别的文档\n\n| 门 | 位置 | 条数 |\n|---|---|---|\n| x | y | 1 |\n"); err == nil {
		t.Errorf("★ 没有 §四 小节却没报错 ⇒ 解析器会静默退化成「零行」⇒ 那是假绿")
	}
	// ② ★ §四 小节在、表头被改 ⇒ 必须报错（★ 响亮失败）。
	if _, err := macdcParseSection4("## 四、门禁清单与变异验证\n\n| 门 | 位置 |\n|---|---|\n| x | y |\n"); err == nil {
		t.Errorf("★ 表头被改了却没报错 ⇒ 口径漂了会静默退化成假绿")
	}
	// ③ ★ §四 小节在、表头在、但**零行** ⇒ 必须报错（★ 不许静默零行）。
	if _, err := macdcParseSection4("## 四、门禁清单与变异验证\n\n" + macdcSection4Header + "\n"); err == nil {
		t.Errorf("★ 只有表头没有数据行却没报错 ⇒ 解析器坏了")
	}
	// ④ ★ 形态解析：★ 三个形态各自认得；★ 其它形态**不认**（⇒ 不会被误核）。
	got := macdcParseCountClaim("**6 测试** / **8 判据** / **12 项对拍表**")
	if got.tests != 6 || got.selfChecks != -1 || got.assertions != -1 {
		t.Errorf("★ 「N 测试」形态解析错：%+v", got)
	}
	got = macdcParseCountClaim("3 → **4 判据** ＋ **4 道自检**（★ 提取器 / ★ 广域候选）")
	if got.tests != -1 || got.selfChecks != 4 || got.assertions != -1 {
		t.Errorf("★ 「N 道自检」形态解析错：%+v", got)
	}
	got = macdcParseCountClaim("**4 判据** ＋ **1 自检（9 条断言）**（★ 自检覆盖面 = ★ AST 检出（2））")
	if got.tests != -1 || got.selfChecks != 1 || got.assertions != 9 {
		t.Errorf("★ 「N 自检（M 条断言）」形态解析错：%+v", got)
	}
	// ★ 裸整数形态**不认**（★ 口径不唯一 ⇒ 不核 ⇒ 见「登记残余」①）。
	got = macdcParseCountClaim("9")
	if got.tests != -1 || got.selfChecks != -1 || got.assertions != -1 {
		t.Errorf("★ 裸整数形态被误认成某种计数（%+v）⇒ 口径不唯一的形态**不许**进机检", got)
	}
	// ★★ **订正横幅里的【历史数】不许被当成当前计数**（★ 本轮实测撞出来的脆弱性）：
	// 单元里既有当前数（6），又有「原写「5 测试」」的历史引用 ⇒ 必须读到 **6**。
	got = macdcParseCountClaim("**6 测试** / **8 判据** ★ ★ **订正（2026-10-25）**：★ 原写「5 测试」⇒ ★★ 实测 6")
	if got.tests != 6 {
		t.Errorf("★ 订正横幅里的**历史数**被当成了当前计数（读到 %d，want 6）⇒ 读数会随横幅措辞漂 ⇒ 假红", got.tests)
	}
	// ★★ 同款：自检数与断言数也必须只从**当前声明段**读。
	got = macdcParseCountClaim("**4 判据** ＋ **1 自检（9 条断言）** ★ ★ **订正（2026-10-25）**：★ 原写「4 条断言」⇒ ★★ 实测 9")
	if got.selfChecks != 1 || got.assertions != 9 {
		t.Errorf("★ 订正横幅污染了自检数/断言数（%+v）⇒ 应当只读当前声明段", got)
	}
	// ⑤ ★ 行解析：★ 必须跳过分隔行、**跳过空行**（★ §四 的表中间就有一行空行）、
	//    且必须只取 5 列 ⇒ ★★ 与完整性门**同口径**（★ 否则两门看的不是同一张表）。
	rows, err := macdcParseSection4("## 四、门禁清单与变异验证\n\n" + macdcSection4Header + "\n|---|---|---|---|---|\n" +
		"| 门A | `a/gate_test.go` | 6 | CI | M1 |\n" +
		"\n" +
		"| 门B | `b/gate_test.go` + `c/other_test.go` | 2 | CI | M2 |\n")
	if err != nil {
		t.Fatalf("★ 解析 §四 失败 ⇒ %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("★ 行数 = %d，want 2（★ 分隔行必须被跳过、★ **空行后面的行也必须被读到**）", len(rows))
	}
	if rows[0].name != "门A" || rows[0].location != "`a/gate_test.go`" || rows[0].count != "6" {
		t.Errorf("★ 行解析错位：%+v", rows[0])
	}
	if !strings.Contains(rows[1].location, "b/gate_test.go") || !strings.Contains(rows[1].location, "c/other_test.go") {
		t.Errorf("★ 多文件行的「位置」列被截断了：%+v", rows[1])
	}
}

// TestMagpieAuditCountColumnCountersHaveTeeth 是 C4 的配套：★ 三个计数器都必须是**AST 事实**。
func TestMagpieAuditCountColumnCountersHaveTeeth(t *testing.T) {
	dir := t.TempDir()

	// ① ★ `func TestXxx` 只数**顶层函数声明**；★ 注释里的不算（★ 注释骗不过）。
	src := `package p

func TestReal(t *testing.T) {}

// func TestComment(t *testing.T) {}

func helper(t *testing.T) {}
`
	path := filepath.Join(dir, "a_test.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := macdcCountTestFuncs(t, path); n != 1 {
		t.Errorf("★ `func TestXxx` 计数 = %d，want 1（★ 注释里的不算、helper 不算）", n)
	}

	// ② ★ 断言计数：★ 四种都算；★ 别的（★ `t.Logf` / `x.Errorf`）不算。
	src2 := `package p

import "testing"

func TestX(t *testing.T) {
	t.Error("a")
	t.Errorf("b")
	t.Fatal("c")
	t.Fatalf("d")
	t.Logf("e")
}
`
	path2 := filepath.Join(dir, "b_test.go")
	if err := os.WriteFile(path2, []byte(src2), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := macdcCountAssertionCalls(t, path2, "TestX"); n != 4 {
		t.Errorf("★ 断言计数 = %d，want 4（★ `t.Logf` 不算）", n)
	}
	if macdcHasFunc(t, path2, "TestNope") {
		t.Errorf("★ `macdcHasFunc` 报了一个不存在的函数存在")
	}
}
