package handoff

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrepareRequest_TransparentOpenAI(t *testing.T) {
	store := &memoryStore{tokenCount: 200_000, msgCount: 12}
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeAuto, AbsoluteThreshold: 180_000,
		MinMessages: 2, SummaryEngine: SummaryRule, MaxPerSession: 5,
		SettingsGetter: &stubSettings{},
	}, store)

	result, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a", ClientModel: "gpt-4o",
		Body:     []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Continue the migration in /srv/app"}]}`),
		Protocol: "openai", ContextWindow: 200_000, TokenEstimate: 1000, MessageCount: 12,
	})
	if err != nil || result == nil || !result.Triggered {
		t.Fatalf("expected transparent handoff, result=%+v err=%v", result, err)
	}
	if result.Explicit || !strings.HasPrefix(result.Reason, "absolute_threshold") {
		t.Fatalf("unexpected handoff result: %+v", result)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(result.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" {
		t.Fatalf("expected resume system message, got %+v", payload.Messages)
	}
	if !strings.Contains(payload.Messages[0].Content, "gateway-handoff-v1") || !strings.Contains(payload.Messages[0].Content, "Continue the migration") {
		t.Fatalf("resume packet lacks real context: %q", payload.Messages[0].Content)
	}
	if payload.Messages[1].Content != "Continue the migration in /srv/app" {
		t.Fatalf("user message changed: %q", payload.Messages[1].Content)
	}
}

func TestPrepareRequest_ManualSkillIsGatewayOnly(t *testing.T) {
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeManual, SkillName: "resume-work",
		MinMessages: 0, SummaryEngine: SummaryRule, MaxPerSession: 5, SettingsGetter: &stubSettings{},
	}, &memoryStore{})
	result, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a", Body: []byte(`{"messages":[{"role":"user","content":"/resume-work continue the failing build"}]}`),
		Protocol: "openai", MessageCount: 1,
	})
	if err != nil || result == nil || !strings.HasPrefix(result.Reason, "manual_skill:resume-work") {
		t.Fatalf("expected manual handoff, result=%+v err=%v", result, err)
	}
	if strings.Contains(string(result.Body), "/resume-work") {
		t.Fatalf("gateway-only skill leaked to upstream: %s", result.Body)
	}
}

func TestPrepareRequest_RedactsSensitiveRuleSummary(t *testing.T) {
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeManual, SummaryEngine: SummaryRule,
		MaxPerSession: 5, SettingsGetter: &stubSettings{},
	}, &memoryStore{})
	result, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a",
		Body:     []byte(`{"messages":[{"role":"user","content":"/handoff token Bearer secret-value-123456789012"}]}`),
		Protocol: "openai", MessageCount: 1,
	})
	if err != nil || result == nil {
		t.Fatalf("expected handoff, result=%+v err=%v", result, err)
	}
	if strings.Contains(result.ResumePacket.Summary, "secret-value") {
		t.Fatalf("sensitive token leaked into packet: %q", result.ResumePacket.Summary)
	}
}

func TestPrepareRequest_ExplicitReturnsPacketWithoutRewrite(t *testing.T) {
	store := &memoryStore{tokenCount: 200_000, msgCount: 12}
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, AbsoluteThreshold: 180_000, MinMessages: 2,
		SummaryEngine: SummaryRule, MaxPerSession: 5, SettingsGetter: &stubSettings{},
	}, store)
	body := []byte(`{"messages":[{"role":"user","content":"keep going"}]}`)
	result, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a", Body: body, Protocol: "openai",
		TokenEstimate: 1000, MessageCount: 12, Explicit: true,
	})
	if err != nil || result == nil || !result.Explicit || len(result.Body) != 0 {
		t.Fatalf("expected explicit packet only, result=%+v err=%v", result, err)
	}
	if result.ResumePacket.PreviousSession != "gw_old" || result.ResumePacket.Summary == "" {
		t.Fatalf("unexpected packet: %+v", result.ResumePacket)
	}
}

func TestPrepareRequest_AnthropicWritesSystemField(t *testing.T) {
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeManual, SkillName: "handoff",
		SummaryEngine: SummaryRule, MaxPerSession: 5, SettingsGetter: &stubSettings{},
	}, &memoryStore{})
	result, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a",
		Body:     []byte(`{"system":"original rules","messages":[{"role":"user","content":"/handoff continue"}]}`),
		Protocol: "anthropic-messages", MessageCount: 1,
	})
	if err != nil || result == nil {
		t.Fatalf("expected anthropic handoff, result=%+v err=%v", result, err)
	}
	var payload struct {
		System   string `json:"system"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(result.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.System, "original rules") || !strings.Contains(payload.System, "gateway-handoff-v1") {
		t.Fatalf("unexpected anthropic system: %q", payload.System)
	}
	if strings.Contains(payload.Messages[0].Content, "/handoff") {
		t.Fatalf("manual command leaked to upstream: %q", payload.Messages[0].Content)
	}
}
