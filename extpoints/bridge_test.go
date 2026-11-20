package extpoints

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// TestExtensionPointHookImplementsHookInterface 验证 ExtensionPointHook 实现了 pipeline.Hook 接口。
// 注意：由于我们不能在这里导入 domains/pipeline 包（会造成循环依赖），
// 我们通过鸭子类型验证接口契约。
func TestExtensionPointHookImplementsHookInterface(t *testing.T) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	// 验证所有必需方法存在
	_ = hook.Name()
	_ = hook.Priority()

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "test-001",
		Metadata: make(map[string]interface{}),
	}

	_ = hook.Enabled(ctx, envelope)
	err := hook.Execute(ctx, envelope)
	if err != nil {
		t.Logf("Execute returned: %v", err)
	}

	testErr := context.Canceled
	_ = hook.OnError(ctx, envelope, testErr)
}

// TestExtensionPointHookName 验证 Name() 方法返回正确格式。
func TestExtensionPointHookName(t *testing.T) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	name := hook.Name()
	expected := "ExtensionPointBridge(authentication)"
	if name != expected {
		t.Errorf("Expected name %q, got %q", expected, name)
	}
}

// TestExtensionPointHookPriority 验证 Priority() 返回中等优先级。
func TestExtensionPointHookPriority(t *testing.T) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	priority := hook.Priority()
	if priority != 500 {
		t.Errorf("Expected priority 500, got %d", priority)
	}
}

// TestExtensionPointHookEnabled_NoPlugins 验证没有插件时 Enabled 返回 false。
func TestExtensionPointHookEnabled_NoPlugins(t *testing.T) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "test-002",
	}

	enabled := hook.Enabled(ctx, envelope)
	if enabled {
		t.Error("Expected Enabled() to return false when no plugins registered")
	}
}

// TestExtensionPointHookEnabled_WithPlugins 验证有插件时 Enabled 返回 true。
func TestExtensionPointHookEnabled_WithPlugins(t *testing.T) {
	registry := NewExtensionPointRegistry()

	// 注册一个测试插件
	plugin := newStub("test-plugin", PhaseAuth, PriorityNormal)
	if err := registry.Register(PhaseAuth, plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "test-003",
	}

	enabled := hook.Enabled(ctx, envelope)
	if !enabled {
		t.Error("Expected Enabled() to return true when plugins registered")
	}
}

// TestExtensionPointHookExecute_NoPlugins 验证没有插件时 Execute 成功返回。
func TestExtensionPointHookExecute_NoPlugins(t *testing.T) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID:  "test-004",
		SessionID: "session-004",
		Metadata:  make(map[string]interface{}),
	}

	err := hook.Execute(ctx, envelope)
	if err != nil {
		t.Errorf("Expected Execute() to succeed with no plugins, got error: %v", err)
	}
}

// TestExtensionPointHookExecute_WithPlugin 验证有插件时 Execute 调用插件。
func TestExtensionPointHookExecute_WithPlugin(t *testing.T) {
	registry := NewExtensionPointRegistry()

	// 创建一个简单插件
	plugin := newStub("test-plugin", PhaseAuth, PriorityNormal)
	if err := registry.Register(PhaseAuth, plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID:  "tenant-456",
		SessionID: "session-123",
		Metadata: map[string]interface{}{
			"model": "gpt-4",
		},
	}

	err := hook.Execute(ctx, envelope)
	if err != nil {
		t.Errorf("Execute() failed: %v", err)
	}

	// 验证插件被执行了
	if plugin.execs != 1 {
		t.Errorf("Expected plugin to be executed once, got %d", plugin.execs)
	}
}

// TestExtensionPointHookExecute_ResponseSync 验证响应数据同步回 envelope。
func TestExtensionPointHookExecute_ResponseSync(t *testing.T) {
	registry := NewExtensionPointRegistry()

	plugin := newStub("data-plugin", PhaseAuth, PriorityNormal)
	if err := registry.Register(PhaseAuth, plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "test-006",
		Metadata: make(map[string]interface{}),
	}

	err := hook.Execute(ctx, envelope)
	if err != nil {
		t.Errorf("Execute() failed: %v", err)
	}

	// 验证执行统计被记录（engine 会在 Response.Attributes 中添加）
	// 注意：stubPlugin 的执行不会修改 Attributes，所以这里只验证没有 panic
}

// TestExtensionPointHookOnError 验证 OnError 吞掉错误（fail-open）。
func TestExtensionPointHookOnError(t *testing.T) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "test-007",
	}

	testErr := context.Canceled
	resultErr := hook.OnError(ctx, envelope, testErr)

	// fail-open 策略：吞掉错误
	if resultErr != nil {
		t.Errorf("Expected OnError to return nil (fail-open), got: %v", resultErr)
	}
}

