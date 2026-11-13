package errorsx

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────
// 本文件是 2026-11-02 **第四轮（F-A 修）**的落地门：**双分类器锁步门**。
//
// ── 为什么要有这道门 ──────────────────────────────────────────────────
//
// 2026-11-02 事故的放大器段：上游回了一个 4xx ⇒ 分类器把它判成
// **节点/凭据级**故障 ⇒ 绑定拉黑 5 分钟 + URSM 记失败 +
// credentialhealth 降级 + nodehealth 连败累计 ⇒ **3 连败整凭据 cooling**
// ⇒ 全租户 503。
//
// 第一轮（4fe043dc3，提交 4fe043dc3）据此给**并发/过载正文规则**加了
// 状态门 `isRequestRejectionStatus`（400/404/413/414/422 ⇒ 请求侧/绑定级
// ⇒ 不落节点侧罚则）。但那一轮**只给 `ClassifyErrorWithBody` 加了门，
// 漏了 `ClassifyResponseBody`**（LESSONS §606：修了主路径、漏了旁路）。
//
// 后果：同一个 (status, body) 在**两个分类器**上分叉——
//
//	400/404/413/414/422 + 并发/过载正文
//	  ClassifyErrorWithBody  ⇒ KindRequestRejected / KindModelUnavailable
//	                           （请求侧 / 绑定级：不落节点侧罚则）
//	  ClassifyResponseBody   ⇒ KindConcurrent
//	                           （**节点侧罚则**：绑定拉黑 + 连败累计 +
//	                             凭据级熔断）
//
// ⇒ 既违反本轮「4xx 正文不得落节点侧罚则」的既定不变量，也违反仓内
// 「两个分类器不得在同一 (status, body) 上分叉」的纪律（V20 / R30 锁步）。
//
// ★ 今天**潜伏**而非在烧：`ClassifyResponseBody` 的生产调用点只有 2 处
//   （executor_chat.go:1673 / executor_anthropic.go:1266），且都只与
//   KindModelNotFound / KindModelDeprecated 比较 ⇒ 今天不触发罚则。
//   但它是**公开分类器** ⇒ 任何新调用点接上就是一条敞着的放大链。
//
// ── 这道门断言什么 ────────────────────────────────────────────────────
//
//	门 1（P，罚则单调性）：**正文分类器不得比状态+正文分类器更狠**。
//	   任意 (status, body)：若 ClassifyResponseBody 判了节点罚则 kind，
//	   则 ClassifyErrorWithBody 必须判**同一个** kind。
//	   ★ 单向而非双向：ClassifyResponseBody 是「正文分类器」——正文没命中
//	     任何规则时它返回 ""（不表态），而 ClassifyErrorWithBody 会按状态
//	     兜底（429 ⇒ rate_limit、503 ⇒ concurrent）。这是**既有设计**，
//	     不是分叉；反向（状态分类器更狠）不构成「正文把请求推进罚则」。
//
//	门 2（Q，请求侧状态族不得落节点罚则）：400/404/413/414/422 ×
//	   并发/过载正文 ⇒ **两个**分类器都不得返回 KindConcurrent /
//	   KindUpstreamOverloaded。
//
//	门 3（阴性对照）：429/500 + 同一批并发正文 ⇒ 两个分类器**仍**返回
//	   KindConcurrent / KindUpstreamOverloaded。
//	   ★ 没有这条，门 1/门 2 可以在「规则被整个删掉」的情况下照样绿 ⇒
//	     门必须有牙：规则还活着，只是不在请求侧状态族上生效。
//
//	门 4（结构性 AST 门）：两个分类器里**每一条**并发/过载正文的
//	   `Match` 调用，都必须挂在 **`!isRequestRejectionStatus(status)`**
//	   （**取负**形态）**的 if 条件**上；
//	   且两个分类器各有**恰好 2 条**这样的规则（恒绿护栏：数到的规则数
//	   变了 ⇒ 门判失败 ⇒ 任何改名/增删都必须显式改本门并写明理由）。
//   ★ 门 4 是给**未来**的：只要新增一条过载正文规则却忘了带门，
//     门 1/门 2 要靠正文语料撞上才抓得到，门 4 则结构上就抓得到。
//   ★ 门 4 判**极性**：门只做文本包含检查时，把门**反向**写成
//     `isRequestRejectionStatus(status) && ...` 也能过 ⇒ 那样请求侧
//     状态族上规则**照旧生效**、容量状态族上规则**反被关掉** ⇒ 恰好把
//     本轮要防的事做反了。变异臂 M24 就是这一条。
//
// ── 量具口径 ───────────────────────────────────────────────────────────
//
// 门 1 遍历 **400..599 全状态码**（不是只跑请求侧）⇒ 因为「分叉」是
// 全局性质：请求侧状态族上的分叉只是它的**一个实例**，先在别处分叉的
// 规则同样会漏进来。语料取仓内既有正则覆盖的形态（并发中/英、配额、
// 额度耗尽、模型不存在、模型弃用、无可用通道、无企业供应商、内容过滤…）。

