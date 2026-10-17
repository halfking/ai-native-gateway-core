package main

// topology_plugins_wiring_test.go — 拓扑插件源适配判据。
//
// 要钉住的契约：
//  1. v2 治理注册中心在场 ⇒ 它是唯一真值（逐插件投影 Name/Direction）；
//  2. v2 管线关闭（当前默认部署形态）⇒ 回退报告 v1 数据面**真实装配**的守卫：
//     脱敏检测器非 nil 才报、敏感词引擎词表非空才报（空词表=不拦截，报了就是
//     装样子）、输出合规恒报（流式执行器内建，不存在「装了没用」形态）；
//  3. external 注册中心 nil ⇒ 空清单（不是 nil，不是 degraded）。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/security"
	"github.com/kaixuan/llm-gateway-go/domains/security/plugins"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/kaixuan/llm-gateway-go/security/sensitive"
)

// fakeDetector = 非 nil 的最小检测器（适配层只看指针是否为 nil，
// 不看词表内容——词表口径由真值侧 main.go 装配时决定）。
var fakeDetector = sanitize.NewPatternDetector()

func TestTopologyPluginsV1FallbackReportsOnlyWiredGuards(t *testing.T) {
	// 敏感词引擎存在但词表为空（BuildFromFile 失败的真实形态）：
	// 不得报 sensitive_word_* —— 空词表拦不了任何东西。
	adapter := topologyPluginsAdapter{swEngine: sensitive.NewSensitiveWordEngine()}
	got := adapter.BuiltinPlugins()
	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Name)
	}
	for _, n := range names {
		if n == "sensitive_word_input" || n == "sensitive_word_output" {
			t.Fatalf("空词表引擎不得上报敏感词插件: %v", names)
		}
	}
	// 输出合规是流式执行器内建，恒在。
	foundCompliance := false
	for _, p := range got {
		if p.Name == "output_compliance" {
			foundCompliance = true
			if p.Direction != "output" {
				t.Fatalf("output_compliance 方向应为 output, got %s", p.Direction)
			}
		}
	}
	if !foundCompliance {
		t.Fatalf("v1 回退必须报 output_compliance, got %v", names)
	}
	// 脱敏检测器未装配（nil）⇒ 不报 sanitize_pattern。
	for _, n := range names {
		if n == "sanitize_pattern" {
			t.Fatalf("nil 检测器不得上报 sanitize_pattern: %v", names)
		}
	}
}

func TestTopologyPluginsV1FallbackWithWiredSanitizer(t *testing.T) {
	// 全装配形态：脱敏检测器非 nil（此处用非 nil 占位指针的运行时形态由
	// main.go 决定，适配层只看指针），清单包含 sanitize_pattern 且方向 both。
	adapter := topologyPluginsAdapter{sanitizer: fakeDetector}
	got := adapter.BuiltinPlugins()
	found := false
	for _, p := range got {
		if p.Name == "sanitize_pattern" {
			found = true
			if p.Direction != "both" {
				t.Fatalf("sanitize_pattern 方向应为 both, got %s", p.Direction)
			}
		}
	}
	if !found {
		t.Fatalf("装配了检测器必须报 sanitize_pattern: %+v", got)
	}
}

func TestTopologyPluginsRegistryTakesPrecedenceOverFallback(t *testing.T) {
	// v2 注册中心在场 ⇒ 一它为准（回退集合不得混入）。
	reg := security.NewRegistry()
	reg.MustRegister(plugins.NewPromptInjectionChecker())
	adapter := topologyPluginsAdapter{builtin: reg, sanitizer: fakeDetector}
	got := adapter.BuiltinPlugins()
	if len(got) != 1 || got[0].Name != "prompt_injection" || got[0].Direction != "input" {
		t.Fatalf("注册中心应独占真值: %+v", got)
	}
}

func TestTopologyPluginsExternalNilIsEmptyList(t *testing.T) {
	adapter := topologyPluginsAdapter{}
	if ext := adapter.ExternalPlugins(); ext == nil || len(ext) != 0 {
		t.Fatalf("nil external 注册中心应返回空清单, got %+v", ext)
	}
}

func TestTopologyPluginsExternalOrderIsDeterministic(t *testing.T) {
	// Snapshot 走 map 迭代，顺序随机 —— 适配层必须排序，
	// 否则前端 chip 每次请求顺序变来变去（闪跳）。
	reg := pluginruntime.NewRegistry()
	reg.SetPlugin(&pluginruntime.PluginState{PluginID: "z-last", Status: "ready"})
	reg.SetPlugin(&pluginruntime.PluginState{PluginID: "a-first", Status: "failed"})
	adapter := topologyPluginsAdapter{external: reg}
	got := adapter.ExternalPlugins()
	if len(got) != 2 || got[0].PluginID != "a-first" || got[1].PluginID != "z-last" {
		t.Fatalf("外部插件应按 PluginID 升序, got %+v", got)
	}
	// 多次调用顺序稳定
	again := adapter.ExternalPlugins()
	if again[0].PluginID != got[0].PluginID || again[1].PluginID != got[1].PluginID {
		t.Fatalf("顺序不稳定: %+v vs %+v", got, again)
	}
}
