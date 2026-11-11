// classify_dual_classifier_divergence_test.go —— 双分类器的**分歧**登记与守卫
// （★ 2026-11-11 审计 C-5 的钉测）
//
// ── 为什么要有这道门 ────────────────────────────────────────────────
//
// 仓内纪律（V20 / R30）：「两个分类器**不得**在同一 (status, body) 上分叉」。
//既有门（classify_dual_classifier_lockstep_test.go）把这条纪律**弱化**成
// 了「正文分类器不得比状态分类器**更狠**」（★ 罚则单调性）★ —— 因为
// 「状态兜底 vs 不表态」被认定为**既有设计**而不是分叉（★ 那个文件里明写了）。
//
// ★ 但「既有设计」这个说法本身是**散文**：★ 没有任何机器检查过
// ★ 「两个分类器的分歧**只有**那几种成因」★★ ⇒ ★★ 于是「纪律」与
// ★★「实现」之间可以无限漂移（★ 而两边都还是绿的）★★。
//
// ★ C-5 的实测把这个缺口量化了：★ 审计档当时写「429 是唯一例外」
// ★ ★⇒ ★★ 实测（400..599 × 仓内语料）：★★ 分歧有两族 ——
//
//	① 空串族：ClassifyResponseBody = ""、ClassifyErrorWithBody ≠ ""
//	   （★ 正文分类器不表态、状态分类器按状态兜底）★ ⇒ ★★ 195~200 个状态码/条
//	② 异值族：两个都表态但**值不同**（★ 如 401 + "context length exceeded"
//	   ⇒ 正文分类器说 context_length_exceeded、状态分类器说 auth）
//	   ★ ⇒ ★★ 实测 **913** 例
//
// ★★ ⇒ ★★ 「429 是唯一例外」这个量化**被推翻**（★ 它只是「异值族」里
// ★★ 一个被先看到的样本）★★ ⇒ ★★ 必须把分歧**收口成可枚举的成因**。
//
// ── 这道门断言什么 ──────────────────────────────────────────────────
//
//	门 C5-a（规则集对账）：★ 两个分类器**函数体内**的正文正则规则集，
//	   其**单边集**必须**恰好**等于一份**独立文件**里登记的集合
//	   （★ errorsx/testdata/dual_classifier_rule_sets.txt）★ ★⇒ ★★
//	   ① 给一侧加规则而不登记 ⇒ 门红（★ 强制表态）；
//	   ② 删掉一侧的规则 ⇒ 门红（★ 单边集变了）；
//	   ③ 登记项被清空 ⇒ 门红（★ 恒绿护栏，★ 防「门退化成什么都不查」）。
//
//	门 C5-b（真分叉必须为零）：★ 对 400..599 × 仓内语料，★ 断言不存在
//	   这样的 (status, body)：★ **两个**分类器都**受正文影响**（★ 即
//	   同一个 status 下换掉正文，分类结果会变）★ 却给出**不同的 kind**
//	   ⇒ ★★ 那才是真正的「分叉」（★ 而非「状态优先」或「单边规则」的已知成因）。
//
// ── 诚实的覆盖边界（★ 本门**管不到**什么）─────────────────────────
//
//	① 规则提取只认**函数体内**的 `XxxRe.Match(body)` 字面形态
//	   ⇒ 经**辅助函数**间接使用的正则（★ 如 isGenericWebBody）**不在**对账范围。
//	② 语料是**有限的**（★ 仓内既有语料 + 本文件补充）⇒ ★ 语料撞不到的分叉
//	   本门抓不到（★ 这是它的固有上限，★ 不是缺陷）。
//	③ 门 C5-b 的「受正文影响」判据是**可观测**的（★ 比较同 status 下
//	   有正文/无正文两次结果）⇒ ★ 它不区分「正文规则命中」与「正文规则命中
//	   但结果恰好与无正文相同」（★ 后者对门无影响，★ 无所谓）。
package errorsx

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────
// 门 C5-a：规则集对账
// ─────────────────────────────────────────────────────────────────────────

