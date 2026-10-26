package ursm_test

// magpie_p2_v2_criteria_test.go —— P2-1 的门：**v2 切换判据 + 行为差异清单**。
//
// # 这道门守住哪块缺口
//
// 计划书 §6-P2-1：「把『默认关』当**未完成**而不是『关闭』：给出切换判据
// （哪些能力必须在 v2 里、哪些可留 v1），并明确『两套路径并存』期间的行为差异清单」
// ⇒ 验收② 是「**每条差异有测试覆盖**」。
//
// ★ 若判据与差异写在 Markdown 里，这句话就只是一句**承诺**。
// ⇒ 本门把两者都当**代码**来校验：
//
//	① 判据清单非空、字段齐全、归属要求在词表内、ID 唯一；
//	② ★ **每条判据的 `Evidence` 必须逐字出现在生产文件里**
//	   （⇒ 判据不能凭空写，判据与实现不会悄悄分家）；
//	③ 差异清单非空、字段齐全、ID 唯一；
//	④ ★ **每条差异的 `TestName` 必须在仓内真实存在**
//	   （⇒ 「每条差异有测试覆盖」成为机器可校验的事实）；
//	④' ★ **每条差异的 `ExtraTestNames` 也必须真实存在**
//	   （⇒ 一条差异的多个语义侧面不会悄悄失去覆盖）；
//	④'' ★ **每条差异的 `RequiredAssertions` 必须逐字出现在【它所归位的那个测试】的
//	   【代码面】里**（⇒ 「测试存在」之外还要「**代码里真的断言了这件事**」；
//	   ★ 代码面 = go/parser 解析出的语料，★ **分两面**（★ 表达式面 / ★ 字面量面），
//	   ★ **注释不计入**；★ 匹配语义是**精确相等**（★ **不是**子串包含）；
//	   ★ 且**逐函数归位** ⇒ A 测试里的一个 `ParseInt` 不能替 B 语义背书）；
//	④''' ★ 代码面是**逐函数**取的（★ **不是**整文件 —— ★ 2026-10-24 第四轮
//	   verifier 实测撞出来的假牙）：语料 = 该测试函数**自身**的函数体
//	   + 它**直接调用**的同文件普通函数（**传递可达**）
//	   ⇒ ⇒ ① 同文件里**别的**函数的字串**进不来**
//	   （★ 于是「把断言从测试里搬走、留在同文件的 helper 里」**蒙混不过去**）；
//	   ⇒ ⇒ ② 被调用 helper 里的断言**算数**
//	   （★ 否则「真正的断言写在 helper 里」这种**正常**写法会被误杀）。
//	⑤ ★ 两条**代码事实**仍然成立：4 个端点都被包装；阶段错误不阻断；默认关。
//
// # ★ 被测集合与期望都不来自被测对象
//
// 期望值是本文件里的**字面量**（端点路径、字符串片段）⇒ 改实现改不掉断言。
//
// ★ 而「期望值」有一部分住在 `internal/pipelinev2` 的清单里（RequiredAssertions）
// ⇒ 那不是自证 ⇒ ⇒ 门**独立地**把那些字面量拿到**代码面**上去核对
// ⇒ ⇒ 清单里写一句空话（例如把断言写成一个只出现在注释里的字符串）⇒ 门红。

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/pipelinev2"
)

// ★★★ strongGuaranteeRequired 是**必须**享有强保证（**代码面**核对语义断言）的差异 ID 列表。
//
// ★★★ ★★ 它住在**门文件**里、**不在清单里** ⇒ 这是关键：★
// ★★★ 清单侧（`internal/pipelinev2`）负责声明「哪些差异有强保证」，
// ★★★ 门侧负责钉住「**这几条**必须有强保证」⇒ ⇒ 两边**互为对手**。
//
// ★★★ ★★ 为什么按【ID】钉、而不是按【数量】钉（★ 2026-10-24 第三轮 verifier 实测撞出来的）：
// ★★★ ★★ 数量地板（`strong >= N`）只保**数量**、不保**对象** ⇒ ⇒
// ★★★ ★★ 把某条差异的断言**迁移**到别的差异（并登记进 `ExtraTestNames`）⇒
// ★★★ ★★ `strong` 仍是 N ⇒ 地板满足 ⇒ **门绿** ⇒ ★★ 而被迁走的那条已掉回弱保证
// ★★★ ★★ ⇒ ⇒ 「代码面核对」这个能力**名存实亡** ⇒ ⇒ **迁移攻击**放行。
// ★★★ ★★ ⇒ ⇒ 按 ID 钉 ⇒ 删掉**或迁走**该差异的断言 ⇒ **该 ID 不再有 `RequiredAssertions`**
// ★★★ ★★ ⇒ ⇒ **门红** ⇒ ⇒ 两个残余（删除 / 迁移）**一次堵死**。
//
// ★★★ ⚠ **诚实的边界**（★ 登记在审计档 §五）：★ 改这个列表 = 改断言 ⇒ 必须动门文件
// ★★★ ⇒ **git diff 可见**（★ 这是**流程性**论证，**不是**技术互锁）⇒ ⇒ 门**挡不住**有人
// ★★★ 显式把它改掉 ⇒ ★⇒ 所以「恒绿路径已堵」这句话**只对清单侧成立**，不护门侧。
var strongGuaranteeRequired = []string{"D09"}

// ★★★ strongGuaranteeAssertions 是「强保证差异」的**期望断言表**（★ **门侧**）。
//
// ★★★ ★★ 为什么门侧要留**一份**（★ 而不只是「清单侧声明了什么就核什么」）：
// ★★★ ★★ 清单侧（`internal/pipelinev2`）是**被审对象** ⇒ 它可以：
// ★★★ ★★ ① 把断言串**弱化**成任意在语料里出现的串（★ 实测：全改成 `t` ⇒ 门绿）；
// ★★★ ★★ ② 把断言列表**清空**（★ 实测：map 留着、列表清空 ⇒ 门**照样**绿
// ★★★ ★★   ⇒ 因为 `range` 零次执行 ⇒ 「零条语义断言被核对」却仍被计为**强保证**）。
// ★★★ ★★ ⇒ ⇒ 门侧必须留一份**独立的期望**，与清单侧**逐条对拍**
// ★★★ ★★ ⇒ ⇒ 这与 C8（认证表 ↔ 设计页）是**同款**手法：★ **两份权威 + 第三方对账**。
//
// ★★★ ★★ 而且它**顺手堵住「门侧换 ID」**：★ 门侧期望表**按 ID 索引** ⇒
// ★★★ ★★ 把 `strongGuaranteeRequired` 里的 `D09` 换成 `D01` ⇒ ★ `D01` 在期望表里
// ★★★ ★★ **没有条目** ⇒ ⇒ **门红**（★ 实测 M72：旧门 RC=0 绿）。
//
// ★★★ ★★ 判据是「清单侧必须**逐条包含**门侧的期望」⇒ ★ **允许**清单侧**加强**
// ★★★ ★★ （多登记断言）⇒ ⇒ **不会**因为「想多写一条断言」而假红。
var strongGuaranteeAssertions = map[string]map[string][]string{
	"D09": {
		"TestV2DispatchRequestIDPrefersServerID": {
			`got.envelopeID != serverID`,
			`got.headerID != serverID`,
		},
		"TestV2DispatchRequestIDSynthesizesAndWritesBack": {
			`"v2pipe-"`,
			`ParseInt`,
			`got.headerID != got.envelopeID`,
		},
	},
}

// magpieP2RepoRoot 返回仓根目录。
func magpieP2RepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// magpieP2ReadFile 读仓内相对路径的文件。
func magpieP2ReadFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	return string(b)
}

// magpieP2ParseFile 解析仓内一个相对路径的 Go 文件。
func magpieP2ParseFile(t *testing.T, root, rel string) (*token.FileSet, *ast.File) {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败（⇒ 门无法取它的代码面）：%v", rel, err)
	}
	return fset, f
}

