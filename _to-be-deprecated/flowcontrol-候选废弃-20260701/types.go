// Package flowcontrol 实现动态流程控制引擎。
//
// 核心能力：
//   - 动态流程编排：支持条件分支、循环、超时、重试
//   - 步骤类型：缓存、工具、匹配、LLM、审批等
//   - 流程持久化：支持暂停/恢复/取消
//   - 事件总线：流程执行过程中的事件通知
//
// 设计原则：
//   - 可组合：流程步骤可自由组合
//   - 可测试：核心逻辑与外部依赖解耦
//   - 可观测：完整的执行日志和指标
package flowcontrol

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// StepType 流程步骤类型
type StepType string

const (
	// StepTypeCache 缓存查找
	StepTypeCache StepType = "cache"
	
	// StepTypeTool 工具执行
	StepTypeTool StepType = "tool"
	
	// StepTypeMatch 结果匹配
	StepTypeMatch StepType = "match"
	
	// StepTypeLLM LLM调用
	StepTypeLLM StepType = "llm"
	
	// StepTypeApproval 审批
	StepTypeApproval StepType = "approval"
	
	// StepTypeAnalysis 分析
	StepTypeAnalysis StepType = "analysis"
	
	// StepTypeTransform 转换
	StepTypeTransform StepType = "transform"
	
	// StepTypeValidation 验证
	StepTypeValidation StepType = "validation"
	
	// StepTypeCustom 自定义步骤
	StepTypeCustom StepType = "custom"
)

// StepStatus 步骤执行状态
type StepStatus string

const (
	// StepStatusPending 待执行
	StepStatusPending StepStatus = "pending"
	
	// StepStatusRunning 执行中
	StepStatusRunning StepStatus = "running"
	
	// StepStatusSuccess 执行成功
	StepStatusSuccess StepStatus = "success"
	
	// StepStatusFailed 执行失败
	StepStatusFailed StepStatus = "failed"
	
	// StepStatusSkipped 已跳过
	StepStatusSkipped StepStatus = "skipped"
	
	// StepStatusTimeout 超时
	StepStatusTimeout StepStatus = "timeout"
)

// FlowStatus 流程执行状态
type FlowStatus string

const (
	// FlowStatusPending 待执行
	FlowStatusPending FlowStatus = "pending"
	
	// FlowStatusRunning 执行中
	FlowStatusRunning FlowStatus = "running"
	
	// FlowStatusPaused 已暂停
	FlowStatusPaused FlowStatus = "paused"
	
	// FlowStatusCompleted 已完成
	FlowStatusCompleted FlowStatus = "completed"
	
	// FlowStatusFailed 执行失败
	FlowStatusFailed FlowStatus = "failed"
	
	// FlowStatusCanceled 已取消
	FlowStatusCanceled FlowStatus = "canceled"
)

// StepCondition 步骤执行条件
type StepCondition func(ctx *FlowContext) bool

// StepExecutor 步骤执行器
type StepExecutor func(ctx context.Context, flowCtx *FlowContext, step *FlowStep) error

// FlowContext 流程上下文
type FlowContext struct {
	// SessionID 会话ID
	SessionID string
	
	// TenantID 租户ID
	TenantID string
	
	// RequestID 请求ID
	RequestID string
	
	// PipelineRequest Pipeline请求（核心数据结构）
	Request *domain.PipelineRequest
	
	// Variables 流程变量（在步骤间传递数据）
	Variables map[string]any
	
	// Metadata 元数据
	Metadata map[string]any
	
	// CreatedAt 创建时间
	CreatedAt time.Time
	
	// UpdatedAt 更新时间
	UpdatedAt time.Time
}

// Get 获取变量
func (fc *FlowContext) Get(key string) (any, bool) {
	if fc.Variables == nil {
		return nil, false
	}
	val, ok := fc.Variables[key]
	return val, ok
}

// Set 设置变量
func (fc *FlowContext) Set(key string, value any) {
	if fc.Variables == nil {
		fc.Variables = make(map[string]any)
	}
	fc.Variables[key] = value
	fc.UpdatedAt = time.Now()
}

// FlowStep 流程步骤
type FlowStep struct {
	// ID 步骤唯一标识
	ID string
	
	// Name 步骤名称
	Name string
	
	// Type 步骤类型
	Type StepType
	
	// Condition 执行条件（可选）
	Condition StepCondition
	
	// Executor 执行器（可选，如果为空则使用默认执行器）
	Executor StepExecutor
	
	// OnSuccess 成功后的下一步ID列表
	OnSuccess []string
	
	// OnFailure 失败后的下一步ID列表
	OnFailure []string
	
	// Timeout 超时时间（0表示无超时）
	Timeout time.Duration
	
	// Retryable 是否可重试
	Retryable bool
	
	// MaxRetries 最大重试次数（Retryable为true时有效）
	MaxRetries int
	
	// RetryDelay 重试延迟
	RetryDelay time.Duration
	
	// Parameters 步骤参数
	Parameters map[string]any
	
	// Metadata 元数据
	Metadata map[string]any
}

