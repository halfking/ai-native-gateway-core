package approval

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ApprovalDetector 审批检测器
type ApprovalDetector struct {
	configManager     ConfigProvider
	sensitiveDetector *SensitiveDetector
	costEstimator     *CostEstimator
}

// ConfigProvider 配置提供者接口
type ConfigProvider interface {
	GetConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error)
}

// NewApprovalDetector 创建审批检测器
func NewApprovalDetector(
	configManager ConfigProvider,
	sensitiveDetector *SensitiveDetector,
	costEstimator *CostEstimator,
) *ApprovalDetector {
	return &ApprovalDetector{
		configManager:     configManager,
		sensitiveDetector: sensitiveDetector,
		costEstimator:     costEstimator,
	}
}

// ShouldApprove 判断是否需要审批
func (d *ApprovalDetector) ShouldApprove(ctx context.Context, sc *session.SessionContext) (*ApprovalDecision, error) {
	// 获取租户的审批配置
	config, err := d.configManager.GetConfig(ctx, sc.TenantID)
	if err != nil {
		return nil, fmt.Errorf("get approval config: %w", err)
	}

	// 审批未启用
	if !config.Enabled || config.Mode == ModeDisabled {
		return &ApprovalDecision{
			RequiresApproval: false,
		}, nil
	}

	// 手动模式：所有请求都需要审批
	if config.Mode == ModeManual {
		return &ApprovalDecision{
			RequiresApproval: true,
			TriggerType:      TriggerManualMode,
			TriggerReason:    "Manual approval mode is enabled",
			RiskLevel:        RiskMedium,
		}, nil
	}

	// 自动模式：基于规则检测
	if config.Mode == ModeAutomatic {
		return d.evaluateRules(ctx, sc, config)
	}

	return &ApprovalDecision{
		RequiresApproval: false,
	}, nil
}

// evaluateRules 评估审批规则
func (d *ApprovalDetector) evaluateRules(ctx context.Context, sc *session.SessionContext, config *ApprovalConfig) (*ApprovalDecision, error) {
	// 按优先级排序规则（优先级高的先执行）
	sortedRules := d.sortRulesByPriority(config.Rules)

	// 构建评估上下文
	evalCtx := d.buildEvaluationContext(ctx, sc)

	// 依次评估规则
	for _, rule := range sortedRules {
		if !rule.Enabled {
			continue
		}

		matched, err := d.evaluateRule(ctx, rule, evalCtx)
		if err != nil {
			// 规则评估失败，记录但继续
			continue
		}

		if matched {
			// 规则匹配，执行动作
			return d.executeRuleAction(rule, evalCtx), nil
		}
	}

	// 没有规则匹配，不需要审批
	return &ApprovalDecision{
		RequiresApproval: false,
	}, nil
}

// sortRulesByPriority 按优先级排序规则
func (d *ApprovalDetector) sortRulesByPriority(rules []ApprovalRule) []ApprovalRule {
	sorted := make([]ApprovalRule, len(rules))
	copy(sorted, rules)

	// 简单冒泡排序，按优先级降序
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].Priority < sorted[j].Priority {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}

// evaluationContext 评估上下文
type evaluationContext struct {
	sessionContext    *session.SessionContext
	messageContent    string
	tokenCount        int
	estimatedCost     float64
	toolNames         []string
	sensitiveResult   *DetectionResult
	model             string
}

// buildEvaluationContext 构建评估上下文
func (d *ApprovalDetector) buildEvaluationContext(ctx context.Context, sc *session.SessionContext) *evaluationContext {
	evalCtx := &evaluationContext{
		sessionContext: sc,
		model:          sc.ClientModel,
		toolNames:      make([]string, 0),
	}

	// 提取消息内容
	if sc.ClientIR != nil {
		evalCtx.messageContent = d.extractMessageContent(sc.ClientIR)
		
		// 提取工具调用（从 Messages 中提取）
		if sc.ClientIR.Messages != nil {
			for _, msg := range sc.ClientIR.Messages {
				if len(msg.ToolCalls) > 0 {
					for _, tc := range msg.ToolCalls {
						evalCtx.toolNames = append(evalCtx.toolNames, tc.Function.Name)
					}
				}
			}
		}
	}

	// 估算 token 数量（简单估算：每4个字符约1个token）
	evalCtx.tokenCount = len(evalCtx.messageContent) / 4

	// 估算成本（假设输出是输入的1.5倍）
	if evalCtx.model != "" && evalCtx.tokenCount > 0 {
		evalCtx.estimatedCost = d.costEstimator.EstimateWithRatio(
			evalCtx.model,
			evalCtx.tokenCount,
			1.5,
		)
	}

	// 检测敏感信息
	if evalCtx.messageContent != "" {
		result, err := d.sensitiveDetector.Detect(ctx, evalCtx.messageContent)
		if err == nil {
			evalCtx.sensitiveResult = result
		}
	}

	return evalCtx
}