// magpieP2Corpus 是「代码面」语料，★ **分两面**。
//
// # ★★★ 为什么必须分两面（★ 2026-10-25 第五轮 verifier 实测撞出来的 F5）
//
// ★★★ 之前语料是**一锅**（★ BasicLit + BinaryExpr + CallExpr + Ident 混在一起）
// ★★★ ⇒ ★★ 于是**字符串字面量**可以冒充**表达式**：
// ★★★   `_ = "got.envelopeID != serverID"`（★ 一个**无意义的字符串**）
// ★★★   ⇒ ★★ 语料里有 `got.envelopeID != serverID` 这个**子串** ⇒ 门认账
// ★★★   ⇒ ★★ 而真正的 `if got.envelopeID != serverID { … }` 断言**已经不在了**。
// ★★★ ⇒ ★★★ 实测的**决定性证据**（★ 第五轮 verifier 的 X5 / Z1）：
// ★★★   ① 把 D09 的三条断言**全部**改成字符串字面量；
// ★★★   ② 同时**破坏生产代码**（★ 删掉 `r.Header.Set` 写回）
// ★★★   ⇒ ★★ **测试绿**（★ 因为断言已被架空）★ 且 **门也绿**
// ★★★   ⇒ ⇒ ★★ 「写回 header 必须与 envelope 同值」这条**最要害**的契约
// ★★★     在**门全绿**的情况下被废掉。
// ★★★   ★★ **对照组**（★ 只破坏生产、断言完好）⇒ 测试 **红** ⇒ ★★ 说明
// ★★★   测试**本身**有牙 ⇒ ★★ 坏掉的是**门的判据**（★ 语料**来源**含 BasicLit）。
//
// ★⇒ ⇒ 正解：★ 语料**分两面** ——
// ★ ① **表达式面**（`expr`）：AST **表达式节点**的规范文本 + **非字符串**字面量；
// ★ ② **字面量面**（`lit`）：**字符串**字面量（★ 存**带引号的原始值**）。
// ★⇒ ⇒ 而「某条期望断言该落在哪一面」由**它自身的形态**决定（★ 见 `magpieP2FaceFor`）：
// ★ ★ **带双引号**的期望（如 `"v2pipe-"`）⇒ 落**字面量面**；
// ★ ★ **其余**（表达式 / 标识符 / 数字）⇒ 落**表达式面**。
// ★⇒ ⇒ ★★ 于是「用字符串冒充表达式」**不可能**：★ 那个串只在**字面量面**里。
//
// # ★★★ 匹配语义：**精确相等**，不是子串包含（★ 分面之后的**第二层**）
//
// ★★★ 「分两面」只堵住「用**裸字面量**冒充表达式」这一种打法 ⇒ ★★ 还有一种：
// ★★★ 把断言换成 `t.Logf("got.envelopeID != serverID")` ⇒ ★★ 它是**调用表达式**
// ★★★ ⇒ ★★ 若匹配语义是**子串包含**，那个**调用表达式打印文本**里就**含**着 needle
// ★★★ ⇒ ★★ 门认账 ⇒ ★★ 而测试**照样绿**（Logf 不会让测试失败）⇒ ★★ 契约再次被架空。
// ★★★ ⇒ ⇒ 正解：★ 语料存的是**节点文本的集合**，匹配取**精确相等**
// ★★★ ⇒ ★★ ⇒ `t.Logf("…")` 的整体文本 ≠ needle ⇒ **不认账** ⇒ ⇒ 门红。
type magpieP2Corpus struct {
	// expr 是**表达式面**语料：表达式节点的**规范文本** + 非字符串字面量。
	expr map[string]bool
	// lit 是**字面量面**语料：**字符串**字面量的**原始值**（★ 含引号）。
	lit map[string]bool
}

// magpieP2FaceExpr / magpieP2FaceLit 是两个「面」的标识。
const (
	magpieP2FaceExpr = "expr"
	magpieP2FaceLit  = "lit"
)

// magpieP2Normalize 折叠文本里的**多余空白** ⇒ 打印器的换行 / 缩进差异不参与匹配。
func magpieP2Normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// magpieP2NodeText 打印一个 AST 节点并**规范化**（★ 折叠空白）。
func magpieP2NodeText(fset *token.FileSet, n ast.Node) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, n); err != nil {
		return ""
	}
	return magpieP2Normalize(b.String())
}

// magpieP2FaceFor 按「期望断言**自身的形态**」决定它该落在哪一面。
//
// ★★ 判据只看**形态**（★ 带不带双引号）⇒ ★ 不需要清单侧声明归属
// ★★ ⇒ ★ 清单侧**一个字都不用改**。
func magpieP2FaceFor(req string) string {
	if len(req) >= 2 && strings.HasPrefix(req, `"`) && strings.HasSuffix(req, `"`) {
		return magpieP2FaceLit
	}
	return magpieP2FaceExpr
}

// magpieP2CorpusHas 判断某条期望断言是否**逐字**落在它**该在**的那一面。
//
// ★★ 语义是**精确相等**（★ **不是**子串包含）⇒ ★★ 「调用表达式打印文本里恰好含着 needle」
// ★★ 这种**夹带**打法**不再**能蒙混过关（★ 见 `magpieP2Corpus` 的说明）。
func magpieP2CorpusHas(c magpieP2Corpus, req string) bool {
	switch magpieP2FaceFor(req) {
	case magpieP2FaceLit:
		return c.lit[req]
	default:
		return c.expr[magpieP2Normalize(req)]
	}
}

// magpieP2IsFailingCall 判断一个调用是不是「**能让测试失败**」的调用。
//
// ★★★ 判据 = ★ `t.Error*` / `t.Fatal*`（★ 前缀形态 ⇒ 含 `Errorf` / `Fatalf` / `Error` / `Fatal`）。
// ★★ 只认**接收者是 `t`**（★ `testing.T` 的惯例名）⇒ ⇒ `log.Errorf` 之类**不算**
// ★★ （★ 它们不会让测试变红 ⇒ ⇒ 不该把那里的表达式算成「活的」）。
func magpieP2IsFailingCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok || recv.Name != "t" {
		return false
	}
	name := sel.Sel.Name
	return strings.HasPrefix(name, "Error") || strings.HasPrefix(name, "Fatal")
}

// magpieP2HasFailingCall 判断一棵子树里有没有「能让测试失败」的调用。
func magpieP2HasFailingCall(n ast.Node) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if found {
			return false
		}
		if call, ok := node.(*ast.CallExpr); ok && magpieP2IsFailingCall(call) {
			found = true
			return false
		}
		return true
	})
	return found
}

// magpieP2LiveExprs 收集「**能导致测试失败**」的位置上的表达式（★ **活性面**）。
//
// ★★★★ 这是「**存在性 ≠ 活性**」的**第二层**（★ 2026-10-25 第六轮 verifier 的
// ★★★★ S1 / S2 实测撞出来的）：★ 语料只收「**出现过**」的节点 ⇒ ★★ 于是
// ★★★★ ① 「**求值即丢弃**」（★ `_ = got.headerID != got.envelopeID`）——
// ★★★★    表达式**原文还在** ⇒ ★★ 门**认账** ⇒ ★★ 而测试**再也不断言**这件事；
// ★★★★ ② 「**no-op 字面量**」（★ `_ = "v2pipe-"`）—— 同款 ⇒ ★★ 契约被架空。
// ★★★★ ⇒ ⇒ 两发都能让「门」与「测试」**同时绿** ⇒ ⇒ ★★ 必须把语料收窄到
// ★★★★ **「能让测试失败的位置」**。
//
// ★★★★ 三条规则：
// ★ R1 ★ `if` / `switch` / `for` 的**条件**，★ 若它的分支里有失败调用 ⇒ 条件**活**；
// ★ R2 ★ 失败调用的**实参** ⇒ 活；
// ★ R3 ★ 赋值的**右侧**，★ 若它的左侧变量出现在**活性面**里 ⇒ 活（★ 定点迭代）。
//
// ★★★★ 为什么必须有 R3：★ 形如 `ns, err := strconv.ParseInt(raw, 10, 64)`
// ★★★★ 的调用**本身**不在任何条件里 ⇒ ★★ 只有 R1 / R2 时它**不算活**
// ★★★★ ⇒ ★★ 「断言『纳秒部分能被解析成整数』」这条契约就**核不到**（★ 假红）。
// ★★★★ ⇒ ★★ R3 把「结果变量喂给了断言」这条链接上 ⇒ ★★ 调用**活** ⇒ ★★ 契约可核。
func magpieP2LiveExprs(body *ast.BlockStmt) []ast.Expr {
	var live []ast.Expr
	liveIdents := map[string]bool{}
	add := func(e ast.Expr) {
		if e == nil {
			return
		}
		live = append(live, e)
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				liveIdents[id.Name] = true
			}
			return true
		})
	}

	// R1 ★ 条件（★ 分支里有失败调用 ⇒ 这个条件能决定测试成败）。
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IfStmt:
			if x.Cond != nil && (magpieP2HasFailingCall(x.Body) || magpieP2HasFailingCall(x.Else)) {
				add(x.Cond)
			}
		case *ast.SwitchStmt:
			if x.Tag != nil && magpieP2HasFailingCall(x.Body) {
				add(x.Tag)
			}
		case *ast.ForStmt:
			if x.Cond != nil && magpieP2HasFailingCall(x.Body) {
				add(x.Cond)
			}
		}
		return true
	})

	// R2 ★ 失败调用的实参（★ 失败消息里的每个值都在断言什么）。
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !magpieP2IsFailingCall(call) {
			return true
		}
		for _, a := range call.Args {
			add(a)
		}
		return true
	})

	// R3 ★ 赋值的右侧（★ 左侧变量出现在活性面里 ⇒ 这个值**喂给了**断言）。
	for iter := 0; iter < 8; iter++ {
		changed := false
		ast.Inspect(body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, rhs := range as.Rhs {
				hit := false
				for _, lhs := range as.Lhs {
					// ★★ **必须排除 `_`**（★ 空白标识符）：★ 否则只要活性面里
					// ★★ 出现过 `_`（★ 例如某个实参里），★ `_ = X` 这种
					// ★★ 「求值即丢弃」的打法就会**被当成活的** ⇒ ★★ 漏洞。
					if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" && liveIdents[id.Name] {
						hit = true
						break
					}
				}
				if !hit {
					continue
				}
				before := len(liveIdents)
				add(rhs)
				if len(liveIdents) != before {
					changed = true
				}
			}
			return true
		})
		if !changed {
			break
		}
	}
	return live
}

