package approval

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// mockConfigManager 模拟配置管理器
type mockConfigManager struct {
	config *ApprovalConfig
	err    error
}

func (m *mockConfigManager) GetConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.config, nil
}

// TestApprovalDetector_DisabledMode 测试禁用模式
func TestApprovalDetector_DisabledMode(t *testing.T) {
	configMgr := &mockConfigManager{
		config: &ApprovalConfig{
			TenantID: "test-tenant",
			Enabled:  false,
			Mode:     ModeDisabled,
		},
	}

	sensitiveDetector := NewSensitiveDetector(DetectorConfig{
		EnablePII:    true,
		EnableSecret: true,
	})

	costEstimator := NewCostEstimator()

	detector := NewApprovalDetector(configMgr, sensitiveDetector, costEstimator)

	sc := &session.SessionContext{
		TenantID:    "test-tenant",
		ClientModel: "gpt-4",
	}

	decision, err := detector.ShouldApprove(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if decision.RequiresApproval {
		t.Errorf("expected no approval required for disabled mode")
	}
}

// TestApprovalDetector_ManualMode 测试手动模式
func TestApprovalDetector_ManualMode(t *testing.T) {
	configMgr := &mockConfigManager{
		config: &ApprovalConfig{
			TenantID: "test-tenant",
			Enabled:  true,
			Mode:     ModeManual,
		},
	}

	sensitiveDetector := NewSensitiveDetector(DetectorConfig{
		EnablePII: true,
	})

	costEstimator := NewCostEstimator()

	detector := NewApprovalDetector(configMgr, sensitiveDetector, costEstimator)

	sc := &session.SessionContext{
		TenantID:    "test-tenant",
		ClientModel: "gpt-4",
	}

	decision, err := detector.ShouldApprove(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !decision.RequiresApproval {
		t.Errorf("expected approval required for manual mode")
	}

	if decision.TriggerType != TriggerManualMode {
		t.Errorf("expected trigger type %s, got %s", TriggerManualMode, decision.TriggerType)
	}

	if decision.RiskLevel != RiskMedium {
		t.Errorf("expected risk level %s, got %s", RiskMedium, decision.RiskLevel)
	}
}

// TestApprovalDetector_HighCostRule 测试高成本规则
func TestApprovalDetector_HighCostRule(t *testing.T) {
	configMgr := &mockConfigManager{
		config: &ApprovalConfig{
			TenantID: "test-tenant",
			Enabled:  true,
			Mode:     ModeAutomatic,
			Rules: []ApprovalRule{
				{
					Name:     "high_cost",
					Enabled:  true,
					Priority: 100,
					Conditions: []RuleCondition{
						{
							Field:    "estimated_cost",
							Operator: "gt",
							Value:    "10.0",
						},
					},
					Action: RuleAction{
						Type:      "require_approval",
						RiskLevel: RiskHigh,
						Reason:    "Estimated cost exceeds $10",
					},
				},
			},
		},
	}

	sensitiveDetector := NewSensitiveDetector(DetectorConfig{})
	costEstimator := NewCostEstimator()

	detector := NewApprovalDetector(configMgr, sensitiveDetector, costEstimator)

	// 创建高成本场景（大量 tokens）
	sc := &session.SessionContext{
		TenantID:    "test-tenant",
		ClientModel: "gpt-4",
	}

	// 构建评估上下文来模拟高成本
	evalCtx := &evaluationContext{
		sessionContext: sc,
		model:          "gpt-4",
		tokenCount:     200000, // 大量 tokens
		estimatedCost:  15.0,   // 超过阈值
	}

	// 测试条件评估
	condition := RuleCondition{
		Field:    "estimated_cost",
		Operator: "gt",
		Value:    "10.0",
	}

	matched, err := detector.evaluateCondition(condition, evalCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !matched {
		t.Errorf("expected condition to match for high cost")
	}
}

// TestApprovalDetector_TokenCountRule 测试 token 数量规则
func TestApprovalDetector_TokenCountRule(t *testing.T) {
	configMgr := &mockConfigManager{
		config: &ApprovalConfig{
			TenantID: "test-tenant",
			Enabled:  true,
			Mode:     ModeAutomatic,
			Rules: []ApprovalRule{
				{
					Name:     "high_token",
					Enabled:  true,
					Priority: 90,
					Conditions: []RuleCondition{
						{
							Field:    "token_count",
							Operator: "gt",
							Value:    "100000",
						},
					},
					Action: RuleAction{
						Type:      "require_approval",
						RiskLevel: RiskMedium,
						Reason:    "Token count exceeds 100k",
					},
				},
			},
		},
	}

	sensitiveDetector := NewSensitiveDetector(DetectorConfig{})
	costEstimator := NewCostEstimator()

	detector := NewApprovalDetector(configMgr, sensitiveDetector, costEstimator)

	evalCtx := &evaluationContext{
		tokenCount: 150000,
	}

	condition := RuleCondition{
		Field:    "token_count",
		Operator: "gt",
		Value:    "100000",
	}

	matched, err := detector.evaluateCondition(condition, evalCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !matched {
		t.Errorf("expected condition to match for high token count")
	}
}

// TestApprovalDetector_SensitiveContentRule 测试敏感内容规则
func TestApprovalDetector_SensitiveContentRule(t *testing.T) {
	configMgr := &mockConfigManager{
		config: &ApprovalConfig{
			TenantID: "test-tenant",
			Enabled:  true,
			Mode:     ModeAutomatic,
			Rules: []ApprovalRule{
				{
					Name:     "sensitive_content",
					Enabled:  true,
					Priority: 100,
					Conditions: []RuleCondition{
						{
							Field:    "message_content",
							Operator: "contains",
							Value:    "身份证",
						},
					},
					Action: RuleAction{
						Type:      "require_approval",
						RiskLevel: RiskHigh,
						Reason:    "Message contains sensitive keyword",
					},
				},
			},
		},
	}

	sensitiveDetector := NewSensitiveDetector(DetectorConfig{
		EnablePII: true,
	})
	costEstimator := NewCostEstimator()

	detector := NewApprovalDetector(configMgr, sensitiveDetector, costEstimator)

	evalCtx := &evaluationContext{
		messageContent: "请帮我查询身份证号码110101199001011234的信息",
	}

	condition := RuleCondition{
		Field:    "message_content",
		Operator: "contains",
		Value:    "身份证",
	}

	matched, err := detector.evaluateCondition(condition, evalCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !matched {
		t.Errorf("expected condition to match for sensitive content")
	}
}

// TestApprovalDetector_ToolCallRule 测试工具调用规则
func TestApprovalDetector_ToolCallRule(t *testing.T) {
	configMgr := &mockConfigManager{
		config: &ApprovalConfig{
			TenantID: "test-tenant",
			Enabled:  true,
			Mode:     ModeAutomatic,
			Rules: []ApprovalRule{
				{
					Name:     "dangerous_tools",
					Enabled:  true,
					Priority: 100,
					Conditions: []RuleCondition{
						{
							Field:    "tool_name",
							Operator: "in",
							Value:    "execute_code,delete_file,run_command",
						},
					},
					Action: RuleAction{
						Type:      "require_approval",
						RiskLevel: RiskCritical,
						Reason:    "Dangerous tool call detected",
					},
				},
			},
		},
	}

	sensitiveDetector := NewSensitiveDetector(DetectorConfig{})
	costEstimator := NewCostEstimator()

	detector := NewApprovalDetector(configMgr, sensitiveDetector, costEstimator)

	evalCtx := &evaluationContext{
		toolNames: []string{"execute_code", "read_file"},
	}

	condition := RuleCondition{
		Field:    "tool_name",
		Operator: "in",
		Value:    "execute_code,delete_file,run_command",
	}

	matched, err := detector.evaluateCondition(condition, evalCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !matched {
		t.Errorf("expected condition to match for dangerous tool")
	}
}

// TestApprovalDetector_RulePriority 测试规则优先级
func TestApprovalDetector_RulePriority(t *testing.T) {
	configMgr := &mockConfigManager{
		config: &ApprovalConfig{
			TenantID: "test-tenant",
			Enabled:  true,
			Mode:     ModeAutomatic,
			Rules: []ApprovalRule{
				{
					Name:     "low_priority_reject",
					Enabled:  true,
					Priority: 50,
					Conditions: []RuleCondition{
						{
							Field:    "token_count",
							Operator: "gt",
							Value:    "1000",
						},
					},
					Action: RuleAction{
						Type:      "auto_reject",
						RiskLevel: RiskCritical,
						Reason:    "Low priority rejection",
					},
				},
				{
					Name:     "high_priority_approve",
					Enabled:  true,
					Priority: 100,
					Conditions: []RuleCondition{
						{
							Field:    "token_count",
							Operator: "gt",
							Value:    "1000",
						},
					},
					Action: RuleAction{
						Type:      "auto_approve",
						RiskLevel: RiskLow,
						Reason:    "High priority approval",
					},
				},
			},
		},
	}

	sensitiveDetector := NewSensitiveDetector(DetectorConfig{})
	costEstimator := NewCostEstimator()

	detector := NewApprovalDetector(configMgr, sensitiveDetector, costEstimator)

	// 排序规则
	sorted := detector.sortRulesByPriority(configMgr.config.Rules)

	// 验证排序结果
	if len(sorted) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(sorted))
	}

	if sorted[0].Priority != 100 {
		t.Errorf("expected first rule priority 100, got %d", sorted[0].Priority)
	}

	if sorted[1].Priority != 50 {
		t.Errorf("expected second rule priority 50, got %d", sorted[1].Priority)
	}

	// 高优先级规则应该先匹配（auto_approve）
	if sorted[0].Action.Type != "auto_approve" {
		t.Errorf("expected first rule action to be auto_approve, got %s", sorted[0].Action.Type)
	}
}

// TestApprovalDetector_StringOperators 测试字符串操作符
func TestApprovalDetector_StringOperators(t *testing.T) {
	detector := &ApprovalDetector{}

	tests := []struct {
		name     string
		value    string
		operator string
		target   string
		expected bool
	}{
		{"contains_match", "hello world", "contains", "world", true},
		{"contains_no_match", "hello world", "contains", "goodbye", false},
		{"eq_match", "test", "eq", "test", true},
		{"eq_no_match", "test", "eq", "other", false},
		{"starts_with_match", "hello world", "starts_with", "hello", true},
		{"starts_with_no_match", "hello world", "starts_with", "world", false},
		{"ends_with_match", "hello world", "ends_with", "world", true},
		{"ends_with_no_match", "hello world", "ends_with", "hello", false},
		{"regex_match", "test123", "regex", `test\d+`, true},
		{"regex_no_match", "test", "regex", `test\d+`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := detector.evaluateStringCondition(tt.value, tt.operator, tt.target)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestApprovalDetector_NumericOperators 测试数值操作符
func TestApprovalDetector_NumericOperators(t *testing.T) {
	detector := &ApprovalDetector{}

	tests := []struct {
		name     string
		value    float64
		operator string
		target   string
		expected bool
	}{
		{"gt_match", 10.5, "gt", "10.0", true},
		{"gt_no_match", 10.0, "gt", "10.5", false},
		{"gte_match", 10.0, "gte", "10.0", true},
		{"lt_match", 5.0, "lt", "10.0", true},
		{"lt_no_match", 15.0, "lt", "10.0", false},
		{"lte_match", 10.0, "lte", "10.0", true},
		{"eq_match", 10.0, "eq", "10.0", true},
		{"eq_no_match", 10.0, "eq", "11.0", false},
		{"ne_match", 10.0, "ne", "11.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := detector.evaluateNumericCondition(tt.value, tt.operator, tt.target)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestApprovalDetector_ArrayOperators 测试数组操作符
func TestApprovalDetector_ArrayOperators(t *testing.T) {
	detector := &ApprovalDetector{}

	tests := []struct {
		name     string
		values   []string
		operator string
		target   string
		expected bool
	}{
		{"in_match", []string{"execute_code", "read_file"}, "in", "execute_code,delete_file", true},
		{"in_no_match", []string{"read_file", "write_file"}, "in", "execute_code,delete_file", false},
		{"not_in_match", []string{"read_file"}, "not_in", "execute_code,delete_file", true},
		{"not_in_no_match", []string{"execute_code"}, "not_in", "execute_code,delete_file", false},
		{"contains_match", []string{"execute_dangerous_code"}, "contains", "dangerous", true},
		{"contains_no_match", []string{"safe_operation"}, "contains", "dangerous", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := detector.evaluateArrayCondition(tt.values, tt.operator, tt.target)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestApprovalDetector_CalculateRiskLevel 测试风险等级计算
func TestApprovalDetector_CalculateRiskLevel(t *testing.T) {
	detector := &ApprovalDetector{}

	tests := []struct {
		name            string
		sensitiveCount  int
		cost            float64
		expectedLevel   RiskLevel
	}{
		{"high_sensitive", 5, 1.0, RiskHigh},
		{"high_cost", 1, 15.0, RiskHigh},
		{"medium_sensitive", 2, 0.5, RiskMedium},
		{"medium_cost", 0, 5.0, RiskMedium},
		{"low_risk", 0, 0.1, RiskLow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result *DetectionResult
			if tt.sensitiveCount > 0 {
				result = &DetectionResult{
					TotalCount:   tt.sensitiveCount,
					HasSensitive: true,
				}
			}

			level := detector.calculateRiskLevel(result, tt.cost)
			if level != tt.expectedLevel {
				t.Errorf("expected risk level %s, got %s", tt.expectedLevel, level)
			}
		})
	}
}

// TestCostEstimator_BasicEstimate 测试基础成本估算
func TestCostEstimator_BasicEstimate(t *testing.T) {
	estimator := NewCostEstimator()

	tests := []struct {
		name         string
		model        string
		inputTokens  int
		outputTokens int
		minCost      float64
		maxCost      float64
	}{
		{"gpt4_small", "gpt-4", 1000, 1000, 0.08, 0.10},
		{"gpt4o_small", "gpt-4o", 1000, 1000, 0.019, 0.021},
		{"gpt35_small", "gpt-3.5-turbo", 1000, 1000, 0.0019, 0.0021},
		{"claude3_opus", "claude-3-opus", 1000, 1000, 0.089, 0.091},
		{"claude3_haiku", "claude-3-haiku", 1000, 1000, 0.00149, 0.00151},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := estimator.Estimate(tt.model, tt.inputTokens, tt.outputTokens)
			if cost < tt.minCost || cost > tt.maxCost {
				t.Errorf("expected cost between %.4f and %.4f, got %.4f", tt.minCost, tt.maxCost, cost)
			}
		})
	}
}

// TestCostEstimator_UnknownModel 测试未知模型
func TestCostEstimator_UnknownModel(t *testing.T) {
	estimator := NewCostEstimator()

	// 未知模型应使用默认 GPT-4 定价
	cost := estimator.Estimate("unknown-model", 1000, 1000)
	if cost <= 0 {
		t.Errorf("expected positive cost for unknown model, got %.4f", cost)
	}

	// 应该大于等于 GPT-4 的成本
	gpt4Cost := estimator.Estimate("gpt-4", 1000, 1000)
	if cost < gpt4Cost {
		t.Errorf("unknown model cost should be at least GPT-4 cost")
	}
}

// TestCostEstimator_WithCache 测试缓存 token 估算
func TestCostEstimator_WithCache(t *testing.T) {
	estimator := NewCostEstimator()

	// GPT-4o 支持缓存
	costWithoutCache := estimator.Estimate("gpt-4o", 1000, 1000)
	costWithCache := estimator.EstimateWithCache("gpt-4o", 500, 1000, 500)

	// 有缓存的应该更便宜
	if costWithCache >= costWithoutCache {
		t.Errorf("cost with cache should be less than without cache")
	}
}

// TestCostEstimator_ModelAlias 测试模型别名
func TestCostEstimator_ModelAlias(t *testing.T) {
	estimator := NewCostEstimator()

	tests := []struct {
		model string
		alias string
	}{
		{"gpt-4o", "gpt-4o-2024-05-13"},
		{"gpt-4o-mini", "gpt-4o-mini-2024-07-18"},
		{"gpt-3.5-turbo", "gpt-3.5-turbo-0125"},
		{"claude-3-opus", "claude-3-opus-20240229"},
	}

	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			cost1 := estimator.Estimate(tt.model, 1000, 1000)
			cost2 := estimator.Estimate(tt.alias, 1000, 1000)

			if cost1 != cost2 {
				t.Errorf("expected same cost for %s and %s, got %.4f and %.4f",
					tt.model, tt.alias, cost1, cost2)
			}
		})
	}
}

// TestCostEstimator_EstimateWithRatio 测试比例估算
func TestCostEstimator_EstimateWithRatio(t *testing.T) {
	estimator := NewCostEstimator()

	inputTokens := 1000
	outputRatio := 1.5

	cost1 := estimator.EstimateWithRatio("gpt-4", inputTokens, outputRatio)
	cost2 := estimator.Estimate("gpt-4", inputTokens, int(float64(inputTokens)*outputRatio))

	if cost1 != cost2 {
		t.Errorf("expected same cost from ratio and direct estimate, got %.4f and %.4f", cost1, cost2)
	}
}

// TestFormatCost 测试成本格式化
func TestFormatCost(t *testing.T) {
	tests := []struct {
		cost     float64
		expected string
	}{
		{0.000001, "$0.000001"},
		{0.001234, "$0.0012"},
		{0.123456, "$0.1235"},
		{1.23456, "$1.23"},
		{12.3456, "$12.35"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := FormatCost(tt.cost)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}