// nodePenaltyKindSet 是「**落节点侧罚则**」的 kind 集合。
//
// 依据 domains/streaming/executors/executor_nodehealth.go 的
// nodeHealthErrorKind：
//
//	KindConcurrent / KindRateLimit  ⇒ nodehealth.ErrorKindRateLimit
//	KindUpstreamOverloaded          ⇒ nodehealth.ErrorKindUpstream
//
// 而 domains/nodehealth/reducer.go 的 isBindingScoped 只认
// ErrorKindModelBinding / ErrorKindModelUnavailable ⇒ 上列 kind **不是**
// 绑定级 ⇒ 走「consecutive_failures ≥ 阈值 ⇒ EffectRecordCircuitFailure
// + EffectSetCredentialUnavailable」那一条 ⇒ **整凭据熔断**，正是
// 2026-11-02 事故放大器的末环。
//
// ★ 不含 KindModelNotFound / KindModelDeprecated：它们落
//
//	ErrorKindModelBinding ⇒ 绑定级（只冷却这一对，不动账号）。
//
// ★ 不含 KindRequestRejected / KindClientBug / KindContextLength /
//
//	KindContentFilter：它们落 ErrorKindRequest ⇒ effectsFor 直接返回
//	nil（不写绑定、不记熔断、不累计连败）。
func nodePenaltyKindSet() map[ErrorKind]bool {
	out := make(map[ErrorKind]bool)
	for _, kind := range AllErrorKinds() {
		if IsNodeFaultPenalty(kind) {
			out[kind] = true
		}
	}
	return out
}

// lockstepBodyCorpus 是本门使用的正文语料。
//
// ★ 语料必须**覆盖两个分类器各自的正文规则**才有意义；尤其必须含
//
//	并发/过载的**中英两种形态**（F-A 的 10 处分歧正是这两形态）。
func lockstepBodyCorpus() []string {
	return []string{
		"",
		// 并发 / 过载（中文 + 英文）—— F-A 的直接载体
		"Too many requests. Please try again later.",
		"并发过大，达到上限，请稍后重试",
		"rate limit exceeded",
		"current concurrency too high, retry later",
		// 配额 / 额度
		"insufficient credit",
		"quota exhausted",
		"no available credits",
		// ★ 2026-11-14（suyun 轮）：new_api 余额预扣失败与单模型分组权限
		//   拒绝——两条新正文规则的**真实生产体**进语料，防止「语料缺 ⇒
		//   门绿」（第十一轮教训：规则加了但语料没有，锁步门永远看不到）。
		`{"error":{"message":"预扣费额度失败, 用户剩余额度: ¥1.474664, 需要预扣费额度: ¥2.497896 (request id: x)","type":"new_api_error","param":"","code":"insufficient_user_quota"}}`,
		`{"error":{"code":"","message":"无权访问 claude不降智稳定版 分组 (request id: x)","type":"new_api_error"}}`,
		// 模型族
		"model_not_found",
		"模型 model not found",
		"This model has been deprecated, please use another model",
		// 无后端可服务
		"No available channel",
		"No enterprise provider is available for this model",
		// 能力 / 内容 / 长度
		`{"code":"unsupported_value"}`,
		"content filter triggered",
		"context length exceeded",
		// ★ 2026-11-02（第十三轮 · F-E）：截断流信号 ——
		//   「eof without done」是**只存在于 ClassifyResponseBody** 的那条
		//   正文规则（ClassifyErrorWithBody 没有）⇒ 它是两个分类器
		//   **可能分叉**的已知来源 ⇒ 语料里没有它，门 1 就**永远看不到**
		//   那一族分叉 —— 这正是第十一轮独立复核发现的那条漏法
		//   （语料缺 ⇒ 门绿）。
		"eof without done",
		"unexpected eof",
		"stream closed before done",
		"premature close",
	}
}

