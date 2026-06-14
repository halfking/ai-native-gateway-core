package relay

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/telemetry"
)

func TestRequestLogContext_BuildFailureEntry_CompleteMeta(t *testing.T) {
	ch := NewChatHandler(circuit.NewManager(), limiter.New(), nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-4-flash"}`))
	r.Header.Set("X-Gw-Task-Id", "task-abc")
	r.Header.Set("X-Gw-Session-Id", "ses-xyz")
	r.Header.Set("X-Device-Seed", "device-1")
	owner := "bob"
	profile := "cursor"
	keyInfo := &auth.KeyInfo{
		ID:                   3,
		TenantID:             "default",
		ApplicationCode:      "openclaw",
		KeyPrefix:            "sk-live123",
		OwnerUser:            &owner,
		DefaultClientProfile: &profile,
	}

	ctx := ch.NewRequestLogContext(r, "req-pipe-1", time.Now().Add(-50*time.Millisecond))
	ctx.SetKey(keyInfo)
	ctx.Body = []byte(`{"model":"glm-4-flash"}`)
	ctx.SetClientModel("glm-4-flash")

	entry := ctx.BuildFailureEntry("rate_limit_exceeded", "rate limit exceeded", nil, nil)
	if entry == nil {
		t.Fatal("nil entry")
	}
	if entry.ClientModel == nil || *entry.ClientModel != "glm-4-flash" {
		t.Fatalf("client_model=%v", entry.ClientModel)
	}
	if entry.GwTaskID == nil || *entry.GwTaskID != "task-abc" {
		t.Fatalf("gw_task_id=%v", entry.GwTaskID)
	}
	if entry.APIKeyOwnerUser == nil || *entry.APIKeyOwnerUser != "bob" {
		t.Fatalf("owner=%v", entry.APIKeyOwnerUser)
	}
	if entry.ApplicationCode == nil || *entry.ApplicationCode != "openclaw" {
		t.Fatalf("app=%v", entry.ApplicationCode)
	}
}

func TestRequestLogContext_EmitFailure_Hook(t *testing.T) {
	var captured *telemetry.RequestLogEntry
	ch := NewChatHandler(circuit.NewManager(), limiter.New(), nil, nil, nil, nil)
	ch.SetRequestLogHook(func(e *telemetry.RequestLogEntry) { captured = e })

	r := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := ch.NewRequestLogContext(r, "req-pipe-2", time.Now())
	ctx.SetError("missing_key", "missing api key")
	ctx.EmitFailure(ctx.ErrCode, ctx.ErrMsg, nil, nil)

	if captured == nil {
		t.Fatal("hook not fired")
	}
	if captured.APIKeyPrefix == nil || *captured.APIKeyPrefix != "无key" {
		t.Fatalf("prefix=%v", captured.APIKeyPrefix)
	}
	if captured.RequestMode == nil || *captured.RequestMode != "anthropic" {
		t.Fatalf("mode=%v", captured.RequestMode)
	}
	if !ctx.IsLogged() {
		t.Fatal("should be marked logged")
	}
}
