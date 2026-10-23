package streaming

// 2026-10-23 审计（探测打标）—— 成功终态 entry 必须携带请求上下文派生字段。
//
// 背景与生产取证见 request_log_pipeline.go 里 applyRequestContextFields 的
// 注释（154 实测：session_turns_hot 里 origin_stage 非空的行 100% 是
// success=false，为空的行 100% 是 success=true ⇒ 成功的探测请求在会话族里
// 不带任何探测标记 ⇒ 实时拓扑把它当业务流量）。
//
// 本文件是这条缺陷的回归门，分三层：
//
//	L1 行为门：applyRequestContextFields 确实把字段补到 entry 上；
//	L2 行为门（端到端）：走真实成功终态路径 emitTelemetry，产出的 entry
//	    带 origin_stage=node_probe / client_ip（= 会话族镜像真正看到的形状）；
//	L3 结构门（AST）：会话族镜像的**每一个**输入点都必须（传递地）经过
//	    applyOriginMetadata 与 applyAutoRouteFields —— 新增镜像输入点却忘了
//	    补字段 ⇒ 门变红。

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ─────────────────────────────────────────────────────────────────────────────
// L1 行为门
// ─────────────────────────────────────────────────────────────────────────────

// originCtxRequest 造一个带 OriginMiddleware ctx 值的请求。
// key 是 middleware/origin_mw.go 里定义的字符串常量（origin.stage 等）。
func originCtxRequest(t *testing.T) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctx := context.WithValue(r.Context(), "origin.stage", "node_probe")
	ctx = context.WithValue(ctx, "origin.actor", "probe-service")
	ctx = context.WithValue(ctx, "origin.client_ip", "127.0.0.1")
	ctx = context.WithValue(ctx, "origin.xff", "127.0.0.1")
	return r.WithContext(ctx)
}

func TestApplyRequestContextFieldsFillsOriginFromRequestContext(t *testing.T) {
	c := &RequestLogContext{Request: originCtxRequest(t)}
	entry := &telemetry.RequestLogEntry{RequestID: "req-origin"}

	applyRequestContextFields(entry, nil, c)

	if entry.OriginStage == nil || *entry.OriginStage != "node_probe" {
		t.Fatalf("OriginStage = %v, want node_probe", entry.OriginStage)
	}
	if entry.OriginActor == nil || *entry.OriginActor != "probe-service" {
		t.Fatalf("OriginActor = %v, want probe-service", entry.OriginActor)
	}
	if entry.ClientIP == nil || *entry.ClientIP != "127.0.0.1" {
		t.Fatalf("ClientIP = %v, want 127.0.0.1", entry.ClientIP)
	}
	if entry.ClientForwardedFor == nil || *entry.ClientForwardedFor != "127.0.0.1" {
		t.Fatalf("ClientForwardedFor = %v, want 127.0.0.1", entry.ClientForwardedFor)
	}
}

func TestApplyRequestContextFieldsFillsAutoRouteFields(t *testing.T) {
	c := &RequestLogContext{
		Request:        originCtxRequest(t),
		IsAutoRequest:  true,
		TaskType:       "probe_triggered",
		WorkType:       "chat",
		AutoConfidence: 0.8,
	}
	entry := &telemetry.RequestLogEntry{RequestID: "req-auto"}

	applyRequestContextFields(entry, nil, c)

	if entry.IsAutoRequest == nil || !*entry.IsAutoRequest {
		t.Fatalf("IsAutoRequest = %v, want true", entry.IsAutoRequest)
	}
	if entry.TaskType == nil || *entry.TaskType != "probe_triggered" {
		t.Fatalf("TaskType = %v, want probe_triggered", entry.TaskType)
	}
	if entry.WorkType == nil || *entry.WorkType != "chat" {
		t.Fatalf("WorkType = %v, want chat", entry.WorkType)
	}
	if entry.AutoConfidence == nil || *entry.AutoConfidence != 0.8 {
		t.Fatalf("AutoConfidence = %v, want 0.8", entry.AutoConfidence)
	}
}

func TestApplyRequestContextFieldsFillsEndpointAndClientRequestID(t *testing.T) {
	c := &RequestLogContext{
		Request:         originCtxRequest(t),
		ClientEndpoint:  "/v1/chat/completions",
		ClientRequestID: "client-req-1",
	}
	entry := &telemetry.RequestLogEntry{RequestID: "req-endpoint"}

	applyRequestContextFields(entry, nil, c)

	if entry.ClientEndpoint == nil || *entry.ClientEndpoint != "/v1/chat/completions" {
		t.Fatalf("ClientEndpoint = %v, want /v1/chat/completions", entry.ClientEndpoint)
	}
	if entry.ClientRequestID == nil || *entry.ClientRequestID != "client-req-1" {
		t.Fatalf("ClientRequestID = %v, want client-req-1", entry.ClientRequestID)
	}
}

