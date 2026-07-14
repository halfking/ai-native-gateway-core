package stats

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestFromTelemetryEntry_skipsInProgress(t *testing.T) {
	status := telemetry.RequestStatusInProgress
	_, _, _, ok := FromTelemetryEntry(&telemetry.RequestLogEntry{
		Op:            telemetry.RequestLogUpdate,
		RequestStatus: &status,
		TenantID:      "default",
	}, testNow())
	if ok {
		t.Fatal("expected in_progress to be skipped")
	}
}

func TestFromTelemetryEntry_completedSuccess(t *testing.T) {
	status := telemetry.RequestStatusSuccess
	prompt := 100
	completion := 50
	credits := int64(12)
	client := "opencode"
	main, dims, drills, ok := FromTelemetryEntry(&telemetry.RequestLogEntry{
		Op:               telemetry.RequestLogUpdate,
		RequestStatus:    &status,
		TenantID:         "tenant-a",
		Success:          true,
		PromptTokens:     &prompt,
		CompletionTokens: &completion,
		CreditsCharged:   &credits,
		ClientProfile:    &client,
	}, testNow())
	if !ok {
		t.Fatal("expected ok")
	}
	if main.Requests != 1 || main.SuccessCount != 1 {
		t.Fatalf("main counts: %+v", main)
	}
	if main.CreditsCharged != 12 {
		t.Fatalf("credits=%d", main.CreditsCharged)
	}
	if len(dims) < 4 {
		t.Fatalf("expected dims, got %d", len(dims))
	}
	if len(drills) != 0 {
		t.Fatalf("success should not produce drills")
	}
}

func TestFromTelemetryEntry_failureProducesDrill(t *testing.T) {
	status := telemetry.RequestStatusFailure
	errKind := "rate_limit"
	model := "gpt-4"
	_, _, drills, ok := FromTelemetryEntry(&telemetry.RequestLogEntry{
		Op:            telemetry.RequestLogUpdate,
		RequestStatus: &status,
		TenantID:      "default",
		Success:       false,
		ErrorKind:     &errKind,
		OutboundModel: &model,
	}, testNow())
	if !ok || len(drills) != 1 {
		t.Fatalf("expected one drill row, ok=%v drills=%d", ok, len(drills))
	}
	if drills[0].ErrorKind != "rate_limit" {
		t.Fatalf("drill=%+v", drills[0])
	}
}

func testNow() time.Time {
	return time.Date(2026, 7, 14, 12, 34, 56, 0, time.UTC)
}
