package notification

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

func TestApprovalCard_ToInteractiveCard(t *testing.T) {
	card := &ApprovalCard{
		SessionID:  "sess_123",
		TenantID:   "tenant_001",
		RequestID:  "req_456",
		ApprovalID: "appr_789",
		RiskLevel:  "high",
		DetectResult: &sessionaudit.DetectResult{
			Score: 8,
			SensitiveWords: []string{"敏感词1", "敏感词2"},
			Threats: []sessionaudit.Threat{
				{
					Type:     "prompt_injection",
					Severity: 7,
					Evidence: "检测到提示注入",
				},
			},
			Decision: sessionaudit.DecisionNeedApproval,
			Reason:   "高风险内容需要审批",
		},
		Actions: []CardAction{
			{ID: "approve", Text: "批准", Style: "primary"},
			{ID: "reject", Text: "拒绝", Style: "danger"},
		},
		CreatedAt: time.Now(),
	}
	
	interactiveCard := card.ToInteractiveCard()
	
	if interactiveCard == nil {
		t.Fatal("expected interactive card, got nil")
	}
	
	if interactiveCard.Header.Title != "🔐 会话审批请求" {
		t.Errorf("expected title '🔐 会话审批请求', got %s", interactiveCard.Header.Title)
	}
	
	if interactiveCard.Header.Template != "orange" {
		t.Errorf("expected template 'orange' for high risk, got %s", interactiveCard.Header.Template)
	}
	
	if len(interactiveCard.Elements) == 0 {
		t.Error("expected elements, got none")
	}
	
	if len(interactiveCard.Actions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(interactiveCard.Actions))
	}
}

func TestRoutingRules_Route(t *testing.T) {
	rules := RoutingRules{
		{
			TenantID:  "tenant_001",
			RiskLevel: "high",
			Recipients: []Recipient{
				{ID: "user_1", Name: "张三", LarkOpenID: "ou_xxx"},
				{ID: "user_2", Name: "李四", LarkOpenID: "ou_yyy"},
			},
			Enabled: true,
		},
		{
			TenantID:  "tenant_001",
			RiskLevel: "medium",
			Recipients: []Recipient{
				{ID: "user_3", Name: "王五", LarkOpenID: "ou_zzz"},
			},
			Enabled: true,
		},
		{
			TenantID:  "tenant_002",
			RiskLevel: "high",
			Recipients: []Recipient{
				{ID: "user_4", Name: "赵六", LarkOpenID: "ou_aaa"},
			},
			Enabled: false, // 未启用
		},
	}
	
	// 测试匹配规则
	recipients := rules.Route("tenant_001", "high")
	if len(recipients) != 2 {
		t.Errorf("expected 2 recipients, got %d", len(recipients))
	}
	
	// 测试未启用的规则
	recipients = rules.Route("tenant_002", "high")
	if len(recipients) != 0 {
		t.Errorf("expected 0 recipients for disabled rule, got %d", len(recipients))
	}
	
	// 测试不存在的规则
	recipients = rules.Route("tenant_003", "high")
	if len(recipients) != 0 {
		t.Errorf("expected 0 recipients for non-existent rule, got %d", len(recipients))
	}
}

func TestApprovalRoutingTable(t *testing.T) {
	rules := RoutingRules{
		{
			TenantID:  "tenant_001",
			RiskLevel: "high",
			Recipients: []Recipient{
				{ID: "user_1", Name: "张三", LarkOpenID: "ou_xxx"},
			},
			Enabled: true,
		},
	}
	
	table := NewApprovalRoutingTable(rules)
	
	// 测试路由
	recipients := table.Route("tenant_001", "high")
	if len(recipients) != 1 {
		t.Errorf("expected 1 recipient, got %d", len(recipients))
	}
	
	// 添加新规则
	table.AddRule(RoutingRule{
		TenantID:  "tenant_001",
		RiskLevel: "medium",
		Recipients: []Recipient{
			{ID: "user_2", Name: "李四", LarkOpenID: "ou_yyy"},
		},
		Enabled: true,
	})
	
	recipients = table.Route("tenant_001", "medium")
	if len(recipients) != 1 {
		t.Errorf("expected 1 recipient after adding rule, got %d", len(recipients))
	}
	
	// 删除规则
	table.RemoveRule("tenant_001", "high")
	recipients = table.Route("tenant_001", "high")
	if len(recipients) != 0 {
		t.Errorf("expected 0 recipients after removing rule, got %d", len(recipients))
	}
}

func TestRiskLevelFromScore(t *testing.T) {
	tests := []struct {
		score    int
		expected string
	}{
		{10, "critical"},
		{9, "critical"},
		{8, "high"},
		{7, "high"},
		{6, "medium"},
		{5, "medium"},
		{4, "low"},
		{3, "low"},
		{0, "low"},
	}
	
	for _, tt := range tests {
		result := riskLevelFromScore(tt.score)
		if result != tt.expected {
			t.Errorf("score %d: expected %s, got %s", tt.score, tt.expected, result)
		}
	}
}

