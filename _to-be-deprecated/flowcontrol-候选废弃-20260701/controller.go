package flowcontrol

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionstate"
)

// FlowController 流程控制器
//
// 职责：
//   - 执行流程计划
//   - 管理步骤执行
//   - 处理条件分支和循环
//   - 超时和重试控制
type FlowController struct {
	stateMachine  *sessionstate.SessionStateMachine
	cacheManager  CacheManager
	toolExecutor  ToolExecutor
	matchEngine   MatchEngine
	llmDispatcher LLMDispatcher
	eventBus      EventBus
	executors     map[StepType]StepExecutor
}

// NewFlowController 创建流程控制器
func NewFlowController(
	stateMachine *sessionstate.SessionStateMachine,
	cacheManager CacheManager,
	toolExecutor ToolExecutor,
	matchEngine MatchEngine,
	llmDispatcher LLMDispatcher,
	eventBus EventBus,
) *FlowController {
	fc := &FlowController{
		stateMachine:  stateMachine,
		cacheManager:  cacheManager,
		toolExecutor:  toolExecutor,
		matchEngine:   matchEngine,
		llmDispatcher: llmDispatcher,
		eventBus:      eventBus,
		executors:     make(map[StepType]StepExecutor),
	}
	
	// 注册默认执行器
	fc.registerDefaultExecutors()
	
	return fc
}

// registerDefaultExecutors 注册默认步骤执行器
func (fc *FlowController) registerDefaultExecutors() {
	// 缓存执行器
	fc.RegisterExecutor(StepTypeCache, fc.executeCacheStep)
	
	// 工具执行器
	fc.RegisterExecutor(StepTypeTool, fc.executeToolStep)
	
	// 匹配执行器
	fc.RegisterExecutor(StepTypeMatch, fc.executeMatchStep)
	
	// LLM执行器
	fc.RegisterExecutor(StepTypeLLM, fc.executeLLMStep)
}

// RegisterExecutor 注册步骤执行器
func (fc *FlowController) RegisterExecutor(stepType StepType, executor StepExecutor) {
	fc.executors[stepType] = executor
}

// ExecuteStep 执行单个步骤
func (fc *FlowController) ExecuteStep(ctx context.Context, flowCtx *FlowContext, step *FlowStep) (*StepExecution, error) {
	stepExec := &StepExecution{
		StepID:    step.ID,
		Status:    StepStatusRunning,
		StartTime: time.Now(),
		Output:    make(map[string]any),
	}
	
	// 发布步骤开始事件
	fc.publishEvent(ctx, &FlowEvent{
		Type:      EventStepStarted,
		StepID:    step.ID,
		SessionID: flowCtx.SessionID,
		TenantID:  flowCtx.TenantID,
		Timestamp: time.Now(),
		Data:      map[string]any{"step_name": step.Name, "step_type": step.Type},
	})
	
	// 检查条件
	if step.Condition != nil && !step.Condition(flowCtx) {
		stepExec.Status = StepStatusSkipped
		endTime := time.Now()
		stepExec.EndTime = &endTime
		stepExec.Duration = endTime.Sub(stepExec.StartTime)
		
		fc.publishEvent(ctx, &FlowEvent{
			Type:      EventStepSkipped,
			StepID:    step.ID,
			SessionID: flowCtx.SessionID,
			TenantID:  flowCtx.TenantID,
			Timestamp: time.Now(),
			Data:      map[string]any{"reason": "condition_not_met"},
		})
		
		return stepExec, nil
	}
	
	// 获取执行器
	executor := step.Executor
	if executor == nil {
		if defaultExecutor, ok := fc.executors[step.Type]; ok {
			executor = defaultExecutor
		} else {
			return nil, fmt.Errorf("no executor found for step type: %s", step.Type)
		}
	}
	
	// 执行步骤（支持重试）
	var err error
	maxRetries := 1
	if step.Retryable && step.MaxRetries > 0 {
		maxRetries = step.MaxRetries + 1
	}
	
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			stepExec.RetryCount++
			fc.publishEvent(ctx, &FlowEvent{
				Type:      EventStepRetrying,
				StepID:    step.ID,
				SessionID: flowCtx.SessionID,
				TenantID:  flowCtx.TenantID,
				Timestamp: time.Now(),
				Data:      map[string]any{"attempt": attempt, "max_retries": step.MaxRetries},
			})
			
			if step.RetryDelay > 0 {
				time.Sleep(step.RetryDelay)
			}
		}
		
		// 创建超时上下文
		execCtx := ctx
		var cancel context.CancelFunc
		if step.Timeout > 0 {
			execCtx, cancel = context.WithTimeout(ctx, step.Timeout)
			defer cancel()
		}
		
		// 执行
		err = executor(execCtx, flowCtx, step)
		if err == nil {
			break
		}
		
		// 检查是否超时
		if execCtx.Err() == context.DeadlineExceeded {
			stepExec.Status = StepStatusTimeout
			break
		}
		
		// 如果不可重试，直接退出
		if !step.Retryable {
			break
		}
	}
	
	// 更新执行结果
	endTime := time.Now()
	stepExec.EndTime = &endTime
	stepExec.Duration = endTime.Sub(stepExec.StartTime)
	
	if err != nil {
		stepExec.Status = StepStatusFailed
		stepExec.Error = err.Error()
		
		fc.publishEvent(ctx, &FlowEvent{
			Type:      EventStepFailed,
			StepID:    step.ID,
			SessionID: flowCtx.SessionID,
			TenantID:  flowCtx.TenantID,
			Timestamp: time.Now(),
			Data:      map[string]any{"error": err.Error(), "retry_count": stepExec.RetryCount},
		})
		
		return stepExec, err
	}
	
	stepExec.Status = StepStatusSuccess
	
	fc.publishEvent(ctx, &FlowEvent{
		Type:      EventStepCompleted,
		StepID:    step.ID,
		SessionID: flowCtx.SessionID,
		TenantID:  flowCtx.TenantID,
		Timestamp: time.Now(),
		Data:      map[string]any{"duration_ms": stepExec.Duration.Milliseconds()},
	})
	
	return stepExec, nil
}

