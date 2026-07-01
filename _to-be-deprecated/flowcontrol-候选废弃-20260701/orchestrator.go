package flowcontrol

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// FlowOrchestrator 流程编排器
//
// 职责：
//   - 创建和管理流程计划
//   - 执行完整流程（包括多步骤编排）
//   - 处理流程暂停/恢复/取消
//   - 持久化流程状态
type FlowOrchestrator struct {
	controller   *FlowController
	planStore    FlowPlanStore
	executionLog ExecutionLogger
}

// NewFlowOrchestrator 创建流程编排器
func NewFlowOrchestrator(
	controller *FlowController,
	planStore FlowPlanStore,
	executionLog ExecutionLogger,
) *FlowOrchestrator {
	return &FlowOrchestrator{
		controller:   controller,
		planStore:    planStore,
		executionLog: executionLog,
	}
}

// Execute 执行流程计划
func (o *FlowOrchestrator) Execute(ctx context.Context, plan *FlowPlan) error {
	if plan == nil {
		return fmt.Errorf("flow plan is nil")
	}
	
	// 初始化流程执行记录
	execution := &FlowExecution{
		FlowID:    plan.ID,
		SessionID: plan.SessionID,
		TenantID:  plan.TenantID,
		Status:    FlowStatusRunning,
		Steps:     make([]*StepExecution, 0),
		StartTime: time.Now(),
	}
	
	// 发布流程开始事件
	o.publishEvent(ctx, &FlowEvent{
		Type:      EventFlowStarted,
		FlowID:    plan.ID,
		SessionID: plan.SessionID,
		TenantID:  plan.TenantID,
		Timestamp: time.Now(),
		Data:      map[string]any{"plan_name": plan.Name},
	})
	
	// 更新流程状态
	plan.Status = FlowStatusRunning
	plan.UpdatedAt = time.Now()
	
	// 保存流程状态
	if o.planStore != nil {
		if err := o.planStore.Save(ctx, plan); err != nil {
			slog.Error("failed to save flow plan", "flow_id", plan.ID, "error", err)
		}
	}
	
	// 执行步骤
	var err error
	currentStepID := plan.CurrentStep
	if currentStepID == "" && len(plan.Steps) > 0 {
		currentStepID = plan.Steps[0].ID
	}
	
	visited := make(map[string]bool)
	
	for currentStepID != "" {
		// 检查是否已访问（防止无限循环）
		if visited[currentStepID] {
			err = fmt.Errorf("circular dependency detected at step: %s", currentStepID)
			break
		}
		visited[currentStepID] = true
		
		// 查找当前步骤
		step := plan.FindStep(currentStepID)
		if step == nil {
			err = fmt.Errorf("step not found: %s", currentStepID)
			break
		}
		
		// 更新当前步骤
		plan.CurrentStep = currentStepID
		plan.UpdatedAt = time.Now()
		
		// 执行步骤
		stepExec, stepErr := o.controller.ExecuteStep(ctx, plan.Context, step)
		if stepExec != nil {
			execution.Steps = append(execution.Steps, stepExec)
			
			// 记录步骤执行
			if o.executionLog != nil {
				if logErr := o.executionLog.LogStep(ctx, plan.ID, stepExec); logErr != nil {
					slog.Error("failed to log step execution", "flow_id", plan.ID, "step_id", step.ID, "error", logErr)
				}
			}
		}
		
		// 处理步骤执行结果
		if stepErr != nil {
			err = stepErr
			
			// 如果有失败路径，继续执行
			if len(step.OnFailure) > 0 {
				currentStepID = step.OnFailure[0]
				continue
			}
			
			// 否则终止流程
			break
		}
		
		// 如果步骤被跳过，使用成功路径
		if stepExec.Status == StepStatusSkipped {
			if len(step.OnSuccess) > 0 {
				currentStepID = step.OnSuccess[0]
			} else {
				currentStepID = ""
			}
			continue
		}
		
		// 步骤成功，获取下一步
		if len(step.OnSuccess) > 0 {
			currentStepID = step.OnSuccess[0]
		} else {
			// 没有下一步，流程结束
			currentStepID = ""
		}
	}
	
	// 更新流程执行结果
	endTime := time.Now()
	execution.EndTime = &endTime
	execution.Duration = endTime.Sub(execution.StartTime)
	
	if err != nil {
		execution.Status = FlowStatusFailed
		execution.Error = err.Error()
		plan.Status = FlowStatusFailed
		
		o.publishEvent(ctx, &FlowEvent{
			Type:      EventFlowFailed,
			FlowID:    plan.ID,
			SessionID: plan.SessionID,
			TenantID:  plan.TenantID,
			Timestamp: time.Now(),
			Data:      map[string]any{"error": err.Error()},
		})
	} else {
		execution.Status = FlowStatusCompleted
		plan.Status = FlowStatusCompleted
		completedAt := time.Now()
		plan.CompletedAt = &completedAt
		
		o.publishEvent(ctx, &FlowEvent{
			Type:      EventFlowCompleted,
			FlowID:    plan.ID,
			SessionID: plan.SessionID,
			TenantID:  plan.TenantID,
			Timestamp: time.Now(),
			Data:      map[string]any{"duration_ms": execution.Duration.Milliseconds()},
		})
	}
	
	// 更新流程状态
	plan.UpdatedAt = time.Now()
	
	// 保存最终状态
	if o.planStore != nil {
		if saveErr := o.planStore.Save(ctx, plan); saveErr != nil {
			slog.Error("failed to save flow plan", "flow_id", plan.ID, "error", saveErr)
		}
	}
	
	// 记录流程执行
	if o.executionLog != nil {
		if logErr := o.executionLog.Log(ctx, execution); logErr != nil {
			slog.Error("failed to log flow execution", "flow_id", plan.ID, "error", logErr)
		}
	}
	
	return err
}

