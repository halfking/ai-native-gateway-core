package sessionaudit

import (
	"testing"
	"time"
)

// TestSessionAuditEvent_Interface 实现 eventbus.Event 接口。
func TestSessionAuditEvent_Interface(t *testing.T) {
	now := time.Now()
	e := &SessionAuditEvent{timestamp: now}
	if e.Type() != EventTypeSessionAudit {
		t.Errorf("Type()=%s, want %s", e.Type(), EventTypeSessionAudit)
	}
	if !e.Timestamp().Equal(now) {
		t.Errorf("Timestamp()=%v, want %v", e.Timestamp(), now)
	}
}

func TestApprovalNeededEvent_Interface(t *testing.T) {
	e := &ApprovalNeededEvent{timestamp: time.Now()}
	if e.Type() != EventTypeApprovalNeeded {
		t.Errorf("Type()=%s, want %s", e.Type(), EventTypeApprovalNeeded)
	}
	if e.Timestamp().IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestApprovalDecidedEvent_Interface(t *testing.T) {
	e := &ApprovalDecidedEvent{timestamp: time.Now()}
	if e.Type() != EventTypeApprovalDecided {
		t.Errorf("Type()=%s, want %s", e.Type(), EventTypeApprovalDecided)
	}
	if e.Timestamp().IsZero() {
		t.Error("Timestamp should be set")
	}
}

// TestAllEventsCarryTenantID 三个事件都必须带 TenantID（多租户前提）。
func TestAllEventsCarryTenantID(t *testing.T) {
	a := &SessionAuditEvent{TenantID: "t1"}
	if a.TenantID != "t1" {
		t.Error("SessionAuditEvent lost TenantID")
	}
	b := &ApprovalNeededEvent{TenantID: "t2"}
	if b.TenantID != "t2" {
		t.Error("ApprovalNeededEvent lost TenantID")
	}
	// ApprovalDecidedEvent 不直接带 tenant_id（按 approval_id 索引），
	// 这是设计意图，但需要 audit worker 二次 join。
}

// TestDecision_String 决策的字符串稳定性。
func TestDecision_String(t *testing.T) {
	cases := map[Decision]string{
		DecisionPass:         "pass",
		DecisionWarn:         "warn",
		DecisionBlock:        "block",
		DecisionNeedApproval: "need_approval",
	}
	for d, want := range cases {
		if string(d) != want {
			t.Errorf("Decision %s != %s", d, want)
		}
	}
	// String() 方法覆盖
	if got := DecisionPass.String(); got != "pass" {
		t.Errorf("Pass.String()=%s", got)
	}
}

// TestApprovalStatus_String 审批状态字符串。
func TestApprovalStatus_String(t *testing.T) {
	cases := map[ApprovalStatus]string{
		ApprovalPending:  "pending",
		ApprovalApproved: "approved",
		ApprovalRejected: "rejected",
		ApprovalTimeout:  "timeout",
	}
	for s, want := range cases {
		if string(s) != want {
			t.Errorf("ApprovalStatus %s != %s", s, want)
		}
	}
}
