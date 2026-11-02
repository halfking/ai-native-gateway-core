package streaming

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// ── F11 的入口侧闸门（2026-11-02 第五轮）────────────────────────────────
//
// ★ 守的断言：
//
//	「给初始候选上过模态门的那道模态，必须原样带进执行器。」
//
// 为什么是**审计**（读源码）而不是行为测试：入口把 ExecParams 交给执行器
// 之后，模态门的效果要等到执行器里**再解析一次候选**才看得见，而那两次
// 再解析分别躺在 dispatch 换模态路径与探针后刷新路径上——在入口层做行为
// 断言需要把整个 dispatch 管线搭起来才能观察到，且观察到的也只是
// 「间接效果」。
//
// ⇒ 这里用**闭集审计**守入口侧的接线（执行器侧的行为由
// executors/modality_gate_test.go 守）：漏带 / 带错 ⇒ 当场红并指出文件。
//
// ★ 审计器必须自己先被证明有牙（阴性对照）：合成一份漏带模态的源码 ⇒
// 审计器必须报出来；否则「扫描器根本没找到 ExecParams 字面量」也会让
// 主门全绿（恒绿陷阱）。
const (
	modalityCarrierFunc     = "resolveCandidatesForRequest"
	modalityCarrierFuncOmni = "resolveOmniFreeCandidates"
	modalityCarrierIdx      = 2 // (candidates, policy, modality, ...) 的第 3 个返回值
)

// modalityBinding 记录一次候选解析把「候选集变量」与「模态变量」绑在一起。
type modalityBinding struct {
	candidates string
	modality   string
}

// execParamsModalityAudit 审一份已解析的源码，返回
// ① 发现的 ExecParams 字面量数、② 每处违规的说明。
//
// 规则 A（闭集）：每一个 `&executors.ExecParams{...}` 字面量都必须带
//
//	`RequestModality` 键——**任何**新增的构造点都在覆盖范围内。
//
// 规则 B（取值来源）：`RequestModality` 的取值必须是本文件里**候选解析**
//
//	返回的那个模态变量（resolveCandidatesForRequest / resolveOmniFreeCandidates
//	的第 3 个返回值）——不是空串、不是别的变量、不是字面量。
//
// 规则 C（配对）：带进 ExecParams 的候选集与模态必须来自**同一次**解析。
//
//	⇒ 否则执行器会拿 A 解析的候选、按 B 解析的模态去复核——两道门用上不同
//	的模态，模态门就只管了第一段路。
func execParamsModalityAudit(file *ast.File) (int, []string) {
	carriers := map[string]bool{}
	var bindings []modalityBinding

	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) <= modalityCarrierIdx {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		fun, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch fun.Name {
		case modalityCarrierFunc, modalityCarrierFuncOmni:
		default:
			return true
		}
		candIdent, ok1 := assign.Lhs[0].(*ast.Ident)
		modIdent, ok2 := assign.Lhs[modalityCarrierIdx].(*ast.Ident)
		if !ok1 || !ok2 {
			return true
		}
		if modIdent.Name == "_" {
			// ★ 模态被这次解析**丢掉**了（`_`）——记下来，规则 C 会追问：
			//   这批候选是在哪个模态下解析出来的？丢掉 ⇒ 后面没法复核。
			bindings = append(bindings, modalityBinding{candidates: candIdent.Name, modality: ""})
			return true
		}
		carriers[modIdent.Name] = true
		bindings = append(bindings, modalityBinding{candidates: candIdent.Name, modality: modIdent.Name})
		return true
	})

	count := 0
	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isExecParamsType(lit.Type) {
			return true
		}
		count++
		where := file.Name.Name
		var modalityValue ast.Expr
		var candidatesName string
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "RequestModality":
				modalityValue = kv.Value
			case "Candidates":
				if ident, ok := kv.Value.(*ast.Ident); ok {
					candidatesName = ident.Name
				}
			}
		}
		if modalityValue == nil {
			violations = append(violations, where+": ExecParams 字面量没有带 RequestModality ⇒ 执行器里的候选再解析会退回无模态过滤的查询")
			return true
		}
		ident, ok := modalityValue.(*ast.Ident)
		if !ok {
			violations = append(violations, where+": RequestModality 取值不是模态变量（取到了非标识符表达式）⇒ 模态没有跟着请求走")
			return true
		}
		if !carriers[ident.Name] {
			violations = append(violations, where+": RequestModality 取值 "+ident.Name+" 不是候选解析返回的模态变量 ⇒ 模态没有跟着请求走")
			return true
		}
		if candidatesName != "" {
			for _, binding := range bindings {
				if binding.candidates != candidatesName {
					continue
				}
				if binding.modality == "" {
					violations = append(violations, where+": 候选集 "+candidatesName+" 来自一次**丢掉了模态**的解析（`_`）⇒ 执行器拿不到这批候选对应的模态，后续复核只能退回无模态过滤的查询")
					continue
				}
				if binding.modality != ident.Name {
					violations = append(violations, where+": 候选集 "+candidatesName+" 与模态 "+ident.Name+" 不是同一次解析的结果（那次解析的模态是 "+binding.modality+"）⇒ 执行器会拿一批候选、按另一道模态去复核")
				}
			}
		}
		return true
	})
	return count, violations
}

