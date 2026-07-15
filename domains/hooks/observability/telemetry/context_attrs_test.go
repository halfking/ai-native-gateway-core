package telemetry

import (
	"context"
	"encoding/json"
	"testing"
)

func TestContextAttrsEntry_ApplyAttrsFromContext(t *testing.T) {
	t.Run("populates all known attrs from ctx", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.identity_hash"), "abc123def4567890")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.virtual_client_id"), "vc-abc123def4567890")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.agent_name"), "claude-code")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.agent_type"), "cli")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.client_ip"), "203.0.113.5")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.client_forwarded_for"), "203.0.113.5, 10.0.0.1")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.client_protocol"), "openai-chat")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.project_id"), "kxpms-go-api")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.source_channel"), "agent")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.origin_stage"), "business")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.api_key_fingerprint"), "abcdef0123456789")
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.customer_id"), int64(42))
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.client_request_id"), "req-xyz-1")

		e := &ContextAttrsEntry{RequestID: "test-1"}
		e.ApplyAttrsFromContext(ctx)

		if e.IdentityHash == nil || *e.IdentityHash != "abc123def4567890" {
			t.Errorf("IdentityHash not populated: %v", e.IdentityHash)
		}
		if e.VirtualClientID == nil || *e.VirtualClientID != "vc-abc123def4567890" {
			t.Errorf("VirtualClientID not populated: %v", e.VirtualClientID)
		}
		if e.AgentName == nil || *e.AgentName != "claude-code" {
			t.Errorf("AgentName not populated: %v", e.AgentName)
		}
		if e.AgentType == nil || *e.AgentType != "cli" {
			t.Errorf("AgentType not populated: %v", e.AgentType)
		}
		if e.ClientIP == nil || *e.ClientIP != "203.0.113.5" {
			t.Errorf("ClientIP not populated: %v", e.ClientIP)
		}
		if e.ClientForwardedFor == nil || *e.ClientForwardedFor != "203.0.113.5, 10.0.0.1" {
			t.Errorf("ClientForwardedFor not populated: %v", e.ClientForwardedFor)
		}
		if e.ClientProtocol == nil || *e.ClientProtocol != "openai-chat" {
			t.Errorf("ClientProtocol not populated: %v", e.ClientProtocol)
		}
		if e.ProjectID == nil || *e.ProjectID != "kxpms-go-api" {
			t.Errorf("ProjectID not populated: %v", e.ProjectID)
		}
		if e.SourceChannel == nil || *e.SourceChannel != "agent" {
			t.Errorf("SourceChannel not populated: %v", e.SourceChannel)
		}
		if e.OriginStage == nil || *e.OriginStage != "business" {
			t.Errorf("OriginStage not populated: %v", e.OriginStage)
		}
		if e.APIKeyFingerprint == nil || *e.APIKeyFingerprint != "abcdef0123456789" {
			t.Errorf("APIKeyFingerprint not populated: %v", e.APIKeyFingerprint)
		}
		if e.CustomerID == nil || *e.CustomerID != 42 {
			t.Errorf("CustomerID not populated: %v", e.CustomerID)
		}
		if e.ClientRequestID == nil || *e.ClientRequestID != "req-xyz-1" {
			t.Errorf("ClientRequestID not populated: %v", e.ClientRequestID)
		}
	})

	t.Run("nil ctx and nil entry are safe", func(t *testing.T) {
		e := &ContextAttrsEntry{RequestID: "test-2"}
		e.ApplyAttrsFromContext(nil)
		if e.IdentityHash != nil {
			t.Errorf("nil ctx should not populate entry")
		}
		var nilEntry *ContextAttrsEntry
		nilEntry.ApplyAttrsFromContext(context.Background()) // no panic
	})

	t.Run("first-write-wins: pre-set fields are not overwritten", func(t *testing.T) {
		preExisting := "pre-existing-id"
		e := &ContextAttrsEntry{
			RequestID:   "test-3",
			IdentityHash: &preExisting,
		}
		ctx := context.Background()
		ctx = context.WithValue(ctx, AttrsCtxKey("attrs.identity_hash"), "from-ctx")
		e.ApplyAttrsFromContext(ctx)
		if e.IdentityHash == nil || *e.IdentityHash != "pre-existing-id" {
			t.Errorf("first-write-wins violated: got %v", e.IdentityHash)
		}
	})

	t.Run("empty string ctx values do not overwrite", func(t *testing.T) {
		preExisting := "kept"
		e := &ContextAttrsEntry{
			RequestID:   "test-4",
			IdentityHash: &preExisting,
		}
		ctx := context.WithValue(context.Background(), AttrsCtxKey("attrs.identity_hash"), "")
		e.ApplyAttrsFromContext(ctx)
		if e.IdentityHash == nil || *e.IdentityHash != "kept" {
			t.Errorf("empty ctx value should not overwrite: got %v", e.IdentityHash)
		}
	})
}

func TestContextAttrsEntry_FingerprintRawJSON(t *testing.T) {
	raw := json.RawMessage(`{"device_seed":"abc","user_agent":"claude-code/1.0"}`)
	e := &ContextAttrsEntry{
		RequestID:     "test-5",
		FingerprintRaw: raw,
	}
	if string(e.FingerprintRaw) != string(raw) {
		t.Errorf("FingerprintRaw mismatch: got %s", e.FingerprintRaw)
	}
}