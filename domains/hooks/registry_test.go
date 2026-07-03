package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// MockHook 用于测试的Mock Hook
type MockHook struct {
	name        string
	priority    int
	enabled     bool
	phase       Phase
	executeFunc func(ctx context.Context, env *Environment) error
	executed    bool
}

func NewMockHook(name string, phase Phase, priority int) *MockHook {
	return &MockHook{
		name:     name,
		priority: priority,
		enabled:  true,
		phase:    phase,
	}
}

func (m *MockHook) Name() string      { return m.name }
func (m *MockHook) Priority() int     { return m.priority }
func (m *MockHook) Enabled() bool     { return m.enabled }
func (m *MockHook) Phase() Phase      { return m.phase }
func (m *MockHook) SetEnabled(e bool) { m.enabled = e }

func (m *MockHook) Execute(ctx context.Context, env *Environment) error {
	m.executed = true
	if m.executeFunc != nil {
		return m.executeFunc(ctx, env)
	}
	return nil
}

func (m *MockHook) WasExecuted() bool {
	return m.executed
}

func (m *MockHook) Reset() {
	m.executed = false
}

// MockConfigurableHook 支持配置热更新的Mock Hook
type MockConfigurableHook struct {
	MockHook
	configChangeFunc func(config map[string]interface{}) error
	configChanged    bool
}

func NewMockConfigurableHook(name string, phase Phase, priority int) *MockConfigurableHook {
	return &MockConfigurableHook{
		MockHook: MockHook{
			name:     name,
			priority: priority,
			enabled:  true,
			phase:    phase,
		},
	}
}

func (m *MockConfigurableHook) OnConfigChange(config map[string]interface{}) error {
	m.configChanged = true
	if m.configChangeFunc != nil {
		return m.configChangeFunc(config)
	}
	return nil
}

// TestHookRegistry_Register 测试Hook注册功能
func TestHookRegistry_Register(t *testing.T) {
	t.Run("successful registration", func(t *testing.T) {
		registry := NewHookRegistry(nil, nil, &NoOpLogger{})
		hook := NewMockHook("test-hook", PhasePreRouting, 10)

		err := registry.Register(hook)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		hooks := registry.GetHooks(PhasePreRouting)
		if len(hooks) != 1 {
			t.Fatalf("expected 1 hook, got %d", len(hooks))
		}
		if hooks[0].Name() != "test-hook" {
			t.Errorf("expected hook name 'test-hook', got %s", hooks[0].Name())
		}
	})

	t.Run("duplicate registration", func(t *testing.T) {
		registry := NewHookRegistry(nil, nil, &NoOpLogger{})
		hook := NewMockHook("test-hook", PhasePreRouting, 10)

		_ = registry.Register(hook)
		err := registry.Register(hook)

		if err == nil {
			t.Fatal("expected error for duplicate registration")
		}
	})

	t.Run("nil hook", func(t *testing.T) {
		registry := NewHookRegistry(nil, nil, &NoOpLogger{})
		err := registry.Register(nil)

		if err == nil {
			t.Fatal("expected error for nil hook")
		}
	})

	t.Run("empty hook name", func(t *testing.T) {
		registry := NewHookRegistry(nil, nil, &NoOpLogger{})
		hook := NewMockHook("", PhasePreRouting, 10)

		err := registry.Register(hook)
		if err == nil {
			t.Fatal("expected error for empty hook name")
		}
	})

	t.Run("priority ordering", func(t *testing.T) {
		registry := NewHookRegistry(nil, nil, &NoOpLogger{})
		hook1 := NewMockHook("hook-1", PhasePreRouting, 50)
		hook2 := NewMockHook("hook-2", PhasePreRouting, 10)
		hook3 := NewMockHook("hook-3", PhasePreRouting, 30)

		_ = registry.Register(hook1)
		_ = registry.Register(hook2)
		_ = registry.Register(hook3)

		hooks := registry.GetHooks(PhasePreRouting)
		if len(hooks) != 3 {
			t.Fatalf("expected 3 hooks, got %d", len(hooks))
		}

		// 验证按Priority排序
		if hooks[0].Name() != "hook-2" || hooks[0].Priority() != 10 {
			t.Errorf("expected first hook to be hook-2 with priority 10")
		}
		if hooks[1].Name() != "hook-3" || hooks[1].Priority() != 30 {
			t.Errorf("expected second hook to be hook-3 with priority 30")
		}
		if hooks[2].Name() != "hook-1" || hooks[2].Priority() != 50 {
			t.Errorf("expected third hook to be hook-1 with priority 50")
		}
	})
}

