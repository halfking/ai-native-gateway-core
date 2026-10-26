package ursm_test

// §143.71 的门：把「历史正文恢复机制」的**四个不可静默消失的属性**钉成静态判据。
//
// ★ 为什么这道门现在才写（§143.71 的起因）：
//
//	§143.70 查实了「2026-09 的会话正文在库里与 PG 备份里都是零」，
//	唯一还活着的载体是网关侧的**降级兜底文件**。
//	§143.71 用**仓内自己的**读取器重测（不是等价实现）⇒ ★ 读数比 §143.70 高 130%
//	⇒ 且实测证明兜底文件装的是【累积正文（matryoshka）】⇒ ⇒ **恢复不是碎片，
//	而是「整条正文链」** ⇒ ⇒ **恢复路径在技术上是通的**。
//
// ★ 于是风险从「能不能恢复」变成「**恢复所需的机制会不会被无声删掉**」——
//
//	① 拆分逻辑（累积体 → 每轮增量）唯一的可复用实现是
//	   `cmd/tools/backfill_session_bodies/derive.go` 的 DeriveTurnDeltas /
//	   ParseRequestMessages（★ 那个工具本体已于 2026-10-20 退役，因为 v1 累积正文族
//	   被迁移 890 DROP；但它的头注明确写「将来若再有『从别的存储面接回累积正文』的
//	   需求，它们仍然可用」⇒ ★ **兜底文件正是那个『别的存储面』**）。
//	② 保真面：v2 的 `Message` 必须仍带 `ContentRaw`（★ 逐字保留 provider 原生内容）
//	   ⇒ 否则恢复出来的东西会退化成纯文本投影 ⇒ 多模态 / 工具调用结构丢失。
//	③ 口径面：分区键 `partition_date` 取 **UTC 日历日**（§143.68 的 `FIXME-14368`）
//	   ⇒ 恢复写入必须用**同一口径** ⇒ ⇒ 该登记不得消失。
//	④ 只读面：打捞工具必须**拒绝**把输出写进 {base}/backups
//	   ⇒ ★ 兜底文件是唯一还活着的正文载体 ⇒ 工具覆盖它 = 自己删掉恢复面。
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
	section14371DerivePath    = "cmd/tools/backfill_session_bodies/derive.go"
	section14371DeriveTest    = "cmd/tools/backfill_session_bodies/derive_test.go"
	section14371BodiesWriter  = "domains/session/v2/bodies_writer.go"
	section14371PartitionDate = "domains/session/v2/partition_date.go"
	section14371SalvageTool   = "cmd/tools/dbdegradation-salvage/main.go"
	section14371SalvageTest   = "cmd/tools/dbdegradation-salvage/main_test.go"
	section14371PrefixTool    = "cmd/tools/dbdegradation-salvage/prefix_analyze.go"
	section14371PrefixTest    = "cmd/tools/dbdegradation-salvage/prefix_analyze_test.go"
)

func repoRoot14371(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func parseGo14371(t *testing.T, path string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	// ★★ 必须带 parser.ParseComments：★ 不带的话注释【根本不会进 AST】
	//   （f.Doc 与 f.Comments 恒为空）⇒ ⇒ 任何「判某条登记是否还在」的判据
	//   都会【恒假】⇒ ⇒ 门会一直红（假红）⇒ ★ 解析器与判据要配套。
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	return f
}

// structHasField14371 报告名为 typeName 的结构体是否有字段 fieldName。
func structHasField14371(t *testing.T, f *ast.File, typeName, fieldName string) (found bool, fieldType string) {
	t.Helper()
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
				if name.Name == fieldName {
					found = true
					if id, ok := fld.Type.(*ast.Ident); ok {
						fieldType = id.Name
					} else if sel, ok := fld.Type.(*ast.SelectorExpr); ok {
						if x, ok := sel.X.(*ast.Ident); ok {
							fieldType = x.Name + "." + sel.Sel.Name
						}
					}
				}
			}
		}
		return false
	})
	return found, fieldType
}

