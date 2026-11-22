// Command gateway — L3 守卫（2026-11-23，211 配置优化遗留 L3）
//
// 背景（211 生产实测）：
//
//	v2 pipeline: preflight error (falling through to v1 handler)
//	  error: pipeline: stage 'provider_discovery' (phase=pre_routing) failed:
//	         hook 'provider.discover' failed: provider: no healthy provider
//	         for model glm-5.3-flash
//
// 根因：v1 包装路径（LLM_GATEWAY_USE_V2_PIPELINE=true，211 生产走的就是它）
// 挂的 provider 目录是**内存 demo 目录**（gpt-4 假凭据）⇒ 任何真实模型都
// 命中「no healthy provider」⇒ 该阶段返回 error ⇒ **管线在此中止** ⇒
// 排在它之后的阶段对真实流量全部不可达。
//
// 这些门钉住三件事：
//
//	① provider_discovery **不再中止管线**（advisory），并把结论写进 metadata；
//	② 「请求路径」阶段默认**不注册**（前置位置跑它们会与 handler 的
//	   postflight 重复写；它们此前同样不可达 ⇒ 不是功能回退）；
//	③ 真实模型请求走完整条管线**不再产生 preflight error**。
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/domains/provider"
)

// ── 门 1：advisory 发现 hook 永不返回 error，且把结论写进 metadata ──────
func TestAdvisoryDiscoveryHookNeverAbortsPipeline(t *testing.T) {
	store := provider.NewInMemoryStore()
	prober := provider.NewProber(store)
	// 目录里只有 demo 的 gpt-4，真实模型查不到。
	if err := store.Save(&provider.Provider{
		ID: "default-openai", Name: "OpenAI", BaseURL: "https://api.openai.com",
		Protocol: provider.ProtocolOpenAI, AuthType: "bearer",
		Models: []provider.ModelSpec{{Name: "gpt-4"}}, TimeoutSec: 60,
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	hook := provider.NewAdvisoryDiscoveryHook(store, prober)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["model"] = "glm-5.3-flash"

	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("advisory hook 返回了 error: %v\n"+
			"  ★ 它一旦返回 error，管线就在这一阶段中止 ⇒ 治理链/路由链对真实流量不可达", err)
	}
	status, _ := env.Metadata["provider_discovery_status"].(string)
	if status != "no_healthy_provider" {
		t.Errorf("provider_discovery_status = %q，期望 %q", status, "no_healthy_provider")
	}
	if detail, _ := env.Metadata["provider_discovery_detail"].(string); detail == "" {
		t.Error("provider_discovery_detail 为空：失败时必须留原因，否则日志里只剩一个状态码")
	}
}

// ── 门 2：严格版仍然严格（v2 独立网关那条路径不受影响）────────────────
func TestStrictDiscoveryHookStillFails(t *testing.T) {
	store := provider.NewInMemoryStore()
	prober := provider.NewProber(store)
	hook := provider.NewProviderDiscoveryHook(store, prober)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["model"] = "glm-5.3-flash"

	if err := hook.Execute(context.Background(), env); err == nil {
		t.Fatal("严格版 hook 必须返回 error（cmd/gateway-v2 那条路径以它为门禁）")
	}
}

// ── 门 3：v1 包装路径注册的是 advisory 版本，且「请求路径」阶段默认不注册 ──
func TestDispatchPipelineStagesArePreflightOnly(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")
	t.Setenv("LLM_GATEWAY_V2_REQUEST_PATH", "")

	deps := newV2DispatchDepsFromMain(loadV2DispatchConfig(), nil, nil, nil)
	if deps == nil || deps.Pipeline == nil {
		t.Fatal("deps.Pipeline 为 nil")
	}

	names := map[string]bool{}
	for _, st := range deps.Pipeline.Stages() {
		names[st.Name] = true
	}

	// ① 请求路径阶段默认不得注册（前置位置跑它们会与 postflight 重复写）。
	for _, banned := range []string{
		"transform", "compression", "tools", "streaming",
		"audit", "cache_save", "metrics", "session_analytics", "session_v2_persist",
	} {
		if names[banned] {
			t.Errorf("阶段 %q 被注册了。\n"+
				"  ★ v1 包装路径的管线是**前置**执行：这些阶段要么看不到上游响应，\n"+
				"    要么会与 handler 自己的 postflight 重复写（request_logs / session 行 /\n"+
				"    分析任务）。它们此前同样不可达（provider_discovery 中止），\n"+
				"    ⇒ 默认不注册不是功能回退，而是把「死代码」标出来。", banned)
		}
	}

	// ② 治理链必须仍然在册（它们才是 v2 架构在 v1 路径上的产出）。
	for _, want := range []string{
		"governance_prompt_injection", "security", "governance_security",
		"governance_interception", "provider_discovery",
	} {
		if !names[want] {
			t.Errorf("阶段 %q 不在册：v1 包装路径的治理链被弄丢了", want)
		}
	}
}

// ── 门 4：provider_discovery 这一阶段挂的必须是 advisory 版本 ──────────
//
// ★ 为什么还要这条「接线门」：门 1 量的是 hook 自己的行为，
//
//	而「调用点还在不在 / 挂的是哪一版」只有看接线才能抓到
//	（AGENTS.md 纪律：接线才是会退化的那一环）。
func TestDispatchPipelineUsesAdvisoryDiscoveryHook(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")

	deps := newV2DispatchDepsFromMain(loadV2DispatchConfig(), nil, nil, nil)
	if deps == nil || deps.Pipeline == nil {
		t.Fatal("deps.Pipeline 为 nil")
	}
	var found bool
	for _, st := range deps.Pipeline.Stages() {
		if st.Name != "provider_discovery" {
			continue
		}
		found = true
		for _, h := range st.Hooks {
			if _, ok := h.(*provider.AdvisoryDiscoveryHook); !ok {
				t.Errorf("provider_discovery 挂的是 %T，期望 *provider.AdvisoryDiscoveryHook。\n"+
					"  ★ 严格版会在「找不到健康 provider」时中止管线 ⇒ 治理链/路由链对真实流量不可达。",
					h)
			}
		}
	}
	if !found {
		t.Fatal("provider_discovery 阶段不在册")
	}
}

// ── 门 5：真实模型请求走完整条管线**不再产生 preflight error** ─────────
//
// 这条是本轮 L3 的**行为**门：它走真实调用点（v2DispatchHandler），
// 断言管线不再中止 ⇒ 生产上那条每请求一条的 WARN 会消失。
func TestRealModelRequestDoesNotAbortPipeline(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")
	t.Setenv("LLM_GATEWAY_V2_REQUEST_PATH", "")

	deps := newV2DispatchDepsFromMain(loadV2DispatchConfig(), nil, nil, nil)
	if deps == nil || deps.Pipeline == nil {
		t.Fatal("deps.Pipeline 为 nil")
	}

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["model"] = "glm-5.3-flash"
	env.Metadata["user_content"] = "hello"

	err := deps.Pipeline.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("管线对真实模型中止了: %v\n"+
			"  ★ 这正是 211 生产日志里每请求一条的 "+
			"「v2 pipeline: preflight error (falling through to v1 handler)」的来源。", err)
	}
	if status, _ := env.Metadata["provider_discovery_status"].(string); status == "" {
		t.Error("provider_discovery_status 为空：发现结论必须落到 metadata，否则无法观测")
	}
}