// magpieP2FuncCorpus 提取**某一个函数**的**代码面**语料（★ 分两面）。
//
// ★★ 这是本门最要紧的一处设计：语料只由 **AST 节点**拼成 ——
// ★★ ① **表达式面**：表达式节点（BinaryExpr / UnaryExpr / CallExpr /
// ★★    SelectorExpr / IndexExpr，用 go/printer 打印成规范格式）+ 标识符名
// ★★    + **非字符串**字面量（数字 / 字符）；
// ★★ ② **字面量面**：**字符串**字面量（BasicLit.Value，★ 原始值，★ 含引号）。
// ⇒⇒ **注释不是 AST 节点** ⇒⇒ 注释里的任何字串都**进不了**语料。
//
// ★⇒ 于是「在注释里写一句『我断言了 X』」蒙混不过去 ——
// ★⇒ 而**这正是**「测试存在但名不符实」这个门抓不到的缺陷的解法。
//
// ★ 反过来：★ 判据**宁可假红也不假绿** ——
// ★ 「代码面」比「全文」窄 ⇒ 命中要求更严 ⇒ 假绿被根除。
//
// # ★★★ 粒度：**逐函数**，不是整文件（★ 2026-10-24 第四轮 verifier 实测撞出来的）
//
// ★★★ 之前这里取的是**整文件**语料（`ast.Inspect(f, …)`）⇒ ★★ 而门自己的文件头
// ★★★ 写着「★ 且**逐测试归位** ⇒ A 测试里的一个 `ParseInt` 不能替 B 语义背书」
// ★★★ ⇒ ★★ 那句话**在【同一文件内的两个测试】上是不真的** ⇒ ★★ 且 D09 的两个测试
// ★★★ **恰好在同一文件**里 ⇒ ★★ 正是唯一享有强保证的那条差异踩在假牙上。
//
// ★★★ 具体的洞（可复现）：把 `TestV2DispatchRequestIDSynthesizesAndWritesBack` 里的
// ★★★ `assertSynthesizedNano(t, got.envelopeID, got.beforeNano, got.afterNano)`
// ★★★ **这一行调用删掉**（helper 函数本身留在文件里）⇒ ★★
// ★★★   ① 语料（整文件）**几乎不变** ⇒ 门**绿**；
// ★★★   ② 而该测试**再也不断言**「合成值是纳秒」这半条契约了（★ 契约里最要害的那半条）。
// ★★★ ⇒ ⇒ 「代码面核对语义断言」这个能力**名存实亡**。
//
// ★★★ 正解：**语料 = 该函数自身的函数体 + 它【直接调用】的同文件普通函数（传递可达）**。
// ★★★ ⇒ ⇒ ① 同文件里**别的**函数的字串**进不来**
// ★★★   （★ 于是「把断言从测试里搬走、留在同文件 helper 里」**蒙混不过去** ——
// ★★★    helper 不再被调用 ⇒ 不可达 ⇒ 语料里没有它的断言 ⇒ 门红）；
// ★★★ ⇒ ⇒ ② 被调用 helper 里的断言**算数**
// ★★★   （★ 否则「真正的断言写在 helper 里」这种**正常**写法会被误杀 ⇒ 假红）。
//
// ★★★ 只跟随**普通函数**（`FuncDecl.Recv == nil`）且只跟随**直接调用**
// ★★★（`CallExpr.Fun` 是 `*ast.Ident`）⇒ ⇒
// ★★★   ① **不**跟随方法（★ `hook.Name()` 的 `Name` 不是普通函数）；
// ★★★   ② **不**跟随选择器调用（★ `strings.HasPrefix` 之类标准库**不**进语料）。
func magpieP2FuncCorpus(fset *token.FileSet, f *ast.File, funcName string) (magpieP2Corpus, bool) {
	// 同文件的普通函数表（★ 方法不进这张表）。
	funcs := map[string]*ast.FuncDecl{}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			funcs[fd.Name.Name] = fd
		}
	}
	start, ok := funcs[funcName]
	if !ok {
		return magpieP2Corpus{}, false
	}

	c := magpieP2Corpus{expr: map[string]bool{}, lit: map[string]bool{}}
	seen := map[*ast.FuncDecl]bool{}
	queue := []*ast.FuncDecl{start}
	for len(queue) > 0 {
		fd := queue[0]
		queue = queue[1:]
		if seen[fd] {
			continue
		}
		seen[fd] = true
		if fd.Body == nil {
			continue
		}

		// ★★★ ① 剪掉「恒假分支」里的子树（★ 存在性 ≠ 活性）。
		// ★★★ ⇒ 「用 `false &&` 把断言废掉」这类打法**不再**能蒙混过关。
		magpieP2PruneDeadBranches(fd.Body)

		// ② 收集本函数的**活性**代码面语料（★ ★★ 分两面 + ★ **精确相等**匹配
		//    + ★★★★ **只收「能导致测试失败」的位置上的节点**）。
		// ★ ★★ 字符串字面量**只**进字面量面 ⇒ ⇒ 「用字符串冒充表达式」不可能。
		// ★ ★★ 数字 / 字符字面量是**表达式级**的原子 ⇒ 归表达式面。
		//
		// ★★★★ ★★ 「**只收活的位置**」是「存在性 ≠ 活性」的**第二层**
		// ★★★★ （★ 第六轮 verifier 的 S1 / S2 实测撞出来的）：★ 旧口径只问
		// ★★★★ 「这个表达式**出现过**吗」⇒ ★★ 「求值即丢弃」（`_ = X`）与
		// ★★★★ 「no-op 字面量」（`_ = "v2pipe-"`）都能让**门与测试同时绿**
		// ★★★★ ⇒ ★★ 现在只收「**能让测试失败**」的位置上的节点 ⇒ ★★ 两发都被堵。
		for _, live := range magpieP2LiveExprs(fd.Body) {
			ast.Inspect(live, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						c.lit[v.Value] = true
					} else {
						c.expr[v.Value] = true
					}
				case *ast.Ident:
					c.expr[v.Name] = true
				case *ast.BinaryExpr:
					c.expr[magpieP2NodeText(fset, v)] = true
				case *ast.UnaryExpr:
					c.expr[magpieP2NodeText(fset, v)] = true
				case *ast.CallExpr:
					c.expr[magpieP2NodeText(fset, v)] = true
				case *ast.SelectorExpr:
					c.expr[magpieP2NodeText(fset, v)] = true
				case *ast.IndexExpr:
					c.expr[magpieP2NodeText(fset, v)] = true
				}
				return true
			})
		}

		// ③ 跟随本函数**直接调用**的同文件普通函数（★ 传递可达）。
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				// ★ 选择器调用（`pkg.Fn` / `recv.Method`）**不**跟随。
				return true
			}
			if callee, ok := funcs[id.Name]; ok && !seen[callee] {
				queue = append(queue, callee)
			}
			return true
		})
	}
	return c, true
}