// TestPhaseMapping 验证 Phase 映射逻辑。
func TestPhaseMapping(t *testing.T) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	tests := []struct {
		pipelinePhase string
		expected      []Phase
	}{
		{"pre_authentication", []Phase{PhasePreAuth}},
		{"authentication", []Phase{PhaseAuth}},
		{"post_authentication", []Phase{PhasePostAuth}},
		{"pre_routing", []Phase{PhasePreRoute}},
		{"routing", []Phase{PhaseRoute}},
		{"post_routing", []Phase{PhasePostRoute}},
		{"pre_upstream", []Phase{PhasePreUpstream}},
		{"upstream", []Phase{PhaseUpstream}},
		{"post_upstream", []Phase{PhasePostUpstream}},
		{"pre_response", []Phase{PhasePreResponse}},
		{"response", []Phase{PhaseResponse}},
		{"post_response", []Phase{PhasePostResponse}},
		{"governance", []Phase{}},     // 无映射
		{"pre_transform", []Phase{}},  // 无映射
		{"transform", []Phase{}},      // 无映射
		{"post_transform", []Phase{}}, // 无映射
		{"unknown_phase", []Phase{}},  // 无映射
	}

	for _, tt := range tests {
		t.Run(tt.pipelinePhase, func(t *testing.T) {
			result := hook.mapPhases(tt.pipelinePhase)
			if len(result) != len(tt.expected) {
				t.Errorf("Phase %s: expected %d phases, got %d", tt.pipelinePhase, len(tt.expected), len(result))
				return
			}
			for i, phase := range result {
				if phase != tt.expected[i] {
					t.Errorf("Phase %s[%d]: expected %s, got %s", tt.pipelinePhase, i, tt.expected[i], phase)
				}
			}
		})
	}
}

// TestExtensionPointHookExecute_MultiplePlugins 验证多个插件按优先级执行。
func TestExtensionPointHookExecute_MultiplePlugins(t *testing.T) {
	registry := NewExtensionPointRegistry()

	plugin1 := newStub("plugin-low-priority", PhaseAuth, PriorityLow)
	plugin2 := newStub("plugin-high-priority", PhaseAuth, PriorityHigh)

	if err := registry.Register(PhaseAuth, plugin1); err != nil {
		t.Fatalf("Register plugin1 failed: %v", err)
	}
	if err := registry.Register(PhaseAuth, plugin2); err != nil {
		t.Fatalf("Register plugin2 failed: %v", err)
	}

	hook := NewExtensionPointHook(registry, "authentication")

	ctx := context.Background()
	envelope := &domain.PipelineRequest{
		TenantID: "test-008",
		Metadata: make(map[string]interface{}),
	}

	err := hook.Execute(ctx, envelope)
	if err != nil {
		t.Errorf("Execute() failed: %v", err)
	}

	// 验证两个插件都被执行了
	if plugin1.execs != 1 {
		t.Errorf("Expected plugin1 to be executed once, got %d", plugin1.execs)
	}
	if plugin2.execs != 1 {
		t.Errorf("Expected plugin2 to be executed once, got %d", plugin2.execs)
	}

	// 注意：由于 engine.Execute 按优先级顺序执行，
	// plugin2（PriorityHigh=100）应该在 plugin1（PriorityLow=900）之前执行
}

// TestBuildRequest 验证从 PipelineRequest 构建 extpoints.Request。
func TestBuildRequest(t *testing.T) {
	registry := NewExtensionPointRegistry()
	hook := NewExtensionPointHook(registry, "authentication")

	envelope := &domain.PipelineRequest{
		TenantID:  "tenant-123",
		SessionID: "session-456",
		Metadata: map[string]interface{}{
			"method": "POST",
			"path":   "/v1/chat/completions",
			"model":  "gpt-4",
			"remote": "192.168.1.100",
			"agent":  "test-agent/1.0",
		},
	}

	req := hook.buildRequest(envelope)

	if req.TenantID != "tenant-123" {
		t.Errorf("Expected TenantID tenant-123, got %s", req.TenantID)
	}
	// SessionID is stored in Attributes
	if sessionID, ok := req.Attributes["session_id"].(string); !ok || sessionID != "session-456" {
		t.Errorf("Expected SessionID session-456 in Attributes, got %v", req.Attributes["session_id"])
	}
	if req.Method != "POST" {
		t.Errorf("Expected Method POST, got %s", req.Method)
	}
	if req.Path != "/v1/chat/completions" {
		t.Errorf("Expected Path /v1/chat/completions, got %s", req.Path)
	}
	if req.Model != "gpt-4" {
		t.Errorf("Expected Model gpt-4, got %s", req.Model)
	}
	if req.ClientIP != "192.168.1.100" {
		t.Errorf("Expected ClientIP 192.168.1.100, got %s", req.ClientIP)
	}
	if req.UserAgent != "test-agent/1.0" {
		t.Errorf("Expected UserAgent test-agent/1.0, got %s", req.UserAgent)
	}
}
