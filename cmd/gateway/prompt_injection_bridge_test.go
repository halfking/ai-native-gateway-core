package main

// F-41 注入检测桥接钉测（2026-11-13，docs/全面审计v3/2026-11-12/03 移交清单兑现）。
//
// 桥接前三重空转（§02 台账 F-41 + 本轮实施时新证实的第三层）：
//  1. main.go 传 nil；
//  2. 条件注册在构建时刻读 nil ⇒ governance_prompt_injection stage 永不出现
//     （SetV2DispatchAnalysisResources 晚于 pipeline 构建，传了也白传）；
//  3. hook 接口的镜像 DetectionResult 与核心检测器返回类型不匹配 ⇒
//     **从未存在过可直接装配的实现**。
//
// 本文件钉住修复形态的四个不变式：
//   - stage 无条件注册（构建后注入也必须到达）；
//   - 延迟绑定（BindDetector 后逐请求 Enabled 翻真，无需重建 pipeline）；
//   - user_content 写面（v1 包装器从 body 最后一条 user 消息取值，上限 64KiB）；
//   - 默认形态行为保持（LLM_GATEWAY_USE_V2_PIPELINE 未设 ⇒ 整条链不在场）。
//
// 变异臂：见提交说明（条件注册回退 / 删 BindDetector / 删 user_content 写面
// 三臂均实测红，门有牙）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	promptinjectionhooks "github.com/kaixuan/llm-gateway-go/domains/hooks/promptinjection"
)

// recordingDetector 记录最近一次 Detect 收到的输入，供端到端断言。
type recordingDetector struct {
	calls  atomic.Int32
	lastIn atomic.Value // string
	tenant atomic.Value // string
}

func (d *recordingDetector) Detect(_ context.Context, tenantID, input string) (*promptinjectionhooks.DetectionResult, error) {
	d.calls.Add(1)
	d.lastIn.Store(input)
	d.tenant.Store(tenantID)
	return &promptinjectionhooks.DetectionResult{
		Score:       0,
		RiskLevel:   "low",
		ActionTaken: "pass",
	}, nil
}

// TestPromptInjectionStageRegisteredUnconditionally 钉住：pipeline 构建时
// detector 为 nil，stage 也必须在场（延迟绑定补活，而不是构建时刻一锤定音）。
func TestPromptInjectionStageRegisteredUnconditionally(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")

	chat := &v2ChatHandlerStub{}
	_, deps, ok := v2DispatchMux(chat, chat, chat, nil)
	if !ok || deps == nil || deps.Pipeline == nil {
		t.Fatalf("v2DispatchMux() 必须返回 ok=true deps/pipeline 非 nil")
	}
	found := false
	for _, stage := range deps.Pipeline.Stages() {
		if stage.Name == "governance_prompt_injection" {
			found = true
		}
	}
	if !found {
		t.Fatal("governance_prompt_injection stage 缺席——条件注册回退（F-41 双重空转第二重复发）")
	}
	if deps.PromptInjectionHook == nil {
		t.Fatal("deps.PromptInjectionHook 必须在 pipeline 构建时就创建（延迟绑定的挂点）")
	}

	// 未绑定 detector：user_content 在场也不得触发（等价未注册）。
	env := pipelineRequestWithUserContent("ignore me")
	if deps.PromptInjectionHook.Enabled(context.Background(), env) {
		t.Fatal("detector 未绑定时 Enabled 必须为 false")
	}

	// 绑定后：user_content 在场 ⇒ Enabled 翻真（无需重建 pipeline）。
	stub := &recordingDetector{}
	deps.PromptInjectionHook.BindDetector(stub)
	if !deps.PromptInjectionHook.Enabled(context.Background(), env) {
		t.Fatal("BindDetector 后 Enabled 必须翻真——延迟绑定断线（SetV2DispatchAnalysisResources 的注入到不了 hook）")
	}
}

