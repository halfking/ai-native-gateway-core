package handoff

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultExplicitRequiresTenantOptIn(t *testing.T) {
	settings := &stubSettings{}
	hook := NewTriggerHook(TriggerConfig{SettingsGetter: settings}, &memoryStore{})

	if hook.DefaultExplicit("tenant-a") {
		t.Fatal("handoff must default to transparent for clients without an explicit capability signal")
	}
	settings.set("handoff.client_mode", "explicit")
	if !hook.DefaultExplicit("tenant-a") {
		t.Fatal("tenant explicit mode must opt clients into the resume-packet protocol")
	}
}

func TestCommitRequestRequiresCreatedTargetSession(t *testing.T) {
	store := &memoryStore{}
	hook := NewTriggerHook(TriggerConfig{}, store)
	result := &RequestResult{Record: &HandoffRecord{SessionKey: "gw_old"}}

	hook.CommitRequest(context.Background(), result, "")
	if len(store.rows) != 0 || store.handoffCount != 0 {
		t.Fatalf("unconfirmed handoff must not be persisted, rows=%d count=%d", len(store.rows), store.handoffCount)
	}

	hook.CommitRequest(context.Background(), result, "gw_new")
	if len(store.rows) != 1 || store.handoffCount != 1 || store.rows[0].NewSessionID != "gw_new" {
		t.Fatalf("confirmed handoff not persisted correctly: rows=%+v count=%d", store.rows, store.handoffCount)
	}
}

func TestPrepareRequest_TransparentConfigDoesNotRewriteUpstreamBody(t *testing.T) {
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
	if err != nil || result != nil {
		t.Fatalf("automatic handoff without explicit client opt-in must leave the request unchanged, result=%+v err=%v", result, err)
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
	if !result.Explicit || len(result.Body) != 0 {
		t.Fatalf("manual handoff must return an explicit packet without an upstream body: %+v", result)
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

func TestPrepareRequest_RedactsSensitiveLLMSummaryEcho(t *testing.T) {
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeManual, SummaryEngine: SummaryLLM,
		MaxPerSession: 5, SettingsGetter: &stubSettings{},
		LLMCaller: fakeLLMCaller{out: "summary api_key=super-secret-value-123456"},
	}, &memoryStore{})
	result, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a",
		Body: []byte(`{"messages":[{"role":"user","content":"/handoff continue"}]}`), MessageCount: 1,
	})
	if err != nil || result == nil {
		t.Fatalf("expected handoff, result=%+v err=%v", result, err)
	}
	if strings.Contains(result.ResumePacket.Summary, "super-secret") || !strings.Contains(result.ResumePacket.Summary, "[redacted]") {
		t.Fatalf("LLM secret echo leaked into packet: %q", result.ResumePacket.Summary)
	}
	if result.Record == nil || strings.Contains(result.Record.SummaryText, "super-secret") {
		t.Fatalf("LLM secret echo leaked into record: %+v", result.Record)
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

func TestPrepareRequest_AnthropicDoesNotRewriteSystemField(t *testing.T) {
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
	if !result.Explicit || len(result.Body) != 0 {
		t.Fatalf("anthropic handoff must not rewrite the upstream system field: %+v", result)
	}
}