// requestSideStatusFamily 是本轮「请求侧 / 绑定级」状态族
// （= isRequestRejectionStatus 的集合，在本文件里显式写出，避免
// 门与实现互相引用而一起漂移；下方有「与实现同源」的一致性断言）。
func requestSideStatusFamily() []int {
	return []int{400, 404, 413, 414, 422}
}

// ── 门 1：罚则单调性 ─────────────────────────────────────────────────

// TestClassifyDualClassifier_PenaltyMonotonicity 断言「正文分类器不得比
// 状态+正文分类器更狠」。
//
// ★ 这一条**泛化**了 F-A：F-A 只是「请求侧状态族 × 并发正文」这一个实例；
//
//	本门在 400..599 全状态码 × 全语料上断言同一性质 ⇒ 任何**未来**的
//	分叉（新增规则、漏门、改门）只要方向是「正文分类器更狠」就会被抓住。
func TestClassifyDualClassifier_PenaltyMonotonicity(t *testing.T) {
	bodies := lockstepBodyCorpus()
	for status := 400; status <= 599; status++ {
		for _, body := range bodies {
			rb := ClassifyResponseBody(status, []byte(body))
			wb := ClassifyErrorWithBody(status, []byte(body))
			// ★ 判据是**严重度序**（请求侧 < 绑定级 < 凭据级），不是「相等」。
			//   理由：两个分类器**结构上**就不同 —— ClassifyErrorWithBody 有
			//   状态兜底（400/404/… 各有裁决），ClassifyResponseBody 没有
			//   （不命中就返回 ""）⇒ 「相等」不是它们之间可成立的不变量。
			//   真正的不变量是**正文分类器不得更狠**。
			if SeverityOf(rb) > SeverityOf(wb) {
				t.Errorf("双分类器分叉（正文分类器更狠）：status=%d body=%q\n  ClassifyResponseBody  = %s（严重度 %d）\n  ClassifyErrorWithBody = %s（严重度 %d）\n  ★ 违反：正文分类器不得比状态+正文分类器更狠（罚则单调性）",
					status, body, rb, SeverityOf(rb), wb, SeverityOf(wb))
			}
		}
	}
}

// ── 门 2：请求侧状态族不得落节点罚则 ─────────────────────────────────

// TestClassifyDualClassifier_RequestSideStatusNeverPenalizes 断言：
// 请求侧/绑定级状态族（400/404/413/414/422）上，**两个**分类器都不得
// 把并发/过载正文判成节点侧罚则。
//
// ★ 断言的是**两个**分类器，不是一个 ⇒ 这正是 F-A 漏掉的那一半。
func TestClassifyDualClassifier_RequestSideStatusNeverPenalizes(t *testing.T) {
	penalty := nodePenaltyKindSet()
	// ★ 恒绿护栏（第十三轮）：派生的「落凭据级罚则」集合**不得为空**
	//   —— 否则「把判据写坏 ⇒ 集合为空 ⇒ 本门什么都不查」就能蒙混过关。
	//   （这正是第七轮那版手写 3 项清单的老毛病：清单一改小，门就变弱。）
	if len(penalty) == 0 {
		t.Fatal("派生的凭据级罚则集合是空的 ⇒ 本门会退化成「什么都不查」⇒ 必须显式登记")
	}
	overloadBodies := []string{
		"Too many requests. Please try again later.",
		"并发过大，达到上限，请稍后重试",
	}
	for _, status := range requestSideStatusFamily() {
		for _, body := range overloadBodies {
			rb := ClassifyResponseBody(status, []byte(body))
			if penalty[rb] {
				t.Errorf("请求侧状态族落了节点侧罚则：status=%d body=%q ClassifyResponseBody=%s（应非节点罚则）",
					status, body, rb)
			}
			wb := ClassifyErrorWithBody(status, []byte(body))
			if penalty[wb] {
				t.Errorf("请求侧状态族落了节点侧罚则：status=%d body=%q ClassifyErrorWithBody=%s（应非节点罚则）",
					status, body, wb)
			}
		}
	}
}