// FlowPlan 流程计划
type FlowPlan struct {
	// ID 流程唯一标识
	ID string
	
	// Name 流程名称
	Name string
	
	// SessionID 会话ID
	SessionID string
	
	// TenantID 租户ID
	TenantID string
	
	// Steps 流程步骤列表
	Steps []*FlowStep
	
	// CurrentStep 当前步骤ID
	CurrentStep string
	
	// Status 流程状态
	Status FlowStatus
	
	// Context 流程上下文
	Context *FlowContext
	
	// CreatedAt 创建时间
	CreatedAt time.Time
	
	// UpdatedAt 更新时间
	UpdatedAt time.Time
	
	// CompletedAt 完成时间
	CompletedAt *time.Time
}

// FindStep 查找步骤
func (fp *FlowPlan) FindStep(stepID string) *FlowStep {
	for _, step := range fp.Steps {
		if step.ID == stepID {
			return step
		}
	}
	return nil
}

// StepExecution 步骤执行记录
type StepExecution struct {
	// StepID 步骤ID
	StepID string
	
	// Status 执行状态
	Status StepStatus
	
	// StartTime 开始时间
	StartTime time.Time
	
	// EndTime 结束时间
	EndTime *time.Time
	
	// Duration 执行时长
	Duration time.Duration
	
	// RetryCount 重试次数
	RetryCount int
	
	// Error 错误信息
	Error string
	
	// Output 输出数据
	Output map[string]any
}

// FlowExecution 流程执行记录
type FlowExecution struct {
	// FlowID 流程ID
	FlowID string
	
	// SessionID 会话ID
	SessionID string
	
	// TenantID 租户ID
	TenantID string
	
	// Status 执行状态
	Status FlowStatus
	
	// Steps 步骤执行记录
	Steps []*StepExecution
	
	// StartTime 开始时间
	StartTime time.Time
	
	// EndTime 结束时间
	EndTime *time.Time
	
	// Duration 总执行时长
	Duration time.Duration
	
	// Error 错误信息
	Error string
}

// FlowEvent 流程事件
type FlowEvent struct {
	// Type 事件类型
	Type FlowEventType
	
	// FlowID 流程ID
	FlowID string
	
	// StepID 步骤ID（可选）
	StepID string
	
	// SessionID 会话ID
	SessionID string
	
	// TenantID 租户ID
	TenantID string
	
	// Timestamp 事件时间
	Timestamp time.Time
	
	// Data 事件数据
	Data map[string]any
}

// FlowEventType 流程事件类型
type FlowEventType string

const (
	// EventFlowStarted 流程开始
	EventFlowStarted FlowEventType = "flow_started"
	
	// EventFlowCompleted 流程完成
	EventFlowCompleted FlowEventType = "flow_completed"
	
	// EventFlowFailed 流程失败
	EventFlowFailed FlowEventType = "flow_failed"
	
	// EventFlowPaused 流程暂停
	EventFlowPaused FlowEventType = "flow_paused"
	
	// EventFlowResumed 流程恢复
	EventFlowResumed FlowEventType = "flow_resumed"
	
	// EventFlowCanceled 流程取消
	EventFlowCanceled FlowEventType = "flow_canceled"
	
	// EventStepStarted 步骤开始
	EventStepStarted FlowEventType = "step_started"
	
	// EventStepCompleted 步骤完成
	EventStepCompleted FlowEventType = "step_completed"
	
	// EventStepFailed 步骤失败
	EventStepFailed FlowEventType = "step_failed"
	
	// EventStepSkipped 步骤跳过
	EventStepSkipped FlowEventType = "step_skipped"
	
	// EventStepRetrying 步骤重试
	EventStepRetrying FlowEventType = "step_retrying"
)

// CacheManager 缓存管理器接口
type CacheManager interface {
	Lookup(ctx context.Context, key string) ([]byte, bool, error)
	Save(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// ToolExecutor 工具执行器接口
type ToolExecutor interface {
	Execute(ctx context.Context, toolName string, params map[string]any) (map[string]any, error)
}

// MatchEngine 匹配引擎接口
type MatchEngine interface {
	Match(ctx context.Context, input any, criteria map[string]any) (bool, float64, error)
}

// LLMDispatcher LLM调度器接口
type LLMDispatcher interface {
	Dispatch(ctx context.Context, req *domain.PipelineRequest) error
}

// EventBus 事件总线接口
type EventBus interface {
	Publish(ctx context.Context, event *FlowEvent) error
	Subscribe(eventType FlowEventType, handler func(*FlowEvent)) error
}

// FlowPlanStore 流程计划存储接口
type FlowPlanStore interface {
	Save(ctx context.Context, plan *FlowPlan) error
	Load(ctx context.Context, flowID string) (*FlowPlan, error)
	Delete(ctx context.Context, flowID string) error
}

// ExecutionLogger 执行日志记录器接口
type ExecutionLogger interface {
	Log(ctx context.Context, execution *FlowExecution) error
	LogStep(ctx context.Context, flowID string, stepExec *StepExecution) error
}
