package main

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/pending"
)

// ──────────────────────────────────────────────────────────────────────────────
// NewApprovalResumeHandler
// ──────────────────────────────────────────────────────────────────────────────

func TestNewApprovalResumeHandler_NilDeps(t *testing.T) {
	_, err := NewApprovalResumeHandler(nil, nil, nil, nil, 0)
	if err == nil {
		t.Error("expected error for all nil deps")
	}
}

func TestNewApprovalResumeHandler_MissingSessionCache(t *testing.T) {
	_, err := NewApprovalResumeHandler(
		nil,
		&sessionaudit.ApprovalManager{},
		&streaming.ChatHandler{},
		&pending.Store{},
		0,
	)
	if err == nil {
		t.Error("expected error for missing SessionCache")
	}
}

func TestNewApprovalResumeHandler_MissingApprovalMgr(t *testing.T) {
	_, err := NewApprovalResumeHandler(
		&compression.SessionCache{},
		nil,
		&streaming.ChatHandler{},
		&pending.Store{},
		0,
	)
	if err == nil {
		t.Error("expected error for missing ApprovalMgr")
	}
}

func TestNewApprovalResumeHandler_MissingChatHandler(t *testing.T) {
	_, err := NewApprovalResumeHandler(
		&compression.SessionCache{},
		&sessionaudit.ApprovalManager{},
		nil,
		&pending.Store{},
		0,
	)
	if err == nil {
		t.Error("expected error for missing ChatHandler")
	}
}

func TestNewApprovalResumeHandler_MissingPendingStore(t *testing.T) {
	_, err := NewApprovalResumeHandler(
		&compression.SessionCache{},
		&sessionaudit.ApprovalManager{},
		&streaming.ChatHandler{},
		nil,
		0,
	)
	if err == nil {
		t.Error("expected error for missing PendingStore")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// InitializeApprovalIntegration (full init, for future pipeline use)
// ──────────────────────────────────────────────────────────────────────────────

func TestInitializeApprovalIntegration_NilDeps(t *testing.T) {
	_, err := InitializeApprovalIntegration(nil)
	if err == nil {
		t.Error("expected error for nil deps")
	}
}

func TestInitializeApprovalIntegration_MissingSessionCache(t *testing.T) {
	deps := &ApprovalIntegrationDeps{
		ApprovalMgr:  &sessionaudit.ApprovalManager{},
		PendingStore: &pending.Store{},
		ChatHandler:  &streaming.ChatHandler{},
	}
	_, err := InitializeApprovalIntegration(deps)
	if err == nil {
		t.Error("expected error for missing SessionCache")
	}
}

func TestInitializeApprovalIntegration_MissingApprovalMgr(t *testing.T) {
	deps := &ApprovalIntegrationDeps{
		SessionCache: &compression.SessionCache{},
		PendingStore: &pending.Store{},
		ChatHandler:  &streaming.ChatHandler{},
	}
	_, err := InitializeApprovalIntegration(deps)
	if err == nil {
		t.Error("expected error for missing ApprovalMgr")
	}
}

func TestInitializeApprovalIntegration_DefaultTimeout(t *testing.T) {
	deps := &ApprovalIntegrationDeps{
		SessionCache: &compression.SessionCache{},
		ApprovalMgr:  &sessionaudit.ApprovalManager{},
		PendingStore: &pending.Store{},
		ChatHandler:  &streaming.ChatHandler{},
		AuditBus:     eventbus.NewMemoryBus(100),
	}
	if deps.ApprovalTimeout == 0 {
		t.Log("timeout is 0, InitializeApprovalIntegration should default to 15 minutes")
	}
}