// TestApplyRequestContextFieldsKeepsExistingOrigin 钉住「只补空位」：
// entry 上已有的 origin 值不被 ctx 覆盖（first-write-wins）。
func TestApplyRequestContextFieldsKeepsExistingOrigin(t *testing.T) {
	c := &RequestLogContext{Request: originCtxRequest(t)}
	existing := "business"
	entry := &telemetry.RequestLogEntry{RequestID: "req-keep", OriginStage: &existing}

	applyRequestContextFields(entry, nil, c)

	if entry.OriginStage == nil || *entry.OriginStage != "business" {
		t.Fatalf("OriginStage = %v, want business（已有值不得被覆盖）", entry.OriginStage)
	}
}

func TestApplyRequestContextFieldsNilSafe(t *testing.T) {
	applyRequestContextFields(nil, nil, nil)

	entry := &telemetry.RequestLogEntry{RequestID: "req-nil"}
	applyRequestContextFields(entry, nil, nil) // c == nil：不得 panic
	if entry.OriginStage != nil {
		t.Fatalf("OriginStage = %v, want nil", entry.OriginStage)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// L2 行为门（端到端）：真实成功终态路径
// ─────────────────────────────────────────────────────────────────────────────

// noopRequestLogSink 让 telemetry.Client 进入 Enabled() 状态（判定条件是
// pgEnabled() || requestSink() != nil），从而 emitTelemetry 不会提前返回。
// 真正的断言落在 requestLogHook 上（同步回调，不依赖落库时序）。
type noopRequestLogSink struct{}

func (noopRequestLogSink) PersistRequestLog(context.Context, *telemetry.RequestLogEntry) error {
	return nil
}

// TestSuccessTerminalEntryCarriesProbeOrigin 走**真实**成功终态路径
// （ChatHandler.emitTelemetry），断言产出的 entry 带探测打标。
//
// 这是本轮缺陷的直接回归点：修复前 emitTelemetry 从零构造 entry，
// origin_stage / origin_actor / client_ip 全空 ⇒ 成功的探测请求在会话族里
// 被当成业务流量。
func TestSuccessTerminalEntryCarriesProbeOrigin(t *testing.T) {
	client := telemetry.NewClient()
	client.SetRequestLogSink(noopRequestLogSink{})
	defer client.Stop()

	var captured *telemetry.RequestLogEntry
	h := &ChatHandler{
		telemetryClient: client,
		requestLogHook: func(e *telemetry.RequestLogEntry) {
			captured = e
		},
	}

	req := originCtxRequest(t)
	logCtx := &RequestLogContext{
		Request:        req,
		StartTime:      time.Now(),
		ClientEndpoint: "/v1/chat/completions",
	}

	evt := audit.Event{
		RequestID:   "req-success-terminal",
		ClientModel: "nemotron-3-ultra-550b-a55b",
	}
	result := &executors.ExecuteResult{
		Candidate: provider.Candidate{CredentialID: 29, ProviderID: 1},
		Response:  &http.Response{Header: http.Header{}},
	}

	h.emitTelemetry(evt, result, "", nil, nil, "chat", nil, nil, nil, logCtx)

	if captured == nil {
		t.Fatal("requestLogHook 未被调用：成功终态 entry 没有产出")
	}
	if captured.OriginStage == nil || *captured.OriginStage != "node_probe" {
		t.Fatalf("成功终态 entry 的 OriginStage = %v, want node_probe", captured.OriginStage)
	}
	if captured.OriginActor == nil || *captured.OriginActor != "probe-service" {
		t.Fatalf("成功终态 entry 的 OriginActor = %v, want probe-service", captured.OriginActor)
	}
	if captured.ClientIP == nil || *captured.ClientIP != "127.0.0.1" {
		t.Fatalf("成功终态 entry 的 ClientIP = %v, want 127.0.0.1", captured.ClientIP)
	}
	if captured.ClientEndpoint == nil || *captured.ClientEndpoint != "/v1/chat/completions" {
		t.Fatalf("成功终态 entry 的 ClientEndpoint = %v, want /v1/chat/completions", captured.ClientEndpoint)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// L3 结构门（AST）：会话族镜像的每个输入点都必须补上下文派生字段
// ─────────────────────────────────────────────────────────────────────────────

// TestEveryMirrorInputPathAppliesRequestContextFields 枚举本包里所有
// `X.requestLogHook(...)` 调用点 —— 那是 session v2 镜像
// （internal/sessionv2mirror.PersistHook）**唯一**的输入来源，也是
// session_turns_hot 里每一行的来源。
//
// 对每个输入点，断言它所在的函数（传递地）调用了：
//
//	applyOriginMetadata   —— origin_stage / origin_actor / client_ip
//	applyAutoRouteFields  —— is_auto_request / task_type / task_type_chosen
//
// 新增一个镜像输入点却忘了补字段 ⇒ 这里变红。
func TestEveryMirrorInputPathAppliesRequestContextFields(t *testing.T) {
	funcs, sites := parseStreamingFuncs(t)

	if len(sites) == 0 {
		t.Fatal("没找到任何 requestLogHook 调用点：镜像输入点的解析口径失效（门会恒绿）")
	}

	for _, site := range sites {
		name := site.fn.Name
		if !reachesFunc(site.fn, "applyOriginMetadata", funcs, map[string]bool{}) {
			t.Errorf("镜像输入点 %s:%d（函数 %s）没有 (传递地) 调用 applyOriginMetadata ⇒ "+
				"该路径产出的终态 entry 不带 origin_stage/origin_actor/client_ip ⇒ "+
				"会话族里探测请求会丢打标",
				site.file, site.line, name)
		}
		if !reachesFunc(site.fn, "applyAutoRouteFields", funcs, map[string]bool{}) {
			t.Errorf("镜像输入点 %s:%d（函数 %s）没有 (传递地) 调用 applyAutoRouteFields ⇒ "+
				"该路径产出的终态 entry 不带 is_auto_request/task_type ⇒ "+
				"stats 侧的探测排除会失效",
				site.file, site.line, name)
		}
	}
}

// parseStreamingFuncs 解析本包（不含测试文件）的全部函数声明，并找出所有
// requestLogHook 调用点及其所在函数。
func parseStreamingFuncs(t *testing.T) (map[string]*ast.FuncDecl, []mirrorSite) {
	t.Helper()
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取包目录失败: %v", err)
	}
	funcs := map[string]*ast.FuncDecl{}
	var sites []mirrorSite

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			// 包级函数与**方法**都要进表：镜像输入点的调用链跨方法边界
			// （EmitFailure → c.BuildFailureEntry → c.buildEntry），
			// 只收包级函数会让传递解析在第一跳断掉 ⇒ 门变红但理由不对。
			funcs[fd.Name.Name] = fd
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				ce, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				se, ok := ce.Fun.(*ast.SelectorExpr)
				if !ok || se.Sel.Name != "requestLogHook" {
					return true
				}
				sites = append(sites, mirrorSite{
					file: path,
					line: fset.Position(ce.Pos()).Line,
					fn:   fd,
				})
				return true
			})
		}
	}
	return funcs, sites
}

