package ursm_test

// §143.72 的门：把「v2 正文表的 request_delta 语义失真」这个**已定位的机制缺陷**
// 钉成静态判据（★ 只读仓内源码 ⇒ 不依赖生产状态、不含任何生产读数）。
//
// ★ 为什么这道门现在才写（§143.72 的起因）：
//
//	§143.71 实测「兜底文件里的累积体」与「库里写进 request_delta 的内容」
//	逐条比对时，发现两者【轮次边界与顺序对不上】（逐条相同 0/34、前缀 0/34、
//	连续子序列 0/34、但单条内容约 69.8% 重合）⇒ ⇒ §143.71.6b 一度归因为
//	「同一份历史的不同版本」⇒ ⇒ ★ 本轮顺着读代码【定位到真正的机制】：
//
//	★ `ProcessedRequest.RequestBody` 的注释是「Full request body from client」
//	★ `ProcessedRequest.LastOutboundBody` 的注释是「Previous turn's outbound」
//	★ `ProcessedRequest.OutboundBody` 的注释是「Actual outbound sent to LLM (after compression)」
//
//	⇒ ⇒ ★★ 被减对象 = 客户端的【完整历史】；差集基准 = 上一轮【压缩后】的出站消息
//	⇒ ⇒ ★★★ ⇒ 在 submit_mode == "full" 时，算出来的 request_delta 是
//	★【压缩残留】而不是【本轮新增】。
//
// ★ 处置范式（按本仓纪律）：★ **正确意图 + 已定位缺陷 ⇒ 钉住现状 + FIXME +
// 反向断言（不许无声存在）** ⇒ ⇒ 本门就是那个「反向断言」。
//
// ★★ 为什么判据 ③ 是【反向断言】而不是「存在性断言」：
//	★★ 「`extractRequestDelta` 存在」这种断言拦不住任何东西（它一直在）；
//	★★ 真正要守的是「**差集基准与被减对象形态不匹配**这个事实」——
//	★★ ⇒ 一旦有人把 `OutboundBody` 改成【未压缩】、或把 `LastOutboundBody`
//	★★ 改成【存客户端完整历史】，★ 形态不匹配就消失了 ⇒ ★★ **那时 defect
//	★★ 不再存在 ⇒ FIXME 必须撤 ⇒ ⇒ 门必须红来提醒同步**。
//
// ★ 本门只读仓内源码 ⇒ **不依赖生产状态、不含任何生产读数**。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14372WriterPath = "domains/session/v2/session_writer_v2.go"
	section14372GatePath   = "scripts/ursmcheck/section_14372_delta_semantics_test.go"
)

func repoRoot14372(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func parseGo14372(t *testing.T, path string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	// ★ 必须带 parser.ParseComments：★ 不带的话注释【根本不会进 AST】
	//   （f.Doc 与 f.Comments 恒为空）⇒ ⇒ 任何「判某条登记是否还在」的判据
	//   都会【恒假】⇒ ⇒ 门会一直红（假红）⇒ ★ 解析器与判据要配套。
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	return f
}

// structFieldComment14372 返回指定结构体里指定字段的注释文本（★ 空串 = 无注释）。
func structFieldComment14372(f *ast.File, typeName, fieldName string) string {
	out := ""
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != typeName {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, fld := range st.Fields.List {
			for _, name := range fld.Names {
				if name.Name != fieldName {
					continue
				}
				if fld.Comment != nil {
					out = fld.Comment.Text()
				} else if fld.Doc != nil {
					out = fld.Doc.Text()
				}
			}
		}
		return false
	})
	return out
}

