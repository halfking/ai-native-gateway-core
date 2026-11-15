package reconciler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor"
)

// TestBackoffTracker_ExponentialPolicy 验证指数退避时间序列
func TestBackoffTracker_ExponentialPolicy(t *testing.T) {
	cfg := BackoffConfig{
		Policy:      BackoffExponential,
		InitialWait: 30 * time.Second,
		MaxWait:     10 * time.Minute,
		MaxRetries:  10,
	}
	bt := NewBackoffTracker(cfg)

	expected := []time.Duration{
		30 * time.Second,  // retry 1: 30s * 2^0
		60 * time.Second,  // retry 2: 30s * 2^1
		120 * time.Second, // retry 3: 30s * 2^2
		240 * time.Second, // retry 4: 30s * 2^3
		480 * time.Second, // retry 5: 30s * 2^4
		600 * time.Second, // retry 6+: capped at MaxWait
	}

	for i, want := range expected {
		got := bt.calculateWait(i + 1)
		if got != want {
			t.Errorf("ExponentialPolicy retry %d: got %v, want %v", i+1, got, want)
		}
	}
}

// TestBackoffTracker_LinearPolicy 验证线性退避时间序列
func TestBackoffTracker_LinearPolicy(t *testing.T) {
	cfg := BackoffConfig{
		Policy:      BackoffLinear,
		InitialWait: 30 * time.Second,
		MaxWait:     5 * time.Minute,
		MaxRetries:  10,
	}
	bt := NewBackoffTracker(cfg)

	expected := []time.Duration{
		30 * time.Second,  // retry 1: 30s * 1
		60 * time.Second,  // retry 2: 30s * 2
		90 * time.Second,  // retry 3: 30s * 3
		120 * time.Second, // retry 4: 30s * 4
		150 * time.Second, // retry 5: 30s * 5
		180 * time.Second, // retry 6: 30s * 6
		210 * time.Second, // retry 7: 30s * 7
		240 * time.Second, // retry 8: 30s * 8
		270 * time.Second, // retry 9: 30s * 9
		300 * time.Second, // retry 10+: capped at MaxWait (5min)
	}

	for i, want := range expected {
		got := bt.calculateWait(i + 1)
		if got != want {
			t.Errorf("LinearPolicy retry %d: got %v, want %v", i+1, got, want)
		}
	}
}

// PLACEHOLDER_FOR_FIBONACCI_TEST

// TestBackoffTracker_FibonacciPolicy 验证斐波那契退避时间序列
func TestBackoffTracker_FibonacciPolicy(t *testing.T) {
	cfg := BackoffConfig{
		Policy:      BackoffFibonacci,
		InitialWait: 30 * time.Second,
		MaxWait:     10 * time.Minute,
		MaxRetries:  10,
	}
	bt := NewBackoffTracker(cfg)

	// fib(1)=1, fib(2)=1, fib(3)=2, fib(4)=3, fib(5)=5, fib(6)=8, ...
	expected := []time.Duration{
		30 * time.Second,  // retry 1: 30s * fib(1)=1
		30 * time.Second,  // retry 2: 30s * fib(2)=1
		60 * time.Second,  // retry 3: 30s * fib(3)=2
		90 * time.Second,  // retry 4: 30s * fib(4)=3
		150 * time.Second, // retry 5: 30s * fib(5)=5
		240 * time.Second, // retry 6: 30s * fib(6)=8
		390 * time.Second, // retry 7: 30s * fib(7)=13
		600 * time.Second, // retry 8+: capped at MaxWait (fib(8)=21 * 30s = 630s > 600s)
	}

	for i, want := range expected {
		got := bt.calculateWait(i + 1)
		if got != want {
			t.Errorf("FibonacciPolicy retry %d: got %v, want %v", i+1, got, want)
		}
	}
}

// TestBackoffTracker_MaxWaitCap 验证 MaxWait 上限生效
func TestBackoffTracker_MaxWaitCap(t *testing.T) {
	cfg := BackoffConfig{
		Policy:      BackoffExponential,
		InitialWait: 1 * time.Minute,
		MaxWait:     2 * time.Minute, // 严格上限
		MaxRetries:  10,
	}
	bt := NewBackoffTracker(cfg)

	// retry 3: 1min * 2^2 = 4min，应被 cap 到 2min
	got := bt.calculateWait(3)
	if got != 2*time.Minute {
		t.Errorf("MaxWait cap failed: got %v, want 2m0s", got)
	}
}

// TestBackoffTracker_CanRetry_DuringBackoff 退避期内 CanRetry=false
func TestBackoffTracker_CanRetry_DuringBackoff(t *testing.T) {
	cfg := BackoffConfig{
		Policy:      BackoffExponential,
		InitialWait: 1 * time.Hour, // 1 小时退避
		MaxWait:     2 * time.Hour,
		MaxRetries:  10,
	}
	bt := NewBackoffTracker(cfg)

	pluginID := "test-plugin"

	// 首次失败，允许重试
	if !bt.CanRetry(pluginID) {
		t.Error("CanRetry should return true for first failure")
	}

	// 记录重试
	bt.RecordRetry(pluginID)

	// 立即检查，应仍在退避期（1 小时未过）
	if bt.CanRetry(pluginID) {
		t.Error("CanRetry should return false during backoff period")
	}
}