// executeCacheStep 执行缓存步骤
func (fc *FlowController) executeCacheStep(ctx context.Context, flowCtx *FlowContext, step *FlowStep) error {
	if fc.cacheManager == nil {
		return fmt.Errorf("cache manager not configured")
	}
	
	// 从参数获取缓存键
	key, ok := step.Parameters["key"].(string)
	if !ok || key == "" {
		return fmt.Errorf("cache key not specified")
	}
	
	// 查找缓存
	value, hit, err := fc.cacheManager.Lookup(ctx, key)
	if err != nil {
		return fmt.Errorf("cache lookup failed: %w", err)
	}
	
	// 保存结果到上下文
	flowCtx.Set("cache_hit", hit)
	if hit {
		flowCtx.Set("cached_value", value)
		slog.Info("cache hit", "key", key, "session_id", flowCtx.SessionID)
	} else {
		slog.Info("cache miss", "key", key, "session_id", flowCtx.SessionID)
	}
	
	return nil
}

// executeToolStep 执行工具步骤
func (fc *FlowController) executeToolStep(ctx context.Context, flowCtx *FlowContext, step *FlowStep) error {
	if fc.toolExecutor == nil {
		return fmt.Errorf("tool executor not configured")
	}
	
	// 从参数获取工具名称
	toolName, ok := step.Parameters["tool_name"].(string)
	if !ok || toolName == "" {
		return fmt.Errorf("tool name not specified")
	}
	
	// 获取工具参数
	params, _ := step.Parameters["params"].(map[string]any)
	
	// 执行工具
	result, err := fc.toolExecutor.Execute(ctx, toolName, params)
	if err != nil {
		return fmt.Errorf("tool execution failed: %w", err)
	}
	
	// 保存结果到上下文
	flowCtx.Set("tool_result", result)
	slog.Info("tool executed", "tool_name", toolName, "session_id", flowCtx.SessionID)
	
	return nil
}

// executeMatchStep 执行匹配步骤
func (fc *FlowController) executeMatchStep(ctx context.Context, flowCtx *FlowContext, step *FlowStep) error {
	if fc.matchEngine == nil {
		return fmt.Errorf("match engine not configured")
	}
	
	// 从上下文获取输入数据
	inputKey, _ := step.Parameters["input_key"].(string)
	if inputKey == "" {
		inputKey = "tool_result"
	}
	
	input, _ := flowCtx.Get(inputKey)
	
	// 获取匹配条件
	criteria, _ := step.Parameters["criteria"].(map[string]any)
	
	// 执行匹配
	matched, confidence, err := fc.matchEngine.Match(ctx, input, criteria)
	if err != nil {
		return fmt.Errorf("match failed: %w", err)
	}
	
	// 保存结果到上下文
	flowCtx.Set("match_result", matched)
	flowCtx.Set("match_confidence", confidence)
	slog.Info("match executed", "matched", matched, "confidence", confidence, "session_id", flowCtx.SessionID)
	
	return nil
}

// executeLLMStep 执行LLM步骤
func (fc *FlowController) executeLLMStep(ctx context.Context, flowCtx *FlowContext, step *FlowStep) error {
	if fc.llmDispatcher == nil {
		return fmt.Errorf("LLM dispatcher not configured")
	}
	
	// 执行LLM调用
	err := fc.llmDispatcher.Dispatch(ctx, flowCtx.Request)
	if err != nil {
		return fmt.Errorf("LLM dispatch failed: %w", err)
	}
	
	slog.Info("LLM executed", "session_id", flowCtx.SessionID)
	
	return nil
}

// publishEvent 发布事件
func (fc *FlowController) publishEvent(ctx context.Context, event *FlowEvent) {
	if fc.eventBus == nil {
		return
	}
	
	if err := fc.eventBus.Publish(ctx, event); err != nil {
		slog.Error("failed to publish event", "type", event.Type, "error", err)
	}
}
