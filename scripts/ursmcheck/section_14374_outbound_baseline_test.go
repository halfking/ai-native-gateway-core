package ursm_test

// §143.74 的门：把「v2 正文表二次方放大的【根因锚点】」钉成静态判据
// （★ 只读仓内源码 ⇒ 不依赖生产状态、不含任何生产读数）。
//
// ★ 为什么这道门要写（§143.74 的起因）：
//
//	§143.73 量出「`request_delta` 每轮整份重存 ⇒ 二次方放大」，但**没钉到根因**。
//	★ 本轮用**全仓 AST 扫描**（找所有 `.OutboundBody` 赋值点）钉到了唯一来源：
//
//	internal/sessionv2mirror/hook.go：
//	    // Outbound body (from session compressor)
//	    if len(entry.OutboundBody) > 0 {
//	        req.OutboundBody = parseMessagesJSON(entry.OutboundBody)
//	    }
//
//	★★ ⇒ 【出站正文只来自压缩器】⇒ ⇒ 未压缩流量（submit_mode == "full"，
//	★★ 10 月占 91.3%；E1 门开启后占 100%）的 `outbound_body` 恒为 jsonb 'null'
//	★★ ⇒ ⇒ 差集基准恒空 ⇒ ⇒ 每轮整份落盘 ⇒ ⇒ 二次方放大（**默认路径，不是边缘情况**）。
//
// ★★ 后果有三个，且互不相同（★ 门要守的正是这三个机制事实）：
//	① 存储：二次方放大（§143.73 已量）；
//	② 读侧：`TurnReader.LoadChain` 是【纯拼接、无去重】⇒ 重建出的会话里
//	    同一个 system prompt 会出现 N 次（★ 实测：15 轮的会话里 14 次）；
//	③ 压缩器输入：`session_cache.loadFromDB` 先试 `LoadLatestOutbound`
//	    （★ 它把 jsonb 'null' 当空 ⇒ 返回 nil）⇒ ⇒ 必然回落 `LoadChain(..., 10)`
//	    ⇒ ⇒ 压缩器拿到的上下文被放大约 10 倍。
//
// ★★ 为什么判据全是【反向断言】（★ 关键）：
//	★★ 真正的守门物不是「某个函数存在」，而是「**这三个机制事实还成立**」——
//	★★ 一旦有人补上出站回填（形态 A 消失）、或给 LoadChain 加去重、或改了
//	★★ 压缩器的回落顺序，★ 那个缺陷就不存在了 ⇒ ★★ ⇒ 门必须红来提醒同步 FIXME。
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
	section14374HookPath    = "internal/sessionv2mirror/hook.go"
	section14374ReaderPath  = "domains/session/v2/turn_reader.go"
	section14374CachePath   = "domains/hooks/compression/session_cache.go"
	section14374AuditPath   = "scripts/audit/verify_session_bodies_read_side_amplification.sql"
	section14374RegisterKey = "FIXME-14374（"
)

func repoRoot14374(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// parseGo14374 解析 Go 源文件，返回 AST、FileSet 与原始字节。
//
// ★ 必须带 parser.ParseComments：★ 不带的话注释【根本不会进 AST】
//
//	（f.Doc 与 f.Comments 恒为空）⇒ ⇒ 任何「判某条登记是否还在」的判据
//	都会【恒假】⇒ ⇒ 门会一直红（假红）⇒ ★ 解析器与判据要配套。
func parseGo14374(t *testing.T, path string) (*ast.File, *token.FileSet) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	return f, fset
}

// nodeText14374 把一个 AST 节点打印成源码文本。
func nodeText14374(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		return ""
	}
	return buf.String()
}

// funcDecl14374 找出指定名字的函数声明。
func funcDecl14374(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
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

// guardedOutboundAssign14374 报告：是否存在一个【由 `entry.OutboundBody` 守卫的】
// `.OutboundBody` 赋值（即根因锚点那个 if）。
//
// ★★ 为什么用【AST 结构】而不是裸子串：★ 因为登记块（FIXME-14374）里也写着
// ★★ `len(entry.OutboundBody) > 0` 这行字 ⇒ ⇒ 裸子串会让判据恒真（假牙）★★
// ★★ ⇒ 必须认【if 语句 + 其 body 里的赋值】这个结构。
func guardedOutboundAssign14374(fset *token.FileSet, f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if !strings.Contains(nodeText14374(fset, ifs.Cond), "entry.OutboundBody") {
			return true
		}
		ast.Inspect(ifs.Body, func(m ast.Node) bool {
			as, ok := m.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if ok && sel.Sel != nil && sel.Sel.Name == "OutboundBody" {
					found = true
				}
			}
			return true
		})
		return true
	})
	return found
}