// hasFunc14371 报告文件里是否声明了名为 name 的函数。
func hasFunc14371(t *testing.T, f *ast.File, name string) bool {
	t.Helper()
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if ok && fd.Name.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// TestSection14371RecoveryDerivationSurvives pins 判据 ①：
// 累积体 → 增量的拆分逻辑必须仍在，且必须有单测覆盖。
func TestSection14371RecoveryDerivationSurvives(t *testing.T) {
	root := repoRoot14371(t)
	derivePath := filepath.Join(root, section14371DerivePath)
	raw, err := os.ReadFile(derivePath)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", section14371DerivePath, err)
	}
	f := parseGo14371(t, derivePath)

	for _, fn := range []string{"DeriveTurnDeltas", "ParseRequestMessages"} {
		if !hasFunc14371(t, f, fn) {
			t.Errorf("%s 里没有 %s ⇒ ★★ 恢复路径唯一的可复用实现没了 "+
				"（★ 兜底文件装的正是「累积正文」⇒ 这两个函数就是把它拆成每轮增量的全部机制）"+
				"⇒ ⇒ 请同步本门，或在删除前先给属主一个替代方案", section14371DerivePath, fn)
		}
	}
	// ★ 契约文本不得被悄悄改掉：它声明了「累积体」这个前提。
	if !strings.Contains(string(raw), "fullMsgs[i]") {
		t.Errorf("%s：找不到 `fullMsgs[i]` 契约注释 ⇒ ★ 「累积体」这个前提的说明被改掉了 "+
			"⇒ ⇒ 恢复语义的依据要重新确认 ⇒ 请同步本门", section14371DerivePath)
	}

	// ★ 单测必须还在（纯函数没有单测覆盖 = 没有被验证过）。
	testPath := filepath.Join(root, section14371DeriveTest)
	testRaw, err := os.ReadFile(testPath)
	if err != nil {
		t.Errorf("读 %s 失败：%v ⇒ ★ 拆分逻辑的单测不见了 ⇒ 请同步本门", section14371DeriveTest, err)
		return
	}
	for _, fn := range []string{"DeriveTurnDeltas", "ParseRequestMessages"} {
		if !strings.Contains(string(testRaw), fn) {
			t.Errorf("%s 里没有对 %s 的覆盖 ⇒ ★ 拆分逻辑失去单测保护 ⇒ 请同步本门",
				section14371DeriveTest, fn)
		}
	}
}

// TestSection14371MessageKeepsContentRaw pins 判据 ②：v2 的 Message 必须保真。
//
// ★ 理由：退役工具的 `Msg` 投影只有 Role + Content（**纯文本**）⇒ 若恢复也用那个投影，
//
//	多模态内容与工具调用结构会丢 ⇒ ⇒ 「恢复出来的正文」与「原来的正文」不等价。
func TestSection14371MessageKeepsContentRaw(t *testing.T) {
	root := repoRoot14371(t)
	path := filepath.Join(root, section14371BodiesWriter)
	f := parseGo14371(t, path)
	found, typ := structHasField14371(t, f, "Message", "ContentRaw")
	if !found {
		t.Errorf("%s：`Message` 没有 `ContentRaw` 字段 ⇒ ★★ 正文恢复出来会退化成纯文本投影 "+
			"⇒ 多模态 / 工具调用结构丢失 ⇒ ⇒ 「恢复出来的」≠「原来的」⇒ 请同步本门",
			section14371BodiesWriter)
		return
	}
	if typ != "json.RawMessage" {
		t.Errorf("%s：`Message.ContentRaw` 的类型是 %q（期望 json.RawMessage）⇒ ★ 保真面被改了 "+
			"⇒ 请同步本门", section14371BodiesWriter, typ)
	}
}