// magpieP2PruneDeadBranches 原地剪掉「恒假分支」里的子树。
//
// ★★★ 这是「**存在性 ≠ 活性**」的解法（★ 2026-10-24 第四轮 verifier 的 M90）：
// ★★★ 语料是**子串匹配** ⇒ 只要那个串**出现在代码里**，门就认账
// ★★★ ⇒ ⇒ 「用 `false &&` 把断言废掉」这种打法本来能蒙混过关
// ★★★ ⇒ ⇒ ★ 这里把**永不执行**的分支剪掉 ⇒ 死代码里的字串**不再进语料** ⇒ ⇒ 门抓得到。
//
// ★ 只认**编译期常量**的假（★ `true` / `false` 两个标识符）⇒ ★ 动态条件**不剪**
// ★ ⇒ ⇒ **不会**误杀「运行时才为假的条件」里的真实断言。
//
// ★ 剪枝的四种形态：
// ① `false && X` ⇒ X 那半边永不求值 ⇒ 剪掉；
// ② `true || X`  ⇒ X 那半边永不求值 ⇒ 剪掉；
// ③ `if false { … }` ⇒ Body 是死代码 ⇒ 剪掉（★ Else 仍是活的）；
// ④ `if true { … } else { … }` ⇒ Else 是死代码 ⇒ 剪掉。
//
// ★★ 并且：**整个条件恒假的 `if`** ⇒ 其 Body 一并剪掉
// ★★ （★ 否则 `if false && X { … }` 里的 `X` 会被 `if` 的 Body 重新带进语料）。
func magpieP2PruneDeadBranches(n ast.Node) {
	ast.Inspect(n, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.BinaryExpr:
			switch x.Op {
			case token.LAND:
				if magpieP2IsStaticallyFalse(x.X) {
					x.Y = &ast.Ident{Name: "_"}
				}
			case token.LOR:
				if magpieP2IsStaticallyTrue(x.X) {
					x.Y = &ast.Ident{Name: "_"}
				}
			}
		case *ast.IfStmt:
			switch {
			case magpieP2IsStaticallyFalse(x.Cond):
				x.Body = &ast.BlockStmt{}
			case magpieP2IsStaticallyTrue(x.Cond):
				x.Else = nil
			}
		}
		return true
	})
}

// magpieP2IsStaticallyFalse 判断一个表达式在**编译期**是否恒假。
func magpieP2IsStaticallyFalse(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "false"
	case *ast.BinaryExpr:
		switch x.Op {
		case token.LAND:
			// A && B 恒假 ⇒ A 恒假 **或** B 恒假
			return magpieP2IsStaticallyFalse(x.X) || magpieP2IsStaticallyFalse(x.Y)
		case token.LOR:
			// A || B 恒假 ⇒ A 恒假 **且** B 恒假
			return magpieP2IsStaticallyFalse(x.X) && magpieP2IsStaticallyFalse(x.Y)
		}
	}
	return false
}

// magpieP2IsStaticallyTrue 判断一个表达式在**编译期**是否恒真。
func magpieP2IsStaticallyTrue(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "true"
	case *ast.BinaryExpr:
		switch x.Op {
		case token.LAND:
			// A && B 恒真 ⇒ A 恒真 **且** B 恒真
			return magpieP2IsStaticallyTrue(x.X) && magpieP2IsStaticallyTrue(x.Y)
		case token.LOR:
			// A || B 恒真 ⇒ A 恒真 **或** B 恒真
			return magpieP2IsStaticallyTrue(x.X) || magpieP2IsStaticallyTrue(x.Y)
		}
	}
	return false
}

// magpieP2MissingAssertions 报告「哪些期望断言**不在**语料里」。
//
// ★★★ 把它**抽成独立函数**（★ 而不是内联在测试里）是**刻意的**：
// ★★★ ⇒ ⇒ 可以给它写一道**量具自检**（★ 仓内既有惯例：★ `TestCertRowRangeExtractorHasTeeth`
// ★★★ / `TestPendingCountExtractorsHaveTeeth` / `TestStaleBannerDetectorHasTeeth`）
// ★★★ ⇒ ⇒ ★ 「把断言比对这一步废掉」（★ 第四轮 verifier 的 M91：★
// ★★★ ⇒ ⇒ 　 `if false && !magpieP2CorpusHas(corpus, req)` ⇒ 旧门**绿**）
// ★★★ ⇒ ⇒ 　 这时**自检会红** ⇒ ⇒ 「比对这一步还在」这件事**有了牙齿**。
func magpieP2MissingAssertions(c magpieP2Corpus, asserts []string) []string {
	var missing []string
	for _, req := range asserts {
		if !magpieP2CorpusHas(c, req) {
			missing = append(missing, req)
		}
	}
	return missing
}

// magpieP2TestCodeCorpus 取「某个测试函数」的代码面语料；
// ★ 找不到该函数 ⇒ 第二个返回值为 false（★ 调用方必须报错，**不许**当成空语料放行）。
func magpieP2TestCodeCorpus(t *testing.T, root, rel, testName string) (magpieP2Corpus, bool) {
	t.Helper()

	fset, f := magpieP2ParseFile(t, root, rel)
	return magpieP2FuncCorpus(fset, f, testName)
}

