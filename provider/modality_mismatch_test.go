package provider

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestModalityAccepts_PinsTheAcceptanceTable pins the acceptance rule the
// router's modality gate and the error diagnosis both rely on.
//
// ★ Why this table must be pinned exactly, and why it lives in the provider
// package (not next to the caller):
//
//	provider/candidate_query_sql_base admits a request when
//	    $3 = ''                       → everything
//	    $3 = 'text'                   → everything EXCEPT modality='embedding'
//	    $3 = 'multimodal'             → modality = 'multimodal'
//	    $3 = 'image'                  → modality IN ('image','vision','multimodal')
//	    $3 IN ('vision','audio','video') → modality IN ($3,'multimodal')
//
//	provider/modality_mismatch.go's ModalityAccepts must agree with it,
//	because the diagnosis uses it to decide whether to tell the client
//	"this model cannot read your images". If it disagrees with the router,
//	the error message blames a cause the router never had — strictly worse
//	than the old generic 503, because it sends the user to fix the wrong
//	thing.
//
//	The mutation arm for this table: making ModalityAccepts always return
//	true must fail domains/streaming's cross-check test
//	(TestModalityAccepts_MatchesPublishedTable).
func TestModalityAccepts_PinsTheAcceptanceTable(t *testing.T) {
	cases := []struct {
		registered string
		requested  string
		want       bool
	}{
		// Text requests are served by everything.
		{"text", "text", true},
		{"multimodal", "text", true},
		{"vision", "text", true},
		{"", "text", true},

		// ★ 2026-11-02（211 生产事故 · embedding 投毒链）：文本请求**不接**
		// 登记为 embedding 的模型。
		//
		// 事故链：外部客户端把 embedding 模型名塞进 POST /v1/chat/completions
		// ⇒ 路由器把它选成候选 ⇒ 网关按 chat 形态转发 ⇒ 上游 400
		// （"does not support chat"）×3 ⇒ 绑定被翻红（avail=false）
		// ⇒ /v1/embeddings 对**所有租户** 503。
		//
		// ⇒ 文本请求必须在候选解析层就与 embedding 登记模型绝缘。
		{"embedding", "text", false},

		// An empty request modality means "no filter" — the router admits
		// everything, so the diagnosis must not call it a mismatch.
		{"text", "", true},
		{"multimodal", "", true},
		{"vision", "", true},

		// Media requests need a matching or multimodal registration.
		{"text", "vision", false},
		{"text", "audio", false},
		{"text", "video", false},
		{"vision", "vision", true},
		{"vision", "audio", false},
		{"vision", "video", false},
		{"audio", "audio", true},
		{"audio", "vision", false},
		{"video", "video", true},
		{"video", "vision", false},
		{"multimodal", "vision", true},
		{"multimodal", "audio", true},
		{"multimodal", "video", true},

		// The image endpoint's modality has its own arm.
		{"image", "image", true},
		{"vision", "image", true},
		{"multimodal", "image", true},
		{"text", "image", false},
		{"audio", "image", false},

		// A multimodal request needs a multimodal registration specifically.
		{"multimodal", "multimodal", true},
		{"vision", "multimodal", false},
		{"text", "multimodal", false},

		// Case and whitespace are normalised before comparison.
		{"TEXT", "VISION", false},
		{" text ", " text ", true},
		{"Multimodal", "VISION", true},
	}

	for _, tc := range cases {
		if got := ModalityAccepts(tc.registered, tc.requested); got != tc.want {
			t.Errorf("ModalityAccepts(%q, %q) = %v, want %v",
				tc.registered, tc.requested, got, tc.want)
		}
	}
}

// TestModalityMismatch_IsZeroSemantics documents the nil/empty contract the
// handler branches on.
//
// A nil mismatch and an empty mismatch both mean "not a modality problem".
// The handler calls IsZero() on a value that may be nil (the resolver
// returns nil when the model simply has no live binding), so IsZero must be
// nil-safe.
func TestModalityMismatch_IsZeroSemantics(t *testing.T) {
	var nilMismatch *ModalityMismatch
	if !nilMismatch.IsZero() {
		t.Error("nil mismatch must report IsZero() = true")
	}
	if !(&ModalityMismatch{}).IsZero() {
		t.Error("a mismatch with no requested modality must report IsZero() = true")
	}
	if (&ModalityMismatch{RequestedModality: "vision"}).IsZero() {
		t.Error("a mismatch with a requested modality must report IsZero() = false")
	}
}