// TestHookRegistry_Execute 测试Hook执行功能
func TestHookRegistry_Execute(t *testing.T) {
	t.Run("execute hooks in order", func(t *testing.T) {
		registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})

		var executionOrder []string
		hook1 := NewMockHook("hook-1", PhasePreRouting, 10)
		hook1.executeFunc = func(ctx context.Context, env *Environment) error {
			executionOrder = append(executionOrder, "hook-1")
			return nil
		}

		hook2 := NewMockHook("hook-2", PhasePreRouting, 20)
		hook2.executeFunc = func(ctx context.Context, env *Environment) error {
			executionOrder = append(executionOrder, "hook-2")
			return nil
		}

		_ = registry.Register(hook1)
		_ = registry.Register(hook2)

		env := NewEnvironment("test-request")
		err := registry.Execute(context.Background(), PhasePreRouting, env)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if len(executionOrder) != 2 {
			t.Fatalf("expected 2 hooks executed, got %d", len(executionOrder))
		}
		if executionOrder[0] != "hook-1" || executionOrder[1] != "hook-2" {
			t.Errorf("hooks not executed in order: %v", executionOrder)
		}
	})

	t.Run("skip disabled hooks", func(t *testing.T) {
		registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})

		hook := NewMockHook("test-hook", PhasePreRouting, 10)
		hook.SetEnabled(false)
		_ = registry.Register(hook)

		env := NewEnvironment("test-request")
		err := registry.Execute(context.Background(), PhasePreRouting, env)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if hook.WasExecuted() {
			t.Error("disabled hook should not be executed")
		}
	})

	t.Run("stop on Skip flag", func(t *testing.T) {
		registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})

		hook1 := NewMockHook("hook-1", PhasePreRouting, 10)
		hook1.executeFunc = func(ctx context.Context, env *Environment) error {
			env.SetSkip()
			return nil
		}

		hook2 := NewMockHook("hook-2", PhasePreRouting, 20)

		_ = registry.Register(hook1)
		_ = registry.Register(hook2)

		env := NewEnvironment("test-request")
		err := registry.Execute(context.Background(), PhasePreRouting, env)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if !hook1.WasExecuted() {
			t.Error("first hook should be executed")
		}
		if hook2.WasExecuted() {
			t.Error("second hook should not be executed when Skip is set")
		}
	})

	t.Run("stop on Abort flag", func(t *testing.T) {
		registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})

		hook1 := NewMockHook("hook-1", PhasePreRouting, 10)
		hook1.executeFunc = func(ctx context.Context, env *Environment) error {
			env.SetAbort("test abort")
			return nil
		}

		hook2 := NewMockHook("hook-2", PhasePreRouting, 20)

		_ = registry.Register(hook1)
		_ = registry.Register(hook2)

		env := NewEnvironment("test-request")
		err := registry.Execute(context.Background(), PhasePreRouting, env)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if !hook1.WasExecuted() {
			t.Error("first hook should be executed")
		}
		if hook2.WasExecuted() {
			t.Error("second hook should not be executed when Abort is set")
		}
		if env.AbortReason != "test abort" {
			t.Errorf("expected abort reason 'test abort', got %s", env.AbortReason)
		}
	})

	t.Run("critical phase error handling", func(t *testing.T) {
		registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})

		hook := NewMockHook("test-hook", PhasePreRouting, 10)
		hook.executeFunc = func(ctx context.Context, env *Environment) error {
			return errors.New("test error")
		}
		_ = registry.Register(hook)

		env := NewEnvironment("test-request")
		err := registry.Execute(context.Background(), PhasePreRouting, env)

		if err == nil {
			t.Fatal("expected error for critical phase")
		}
	})

	t.Run("non-critical phase error handling", func(t *testing.T) {
		registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})

		hook1 := NewMockHook("hook-1", PhasePostResponse, 10)
		hook1.executeFunc = func(ctx context.Context, env *Environment) error {
			return errors.New("test error")
		}

		hook2 := NewMockHook("hook-2", PhasePostResponse, 20)

		_ = registry.Register(hook1)
		_ = registry.Register(hook2)

		env := NewEnvironment("test-request")
		err := registry.Execute(context.Background(), PhasePostResponse, env)

		// 非关键阶段的错误不应该中断执行
		if err != nil {
			t.Fatalf("expected no error for non-critical phase, got %v", err)
		}

		if !hook2.WasExecuted() {
			t.Error("second hook should be executed even if first hook fails in non-critical phase")
		}
	})

	t.Run("nil environment", func(t *testing.T) {
		registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})
		err := registry.Execute(context.Background(), PhasePreRouting, nil)

		if err == nil {
			t.Fatal("expected error for nil environment")
		}
	})

	t.Run("invalid phase", func(t *testing.T) {
		registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})
		env := NewEnvironment("test-request")
		err := registry.Execute(context.Background(), Phase("invalid"), env)

		if err == nil {
			t.Fatal("expected error for invalid phase")
		}
	})

	t.Run("metadata sharing between hooks", func(t *testing.T) {
		registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})

		hook1 := NewMockHook("hook-1", PhasePreRouting, 10)
		hook1.executeFunc = func(ctx context.Context, env *Environment) error {
			env.Metadata["key1"] = "value1"
			return nil
		}

		hook2 := NewMockHook("hook-2", PhasePreRouting, 20)
		hook2.executeFunc = func(ctx context.Context, env *Environment) error {
			if val, ok := env.Metadata["key1"]; !ok || val != "value1" {
				return fmt.Errorf("metadata not shared correctly")
			}
			return nil
		}

		_ = registry.Register(hook1)
		_ = registry.Register(hook2)

		env := NewEnvironment("test-request")
		err := registry.Execute(context.Background(), PhasePreRouting, env)

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})
}

