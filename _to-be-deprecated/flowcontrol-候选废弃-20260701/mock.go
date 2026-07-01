package flowcontrol

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// InMemoryEventBus 内存事件总线（用于测试）
type InMemoryEventBus struct {
	handlers map[FlowEventType][]func(*FlowEvent)
	mu       sync.RWMutex
}

// NewInMemoryEventBus 创建内存事件总线
func NewInMemoryEventBus() *InMemoryEventBus {
	return &InMemoryEventBus{
		handlers: make(map[FlowEventType][]func(*FlowEvent)),
	}
}

// Publish 发布事件
func (b *InMemoryEventBus) Publish(ctx context.Context, event *FlowEvent) error {
	b.mu.RLock()
	handlers := b.handlers[event.Type]
	b.mu.RUnlock()
	
	for _, handler := range handlers {
		handler(event)
	}
	
	return nil
}

// Subscribe 订阅事件
func (b *InMemoryEventBus) Subscribe(eventType FlowEventType, handler func(*FlowEvent)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	b.handlers[eventType] = append(b.handlers[eventType], handler)
	return nil
}

// InMemoryPlanStore 内存流程计划存储（用于测试）
type InMemoryPlanStore struct {
	plans map[string]*FlowPlan
	mu    sync.RWMutex
}

// NewInMemoryPlanStore 创建内存流程计划存储
func NewInMemoryPlanStore() *InMemoryPlanStore {
	return &InMemoryPlanStore{
		plans: make(map[string]*FlowPlan),
	}
}

// Save 保存流程计划
func (s *InMemoryPlanStore) Save(ctx context.Context, plan *FlowPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	// 深拷贝（简化版）
	s.plans[plan.ID] = plan
	return nil
}

// Load 加载流程计划
func (s *InMemoryPlanStore) Load(ctx context.Context, flowID string) (*FlowPlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	plan, ok := s.plans[flowID]
	if !ok {
		return nil, errors.New("flow plan not found")
	}
	
	return plan, nil
}

// Delete 删除流程计划
func (s *InMemoryPlanStore) Delete(ctx context.Context, flowID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	delete(s.plans, flowID)
	return nil
}

// InMemoryExecutionLogger 内存执行日志（用于测试）
type InMemoryExecutionLogger struct {
	executions map[string]*FlowExecution
	stepLogs   map[string][]*StepExecution
	mu         sync.RWMutex
}

// NewInMemoryExecutionLogger 创建内存执行日志
func NewInMemoryExecutionLogger() *InMemoryExecutionLogger {
	return &InMemoryExecutionLogger{
		executions: make(map[string]*FlowExecution),
		stepLogs:   make(map[string][]*StepExecution),
	}
}

// Log 记录流程执行
func (l *InMemoryExecutionLogger) Log(ctx context.Context, execution *FlowExecution) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	
	l.executions[execution.FlowID] = execution
	return nil
}

// LogStep 记录步骤执行
func (l *InMemoryExecutionLogger) LogStep(ctx context.Context, flowID string, stepExec *StepExecution) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	
	l.stepLogs[flowID] = append(l.stepLogs[flowID], stepExec)
	return nil
}

// GetExecution 获取流程执行记录
func (l *InMemoryExecutionLogger) GetExecution(flowID string) *FlowExecution {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.executions[flowID]
}

// GetStepLogs 获取步骤执行日志
func (l *InMemoryExecutionLogger) GetStepLogs(flowID string) []*StepExecution {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.stepLogs[flowID]
}

// MockCacheManager 模拟缓存管理器
type MockCacheManager struct {
	data map[string][]byte
	mu   sync.RWMutex
}

// NewMockCacheManager 创建模拟缓存管理器
func NewMockCacheManager() *MockCacheManager {
	return &MockCacheManager{
		data: make(map[string][]byte),
	}
}

// Lookup 查找缓存
func (m *MockCacheManager) Lookup(ctx context.Context, key string) ([]byte, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	value, ok := m.data[key]
	return value, ok, nil
}

// Save 保存缓存
func (m *MockCacheManager) Save(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.data[key] = value
	return nil
}

// MockToolExecutor 模拟工具执行器
type MockToolExecutor struct {
	results map[string]map[string]any
	mu      sync.RWMutex
}

// NewMockToolExecutor 创建模拟工具执行器
func NewMockToolExecutor() *MockToolExecutor {
	return &MockToolExecutor{
		results: make(map[string]map[string]any),
	}
}

// Execute 执行工具
func (m *MockToolExecutor) Execute(ctx context.Context, toolName string, params map[string]any) (map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if result, ok := m.results[toolName]; ok {
		return result, nil
	}
	
	// 默认返回成功
	return map[string]any{"status": "success", "tool": toolName}, nil
}

// SetResult 设置工具结果（用于测试）
func (m *MockToolExecutor) SetResult(toolName string, result map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.results[toolName] = result
}

// MockMatchEngine 模拟匹配引擎
type MockMatchEngine struct {
	defaultMatch      bool
	defaultConfidence float64
}

// NewMockMatchEngine 创建模拟匹配引擎
func NewMockMatchEngine(defaultMatch bool, defaultConfidence float64) *MockMatchEngine {
	return &MockMatchEngine{
		defaultMatch:      defaultMatch,
		defaultConfidence: defaultConfidence,
	}
}

// Match 执行匹配
func (m *MockMatchEngine) Match(ctx context.Context, input any, criteria map[string]any) (bool, float64, error) {
	return m.defaultMatch, m.defaultConfidence, nil
}

// MockLLMDispatcher 模拟LLM调度器
type MockLLMDispatcher struct {
	shouldFail bool
}

// NewMockLLMDispatcher 创建模拟LLM调度器
func NewMockLLMDispatcher(shouldFail bool) *MockLLMDispatcher {
	return &MockLLMDispatcher{shouldFail: shouldFail}
}

// Dispatch 调度LLM请求
func (m *MockLLMDispatcher) Dispatch(ctx context.Context, req *domain.PipelineRequest) error {
	if m.shouldFail {
		return errors.New("LLM dispatch failed")
	}
	return nil
}
