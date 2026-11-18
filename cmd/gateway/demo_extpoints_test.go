package main

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/extpoints"
)

// TestDemoAuthPlugin 测试认证演示插件。
func TestDemoAuthPlugin(t *testing.T) {
	plugin := NewDemoAuthPlugin()

	// 验证元数据
	md := plugin.Metadata()
	if md.PluginID != "demo-auth-logger" {
		t.Errorf("Expected plugin_id 'demo-auth-logger', got '%s'", md.PluginID)
	}
	if md.Phase != extpoints.PhaseAuth {
		t.Errorf("Expected phase PhaseAuth, got %s", md.Phase)
	}

	// 执行插件
	ctx := &extpoints.ExecutionContext{
		Context:   context.Background(),
		Phase:     extpoints.PhaseAuth,
		PluginID:  md.PluginID,
		State:     make(map[string]interface{}),
		RequestID: "test-request-123",
	}

	result, err := plugin.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// 验证结果
	if !result.Success {
		t.Error("Expected success=true")
	}

	// 验证 State 被更新
	if !ctx.State["demo_auth_checked"].(bool) {
		t.Error("Expected demo_auth_checked=true in State")
	}

	// 验证 Metadata
	if authChecked, ok := result.Metadata["auth_checked"].(bool); !ok || !authChecked {
		t.Error("Expected auth_checked=true in Metadata")
	}
}

// TestDemoHeaderPlugin 测试 header 注入演示插件。
func TestDemoHeaderPlugin(t *testing.T) {
	plugin := NewDemoHeaderPlugin()

	ctx := &extpoints.ExecutionContext{
		Context:   context.Background(),
		Phase:     extpoints.PhasePreRoute,
		PluginID:  plugin.Metadata().PluginID,
		State:     make(map[string]interface{}),
		RequestID: "test-request-456",
	}

	// 模拟上游认证插件已执行
	ctx.State["demo_auth_checked"] = true

	result, err := plugin.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Error("Expected success=true")
	}

	if !result.Modified {
		t.Error("Expected modified=true")
	}

	// 验证 header 注入标记
	if !ctx.State["demo_header_injected"].(bool) {
		t.Error("Expected demo_header_injected=true in State")
	}

	// 验证 Metadata
	if headerName, ok := result.Metadata["header_name"].(string); !ok || headerName != "X-Demo-Plugin" {
		t.Errorf("Expected header_name='X-Demo-Plugin', got %v", result.Metadata["header_name"])
	}
}

// TestDemoMetricsPlugin 测试指标记录演示插件。
func TestDemoMetricsPlugin(t *testing.T) {
	plugin := NewDemoMetricsPlugin()

	ctx := &extpoints.ExecutionContext{
		Context:   context.Background(),
		Phase:     extpoints.PhasePostResponse,
		PluginID:  plugin.Metadata().PluginID,
		State:     make(map[string]interface{}),
		RequestID: "test-request-789",
	}

	// 模拟上游插件的 State
	ctx.State["demo_auth_time"] = time.Now().Add(-100 * time.Millisecond)
	ctx.State["demo_header_injected"] = true

	result, err := plugin.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Error("Expected success=true")
	}

	// 验证 Metadata
	if metricsRecorded, ok := result.Metadata["metrics_recorded"].(bool); !ok || !metricsRecorded {
		t.Error("Expected metrics_recorded=true in Metadata")
	}

	if pluginsCount, ok := result.Metadata["plugins_count"].(int); !ok || pluginsCount != 2 {
		t.Errorf("Expected plugins_count=2, got %v", result.Metadata["plugins_count"])
	}
}

