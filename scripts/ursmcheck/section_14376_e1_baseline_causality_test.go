package ursm_test

// §143.76 的门：把「★★ E1 门是形态 A 的【成因】」这条因果链钉成静态判据
// （★ 只读仓内源码 ⇒ 不依赖生产状态、不含任何生产读数）。
//
// ★★★ 为什么这道门要写（§143.76 的起因：★★ 本轮【推翻了自己前两轮的一条结论】）：
//
//	§143.73/§143.74 定位了「形态 A ⇒ 每轮整份落盘 ⇒ 二次方放大」，
//	§143.74 的订正横幅五写的是「E1 对 delta 形态几乎无影响；E1 真实作用在读侧」。
//	★ 本轮把横幅五**推翻**了 —— 依据是【代码 + 生产只读实测】两处互相印证：
//
//	① 【代码（自证）】`domains/session/v2/session_writer_v2.go`：
//	   - 写侧：E1 门开着时，逐轮 bodies 行的 `outbound_body` 被**显式置 nil**
//	     （`bodiesRec.OutboundBody = nil`，见该文件 E1 门注释块）；
//	   - 读侧（同文件，写入前）：差集基准取自【库里上一轮】那一行
//	     （`GetLatestBodiesInTx` → `previousBody.OutboundBody` → `req.LastOutboundBody`）；
//	   - 差分：基准为空 ⇒ `extractRequestDelta` 走 `len(req.LastOutboundBody) == 0`
//	     那条分支 ⇒ **整份客户端历史**被落成 request_delta。
//	   ⇒ ⇒ ★★ 三段接起来就是完整因果链：
//	     【E1 置 nil】⇒【下一轮读到空的基准】⇒【整份落盘】⇒【形态 A】。
//
//	② 【生产实测（252 共享库，只读；口径见
//	   scripts/audit/verify_session_bodies_e1_baseline_causality.sql）】：
//	   - E1 开启**前**：逐轮行 outbound_body 带值率 = **30.25%**（82,516 / 272,819）；
//	     E1 开启**后**：= **0.00%**（0 / 5,693）★ ⇒ 直接封顶到零。
//	   - ★★ E1 后「本来有出站」的会话（用 final_full 行的存在性量）= **51.97%**；
//	     而这批会话的逐轮行 outbound_body 带值率 = **0.00%**（0 / 2,953）
//	     ⇒ ⇒ ★★★ 「E1 把有出站的会话也打成了形态 A」**坐实**
//	     ⇒ ⇒ **不是「压缩器没产出」，是「E1 主动置 nil」**。
//	   - ★★★ 同一 session 跨 E1 边界的【自然实验】（3 个会话，排除流量构成差异）：
//	     平均 delta 体量 **12,074 → 42,389 B/轮（3.5×）**，形态 B 占比 **88.89% → 0.00%**。
//	   - ★★★ 控制会话轮数后分层（排除「长会话本来就大」）：
//	     >10 轮层内「基准可得」= 41,796 B vs「基准缺失」= 92,462 B（**2.2×**）；
//	     2-3 轮层内 14,915 B vs 223,005 B（**15×**）★ ⇒ 基准就是放大器。
//	   - ★ 形态 B 的传播性：17,562 / 18,629 = **94.3%** 的 B 轮，其下一轮也是 B
//	     ⇒ ⇒ 形态 B **自我强化** ⇒ ⇒ E1 一刀切断它 ⇒ ⇒ 二次方从 ~30% 涨到 100%。
//
// ★★ 判据的形态（★ 与 §143.73/§143.75 同款纪律）：
//
//	★★ 本门要守的不是「某个函数存在」，而是【几件仍然为真的事实】：
//	★★ ① E1 门的**置 nil 仍在**（★ 它是形态 A 的成因 ⇒ ⇒ 一旦有人改掉它，
//	★★   「形态 A 的成因」这句话就作废 ⇒ ⇒ FIXME 与本门都必须复核）；
//	★★ ② 差集基准**仍取自库里上一轮**（★ 一旦改成读 final_full ⇒ ★★ 形态 A
//	★★   就有了修法 ⇒ ⇒ FIXME-14376 与本门都必须撤）；
//	★★ ③ 差分内核的「基准为空 ⇒ 整份落盘」分支**仍在**（★ 反向断言）；
//	★★ ④ 复算脚本**同时钉住**「形态标记口径」与「E1 前后分段口径」（★ 缺一不可）。
//
// ★ 本门只读仓内源码 ⇒ **不依赖生产状态、不含任何生产读数**。

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14376WriterPath = "domains/session/v2/session_writer_v2.go"
	section14376AuditPath  = "scripts/audit/verify_session_bodies_e1_baseline_causality.sql"
)