// TestHookRegistry_GetHookByName 测试按名称获取Hook
func TestHookRegistry_GetHookByName(t *testing.T) {
	registry := NewHookRegistry(nil, nil, &NoOpLogger{})
	hook := NewMockHook("test-hook", PhasePreRouting, 10)
	_ = registry.Register(hook)

	t.Run("existing hook", func(t *testing.T) {
		found, exists := registry.GetHookByName("test-hook")
		if !exists {
			t.Fatal("expected hook to exist")
		}
		if found.Name() != "test-hook" {
			t.Errorf("expected hook name 'test-hook', got %s", found.Name())
		}
	})

	t.Run("non-existing hook", func(t *testing.T) {
		_, exists := registry.GetHookByName("non-existing")
		if exists {
			t.Error("expected hook not to exist")
		}
	})
}

// TestHookRegistry_ReloadConfig 测试配置重载
func TestHookRegistry_ReloadConfig(t *testing.T) {
	t.Run("reload with configurable hook", func(t *testing.T) {
		cm := NewNoOpConfigManager()
		registry := NewHookRegistry(cm, nil, &NoOpLogger{})

		hook := NewMockConfigurableHook("test-hook", PhasePreRouting, 10)
		_ = registry.Register(hook)

		err := registry.ReloadConfig()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if !hook.configChanged {
			t.Error("expected config change to be called")
		}
	})

	t.Run("reload without config manager", func(t *testing.T) {
		registry := NewHookRegistry(nil, nil, &NoOpLogger{})
		err := registry.ReloadConfig()
		if err != nil {
			t.Fatalf("expected no error when no config manager, got %v", err)
		}
	})
}