// TestRequestSideStatusFamilyMatchesImplementation 断言本文件写死的
// 状态族与实现里的 `isRequestRejectionStatus` **同源**。
//
// ★ 没有这条，本文件的状态族会随实现漂移而无人察觉（门自己写错了也绿）。
func TestRequestSideStatusFamilyMatchesImplementation(t *testing.T) {
	want := requestSideStatusFamily()
	for _, st := range want {
		if !isRequestRejectionStatus(st) {
			t.Errorf("状态族漂移：%d 在本文件的请求侧状态族里，但实现 isRequestRejectionStatus 说它不是", st)
		}
	}
	// 反向：实现里在集合中的状态码，必须都出现在本文件的家族里。
	for st := 400; st <= 499; st++ {
		if isRequestRejectionStatus(st) && !containsInt(want, st) {
			t.Errorf("状态族漂移：实现把 %d 归入请求侧状态族，本文件的门禁没覆盖它", st)
		}
	}
}

// ── 门 3：阴性对照（规则还活着）─────────────────────────────────────

// TestClassifyDualClassifier_OverloadRuleAliveOnCapacityStatuses 是
// **阴性对照**：429/500/502 上，同一批并发正文**仍**必须被判成
// KindConcurrent / KindUpstreamOverloaded。
//
// ★ 没有这条，门 1/门 2 可以在「并发/过载规则被整个删掉」的情况下照样
//
//	绿 ⇒ 门必须有牙：规则还活着，只是**不在请求侧状态族上**生效。
func TestClassifyDualClassifier_OverloadRuleAliveOnCapacityStatuses(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorKind
	}{
		{429, KindConcurrent},         // 容量信号：并发
		{500, KindUpstreamOverloaded}, // 中转自报过载
		{502, KindUpstreamOverloaded}, // 中转自报过载
		{503, KindConcurrent},         // 容量信号：并发
	}
	bodies := []string{
		"Too many requests. Please try again later.",
		"并发过大，达到上限，请稍后重试",
	}
	for _, c := range cases {
		for _, body := range bodies {
			if got := ClassifyErrorWithBody(c.status, []byte(body)); got != c.want {
				t.Errorf("阴性对照失败（状态+正文分类器）：status=%d body=%q ⇒ %s，期望 %s（规则必须还活着）",
					c.status, body, got, c.want)
			}
			if got := ClassifyResponseBody(c.status, []byte(body)); got != c.want {
				t.Errorf("阴性对照失败（正文分类器）：status=%d body=%q ⇒ %s，期望 %s（规则必须还活着）",
					c.status, body, got, c.want)
			}
		}
	}
}

// ── 门 4：结构性 AST 门 ─────────────────────────────────────────────

// overloadMatchCall 是一个并发/过载正文的 Match 调用。
type overloadMatchCall struct {
	from, to token.Pos
	pos      token.Position
	rendered string
}