func repoRoot14376(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func parseGo14376(t *testing.T, path string) (*ast.File, *token.FileSet) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	fset := token.NewFileSet()
	// ★ 必须带 parser.ParseComments：★ 不带的话注释【根本不会进 AST】
	//   （f.Doc 与 f.Comments 恒为空）⇒ ⇒ 任何「判某条登记是否还在」的判据
	//   都会【恒假】⇒ ⇒ 门会一直红（假红）⇒ ★ 解析器与判据要配套。
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	return f, fset
}

func nodeText14376(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		return ""
	}
	return buf.String()
}

func funcDecl14376(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Name != nil && d.Name.Name == name {
			return d
		}
	}
	t.Fatalf("找不到函数 %s", name)
	return nil
}

// commentGroupHasAll14376 报告是否存在某个注释组同时含有全部 needle。
//
// ★★ needle 必须选【只有原文自述才有】的形态：★ 用「置 nil」「整份」这种
// ★★ 通用词当 needle ⇒ ★★ 登记块 FIXME-14376 里也有 ⇒ ⇒ 判据恒真
// ★★ （★ §143.73 变异 M4 / §143.75 判据② 的教训）。
func commentGroupHasAll14376(f *ast.File, needles ...string) bool {
	groups := make([]*ast.CommentGroup, 0, len(f.Comments)+1)
	if f.Doc != nil {
		groups = append(groups, f.Doc)
	}
	groups = append(groups, f.Comments...)
	for _, cg := range groups {
		txt := cg.Text()
		all := true
		for _, n := range needles {
			if !strings.Contains(txt, n) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// stripSQLComments14376 剥掉 SQL 注释（-- 行注释与 /* */ 块注释），
// 用来在【代码/脚本正文】上查判据 ⇒ ★ 避免「注释里也有这个词」导致判据恒真
// （★ §143.73 变异 M6 / §143.75 判据⑤ 的教训）。
func stripSQLComments14376(src string) string {
	var out strings.Builder
	lines := strings.Split(src, "\n")
	inBlock := false
	for _, ln := range lines {
		i := 0
		for i < len(ln) {
			if inBlock {
				if idx := strings.Index(ln[i:], "*/"); idx >= 0 {
					inBlock = false
					i += idx + 2
					continue
				}
				i = len(ln)
				continue
			}
			if idx := strings.Index(ln[i:], "--"); idx >= 0 {
				// ★ 行注释：只当它是注释（★ 本脚本的正文里没有字符串字面量含 "--"）。
				out.WriteString(ln[i : i+idx])
				out.WriteString("\n")
				i = len(ln)
				continue
			}
			if idx := strings.Index(ln[i:], "/*"); idx >= 0 {
				out.WriteString(ln[i : i+idx])
				inBlock = true
				i += idx + 2
				continue
			}
			out.WriteString(ln[i:])
			i = len(ln)
		}
		if !strings.HasSuffix(ln, "\n") {
			out.WriteString("\n")
		}
	}
	return out.String()
}

// TestSection14376E1GateStillNilsOutbound pins 判据 ①：
// ★★ E1 门【显式置 nil】的那一行必须仍在（★ 它是形态 A 的【成因】）。
//
// ★ 反向断言：★ 一旦有人把置 nil 改掉（例如改成「只对压缩流量置 nil」），
// ★ ⇒ 「形态 A 是 E1 造成的」这句话就不成立 ⇒ ⇒ FIXME-14376 与本门都要复核。
func TestSection14376E1GateStillNilsOutbound(t *testing.T) {
	root := repoRoot14376(t)
	f, fset := parseGo14376(t, filepath.Join(root, section14376WriterPath))

	// ★ 判据要落在【代码】上（不是注释上）⇒ ⇒ 用 AST 找赋值语句。
	// ★ needle 用【完整形态】`bodiesRec.OutboundBody = nil`：
	// ★ 裸子串 "OutboundBody = nil" 在注释里也可能出现 ⇒ 会被误判为通过。
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := lhs.X.(*ast.Ident)
		if !ok || ident.Name != "bodiesRec" || lhs.Sel.Name != "OutboundBody" {
			return true
		}
		if id, ok := assign.Rhs[0].(*ast.Ident); ok && id.Name == "nil" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("★ 找不到 `bodiesRec.OutboundBody = nil` 这条赋值 ⇒ " +
			"⇒ E1 门不再【显式置 nil】⇒ ⇒ 「形态 A 的成因是 E1」这句话需要复核 " +
			"⇒ 请同步复核 FIXME-14376 与本门")
	}
	_ = fset
}

// TestSection14376BaselineStillComesFromPreviousRow pins 判据 ②：
// ★★ 差集基准必须**仍取自库里上一轮**（`previousBody.OutboundBody` → `req.LastOutboundBody`）。
//
// ★★ 反向断言：★★ 一旦有人把基准改成读 final_full（★ 那正是 §143.73/§143.75
// ★★ 提的 A1 方案）⇒ ★★ **形态 A 就有了修法** ⇒ ⇒ FIXME-14376 与本门都必须撤。
func TestSection14376BaselineStillComesFromPreviousRow(t *testing.T) {
	root := repoRoot14376(t)
	f, fset := parseGo14376(t, filepath.Join(root, section14376WriterPath))

	// ★ 找 `req.LastOutboundBody = previousBody.OutboundBody` 这条赋值。
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		lid, ok := lhs.X.(*ast.Ident)
		if !ok || lid.Name != "req" || lhs.Sel.Name != "LastOutboundBody" {
			return true
		}
		rhs, ok := assign.Rhs[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		rid, ok := rhs.X.(*ast.Ident)
		if ok && rid.Name == "previousBody" && rhs.Sel.Name == "OutboundBody" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("★ 找不到 `req.LastOutboundBody = previousBody.OutboundBody` ⇒ " +
			"⇒ 差集基准的来源变了 ⇒ ⇒ （★ 若已改成读 final_full ⇒ 形态 A 就有修法了 " +
			"⇒ ⇒ 请同步撤掉 FIXME-14376 与本门）")
	}
	_ = fset
}

// TestSection14376EmptyBaselineStillStoresFullBody pins 判据 ③：
// ★ `extractRequestDelta` 的「基准为空 ⇒ 返回整份请求体」这条分支必须仍在。
//
// ★ 这条分支是【放大器】的最后一段：基准空 ⇒ 整份落盘 ⇒ 二次方。
//
// ★★ 判据必须按【分支块】判，不能用「全文 index 顺序」：
// ★★ `return req.RequestBody` 在该函数里出现【两次】——先在 submitMode 分支
// ★★ （`delta`/`snapshot`/`inferred_compressed`），后在本分支
// ★★ ⇒ ⇒ 用 `strings.Index` 找第一个就会【匹配到另一条分支】
// ★★ ⇒ ⇒ 判据要么恒绿、要么误判（★ 本门第一版就是这么写坏的，
// ★★ 是【门自己红】把它抓出来的 ⇒ ★ 改用 AST 定位「条件含基准判据的那个 if 块」）。
func TestSection14376EmptyBaselineStillStoresFullBody(t *testing.T) {
	root := repoRoot14376(t)
	f, fset := parseGo14376(t, filepath.Join(root, section14376WriterPath))

	fn := funcDecl14376(t, f, "extractRequestDelta")
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if found {
			return false
		}
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		cond := nodeText14376(fset, ifs.Cond)
		if !strings.Contains(cond, "len(req.LastOutboundBody) == 0") {
			return true
		}
		// ★ 命中了「基准为空」那个 if ⇒ 它的【块】里必须有 `return req.RequestBody`。
		body := nodeText14376(fset, ifs.Body)
		if strings.Contains(body, "return req.RequestBody") {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("★ extractRequestDelta 里「基准为空 ⇒ 返回整份请求体」这条分支不在了 ⇒ " +
			"⇒ 放大链的最后一环变了 ⇒ 请同步复核 FIXME-14376 与本门")
	}
}

// TestSection14376DefectStillRegistered pins 判据 ④：
// 「E1 门是形态 A 的成因」这条登记必须仍在（FIXME-14376 注释组）。
//
// ★★ needle 用【完整登记标记形态】（带全角左括号）：★ 裸子串会让「把登记改名」
// ★★ 这种最常见的退役动作逃过门（★ §143.73 变异 M4 的教训）。
// ★★ 且必须同时点明【成因】与【置 nil】⇒ 能识破「已作废 / 改名」的写法。
func TestSection14376DefectStillRegistered(t *testing.T) {
	root := repoRoot14376(t)
	f, _ := parseGo14376(t, filepath.Join(root, section14376WriterPath))
	if !commentGroupHasAll14376(f, "FIXME-14376（", "成因", "置 nil") {
		t.Fatal("★ 登记块 FIXME-14376 不在了，或它没有同时点明「成因」与「置 nil」⇒ " +
			"⇒ （★ 若 E1 已不再置 nil ⇒ 请同步撤掉 FIXME-14376 与本门）")
	}
}

// TestSection14376AuditScriptPinsBothCalibers pins 判据 ⑤：
// 复算脚本必须同时钉住【两个口径】：形态标记（不 detoast 的字节判据）与 E1 前后分段。
//
// ★★ 必须【剥掉注释】再查：★ 否则注释里提到的锚点会让判据恒真（★ §143.73 变异 M6 的教训）。
func TestSection14376AuditScriptPinsBothCalibers(t *testing.T) {
	root := repoRoot14376(t)
	raw, err := os.ReadFile(filepath.Join(root, section14376AuditPath))
	if err != nil {
		t.Fatalf("读取复算脚本失败：%v", err)
	}
	body := stripSQLComments14376(string(raw))

	// ① 形态标记必须用 pg_column_size（不 detoast）⇒ ★ 不能用 jsonb_typeof。
	if !strings.Contains(body, "pg_column_size(outbound_body) > 12") {
		t.Fatal("★ 复算脚本的形态标记不是 `pg_column_size(outbound_body) > 12` ⇒ " +
			"⇒ （★ jsonb_typeof 会 detoast ⇒ 在 27 万行上会超时）")
	}
	if strings.Contains(body, "jsonb_typeof(outbound_body)") {
		t.Fatal("★ 复算脚本里出现 jsonb_typeof(outbound_body) ⇒ 会 detoast ⇒ 普查会超时")
	}
	// ② E1 前后分段口径必须仍在（★ 这是本轮的核心口径）。
	if !strings.Contains(body, "2026-10-21 21:27:26+08") {
		t.Fatal("★ 复算脚本里没有 E1 门时点 ⇒ ⇒ 「E1 前后分段」这个核心口径没了")
	}
	if !strings.Contains(body, "after_E1") || !strings.Contains(body, "before_E1") {
		t.Fatal("★ 复算脚本没有 before_E1 / after_E1 两个分段标记 ⇒ 前后对比口径没了")
	}
	// ③ 压缩器产出率必须用 final_full 的存在性量（★ 不用 outbound_body 是否为空）。
	if !strings.Contains(body, "kind='final_full'") {
		t.Fatal("★ 复算脚本没有用 final_full 行量「压缩器产出率」⇒ " +
			"⇒ （★ E1 之后 outbound_body 恒空 ⇒ 用它量会得到 0% 的假象）")
	}
	// ④ 基准可得性的分层口径必须仍在（★ 本轮的因果判据）。
	if !strings.Contains(body, "baseline_available") || !strings.Contains(body, "baseline_missing") {
		t.Fatal("★ 复算脚本没有「基准可得 / 基准缺失」两个分层标记 ⇒ 因果口径没了")
	}
}