// TestMagpieP2FuncCorpusIsPerFunction 是**提取器量具自检**。
//
// ★★ 为什么需要它：★ 提取器的粒度（**逐函数** vs **整文件**）是本门全部牙齿的根，
// ★★ 而粒度**退化了不会让任何现有断言变红**（★ 因为整文件语料是逐函数语料的**超集**）
// ★★ ⇒ ⇒ 只靠「门红」永远发现不了「粒度被改回整文件」⇒ ⇒ 必须有一道**自检**钉住粒度。
//
// ★★ 这也是本仓库的既有纪律（★ 见 `TestCertRowRangeExtractorHasTeeth` /
// ★★ `TestPendingCountExtractorsHaveTeeth`）：★ **提取器本身也要被量具量一遍**。
func TestMagpieP2FuncCorpusIsPerFunction(t *testing.T) {
	// ★ 合成源码：★ 两个测试 + 两级 helper，**都在同一个文件里**
	// ★ ⇒ ★★ 正是「整文件语料」会出错的形态。
	// ★★ 注意：★ 合成源码里的每个 marker 都必须处在「**能让测试失败**」的位置上
	// ★★ （★ `t.Errorf` / `t.Fatalf` 的分支里）⇒ ★★ 否则新的「活性」口径会把它剪掉
	// ★★ ⇒ ★★ 本自检要量的仍是「**逐函数**粒度」（★ 不是「活不活」）。
	const src = `package p

import "strconv"

func helperDeep() string {
	if deepMarker != 42 {
		t.Errorf("deep")
	}
	return strconv.Itoa(42)
}

func helperMid() string {
	if helperDeep() != "" {
		t.Errorf("mid")
	}
	return helperDeep()
}

func TestAlpha(t *testing.T) {
	if helperMid() != "" {
		t.Errorf("alpha")
	}
}

func TestBeta(t *testing.T) {
	_ = "only-in-beta"
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}

	alpha, ok := magpieP2FuncCorpus(fset, f, "TestAlpha")
	if !ok {
		t.Fatal("提取器在合成源码里找不到 TestAlpha ⇒ 提取器坏了")
	}
	beta, ok := magpieP2FuncCorpus(fset, f, "TestBeta")
	if !ok {
		t.Fatal("提取器在合成源码里找不到 TestBeta ⇒ 提取器坏了")
	}

	// ① ★ 直接调用的 helper 必须在语料里（★ 标识符 ⇒ 表达式面）。
	if !alpha.expr["helperMid"] {
		t.Errorf("TestAlpha 直接调用了 helperMid ⇒ 它的语料里必须有 helperMid（表达式面 %d 条）", len(alpha.expr))
	}
	// ② ★★★ **传递可达**的 helper 也必须在语料里
	//    （★ 否则「真正的断言写在 helper 里」这种正常写法会被误杀 ⇒ 假红）。
	if !alpha.expr["helperDeep"] {
		t.Errorf("TestAlpha → helperMid → helperDeep 是**传递可达**的 ⇒ helperDeep 必须进语料")
	}
	// ★ 数字字面量是**表达式级**的原子 ⇒ 归**表达式面**（★ 不是字面量面）。
	if !alpha.expr["42"] {
		t.Errorf("helperDeep 里的数字字面量 42 必须**传递**进 TestAlpha 的表达式面")
	}
	// ③ ★★★ **别的**函数的独有字串**不得**进语料
	//    （★ 这一条就是「逐函数」的牙齿：★ 整文件语料会让它进来）。
	if alpha.lit[`"only-in-beta"`] {
		t.Errorf("★ 提取器是**整文件**级的 ⇒ TestBeta 的独有字串混进了 TestAlpha 的语料")
	}
	if alpha.expr["TestBeta"] {
		t.Errorf("★ 提取器是**整文件**级的 ⇒ 别的测试函数名混进了 TestAlpha 的语料")
	}
	// ④ ★ 反向：TestBeta 的语料里**不得**有 TestAlpha 那条链上的东西。
	for _, forbidden := range []string{"helperMid", "helperDeep", "42"} {
		if beta.expr[forbidden] {
			t.Errorf("★ 提取器是**整文件**级的 ⇒ TestAlpha 那条链上的 %q 混进了 TestBeta 的语料", forbidden)
		}
	}
	// ⑤ ★★ 找不到的函数必须**如实**报告 false（★ 不许当成空语料放行）。
	if _, ok := magpieP2FuncCorpus(fset, f, "TestNotExist"); ok {
		t.Errorf("★ 提取器对不存在的函数返回了「找到」⇒ 调用方会拿空语料当命中")
	}
}

// TestMagpieP2DeadBranchesArePruned 是**「死分支剪枝」这一步的量具自检**。
//
// ★★★ 为什么需要它：★ 语料是**子串匹配** ⇒ 「用 `false &&` 把断言废掉」
// ★★★ 这种打法**不会**让任何现有断言变红（★ 第四轮 verifier 的 M90：★ 旧门 RC=0 绿）
// ★★★ ⇒ ⇒ 必须有一道**自检**钉住「死代码不进语料」这件事。
func TestMagpieP2DeadBranchesArePruned(t *testing.T) {
	const src = `package p

func TestGamma(t *testing.T) {
	if false && deadLandMarker != 0 {
		t.Fatal("never")
	}
	if true || deadLorMarker != 0 {
		t.Fatal("never")
	}
	if false {
		t.Fatal("deadIfMarker")
	} else {
		t.Errorf("live")
	}
	if true {
		t.Errorf("live")
	} else {
		t.Fatal("deadElseMarker")
	}
	if liveCondition != 0 {
		t.Errorf("live")
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	c, ok := magpieP2FuncCorpus(fset, f, "TestGamma")
	if !ok {
		t.Fatal("提取器在合成源码里找不到 TestGamma ⇒ 提取器坏了")
	}

	// ① ★★ 恒假/恒真分支里的标记**不得**进语料
	//    （★ 标识符形态查表达式面、★ 字符串形态查字面量面 ⇒ **两面**都查）。
	for _, dead := range []string{"deadLandMarker", "deadLorMarker", "deadIfMarker", "deadElseMarker"} {
		if c.expr[dead] || c.lit[dead] || c.lit[`"`+dead+`"`] {
			t.Errorf("★ 「%s」在**永不执行**的分支里 ⇒ 它**不得**进语料（★ 存在性 ≠ 活性）", dead)
		}
	}
	// ② ★★ **动态**条件里的内容**必须**仍在语料里
	//    （★ 否则「误杀真实断言」⇒ 假红 ⇒ ★ 比假绿更费）。
	if !c.expr["liveCondition"] {
		t.Errorf("★ 动态条件 `liveCondition` 必须在语料里（★ 剪枝只认**编译期常量**的假）")
	}
	// ③ ★★ `if false { … } else { … }` ⇒ **Else** 仍是活的 ⇒ 其内容必须在语料里。
	if !c.lit[`"live"`] && !c.expr["live"] {
		t.Errorf("★ `if false { … } else { … }` 的 Else 仍是活代码 ⇒ 其内容必须在语料里")
	}
}

// TestMagpieP2MissingAssertionsHasTeeth 是**「断言比对」这一步的量具自检**。
//
// ★★★ 为什么需要它：★ 第四轮 verifier 的 M91：★ 把门里的比对改成
// ★★★ `if false && !magpieP2CorpusHas(corpus, req)` ⇒ 旧门**绿** ⇒
// ★★★ ⇒ 「断言比对这一步还在」这件事**没有任何独立来源**能发现
// ★★★ ⇒ ⇒ 把它**抽成独立函数**（`magpieP2MissingAssertions`）+ 一道**自检**
// ★★★ ⇒ ⇒ ★ 比对这一步被废掉 ⇒ **自检会红**。
func TestMagpieP2MissingAssertionsHasTeeth(t *testing.T) {
	c := magpieP2Corpus{
		expr: map[string]bool{
			"got.envelopeID != serverID": true,
			"got.headerID != serverID":   true,
		},
		lit: map[string]bool{},
	}

	// ① 全部命中 ⇒ 不得报缺失。
	if missing := magpieP2MissingAssertions(c, []string{
		`got.envelopeID != serverID`,
		`got.headerID != serverID`,
	}); len(missing) != 0 {
		t.Errorf("★ 两条断言都在语料里 ⇒ 不该报缺失，实际报了 %v", missing)
	}
	// ② ★★ 缺一条 ⇒ 必须**如实**报出那一条（★ 这一条才是「有牙」的证明）。
	missing := magpieP2MissingAssertions(c, []string{
		`got.envelopeID != serverID`,
		`"v2pipe-"`,
	})
	if len(missing) != 1 || missing[0] != `"v2pipe-"` {
		t.Errorf("★ 比对必须如实报出唯一缺失的那一条，实际报 %v", missing)
	}
	// ③ 空断言列表 ⇒ 不得报缺失（★ 但**上层**另有「空列表 ⇒ 报错」的判据，
	//    ★ 这里只保证比对函数本身的行为是确定的）。
	if missing := magpieP2MissingAssertions(c, nil); len(missing) != 0 {
		t.Errorf("★ 空断言列表不该报缺失，实际报 %v", missing)
	}
}

// TestMagpieP2CorpusFacesAreSeparated 是**「语料分两面 + 精确相等匹配」这两步的量具自检**。
//
// ★★★ 为什么需要它（★ 2026-10-25 第五轮 verifier 的 F5 实测撞出来的）：
// ★★★ ① **分面**：★ 语料若是**一锅** ⇒ ★ 字符串字面量能冒充**表达式**
// ★★★    ⇒ `_ = "got.envelopeID != serverID"` 就能骗过门；
// ★★★ ② **精确相等**：★ 匹配若是**子串包含** ⇒ ★ 调用表达式的打印文本
// ★★★    （`t.Logf("got.envelopeID != serverID")`）里就**含**着 needle ⇒ 照样被骗。
// ★★★ ⇒ ⇒ 而这两点**退化**都**不会**让任何现有断言变红
// ★★★ ⇒ ⇒ ⇒ 必须有一道**自检**把这两点钉死。
func TestMagpieP2CorpusFacesAreSeparated(t *testing.T) {
	// ★★ 「活的位置」= ★ 能让测试失败的地方（★ `t.Error*` / `t.Fatal*`）。
	const src = `package p

func TestRealExpr(t *testing.T) {
	if alpha != beta {
		t.Errorf("live")
	}
	t.Errorf("alpha != beta")
	t.Errorf("litOnlyMarker")
	_ = "noOpMarker"
}

func TestOnlyString(t *testing.T) {
	_ = "gamma != delta"
}

func TestCallWrap(t *testing.T) {
	t.Logf("eta != theta")
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	real, ok := magpieP2FuncCorpus(fset, f, "TestRealExpr")
	if !ok {
		t.Fatal("提取器在合成源码里找不到 TestRealExpr ⇒ 提取器坏了")
	}
	onlyStr, ok := magpieP2FuncCorpus(fset, f, "TestOnlyString")
	if !ok {
		t.Fatal("提取器在合成源码里找不到 TestOnlyString ⇒ 提取器坏了")
	}
	callWrap, ok := magpieP2FuncCorpus(fset, f, "TestCallWrap")
	if !ok {
		t.Fatal("提取器在合成源码里找不到 TestCallWrap ⇒ 提取器坏了")
	}

	// ① ★★ 真正的二元表达式 ⇒ 落**表达式面**。
	if !real.expr["alpha != beta"] {
		t.Errorf("★ 真正的 `if alpha != beta { … }` 必须在表达式面里")
	}
	// ② ★★ 字符串字面量 ⇒ 落**字面量面**（★ 原始值，★ 含引号）。
	if !real.lit[`"alpha != beta"`] {
		t.Errorf("★ 字符串字面量必须在字面量面里（★ 原始值含引号）")
	}
	// ③ ★★★ **决定性的一条**：★ 只有**字符串字面量**形态时 ⇒ 表达式面**不得**命中
	//    （★ 这正是 F5 那条「把断言改成字符串」的打法 ⇒ 必须**不认账**）。
	if magpieP2CorpusHas(onlyStr, "gamma != delta") {
		t.Errorf("★★ 「_ = \"gamma != delta\"」是**字符串字面量** ⇒ 它**不得**满足表达式形态的期望\n" +
			"★★ ⇒ 「用字符串冒充表达式」的打法还能蒙混过关 ⇒ 语料的分面被破坏了")
	}
	// ④ ★★★ **夹带**：★ 调用表达式的打印文本里**恰好含**着 needle ⇒ 也不得命中
	//    （★ 这正是「匹配语义退化成子串包含」的症状）。
	if magpieP2CorpusHas(callWrap, "eta != theta") {
		t.Errorf("★★ `t.Logf(\"eta != theta\")` 的**调用表达式**文本 ≠ needle ⇒ 它**不得**被认账\n" +
			"★★ ⇒ 匹配语义退化成**子串包含**了 ⇒ 「CallExpr 夹带」的打法还能蒙混过关")
	}
	// ⑤ ★★ 纯字面量标记 ⇒ 只在**字面量面**，不得在表达式面。
	//    ★★ 两种形态都查（★ **带引号**的原文形态也要查 ⇒ ★ 否则「分面被废」
	//    （★ 字符串字面量也进表达式面）这个变异**抓不到** ⇒ ★ 门自己有假牙）。
	if real.expr["litOnlyMarker"] || real.expr[`"litOnlyMarker"`] {
		t.Errorf("★ 字符串字面量「litOnlyMarker」**不得**出现在表达式面里（★ 含引号的原文形态同样不得）")
	}
	if real.expr[`"alpha != beta"`] {
		t.Errorf("★ 字符串字面量「alpha != beta」**不得**出现在表达式面里（★ 含引号的原文形态同样不得）")
	}
	if !real.lit[`"litOnlyMarker"`] {
		t.Errorf("★ 字符串字面量「litOnlyMarker」必须在字面量面里")
	}
	// ⑦ ★★★★ **no-op 字面量**（★ `_ = "noOpMarker"`）⇒ **不得**进语料
	//    （★ 这就是第六轮 verifier 的 **S2**：★ 「求值即丢弃」的打法）。
	if real.lit[`"noOpMarker"`] || real.expr["noOpMarker"] {
		t.Errorf("★★★ `_ = \"noOpMarker\"` 是**求值即丢弃** ⇒ 它**不得**进语料\n" +
			"★★★ ⇒ 语料没做「活性」收窄 ⇒ 「no-op 字面量」的打法还能蒙混过关")
	}
	// ⑥ ★★ 「面」的归属判定：★ 带双引号 ⇒ 字面量面；★ 其余 ⇒ 表达式面。
	if got := magpieP2FaceFor(`"v2pipe-"`); got != magpieP2FaceLit {
		t.Errorf("★ 带双引号的期望必须落字面量面，实际判成 %q", got)
	}
	if got := magpieP2FaceFor(`got.envelopeID != serverID`); got != magpieP2FaceExpr {
		t.Errorf("★ 不带引号的期望必须落表达式面，实际判成 %q", got)
	}
}

// TestMagpieP2LivenessIsEnforced 是「**活性面**」这一步的量具自检。
//
// ★★★★ 为什么需要它（★ 2026-10-25 第六轮 verifier 的 **S1 / S2** 实测撞出来的）：
// ★★★★ 旧语料只问「这个表达式**出现过**吗」⇒ ★★ 于是
// ★★★★ ① 「**求值即丢弃**」（★ `_ = got.headerID != got.envelopeID`）——
// ★★★★    表达式**原文还在** ⇒ ★★ 门**认账** ⇒ ★★ 而测试**再也不断言**这件事；
// ★★★★ ② 「**no-op 字面量**」（★ `_ = "v2pipe-"`）—— 同款 ⇒ ★★ 契约被架空。
// ★★★★ ⇒ ⇒ 两发都能让「门」与「测试」**同时绿** ⇒ ⇒ ★★ 语料必须只收
// ★★★★ **「能让测试失败的位置」**上的节点 ⇒ ⇒ 这道自检钉住那一步**有牙**。
func TestMagpieP2LivenessIsEnforced(t *testing.T) {
	const src = `package p

import "strconv"

func TestLive(t *testing.T) {
	if condA != condB {
		t.Errorf("live")
	}
	_ = condC != condD
	_ = "literalOnly"
	t.Logf("notAFailure")
	ns, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("bad")
	}
	_ = ns
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	c, ok := magpieP2FuncCorpus(fset, f, "TestLive")
	if !ok {
		t.Fatal("提取器在合成源码里找不到 TestLive ⇒ 提取器坏了")
	}

	// ① ★★★★ **求值即丢弃** ⇒ 表达式**不得**进语料（★ S1 的牙）。
	if c.expr["condC != condD"] {
		t.Errorf("★★★ `_ = condC != condD` 是**求值即丢弃** ⇒ 它**不得**进语料\n" +
			"★★★ ⇒ 语料没做「活性」收窄 ⇒ 「求值即丢弃」的打法还能蒙混过关")
	}
	// ② ★★★★ **no-op 字面量** ⇒ **不得**进语料（★ S2 的牙）。
	if c.lit[`"literalOnly"`] || c.expr["literalOnly"] {
		t.Errorf("★★★ `_ = \"literalOnly\"` 是**求值即丢弃** ⇒ 它**不得**进语料\n" +
			"★★★ ⇒ 语料没做「活性」收窄 ⇒ 「no-op 字面量」的打法还能蒙混过关")
	}
	// ③ ★★ **活的位置**上的条件 ⇒ **必须**进语料（★ 否则就是「收得太紧 ⇒ 假红」）。
	if !c.expr["condA != condB"] {
		t.Errorf("★★ `if condA != condB { t.Errorf(…) }` 是**活的位置** ⇒ 它必须在语料里")
	}
	// ④ ★★★ **R3**：★ 结果变量喂给了断言 ⇒ ★ 赋值的**右侧**也**活**
	//    （★ 否则「断言纳秒能被解析成整数」这条契约就**核不到** ⇒ 假红）。
	if !c.expr["ParseInt"] {
		t.Errorf("★★★ `ns, err := strconv.ParseInt(…)` 的结果 `err` 被断言检查 ⇒ ★★ 右侧**活**\n" +
			"★★★ ⇒ `ParseInt` 必须在语料里（★ 否则这条契约就核不到）")
	}
	// ⑤ ★★★ `t.Logf` **不是**失败调用 ⇒ 它里面的字面量**不活**。
	if c.lit[`"notAFailure"`] {
		t.Errorf("★★★ `t.Logf` 不会让测试失败 ⇒ 它里面的字面量**不得**进语料\n" +
			"★★★ ⇒ 「活性」判据把「只是打印」也算成了断言 ⇒ 门会放行没牙的测试")
	}
}