func isExecParamsType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "executors" && sel.Sel.Name == "ExecParams"
}

// productionGoFiles 返回包目录下的全部非测试 .go 文件名。
func productionGoFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取包目录失败：%v", err)
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatalf("包目录下没有非测试 .go 文件 ⇒ 审计范围写错了，门会恒绿")
	}
	return out
}

// TestExecParamsCarryRequestModality 是 F11 入口侧的闭集门：包里**任何**
// 非测试文件里构造的 ExecParams 都必须带上请求模态。
func TestExecParamsCarryRequestModality(t *testing.T) {
	fset := token.NewFileSet()
	total := 0
	for _, name := range productionGoFiles(t) {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		parsed, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", name, err)
		}
		count, violations := execParamsModalityAudit(parsed)
		total += count
		for _, violation := range violations {
			t.Errorf("%s", violation)
		}
	}
	// ★ 恒绿陷阱的护栏：审计器一个 ExecParams 字面量都没找到 ⇒ 它根本没在工作
	//（类型名被改 / 构造点被搬走 / 扫描范围写错）⇒ 上面的断言会「全绿」。
	if total < 5 {
		t.Fatalf("只审计到 %d 处 ExecParams 字面量（预期 ≥5）⇒ 审计器本身失效，门会恒绿", total)
	}
}

// TestExecParamsModalityAudit_HasTeeth 是阴性对照：证明审计器会真的报出来。
// 没有它，「审计器什么都没找到」与「审计器找到了但没报」都表现为全绿。
func TestExecParamsModalityAudit_HasTeeth(t *testing.T) {
	fset := token.NewFileSet()

	cases := []struct {
		name      string
		src       string
		wantCount int
		wantBad   bool
	}{
		{
			name: "漏带模态",
			src: `package p

import "x/executors"

func f() {
	_ = &executors.ExecParams{}
}
`,
			wantCount: 1,
			wantBad:   true,
		},
		{
			name: "带的是别的变量",
			src: `package p

import "x/executors"

func f() {
	candidates, policy, requestModality, err := resolveCandidatesForRequest()
	_ = candidates
	_ = policy
	_ = err
	_ = &executors.ExecParams{RequestModality: someOtherVar}
}
`,
			wantCount: 1,
			wantBad:   true,
		},
		{
			name: "带的是字面量",
			src: `package p

import "x/executors"

func f() {
	candidates, policy, requestModality, err := resolveCandidatesForRequest()
	_ = candidates
	_ = policy
	_ = err
	_ = &executors.ExecParams{RequestModality: "text"}
}
`,
			wantCount: 1,
			wantBad:   true,
		},
		{
			name: "取的是另一个返回值",
			src: `package p

import "x/executors"

func f() {
	candidates, policy, requestModality, err := resolveCandidatesForRequest()
	_ = policy
	_ = err
	_ = &executors.ExecParams{RequestModality: candidates}
}
`,
			wantCount: 1,
			wantBad:   true,
		},
		{
			name: "候选来自一次丢掉模态的解析",
			src: `package p

import "x/executors"

func f() {
	candidates, policy, requestModality, err := resolveCandidatesForRequest()
	_ = candidates
	_ = policy
	_ = err
	otherCands, otherPolicy, _, otherErr := resolveCandidatesForRequest()
	_ = otherCands
	_ = otherPolicy
	_ = otherErr
	_ = &executors.ExecParams{Candidates: otherCands, RequestModality: requestModality}
}
`,
			wantCount: 1,
			wantBad:   true,
		},
		{
			name: "候选与模态来自不同的解析",
			src: `package p

import "x/executors"

func f() {
	candidates, policy, requestModality, err := resolveCandidatesForRequest()
	_ = candidates
	_ = policy
	_ = err
	otherCands, otherPolicy, otherModality, otherErr := resolveCandidatesForRequest()
	_ = otherCands
	_ = otherPolicy
	_ = otherModality
	_ = otherErr
	_ = &executors.ExecParams{Candidates: otherCands, RequestModality: requestModality}
}
`,
			wantCount: 1,
			wantBad:   true,
		},
		{
			name: "合规",
			src: `package p

import "x/executors"

func f() {
	candidates, policy, requestModality, err := resolveCandidatesForRequest()
	_ = candidates
	_ = policy
	_ = err
	_ = &executors.ExecParams{RequestModality: requestModality}
}
`,
			wantCount: 1,
			wantBad:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parser.ParseFile(fset, tc.name, tc.src, parser.ParseComments)
			if err != nil {
				t.Fatalf("解析合成源码失败：%v", err)
			}
			count, violations := execParamsModalityAudit(parsed)
			if count != tc.wantCount {
				t.Fatalf("审计到的 ExecParams 字面量数 = %d, want %d", count, tc.wantCount)
			}
			if tc.wantBad && len(violations) == 0 {
				t.Fatalf("审计器对「%s」没有报出任何违规 ⇒ 它没有牙", tc.name)
			}
			if !tc.wantBad && len(violations) != 0 {
				t.Fatalf("审计器对合规源码报了违规：%v", violations)
			}
		})
	}
}