// analyzeOverloadGating 统计某函数体内并发/过载正文规则的「带门」情况。
//
// 返回：total（规则总数）、gated（挂在带 isRequestRejectionStatus 门的
// if 条件上的条数）、ungated（没带门的条目描述）。
func analyzeOverloadGating(fset *token.FileSet, fn *ast.FuncDecl) (total, gated int, ungated []string) {
	// 1) 找出所有并发/过载 Match 调用。
	var calls []overloadMatchCall
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Match" {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name != "concurrentOverloadRe" && id.Name != "concurrentOverloadCJKRe" {
			return true
		}
		calls = append(calls, overloadMatchCall{
			from:     call.Pos(),
			to:       call.End(),
			pos:      fset.Position(call.Pos()),
			rendered: renderNode(fset, call),
		})
		return true
	})

	// 2) 找出所有「条件里含并发/过载规则」的 if 语句及其是否带门。
	type condGuard struct {
		from, to token.Pos
		gated    bool
	}
	var guards []condGuard
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		condText := renderNode(fset, ifStmt.Cond)
		if !strings.Contains(condText, "concurrentOverload") {
			return true
		}
		guards = append(guards, condGuard{
			from:  ifStmt.Cond.Pos(),
			to:    ifStmt.Cond.End(),
			gated: condHasNegatedRequestRejectionGate(fset, ifStmt.Cond),
		})
		return true
	})

	// 3) 逐条调用归类。
	for _, c := range calls {
		total++
		covered := false
		gatedHere := false
		for _, g := range guards {
			if g.from <= c.from && c.to <= g.to {
				covered = true
				if g.gated {
					gatedHere = true
				}
			}
		}
		switch {
		case gatedHere:
			gated++
		case covered:
			ungated = append(ungated, fmt.Sprintf("%s: %s（挂在**没带** isRequestRejectionStatus 门的 if 条件上）", c.pos, c.rendered))
		default:
			ungated = append(ungated, fmt.Sprintf("%s: %s（不在任何 if 条件里 ⇒ 无门）", c.pos, c.rendered))
		}
	}
	return total, gated, ungated
}

// TestClassifyDualClassifier_OverloadRulesAreGated 是**结构性**门：
// 两个分类器里每一条并发/过载正文规则都必须挂在带
// `isRequestRejectionStatus` 门的 if 条件上。
//
// ★ 门 1/门 2 靠**语料**撞，分叉只有语料撞上了才抓得到；本门在**结构**上
//
//	抓 ⇒ 未来新增一条过载正文规则却忘了带门，本门立刻红。
func TestClassifyDualClassifier_OverloadRulesAreGated(t *testing.T) {
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

	// 恒绿护栏：每个分类器**恰好** 2 条并发/过载规则。
	// ★ 数到的条数变了 ⇒ 门判失败 ⇒ 改名/增删必须显式改本门并写明理由
	//   （与 errorsx/classify_4xx_matrix_test.go 同一口径）。
	const wantRules = 2

	for _, fnName := range []string{"ClassifyErrorWithBody", "ClassifyResponseBody"} {
		fn := findFuncDecl(f, fnName)
		if fn == nil {
			t.Fatalf("在 %s 里找不到函数 %s", src, fnName)
		}
		total, gated, ungated := analyzeOverloadGating(fset, fn)
		if total != wantRules {
			t.Errorf("%s 的并发/过载正文规则数 = %d，期望 %d。★ 规则集合变了 ⇒ 请同步修改本门禁并在审计档写明理由", fnName, total, wantRules)
		}
		if len(ungated) > 0 {
			t.Errorf("%s 有**没带**请求侧状态门的并发/过载正文规则（这会让同一 (status, body) 在两个分类器上分叉）：\n  %s",
				fnName, strings.Join(ungated, "\n  "))
		}
		if gated != total {
			t.Errorf("%s 带门的并发/过载规则数 = %d，总数 = %d ⇒ 有规则没被识别为带门", fnName, gated, total)
		}
	}
}

// ── 小工具 ───────────────────────────────────────────────────────────