// Pause 暂停流程
func (o *FlowOrchestrator) Pause(ctx context.Context, flowID string) error {
	plan, err := o.loadPlan(ctx, flowID)
	if err != nil {
		return err
	}
	
	if plan.Status != FlowStatusRunning {
		return fmt.Errorf("cannot pause flow in status: %s", plan.Status)
	}
	
	plan.Status = FlowStatusPaused
	plan.UpdatedAt = time.Now()
	
	if o.planStore != nil {
		if err := o.planStore.Save(ctx, plan); err != nil {
			return fmt.Errorf("failed to save paused flow: %w", err)
		}
	}
	
	o.publishEvent(ctx, &FlowEvent{
		Type:      EventFlowPaused,
		FlowID:    flowID,
		SessionID: plan.SessionID,
		TenantID:  plan.TenantID,
		Timestamp: time.Now(),
	})
	
	return nil
}

// Resume 恢复流程
func (o *FlowOrchestrator) Resume(ctx context.Context, flowID string) error {
	plan, err := o.loadPlan(ctx, flowID)
	if err != nil {
		return err
	}
	
	if plan.Status != FlowStatusPaused {
		return fmt.Errorf("cannot resume flow in status: %s", plan.Status)
	}
	
	o.publishEvent(ctx, &FlowEvent{
		Type:      EventFlowResumed,
		FlowID:    flowID,
		SessionID: plan.SessionID,
		TenantID:  plan.TenantID,
		Timestamp: time.Now(),
	})
	
	// 继续执行流程
	return o.Execute(ctx, plan)
}

// Cancel 取消流程
func (o *FlowOrchestrator) Cancel(ctx context.Context, flowID string) error {
	plan, err := o.loadPlan(ctx, flowID)
	if err != nil {
		return err
	}
	
	if plan.Status == FlowStatusCompleted || plan.Status == FlowStatusFailed || plan.Status == FlowStatusCanceled {
		return fmt.Errorf("cannot cancel flow in status: %s", plan.Status)
	}
	
	plan.Status = FlowStatusCanceled
	plan.UpdatedAt = time.Now()
	completedAt := time.Now()
	plan.CompletedAt = &completedAt
	
	if o.planStore != nil {
		if err := o.planStore.Save(ctx, plan); err != nil {
			return fmt.Errorf("failed to save canceled flow: %w", err)
		}
	}
	
	o.publishEvent(ctx, &FlowEvent{
		Type:      EventFlowCanceled,
		FlowID:    flowID,
		SessionID: plan.SessionID,
		TenantID:  plan.TenantID,
		Timestamp: time.Now(),
	})
	
	return nil
}