// TestBackoffTracker_CanRetry_AfterBackoff 退避期过后 CanRetry=true
func TestBackoffTracker_CanRetry_AfterBackoff(t *testing.T) {
	cfg := BackoffConfig{
		Policy:      BackoffExponential,
		InitialWait: 100 * time.Millisecond, // 短退避便于测试
		MaxWait:     1 * time.Second,
		MaxRetries:  10,
	}
	bt := NewBackoffTracker(cfg)

	pluginID := "test-plugin"
	bt.RecordRetry(pluginID)

	// 立即检查，仍在退避期
	if bt.CanRetry(pluginID) {
		t.Error("CanRetry should return false immediately after RecordRetry")
	}

	// 等待退避期过
	time.Sleep(150 * time.Millisecond)

	// 退避期过后，允许重试
	if !bt.CanRetry(pluginID) {
		t.Error("CanRetry should return true after backoff period expired")
	}
}

// PLACEHOLDER_FOR_MORE_TESTS

// TestBackoffTracker_RecordRetry_UpdatesState 验证 RecordRetry 正确更新状态
func TestBackoffTracker_RecordRetry_UpdatesState(t *testing.T) {
	cfg := BackoffConfig{
		Policy:      BackoffExponential,
		InitialWait: 30 * time.Second,
		MaxWait:     10 * time.Minute,
		MaxRetries:  10,
	}
	bt := NewBackoffTracker(cfg)

	pluginID := "test-plugin"
	startTime := time.Now()

	// 第一次重试
	bt.RecordRetry(pluginID)
	state := bt.GetState(pluginID)
	if state == nil {
		t.Fatal("GetState should return non-nil after RecordRetry")
	}
	if state.RetryCount != 1 {
		t.Errorf("RetryCount after 1st retry: got %d, want 1", state.RetryCount)
	}
	if state.NextRetryTime.Before(startTime.Add(29 * time.Second)) {
		t.Errorf("NextRetryTime too early: %v", state.NextRetryTime)
	}

	// 第二次重试
	bt.RecordRetry(pluginID)
	state = bt.GetState(pluginID)
	if state.RetryCount != 2 {
		t.Errorf("RetryCount after 2nd retry: got %d, want 2", state.RetryCount)
	}
}

// TestBackoffTracker_Reset_ClearsState 验证 Reset 清空状态
func TestBackoffTracker_Reset_ClearsState(t *testing.T) {
	cfg := defaultBackoffConfig()
	bt := NewBackoffTracker(cfg)

	pluginID := "test-plugin"
	bt.RecordRetry(pluginID)

	if bt.GetState(pluginID) == nil {
		t.Fatal("State should exist after RecordRetry")
	}

	bt.Reset(pluginID)

	if bt.GetState(pluginID) != nil {
		t.Error("State should be nil after Reset")
	}

	// Reset 后应允许立即重试
	if !bt.CanRetry(pluginID) {
		t.Error("CanRetry should return true after Reset")
	}
}

// TestBackoffTracker_MaxRetries_ReachesLimit 验证 MaxRetries 后永久禁用
func TestBackoffTracker_MaxRetries_ReachesLimit(t *testing.T) {
	cfg := BackoffConfig{
		Policy:      BackoffExponential,
		InitialWait: 1 * time.Millisecond, // 短退避便于测试
		MaxWait:     10 * time.Millisecond,
		MaxRetries:  3, // 限制 3 次
	}
	bt := NewBackoffTracker(cfg)

	pluginID := "test-plugin"

	// 前 3 次重试
	for i := 0; i < 3; i++ {
		if !bt.CanRetry(pluginID) {
			t.Fatalf("CanRetry should be true for retry %d", i+1)
		}
		bt.RecordRetry(pluginID)
		time.Sleep(15 * time.Millisecond) // 等待退避期过
	}

	// 第 4 次尝试，应被拒绝（达到 MaxRetries）
	if bt.CanRetry(pluginID) {
		t.Error("CanRetry should return false after MaxRetries reached")
	}

	state := bt.GetState(pluginID)
	if state.RetryCount != 3 {
		t.Errorf("RetryCount: got %d, want 3", state.RetryCount)
	}
}

// TestBackoffTracker_Concurrent 验证并发访问安全
func TestBackoffTracker_Concurrent(t *testing.T) {
	cfg := defaultBackoffConfig()
	bt := NewBackoffTracker(cfg)

	var wg sync.WaitGroup
	pluginCount := 10
	goroutinesPerPlugin := 5

	for i := 0; i < pluginCount; i++ {
		pluginID := string(rune('A' + i))
		for j := 0; j < goroutinesPerPlugin; j++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				bt.RecordRetry(id)
				bt.CanRetry(id)
				bt.GetState(id)
				bt.Reset(id)
			}(pluginID)
		}
	}

	wg.Wait()
	t.Log("Concurrent test passed without race")
}

