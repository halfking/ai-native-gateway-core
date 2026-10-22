package ursm_test

// §143.73 的门：把「v2 正文表 `request_delta` 的二次方放大」这个**已定位的机制缺陷**
// 钉成静态判据（★ 只读仓内源码 ⇒ 不依赖生产状态、不含任何生产读数）。
//
// ★ 为什么这道门要写（§143.73 的起因）：
//
//	§143.72 定位到「差集基准 = 上一轮【压缩后】的出站、被减对象 = 客户端完整历史」
//	⇒ 形态不匹配 ⇒ request_delta 不是「本轮新增」。
//	★ 本轮在生产上把这件事【量化并闭合】：
//	  ① 内容级：形态 A（上一轮 outbound_body 在库里是 jsonb 字面量 'null'）
//	     ⇒ 差集基线【读空】⇒ 相邻轮 13/14 成立「delta(t) ⊇ delta(t−1) 全部消息」，
//	     且公共前缀长度恰等于上一轮长度 ⇒ ⇒ 每轮把整份历史重存一遍；
//	  ② 形态 B（outbound_body 是数组）⇒ 基线有效 ⇒ 相邻轮 0/22 成立 ⇒ 真只存新增；
//	  ③ 体量：10 月分区 request_delta 合计 2.63 GB = 分区 5.09 GB 的 51.6%，
//	     而 response_delta（≈每轮真实新增）只有 57.8 MB ⇒ ⇒ delta 是真实新增的 45 倍；
//	  ④ ≥10 轮的 220 个会话里 127 个是二次方形态，持有 747 MB，却只占 27.4% 的轮数。
//
// ★★ 为什么判据是【三条「整份落盘」分支的计数】而不是「某个字符串在不在」：
//	★★ 真正的守门物是「**这个函数会把完整历史落成 delta 的入口有几条**」——
//	★★ 一旦有人【新增】一条这样的分支（例如再补一种 submit_mode），计数就会变；
//	★★ 一旦有人【删掉】一条（例如把 `len(req.LastOutboundBody) == 0` 分支改成
//	★★ 返回空切片），计数也会变 ⇒ ⇒ ⇒ 判据能【双向】感知改动。
//
// ★ 处置范式（按本仓纪律）：★ **正确意图 + 已定位缺陷 ⇒ 钉住现状 + FIXME +
// 反向断言（不许无声存在）** ⇒ ⇒ 本门就是那个「反向断言」。
//
// ★★ 为什么判据 ② 是【反向断言】：
//	★★ `bodies_writer.go` 的注释原文写着
//	★★   「…avoiding exponential growth of storing full history in every row.」
//	★★ —— 这是【设计意图】的原文证据；★ 一旦有人修好基线（让 delta 真的只存新增），
//	★★ 这句注释就该改写 ⇒ ⇒ 门必须红来提醒同步 FIXME-14373 与设计意图的表述。
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
	section14373WriterPath = "domains/session/v2/session_writer_v2.go"
	section14373BodiesPath = "domains/session/v2/bodies_writer.go"
	section14373AuditPath  = "scripts/audit/verify_session_bodies_delta_amplification.sql"
)

func repoRoot14373(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// parseGoSrc14373 解析 Go 源文件并同时返回 AST 与原始字节（★ 原始字节用来切函数体）。
func parseGoSrc14373(t *testing.T, path string) (*ast.File, []byte) {
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
	return f, src
}

// countOccurrences14373 统计子串出现次数（★ 门自己要用它 ⇒ 必须能被自证不是常量）。
func countOccurrences14373(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	return strings.Count(haystack, needle)
}

// commentGroups14373 取出文件里所有注释组（★ 含包文档：ast.File.Comments 不含 f.Doc）。
func commentGroups14373(f *ast.File) []*ast.CommentGroup {
	groups := make([]*ast.CommentGroup, 0, len(f.Comments)+1)
	if f.Doc != nil {
		groups = append(groups, f.Doc)
	}
	groups = append(groups, f.Comments...)
	return groups
}

// commentGroupHasAll14373 报告是否存在某个注释组同时含有全部 needle。
func commentGroupHasAll14373(f *ast.File, needles ...string) bool {
	for _, cg := range commentGroups14373(f) {
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

// TestSection14373DeltaHasThreeFullBodyBranches pins 判据 ①：
// `extractRequestDelta` 里【把完整历史整份落成 delta】的分支必须仍是三条。
//
// ★ 三条分别是：
//
//	① submit_mode ∈ {delta, snapshot, inferred_compressed} ⇒ return req.RequestBody；
//	② len(req.LastOutboundBody) == 0（基线读空）⇒ return req.RequestBody；
//	③ len(delta) == 0（差集为空回落）⇒ return req.RequestBody。
//
// ★ 计数用 `return req.RequestBody` 的出现次数 ⇒ 增删任一分支都会让门变红。
func TestSection14373DeltaHasThreeFullBodyBranches(t *testing.T) {
	root := repoRoot14373(t)
	writerPath := filepath.Join(root, section14373WriterPath)
	src, err := os.ReadFile(writerPath)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", writerPath, err)
	}
	// ★ 只看 extractRequestDelta 函数体内部的分支 ⇒ 不能把文件其它地方的
	// ★ `return req.RequestBody` 算进来（否则计数判据会被稀释）。
	body := extractFuncBody14373(t, writerPath, src, "extractRequestDelta")

	got := countOccurrences14373(body, "return req.RequestBody")
	if got < 3 {
		t.Fatalf("extractRequestDelta 里的「整份落盘」分支只剩 %d 条（期望 ≥3）：\n%s", got, body)
	}
	for _, needle := range []string{
		`submitMode == "inferred_compressed"`,
		"len(req.LastOutboundBody) == 0",
		"len(delta) == 0",
		"buildMessageSet(req.LastOutboundBody)",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("extractRequestDelta 里缺少关键分支锚点 %q：\n%s", needle, body)
		}
	}
}

// extractFuncBody14373 返回指定函数的【函数体】源码文本。
func extractFuncBody14373(t *testing.T, path string, src []byte, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	var fd *ast.FuncDecl
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Name != nil && d.Name.Name == name && d.Body != nil {
			fd = d
			break
		}
	}
	if fd == nil {
		t.Fatalf("找不到函数 %s", name)
	}
	start := fset.Position(fd.Body.Lbrace).Offset
	end := fset.Position(fd.Body.Rbrace).Offset
	if end <= start || end > len(src) {
		t.Fatalf("函数 %s 的源码区间异常：start=%d end=%d len=%d", name, start, end, len(src))
	}
	return string(src[start : end+1])
}