// TestMagpieP2V2CriteriaAreGroundedInProduction G1 + G2：
// 判据清单非空、字段齐全、且**每条判据的依据都逐字出现在生产文件里**。
func TestMagpieP2V2CriteriaAreGroundedInProduction(t *testing.T) {
	root := magpieP2RepoRoot(t)
	list := pipelinev2.Criteria()
	if len(list) == 0 {
		t.Fatal("判据清单是空的 ⇒ 门恒绿（形同虚设）")
	}
	seen := map[string]bool{}
	for _, c := range list {
		if c.ID == "" || c.Capability == "" || c.Reason == "" || c.Evidence == "" {
			t.Errorf("判据 %+v 字段不完整", c)
			continue
		}
		if seen[c.ID] {
			t.Errorf("判据 ID 重复：%s", c.ID)
		}
		seen[c.ID] = true
		switch c.Requirement {
		case pipelinev2.MustStayInV1, pipelinev2.MustBeInV2, pipelinev2.MayStayInV1:
		default:
			t.Errorf("判据 %s 的归属要求 %q 不在词表内", c.ID, c.Requirement)
		}
		file := c.EvidenceFile
		if file == "" {
			file = pipelinev2.EvidenceFile
		}
		src := magpieP2ReadFile(t, root, file)
		if !strings.Contains(src, c.Evidence) {
			t.Errorf("判据 %s（%s）的依据 %q 在 %s 里**找不到** ⇒ "+
				"判据与实现已经分家 ⇒ 要么更新判据，要么修实现",
				c.ID, c.Capability, c.Evidence, file)
		}
	}
}