// TestEnvironment 测试Environment结构
func TestEnvironment(t *testing.T) {
	t.Run("new environment", func(t *testing.T) {
		env := NewEnvironment("test-request")

		if env.RequestID != "test-request" {
			t.Errorf("expected request ID 'test-request', got %s", env.RequestID)
		}
		if env.Metadata == nil {
			t.Error("expected metadata to be initialized")
		}
		if env.StartTime.IsZero() {
			t.Error("expected start time to be set")
		}
	})

	t.Run("should continue", func(t *testing.T) {
		env := NewEnvironment("test-request")

		if !env.ShouldContinue() {
			t.Error("expected should continue to be true")
		}

		env.SetSkip()
		if env.ShouldContinue() {
			t.Error("expected should continue to be false after skip")
		}
	})

	t.Run("set abort", func(t *testing.T) {
		env := NewEnvironment("test-request")
		env.SetAbort("test reason")

		if !env.Abort {
			t.Error("expected abort to be true")
		}
		if env.AbortReason != "test reason" {
			t.Errorf("expected abort reason 'test reason', got %s", env.AbortReason)
		}
	})

	t.Run("session integration", func(t *testing.T) {
		env := NewEnvironment("test-request")
		sess := &session.Session{
			SessionKey: "test-key",
			TenantID:   "test-tenant",
		}
		env.Session = sess

		if env.Session.SessionKey != "test-key" {
			t.Errorf("expected session key 'test-key', got %s", env.Session.SessionKey)
		}
	})
}

// TestPhase 测试Phase类型
func TestPhase(t *testing.T) {
	t.Run("valid phases", func(t *testing.T) {
		validPhases := []Phase{
			PhasePreRouting,
			PhaseRouting,
			PhasePreUpstream,
			PhasePostUpstream,
			PhasePostResponse,
		}

		for _, phase := range validPhases {
			if !phase.IsValid() {
				t.Errorf("expected phase %s to be valid", phase)
			}
		}
	})

	t.Run("invalid phase", func(t *testing.T) {
		invalidPhase := Phase("invalid")
		if invalidPhase.IsValid() {
			t.Error("expected invalid phase to be invalid")
		}
	})

	t.Run("phase string", func(t *testing.T) {
		phase := PhasePreRouting
		if phase.String() != "pre_routing" {
			t.Errorf("expected 'pre_routing', got %s", phase.String())
		}
	})
}

// TestHookTimeout 测试Hook超时控制
func TestHookTimeout(t *testing.T) {
	t.Run("hook execution timeout", func(t *testing.T) {
		cm := NewNoOpConfigManager()
		registry := NewHookRegistry(cm, NewNoOpMetricsCollector(), &NoOpLogger{})

		hook := NewMockHook("slow-hook", PhasePreRouting, 10)
		hook.executeFunc = func(ctx context.Context, env *Environment) error {
			// 模拟慢操作
			time.Sleep(10 * time.Second)
			return nil
		}
		_ = registry.Register(hook)

		env := NewEnvironment("test-request")
		ctx := context.Background()

		start := time.Now()
		err := registry.Execute(ctx, PhasePreRouting, env)
		duration := time.Since(start)

		// 应该在超时时间内返回错误
		if duration > 6*time.Second {
			t.Errorf("expected timeout to trigger within 6 seconds, took %v", duration)
		}
		if err == nil {
			t.Error("expected timeout error")
		}
	})
}

// BenchmarkHookExecution Hook执行性能测试
func BenchmarkHookExecution(b *testing.B) {
	registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})

	// 注册5个轻量级Hook
	for i := 0; i < 5; i++ {
		hook := NewMockHook(fmt.Sprintf("hook-%d", i), PhasePreRouting, i*10)
		_ = registry.Register(hook)
	}

	env := NewEnvironment("bench-request")
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = registry.Execute(ctx, PhasePreRouting, env)
	}
}