// TestSection14371PartitionKeyCaliberStillRegistered pins 判据 ③：
// 分区键的 UTC 日历日口径必须仍被登记。
//
// ★ 理由：恢复写入必须用**同一口径**（§143.68：`calendarDate` = `t.UTC()` 取日），
//
//	否则恢复出来的行会落进与原行不同的月份分区 ⇒ ⇒ 查「9 月」会漏。
func TestSection14371PartitionKeyCaliberStillRegistered(t *testing.T) {
	root := repoRoot14371(t)
	raw, err := os.ReadFile(filepath.Join(root, section14371PartitionDate))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", section14371PartitionDate, err)
	}
	body := string(raw)
	if !strings.Contains(body, "FIXME-14368") {
		t.Errorf("%s：找不到 `FIXME-14368` 登记 ⇒ ★ 分区键口径（UTC 日历日 vs Asia/Shanghai 零点）"+
			"的错位不再被登记 ⇒ ⇒ 恢复写入时无人知道该用哪个口径 ⇒ 请同步本门",
			section14371PartitionDate)
	}
	if !strings.Contains(body, "Asia/Shanghai") {
		t.Errorf("%s：登记里没有提到 `Asia/Shanghai` ⇒ ★ 错位的一方（正典）没被写明 "+
			"⇒ ⇒ 恢复口径无从对照 ⇒ 请同步本门", section14371PartitionDate)
	}
}

// TestSection14371SalvageToolStaysReadOnly pins 判据 ④：打捞工具必须只读。
//
// ★ 理由：兜底文件是**唯一还活着的历史正文载体**（§143.70）⇒ 一个能把输出写回
//
//	同一目录的工具 = 自己删掉恢复面 ⇒ ⇒ 护栏必须在，且必须有单测覆盖。
func TestSection14371SalvageToolStaysReadOnly(t *testing.T) {
	root := repoRoot14371(t)
	toolPath := filepath.Join(root, section14371SalvageTool)
	f := parseGo14371(t, toolPath)
	if !hasFunc14371(t, f, "refuseBackupDirWrite") {
		t.Errorf("%s：找不到 `refuseBackupDirWrite` 护栏 ⇒ ★★ 打捞工具可以往 {base}/backups "+
			"里写输出 ⇒ ⇒ 而兜底文件是唯一还活着的正文载体 ⇒ ⇒ 请同步本门",
			section14371SalvageTool)
	}
	testPath := filepath.Join(root, section14371SalvageTest)
	testRaw, err := os.ReadFile(testPath)
	if err != nil {
		t.Errorf("读 %s 失败：%v ⇒ ★ 只读护栏没有单测保护 ⇒ 请同步本门", section14371SalvageTest, err)
		return
	}
	if !strings.Contains(string(testRaw), "refuseBackupDirWrite") {
		t.Errorf("%s：没有对 `refuseBackupDirWrite` 的覆盖 ⇒ ★ 只读护栏没有单测保护 ⇒ 请同步本门",
			section14371SalvageTest)
	}
}