// TestFibonacci 验证 fibonacci 函数计算正确
func TestFibonacci(t *testing.T) {
	tests := []struct {
		n    int
		want int
	}{
		{0, 1},  // 边界：fib(0)=1（调整后）
		{1, 1},  // fib(1)=1
		{2, 1},  // fib(2)=1
		{3, 2},  // fib(3)=2
		{4, 3},  // fib(4)=3
		{5, 5},  // fib(5)=5
		{6, 8},  // fib(6)=8
		{7, 13}, // fib(7)=13
		{8, 21}, // fib(8)=21
	}

	for _, tt := range tests {
		got := fibonacci(tt.n)
		if got != tt.want {
			t.Errorf("fibonacci(%d): got %d, want %d", tt.n, got, tt.want)
		}
	}
}

// TestDefaultBackoffConfig 验证默认配置值
func TestDefaultBackoffConfig(t *testing.T) {
	cfg := defaultBackoffConfig()

	if cfg.Policy != BackoffExponential {
		t.Errorf("Default policy: got %v, want %v", cfg.Policy, BackoffExponential)
	}
	if cfg.InitialWait != 30*time.Second {
		t.Errorf("Default InitialWait: got %v, want 30s", cfg.InitialWait)
	}
	if cfg.MaxWait != 10*time.Minute {
		t.Errorf("Default MaxWait: got %v, want 10m", cfg.MaxWait)
	}
	if cfg.MaxRetries != 10 {
		t.Errorf("Default MaxRetries: got %d, want 10", cfg.MaxRetries)
	}
}

// ── 2026-11-15 审计轮收口钉测（computeActions 集成面）──────────────────────
// 首版 D10 的缺陷：CanRetry=false 后 computeActions 只 continue——插件永远
// 停在 StateFailed，每 30s WARN 一次、永不 disable（crashTracker 1h 窗口
// 清零使 CrashCount>=MaxCrashCount 的 disable 门也不可达）。钉两条：
//
//	① 退避中（未耗尽）⇒ 零 action（不重启）
//	② 退避耗尽（RetryCount>=MaxRetries）⇒ ActionDisable 终态
func TestReconcile_BackoffExhausted_ProducesDisable(t *testing.T) {
	sup := newMockSupervisor()
	reg := newMockRegistry()

	reg.plugins["p1"] = &RegistryPlugin{ID: "p1", Name: "p1", Version: "1.0.0", Enabled: true}
	reg.manifests["p1"] = &discovery.Manifest{Metadata: discovery.Metadata{Name: "p1", Version: "1.0.0"}}

	config := &Config{Interval: 30 * time.Second, MaxCrashCount: 10, CrashResetWindow: 1 * time.Hour}
	r := NewReconciler(config, sup, reg).(*reconciler)

	// 插件处于 failed 态，且退避已耗尽（10 次 ≥ 默认 MaxRetries=10）。
	sup.processes["p1"] = &supervisor.ProcessState{PluginID: "p1", Status: supervisor.StatusCrashed}
	for i := 0; i < 10; i++ {
		r.backoffTracker.RecordRetry("p1")
	}

	desired, err := r.getDesiredStates(context.Background())
	if err != nil {
		t.Fatalf("getDesiredStates: %v", err)
	}
	actual, err := r.getActualStates(context.Background())
	if err != nil {
		t.Fatalf("getActualStates: %v", err)
	}

	actions := r.computeActions(desired, actual)
	if len(actions) != 1 || actions[0].Action != ActionDisable {
		t.Fatalf("退避耗尽必须产出 ActionDisable 终态, got %+v", actions)
	}
	if actions[0].PluginID != "p1" {
		t.Fatalf("disable 指向错误插件: %s", actions[0].PluginID)
	}
}

func TestReconcile_InBackoff_ProducesNoAction(t *testing.T) {
	sup := newMockSupervisor()
	reg := newMockRegistry()

	reg.plugins["p1"] = &RegistryPlugin{ID: "p1", Name: "p1", Version: "1.0.0", Enabled: true}
	reg.manifests["p1"] = &discovery.Manifest{Metadata: discovery.Metadata{Name: "p1", Version: "1.0.0"}}

	config := &Config{Interval: 30 * time.Second, MaxCrashCount: 10, CrashResetWindow: 1 * time.Hour}
	r := NewReconciler(config, sup, reg).(*reconciler)

	sup.processes["p1"] = &supervisor.ProcessState{PluginID: "p1", Status: supervisor.StatusCrashed}
	// 记录 1 次失败：指数退避 30s 内 CanRetry=false（退避中，未耗尽）。
	r.backoffTracker.RecordRetry("p1")

	desired, _ := r.getDesiredStates(context.Background())
	actual, _ := r.getActualStates(context.Background())

	actions := r.computeActions(desired, actual)
	if len(actions) != 0 {
		t.Fatalf("退避中不得产生任何 action（防重启风暴）, got %+v", actions)
	}
}