// TestModalityMismatchSQL_Shape guards the diagnosis query's shape.
//
// ★ The query answers two things the error message depends on: how the
// model is registered, and whether it is routable at all when the modality
// filter is dropped. The second is the load-bearing part — without it the
// diagnosis would fire for models that are simply unavailable, and the client
// would be told to change a request that was never the problem.
//
// The assertions are on STRUCTURE (which tables and predicates appear), not
// on literal text: a reformat must not fail the test, but dropping the
// is_routable gate must.
func TestModalityMismatchSQL_Shape(t *testing.T) {
	sql := modalityMismatchSQL

	required := []string{
		"models_canonical",             // where the registered modality lives
		"mc.modality",                  // the registration itself
		"v_routable_credential_models", // the router's own availability gate
		"is_routable",                  // ...and its predicate
	}
	for _, needle := range required {
		if !strings.Contains(sql, needle) {
			t.Errorf("diagnosis SQL is missing %q — the diagnosis and the router "+
				"must read the same facts", needle)
		}
	}

	// The query must NOT re-apply a modality filter: its whole purpose is to
	// ask "would this model be routable if the modality gate were removed?".
	if strings.Contains(sql, "mc.modality IN") {
		t.Error("diagnosis SQL must not filter by modality — that is the gate it is diagnosing")
	}
}

// TestModalityMismatchDecision_Table pins the "was the zero-candidate exit
// caused by the modality?" decision, cell by cell.
//
// ★ Why this table must be pinned exactly, and why it exists at all
// (2026-11-02 第十四轮 · F-16):
//
// 此前这条判决长在 ModelModalityMismatch 里（查库 + 判决混在一个函数里）
// ⇒ 判决表只能靠真实数据库验（provider/modality_mismatch_realdb_test.go，
// 没有 TEST_DATABASE_URL 时整条跳过）⇒ **本地永远验不到**。
//
// ★ 更糟的是当时还留着一条**前提为假**的门
// （TestModelModalityMismatch_ShortCircuitsOnText，本轮已删除）：
// 它的注释断言「文本请求从不被模态门排除」——而 2026-11-02（211 生产事故
// · embedding 投毒链）之后恰恰相反：文本臂**排除**登记为 embedding 的模型。
// 而且它并没有真的测早退（用的是 dbPool == nil 的 client，走的是另一道
// 早退 `!c.Enabled() || c.dbPool == nil`）⇒ 「text 早退」这件事从头到尾
// 没被验过，却有一条绿门站在那儿 ⇒ 它会**引导**后来者去实现那个早退
// （那会让 chat + embedding ⇒ 没有原因的 503 ⇒ 事故链复活），
// 而且实现之后**它仍然是绿的**。
//
// ⇒ 判决表必须有一道**不需要数据库**的门把每一格钉死；下面这张表就是它。
//
// 表的形状与 modalityMismatchDecision 的注释一一对应：
// 请求模态为空 ⇒ 无诊断（没有模态概念，没有东西可诊断）
// 忽略模态过滤后不可路由 ⇒ 无诊断（压根没绑定 ⇒ 是「暂时没东西可路由」）
// 判据说它能服务这个模态 ⇒ 无诊断（被别的原因排除：探针判决、租户范围…）
// 否则 ⇒ 模态不匹配（登记模态服务不了本次请求）
func TestModalityMismatchDecision_Table(t *testing.T) {
	const canonical = "qwen3-embedding:0.6b"
	cases := []struct {
		name string
		// registered is what models_canonical.modality says.
		registered string
		// requested is the request modality the handler detected.
		requested string
		// routableIgnoringModality is the DB's "is there a live binding at
		// all, ignoring the modality filter" answer.
		routableIgnoringModality bool
		wantMismatch             bool
	}{
		{
			// ★★ 2026-11-02 生产事故链的那一格：chat 端点收到一个登记为
			// embedding 的模型名 ⇒ 模态门把它排除 ⇒ 零候选 ⇒ 必须给出
			// 400 modality_mismatch（而不是没有原因的 503）。
			name:                     "文本请求打登记为 embedding 的模型 ⇒ 有诊断",
			registered:               "embedding",
			requested:                "text",
			routableIgnoringModality: true,
			wantMismatch:             true,
		},
		{
			name:                     "文本请求打登记为 text 的模型 ⇒ 无诊断",
			registered:               "text",
			requested:                "text",
			routableIgnoringModality: true,
			wantMismatch:             false,
		},
		{
			name:                     "文本请求打登记为 vision 的模型 ⇒ 无诊断（文本臂只排除 embedding）",
			registered:               "vision",
			requested:                "text",
			routableIgnoringModality: true,
			wantMismatch:             false,
		},
		{
			// 「压根没有绑定」是「暂时没东西可路由」⇒ 客户端重试是对的 ⇒ 无诊断。
			name:                     "登记为 embedding 但压根不可路由 ⇒ 无诊断",
			registered:               "embedding",
			requested:                "text",
			routableIgnoringModality: false,
			wantMismatch:             false,
		},
		{
			name:                     "请求模态为空 ⇒ 无诊断",
			registered:               "embedding",
			requested:                "",
			routableIgnoringModality: true,
			wantMismatch:             false,
		},
		{
			name:                     "embedding 请求打登记为 embedding 的模型 ⇒ 无诊断（精确相等兜底臂）",
			registered:               "embedding",
			requested:                "embedding",
			routableIgnoringModality: true,
			wantMismatch:             false,
		},
		{
			name:                     "vision 请求打登记为 text 的模型 ⇒ 有诊断",
			registered:               "text",
			requested:                "vision",
			routableIgnoringModality: true,
			wantMismatch:             true,
		},
		{
			name:                     "vision 请求打登记为 multimodal 的模型 ⇒ 无诊断",
			registered:               "multimodal",
			requested:                "vision",
			routableIgnoringModality: true,
			wantMismatch:             false,
		},
		{
			// 未知请求模态 ⇒ 精确相等（与 SQL 兜底臂同形）：登记 text 的模型
			// 服务不了 "document" ⇒ 有诊断。
			name:                     "未知请求模态 ⇒ 精确相等",
			registered:               "text",
			requested:                "document",
			routableIgnoringModality: true,
			wantMismatch:             true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := modalityMismatchDecision(tc.registered, tc.requested, canonical, tc.routableIgnoringModality)
			if tc.wantMismatch {
				if got.IsZero() {
					t.Fatalf("判决 = 无诊断, want 模态不匹配 —— 客户端会拿到一个没有原因的 503，然后无限重试")
				}
				if got.RequestedModality != tc.requested {
					t.Errorf("RequestedModality = %q, want %q", got.RequestedModality, tc.requested)
				}
				if got.RegisteredModality != tc.registered {
					t.Errorf("RegisteredModality = %q, want %q", got.RegisteredModality, tc.registered)
				}
				if got.CanonicalModel != canonical {
					t.Errorf("CanonicalModel = %q, want %q", got.CanonicalModel, canonical)
				}
				return
			}
			if !got.IsZero() {
				t.Fatalf("判决 = %+v, want 无诊断", got)
			}
		})
	}
}