// TestConfigManager 测试配置管理器
func TestConfigManager(t *testing.T) {
	t.Run("load config file", func(t *testing.T) {
		// 创建临时配置文件
		tmpFile := "/tmp/test_hooks.yaml"
		content := `hooks:
  - name: test-hook
    enabled: true
    priority: 10
    phase: pre_routing
    timeout: 3s
    config:
      key1: value1
`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		config := cm.GetHookConfig("test-hook")
		if config == nil {
			t.Fatal("expected config to exist")
		}
		if config["key1"] != "value1" {
			t.Errorf("expected key1=value1, got %v", config["key1"])
		}
	})

	t.Run("get hook timeout", func(t *testing.T) {
		tmpFile := "/tmp/test_hooks2.yaml"
		content := `hooks:
  - name: test-hook
    timeout: 10s
`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatal(err)
		}

		timeout := cm.GetHookTimeout("test-hook")
		if timeout != 10*time.Second {
			t.Errorf("expected 10s, got %v", timeout)
		}

		// 测试默认超时
		defaultTimeout := cm.GetHookTimeout("non-existing")
		if defaultTimeout != 5*time.Second {
			t.Errorf("expected 5s default, got %v", defaultTimeout)
		}
	})

	t.Run("is hook enabled", func(t *testing.T) {
		tmpFile := "/tmp/test_hooks3.yaml"
		content := `hooks:
  - name: enabled-hook
    enabled: true
  - name: disabled-hook
    enabled: false
`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatal(err)
		}

		if !cm.IsHookEnabled("enabled-hook") {
			t.Error("expected enabled-hook to be enabled")
		}
		if cm.IsHookEnabled("disabled-hook") {
			t.Error("expected disabled-hook to be disabled")
		}
		// 默认启用
		if !cm.IsHookEnabled("non-existing") {
			t.Error("expected non-existing hook to be enabled by default")
		}
	})

	t.Run("non-existent config file", func(t *testing.T) {
		cm, err := NewConfigManager("/tmp/non_existent_file.yaml")
		// 应该创建成功但使用空配置
		if err != nil {
			t.Fatalf("expected no error for non-existent file, got %v", err)
		}
		if cm == nil {
			t.Fatal("expected config manager to be created")
		}
		// 验证返回默认值
		if !cm.IsHookEnabled("any-hook") {
			t.Error("expected default enabled to be true")
		}
	})

	t.Run("invalid yaml format", func(t *testing.T) {
		tmpFile := "/tmp/test_invalid.yaml"
		content := `invalid: yaml: content: [[[`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		_, err := NewConfigManager(tmpFile)
		if err == nil {
			t.Fatal("expected error for invalid yaml")
		}
	})

	t.Run("get all hooks", func(t *testing.T) {
		tmpFile := "/tmp/test_hooks4.yaml"
		content := `hooks:
  - name: hook1
    enabled: true
  - name: hook2
    enabled: false
`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatal(err)
		}

		hooks := cm.GetAllHooks()
		if len(hooks) != 2 {
			t.Errorf("expected 2 hooks, got %d", len(hooks))
		}
	})

	t.Run("stop config manager", func(t *testing.T) {
		cm := NewNoOpConfigManager()
		cm.Stop() // 应该不会panic
	})
}

// TestMetricsCollector 测试指标收集器
func TestMetricsCollector(t *testing.T) {
	t.Run("prometheus metrics collector", func(t *testing.T) {
		mc := NewMetricsCollector()
		
		// 这些方法不应该panic
		mc.RecordHookExecution("test-hook", PhasePreRouting, 100*time.Millisecond, true)
		mc.RecordHookExecution("test-hook", PhasePreRouting, 200*time.Millisecond, false)
		mc.RecordHookFailure("test-hook", PhasePreRouting, "timeout")
		mc.RecordHookSkipped("test-hook", PhasePreRouting)
		mc.RecordHookTimeout("test-hook", PhasePreRouting)
	})

	t.Run("noop metrics collector", func(t *testing.T) {
		mc := NewNoOpMetricsCollector()
		
		// 这些方法不应该panic
		mc.RecordHookExecution("test-hook", PhasePreRouting, 100*time.Millisecond, true)
		mc.RecordHookFailure("test-hook", PhasePreRouting, "error")
		mc.RecordHookSkipped("test-hook", PhasePreRouting)
		mc.RecordHookTimeout("test-hook", PhasePreRouting)
	})
}

