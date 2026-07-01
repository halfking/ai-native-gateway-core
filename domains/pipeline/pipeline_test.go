package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// mockHook 测试用 Hook
type mockHook struct {
	name        string
	priority    int
	enabled     bool
	executeErr  error
	onErrorRet  error
	executeFunc func(ctx context.Context, env *domain.PipelineRequest) error

	executeCalls int32
	onErrorCalls int32
}

func (h *mockHook) Name() string  { return h.name }
func (h *mockHook) Priority() int { return h.priority }
func (h *mockHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return h.enabled
}

func (h *mockHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	atomic.AddInt32(&h.executeCalls, 1)
	if h.executeFunc != nil {
		return h.executeFunc(ctx, env)
	}
	return h.executeErr
}

func (h *mockHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	atomic.AddInt32(&h.onErrorCalls, 1)
	return h.onErrorRet
}

func newMockHook(name string, priority int) *mockHook {
	return &mockHook{
		name:     name,
		priority: priority,
		enabled:  true,
	}
}

// Test 1: 基本执行
func TestRequestPipeline_Execute(t *testing.T) {
	p := NewRequestPipeline()
	hook := newMockHook("test", 100)

	p.AddStage(&PipelineStage{
		Name:  "stage1",
		Phase: PhaseAuthentication,
		Mode:  ModeSequential,
		Hooks: []Hook{hook},
	})

	env := domain.NewRequestEnvelope(context.Background(), nil)
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if atomic.LoadInt32(&hook.executeCalls) != 1 {
		t.Errorf("expected 1 execute call, got %d", hook.executeCalls)
	}
}

// Test 2: 串行执行 + 顺序
func TestRequestPipeline_SequentialOrder(t *testing.T) {
	p := NewRequestPipeline()

	var order []string
	var mu sync.Mutex

	hooks := []Hook{
		&mockHook{
			name:     "second",
			priority: 200,
			enabled:  true,
			executeFunc: func(ctx context.Context, env *domain.PipelineRequest) error {
				mu.Lock()
				order = append(order, "second")
				mu.Unlock()
				return nil
			},
		},
		&mockHook{
			name:     "first",
			priority: 100,
			enabled:  true,
			executeFunc: func(ctx context.Context, env *domain.PipelineRequest) error {
				mu.Lock()
				order = append(order, "first")
				mu.Unlock()
				return nil
			},
		},
		&mockHook{
			name:     "third",
			priority: 300,
			enabled:  true,
			executeFunc: func(ctx context.Context, env *domain.PipelineRequest) error {
				mu.Lock()
				order = append(order, "third")
				mu.Unlock()
				return nil
			},
		},
	}

	p.AddStage(&PipelineStage{
		Name:  "sequential",
		Phase: PhasePreRouting,
		Mode:  ModeSequential,
		Hooks: hooks,
	})

	env := domain.NewRequestEnvelope(context.Background(), nil)
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	expected := []string{"first", "second", "third"}
	if len(order) != 3 {
		t.Fatalf("expected 3 hooks called, got %d", len(order))
	}
	for i, exp := range expected {
		if order[i] != exp {
			t.Errorf("at position %d: expected %q, got %q", i, exp, order[i])
		}
	}
}

