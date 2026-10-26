package ursm_test

// magpie_p0_extension_onoff_test.go — P0-5 的门：**每个扩展点的 on/off 对照**。
//
// # 这道门守住哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P0-5）要求："对每个点写『**开着有行为、关着原样放行**』的对照测试。
// 每点 1 条 on + 1 条 off，共 ≥2N 条；变异『**关掉后仍生效**』必须红。"
//
// ★ 依据是 magpie 的扩展点表：每个点都有独立的失败策略与"关掉后"语义，
//   而且**每个点都有 on/off 对照测试** ⇒ "关掉 ⇒ 请求原样发出"是**被断言出来的**，
//   不是被相信的。本仓此前没有这一层。
//
// # 本门的三层结构
//
//	① 逐点对照（6 个扩展点 × on/off = 12 条）
//	② ★ 覆盖度门：扩展点裁决表（internal/extpoints）里每一条机制，
//	   都必须在本文件里有对应的 on + off 两条用例 ⇒ **≥2N 条**不是口号，是判据。
//	③ 变异：把"关掉"改成"仍然生效" ⇒ 对应用例必须红。
//
// # 逐点清单（与 internal/extpoints 的裁决表一一对应）
//
//	|http_middleware_chain|  中间件链：on ⇒ 有安全响应头；off ⇒ 原样放行
//	|request_pipeline|      v2 pipeline：on ⇒ hook 执行；off ⇒ envelope 原样
//	|hook_registry|         注册表：on ⇒ hook 执行；off ⇒ 被跳过、无效果
//	|hook_phase_vocabulary| 别名视图：on ⇒ 旧名解析到骨架 phase；off ⇒ 骨架独有 phase 无别名
//	|executor_hooks|        执行器钩子：on ⇒ 钩子可达；off ⇒ 无钩子、无效果
//	|response_intercept|    响应拦截链：on ⇒ body 被改；off ⇒ body 逐字节不变

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/hooks"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/internal/extpoints"
	"github.com/kaixuan/llm-gateway-go/middleware"
)

// ── 点 1：http_middleware_chain ───────────────────────────────────────────

// TestExtPointOn_http_middleware_chain —— 主链**开着** ⇒ 安全响应头在。
func TestExtPointOn_http_middleware_chain(t *testing.T) {
	h := middleware.NewBuilder().
		Add(middleware.NewSecurityHeadersMiddleware()).
		Build().
		Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("开着时 X-Content-Type-Options = %q，want nosniff", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("开着时 X-Frame-Options = %q，want DENY", got)
	}
}

// TestExtPointOff_http_middleware_chain —— 主链**关掉**（链为空）⇒ 原样放行。
func TestExtPointOff_http_middleware_chain(t *testing.T) {
	h := middleware.NewBuilder().Build().
		Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	for _, header := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy"} {
		if got := rec.Header().Get(header); got != "" {
			t.Fatalf("关掉时 %s = %q，want 空（请求应当原样放行）", header, got)
		}
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("关掉时状态码 = %d，want 200（请求应当原样放行）", rec.Code)
	}
}

// ── 点 2：request_pipeline ───────────────────────────────────────────────

// magpieP0PipelineHook 是一个可开关的 pipeline hook。
type magpieP0PipelineHook struct {
	enabled bool
	calls   int
}

func (h *magpieP0PipelineHook) Name() string  { return "magpie-p0-probe" }
func (h *magpieP0PipelineHook) Priority() int { return 100 }
func (h *magpieP0PipelineHook) Enabled(context.Context, *domain.PipelineRequest) bool {
	return h.enabled
}
func (h *magpieP0PipelineHook) OnError(context.Context, *domain.PipelineRequest, error) error {
	return nil
}
func (h *magpieP0PipelineHook) Execute(_ context.Context, env *domain.PipelineRequest) error {
	h.calls++
	env.SessionID = "touched-by-hook"
	return nil
}