// extractMessageContent 提取消息内容
func (d *ApprovalDetector) extractMessageContent(irReq interface{}) string {
	// 从 IR 中提取所有消息内容
	if irReq == nil {
		return ""
	}
	
	// 类型断言为 InternalRequest
	req, ok := irReq.(*ir.InternalRequest)
	if !ok {
		return ""
	}
	
	var content strings.Builder
	
	// 提取系统提示
	if req.System != nil && req.System.Content != "" {
		content.WriteString(req.System.Content)
		content.WriteString(" ")
	}
	
	// 提取所有消息的文本内容
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if block.Type == "text" && block.Text != "" {
				content.WriteString(block.Text)
				content.WriteString(" ")
			}
		}
	}
	
	return strings.TrimSpace(content.String())
}

// evaluateRule 评估单个规则
func (d *ApprovalDetector) evaluateRule(ctx context.Context, rule ApprovalRule, evalCtx *evaluationContext) (bool, error) {
	// 所有条件都必须满足（AND 逻辑）
	for _, condition := range rule.Conditions {
		matched, err := d.evaluateCondition(condition, evalCtx)
		if err != nil {
			return false, err
		}
		if !matched {
			return false, nil
		}
	}

	return true, nil
}

// evaluateCondition 评估单个条件
func (d *ApprovalDetector) evaluateCondition(condition RuleCondition, evalCtx *evaluationContext) (bool, error) {
	switch condition.Field {
	case "message_content":
		return d.evaluateStringCondition(evalCtx.messageContent, condition.Operator, condition.Value)
	
	case "token_count":
		return d.evaluateNumericCondition(float64(evalCtx.tokenCount), condition.Operator, condition.Value)
	
	case "estimated_cost":
		return d.evaluateNumericCondition(evalCtx.estimatedCost, condition.Operator, condition.Value)
	
	case "tool_name":
		return d.evaluateArrayCondition(evalCtx.toolNames, condition.Operator, condition.Value)
	
	case "model":
		return d.evaluateStringCondition(evalCtx.model, condition.Operator, condition.Value)
	
	case "sensitive_count":
		count := 0
		if evalCtx.sensitiveResult != nil {
			count = evalCtx.sensitiveResult.TotalCount
		}
		return d.evaluateNumericCondition(float64(count), condition.Operator, condition.Value)
	
	case "has_sensitive":
		hasSensitive := evalCtx.sensitiveResult != nil && evalCtx.sensitiveResult.HasSensitive
		return d.evaluateBoolCondition(hasSensitive, condition.Operator, condition.Value)
	
	default:
		return false, fmt.Errorf("unknown field: %s", condition.Field)
	}
}

// evaluateStringCondition 评估字符串条件
func (d *ApprovalDetector) evaluateStringCondition(value, operator, target string) (bool, error) {
	switch operator {
	case "contains":
		return strings.Contains(strings.ToLower(value), strings.ToLower(target)), nil
	
	case "not_contains":
		return !strings.Contains(strings.ToLower(value), strings.ToLower(target)), nil
	
	case "eq":
		return strings.EqualFold(value, target), nil
	
	case "ne":
		return !strings.EqualFold(value, target), nil
	
	case "regex":
		re, err := regexp.Compile(target)
		if err != nil {
			return false, fmt.Errorf("invalid regex: %w", err)
		}
		return re.MatchString(value), nil
	
	case "starts_with":
		return strings.HasPrefix(strings.ToLower(value), strings.ToLower(target)), nil
	
	case "ends_with":
		return strings.HasSuffix(strings.ToLower(value), strings.ToLower(target)), nil
	
	default:
		return false, fmt.Errorf("unknown operator for string: %s", operator)
	}
}

// evaluateNumericCondition 评估数值条件
func (d *ApprovalDetector) evaluateNumericCondition(value float64, operator, target string) (bool, error) {
	targetValue, err := strconv.ParseFloat(target, 64)
	if err != nil {
		return false, fmt.Errorf("invalid numeric value: %w", err)
	}

	switch operator {
	case "gt":
		return value > targetValue, nil
	case "gte":
		return value >= targetValue, nil
	case "lt":
		return value < targetValue, nil
	case "lte":
		return value <= targetValue, nil
	case "eq":
		return value == targetValue, nil
	case "ne":
		return value != targetValue, nil
	default:
		return false, fmt.Errorf("unknown operator for numeric: %s", operator)
	}
}

