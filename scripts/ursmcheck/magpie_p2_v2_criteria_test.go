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
//	   ★ 代码面 = go/parser 解析出的表达式 + 字符串字面量语料，**注释不计入**；
//	   ★ 且**逐测试归位** ⇒ A 测试里的一个 `ParseInt` 不能替 B 语义背书）；
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

// magpieP2CodeCorpus 提取一个 Go 文件的**代码面**语料。
//
// ★★ 这是本门最要紧的一处设计：语料只由 **AST 节点**拼成 ——
// ★★ ① 字符串字面量（BasicLit.Value）
// ★★ ② 表达式（BinaryExpr / CallExpr，用 go/printer 打印成规范格式）
// ★★ ③ 标识符名
// ⇒⇒ **注释不是 AST 节点** ⇒⇒ 注释里的任何字串都**进不了**语料。
//
// ★⇒ 于是「在注释里写一句『我断言了 X』」蒙混不过去 ——
// ★⇒ 而**这正是**「测试存在但名不符实」这个门抓不到的缺陷的解法。
//
// ★ 反过来：★ 判据**宁可假红也不假绿** ——
// ★ 「代码面」比「全文」窄 ⇒ 命中要求更严 ⇒ 假绿被根除。
func magpieP2CodeCorpus(t *testing.T, root, rel string) string {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败（⇒ 门无法取它的代码面）：%v", rel, err)
	}

	var b strings.Builder
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			b.WriteString(" ")
			b.WriteString(v.Value)
		case *ast.BinaryExpr:
			b.WriteString(" ")
			_ = printer.Fprint(&b, fset, v)
		case *ast.CallExpr:
			b.WriteString(" ")
			_ = printer.Fprint(&b, fset, v)
		case *ast.Ident:
			b.WriteString(" ")
			b.WriteString(v.Name)
		}
		return true
	})
	return b.String()
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
				corpus := magpieP2CodeCorpus(t, root, w)
				for _, req := range asserts {
					if !strings.Contains(corpus, req) {
						t.Errorf("差异 %s（%s）要求的语义断言 %q **不在**测试 %s 的代码面里（文件：%s）⇒ "+
							"★ 「测试存在」但**代码里没有断言这件事** ⇒ 名不符实\n"+
							"★ ⇒ 修法：要么把该断言真正写进那个测试，要么这条差异其实没被覆盖",
							d.ID, d.Dimension, req, testName, w)
					}
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