// GetStatus 获取流程状态
func (o *FlowOrchestrator) GetStatus(ctx context.Context, flowID string) (*FlowPlan, error) {
	return o.loadPlan(ctx, flowID)
}

// loadPlan 加载流程计划
func (o *FlowOrchestrator) loadPlan(ctx context.Context, flowID string) (*FlowPlan, error) {
	if o.planStore == nil {
		return nil, fmt.Errorf("plan store not configured")
	}
	
	plan, err := o.planStore.Load(ctx, flowID)
	if err != nil {
		return nil, fmt.Errorf("failed to load flow plan: %w", err)
	}
	
	return plan, nil
}

// publishEvent 发布事件
func (o *FlowOrchestrator) publishEvent(ctx context.Context, event *FlowEvent) {
	if o.controller.eventBus == nil {
		return
	}
	
	if err := o.controller.eventBus.Publish(ctx, event); err != nil {
		slog.Error("failed to publish event", "type", event.Type, "error", err)
	}
}

// ExecuteDynamicFlow 执行动态流程（缓存→工具→匹配→LLM）
// 这是一个便捷方法，用于快速创建常见的流程模式
func (o *FlowOrchestrator) ExecuteDynamicFlow(ctx context.Context, flowCtx *FlowContext) error {
	plan := &FlowPlan{
		ID:        uuid.New().String(),
		Name:      "dynamic_flow",
		SessionID: flowCtx.SessionID,
		TenantID:  flowCtx.TenantID,
		Context:   flowCtx,
		Status:    FlowStatusPending,
		Steps: []*FlowStep{
			{
				ID:   "cache_lookup",
				Name: "缓存查找",
				Type: StepTypeCache,
				Condition: func(ctx *FlowContext) bool {
					// 总是执行缓存查找
					return true
				},
				OnSuccess: []string{"check_cache_hit"},
				OnFailure: []string{"intent_analysis"},
				Parameters: map[string]any{
					"key": fmt.Sprintf("sess:%s:req:%s", flowCtx.SessionID, flowCtx.RequestID),
				},
			},
			{
				ID:   "check_cache_hit",
				Name: "检查缓存命中",
				Type: StepTypeCustom,
				Condition: func(ctx *FlowContext) bool {
					hit, _ := ctx.Get("cache_hit")
					return hit == true
				},
				OnSuccess: []string{}, // 命中则结束
				OnFailure: []string{"intent_analysis"},
			},
			{
				ID:        "intent_analysis",
				Name:      "意图分析",
				Type:      StepTypeAnalysis,
				OnSuccess: []string{"tool_dispatch"},
				OnFailure: []string{"direct_llm"},
				Timeout:   5 * time.Second,
			},
			{
				ID:        "tool_dispatch",
				Name:      "工具调度",
				Type:      StepTypeTool,
				OnSuccess: []string{"result_match"},
				OnFailure: []string{"direct_llm"},
				Timeout:   30 * time.Second,
				Retryable: true,
				MaxRetries: 2,
				RetryDelay: time.Second,
			},
			{
				ID:   "result_match",
				Name: "结果匹配",
				Type: StepTypeMatch,
				Condition: func(ctx *FlowContext) bool {
					// 检查工具结果的置信度
					confidence, ok := ctx.Get("tool_result_confidence")
					if !ok {
						return false
					}
					conf, ok := confidence.(float64)
					return ok && conf > 0.8
				},
				OnSuccess: []string{"compose_response"},
				OnFailure: []string{"tool_dispatch"}, // 重新执行工具
			},
			{
				ID:        "direct_llm",
				Name:      "直接调用LLM",
				Type:      StepTypeLLM,
				OnSuccess: []string{"cache_save"},
				OnFailure: []string{}, // LLM失败则结束
			},
			{
				ID:   "cache_save",
				Name: "保存缓存",
				Type: StepTypeCache,
				Parameters: map[string]any{
					"key": fmt.Sprintf("sess:%s:req:%s", flowCtx.SessionID, flowCtx.RequestID),
					"ttl": 3600,
				},
				OnSuccess: []string{},
			},
			{
				ID:        "compose_response",
				Name:      "组装响应",
				Type:      StepTypeTransform,
				OnSuccess: []string{"cache_save"},
			},
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	
	return o.Execute(ctx, plan)
}