// TestNoOpLogger 测试无操作日志记录器
func TestNoOpLogger(t *testing.T) {
	logger := &NoOpLogger{}
	
	// 这些方法不应该panic
	logger.Info("test")
	logger.Error("test")
	logger.Debug("test")
	logger.Warn("test")
}

// TestHookRegistryWithConfigManager 测试带配置管理器的Registry
func TestHookRegistryWithConfigManager(t *testing.T) {
	t.Run("use config timeout", func(t *testing.T) {
		tmpFile := "/tmp/test_registry_config.yaml"
		content := `hooks:
  - name: fast-hook
    timeout: 100ms
`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatal(err)
		}

		registry := NewHookRegistry(cm, NewNoOpMetricsCollector(), &NoOpLogger{})
		
		hook := NewMockHook("fast-hook", PhasePreRouting, 10)
		hook.executeFunc = func(ctx context.Context, env *Environment) error {
			time.Sleep(200 * time.Millisecond)
			return nil
		}
		_ = registry.Register(hook)

		env := NewEnvironment("test-request")
		err = registry.Execute(context.Background(), PhasePreRouting, env)
		
		// 应该超时
		if err == nil {
			t.Error("expected timeout error")
		}
	})
}

// TestHookPanic 测试Hook panic恢复
func TestHookPanic(t *testing.T) {
	registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})
	
	hook := NewMockHook("panic-hook", PhasePostResponse, 10)
	hook.executeFunc = func(ctx context.Context, env *Environment) error {
		panic("test panic")
	}
	_ = registry.Register(hook)

	env := NewEnvironment("test-request")
	err := registry.Execute(context.Background(), PhasePostResponse, env)

	// 应该捕获panic并返回错误，但不会中断（非关键阶段）
	if err != nil {
		t.Logf("Got expected error: %v", err)
	}
}

// TestMultiplePhases 测试多个阶段的Hook
func TestMultiplePhases(t *testing.T) {
	registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})
	
	hook1 := NewMockHook("hook1", PhasePreRouting, 10)
	hook2 := NewMockHook("hook2", PhaseRouting, 10)
	hook3 := NewMockHook("hook3", PhasePreUpstream, 10)
	hook4 := NewMockHook("hook4", PhasePostUpstream, 10)
	hook5 := NewMockHook("hook5", PhasePostResponse, 10)
	
	_ = registry.Register(hook1)
	_ = registry.Register(hook2)
	_ = registry.Register(hook3)
	_ = registry.Register(hook4)
	_ = registry.Register(hook5)

	env := NewEnvironment("test-request")
	
	// 执行所有阶段
	phases := []Phase{
		PhasePreRouting,
		PhaseRouting,
		PhasePreUpstream,
		PhasePostUpstream,
		PhasePostResponse,
	}
	
	for _, phase := range phases {
		err := registry.Execute(context.Background(), phase, env)
		if err != nil {
			t.Errorf("unexpected error in phase %s: %v", phase, err)
		}
	}
	
	// 验证所有Hook都执行了
	if !hook1.WasExecuted() || !hook2.WasExecuted() || !hook3.WasExecuted() || 
	   !hook4.WasExecuted() || !hook5.WasExecuted() {
		t.Error("not all hooks were executed")
	}
}

// TestEnvironmentFields 测试Environment的所有字段
func TestEnvironmentFields(t *testing.T) {
	env := NewEnvironment("req-123")
	
	env.TenantID = "tenant-1"
	env.SessionKey = "session-1"
	env.TaskID = "task-1"
	env.Request = "test-request"
	env.Response = "test-response"
	env.UpstreamRequest = "upstream-request"
	env.UpstreamResponse = "upstream-response"
	
	if env.TenantID != "tenant-1" {
		t.Error("TenantID not set correctly")
	}
	if env.SessionKey != "session-1" {
		t.Error("SessionKey not set correctly")
	}
	if env.TaskID != "task-1" {
		t.Error("TaskID not set correctly")
	}
}