// commentGroupHasAll14374 报告是否存在某个注释组同时含有全部 needle。
func commentGroupHasAll14374(f *ast.File, needles ...string) bool {
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

// TestSection14374RootCauseAnchorStillGuarded pins 判据 ①：
// 根因锚点必须仍在：`.OutboundBody` 的赋值仍然被 `entry.OutboundBody` 的长度守卫。
//
// ★ 反向断言：★ 一旦有人给这个字段补上【无压缩产物时的回填】，形态 A 就消失
// ★ ⇒ ⇒ 那时 §143.73/§143.74 的 defect 不再存在 ⇒ FIXME 与本门都要撤 ⇒ ⇒ 门红。
func TestSection14374RootCauseAnchorStillGuarded(t *testing.T) {
	root := repoRoot14374(t)
	f, fset := parseGo14374(t, filepath.Join(root, section14374HookPath))
	if !guardedOutboundAssign14374(fset, f) {
		t.Fatal("★ 根因锚点不在了：`.OutboundBody` 的赋值不再被 `len(entry.OutboundBody) > 0` 守卫 ⇒ " +
			"⇒ （★ 若这是有意的修复 ⇒ 形态 A 已消失 ⇒ 请同步撤掉 FIXME-14373/14374 与本门）")
	}
}

// TestSection14374LoadChainIsPureConcatenation pins 判据 ②：
// `LoadChain` 必须仍是【纯拼接、无去重】。
//
// ★ 反向断言：★ 有人给读侧加去重 ⇒ 读侧重复消失 ⇒ ⇒ 那时「读侧累加重复」这个
// ★ 后果不再成立 ⇒ ⇒ 需要同步 FIXME ⇒ ⇒ 门红。
func TestSection14374LoadChainIsPureConcatenation(t *testing.T) {
	root := repoRoot14374(t)
	f, fset := parseGo14374(t, filepath.Join(root, section14374ReaderPath))
	text := nodeText14374(fset, funcDecl14374(t, f, "LoadChain"))

	for _, needle := range []string{"append(chain, reqMsgs...)", "append(chain, respMsgs...)"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("LoadChain 里缺少纯拼接锚点 %q（★ 若读侧改成去重/重建 ⇒ 请同步撤 FIXME）", needle)
		}
	}
	if strings.Contains(text, "map[") {
		t.Fatal("★ LoadChain 里出现了 map[ ⇒ 读侧已经不是纯拼接 ⇒ 请同步撤 FIXME-14374 与本门")
	}
}

// TestSection14374CompressorPrefersLatestOutboundBeforeChain pins 判据 ③：
// 压缩器的 `loadFromDB` 必须仍然是【先 LoadLatestOutbound、再回落 LoadChain】。
//
// ★ 这是「形态 A ⇒ 压缩器输入被放大 10 倍」的机制前提。
func TestSection14374CompressorPrefersLatestOutboundBeforeChain(t *testing.T) {
	root := repoRoot14374(t)
	f, fset := parseGo14374(t, filepath.Join(root, section14374CachePath))
	text := nodeText14374(fset, funcDecl14374(t, f, "loadFromDB"))

	first := strings.Index(text, "LoadLatestOutbound")
	second := strings.Index(text, "LoadChain")
	if first < 0 || second < 0 {
		t.Fatalf("loadFromDB 里找不到回落顺序的两个锚点（LoadLatestOutbound=%d LoadChain=%d）", first, second)
	}
	if first > second {
		t.Fatal("★ loadFromDB 的顺序变了：LoadChain 现在排在 LoadLatestOutbound 之前 ⇒ " +
			"⇒ 「形态 A ⇒ 压缩器输入放大」这个后果的机制前提变了 ⇒ 请同步复核 FIXME-14374")
	}
}

// TestSection14374NullOutboundTreatedAsEmpty pins 判据 ④：
// `LoadLatestOutbound` 必须仍然把 jsonb 字面量 'null' 当成「没有出站」。
//
// ★ 这是「形态 A ⇒ LoadLatestOutbound 返回 nil ⇒ 必然回落 LoadChain」的关键一跳。
func TestSection14374NullOutboundTreatedAsEmpty(t *testing.T) {
	root := repoRoot14374(t)
	f, fset := parseGo14374(t, filepath.Join(root, section14374ReaderPath))
	text := nodeText14374(fset, funcDecl14374(t, f, "LoadLatestOutbound"))
	if !strings.Contains(text, `string(raw) == "null"`) {
		t.Fatal("★ LoadLatestOutbound 里不再把 jsonb 'null' 当空 ⇒ " +
			"⇒ 「形态 A ⇒ 回落 LoadChain」这一跳的机制变了 ⇒ 请同步复核 FIXME-14374")
	}
}