// TestSection14373BaselineStillComesFromStoredBodies pins 判据 ②：
// 差集基准必须仍然来自【库里上一轮的 bodies 行】（⇒ 这正是「基线可能为空」的成因）。
func TestSection14373BaselineStillComesFromStoredBodies(t *testing.T) {
	root := repoRoot14373(t)
	writerPath := filepath.Join(root, section14373WriterPath)
	src, err := os.ReadFile(writerPath)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", writerPath, err)
	}
	text := string(src)

	for _, needle := range []string{
		"GetLatestBodiesInTx",
		"req.LastOutboundBody = previousBody.OutboundBody",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("差集基准的来源锚点 %q 不在了（★ 基线一旦改成别的来源 ⇒ FIXME-14373 要撤）", needle)
		}
	}
}

// TestSection14373AmplificationDefectStillRegistered pins 判据 ③：
// ① 「放大」这个缺陷必须仍被登记（FIXME-14373 注释组，且必须同时点明基线来源与空基线分支）；
// ② 设计意图的原文证据（「avoiding exponential growth」）必须仍在 bodies_writer.go。
func TestSection14373AmplificationDefectStillRegistered(t *testing.T) {
	root := repoRoot14373(t)

	writerF, _ := parseGoSrc14373(t, filepath.Join(root, section14373WriterPath))
	// ★★ needle 必须带【登记标记的完整形态】"FIXME-14373（"（全角左括号），
	// ★★ 不能只写 "FIXME-14373" —— ★ 因为子串匹配会让【把登记改名】这种变异逃过门
	// ★★ （实测：改成 "FIXME-14373-RETIRED" 时，裸子串判据仍然绿 ⇒ 门没牙）。
	if !commentGroupHasAll14373(writerF, "FIXME-14373（", "previousBody.OutboundBody", "len(req.LastOutboundBody) == 0") {
		t.Fatal("★ 登记块 FIXME-14373 不在了，或它没有同时点明「基线来源」与「空基线分支」⇒ " +
			"（★ 若基线已修好 ⇒ defect 不再存在 ⇒ 请同步撤掉 FIXME-14373 与本门）")
	}

	bodiesF, _ := parseGoSrc14373(t, filepath.Join(root, section14373BodiesPath))
	if !commentGroupHasAll14373(bodiesF, "avoiding exponential growth") {
		t.Fatal("★ 设计意图的原文证据（bodies_writer.go 的「avoiding exponential growth」）不在了 ⇒ " +
			"⇒ 请确认是不是已经改成别的表述，并同步更新 FIXME-14373 的登记内容")
	}
}