// TestExtPointOn_request_pipeline —— pipeline **开着** ⇒ hook 执行、envelope 被改。
func TestExtPointOn_request_pipeline(t *testing.T) {
	hook := &magpieP0PipelineHook{enabled: true}
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "probe",
		Phase: pipeline.PhaseRouting,
		Hooks: []pipeline.Hook{hook},
		Mode:  pipeline.ModeSequential,
	})
	env := &domain.PipelineRequest{}
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if hook.calls != 1 {
		t.Fatalf("hook 调用次数 = %d，want 1", hook.calls)
	}
	if env.SessionID != "touched-by-hook" {
		t.Fatalf("envelope 未被 hook 改写（SessionID=%q）", env.SessionID)
	}
}

// TestExtPointOff_request_pipeline —— pipeline **关掉**（hook 禁用）⇒ envelope 原样。
func TestExtPointOff_request_pipeline(t *testing.T) {
	hook := &magpieP0PipelineHook{enabled: false}
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "probe",
		Phase: pipeline.PhaseRouting,
		Hooks: []pipeline.Hook{hook},
		Mode:  pipeline.ModeSequential,
	})
	env := &domain.PipelineRequest{SessionID: "untouched"}
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if hook.calls != 0 {
		t.Fatalf("禁用的 hook 被调用了 %d 次 —— 关掉 ⇒ 原样放行", hook.calls)
	}
	if env.SessionID != "untouched" {
		t.Fatalf("envelope 被改写了（SessionID=%q）—— 关掉 ⇒ 原样放行", env.SessionID)
	}
}

// ── 点 3：hook_registry ───────────────────────────────────────────────────

// magpieP0RegistryHook 是一个可开关的 hooks.Hook。
type magpieP0RegistryHook struct {
	enabled bool
	calls   int
}

func (h *magpieP0RegistryHook) Name() string       { return "magpie-p0-registry-probe" }
func (h *magpieP0RegistryHook) Priority() int      { return 100 }
func (h *magpieP0RegistryHook) Enabled() bool      { return h.enabled }
func (h *magpieP0RegistryHook) Phase() hooks.Phase { return hooks.PhaseRouting }
func (h *magpieP0RegistryHook) Execute(context.Context, *hooks.Environment) error {
	h.calls++
	return nil
}