// TestSection14374DefectStillRegistered pins 判据 ⑤：
// 「根因 + 三个后果」必须仍被登记（FIXME-14374 注释组，且必须同时点明守卫条件与读侧后果）。
//
// ★★ needle 用【完整登记标记形态】（带全角左括号）：★ 裸子串会让「把登记改名」
// ★★ 这种最常见的退役动作逃过门（★ §143.73 变异 M4 的教训）。
func TestSection14374DefectStillRegistered(t *testing.T) {
	root := repoRoot14374(t)
	f, _ := parseGo14374(t, filepath.Join(root, section14374HookPath))
	if !commentGroupHasAll14374(f, section14374RegisterKey, "len(entry.OutboundBody) > 0", "LoadChain") {
		t.Fatal("★ 登记块 FIXME-14374 不在了，或它没有同时点明「守卫条件」与「读侧后果」⇒ " +
			"⇒ （★ 若根因已修 ⇒ 请同步撤掉 FIXME-14374 与本门）")
	}
}

// TestSection14374AuditScriptPinsBothCalibers pins 判据 ⑥：
// 复算脚本必须同时钉住【两个口径】：基线形态（jsonb_typeof）与流量形态（submit_mode）。
//
// ★ 为什么必须两个口径都在：★ 只看 outbound 形态看不出「这是未压缩流量导致的」；
// ★ 只看 submit_mode 说不清「库里那列到底是不是空」⇒ ⇒ 缺一个口径就不能复算本轮结论。
func TestSection14374AuditScriptPinsBothCalibers(t *testing.T) {
	root := repoRoot14374(t)
	b, err := os.ReadFile(filepath.Join(root, section14374AuditPath))
	if err != nil {
		t.Fatalf("读取复算脚本 %s 失败：%v", section14374AuditPath, err)
	}
	// ★ 必须剥掉注释再查：★ 否则注释里提到的锚点会让判据恒真（★ §143.73 变异 M6 的教训）。
	code := stripSQLComments14373(string(b))
	for _, needle := range []string{
		"jsonb_typeof(outbound_body)",        // 口径①：基线形态
		"submit_mode",                        // 口径②：流量形态
		"final_full",                         // 口径①-B：形态 A 连 final_full 都不写
		"storage.session_final_full_enabled", // 口径②-C：E1 门时点（本轮的因果时点）
	} {
		if !strings.Contains(code, needle) {
			t.Fatalf("复算脚本 %s 的【可执行代码】里缺少口径锚点 %q（★ 两个口径缺一个 ⇒ 不能复算本轮结论）", section14374AuditPath, needle)
		}
	}
}

// TestSection14374JudgesAreNotConstant 自证判据不是常量：
// ① 守卫检测在【合成源码】上必须给出与输入一致的结论；
// ② 真实文件里必须确实检出守卫。
func TestSection14374JudgesAreNotConstant(t *testing.T) {
	// ① 合成源码自证：带守卫 ⇒ true；把守卫去掉 ⇒ false。
	guarded := `package p
func f(entry *E, req *R) {
	if len(entry.OutboundBody) > 0 {
		req.OutboundBody = parseMessagesJSON(entry.OutboundBody)
	}
}`
	unguarded := `package p
func f(entry *E, req *R) {
	req.OutboundBody = parseMessagesJSON(entry.OutboundBody)
}`
	for _, tc := range []struct {
		src  string
		want bool
	}{{guarded, true}, {unguarded, false}} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic.go", tc.src, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			t.Fatalf("解析合成源码失败：%v", err)
		}
		if got := guardedOutboundAssign14374(fset, f); got != tc.want {
			t.Fatalf("★ 守卫检测不自证：合成源码期望 %v，实际 %v ⇒ 判据可能是写死的", tc.want, got)
		}
	}

	// ② 真实文件
	root := repoRoot14374(t)
	f, fset := parseGo14374(t, filepath.Join(root, section14374HookPath))
	if !guardedOutboundAssign14374(fset, f) {
		t.Fatal("★ 真实文件里没检出守卫 ⇒ 判据 ① 现在是恒假的空壳 ⇒ 门没有牙")
	}
}