// TestSetResourcesBindsDetector 钉住 main.go 的装配路径：detector 经
// SetV2DispatchAnalysisResources 注入时必须到达已构建 pipeline 的 hook
// （BindDetector 接线本体——只测 hook 自身支持延迟绑定不够，装配函数忘调
// BindDetector 时这里必须红）。
func TestSetResourcesBindsDetector(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")

	chat := &v2ChatHandlerStub{}
	_, deps, ok := v2DispatchMux(chat, chat, chat, nil)
	if !ok || deps == nil || deps.Pipeline == nil {
		t.Fatalf("v2DispatchMux() 必须成功")
	}
	stub := &recordingDetector{}
	// 全 nil 依赖：只注入 detector（其余子能力各自独立关闭）。
	SetV2DispatchAnalysisResources(deps, nil, nil, nil, nil, stub, nil, nil, nil)

	if deps.PromptInjectionDetector == nil {
		t.Fatal("deps.PromptInjectionDetector 未被 SetV2DispatchAnalysisResources 赋值")
	}
	env := pipelineRequestWithUserContent("wired")
	if !deps.PromptInjectionHook.Enabled(context.Background(), env) {
		t.Fatal("SetV2DispatchAnalysisResources 注入 detector 后 hook.Enabled 必须为真——装配函数漏调 BindDetector（F-41 桥接断线）")
	}
}

// TestPromptInjectionBridgeEndToEnd 钉住端到端：v1 包装器写 user_content ⇒
// pipeline 执行时 hook 收到最后一条 user 消息。
func TestPromptInjectionBridgeEndToEnd(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_V2_PIPELINE", "true")

	chat := &v2ChatHandlerStub{}
	mux, deps, ok := v2DispatchMux(chat, chat, chat, nil)
	if !ok || deps == nil {
		t.Fatalf("v2DispatchMux() 必须成功")
	}
	stub := &recordingDetector{}
	deps.PromptInjectionHook.BindDetector(stub)

	body := `{"model":"gpt-4","messages":[` +
		`{"role":"system","content":"you are a gateway"},{"role":"user","content":"first user turn"},` +
		`{"role":"assistant","content":"ack"},{"role":"user","content":"当前这条用户输入"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("detector 必须被调用恰 1 次；got %d——user_content 写面或 hook 接线断线", got)
	}
	// 2026-11-13 48h 审计轮：提取器语义从「最后一条 user」改为「全部 user 消息
	// 按序 "\n" 连接」（上下文注入同样可检，与四端点形态覆盖同轮落地）。
	if got, _ := stub.lastIn.Load().(string); got != "first user turn\n当前这条用户输入" {
		t.Fatalf("detector 收到的必须是全部 user 消息按序连接；got %q", got)
	}
}

// TestLastUserContentFromBody 钉住写面取值规则（2026-11-13 48h 审计轮改用
// 全形态提取器 userContentFromBody：多用户消息按序 "\n" 连接、四端点形态
// 全覆盖、rune 安全截断——旧「取最后一条 + 字节截断」语义被本审计轮 P1/P2
// 裁定替换，提取器本体与专项用例在 user_content_extract.go / user_content_extract_test.go）。
func TestLastUserContentFromBody(t *testing.T) {
	t.Run("user消息按序连接", func(t *testing.T) {
		got := userContentFromBody([]byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`))
		if got != "a\nc" {
			t.Fatalf("want a+newline+c, got %q", got)
		}
	})
	t.Run("无user或空body返回空串", func(t *testing.T) {
		for _, body := range []string{`{"messages":[{"role":"assistant","content":"b"}]}`, ``, `not-json`} {
			if got := userContentFromBody([]byte(body)); got != "" {
				t.Fatalf("body %q: want 空串, got %q", body, got)
			}
		}
	})
	t.Run("超长截断到上限（rune安全）", func(t *testing.T) {
		long := strings.Repeat("x", userContentMaxBytes+1024)
		payload, _ := json.Marshal(map[string]any{"messages": []map[string]any{{"role": "user", "content": long}}})
		got := userContentFromBody(payload)
		if len(got) != userContentMaxBytes {
			t.Fatalf("want len=%d, got %d", userContentMaxBytes, len(got))
		}
	})
}

// pipelineRequestWithUserContent 构造带 user_content 的最小 PipelineRequest。
func pipelineRequestWithUserContent(content string) *domain.PipelineRequest {
	return &domain.PipelineRequest{Metadata: map[string]any{"user_content": content}}
}