// TestInvalidPhaseRegistration 测试无效Phase注册
func TestInvalidPhaseRegistration(t *testing.T) {
	registry := NewHookRegistry(nil, nil, &NoOpLogger{})
	
	hook := &MockHook{
		name:     "invalid-hook",
		priority: 10,
		enabled:  true,
		phase:    Phase("invalid_phase"),
	}
	
	err := registry.Register(hook)
	if err == nil {
		t.Fatal("expected error for invalid phase")
	}
}

// TestConfigManagerWatch 测试配置热更新监控
func TestConfigManagerWatch(t *testing.T) {
	t.Run("watch config file changes", func(t *testing.T) {
		tmpFile := "/tmp/test_watch.yaml"
		content := `hooks:
  - name: test-hook
    enabled: true
`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatal(err)
		}
		defer cm.Stop()

		callbackCalled := false
		callback := func() {
			callbackCalled = true
		}

		err = cm.Watch(callback)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		// 修改文件
		time.Sleep(100 * time.Millisecond)
		newContent := `hooks:
  - name: test-hook
    enabled: false
`
		if err := os.WriteFile(tmpFile, []byte(newContent), 0644); err != nil {
			t.Fatal(err)
		}

		// 等待监控检测到变化
		time.Sleep(4 * time.Second)

		if !callbackCalled {
			t.Error("expected callback to be called")
		}
	})

	t.Run("watch already watching", func(t *testing.T) {
		tmpFile := "/tmp/test_watch2.yaml"
		content := `hooks: []`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatal(err)
		}
		defer cm.Stop()

		_ = cm.Watch(nil)
		err = cm.Watch(nil)
		if err == nil {
			t.Fatal("expected error when already watching")
		}
	})

	t.Run("stop watching", func(t *testing.T) {
		tmpFile := "/tmp/test_watch3.yaml"
		content := `hooks: []`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatal(err)
		}

		_ = cm.Watch(nil)
		cm.Stop()

		// 停止后应该可以再次Watch
		time.Sleep(100 * time.Millisecond)
		err = cm.Watch(nil)
		if err != nil {
			t.Fatalf("expected no error after stop, got %v", err)
		}
		cm.Stop()
	})
}

// TestNoOpConfigManagerMethods 测试NoOpConfigManager的所有方法
func TestNoOpConfigManagerMethods(t *testing.T) {
	cm := NewNoOpConfigManager()
	
	if err := cm.Load(); err != nil {
		t.Error("NoOpConfigManager.Load should not error")
	}
	
	if config := cm.GetHookConfig("any"); config != nil {
		t.Error("expected nil config")
	}
	
	if timeout := cm.GetHookTimeout("any"); timeout != 5*time.Second {
		t.Errorf("expected 5s, got %v", timeout)
	}
	
	if !cm.IsHookEnabled("any") {
		t.Error("expected true")
	}
	
	if err := cm.Watch(nil); err != nil {
		t.Error("NoOpConfigManager.Watch should not error")
	}
	
	cm.Stop() // should not panic
}

// TestNoOpMetricsCollectorMethods 测试NoOpMetricsCollector的所有方法
func TestNoOpMetricsCollectorMethods(t *testing.T) {
	mc := NewNoOpMetricsCollector()
	
	// 所有方法都不应该panic
	mc.RecordHookExecution("hook", PhasePreRouting, time.Second, true)
	mc.RecordHookExecution("hook", PhasePreRouting, time.Second, false)
	mc.RecordHookFailure("hook", PhasePreRouting, "error")
	mc.RecordHookSkipped("hook", PhasePreRouting)
	mc.RecordHookTimeout("hook", PhasePreRouting)
}

// TestNoOpLoggerMethods 测试NoOpLogger的所有方法
func TestNoOpLoggerMethods(t *testing.T) {
	logger := &NoOpLogger{}
	
	// 所有方法都不应该panic
	logger.Info("msg", "key", "value")
	logger.Error("msg", "key", "value")
	logger.Debug("msg", "key", "value")
	logger.Warn("msg", "key", "value")
}

