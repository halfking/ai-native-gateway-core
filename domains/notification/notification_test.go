package notification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock helpers
// ─────────────────────────────────────────────────────────────────────────────

type mockChannel struct {
	name          string
	mu            sync.Mutex
	sentMessages  []*Message
	sentCards     []*InteractiveCard
	parsedPayload [][]byte
	parseErr      error
	sendErr       error
	healthErr     error
}

func newMockChannel(name string) *mockChannel {
	return &mockChannel{name: name}
}

func (m *mockChannel) Name() string { return m.name }

func (m *mockChannel) Send(ctx context.Context, msg *Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return m.sendErr
	}
	cp := *msg
	m.sentMessages = append(m.sentMessages, &cp)
	return nil
}

func (m *mockChannel) SendCard(ctx context.Context, card *InteractiveCard) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return m.sendErr
	}
	cp := *card
	cp.Metadata = copyOf(card.Metadata)
	cp.Elements = append([]CardElement(nil), card.Elements...)
	cp.Actions = append([]CardAction(nil), card.Actions...)
	m.sentCards = append(m.sentCards, &cp)
	return nil
}

func (m *mockChannel) ParseCallback(ctx context.Context, raw []byte) (*Callback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := append([]byte(nil), raw...)
	m.parsedPayload = append(m.parsedPayload, cp)
	if m.parseErr != nil {
		return nil, m.parseErr
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	cb := &Callback{
		Action:    stringFromAny(data["action"]),
		Data:      data,
		Timestamp: time.Now(),
	}
	// URL verification challenge
	if _, ok := data["challenge"]; ok {
		cb.Action = "url_verification"
	}
	if u, ok := data["user"].(map[string]any); ok {
		cb.User.OpenID = stringFromAny(u["open_id"])
		cb.User.ID = stringFromAny(u["id"])
		cb.User.Name = stringFromAny(u["name"])
	}
	cb.TenantID = stringFromAny(data["tenant_id"])
	cb.SessionID = stringFromAny(data["session_id"])
	return cb, nil
}

func (m *mockChannel) HealthCheck(ctx context.Context) error { return m.healthErr }

func copyOf(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

type mockSummary struct {
	view *SessionSummaryView
	err  error
}

func (m *mockSummary) GetSummaryView(ctx context.Context, tenantID, sessionKey string) (*SessionSummaryView, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.view, nil
}

type mockResumer struct {
	calls []string
	err   error
}

func (m *mockResumer) ResumeAfterApproval(ctx context.Context, tenantID, sessionID string) error {
	m.calls = append(m.calls, tenantID+"/"+sessionID)
	return m.err
}

// ─────────────────────────────────────────────────────────────────────────────
// Routing rules
// ─────────────────────────────────────────────────────────────────────────────

func TestRoutingRules_Route(t *testing.T) {
	rules := RoutingRules{
		{
			TenantID: "tenant_001", RiskLevel: RiskLevelHigh, Channel: ChannelLark,
			Recipients: []Recipient{
				{ID: "u1", Name: "张三", LarkOpenID: "ou_xxx"},
				{ID: "u2", Name: "李四", LarkOpenID: "ou_yyy"},
			},
			Enabled: true,
		},
		{
			TenantID: "tenant_001", RiskLevel: RiskLevelHigh, Channel: ChannelDingTalk,
			Recipients: []Recipient{
				{ID: "u3", Name: "王五", DingTalkUserID: "dt_xxx"},
			},
			Enabled: true,
		},
		{
			TenantID: "tenant_002", RiskLevel: RiskLevelHigh,
			Enabled: false,
		},
	}

	got := rules.Route("tenant_001", RiskLevelHigh)
	if len(got) != 3 {
		t.Fatalf("expected 3 recipients, got %d", len(got))
	}

	if len(rules.Route("tenant_002", RiskLevelHigh)) != 0 {
		t.Errorf("disabled rule should not match")
	}

	if len(rules.Route("tenant_003", RiskLevelHigh)) != 0 {
		t.Errorf("non-existent rule should not match")
	}

	// Critical 风险级别无规则 → 空
	if len(rules.Route("tenant_001", RiskLevelCritical)) != 0 {
		t.Errorf("critical should have no match")
	}
}

func TestRoutingRules_GlobalFallback(t *testing.T) {
	rules := RoutingRules{
		{
			TenantID: "", RiskLevel: RiskLevelCritical, Channel: ChannelLark,
			Recipients: []Recipient{{ID: "admin", LarkOpenID: "ou_admin"}},
			Enabled:    true,
		},
	}
	got := rules.Route("any_tenant", RiskLevelCritical)
	if len(got) != 1 || got[0].ID != "admin" {
		t.Errorf("global fallback rule failed: %+v", got)
	}
}

func TestRoutingRules_DedupByID(t *testing.T) {
	rules := RoutingRules{
		{
			TenantID: "t1", RiskLevel: RiskLevelHigh,
			Recipients: []Recipient{{ID: "u1", LarkOpenID: "ou_1"}},
			Enabled:    true,
		},
		{
			TenantID: "t1", RiskLevel: RiskLevelHigh, Priority: 1,
			Recipients: []Recipient{{ID: "u1", LarkOpenID: "ou_1"}, {ID: "u2", LarkOpenID: "ou_2"}},
			Enabled:    true,
		},
	}
	got := rules.Route("t1", RiskLevelHigh)
	if len(got) != 2 {
		t.Errorf("dedup failed, got %d recipients: %+v", len(got), got)
	}
}

func TestApprovalRoutingTable_HotUpdate(t *testing.T) {
	tbl := NewEmptyRoutingTable()
	if got := tbl.Route("t1", RiskLevelHigh); got != nil {
		t.Errorf("empty table should return nil, got %v", got)
	}

	tbl.AddRule(RoutingRule{
		TenantID: "t1", RiskLevel: RiskLevelHigh, Enabled: true,
		Recipients: []Recipient{{ID: "u1", LarkOpenID: "ou_1"}},
	})
	if got := tbl.Route("t1", RiskLevelHigh); len(got) != 1 {
		t.Errorf("after AddRule expected 1, got %d", len(got))
	}

	tbl.SetRules(RoutingRules{
		{
			TenantID: "t1", RiskLevel: RiskLevelCritical, Enabled: true,
			Recipients: []Recipient{{ID: "u9", LarkOpenID: "ou_9"}},
		},
	})
	if got := tbl.Route("t1", RiskLevelHigh); got != nil {
		t.Errorf("SetRules should replace; got %v", got)
	}
	if got := tbl.Route("t1", RiskLevelCritical); len(got) != 1 {
		t.Errorf("expected new rule applied")
	}

	tbl.RemoveRule("t1", RiskLevelCritical)
	if got := tbl.Route("t1", RiskLevelCritical); got != nil {
		t.Errorf("RemoveRule should clear; got %v", got)
	}
}

type fakeRoutingLoader struct {
	rows []RoutingRuleDBRow
	err  error
}

func (f *fakeRoutingLoader) LoadRoutingRules(ctx context.Context) ([]RoutingRuleDBRow, error) {
	return f.rows, f.err
}

func TestApprovalRoutingTable_LoadFromDB(t *testing.T) {
	approvers, _ := json.Marshal([]ApproverDTO{
		{UserID: "u1", Name: "张三", LarkOpenID: "ou_1"},
	})
	loader := &fakeRoutingLoader{
		rows: []RoutingRuleDBRow{
			{ID: 1, TenantID: "t1", RiskLevel: "high", Channel: "lark",
				Approvers: approvers, Priority: 0, Enabled: true},
			{ID: 2, TenantID: "bad", RiskLevel: "low", Channel: "lark",
				Approvers: json.RawMessage("not json"), Enabled: true},
		},
	}
	tbl := NewEmptyRoutingTable()
	if err := tbl.LoadFromDB(context.Background(), loader); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	got := tbl.Route("t1", RiskLevelHigh)
	if len(got) != 1 || got[0].ID != "u1" {
		t.Errorf("expected u1, got %+v", got)
	}
	if got := tbl.Route("bad", RiskLevelLow); len(got) != 0 {
		t.Errorf("bad row should be skipped, got %+v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Risk/Priority helpers
// ─────────────────────────────────────────────────────────────────────────────

func TestRiskLevelFromScore(t *testing.T) {
	cases := []struct {
		score int
		want  RiskLevel
	}{
		{10, RiskLevelCritical},
		{9, RiskLevelCritical},
		{8, RiskLevelHigh},
		{7, RiskLevelHigh},
		{6, RiskLevelMedium},
		{5, RiskLevelMedium},
		{4, RiskLevelLow},
		{0, RiskLevelLow},
	}
	for _, c := range cases {
		if got := RiskLevelFromScore(c.score); got != c.want {
			t.Errorf("score %d: want %s got %s", c.score, c.want, got)
		}
	}
}

func TestPriorityFromScore(t *testing.T) {
	cases := []struct {
		score int
		want  Priority
	}{
		{9, PriorityUrgent},
		{7, PriorityHigh},
		{5, PriorityNormal},
		{4, PriorityLow},
	}
	for _, c := range cases {
		if got := PriorityFromScore(c.score); got != c.want {
			t.Errorf("score %d: want %s got %s", c.score, c.want, got)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ApprovalCard → InteractiveCard
// ─────────────────────────────────────────────────────────────────────────────

func TestApprovalCard_ToInteractiveCard_WithSummary(t *testing.T) {
	card := &ApprovalCard{
		SessionID:  "sess_123",
		TenantID:   "tenant_001",
		RequestID:  "req_456",
		ApprovalID: "appr_789",
		RiskLevel:  string(RiskLevelHigh),
		DetectResult: &sessionaudit.DetectResult{
			Score:          8,
			SensitiveWords: []string{"foo", "bar"},
			Threats: []sessionaudit.Threat{
				{Type: "prompt_injection", Severity: 7, Evidence: "ignored"},
			},
			Decision: sessionaudit.DecisionNeedApproval,
			Reason:   "high risk",
		},
		SessionSummary: &SessionSummaryView{
			Title:      "客服对话",
			Summary:    "用户咨询订单状态",
			KeyTopics:  []string{"订单", "物流"},
			UserIntent: "data_analysis",
			HasSummary: true,
		},
		Snapshot: &sessionaudit.RequestSnapshot{
			ClientModel: "gpt-4",
			ClientInfo:  sessionaudit.ClientInfo{IP: "10.0.0.1"},
		},
		Actions: []CardAction{
			{ID: "approve", Text: "批准", Style: "primary"},
			{ID: "reject", Text: "拒绝", Style: "danger"},
		},
		CreatedAt: time.Now(),
	}

	out := card.ToInteractiveCard()
	if out.Header.Template != "orange" {
		t.Errorf("high risk template should be orange, got %s", out.Header.Template)
	}
	if out.Metadata["approval_id"] != "appr_789" {
		t.Errorf("metadata.approval_id missing")
	}
	if out.Metadata["risk_level"] != "high" {
		t.Errorf("metadata.risk_level missing")
	}

	// 必须包含会话总结区块
	hasSummaryText := false
	hasKeyTopics := false
	for _, e := range out.Elements {
		if e.Type == ElementTypeText && strings.Contains(e.Text, "客服对话") {
			hasSummaryText = true
		}
		if e.Type == ElementTypeText && strings.Contains(e.Text, "订单, 物流") {
			hasKeyTopics = true
		}
	}
	if !hasSummaryText {
		t.Error("card should contain session summary title")
	}
	if !hasKeyTopics {
		t.Error("card should contain key topics")
	}
}

func TestApprovalCard_ToInteractiveCard_NoSummary(t *testing.T) {
	card := &ApprovalCard{
		SessionID: "s1", TenantID: "t1", RequestID: "r1", ApprovalID: "a1",
		RiskLevel:    string(RiskLevelLow),
		DetectResult: &sessionaudit.DetectResult{Score: 2, Decision: sessionaudit.DecisionPass},
	}
	out := card.ToInteractiveCard()
	if out.Header.Template != "green" {
		t.Errorf("low risk should be green, got %s", out.Header.Template)
	}
	for _, e := range out.Elements {
		if e.Type == ElementTypeText && strings.Contains(e.Text, "会话主题") {
			t.Error("summary block should not appear when HasSummary=false")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ApprovalNotifier
// ─────────────────────────────────────────────────────────────────────────────

// fakeApprovalStore 实现 ApprovalStore 接口，用于单元测试。
type fakeApprovalStore struct {
	mu       sync.Mutex
	approved map[string]string
	rejected map[string]string
	records  map[string]*sessionaudit.ApprovalRecord
	getErr   error
}

func newFakeApprovalStore() *fakeApprovalStore {
	return &fakeApprovalStore{
		approved: map[string]string{},
		rejected: map[string]string{},
		records:  map[string]*sessionaudit.ApprovalRecord{},
	}
}

func (m *fakeApprovalStore) Approve(ctx context.Context, approvalID, tenantID, user, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.approved[approvalID]; ok {
		return errors.New("already approved")
	}
	m.approved[approvalID] = reason
	return nil
}

func (m *fakeApprovalStore) Reject(ctx context.Context, approvalID, tenantID, user, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rejected[approvalID]; ok {
		return errors.New("already rejected")
	}
	m.rejected[approvalID] = reason
	return nil
}

func (m *fakeApprovalStore) GetForTenant(ctx context.Context, approvalID, expectedTenantID string) (*sessionaudit.ApprovalRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	if rec, ok := m.records[approvalID]; ok {
		return rec, nil
	}
	return &sessionaudit.ApprovalRecord{
		ID:           approvalID,
		SessionID:    "sess_1",
		TenantID:     "tenant_001",
		RequestID:    "req_1",
		Status:       sessionaudit.ApprovalPending,
		DetectResult: &sessionaudit.DetectResult{Score: 7},
		CreatedAt:    time.Now(),
	}, nil
}

func TestNewApprovalNotifier_Validation(t *testing.T) {
	mock := newMockChannel("lark")
	chans := map[ChannelType]NotificationChannel{ChannelLark: mock}
	store := newFakeApprovalStore()

	cases := []struct {
		name string
		cfg  NotifierConfig
	}{
		{"missing approval mgr", NotifierConfig{Channels: chans}},
		{"missing channels", NotifierConfig{Routing: NewEmptyRoutingTable(), ApprovalMgr: store}},
		{"bad default channel",
			NotifierConfig{
				Channels:       map[ChannelType]NotificationChannel{ChannelLark: mock},
				DefaultChannel: ChannelWeChat,
				Routing:        NewEmptyRoutingTable(),
				ApprovalMgr:    store,
			},
		},
	}
	for _, c := range cases {
		_, err := NewApprovalNotifier(c.cfg)
		if err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}

	// 路由字段缺省应自动补全（而非报错）
	notifier, err := NewApprovalNotifier(NotifierConfig{
		Channels:    chans,
		ApprovalMgr: store,
	})
	if err != nil {
		t.Errorf("default routing should be filled, got %v", err)
	}
	if notifier == nil {
		t.Error("notifier should not be nil on success")
	}
}

func TestNotifier_NotifyApproval_MockIntegration(t *testing.T) {
	lark := newMockChannel("lark")
	ding := newMockChannel("dingtalk")
	store := newFakeApprovalStore()
	routing := NewApprovalRoutingTable(RoutingRules{
		{
			TenantID: "tenant_001", RiskLevel: RiskLevelHigh,
			Recipients: []Recipient{
				{ID: "u1", LarkOpenID: "ou_1"},
				{ID: "u2", DingTalkUserID: "dt_1"},
			},
			Enabled: true,
		},
	})

	notifier, err := buildNotifier(lark, ding, routing, &mockSummary{
		view: &SessionSummaryView{
			Title: "x", Summary: "y", KeyTopics: []string{"k"}, UserIntent: "chat", HasSummary: true,
		},
	}, &mockResumer{}, store)
	if err != nil {
		t.Fatalf("buildNotifier: %v", err)
	}

	record := &sessionaudit.ApprovalRecord{
		ID: "appr_1", SessionID: "sess_1", TenantID: "tenant_001", RequestID: "req_1",
		Status: sessionaudit.ApprovalPending,
		DetectResult: &sessionaudit.DetectResult{
			Score:          8,
			SensitiveWords: []string{"foo"},
			Threats:        []sessionaudit.Threat{{Type: "x", Severity: 5}},
			Decision:       sessionaudit.DecisionNeedApproval,
		},
	}
	if err := notifier.NotifyApproval(context.Background(), record); err != nil {
		t.Fatalf("NotifyApproval: %v", err)
	}
	if len(lark.sentCards) != 1 {
		t.Fatalf("expected 1 lark card, got %d", len(lark.sentCards))
	}
	if len(ding.sentCards) != 1 {
		t.Fatalf("expected 1 dingtalk card, got %d", len(ding.sentCards))
	}

	card := lark.sentCards[0]
	if card.Metadata["channel"] != string(ChannelLark) {
		t.Errorf("expected channel=lark, got %v", card.Metadata["channel"])
	}
	recs, ok := card.Metadata["recipients"].([]string)
	if !ok || len(recs) != 1 || recs[0] != "ou_1" {
		t.Errorf("lark recipient missing: %+v", card.Metadata["recipients"])
	}
	if card.Metadata["approval_id"] != "appr_1" {
		t.Errorf("approval_id missing in metadata")
	}
}

func TestNotifier_NoApprovers(t *testing.T) {
	notifier, err := buildNotifier(newMockChannel("lark"), nil, NewEmptyRoutingTable(), nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	record := &sessionaudit.ApprovalRecord{
		ID: "a1", SessionID: "s1", TenantID: "t1",
		DetectResult: &sessionaudit.DetectResult{Score: 8},
	}
	err = notifier.NotifyApproval(context.Background(), record)
	if err == nil || !strings.Contains(err.Error(), "no approvers") {
		t.Errorf("expected no approvers error, got %v", err)
	}
}

func TestNotifier_PartialChannelFailure(t *testing.T) {
	good := newMockChannel("dingtalk")
	bad := newMockChannel("wechat")
	bad.sendErr = errors.New("network down")

	routing := NewApprovalRoutingTable(RoutingRules{
		{
			TenantID: "t1", RiskLevel: RiskLevelHigh, Enabled: true,
			Recipients: []Recipient{
				{ID: "u1", DingTalkUserID: "dt_1"},
				{ID: "u2", WeChatUserID: "wc_1"},
			},
		},
	})
	notifier, err := buildNotifier(nil, good, routing, nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	notifier.RegisterChannel(ChannelDingTalk, good)
	notifier.RegisterChannel(ChannelWeChat, bad)
	notifier.RegisterChannel(ChannelLark, newMockChannel("lark"))

	record := &sessionaudit.ApprovalRecord{
		ID: "a1", SessionID: "s1", TenantID: "t1",
		DetectResult: &sessionaudit.DetectResult{Score: 8},
	}
	if err := notifier.NotifyApproval(context.Background(), record); err != nil {
		t.Errorf("partial failure should not return error, got %v", err)
	}
	if len(good.sentCards) != 1 {
		t.Errorf("good channel should receive card, got %d", len(good.sentCards))
	}
	if len(bad.sentCards) != 0 {
		t.Errorf("bad channel should not have cards recorded (error returned)")
	}
}

func TestNotifier_HandleApprovalCallback_Approve(t *testing.T) {
	mock := newMockChannel("lark")
	store := newFakeApprovalStore()
	resumer := &mockResumer{}
	notifier, err := buildNotifier(mock, nil, NewApprovalRoutingTable(nil), nil, resumer, store)
	if err != nil {
		t.Fatal(err)
	}

	cb := &Callback{
		Action:    "approve",
		User:      CallbackUser{OpenID: "ou_1", Name: "张三"},
		TenantID:  "t1",
		SessionID: "s1",
		Data: map[string]any{
			"approval_id": "appr_1",
		},
	}
	if err := notifier.HandleApprovalCallback(context.Background(), cb); err != nil {
		t.Fatalf("HandleApprovalCallback: %v", err)
	}
	if len(mock.sentMessages) != 1 {
		t.Errorf("expected confirmation message, got %d", len(mock.sentMessages))
	}
	if store.approved["appr_1"] == "" {
		t.Error("fake store should record approval")
	}
	if len(resumer.calls) != 1 || resumer.calls[0] != "t1/s1" {
		t.Errorf("resumer should be called with t1/s1, got %v", resumer.calls)
	}
}

func TestNotifier_HandleApprovalCallback_Reject_NoResume(t *testing.T) {
	mock := newMockChannel("lark")
	store := newFakeApprovalStore()
	resumer := &mockResumer{}
	notifier, err := buildNotifier(mock, nil, NewApprovalRoutingTable(nil), nil, resumer, store)
	if err != nil {
		t.Fatal(err)
	}

	cb := &Callback{
		Action: "reject",
		User:   CallbackUser{OpenID: "ou_1", Name: "李四"},
		Data: map[string]any{
			"approval_id": "appr_1",
			"tenant_id":   "t1",
			"session_id":  "s1",
		},
	}
	if err := notifier.HandleApprovalCallback(context.Background(), cb); err != nil {
		t.Fatalf("HandleApprovalCallback: %v", err)
	}
	if store.rejected["appr_1"] == "" {
		t.Error("fake store should record rejection")
	}
	if len(resumer.calls) != 0 {
		t.Errorf("reject should not trigger resumer, got %v", resumer.calls)
	}
}

func TestNotifier_HandleApprovalCallback_UnknownAction(t *testing.T) {
	notifier, err := buildNotifier(newMockChannel("lark"), nil, NewApprovalRoutingTable(nil), nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	cb := &Callback{Action: "wat", Data: map[string]any{"approval_id": "x"}}
	if err := notifier.HandleApprovalCallback(context.Background(), cb); err == nil {
		t.Error("expected unknown action error")
	}
}

func TestNotifier_HandleApprovalCallback_MissingApprovalID(t *testing.T) {
	notifier, err := buildNotifier(newMockChannel("lark"), nil, NewApprovalRoutingTable(nil), nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	cb := &Callback{Action: "approve", Data: map[string]any{}}
	if err := notifier.HandleApprovalCallback(context.Background(), cb); err == nil {
		t.Error("expected missing approval_id error")
	}
}

func TestNotifier_HandleApprovalCallback_Detail(t *testing.T) {
	mock := newMockChannel("lark")
	store := newFakeApprovalStore()
	store.records["appr_1"] = &sessionaudit.ApprovalRecord{
		ID: "appr_1", SessionID: "sess_1", TenantID: "tenant_001", RequestID: "req_1",
		Status: sessionaudit.ApprovalPending,
		DetectResult: &sessionaudit.DetectResult{
			Score:          8,
			SensitiveWords: []string{"foo"},
			Threats:        []sessionaudit.Threat{{Type: "x", Severity: 5}},
			Decision:       sessionaudit.DecisionNeedApproval,
		},
		Snapshot:  &sessionaudit.RequestSnapshot{ClientModel: "gpt-4", ClientInfo: sessionaudit.ClientInfo{IP: "1.2.3.4"}},
		CreatedAt: time.Now(),
	}
	notifier, err := buildNotifier(mock, nil, NewApprovalRoutingTable(nil), nil, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	cb := &Callback{
		Action: "detail",
		User:   CallbackUser{OpenID: "ou_1", Name: "审"},
		Data:   map[string]any{"approval_id": "appr_1"},
	}
	if err := notifier.HandleApprovalCallback(context.Background(), cb); err != nil {
		t.Fatalf("HandleApprovalCallback: %v", err)
	}
	if len(mock.sentCards) != 1 {
		t.Errorf("expected 1 detail card, got %d", len(mock.sentCards))
	}
}

func TestNotifier_HandleApprovalCallback_NilCallback(t *testing.T) {
	notifier, err := buildNotifier(newMockChannel("lark"), nil, NewApprovalRoutingTable(nil), nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.HandleApprovalCallback(context.Background(), nil); err == nil {
		t.Error("expected nil callback error")
	}
}

func TestNotifier_NilRecord(t *testing.T) {
	notifier, err := buildNotifier(newMockChannel("lark"), nil, NewApprovalRoutingTable(nil), nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.NotifyApproval(context.Background(), nil); err == nil {
		t.Error("expected nil record error")
	}
}

func TestNotifier_NilDetectResult(t *testing.T) {
	notifier, err := buildNotifier(newMockChannel("lark"), nil, NewApprovalRoutingTable(nil), nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	record := &sessionaudit.ApprovalRecord{ID: "a", SessionID: "s", TenantID: "t"}
	if err := notifier.NotifyApproval(context.Background(), record); err == nil {
		t.Error("expected missing DetectResult error")
	}
}

func TestNotifier_AllChannelsFail(t *testing.T) {
	bad1 := newMockChannel("lark")
	bad1.sendErr = errors.New("down1")
	bad2 := newMockChannel("dingtalk")
	bad2.sendErr = errors.New("down2")

	routing := NewApprovalRoutingTable(RoutingRules{
		{
			TenantID: "t1", RiskLevel: RiskLevelHigh, Enabled: true,
			Recipients: []Recipient{
				{ID: "u1", LarkOpenID: "ou_1", DingTalkUserID: "dt_1"},
			},
		},
	})
	notifier, err := buildNotifier(bad1, bad2, routing, nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	record := &sessionaudit.ApprovalRecord{
		ID: "a", SessionID: "s", TenantID: "t1",
		DetectResult: &sessionaudit.DetectResult{Score: 8},
	}
	err = notifier.NotifyApproval(context.Background(), record)
	if err == nil {
		t.Error("expected all-channels-failed error")
	}
	if !strings.Contains(err.Error(), "all channels failed") {
		t.Errorf("expected aggregated error, got %v", err)
	}
}

func TestNotifier_DefaultChannelFallback(t *testing.T) {
	// 测试：当 routing 表中没有 channel 字段时，approvers 只有 LarkOpenID，
	// 应自动 fallback 到默认 lark 渠道。
	mock := newMockChannel("lark")
	routing := NewApprovalRoutingTable(RoutingRules{
		{
			TenantID: "t1", RiskLevel: RiskLevelHigh, Enabled: true,
			Recipients: []Recipient{{ID: "u1", LarkOpenID: "ou_1"}},
		},
	})
	notifier, err := buildNotifier(mock, nil, routing, nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	record := &sessionaudit.ApprovalRecord{
		ID: "a", SessionID: "s", TenantID: "t1",
		DetectResult: &sessionaudit.DetectResult{Score: 8},
	}
	if err := notifier.NotifyApproval(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if len(mock.sentCards) != 1 {
		t.Errorf("expected 1 fallback card")
	}
}

// buildNotifier 注入 mock 组件创建 notifier。
// defaultChannel 固定为 lark；其余渠道按需注册。
// lark==nil 时自动建一个 mock lark（多数测试需要 default channel）。
func buildNotifier(lark, dingtalk NotificationChannel, routing *ApprovalRoutingTable, summary SummarySource, resumer SessionResumer, store ApprovalStore) (*ApprovalNotifier, error) {
	chans := map[ChannelType]NotificationChannel{}
	if lark != nil {
		chans[ChannelLark] = lark
	} else {
		chans[ChannelLark] = newMockChannel("lark")
	}
	if dingtalk != nil {
		chans[ChannelDingTalk] = dingtalk
	}
	return NewApprovalNotifier(NotifierConfig{
		Channels:       chans,
		DefaultChannel: ChannelLark,
		Routing:        routing,
		Summary:        summary,
		Resumer:        resumer,
		ApprovalMgr:    store,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// CallbackHandler HTTP
// ─────────────────────────────────────────────────────────────────────────────

func TestCallbackHandler_URLVerification(t *testing.T) {
	mock := newMockChannel("lark")
	mock.parseErr = nil
	notifier, _ := buildNotifier(mock, nil, NewApprovalRoutingTable(nil), nil, nil, newFakeApprovalStore())
	h := NewCallbackHandler(notifier, nil)

	body := []byte(`{"challenge":"abc123"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/lark", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "abc123") {
		t.Errorf("expected challenge in response, got %s", rec.Body.String())
	}
}

func TestCallbackHandler_ApproveCallback(t *testing.T) {
	mock := newMockChannel("lark")
	notifier, _ := buildNotifier(mock, nil, NewApprovalRoutingTable(nil), nil, &mockResumer{}, newFakeApprovalStore())
	h := NewCallbackHandler(notifier, nil)

	body := []byte(`{
		"action":"approve",
		"tenant_id":"t1",
		"session_id":"s1",
		"user":{"open_id":"ou_1","name":"张三"},
		"approval_id":"appr_1"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/lark", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Errorf("expected success=true, got %s", rec.Body.String())
	}
}

func TestCallbackHandler_WrongMethod(t *testing.T) {
	mock := newMockChannel("lark")
	notifier, _ := buildNotifier(mock, nil, NewApprovalRoutingTable(nil), nil, nil, newFakeApprovalStore())
	h := NewCallbackHandler(notifier, nil)

	req := httptest.NewRequest(http.MethodGet, "/webhooks/lark", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestCallbackHandler_UnknownPath(t *testing.T) {
	mock := newMockChannel("lark")
	notifier, _ := buildNotifier(mock, nil, NewApprovalRoutingTable(nil), nil, nil, newFakeApprovalStore())
	h := NewCallbackHandler(notifier, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/unknown", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Lark / DingTalk / WeChat card conversion (pure functions)
// ─────────────────────────────────────────────────────────────────────────────

func TestLarkBotChannel_ConvertToLarkCard(t *testing.T) {
	c := NewLarkBotChannel(LarkBotConfig{AppID: "x", AppSecret: "y"})
	card := &InteractiveCard{
		Header:   CardHeader{Title: "标题", Template: "blue"},
		Elements: []CardElement{{Type: ElementTypeText, Text: "hello"}},
		Actions:  []CardAction{{ID: "approve", Text: "批准", Style: "primary"}},
	}
	out := c.convertToLarkCard(card)
	header, ok := out["header"].(map[string]any)
	if !ok {
		t.Fatal("missing header")
	}
	title := header["title"].(map[string]any)
	if title["content"] != "标题" {
		t.Errorf("title content wrong: %v", title["content"])
	}
	elements := out["elements"].([]map[string]any)
	if len(elements) < 2 {
		t.Errorf("expected text element + action group, got %d", len(elements))
	}
	last := elements[len(elements)-1]
	if last["tag"] != "action" {
		t.Errorf("last element should be action group, got %v", last["tag"])
	}
}

func TestDingTalk_PostSigned_UsesTimestampAndSign(t *testing.T) {
	// 仅校验 buildCardText 的渲染结果
	text := buildCardText(&InteractiveCard{
		Header: CardHeader{Title: "审批"},
		Elements: []CardElement{
			{Type: ElementTypeText, Text: "hello"},
			{Type: ElementTypeField, Fields: []CardField{{Key: "k", Value: "v", Short: true}}},
			{Type: ElementTypeDivider},
			{Type: ElementTypeNote, Text: "note"},
		},
	})
	for _, must := range []string{"# 审批", "hello", "**k**: v", "---", "> note"} {
		if !strings.Contains(text, must) {
			t.Errorf("expected %q in:\n%s", must, text)
		}
	}
}

func TestComputeDingTalkSign_FormatStable(t *testing.T) {
	// 仅校验钉钉加签格式（base64 of hmac-sha256）
	ch := NewDingTalkChannel(DingTalkConfig{SignSecret: "abc"})
	sig := ch.computeSign("1234567890")
	if sig == "" {
		t.Fatal("empty signature")
	}
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || len(decoded) != 32 {
		t.Errorf("signature should decode to 32 bytes, got %d (err=%v)", len(decoded), err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HealthCheck aggregation
// ─────────────────────────────────────────────────────────────────────────────

func TestNotifier_HealthCheck_Aggregation(t *testing.T) {
	good := newMockChannel("lark")
	bad := newMockChannel("dingtalk")
	bad.healthErr = errors.New("down")

	notifier, err := buildNotifier(good, nil, NewApprovalRoutingTable(nil), nil, nil, newFakeApprovalStore())
	if err != nil {
		t.Fatal(err)
	}
	notifier.RegisterChannel(ChannelDingTalk, bad)
	for _, ch := range []ChannelType{ChannelLark, ChannelDingTalk} {
		_ = notifier.cfg.Channels[ch].HealthCheck(context.Background())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Truncate / joinStrings
// ─────────────────────────────────────────────────────────────────────────────

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string should not truncate: %s", got)
	}
	if got := truncate("helloworld", 5); got != "hello..." {
		t.Errorf("truncate: %s", got)
	}
	if got := truncate("", 5); got != "" {
		t.Errorf("empty: %s", got)
	}
	if got := truncate("x", 0); got != "x" {
		t.Errorf("max<=0 means no truncate: %s", got)
	}
}

func TestJoinStrings(t *testing.T) {
	if got := joinStrings([]string{"a", "b", "c"}, ", "); got != "a, b, c" {
		t.Errorf("got %s", got)
	}
	if got := joinStrings([]string{}, ", "); got != "" {
		t.Errorf("empty: %s", got)
	}
	if got := joinStrings([]string{"only"}, ", "); got != "only" {
		t.Errorf("single: %s", got)
	}
}

// 抑制 unused import 警告（fmt 在 test helper 中间接使用）
var _ = fmt.Sprintf