// commentGroupHas14371 报告某个注释组里是否同时含有 needle 与 alsoNeedle。
//
// ★ 为什么不用裸子串：★ 「登记存在」类判据若用裸子串，会被
//
//	① 写在别处的散文、② 已被作废的写法（"FIXME-14371 已关闭"）同时骗过
//	⇒ ⇒ 这里走 go/ast 的 CommentGroup ⇒ 结构性检出。
func commentGroupHas14371(f *ast.File, needle, alsoNeedle string) bool {
	// ★★ `ast.File.Comments` 的文档明确写着：它【不含包文档】（包文档在 f.Doc）
	//   ⇒ ⇒ 只看 f.Comments 会漏掉整个包文档 ⇒ 而本仓的登记习惯恰恰写在包文档里
	//   ⇒ ⇒ 漏看 = 门恒红（假红）⇒ ★ 必须两处都看。
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

// TestSection14371FidelityGapStillRegistered pins 判据 ⑤：
// 「退役工具的纯文本投影不能直接写回 v2」这个缺口必须仍被登记。
//
// ★ 理由（§143.71 实测）：
//
//	退役工具的 `Msg` 只有 {Role, Content}，Content 是 plainContent 投影
//	⇒ 丢 tool_calls / 多模态结构 / provider 原生形态
//	⇒ ⇒ 写回 v2 的 session_bodies 需要一层【保真适配】（原始 content → ContentRaw、
//	    tool_calls → ToolCalls）⇒ ⇒ 该适配层在仓内【不存在】
//	⇒ ⇒ ⇒ 「恢复机制在仓内已具备」这句话**必须打折**：
//	    算法内核具备，★ 适配层不具备。
//
// ★ 所以「缺口」本身要钉住：一旦哪天适配层被写出来，登记自然过时 ⇒
//
//	那时同步改本门（而不是让缺口悄悄消失）。
func TestSection14371FidelityGapStillRegistered(t *testing.T) {
	root := repoRoot14371(t)
	path := filepath.Join(root, section14371DerivePath)
	f := parseGo14371(t, path)
	if !commentGroupHas14371(f, "FIXME-14371", "ContentRaw") {
		t.Errorf("%s：找不到同时提到 `FIXME-14371` 与 `ContentRaw` 的登记注释组 ⇒ "+
			"★★ 「退役工具的纯文本投影不能直接写回 v2（缺保真适配层）」这个缺口不再被登记 "+
			"⇒ ⇒ 有人会以为恢复是现成可跑的 ⇒ ⇒ 请同步本门", section14371DerivePath)
	}
}

// TestSection14371ChainJudgeSurvives pins 判据 ⑥：
// 「可恢复轮数由【前缀链】给出」这个判定器必须仍在。
//
// ★ 理由（§143.71 实测）：★ 真实累积体会被改写、也会被裁剪 ⇒
//
//	245 上 content 级「前一条是后一条真前缀」只成立 51.6%、
//	role 级 81.2% ⇒ ⇒ 「捞回第 N 轮 ⇒ 前 N 轮都能重建」不成立
//	⇒ ⇒ 能恢复多少轮【取决于链】⇒ ⇒ 这个判定器是恢复面量级的唯一依据
//	⇒ ⇒ 它被删掉 ⇒ 恢复面的量级就退回「上界」这种高估值。
func TestSection14371ChainJudgeSurvives(t *testing.T) {
	root := repoRoot14371(t)
	path := filepath.Join(root, section14371PrefixTool)
	f := parseGo14371(t, path)
	for _, fn := range []string{"isContentPrefix", "isRolePrefix", "analyzePrefix"} {
		if !hasFunc14371(t, f, fn) {
			t.Errorf("%s：找不到 %s ⇒ ★★ 「可恢复轮数由前缀链给出」这个判定器没了 "+
				"⇒ ⇒ 恢复面的量级只能报上界（实测比链口径高一个数量级）⇒ ⇒ 请同步本门",
				section14371PrefixTool, fn)
		}
	}
	for _, field := range []string{"ContentChainTurns", "RoleChainTurns", "SplittableTurns"} {
		if ok, _ := structHasField14371(t, f, "analyzeResult", field); !ok {
			t.Errorf("%s：`analyzeResult` 没有字段 %s ⇒ ★ 链口径的读数没有出口 ⇒ 请同步本门",
				section14371PrefixTool, field)
		}
	}
	// ★ 单测必须覆盖链算法（否则链口径没被验证过）。
	testPath := filepath.Join(root, section14371PrefixTest)
	testRaw, err := os.ReadFile(testPath)
	if err != nil {
		t.Errorf("读 %s 失败：%v ⇒ ★ 链判定器没有单测保护 ⇒ 请同步本门", section14371PrefixTest, err)
		return
	}
	for _, needle := range []string{"ContentChainTurns", "RoleChainTurns"} {
		if !strings.Contains(string(testRaw), needle) {
			t.Errorf("%s 里没有对 %s 的断言 ⇒ ★ 链口径没被单测钉住 ⇒ 请同步本门",
				section14371PrefixTest, needle)
		}
	}
}

// TestSection14371JudgesAreNotConstant 用**合成源码**自证判据 ②④ 不是恒真。
func TestSection14371JudgesAreNotConstant(t *testing.T) {
	// 判据 ② 的自检：去掉 ContentRaw ⇒ 必须判不出来
	const withoutRaw = `package p
type Message struct {
	Role    string
	Content string
}`
	const withRaw = `package p
type Message struct {
	Role       string
	ContentRaw json.RawMessage
}`
	fset := token.NewFileSet()
	fa, err := parser.ParseFile(fset, "a.go", withoutRaw, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	fb, err := parser.ParseFile(fset, "b.go", withRaw, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	if ok, _ := structHasField14371(t, fa, "Message", "ContentRaw"); ok {
		t.Errorf("反例（无 ContentRaw）竟被判为有 ⇒ ★ 判据 ② 是恒真的")
	}
	ok, typ := structHasField14371(t, fb, "Message", "ContentRaw")
	if !ok || typ != "json.RawMessage" {
		t.Errorf("正例（有 ContentRaw json.RawMessage）竟未被认出（ok=%v typ=%q）⇒ ★ 判据 ② 判反了",
			ok, typ)
	}

	// 判据 ④ 的自检：没有护栏函数 ⇒ 必须判不出来
	fset2 := token.NewFileSet()
	fc, err := parser.ParseFile(fset2, "c.go", `package p
func main() {}`, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	if hasFunc14371(t, fc, "refuseBackupDirWrite") {
		t.Errorf("反例（无护栏函数）竟被判为有 ⇒ ★ 判据 ④ 是恒真的")
	}

	// ★ 判据 ⑤ 的自检：注释组里没有 FIXME-14371 ⇒ 必须判不出来；
	//   ★ 且【只有 FIXME-14371 没有提到 ContentRaw】也必须判不出来
	//   （⇒ 证明「两个 needle 同时出现」这条要求真的有区分力）。
	fset3 := token.NewFileSet()
	fd, err := parser.ParseFile(fset3, "d.go", `package p
// FIXME-14371: 只是一句无关的登记
func main() {}`, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	if commentGroupHas14371(fd, "FIXME-14371", "ContentRaw") {
		t.Errorf("反例（只有 FIXME-14371、没有 ContentRaw）竟被判为已登记 ⇒ ★ 判据 ⑤ 区分力不足")
	}
	fe, err := parser.ParseFile(fset3, "e.go", `package p
// FIXME-14371: 缺口：纯文本投影不能写回 v2（缺 ContentRaw 适配）
func main() {}`, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	if !commentGroupHas14371(fe, "FIXME-14371", "ContentRaw") {
		t.Errorf("正例（FIXME-14371 + ContentRaw 都在注释组里）竟未被认出 ⇒ ★ 判据 ⑤ 判反了")
	}

	// ★ 判据 ⑥ 的自检：没有链判定函数 ⇒ 必须判不出来。
	ff, err := parser.ParseFile(fset3, "f.go", `package p
func analyzePrefix() {}
type analyzeResult struct { SplittableTurns int }`, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	for _, fn := range []string{"isContentPrefix", "isRolePrefix"} {
		if hasFunc14371(t, ff, fn) {
			t.Errorf("反例（无 %s）竟被判为有 ⇒ ★ 判据 ⑥ 是恒真的", fn)
		}
	}
	if ok, _ := structHasField14371(t, ff, "analyzeResult", "ContentChainTurns"); ok {
		t.Errorf("反例（analyzeResult 无 ContentChainTurns）竟被判为有 ⇒ ★ 判据 ⑥ 是恒真的")
	}
}