// TestNewHookRegistryWithNilParams 测试nil参数的Registry创建
func TestNewHookRegistryWithNilParams(t *testing.T) {
	// 所有参数为nil
	registry := NewHookRegistry(nil, nil, nil)
	if registry == nil {
		t.Fatal("expected registry to be created")
	}
	
	// 应该使用默认值
	hook := NewMockHook("test", PhasePreRouting, 10)
	err := registry.Register(hook)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestReloadConfigWithError 测试配置重载错误处理
func TestReloadConfigWithError(t *testing.T) {
	t.Run("reload with load error", func(t *testing.T) {
		tmpFile := "/tmp/test_reload_error.yaml"
		content := `hooks: []`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatal(err)
		}

		// 删除文件，使Load失败
		os.Remove(tmpFile)

		registry := NewHookRegistry(cm, nil, &NoOpLogger{})
		err = registry.ReloadConfig()
		
		if err == nil {
			t.Error("expected error when config file is missing")
		}
	})

	t.Run("reload with configurable hook error", func(t *testing.T) {
		tmpFile := "/tmp/test_reload_hook_error.yaml"
		content := `hooks:
  - name: error-hook
    config:
      key: value
`
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpFile)

		cm, err := NewConfigManager(tmpFile)
		if err != nil {
			t.Fatal(err)
		}

		registry := NewHookRegistry(cm, nil, &NoOpLogger{})
		
		hook := NewMockConfigurableHook("error-hook", PhasePreRouting, 10)
		hook.configChangeFunc = func(config map[string]interface{}) error {
			return fmt.Errorf("config change error")
		}
		_ = registry.Register(hook)

		// 应该记录错误但不返回错误
		err = registry.ReloadConfig()
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})
}

// TestGetHookConfigCopy 测试配置返回副本
func TestGetHookConfigCopy(t *testing.T) {
	tmpFile := "/tmp/test_config_copy.yaml"
	content := `hooks:
  - name: test-hook
    config:
      key1: value1
`
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile)

	cm, err := NewConfigManager(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	config1 := cm.GetHookConfig("test-hook")
	config2 := cm.GetHookConfig("test-hook")

	// 修改config1不应该影响config2
	config1["key2"] = "value2"

	if _, ok := config2["key2"]; ok {
		t.Error("modifying one config should not affect another (not a copy)")
	}
}

// TestGetHooksCopy 测试GetHooks返回副本
func TestGetHooksCopy(t *testing.T) {
	registry := NewHookRegistry(nil, nil, &NoOpLogger{})
	hook := NewMockHook("test", PhasePreRouting, 10)
	_ = registry.Register(hook)

	hooks1 := registry.GetHooks(PhasePreRouting)
	hooks2 := registry.GetHooks(PhasePreRouting)

	// 修改hooks1不应该影响hooks2
	if len(hooks1) > 0 {
		hooks1[0] = nil
	}

	if len(hooks2) == 0 || hooks2[0] == nil {
		t.Error("modifying one slice should not affect another (not a copy)")
	}
}

// TestEmptyPhaseExecution 测试执行空Phase
func TestEmptyPhaseExecution(t *testing.T) {
	registry := NewHookRegistry(nil, nil, &NoOpLogger{})
	env := NewEnvironment("test")

	// 执行没有Hook的Phase
	err := registry.Execute(context.Background(), PhaseRouting, env)
	if err != nil {
		t.Errorf("expected no error for empty phase, got %v", err)
	}
}

// TestContextCancellation 测试Context取消
func TestContextCancellation(t *testing.T) {
	registry := NewHookRegistry(nil, NewNoOpMetricsCollector(), &NoOpLogger{})
	
	hook := NewMockHook("slow-hook", PhasePreRouting, 10)
	hook.executeFunc = func(ctx context.Context, env *Environment) error {
		select {
		case <-time.After(10 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	_ = registry.Register(hook)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	env := NewEnvironment("test")
	err := registry.Execute(ctx, PhasePreRouting, env)

	if err == nil {
		t.Error("expected error for cancelled context")
	}
}