// Test 3: 并行执行
func TestRequestPipeline_Parallel(t *testing.T) {
	p := NewRequestPipeline()

	var started int32
	var maxConcurrent int32

	hooks := make([]Hook, 5)
	for i := 0; i < 5; i++ {
		i := i
		hooks[i] = &mockHook{
			name:     fmt.Sprintf("hook-%d", i),
			priority: 100 + i,
			enabled:  true,
			executeFunc: func(ctx context.Context, env *domain.PipelineRequest) error {
				cur := atomic.AddInt32(&started, 1)
				// 追踪最大并发
				for {
					old := atomic.LoadInt32(&maxConcurrent)
					if cur > old {
						if atomic.CompareAndSwapInt32(&maxConcurrent, old, cur) {
							break
						}
					} else {
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
				atomic.AddInt32(&started, -1)
				return nil
			},
		}
	}

	p.AddStage(&PipelineStage{
		Name:  "parallel",
		Phase: PhasePreRouting,
		Mode:  ModeParallel,
		Hooks: hooks,
	})

	env := domain.NewRequestEnvelope(context.Background(), nil)
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if got := atomic.LoadInt32(&maxConcurrent); got < 2 {
		t.Errorf("expected parallel execution (max concurrent >= 2), got %d", got)
	}
}

// Test 4: Hook 过滤（Enabled=false 跳过）
func TestRequestPipeline_HookFiltering(t *testing.T) {
	p := NewRequestPipeline()

	enabled := newMockHook("enabled", 100)
	disabled := newMockHook("disabled", 200)
	disabled.enabled = false

	p.AddStage(&PipelineStage{
		Name:  "stage",
		Phase: PhaseAuthentication,
		Mode:  ModeSequential,
		Hooks: []Hook{enabled, disabled},
	})

	env := domain.NewRequestEnvelope(context.Background(), nil)
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if atomic.LoadInt32(&enabled.executeCalls) != 1 {
		t.Errorf("expected enabled hook called 1 time, got %d", enabled.executeCalls)
	}
	if atomic.LoadInt32(&disabled.executeCalls) != 0 {
		t.Errorf("expected disabled hook called 0 times, got %d", disabled.executeCalls)
	}
}

// Test 5: 错误处理（OnError 传递）
func TestRequestPipeline_ErrorPropagation(t *testing.T) {
	p := NewRequestPipeline()

	errHook := &mockHook{
		name:       "err",
		priority:   100,
		enabled:    true,
		executeErr: errors.New("hook error"),
		onErrorRet: errors.New("propagated"),
	}

	p.AddStage(&PipelineStage{
		Name:  "stage",
		Phase: PhaseAuthentication,
		Mode:  ModeSequential,
		Hooks: []Hook{errHook},
	})

	env := domain.NewRequestEnvelope(context.Background(), nil)
	err := p.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected error from Execute")
	}
	if atomic.LoadInt32(&errHook.onErrorCalls) != 1 {
		t.Errorf("expected OnError called 1 time, got %d", errHook.onErrorCalls)
	}
}

// Test 6: OnError 吞掉错误
func TestRequestPipeline_OnErrorSwallows(t *testing.T) {
	p := NewRequestPipeline()

	errHook := &mockHook{
		name:       "err",
		priority:   100,
		enabled:    true,
		executeErr: errors.New("hook error"),
		onErrorRet: nil, // 吞掉
	}

	nextHook := newMockHook("next", 200)

	p.AddStage(&PipelineStage{
		Name:  "stage",
		Phase: PhaseAuthentication,
		Mode:  ModeSequential,
		Hooks: []Hook{errHook, nextHook},
	})

	env := domain.NewRequestEnvelope(context.Background(), nil)
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute should succeed when OnError swallows: %v", err)
	}

	if atomic.LoadInt32(&nextHook.executeCalls) != 1 {
		t.Errorf("expected next hook to be called after OnError swallow, got %d calls", nextHook.executeCalls)
	}
}

// Test 7: 多阶段顺序执行
func TestRequestPipeline_MultiStage(t *testing.T) {
	p := NewRequestPipeline()

	stage1Hook := newMockHook("stage1", 100)
	stage2Hook := newMockHook("stage2", 100)

	p.AddStage(&PipelineStage{
		Name:  "auth",
		Phase: PhaseAuthentication,
		Mode:  ModeSequential,
		Hooks: []Hook{stage1Hook},
	})
	p.AddStage(&PipelineStage{
		Name:  "routing",
		Phase: PhaseRouting,
		Mode:  ModeSequential,
		Hooks: []Hook{stage2Hook},
	})

	env := domain.NewRequestEnvelope(context.Background(), nil)
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if atomic.LoadInt32(&stage1Hook.executeCalls) != 1 {
		t.Errorf("stage1 hook should be called once")
	}
	if atomic.LoadInt32(&stage2Hook.executeCalls) != 1 {
		t.Errorf("stage2 hook should be called once")
	}

	if got := len(p.Stages()); got != 2 {
		t.Errorf("expected 2 stages, got %d", got)
	}
}