// TestDemoPluginsChain 测试多个演示插件的链式执行。
func TestDemoPluginsChain(t *testing.T) {
	// 创建所有 demo 插件
	authPlugin := NewDemoAuthPlugin()
	headerPlugin := NewDemoHeaderPlugin()
	metricsPlugin := NewDemoMetricsPlugin()

	// 共享 State（模拟请求流）
	state := make(map[string]interface{})
	requestID := "test-chain-001"

	// 1. 执行认证插件
	authCtx := &extpoints.ExecutionContext{
		Context:   context.Background(),
		Phase:     extpoints.PhaseAuth,
		PluginID:  authPlugin.Metadata().PluginID,
		State:     state,
		RequestID: requestID,
	}

	authResult, err := authPlugin.Execute(authCtx)
	if err != nil {
		t.Fatalf("Auth plugin failed: %v", err)
	}
	if !authResult.Success {
		t.Error("Auth plugin should succeed")
	}

	// 2. 执行 header 插件
	headerCtx := &extpoints.ExecutionContext{
		Context:   context.Background(),
		Phase:     extpoints.PhasePreRoute,
		PluginID:  headerPlugin.Metadata().PluginID,
		State:     state,
		RequestID: requestID,
	}

	headerResult, err := headerPlugin.Execute(headerCtx)
	if err != nil {
		t.Fatalf("Header plugin failed: %v", err)
	}
	if !headerResult.Success {
		t.Error("Header plugin should succeed")
	}

	// 3. 执行 metrics 插件
	metricsCtx := &extpoints.ExecutionContext{
		Context:   context.Background(),
		Phase:     extpoints.PhasePostResponse,
		PluginID:  metricsPlugin.Metadata().PluginID,
		State:     state,
		RequestID: requestID,
	}

	metricsResult, err := metricsPlugin.Execute(metricsCtx)
	if err != nil {
		t.Fatalf("Metrics plugin failed: %v", err)
	}
	if !metricsResult.Success {
		t.Error("Metrics plugin should succeed")
	}

	// 验证完整的 State 链
	if !state["demo_auth_checked"].(bool) {
		t.Error("Expected auth_checked in State")
	}
	if !state["demo_header_injected"].(bool) {
		t.Error("Expected header_injected in State")
	}
	if _, ok := state["demo_auth_time"].(time.Time); !ok {
		t.Error("Expected auth_time in State")
	}

	// 验证所有插件都记录了信息
	t.Logf("Chain execution complete: %d state entries", len(state))
}

// TestRegisterDemoExtensionPoints 测试插件注册函数。
func TestRegisterDemoExtensionPoints(t *testing.T) {
	// 创建一个 mock deps
	registry := extpoints.NewExtensionPointRegistry()
	deps := &v2DispatchDeps{
		ExtensionPointRegistry: registry,
	}

	// 注册 demo 插件
	err := registerDemoExtensionPoints(deps)
	if err != nil {
		t.Fatalf("registerDemoExtensionPoints failed: %v", err)
	}

	// 验证插件已注册
	authPlugins := registry.Get(extpoints.PhaseAuth)
	if len(authPlugins) != 1 {
		t.Errorf("Expected 1 plugin in PhaseAuth, got %d", len(authPlugins))
	}

	preRoutePlugins := registry.Get(extpoints.PhasePreRoute)
	if len(preRoutePlugins) != 1 {
		t.Errorf("Expected 1 plugin in PhasePreRoute, got %d", len(preRoutePlugins))
	}

	postResponsePlugins := registry.Get(extpoints.PhasePostResponse)
	if len(postResponsePlugins) != 1 {
		t.Errorf("Expected 1 plugin in PhasePostResponse, got %d", len(postResponsePlugins))
	}

	// 验证插件 ID
	if authPlugins[0].Metadata().PluginID != "demo-auth-logger" {
		t.Error("Auth plugin has wrong ID")
	}
}

// TestRegisterDemoExtensionPoints_NilRegistry 测试 nil registry 处理。
func TestRegisterDemoExtensionPoints_NilRegistry(t *testing.T) {
	// deps 为 nil
	err := registerDemoExtensionPoints(nil)
	if err != nil {
		t.Errorf("Expected no error with nil deps, got: %v", err)
	}

	// registry 为 nil
	deps := &v2DispatchDeps{
		ExtensionPointRegistry: nil,
	}
	err = registerDemoExtensionPoints(deps)
	if err != nil {
		t.Errorf("Expected no error with nil registry, got: %v", err)
	}
}