// evaluateArrayCondition 评估数组条件
func (d *ApprovalDetector) evaluateArrayCondition(values []string, operator, target string) (bool, error) {
	switch operator {
	case "in":
		// target 是逗号分隔的值列表
		targetValues := strings.Split(target, ",")
		for _, v := range values {
			for _, tv := range targetValues {
				if strings.TrimSpace(strings.ToLower(v)) == strings.TrimSpace(strings.ToLower(tv)) {
					return true, nil
				}
			}
		}
		return false, nil
	
	case "not_in":
		targetValues := strings.Split(target, ",")
		for _, v := range values {
			for _, tv := range targetValues {
				if strings.TrimSpace(strings.ToLower(v)) == strings.TrimSpace(strings.ToLower(tv)) {
					return false, nil
				}
			}
		}
		return true, nil
	
	case "contains":
		for _, v := range values {
			if strings.Contains(strings.ToLower(v), strings.ToLower(target)) {
				return true, nil
			}
		}
		return false, nil
	
	default:
		return false, fmt.Errorf("unknown operator for array: %s", operator)
	}
}

// evaluateBoolCondition 评估布尔条件
func (d *ApprovalDetector) evaluateBoolCondition(value bool, operator, target string) (bool, error) {
	targetBool, err := strconv.ParseBool(target)
	if err != nil {
		return false, fmt.Errorf("invalid boolean value: %w", err)
	}

	switch operator {
	case "eq":
		return value == targetBool, nil
	case "ne":
		return value != targetBool, nil
	default:
		return false, fmt.Errorf("unknown operator for boolean: %s", operator)
	}
}

// executeRuleAction 执行规则动作
func (d *ApprovalDetector) executeRuleAction(rule ApprovalRule, evalCtx *evaluationContext) *ApprovalDecision {
	decision := &ApprovalDecision{
		MatchedRule: &rule,
	}

	switch rule.Action.Type {
	case "require_approval":
		decision.RequiresApproval = true
		decision.TriggerType = TriggerPolicyMatch
		decision.TriggerReason = rule.Action.Reason
		decision.RiskLevel = rule.Action.RiskLevel
	
	case "auto_approve":
		decision.RequiresApproval = false
		decision.TriggerType = TriggerPolicyMatch
		decision.TriggerReason = rule.Action.Reason
		decision.RiskLevel = RiskLow
	
	case "auto_reject":
		// auto_reject 也算是需要"审批"（被拒绝）
		decision.RequiresApproval = true
		decision.TriggerType = TriggerPolicyMatch
		decision.TriggerReason = rule.Action.Reason
		decision.RiskLevel = RiskCritical
	
	default:
		decision.RequiresApproval = false
	}

	// 附加成本和敏感信息
	decision.EstimatedCost = evalCtx.estimatedCost
	decision.EstimatedTokens = evalCtx.tokenCount
	
	if evalCtx.sensitiveResult != nil {
		decision.SensitiveItems = evalCtx.sensitiveResult.Items
		
		// 如果没有指定风险等级，根据敏感信息数量自动判断
		if decision.RiskLevel == "" && decision.RequiresApproval {
			decision.RiskLevel = d.calculateRiskLevel(evalCtx.sensitiveResult, evalCtx.estimatedCost)
		}
	}

	return decision
}

// calculateRiskLevel 根据检测结果计算风险等级
func (d *ApprovalDetector) calculateRiskLevel(sensitiveResult *DetectionResult, cost float64) RiskLevel {
	// 基于敏感信息数量和成本综合判断
	sensitiveCount := 0
	if sensitiveResult != nil {
		sensitiveCount = sensitiveResult.TotalCount
	}

	// 高风险：3个以上敏感项 或 成本超过$10
	if sensitiveCount >= 3 || cost > 10.0 {
		return RiskHigh
	}

	// 中风险：1-2个敏感项 或 成本超过$1
	if sensitiveCount > 0 || cost > 1.0 {
		return RiskMedium
	}

	// 低风险
	return RiskLow
}

// DetectSensitiveInfo 单独检测敏感信息（供外部调用）
func (d *ApprovalDetector) DetectSensitiveInfo(ctx context.Context, content string) (*DetectionResult, error) {
	return d.sensitiveDetector.Detect(ctx, content)
}

// EstimateCost 单独估算成本（供外部调用）
func (d *ApprovalDetector) EstimateCost(model string, inputTokens, outputTokens int) float64 {
	return d.costEstimator.Estimate(model, inputTokens, outputTokens)
}