// classifyErrorWithBodyRules / classifyResponseBodyRules 是两个分类器
// **函数体内**直接调用的正文正则规则集。
func classifierBodyRules(t *testing.T, fnName string) map[string]bool {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) 失败")
	}
	src := filepath.Join(filepath.Dir(thisFile), "classify.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", src, err)
	}
	fn := findFuncDecl(f, fnName)
	if fn == nil {
		t.Fatalf("在 %s 里找不到函数 %s", src, fnName)
	}
	out := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// ★ 认 Match 与 MatchString 两种调用形态（★ 规则标识符相同 ⇒
		// ★ 规则还是**同一条** ⇒ ★ 不该被误判成「规则不见了」）。
		if sel.Sel.Name != "Match" && sel.Sel.Name != "MatchString" {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		out[id.Name] = true
		return true
	})
	return out
}

// registeredDivergenceRules 读取**独立夹具**里登记的单边规则。
//
// ★ 为什么判据侧放**独立文件**（★ 不放在本文件里）：★ LESSONS §680 ——
// ★ 「守卫的期望值必须与被守卫内容**分文件**」★ ★⇒ ★★ 否则**同源变异**
// ★★（★ 改实现的同时改判据）★★ 会把守卫一起改掉 ⇒ ★★ 守卫致盲 ⇒ ★★
// ★★ 「变异打不中」会被误读成「守卫没牙」。
func registeredDivergenceRules(t *testing.T) map[string]map[string]string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) 失败")
	}
	path := filepath.Join(filepath.Dir(thisFile), "testdata", "dual_classifier_rule_sets.txt")
	f, err := os.Open(path)
	if err != nil {
		// ★ 读不到 ⇒ **fail-closed**（★ 「不可判」≠「没发现问题」）。
		t.Fatalf("读不到登记文件 %s：%v（★ 读不到 ⇒ 必须判失败 ⇒不得静默跳过）", path, err)
	}
	defer f.Close()

	out := map[string]map[string]string{
		"ClassifyErrorWithBody": {},
		"ClassifyResponseBody":  {},
	}
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		parts := strings.Split(text, "|")
		if len(parts) != 3 {
			t.Fatalf("%s:%d 格式错误（期望 <分类器>|<规则>|<理由>）：%q", path, line, text)
		}
		cls := strings.TrimSpace(parts[0])
		rule := strings.TrimSpace(parts[1])
		reason := strings.TrimSpace(parts[2])
		if _, ok := out[cls]; !ok {
			t.Fatalf("%s:%d 分类器名未知：%q", path, line, cls)
		}
		if rule == "" || reason == "" {
			t.Fatalf("%s:%d 规则名与理由都不得为空：%q", path, line, text)
		}
		out[cls][rule] = reason
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestDualClassifierRuleSetDivergenceIsRegistered 是**门 C5-a**：
// 两个分类器的**单边正文规则集**必须恰好等于登记文件里登记的集合。
//
// ★ 变异臂：
//
//	① 给 ClassifyResponseBody 加一条新规则而**不登记** ⇒ 门红
//	② 给 ClassifyErrorWithBody 加一条新规则而**不登记** ⇒ 门红
//	③ 删掉登记里的一条 ⇒ 门红
//	④ 把登记文件整个删掉 ⇒ 门红（★ fail-closed）
//	⑤ 把登记清空（留注释）⇒ 门红（★ 恒绿护栏）
func TestDualClassifierRuleSetDivergenceIsRegistered(t *testing.T) {
	a := classifierBodyRules(t, "ClassifyErrorWithBody")
	b := classifierBodyRules(t, "ClassifyResponseBody")
	registered := registeredDivergenceRules(t)

	// 恒绿护栏：提取器没坏（★ 两侧共享规则集不该是空的/个位数的）。
	// ★ 少了这条，★ 「AST 提取退化成空集」⇒ 单边集也为空 ⇒ 门会绿。
	const wantSharedMin = 20
	shared := 0
	for k := range a {
		if b[k] {
			shared++
		}
	}
	if shared < wantSharedMin {
		t.Fatalf("两侧共享的正文规则数 = %d，少于下限 %d ⇒ ★ 提取器可能已经坏了（★ 门会退化成什么都不查）",
			shared, wantSharedMin)
	}

	// 恒绿护栏：登记集**非空**（★ 空 ⇒ 门只校验「两侧规则集相同」⇒ 无牙）。
	total := 0
	for _, rules := range registered {
		total += len(rules)
	}
	if total == 0 {
		t.Fatal("登记文件里一条单边规则都没有 ⇒ 本门会退化成「两侧规则集必须相同」（★ 无牙）⇒ 必须显式登记")
	}

	for _, tc := range []struct {
		cls  string
		only map[string]bool
	}{
		{"ClassifyErrorWithBody", onlyIn(a, b)},
		{"ClassifyResponseBody", onlyIn(b, a)},
	} {
		want := registered[tc.cls]
		// 缺失（实现里有、登记里没有）⇒ 红。
		for _, rule := range sortedKeys(tc.only) {
			if _, ok := want[rule]; !ok {
				t.Errorf("%s 有单边正文规则 %q，★ 但它**没有**登记在 dual_classifier_rule_sets.txt 里\n"+
					"  ⇒ 这会让两个分类器在同一 (status, body) 上分叉而没有任何机器知道\n"+
					"  ⇒ 请二选一：★ ① 把它补到另一侧（★ 同门同判）；★ ② 在登记文件里写明「为什么单边是有意的」",
					tc.cls, rule)
			}
		}
		// 多余（登记里有、实现里没有）⇒ 红（★ 登记漂移 / 规则被删）。
		for rule, reason := range want {
			if !tc.only[rule] {
				t.Errorf("登记文件登记了 %s 的单边规则 %q（理由：%s），★ 但实现里它已经**不单边**了\n"+
					"  ⇒ 登记漂移了（★ 要么规则被补到了另一侧，★ 要么它被删了）⇒ 请同步登记文件",
					tc.cls, rule, reason)
			}
		}
	}

	// ★显式不变式：★ ClassifyErrorWithBody 侧**当前没有**单边正文规则。
	// ★ 将来给它加规则 ⇒ 必须显式改登记文件并写明理由 ⇒ 强制表态。
	if got := onlyIn(a, b); len(got) != 0 {
		t.Errorf("ClassifyErrorWithBody 出现了单边正文规则 %v\n"+
			"  ⇒ ★状态分类器比正文分类器多规则 ⇒ 两个分类器会在同一 (status, body) 上分叉\n"+
			"  ⇒ 请二选一：★ ① 同步到 ClassifyResponseBody；★ ② 在登记文件里写明为什么单边是有意的",
			sortedKeys(got))
	}
}