// ── 门 6：源码层不得再把严格版挂到 v1 包装路径上 ──────────────────────
//
// 文本门量的是「接线形态」，行为门（门 5）量的是「运行时结果」；
// 两者互补：文本门能在改动还没跑起来时就抓住「又换回严格版」。
func TestDispatchSourceDoesNotUseStrictDiscoveryHook(t *testing.T) {
	src, err := os.ReadFile("main_pipeline.go")
	if err != nil {
		t.Fatalf("读 main_pipeline.go 失败: %v", err)
	}
	body := string(src)
	if strings.Contains(body, "provider.NewProviderDiscoveryHook(deps.ProviderStore, deps.ProviderProber)") {
		t.Error("main_pipeline.go 里还有 provider.NewProviderDiscoveryHook(...)。\n" +
			"  ★ v1 包装路径必须用 advisory 版本：严格版在 demo 目录上会中止整条管线。")
	}
}

// ── 门 7：请求路径阶段必须能被显式打开（不是被删掉）──────────────────
//
// 门 3 断言「默认不注册」；这条断言「开关一打开就注册」⇒
// 证明这些阶段仍然存在、只是默认不接，而不是被删成了死代码。
func TestRequestPathStagesCanBeEnabledExplicitly(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")
	t.Setenv("LLM_GATEWAY_V2_REQUEST_PATH", "true")

	deps := newV2DispatchDepsFromMain(loadV2DispatchConfig(), nil, nil, nil)
	if deps == nil || deps.Pipeline == nil {
		t.Fatal("deps.Pipeline 为 nil")
	}
	names := map[string]bool{}
	for _, st := range deps.Pipeline.Stages() {
		names[st.Name] = true
	}
	for _, want := range []string{"transform", "audit", "metrics"} {
		if !names[want] {
			t.Errorf("LLM_GATEWAY_V2_REQUEST_PATH=true 时阶段 %q 仍不在册：这些阶段被删掉了？", want)
		}
	}
}

// 让未使用的 import 有交代（http/httptest 供将来的接线测试使用）。
var _ = http.StatusOK
var _ = httptest.NewRecorder
var _ pipeline.Hook
