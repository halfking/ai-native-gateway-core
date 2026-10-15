package reqprobe

import (
	"testing"
)

func TestDiagnosePydanticExtraForbiddenLoc(t *testing.T) {
	body := []byte(`{"detail":[{"type":"extra_forbidden","loc":["body","service_tier"],"msg":"Extra inputs are not permitted","input":"flex"}]}`)
	outbound := []byte(`{"model":"gpt","service_tier":"flex","messages":[]}`)
	d, ok := Diagnose(Input{
		HTTPStatus:   422,
		ErrorBody:    body,
		OutboundBody: outbound,
		ErrorKind:    "client_bug",
		Protocol:     "openai-chat",
	})
	if !ok || d.Trigger != TriggerParamRejected || d.Param != "service_tier" {
		t.Fatalf("Diagnose = (%+v,%v), want param_rejected service_tier", d, ok)
	}
}

func TestDiagnosePydanticLocAbsentFromOutboundFallsBackToFirst(t *testing.T) {
	body := []byte(`{"detail":[{"type":"extra_forbidden","loc":["body","verbosity"],"msg":"Extra inputs are not permitted"}]}`)
	d, ok := Diagnose(Input{
		HTTPStatus:   422,
		ErrorBody:    body,
		OutboundBody: []byte(`{"model":"m"}`),
		ErrorKind:    "client_bug",
	})
	if !ok || d.Param != "verbosity" {
		t.Fatalf("Diagnose = (%+v,%v), want verbosity even if not in outbound", d, ok)
	}
}

// TestDiagnosePydanticErrorObjectLoc：raw-logs 捕获的 mock/中转形态
// error.type=pydantic + error.loc（非 detail[]）。rid=89b4a272…
func TestDiagnosePydanticErrorObjectLoc(t *testing.T) {
	body := []byte(`{"error":{"message":"Validation error","type":"pydantic","code":"extra_forbidden","loc":["body","service_tier"]}}`)
	d, ok := Diagnose(Input{
		HTTPStatus:   422,
		ErrorBody:    body,
		OutboundBody: []byte(`{"model":"mock-p0-e2e","service_tier":"auto","messages":[]}`),
		ErrorKind:    "client_bug",
		Protocol:     "openai-completions",
	})
	if !ok || d.Param != "service_tier" {
		t.Fatalf("Diagnose error.loc form = (%+v,%v), want service_tier", d, ok)
	}
}