// TestMagpieP2V2DifferencesHaveRealTests G3 + G4：
// 差异清单非空、字段齐全，且**每条差异挂的测试真实存在**。
//
// ★ 这是 P2-1 验收②「每条差异有测试覆盖」的机器化：
// 差异清单里的 `TestName` 必须在仓内某条 `*_test.go` 里**真的有一个同名测试函数**。
func TestMagpieP2V2DifferencesHaveRealTests(t *testing.T) {
	root := magpieP2RepoRoot(t)
	list := pipelinev2.Differences()
	if len(list) == 0 {
		t.Fatal("差异清单是空的 ⇒ 门恒绿（形同虚设）")
	}

	// 扫出仓内所有测试函数名（一次性）。
	defined := map[string]string{} // 测试名 → 所在文件
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, m := range regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`).FindAllStringSubmatch(string(b), -1) {
			if _, dup := defined[m[1]]; !dup {
				rel, _ := filepath.Rel(root, path)
				defined[m[1]] = filepath.ToSlash(rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫测试函数失败：%v", err)
	}
	if len(defined) == 0 {
		t.Fatal("一个测试函数都没扫到 ⇒ 判据失效")
	}

	seen := map[string]bool{}
	strong, weak := 0, 0
	for _, d := range list {
		if d.ID == "" || d.Dimension == "" || d.V1 == "" || d.V2 == "" || d.Impact == "" {
			t.Errorf("差异 %+v 字段不完整", d)
			continue
		}
		if seen[d.ID] {
			t.Errorf("差异 ID 重复：%s", d.ID)
		}
		seen[d.ID] = true

		// ★ 恰有一个非空：要么有测试，要么**显式**承认没有。
		switch {
		case d.TestName != "" && d.Uncovered != "":
			t.Errorf("差异 %s 同时给了 TestName 与 Uncovered ⇒ 二者恰有一个非空", d.ID)
			continue
		case d.TestName == "" && d.Uncovered == "":
			t.Errorf("差异 %s 既没有测试也没有登记缺口 ⇒ 它要么有测试、要么显式承认没有", d.ID)
			continue
		case d.Uncovered != "":
			// 显式登记的缺口：理由必须是**实质**的（不是占位符）。
			if len(d.Uncovered) < 20 {
				t.Errorf("差异 %s 的 Uncovered 理由太短（%d 字）：%q", d.ID, len(d.Uncovered), d.Uncovered)
			}
			for _, ph := range []string{"todo", "TBD", "待补", "无", "?"} {
				if d.Uncovered == ph {
					t.Errorf("差异 %s 的 Uncovered 理由是占位符 %q", d.ID, ph)
				}
			}
			t.Logf("差异 %s（%s）⇒ ★ 显式登记为**无测试覆盖**：%s", d.ID, d.Dimension, d.Uncovered)
			continue
		}

		where, ok := defined[d.TestName]
		if !ok {
			t.Errorf("差异 %s（%s）挂的测试 %s 在仓内**不存在** ⇒ "+
				"「每条差异有测试覆盖」不成立",
				d.ID, d.Dimension, d.TestName)
			continue
		}
		// ★ 名实相符：主题词必须出现在测试名里。
		// ★ 这一条抓的是「把 A 差异挂到 B 差异的测试上」——
		//   那种情况下测试**存在**，但它根本不测 A ⇒ 单看「存在」会放行。
		if d.Topic == "" {
			t.Errorf("差异 %s 没有 Topic ⇒ 无法校验「名实相符」", d.ID)
			continue
		}
		if !strings.Contains(d.TestName, d.Topic) {
			t.Errorf("差异 %s（%s）的主题词 %q **不出现在**测试名 %s 里 ⇒ "+
				"「测试存在」但**名不符实**（它测的是别的东西）",
				d.ID, d.Dimension, d.Topic, d.TestName)
			continue
		}

		// ★ 补充覆盖测试（一条差异可以有**多个**语义侧面）。
		// ★ ExtraTestNames 与 TestName **都**必须真实存在 ⇒ 删掉任一个 ⇒ 门红。
		coverFiles := []string{where}
		missingCover := false
		for _, name := range d.ExtraTestNames {
			if name == d.TestName {
				t.Errorf("差异 %s 的 ExtraTestNames 重复了 TestName（%s）⇒ 重复登记没有意义", d.ID, name)
				missingCover = true
				continue
			}
			w, ok := defined[name]
			if !ok {
				t.Errorf("差异 %s（%s）的补充覆盖测试 %s 在仓内**不存在** ⇒ "+
					"这条差异的一个语义侧面**悄悄失去**了覆盖",
					d.ID, d.Dimension, name)
				missingCover = true
				continue
			}
			coverFiles = append(coverFiles, w)
		}
		if missingCover {
			continue
		}

		// ★★★ 语义断言：RequiredAssertions 按【测试归位】核对。
		// ★★★ 「代码面」= go/parser 的 AST 语料 ⇒ **注释不计入** ⇒
		// ★★★ 「在注释里写『我断言了 X』」蒙混不过去。
		// ★★★ 且**逐测试**核对 ⇒ A 测试里的一个 `ParseInt` 不能替 B 语义背书
		// ★★★ （★ 否则就是「子串偶然命中」那一族）。
		if len(d.RequiredAssertions) > 0 {
			// ① map 的 key 必须是本差异登记过的覆盖测试。
			allowed := map[string]bool{d.TestName: true}
			for _, n := range d.ExtraTestNames {
				allowed[n] = true
			}
			for testName, asserts := range d.RequiredAssertions {
				if !allowed[testName] {
					t.Errorf("差异 %s 的 RequiredAssertions 引用了**未登记**的测试 %s ⇒ "+
						"它要么拼错了，要么这条差异多了一个**没被登记**的覆盖面",
						d.ID, testName)
					continue
				}
				w, ok := defined[testName]
				if !ok {
					t.Errorf("差异 %s 的 RequiredAssertions 指向的测试 %s 在仓内**不存在**",
						d.ID, testName)
					continue
				}
				corpus, ok := magpieP2TestCodeCorpus(t, root, w, testName)
				if !ok {
					t.Errorf("差异 %s（%s）的覆盖测试 %s 在文件 %s 里**找不到函数体** ⇒\n"+
						"★ ⇒ 门无法核对它的代码面 ⇒ 修法：测试名与文件登记必须对得上",
						d.ID, d.Dimension, testName, w)
					continue
				}
				for _, req := range magpieP2MissingAssertions(corpus, asserts) {
					t.Errorf("差异 %s（%s）要求的语义断言 %q **不在**测试 %s 的代码面里（文件：%s）⇒ "+
						"★ 「测试存在」但**代码里没有断言这件事** ⇒ 名不符实\n"+
						"★ ⇒ 修法：要么把该断言真正写进那个测试，要么这条差异其实没被覆盖",
						d.ID, d.Dimension, req, testName, w)
				}
			}
			strong++
		} else {
			// ★ 显式记录「只享受弱保证」的差异数 ⇒ 局限**可见**，而不是隐在沉默里。
			weak++
			t.Logf("★ 差异 %s（%s）只享受**弱保证**（测试存在 + 主题词在名字里）；"+
				"★ 未登记 RequiredAssertions ⇒ 门无法核对「代码里真的断言了这件事」",
				d.ID, d.Dimension)
		}
		t.Logf("差异 %s（%s）⇒ 覆盖测试 %s（%s）",
			d.ID, d.Dimension, d.TestName, strings.Join(coverFiles, ", "))
	}

	// ★ 强弱保证的**分布**必须可见 ⇒ 局限不许隐在沉默里。
	// ★ 「17 条差异全部有测试覆盖」这句话，在有强保证的那部分是真的，
	// ★ 在只享受弱保证的那部分只是「存在性」⇒ ⇒ 把数字写出来，而不是让读者猜。
	t.Logf("★ 覆盖强度分布：**强保证**（代码面核对语义断言）%d 条，"+
		"**弱保证**（仅存在性 + 主题词）%d 条 ⇒ ⇒ 「每条差异有测试覆盖」的机器化程度不均匀",
		strong, weak)

	// ★★★ **按 ID 钉的强保证断言**：★ 门侧那份清单里的每一条差异，
	// ★★★ 都**必须**带 `RequiredAssertions`（即**必须**享受「代码面核对语义断言」）。
	//
	// ★★★ 为什么不是「数量地板」（★ 2026-10-24 verifier 实测撞出来的两个残余）：
	// ★★★ ① **删除攻击**：删掉某条差异的整块 `RequiredAssertions` ⇒ 数量掉下去 ⇒ 数量地板能抓；
	// ★★★ ② ★★ **迁移攻击**：把它的断言**迁到别的差异**（并登记进 `ExtraTestNames`）⇒
	// ★★★    ★★ 数量**不变** ⇒ 数量地板**放行** ⇒ 而被迁走的那条已掉回弱保证
	// ★★★    ⇒ ⇒ 「代码面核对」**名存实亡** ⇒ ⇒ 按 ID 钉 ⇒ 删**或迁**都表现为
	// ★★★    「该 ID 不再有 `RequiredAssertions`」⇒ **门红**。
	//
	// ★★ 与 C8 的 `checked == 0 ⇒ t.Fatal` **同款**：★ 门必须钉住「**这几条**我确实核对了」。
	for _, id := range strongGuaranteeRequired {
		d := magpieP2FindDifference(list, id)
		if d == nil {
			t.Errorf("★★ 强保证清单里的差异 %s 在清单中**不存在** ⇒ 判据的输入已漂\n"+
				"★★ ⇒ 修法：清单里补回 %s，或更新本门侧的强保证清单", id, id)
			continue
		}
		if len(d.RequiredAssertions) == 0 {
			t.Errorf("★★ 差异 %s 被列入「**必须**享有强保证」清单，却**没有** `RequiredAssertions` ⇒\n"+
				"★★ ⇒ 「代码面核对语义断言」这个能力对它**已被撤销**（★ 可能是被删，也可能是被迁走）\n"+
				"★★ ⇒ 修法：把该差异的 `RequiredAssertions` 补回去（★ 迁到别的差异上**不算**）",
				id)
			continue
		}

		// ★★★ C1（★ 2026-10-24 第四轮 verifier 的 M78）：★ 该 ID 下**每个**断言列表
		// ★★★ 都必须**非空** ⇒ ★★ 否则「零条语义断言被核对」却仍被计为**强保证**
		// ★★★ ⇒ ★★ 门在撒谎（★ 旧门对 M78 是 RC=0 绿 ⇒ 且仍 log「强保证 1 条」）。
		for testName, asserts := range d.RequiredAssertions {
			if len(asserts) == 0 {
				t.Errorf("★★ 差异 %s 为测试 %s 登记了**空的**断言列表 ⇒\n"+
					"★★ ⇒ 「零条语义断言被核对」却仍被计为**强保证** ⇒ 门在撒谎\n"+
					"★★ ⇒ 修法：补上该测试真正断言的那些语义标记（★ 或删掉这条空登记）",
					id, testName)
			}
		}

		// ★★★ C2（★ 第四轮 verifier 的 M73）：★ 清单侧声明的断言必须**逐条包含**
		// ★★★ 门侧的期望 ⇒ ★★ 否则清单侧可以把断言**弱化**成任意在语料里出现的串
		// ★★★ （★ 实测：全改成 `t` ⇒ 旧门 RC=0 绿 ⇒ 且仍 log「强保证 1 条」）。
		expect, ok := strongGuaranteeAssertions[id]
		if !ok {
			t.Errorf("★★ 门侧的期望断言表里**没有** %s ⇒ 门侧与清单侧**不同源** ⇒\n"+
				"★★ ⇒ 修法：在门侧补上 %s 的期望断言表\n"+
				"★★ ⇒ ★ 这也堵住了「把门侧钉的 ID 换成别的」（★ 实测 M72：旧门 RC=0 绿）",
				id, id)
			continue
		}
		for testName, asserts := range expect {
			got, ok := d.RequiredAssertions[testName]
			if !ok {
				t.Errorf("★★ 差异 %s 在清单侧**没有**为测试 %s 登记断言 ⇒\n"+
					"★★ ⇒ 该测试的这个语义侧面**悄悄失去**了强保证\n"+
					"★★ ⇒ 修法：把它补回去（★ 登记到别的测试上**不算**）",
					id, testName)
				continue
			}
			for _, req := range asserts {
				if !magpieP2ContainsString(got, req) {
					t.Errorf("★★ 差异 %s 为测试 %s 登记的断言里**没有** %q ⇒\n"+
						"★★ ⇒ 清单侧把这条语义断言**弱化**了（★ 弱化成任意在语料里出现的串即可蒙混）\n"+
						"★★ ⇒ 修法：把它改回门侧期望的那一条（★ 清单侧与门侧必须对得上）",
						id, testName, req)
				}
			}
		}

		// ★★★ C5（★ 2026-10-25 第五轮 verifier 的 F5 逼出来的**结构性**约束）：
		// ★★★ 门侧期望表里**每个 ID** 必须**至少有一条表达式形态**的期望
		// ★★★ ⇒ ★★ 结构上排除「把该 ID 的全部期望**退化成字符串字面量**」的打法
		// ★★★ ⇒ ★★ （★ 退化后，攻击者只要在测试里加一个同名**字面量**就能骗过字面量面）。
		// ★★★ ⇒ ★★ 而「全部退化」会让这条差异**彻底失去**「代码面核对」这个能力
		// ★★★ ⇒ ★★ ⇒ ⇒ 门侧必须自己钉住「**至少一条**真正的表达式」。
		hasExpr := false
		for _, asserts := range expect {
			for _, req := range asserts {
				if magpieP2FaceFor(req) == magpieP2FaceExpr {
					hasExpr = true
				}
			}
		}
		if !hasExpr {
			t.Errorf("★★ 门侧期望表里差异 %s **没有一条表达式形态**的期望 ⇒\n"+
				"★★ ⇒ 它可以被「全部退化成字符串字面量」架空 ⇒ 门侧必须钉住**至少一条**真正的表达式\n"+
				"★★ ⇒ 修法：给它补一条表达式形态的期望（★ 如 `x != y` 这样的二元表达式）",
				id)
		}
	}

	// ★★★ C4（★ 第四轮 verifier 的 M71 / M72）：★ 门侧的两份声明必须**互相对得上** ——
	// ★★★ ① `strongGuaranteeRequired`（哪些 ID 必须享有强保证）
	// ★★★ ② `strongGuaranteeAssertions`（这些 ID 的期望断言）
	// ★★★ ⇒ ★★★ 交叉核对 ⇒ ★ 「把清单清空」（M71）或「把 ID 换成别的」（M72）
	// ★★★ ⇒ ★★★ 都表现为「两份声明**对不上**」⇒ ⇒ **门红**。
	// ★★★ ★★ 注意诚实边界：★ 两份都在**门文件里** ⇒ ★ 有人**同时**改两份仍能放行
	// ★★★ ⇒ ★★ 那属「门侧无技术互锁」（§五 已登记）⇒ ★ 本交叉核对堵的是
	// ★★★ ⇒ ★★ 「**只改一处**」这一族（★ 现实中最常见的是**误改**）。
	for id := range strongGuaranteeAssertions {
		if !magpieP2ContainsString(strongGuaranteeRequired, id) {
			t.Errorf("★★ 门侧期望断言表里有 %s，但它**不在**强保证 ID 清单里 ⇒\n"+
				"★★ ⇒ 门侧的两份声明**对不上** ⇒ 它的强保证**根本没有被检查**\n"+
				"★★ ⇒ 修法：把它加进 `strongGuaranteeRequired`（★ 或从期望表里删掉）",
				id)
		}
	}
	for _, id := range strongGuaranteeRequired {
		if _, ok := strongGuaranteeAssertions[id]; !ok {
			t.Errorf("★★ 强保证 ID 清单里的 %s，在门侧期望断言表里**没有**条目 ⇒\n"+
				"★★ ⇒ 门侧的两份声明**对不上** ⇒ 该 ID 的断言内容**不受任何期望约束**\n"+
				"★★ ⇒ 修法：在期望表里补上 %s（★ 或从 ID 清单里删掉）",
				id, id)
		}
	}
}

// magpieP2ContainsString 判断字符串切片里是否含某一项（★ 精确相等）。
func magpieP2ContainsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// magpieP2FindDifference 在差异清单里按 ID 取一条。
func magpieP2FindDifference(list []pipelinev2.Difference, id string) *pipelinev2.Difference {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// TestMagpieP2V2ProductionFactsStillHold G5：
// 三条**代码事实**仍然成立（判据表不能取代代码事实）。
func TestMagpieP2V2ProductionFactsStillHold(t *testing.T) {
	root := magpieP2RepoRoot(t)
	src := magpieP2ReadFile(t, root, "cmd/gateway/main_pipeline.go")

	// ① 4 个端点都被包装（注册点形态：`mux.Handle("<path>", v2DispatchHandler(...))`）。
	for _, ep := range pipelinev2.WrappedEndpoints() {
		needle := "mux.Handle(\"" + ep + "\", v2DispatchHandler("
		if !strings.Contains(src, needle) {
			t.Errorf("端点 %s 没有被 v2DispatchHandler 包装（找不到 %q）", ep, needle)
		}
	}
	if len(pipelinev2.WrappedEndpoints()) != 4 {
		t.Errorf("被包装的端点数 = %d，want 4", len(pipelinev2.WrappedEndpoints()))
	}

	// ② 「阶段错误不阻断」这条**安全契约**仍在。
	if !strings.Contains(src, "does NOT block the request") {
		t.Error("找不到「阶段错误不阻断请求」的安全契约措辞 ⇒ " +
			"★ 若它被改成阻断，v2 的「misbehaving Hook 不能丢数据」前提就没了")
	}

	// ③ 默认关：`v2UsePipeline` 在没有任何环境变量为真时返回 false。
	start := strings.Index(src, "func v2UsePipeline() bool {")
	if start < 0 {
		t.Fatal("找不到 v2UsePipeline ⇒ 判据失效")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("v2UsePipeline 的函数体边界读不出来 ⇒ 判据失效")
	}
	body := src[start : start+end]
	if !strings.Contains(body, "return false") {
		t.Error("v2UsePipeline 的默认返回值不是 false ⇒ 「默认关」这条判据已被破坏")
	}
}