func TestPriorityFromScore(t *testing.T) {
	tests := []struct {
		score    int
		expected Priority
	}{
		{10, PriorityUrgent},
		{9, PriorityUrgent},
		{8, PriorityHigh},
		{7, PriorityHigh},
		{6, PriorityNormal},
		{5, PriorityNormal},
		{4, PriorityLow},
		{0, PriorityLow},
	}
	
	for _, tt := range tests {
		result := priorityFromScore(tt.score)
		if result != tt.expected {
			t.Errorf("score %d: expected %s, got %s", tt.score, tt.expected, result)
		}
	}
}

// MockNotificationChannel 模拟通知渠道（用于测试）
type MockNotificationChannel struct {
	sentMessages []*Message
	sentCards    []*InteractiveCard
	callbacks    []*Callback
}

func NewMockNotificationChannel() *MockNotificationChannel {
	return &MockNotificationChannel{
		sentMessages: make([]*Message, 0),
		sentCards:    make([]*InteractiveCard, 0),
		callbacks:    make([]*Callback, 0),
	}
}

func (m *MockNotificationChannel) Name() string {
	return "mock"
}

func (m *MockNotificationChannel) Send(ctx context.Context, msg *Message) error {
	m.sentMessages = append(m.sentMessages, msg)
	return nil
}

func (m *MockNotificationChannel) SendCard(ctx context.Context, card *InteractiveCard) error {
	m.sentCards = append(m.sentCards, card)
	return nil
}

func (m *MockNotificationChannel) HandleCallback(ctx context.Context, callback *Callback) error {
	m.callbacks = append(m.callbacks, callback)
	return nil
}

func TestLarkBotChannel_ConvertToLarkCard(t *testing.T) {
	config := LarkBotConfig{
		AppID:     "test_app_id",
		AppSecret: "test_secret",
	}
	
	channel := NewLarkBotChannel(config, nil)
	
	card := &InteractiveCard{
		Header: CardHeader{
			Title:    "测试卡片",
			Template: "blue",
		},
		Elements: []CardElement{
			{
				Type: ElementTypeText,
				Text: "这是一条测试消息",
			},
			{
				Type: ElementTypeField,
				Fields: []CardField{
					{Key: "字段1", Value: "值1", Short: true},
					{Key: "字段2", Value: "值2", Short: true},
				},
			},
			{
				Type: ElementTypeDivider,
			},
		},
		Actions: []CardAction{
			{ID: "action1", Text: "按钮1", Style: "primary"},
		},
	}
	
	larkCard := channel.convertToLarkCard(card)
	
	if larkCard == nil {
		t.Fatal("expected lark card, got nil")
	}
	
	// 验证header
	header, ok := larkCard["header"].(map[string]any)
	if !ok {
		t.Fatal("expected header in lark card")
	}
	
	title, ok := header["title"].(map[string]any)
	if !ok {
		t.Fatal("expected title in header")
	}
	
	if title["content"] != "测试卡片" {
		t.Errorf("expected title '测试卡片', got %v", title["content"])
	}
	
	// 验证elements
	elements, ok := larkCard["elements"].([]map[string]any)
	if !ok {
		t.Fatal("expected elements in lark card")
	}
	
	if len(elements) < 3 {
		t.Errorf("expected at least 3 elements, got %d", len(elements))
	}
}

func TestCallbackServer_RegisterHandler(t *testing.T) {
	config := LarkBotConfig{
		VerificationToken: "test_token",
	}
	
	server := NewCallbackServer(config)
	
	handlerCalled := false
	server.RegisterHandler("test_action", func(ctx context.Context, callback *Callback) error {
		handlerCalled = true
		return nil
	})
	
	// 模拟回调
	callback := &Callback{
		Action: "test_action",
		Data:   make(map[string]any),
	}
	
	// 注意：这里只是测试注册，实际的HTTP处理需要更复杂的测试
	if server.handlers["test_action"] == nil {
		t.Error("expected handler to be registered")
	}
	
	// 调用处理器
	err := server.handlers["test_action"](context.Background(), callback)
	if err != nil {
		t.Errorf("handler failed: %v", err)
	}
	
	if !handlerCalled {
		t.Error("expected handler to be called")
	}
}

func TestFormatHelpers(t *testing.T) {
	// 测试 formatScore
	score := formatScore(8)
	if score != "8/10" {
		t.Errorf("expected '8/10', got %s", score)
	}
	
	// 测试 joinStrings
	joined := joinStrings([]string{"a", "b", "c"}, ", ")
	if joined != "a, b, c" {
		t.Errorf("expected 'a, b, c', got %s", joined)
	}
	
	// 测试空数组
	joined = joinStrings([]string{}, ", ")
	if joined != "" {
		t.Errorf("expected empty string, got %s", joined)
	}
	
	// 测试 formatThreats
	threats := []sessionaudit.Threat{
		{Type: "type1", Severity: 5},
		{Type: "type2", Severity: 7},
	}
	formatted := formatThreats(threats)
	expected := "type1(严重度:5), type2(严重度:7)"
	if formatted != expected {
		t.Errorf("expected %s, got %s", expected, formatted)
	}
}