func onlyIn(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		if !b[k] {
			out[k] = true
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────
// 门 C5-b：真分叉必须为零
// ─────────────────────────────────────────────────────────────────────────

// TestDualClassifierHasNoRealFork 断言：★ 对 400..599 × 仓内语料，
// ★ 不存在这样的 (status, body)：★ **两个**分类器都**受正文影响**
// ★ ★⇒ ★★ 却给出**不同的 kind**。
//
// ★ 「受正文影响」的可观测判据：★ 同一个 status 下，★ 分类结果**会随正文变**。
//
//	· ClassifyErrorWithBody 受影响 ⇔ CEWB(status, body) ≠ CEWB(status, nil)
//	· ClassifyResponseBody  受影响 ⇔ CRB(status, body) ≠ "" （★ 它对空正文不表态）
//
// ★★ 为什么这个判据能容下「状态优先」：★ 状态早退/状态兜底的那一腿
// ★★ 其结果**不受正文影响**（★ CEWB(status, nil) 就是它）★★ ⇒ ★★ 于是
// ★★ 「正文分类器更具体、状态分类器按状态裁决」这种已知成因**不**算真分叉。
//
// ★★ 而「真分叉」（★ 两边都认真读了正文、★ 却给出不同答案）★ ★⇒ ★★ 门红。
func TestDualClassifierHasNoRealFork(t *testing.T) {
	bodies := lockstepBodyCorpus()
	for status := 400; status <= 599; status++ {
		wbNil := ClassifyErrorWithBody(status, nil)
		for _, body := range bodies {
			rb := ClassifyResponseBody(status, []byte(body))
			wb := ClassifyErrorWithBody(status, []byte(body))
			// 正文分类器没表态 ⇒ 不构成「两个都表态却异值」。
			if rb == "" {
				continue
			}
			// 两边同值 ⇒ 一致，★ 不是分叉。
			if rb == wb {
				continue
			}
			// 状态分类器的结果与正文无关 ⇒ 它走的是状态族/兜底 ⇒ 已知成因。
			if wb == wbNil {
				continue
			}
			t.Errorf("真分叉（两个分类器都受正文影响却给出不同 kind）：status=%d body=%q\n"+
				"  ClassifyResponseBody  = %s\n"+
				"  ClassifyErrorWithBody = %s（同一个 status 下空正文的分类是 %s ⇒ 这个结果**确实**受正文影响）\n"+
				"  ⇒违反「两个分类器不得在同一 (status, body) 上分叉」（V20 / R30）",
				status, body, rb, wb, wbNil)
		}
	}
}

// TestDualClassifierDivergenceIsQuantified 把「两族分歧」的**规模**钉成事实。
//
// ★ 为什么要有它：★ C-5 的教训是「审计档写『429 是唯一例外』⇒ ★★ 实测
// ★★ 推翻」★★ ⇒ ★★ 「规模」这种数字也必须由机器算出来而不是手写进文档。
//
// ★ 本用例断言两件事：
//
//	① 两族分歧**都存在**（★ 若哪天某族归零 ⇒ 说明口径变了 ⇒ 门红提醒更新文档）。
//	② 在请求侧状态族上，异值分歧的**两侧**都不得落节点侧罚则。
//
// ★★ 这才是这里真正要守的不变量（★ 2026-11-02 事故的放大链就在这一环）：
// ★★ 请求侧状态族（400/404/413/414/422）上，★ 两个分类器**分不分叉没关系**
// ★★（★ 分叉本身不罚节点）★★ —— ★★ 但**只要有一侧把它推进节点侧罚则**
// ★★ ⇒ ★★ 就是那条放大链（★ 绑定拉黑 + 连败累计 + 整凭据 cooling）★★。
//
// ★ 实测（2026-11-11）：★ 请求侧状态族上的异值分歧共 **2** 例 ——
// ★404 + "context length exceeded"（★ 正文侧 context_length_exceeded、
// ★ 状态侧 model_unavailable）与 414 + 同正文（★ 正文侧 context_length_exceeded、
// ★ 状态侧 request_rejected）★★ ⇒ ★★ 两侧都是请求侧/绑定级归因 ✔。
func TestDualClassifierDivergenceIsQuantified(t *testing.T) {
	bodies := lockstepBodyCorpus()
	penalty := nodePenaltyKindSet()
	if len(penalty) == 0 {
		t.Fatal("派生的凭据级罚则集合是空的 ⇒ 本门会退化成「什么都不查」⇒ 必须显式登记")
	}
	emptyFork := 0
	valueFork := 0
	valueForkOnRequestSide := 0
	for status := 400; status <= 599; status++ {
		for _, body := range bodies {
			rb := ClassifyResponseBody(status, []byte(body))
			wb := ClassifyErrorWithBody(status, []byte(body))
			switch {
			case rb == "" && wb != "":
				emptyFork++
			case rb != "" && rb != wb:
				valueFork++
				if isRequestRejectionStatus(status) {
					valueForkOnRequestSide++
					if penalty[rb] {
						t.Errorf("请求侧状态族上的异值分歧里，★ 正文分类器落了节点侧罚则：status=%d body=%q CRB=%s（★ 这就是 2026-11-02 事故的放大链）",
							status, body, rb)
					}
					if penalty[wb] {
						t.Errorf("请求侧状态族上的异值分歧里，★ 状态分类器落了节点侧罚则：status=%d body=%q CEB=%s（★ 这就是 2026-11-02 事故的放大链）",
							status, body, wb)
					}
				}
			}
		}
	}
	if emptyFork == 0 {
		t.Error("空串族分歧为 0 ⇒ ★ 两个分类器已经处处表态 ⇒ 口径变了 ⇒ 请更新审计档的分歧规模描述")
	}
	if valueFork == 0 {
		t.Error("异值族分歧为 0 ⇒ ★ 口径变了 ⇒ 请更新审计档的分歧规模描述")
	}
	fmt.Printf("双分类器分歧规模（400..599 × %d 条语料）：空串族=%d 异值族=%d（其中请求侧状态族=%d）\n",
		len(bodies), emptyFork, valueFork, valueForkOnRequestSide)
}