// commentGroupHas14372 报告某个文件的注释里是否同时含有 needle 与 alsoNeedle。
//
// ★★ `ast.File.Comments` 不含包文档（包文档在 f.Doc）⇒ ⇒ 两处都要看。
func commentGroupHas14372(f *ast.File, needle, alsoNeedle string) bool {
	groups := make([]*ast.CommentGroup, 0, len(f.Comments)+1)
	if f.Doc != nil {
		groups = append(groups, f.Doc)
	}
	groups = append(groups, f.Comments...)
	for _, cg := range groups {
		txt := cg.Text()
		if strings.Contains(txt, needle) && (alsoNeedle == "" || strings.Contains(txt, alsoNeedle)) {
			return true
		}
	}
	return false
}

// TestSection14372DeltaDefectStillRegistered pins 判据 ①②：
// ① `extractRequestDelta` 仍在，且仍用 `LastOutboundBody` 作差集基准（★ 钉住现状）；
// ② 形态不匹配这个缺陷【必须仍被登记】（`FIXME-14372` 注释组，
//
//	★ 且必须同时提到 `LastOutboundBody` 与 `after compression`
//	⇒ ⇒ 能识破「已作废 / 改名」的写法）。
func TestSection14372DeltaDefectStillRegistered(t *testing.T) {
	root := repoRoot14372(t)
	path := filepath.Join(root, section14372WriterPath)
	f := parseGo14372(t, path)

	// ① 现状钉住：函数仍在 + 基准仍是 LastOutboundBody
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if ok && fd.Name.Name == "extractRequestDelta" {
			found = true
		}
		return true
	})
	if !found {
		t.Errorf("%s：找不到 `extractRequestDelta` ⇒ ★ 它是 v2 正文增量的唯一实现 "+
			"⇒ ⇒ 请同步本门", section14372WriterPath)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", section14372WriterPath, err)
	}
	if !strings.Contains(string(raw), "buildMessageSet(req.LastOutboundBody)") {
		t.Errorf("%s：找不到 `buildMessageSet(req.LastOutboundBody)` ⇒ ★★ 差集基准已被换掉 "+
			"⇒ ⇒ ★★ 形态不匹配（客户端完整历史 vs 压缩后出站）要么消失了、要么换了个形态 "+
			"⇒ ⇒ ★ FIXME-14372 必须同步（撤掉或改写）⇒ ⇒ 请同步本门",
			section14372WriterPath)
	}

	// ② 缺陷登记必须仍在（★ 两个 needle 必须在【同一注释组】）
	if !commentGroupHas14372(f, "FIXME-14372", "LastOutboundBody") {
		t.Errorf("%s：找不到同时提到 `FIXME-14372` 与 `LastOutboundBody` 的登记注释组 ⇒ "+
			"★★「delta 语义失真（差集基准 = 压缩后出站，被减对象 = 客户端完整历史）」"+
			"这个已定位的缺陷不再被登记 ⇒ ⇒ 有人会以为 request_delta 就是「本轮新增」⇒ ⇒ 请同步本门",
			section14372WriterPath)
	}
	if !commentGroupHas14372(f, "FIXME-14372", "after compression") {
		t.Errorf("%s：找不到同时提到 `FIXME-14372` 与 `after compression` 的登记注释组 ⇒ "+
			"★★ 登记里没有写明【差集基准是压缩后的出站】⇒ ⇒ 形态不匹配这一层没被记下 "+
			"⇒ ⇒ 请同步本门", section14372WriterPath)
	}
}