// TestModalityMismatchDecision_NormalisesRequestedModality pins the token
// the client sees: "TEXT" / " text " must be reported as "text", and the
// decision must not depend on the spelling.
//
// ★ 客户端按 error.code / error.modality_mismatch.requested_modality 分档，
// 而这两处要拿到的是**归一后的令牌**（"TEXT" 与 "text" 必须是同一个意思）。
func TestModalityMismatchDecision_NormalisesRequestedModality(t *testing.T) {
	for _, requested := range []string{"TEXT", " text ", "Text"} {
		got := modalityMismatchDecision("embedding", requested, "m", true)
		if got.IsZero() {
			t.Fatalf("请求模态 %q: 判决 = 无诊断, want 模态不匹配", requested)
		}
		if got.RequestedModality != "text" {
			t.Errorf("请求模态 %q ⇒ RequestedModality = %q, want %q", requested, got.RequestedModality, "text")
		}
	}
}

// TestModelModalityMismatch_DisabledClientReturnsNil pins the early return
// that actually exists: a client without a database pool answers nothing.
//
// ★ 这条门**曾经**叫 TestModelModalityMismatch_ShortCircuitsOnText，
// 注释说「文本请求从不被模态门排除，所以诊断连数据库都不该进」。
// ★★ 那个前提在 2026-11-02（211 生产事故 · embedding 投毒链）之后是**假的**
// （文本臂恰恰排除登记为 embedding 的模型），而且那条门**没有真的测早退**
// ——它用的 `&Client{}`（dbPool == nil）走的是 `!c.Enabled() || c.dbPool == nil`
// 这道**另一个**早退 ⇒ 「text 早退」从头到尾没被验过，只是名字与注释在讲它。
// ⇒ 本轮把门名改成它**真正**在钉的东西（未启用的 client 一律不诊断），
// 并把判决表交给 TestModalityMismatchDecision_Table 逐格钉死。
func TestModelModalityMismatch_DisabledClientReturnsNil(t *testing.T) {
	c := &Client{} // dbPool == nil: any query attempt would panic/fail
	ctx := context.Background()

	for _, mod := range []string{"", "text", "TEXT", " text ", "vision", "embedding"} {
		got, err := c.ModelModalityMismatch(ctx, "glm-5.3-flash", "", "default", mod)
		if err != nil {
			t.Errorf("modality %q: unexpected error %v", mod, err)
		}
		if !got.IsZero() {
			t.Errorf("modality %q: an unusable client must not diagnose anything, got %+v", mod, got)
		}
	}
}