// TestSection14373AuditScriptPinsBothCalibers pins 判据 ④：
// 复算脚本必须同时钉住【两个口径】——形状（二阶系数）与基线（outbound_body 的 jsonb 形态）。
//
// ★ 为什么必须两个口径都在：★ 只看形状会把「形态 B 的个别异常轮」误判成放大；
// ★ 只看基线会漏掉「二次方但基线有效」的组合 ⇒ ⇒ 缺一个口径，脚本就不能复算本轮结论。
func TestSection14373AuditScriptPinsBothCalibers(t *testing.T) {
	root := repoRoot14373(t)
	auditPath := filepath.Join(root, section14373AuditPath)
	b, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("读取复算脚本 %s 失败：%v", auditPath, err)
	}
	// ★★★ 必须【剥掉注释】再查：★ 否则注释里提到的锚点会让判据恒真
	// ★★★ （实测变异 M6：把代码里的 jsonb_typeof(outbound_body)='array' 换掉，
	// ★★★   注释里还留着同一个词 ⇒ 裸子串判据仍然绿 ⇒ 门没牙）。
	code := stripSQLComments14373(string(b))

	for _, needle := range []string{
		"count(*) * count(*)",                 // 形状口径：二阶系数
		"pg_column_size(request_delta)",       // 形状口径：落盘字节
		"jsonb_typeof(outbound_body)='array'", // 基线口径：库里存的是数组
		"jsonb_typeof(outbound_body)='null'",  // 基线口径：库里存的是 jsonb 字面量 'null'
	} {
		if !strings.Contains(code, needle) {
			t.Fatalf("复算脚本 %s 的【可执行代码】里缺少口径锚点 %q（★ 两个口径缺一个 ⇒ 就不能复算本轮结论）", section14373AuditPath, needle)
		}
	}
}

// stripSQLComments14373 去掉 SQL 里的行注释（--）与块注释（/* */），只留可执行代码。
//
// ★ 为什么必须有它：★ 判据要守的是【口径代码还在不在】，而不是【这个词还在不在】——
// ★ 而本仓的审计脚本大量用注释解释口径 ⇒ ⇒ 注释里的锚点会让判据恒真。
func stripSQLComments14373(sqlText string) string {
	var out strings.Builder
	inBlock := false
	for _, line := range strings.Split(sqlText, "\n") {
		// 块注释跨行：逐字符扫，处理 /* ... */
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if idx := strings.Index(trimmed, "*/"); idx >= 0 {
				inBlock = false
				trimmed = trimmed[idx+2:]
			} else {
				continue
			}
		}
		for {
			openIdx := strings.Index(trimmed, "/*")
			lineIdx := strings.Index(trimmed, "--")
			if openIdx >= 0 && (lineIdx < 0 || openIdx < lineIdx) {
				out.WriteString(trimmed[:openIdx])
				if end := strings.Index(trimmed[openIdx:], "*/"); end >= 0 {
					trimmed = trimmed[openIdx+end+2:]
					continue
				}
				inBlock = true
				trimmed = ""
				break
			}
			if lineIdx >= 0 {
				out.WriteString(trimmed[:lineIdx])
				trimmed = ""
			} else {
				out.WriteString(trimmed)
			}
			break
		}
		out.WriteString("\n")
	}
	return out.String()
}

// TestSection14373JudgesAreNotConstant 自证判据不是常量：
// ① 计数器在合成源码上必须给出【与输入一致】的数（而不是写死 3）；
// ② 真实文件里的计数必须确实 ≥3（否则上面的判据是恒假的空壳）。
func TestSection14373JudgesAreNotConstant(t *testing.T) {
	// ① 合成源码自证
	if got := countOccurrences14373("return req.RequestBody; return req.RequestBody;", "return req.RequestBody"); got != 2 {
		t.Fatalf("★ 计数器不是自证的：合成源码里 2 处却数出 %d 处 ⇒ 判据可能是写死的", got)
	}
	if got := countOccurrences14373("", "return req.RequestBody"); got != 0 {
		t.Fatalf("★ 空输入应得 0，却得 %d", got)
	}

	// ② 剥注释器自证：★ 剥注释器本身必须能工作（否则判据④要么恒红要么恒绿）。
	probe := "-- 注释里写着 jsonb_typeof(outbound_body)='array'\nSELECT jsonb_typeof(outbound_body)='null';\n"
	stripped := stripSQLComments14373(probe)
	if strings.Contains(stripped, "注释里写着") {
		t.Fatal("★ 剥注释器没把行注释剥掉 ⇒ 判据④会恒真")
	}
	if !strings.Contains(stripped, "jsonb_typeof(outbound_body)='null'") {
		t.Fatal("★ 剥注释器把代码也剥掉了 ⇒ 判据④会恒假")
	}
	if strings.Contains(stripped, "jsonb_typeof(outbound_body)='array'") {
		t.Fatal("★ 剥注释器没把注释里的锚点剥掉 ⇒ 判据④会恒真")
	}

	// ③ 真实文件
	root := repoRoot14373(t)
	writerPath := filepath.Join(root, section14373WriterPath)
	src, err := os.ReadFile(writerPath)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", writerPath, err)
	}
	body := extractFuncBody14373(t, writerPath, src, "extractRequestDelta")
	if got := countOccurrences14373(body, "return req.RequestBody"); got < 3 {
		t.Fatalf("真实文件里只数出 %d 处「整份落盘」⇒ 判据 ① 现在是恒假 ⇒ 门没有牙", got)
	}
}