// condHasNegatedRequestRejectionGate 判断 cond 里是否存在
// **`!isRequestRejectionStatus(status)`** 形态的门。
//
// ★ 为什么必须看**极性**而不只是「条件里出现过这个函数」：
//
//	门只做文本包含检查时，把门**反向**写成
//	`isRequestRejectionStatus(status) && ...` 也能过 ⇒
//	那样请求侧状态族上规则**照旧生效**、容量状态族上规则**反被关掉** ⇒
//	恰好把本轮要防的事做反了。变异臂 M24 就是这一条。
//
// ★ 为什么还要核对**实参**是 `status`：门判的是「这个响应的状态码」，
//
//	写成别的变量就门不到点上。
func condHasNegatedRequestRejectionGate(fset *token.FileSet, cond ast.Expr) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		un, ok := n.(*ast.UnaryExpr)
		if !ok || un.Op != token.NOT {
			return true
		}
		call, ok := un.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		fun, ok := call.Fun.(*ast.Ident)
		if !ok || fun.Name != "isRequestRejectionStatus" {
			return true
		}
		if len(call.Args) != 1 {
			return true
		}
		arg, ok := call.Args[0].(*ast.Ident)
		if !ok || arg.Name != "status" {
			return true
		}
		found = true
		return false
	})
	return found
}

func findFuncDecl(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func renderNode(fset *token.FileSet, n ast.Node) string {
	var sb strings.Builder
	//nolint:errcheck // 渲染失败时返回空串即可（门会因"缺门"而红，不会误绿）
	_ = printer.Fprint(&sb, fset, n)
	return sb.String()
}

// ── 门 6：截断流规则的状态门（第十三轮）───────────────────────────────

// TestClassifyDualClassifier_TruncationRuleIsStatusGated 钉门 6：
// 「eof without done」（上游流被截断）这条正文规则**只存在于
// ClassifyResponseBody** ⇒ 它是「两分类器可能分叉」的**唯一已知来源**
// ⇒ 必须有一道门钉住它的状态门。
//
// ★ 两条断言缺一不可：
// ① 请求侧 / 绑定级状态族上，两个分类器都**不得**落凭据级罚则
// （否则正文分类器比状态分类器更狠 ⇒ 违反 V20 / R30）；
// ② 5xx 上截断信号**必须仍然生效** —— 否则这道门会把「把这条规则
// 整个关掉」也判成合格（那是把工具变成另一种缺陷）。
func TestClassifyDualClassifier_TruncationRuleIsStatusGated(t *testing.T) {
	truncationBodies := []string{
		"eof without done",
		"unexpected eof",
		"stream closed before done",
		"premature close",
	}
	// ★ 语料护栏（第十三轮）：主语料里**必须**含截断流正文 ——
	//   否则门 1（严重度序）永远看不到这一族分叉，而门 6 用的是自己的
	//   语料 ⇒ 两道门都绿 ⇒ 这条漏法复发（第十一轮复核发现的那一条）。
	corpusHasTruncation := false
	for _, body := range lockstepBodyCorpus() {
		for _, trunc := range truncationBodies {
			if body == trunc {
				corpusHasTruncation = true
			}
		}
	}
	if !corpusHasTruncation {
		t.Error("主语料里没有截断流正文 ⇒ 门 1 看不到这一族分叉 ⇒ 必须把 eof 形态加进 lockstepBodyCorpus")
	}

	for _, status := range requestSideStatusFamily() {
		for _, body := range truncationBodies {
			rb := ClassifyResponseBody(status, []byte(body))
			if SeverityOf(rb) == SeverityCredential {
				t.Errorf("请求侧/绑定级状态族落了凭据级罚则：status=%d body=%q ClassifyResponseBody=%s（%s）",
					status, body, rb, rb)
			}
			wb := ClassifyErrorWithBody(status, []byte(body))
			if SeverityOf(wb) == SeverityCredential {
				t.Errorf("请求侧/绑定级状态族落了凭据级罚则：status=%d body=%q ClassifyErrorWithBody=%s",
					status, body, wb)
			}
		}
	}
	for _, status := range []int{500, 502, 503} {
		body := "eof without done"
		if got := ClassifyResponseBody(status, []byte(body)); got != KindStreamTimeout {
			t.Errorf("5xx 上截断信号失效：status=%d body=%q ClassifyResponseBody=%s, want %s",
				status, body, got, KindStreamTimeout)
		}
	}
}