// TestModalityDiagnosisQueryNeeded pins the ONE legitimate early return of
// the diagnosis: "this request carries no modality concept".
//
// ★ The cell that matters most is "text ⇒ needs the query". Text requests
// ARE excluded by the modality gate — the gate excludes models registered as
// 'embedding' (ModalityAccepts's text arm, 2026-11-02 · 211 production
// incident · embedding poisoning chain) ⇒ a chat request that names an
// embedding model produces a zero-candidate exit that IS a modality
// problem ⇒ skipping the query on text turns it back into a reasonless 503
// and the client retries forever.
//
// ★ This gate exists because the previous one (deleted this round) asserted
// the OPPOSITE premise in its comment while actually testing nothing about
// early returns — a gate whose stated premise is false is worse than no gate:
// it invites somebody to implement the early return, and stays green when
// they do.
func TestModalityDiagnosisQueryNeeded(t *testing.T) {
	cases := []struct {
		modality string
		want     bool
	}{
		{modality: "", want: false},
		{modality: "   ", want: false},
		{modality: "text", want: true},
		{modality: "TEXT", want: true},
		{modality: " text ", want: true},
		{modality: "vision", want: true},
		{modality: "audio", want: true},
		{modality: "video", want: true},
		{modality: "image", want: true},
		{modality: "multimodal", want: true},
		{modality: "embedding", want: true},
	}
	for _, tc := range cases {
		got := modalityDiagnosisQueryNeeded(tc.modality)
		if got != tc.want {
			t.Errorf("modalityDiagnosisQueryNeeded(%q) = %v, want %v", tc.modality, got, tc.want)
		}
	}
}

// TestModelModalityMismatch_HasNoModalitySpecificBranch is the structural
// gate: the query function must not contain a verdict of its own.
//
// ★ Why structure and not behaviour: the verdict lives in
// modalityMismatchDecision and the cost decision in
// modalityDiagnosisQueryNeeded — two pure functions, both unit-testable
// without a database. The query function's only job is to fetch three
// facts and hand them over.
//
// ★ A modality literal inside ModelModalityMismatch means somebody decided
// something there ("if the request is text, skip it" — the exact shape of
// the 2026-11-02 stale premise). Such a branch would be invisible to the
// decision-table gate (which calls the pure function) AND to the
// early-return gate (which calls the other pure function) ⇒ without this
// structural gate, a verdict smuggled into the query function would keep
// the whole suite green.
//
// ★ The gate also asserts the two calls are present, and that it really
// looked at a function body (a gate that finds nothing must not pass
// silently).
func TestModelModalityMismatch_HasNoModalitySpecificBranch(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "modality_mismatch.go", nil, 0)
	if err != nil {
		t.Fatalf("parse modality_mismatch.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Name.Name != "ModelModalityMismatch" || d.Recv == nil {
			continue
		}
		fn = d
	}
	if fn == nil {
		t.Fatal("modality_mismatch.go 里找不到 (*Client).ModelModalityMismatch —— 这道门必须真的看到那个函数体（否则它会静默通过）")
	}

	// Modality tokens that must never be compared inside the query function.
	modalityTokens := map[string]bool{
		"text": true, "vision": true, "audio": true, "video": true,
		"image": true, "multimodal": true, "embedding": true,
	}

	called := map[string]bool{}
	visited := 0
	var found string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		visited++
		switch node := n.(type) {
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				called[sel.Sel.Name] = true
			} else if ident, ok := node.Fun.(*ast.Ident); ok {
				called[ident.Name] = true
			}
		case *ast.BasicLit:
			if node.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(node.Value)
			if err != nil {
				return true
			}
			if modalityTokens[strings.ToLower(val)] {
				found = val
				return false
			}
		}
		return true
	})

	if visited == 0 {
		t.Fatal("这道门没有遍历到函数体的任何节点 ⇒ 它什么也没检查（恒绿护栏）")
	}
	if found != "" {
		t.Fatalf("ModelModalityMismatch 的函数体里出现了模态字面量 %q ⇒ 判决被写进了查询函数 ⇒ "+
			"「要不要查库」（modalityDiagnosisQueryNeeded）与「是不是模态问题」（modalityMismatchDecision）"+
			"两条线又混在一起了；而这种分支对两道行为门都是隐形的", found)
	}
	for _, want := range []string{"modalityDiagnosisQueryNeeded", "modalityMismatchDecision"} {
		if !called[want] {
			t.Errorf("ModelModalityMismatch 没有调用 %s —— 判决/成本两条线必须各走各的纯函数入口", want)
		}
	}
}