type mirrorSite struct {
	file string
	line int
	fn   *ast.FuncDecl
}

// reachesFunc 报告 fn 是否（传递地）调用了 target。
//
// 传递范围限定在**本包**的函数声明里（含本包方法）：跨包解析会把不相关的
// 调用链也算进来，让门变弱。已解析不到的外部函数一律视为「不达成」。
func reachesFunc(fn *ast.FuncDecl, target string, funcs map[string]*ast.FuncDecl, seen map[string]bool) bool {
	if fn == nil || fn.Body == nil {
		return false
	}
	if fn.Name.Name == target {
		return true
	}
	if seen[fn.Name.Name] {
		return false
	}
	seen[fn.Name.Name] = true
	for _, callee := range calledFuncNames(fn) {
		next, ok := funcs[callee]
		if !ok {
			continue
		}
		if reachesFunc(next, target, funcs, seen) {
			return true
		}
	}
	return false
}

// calledFuncNames 收集 fn 里直接调用的函数名（普通调用取 Ident，方法调用取
// 选择器的 Sel 名 —— 后者覆盖同包内的方法调用，如 c.buildEntry(...)）。
func calledFuncNames(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := ce.Fun.(type) {
		case *ast.Ident:
			out = append(out, fun.Name)
		case *ast.SelectorExpr:
			out = append(out, fun.Sel.Name)
		}
		return true
	})
	return out
}