// TestExtPointOn_hook_registry —— 注册表**开着** ⇒ hook 被执行。
func TestExtPointOn_hook_registry(t *testing.T) {
	hook := &magpieP0RegistryHook{enabled: true}
	reg := hooks.NewHookRegistry(nil, hooks.NewNoOpMetricsCollector(), &hooks.NoOpLogger{})
	if err := reg.Register(hook); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Execute(context.Background(), hooks.PhaseRouting, hooks.NewEnvironment("req-1")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if hook.calls != 1 {
		t.Fatalf("hook 调用次数 = %d，want 1", hook.calls)
	}
}

// TestExtPointOff_hook_registry —— 注册表里的 hook **关掉** ⇒ 被跳过、无效果。
func TestExtPointOff_hook_registry(t *testing.T) {
	hook := &magpieP0RegistryHook{enabled: false}
	reg := hooks.NewHookRegistry(nil, hooks.NewNoOpMetricsCollector(), &hooks.NoOpLogger{})
	if err := reg.Register(hook); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Execute(context.Background(), hooks.PhaseRouting, hooks.NewEnvironment("req-1")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if hook.calls != 0 {
		t.Fatalf("禁用的 hook 被执行了 %d 次 —— 关掉 ⇒ 无效果", hook.calls)
	}
}

// ── 点 4：hook_phase_vocabulary ──────────────────────────────────────────

// TestExtPointOn_hook_phase_vocabulary —— 别名视图**开着** ⇒ 旧阶段名解析到骨架 phase。
func TestExtPointOn_hook_phase_vocabulary(t *testing.T) {
	alias, ok := extpoints.HookPhaseAliases["pre_routing"]
	if !ok {
		t.Fatal("旧阶段名 pre_routing 没有别名 —— 别名视图失效")
	}
	if alias != "pre_routing" {
		t.Fatalf("别名 pre_routing → %q，want pre_routing（骨架里的同名 phase）", alias)
	}
	// 别名目标必须真的在骨架的 phase 集合里。
	found := false
	for _, p := range pipeline.AllPhases() {
		if string(p) == alias {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("别名目标 %q 不在骨架的 phase 集合里", alias)
	}
}

// TestExtPointOff_hook_phase_vocabulary —— 骨架**独有**的 phase 没有别名
// ⇒ 别名视图不会遮蔽骨架自己的词表。
func TestExtPointOff_hook_phase_vocabulary(t *testing.T) {
	// governance 是骨架独有（v4 新增），不在旧词表里。
	if _, ok := extpoints.HookPhaseAliases["governance"]; ok {
		t.Fatal("governance 不该有别名 —— 它是骨架独有的 phase")
	}
	// 骨架里的 phase 数必须严格多于旧词表（否则"别名是子集"的前提不成立）。
	all := pipeline.AllPhases()
	if len(all) <= len(extpoints.HookPhaseAliases) {
		t.Fatalf("骨架 phase 数 %d 未多于旧词表 %d —— 别名视图应当是**真子集**",
			len(all), len(extpoints.HookPhaseAliases))
	}
}

// ── 点 5：executor_hooks ────────────────────────────────────────────────

// TestExtPointOn_executor_hooks —— 执行器钩子**开着** ⇒ 钩子可达、可被取用。
//
// ★★★ **口径订正（2026-10-24，审计档 §3.14）**：本用例证明的是
// ★ **「API 装上能用」**，**不是**「**生产里装了**」——
// ★★ 生产代码**零安装方**（`WithExecuteHooks(` 只在测试里被调）⇒
// ★★ `executor_hooks` 已被**改判为 `dead`**（它和 `HookRegistry` 同一种形态）。
// ⇒ ⇒ 所以这里的 on/off 是「**API 开关**」的对照，不是「**生产接线**」的对照。
func TestExtPointOn_executor_hooks(t *testing.T) {
	called := 0
	hk := &executors.ExecuteHooks{
		OnCompressing: func() { called++ },
	}
	ctx := executors.WithExecuteHooks(context.Background(), hk)
	got := executors.ExecuteHooksFromContext(ctx)
	if got == nil {
		t.Fatal("钩子不可达 —— 开着时应当能取到")
	}
	if got.OnCompressing == nil {
		t.Fatal("OnCompressing 回调丢失")
	}
	got.OnCompressing()
	if called != 1 {
		t.Fatalf("回调次数 = %d，want 1", called)
	}
}

// TestExtPointOff_executor_hooks —— **不装**钩子 ⇒ 取不到、无效果（且不 panic）。
func TestExtPointOff_executor_hooks(t *testing.T) {
	ctx := executors.WithExecuteHooks(context.Background(), nil)
	if got := executors.ExecuteHooksFromContext(ctx); got != nil {
		t.Fatalf("没装钩子却取到了 %+v —— 关掉 ⇒ 无效果", got)
	}
	// 未装钩子的上下文必须原样可用（不 panic、不换上下文语义）。
	if executors.ExecuteHooksFromContext(context.Background()) != nil {
		t.Fatal("空上下文里不应取到钩子")
	}
}

// ── 点 6：response_intercept ────────────────────────────────────────────

// magpieP0Interceptor 是一个改写 body 的拦截器。
type magpieP0Interceptor struct{}

func (magpieP0Interceptor) InterceptNonStream(_ context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	return &response.InterceptResult{ModifiedBody: []byte("intercepted")}, nil
}
func (magpieP0Interceptor) InterceptStreamChunk(context.Context, []byte, *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}
func (magpieP0Interceptor) InterceptStreamEnd(context.Context, *response.StreamMeta) (*response.EndResult, error) {
	return nil, nil
}

// TestExtPointOn_response_intercept —— 拦截链**开着** ⇒ body 被改写。
func TestExtPointOn_response_intercept(t *testing.T) {
	chain := response.NewInterceptorChain(magpieP0Interceptor{})
	res, err := chain.InterceptNonStream(context.Background(), &response.InterceptRequest{
		ResponseBody: []byte("original"),
	})
	if err != nil {
		t.Fatalf("InterceptNonStream: %v", err)
	}
	if res == nil || string(res.ModifiedBody) != "intercepted" {
		t.Fatalf("开着时 body 未被改写：%+v", res)
	}
}

// TestExtPointOff_response_intercept —— 拦截链**关掉**（空链）⇒ body 逐字节不变。
func TestExtPointOff_response_intercept(t *testing.T) {
	chain := response.NewInterceptorChain()
	res, err := chain.InterceptNonStream(context.Background(), &response.InterceptRequest{
		ResponseBody: []byte("original"),
	})
	if err != nil {
		t.Fatalf("InterceptNonStream: %v", err)
	}
	if res != nil {
		t.Fatalf("关掉时返回了改写结果 %+v —— 关掉 ⇒ body 原样", res)
	}
}

// ── 覆盖度门（≥2N 条）────────────────────────────────────────────────────

// TestMagpieP0EveryExtensionPointHasOnOffPair 是 P0-5 的**核心**判据：
// 裁决表里的**每一条**机制，都必须在本文件里有 on + off 两条用例。
//
// ★ 怎么数的：**读本文件自己的源码**（不是读一张"我记着哪些点"的表）⇒
//
//	新增一条机制却忘了写对照用例 ⇒ 这里红。
func TestMagpieP0EveryExtensionPointHasOnOffPair(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读本文件失败: %v", err)
	}
	src := string(b)
	onRe := regexp.MustCompile(`func TestExtPointOn_([A-Za-z0-9_]+)\(`)
	offRe := regexp.MustCompile(`func TestExtPointOff_([A-Za-z0-9_]+)\(`)
	on := map[string]bool{}
	off := map[string]bool{}
	for _, m := range onRe.FindAllStringSubmatch(src, -1) {
		on[m[1]] = true
	}
	for _, m := range offRe.FindAllStringSubmatch(src, -1) {
		off[m[1]] = true
	}

	mechs := extpoints.Mechanisms()
	if len(mechs) == 0 {
		t.Fatal("裁决表为空 ⇒ 判据失效")
	}
	for _, m := range mechs {
		if !on[m.ID] {
			t.Fatalf("机制 %q 没有 on 用例（命名约定：TestExtPointOn_<id>）", m.ID)
		}
		if !off[m.ID] {
			t.Fatalf("机制 %q 没有 off 用例（命名约定：TestExtPointOff_<id>）", m.ID)
		}
	}
	// ★ 至少 2N 条：N = 机制数。
	if len(on) < len(mechs) || len(off) < len(mechs) {
		t.Fatalf("on 用例 %d 条 / off 用例 %d 条，少于 2×%d", len(on), len(off), len(mechs))
	}
	// 反向：多余的用例（不在裁决表里的机制）说明表与用例不同源。
	for id := range on {
		if _, ok := extpoints.MechanismByID(id); !ok {
			t.Fatalf("on 用例覆盖了裁决表里没有的机制 %q —— 表与用例不同源", id)
		}
	}
	for id := range off {
		if _, ok := extpoints.MechanismByID(id); !ok {
			t.Fatalf("off 用例覆盖了裁决表里没有的机制 %q —— 表与用例不同源", id)
		}
	}
}

// TestMagpieP0OnOffPairFileIsRegistered 断言本文件在仓内（防止门被搬走后失效）。
func TestMagpieP0OnOffPairFileIsRegistered(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	rel, err := filepath.Rel(filepath.Dir(filepath.Dir(filepath.Dir(file))), file)
	if err != nil {
		t.Fatalf("定位失败: %v", err)
	}
	if !strings.Contains(filepath.ToSlash(rel), "scripts/ursmcheck/") {
		t.Fatalf("本门不在 scripts/ursmcheck/ 下（实际 %s）—— 它就**不会**在 CI 上跑", rel)
	}
}
