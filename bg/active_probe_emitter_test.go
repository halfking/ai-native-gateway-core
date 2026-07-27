// bg/active_probe_emitter_test.go — unit tests for the probe telemetry
// emitter helpers, focused on the 2026-07-17 observability fixes:
//   - parseProbeUsage: replaces the hardcoded 1/0 token placeholder with
//     real usage parsed from the upstream response body
//
// The Emit() path itself needs a *telemetry.Client and is exercised
// end-to-end via the worker integration tests; here we cover the pure
// helpers that were previously untested.
package bg

import (
	"testing"
	"time"
)

func TestParseProbeUsage(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantPrompt int
		wantCompl  int
		wantOK     bool
	}{
		{
			name:       "openai usage",
			body:       `{"id":"x","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":1}}`,
			wantPrompt: 7, wantCompl: 1, wantOK: true,
		},
		{
			name:       "anthropic usage (input/output tokens)",
			body:       `{"id":"msg_x","usage":{"input_tokens":3,"output_tokens":2}}`,
			wantPrompt: 3, wantCompl: 2, wantOK: true,
		},
		{
			name:   "empty body",
			body:   "",
			wantOK: false,
		},
		{
			name:   "non-json body",
			body:   "Internal Server Error",
			wantOK: false,
		},
		{
			name:   "json without usage object",
			body:   `{"error":"rate limited"}`,
			wantOK: false,
		},
		{
			name:   "usage object with all-zero tokens",
			body:   `{"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pt, ct, ok := parseProbeUsage(c.body)
			if ok != c.wantOK {
				t.Fatalf("parseProbeUsage ok = %v, want %v", ok, c.wantOK)
			}
			if ok {
				if pt != c.wantPrompt || ct != c.wantCompl {
					t.Errorf("parseProbeUsage = (%d, %d), want (%d, %d)", pt, ct, c.wantPrompt, c.wantCompl)
				}
			}
		})
	}
}

func TestBuildProbeRequestLogEntry_PreservesProbeOriginAndDiagnostics(t *testing.T) {
	eventAt := time.Now()
	result := &ProbeResult{
		Status:       ProbeStatusHTTP5xx,
		HTTPStatus:   503,
		ErrCode:      "service_unavailable",
		ErrMsg:       "upstream unavailable",
		LatencyMs:    42,
		ResponseBody: `{"error":"overloaded"}`,
		RequestBody:  `{"model":"gpt-test","messages":[{"role":"user","content":"ping"}]}`,
		RequestURL:   "https://provider.example/v1/chat/completions",
		StartedAt:    eventAt.Add(-42 * time.Millisecond),
		CompletedAt:  eventAt,
		OriginStage:  "node_probe",
		OriginActor:  "node-probe-worker",
	}

	entry := buildProbeRequestLogEntry(7, 11, "default", "gpt-test", "gpt-test", "direct", "parent-123", 2, result)
	if entry == nil {
		t.Fatal("buildProbeRequestLogEntry returned nil")
	}
	if entry.OriginStage == nil || *entry.OriginStage != "node_probe" {
		t.Fatalf("origin_stage = %v, want node_probe", entry.OriginStage)
	}
	if entry.OriginActor == nil || *entry.OriginActor != "node-probe-worker" {
		t.Fatalf("origin_actor = %v, want node-probe-worker", entry.OriginActor)
	}
	if entry.TaskType == nil || *entry.TaskType != "probe_triggered" {
		t.Fatalf("task_type = %v, want probe_triggered", entry.TaskType)
	}
	if entry.TaskTypeChosen == nil || *entry.TaskTypeChosen != "probe_direct" {
		t.Fatalf("task_type_chosen = %v, want probe_direct", entry.TaskTypeChosen)
	}
	if entry.Success || entry.IsAutoRequest == nil || !*entry.IsAutoRequest {
		t.Fatalf("probe flags not populated: success=%v is_auto=%v", entry.Success, entry.IsAutoRequest)
	}
	if entry.ParentRequestID == nil || *entry.ParentRequestID != "parent-123" || entry.ClientRequestID == nil || *entry.ClientRequestID != "parent-123" {
		t.Fatalf("probe correlation not populated: parent=%v client=%v", entry.ParentRequestID, entry.ClientRequestID)
	}
	if entry.CompressionReason == nil || *entry.CompressionReason != "probe_correlation" {
		t.Fatalf("compression_reason = %v, want probe_correlation", entry.CompressionReason)
	}
	if entry.RequestBody == nil || *entry.RequestBody != result.RequestBody || entry.ResponseBody == nil || *entry.ResponseBody != result.ResponseBody {
		t.Fatalf("diagnostic bodies not preserved: request=%v response=%v", entry.RequestBody, entry.ResponseBody)
	}
}