// Test 8: 阶段失败中断后续
func TestRequestPipeline_StageFailureStops(t *testing.T) {
	p := NewRequestPipeline()

	failHook := &mockHook{
		name:       "fail",
		priority:   100,
		enabled:    true,
		executeErr: errors.New("boom"),
		onErrorRet: errors.New("propagated"),
	}
	afterHook := newMockHook("after", 100)

	p.AddStage(&PipelineStage{
		Name:  "fail-stage",
		Phase: PhaseRouting,
		Mode:  ModeSequential,
		Hooks: []Hook{failHook},
	})
	p.AddStage(&PipelineStage{
		Name:  "after-stage",
		Phase: PhaseTransform,
		Mode:  ModeSequential,
		Hooks: []Hook{afterHook},
	})

	env := domain.NewRequestEnvelope(context.Background(), nil)
	err := p.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected error")
	}

	if atomic.LoadInt32(&afterHook.executeCalls) != 0 {
		t.Error("after hook should not be called when previous stage fails")
	}
}

// Test 9: AllPhases returns the canonical phase order (PR-V4-02: 16 phases).
func TestAllPhases(t *testing.T) {
	phases := AllPhases()
	if len(phases) != 16 {
		t.Errorf("expected 16 phases, got %d", len(phases))
	}

	// 检查顺序
	expected := []Phase{
		PhasePreAuthentication, PhaseAuthentication, PhasePostAuthentication,
		PhasePreRouting, PhaseRouting, PhasePostRouting,
		PhaseGovernance,
		PhasePreTransform, PhaseTransform, PhasePostTransform,
		PhasePreUpstream, PhaseUpstream, PhasePostUpstream,
		PhasePreResponse, PhaseResponse, PhasePostResponse,
	}
	for i, p := range expected {
		if phases[i] != p {
			t.Errorf("at %d: expected %s, got %s", i, p, phases[i])
		}
	}
}

// Test 10: nil envelope
func TestRequestPipeline_NilEnvelope(t *testing.T) {
	p := NewRequestPipeline()
	p.AddStage(&PipelineStage{
		Name:  "stage",
		Phase: PhaseAuthentication,
		Mode:  ModeSequential,
		Hooks: []Hook{newMockHook("h", 100)},
	})

	err := p.Execute(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil envelope")
	}
}

// Test 11: AddStage nil 安全
func TestRequestPipeline_AddStage_NilIgnored(t *testing.T) {
	p := NewRequestPipeline()
	p.AddStage(nil)
	if len(p.Stages()) != 0 {
		t.Fatal("nil stage should be ignored")
	}
}

// Test 12: 并行失败取消其他 hook
func TestRequestPipeline_ParallelOneHookFailsCancelsOthers(t *testing.T) {
	p := NewRequestPipeline()
	var sawCancel int32

	hooks := []Hook{
		&mockHook{
			name:       "fast-fail",
			priority:   100,
			enabled:    true,
			executeErr: errors.New("boom"),
		},
		&mockHook{
			name:     "slow",
			priority: 200,
			enabled:  true,
			executeFunc: func(ctx context.Context, env *domain.PipelineRequest) error {
				select {
				case <-ctx.Done():
					atomic.StoreInt32(&sawCancel, 1)
				case <-time.After(200 * time.Millisecond):
				}
				return nil
			},
		},
	}

	p.AddStage(&PipelineStage{
		Name:  "p",
		Phase: PhaseRouting,
		Mode:  ModeParallel,
		Hooks: hooks,
	})

	env := domain.NewRequestEnvelope(context.Background(), nil)
	_ = p.Execute(context.Background(), env)
	if atomic.LoadInt32(&sawCancel) != 1 {
		t.Error("expected slow hook to observe cancellation")
	}
}