// TestSection14372CompressionShapeStillMarked pins 判据 ③（★ 反向断言）：
// ★★ `ProcessedRequest` 的三个字段注释必须保持【形态标注】：
//
//	LastOutboundBody = "Previous turn's outbound"（★ 上一轮的出站）
//	OutboundBody     = "after compression"（★ 压缩后）
//	RequestBody      = "from client"（★ 客户端完整请求体）
//
// ★★ 为什么这是反向断言：★★ 一旦有人把 `OutboundBody` 改成【未压缩】、
//
//	或把 `LastOutboundBody` 改成【存客户端完整历史】，★ ★★ 形态不匹配就消失了
//	⇒ ⇒ ★★ 那时 defect 不再存在 ⇒ FIXME-14372 必须撤 ⇒ ⇒ ★★ **门必须红来提醒同步**。
func TestSection14372CompressionShapeStillMarked(t *testing.T) {
	root := repoRoot14372(t)
	path := filepath.Join(root, section14372WriterPath)
	f := parseGo14372(t, path)

	cases := []struct {
		field string
		need  string
		why   string
	}{
		{"LastOutboundBody", "Previous turn's outbound",
			"★ 差集基准是【上一轮的出站】—— ★ 这条形态标注一旦被改成「客户端完整历史」，★ 形态不匹配就消失"},
		{"OutboundBody", "after compression",
			"★ 出站是【压缩后】的 —— ★ 这条形态标注一旦被改成「未压缩」，★★ 差集基准就与被减对象同形态了"},
		{"RequestBody", "from client",
			"★ 被减对象是【客户端的完整请求体】—— ★ 这条形态标注一旦变了，★ 形态不匹配的另一半就没了"},
	}
	for _, c := range cases {
		cmt := structFieldComment14372(f, "ProcessedRequest", c.field)
		if cmt == "" {
			t.Errorf("%s：`ProcessedRequest.%s` 没有字段注释 ⇒ ★★ 无法判断它的【形态】⇒ "+
				"⇒ ★★ 形态不匹配这个缺陷就【不可见】了 ⇒ ⇒ 请同步本门", section14372WriterPath, c.field)
			continue
		}
		if !strings.Contains(cmt, c.need) {
			t.Errorf("%s：`ProcessedRequest.%s` 的注释是 %q，期望包含 %q ⇒ %s ⇒ "+
				"⇒ ★★ 请同步本门（并同步 `FIXME-14372` 登记）",
				section14372WriterPath, c.field, strings.TrimSpace(cmt), c.need, c.why)
		}
	}
}

// TestSection14372JudgesAreNotConstant 用**合成源码**自证判据 ②③ 不是恒真。
func TestSection14372JudgesAreNotConstant(t *testing.T) {
	fset := token.NewFileSet()

	// 判据 ③ 的自检：把 `OutboundBody` 的形态标注改成「未压缩」⇒ 必须判不出来。
	const uncompacted = `package p
type ProcessedRequest struct {
	RequestBody      []Message // Full request body from client
	LastOutboundBody []Message // Previous turn's outbound (for delta extraction)
	OutboundBody     []Message // Actual outbound sent to LLM
}`
	fa, err := parser.ParseFile(fset, "a.go", uncompacted, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	cmt := structFieldComment14372(fa, "ProcessedRequest", "OutboundBody")
	if strings.Contains(cmt, "after compression") {
		t.Errorf("反例（OutboundBody 未标 after compression）竟被判为已标注 ⇒ ★ 判据 ③ 是恒真的")
	}

	// 正例：形态标注齐全 ⇒ 必须认出。
	const marked = `package p
type ProcessedRequest struct {
	RequestBody      []Message // Full request body from client
	LastOutboundBody []Message // Previous turn's outbound (for delta extraction)
	OutboundBody     []Message // Actual outbound sent to LLM (after compression)
}`
	fb, err := parser.ParseFile(fset, "b.go", marked, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	cmt = structFieldComment14372(fb, "ProcessedRequest", "OutboundBody")
	if !strings.Contains(cmt, "after compression") {
		t.Errorf("正例（OutboundBody 标了 after compression）竟未被认出（got %q）⇒ ★ 判据 ③ 判反了", cmt)
	}

	// 判据 ② 的自检：注释组里只有 FIXME-14372、没有另一个 needle ⇒ 必须判不出来。
	fc, err := parser.ParseFile(fset, "c.go", `package p
// FIXME-14372: 只是一句无关的登记
func main() {}`, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	if commentGroupHas14372(fc, "FIXME-14372", "after compression") {
		t.Errorf("反例（只有 FIXME-14372、没有 after compression）竟被判为已登记 ⇒ ★ 判据 ② 区分力不足")
	}
}
