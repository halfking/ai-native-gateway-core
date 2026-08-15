// wire_schema_test.go validates the outbox wire envelope against
// gateway-event-schema-v1.json — the event contract shared with
// ai-session-manager (GW-1.2, docs/全面优化v1/README.md).
//
// The schema file in testdata/ is the gateway-authoritative v1 fixture based on
// ai-session-manager/docs/全面优化v1/gateway-event-schema-v1.json, with the
// handoff-required discriminator `type`. The current SM copy still uses
// `event_type` and must be aligned separately before cross-repo E2E validation.
package outbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// eventSchemaID is the $id of gateway-event-schema-v1.json.
const eventSchemaID = "https://platform.example.com/schemas/gateway-events-v1.json"

// loadEventSchema compiles the composition of the upstream contract's
// EventEnvelope definition and its root oneOf (payload-per-type). The
// upstream root schema alone only pins the payload branch; the wrapper lives
// in testdata/gateway-event-envelope-validator.json and references the
// upstream document by its $id, which is registered via AddResource so no
// network fetch happens.
func loadEventSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "gateway-event-schema-v1.json"))
	if err != nil {
		t.Fatalf("read gateway-event-schema-v1.json: %v", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal gateway-event-schema-v1.json: %v", err)
	}
	c := jsonschema.NewCompiler()
	// Assert date-time formats (occurred_at) instead of ignoring them.
	c.AssertFormat()
	if err := c.AddResource(eventSchemaID, doc); err != nil {
		t.Fatalf("register gateway-event-schema-v1.json: %v", err)
	}
	sch, err := c.Compile(filepath.Join("testdata", "gateway-event-envelope-validator.json"))
	if err != nil {
		t.Fatalf("compile gateway-event-envelope-validator.json: %v", err)
	}
	return sch
}

func validateAgainstSchema(t *testing.T, sch *jsonschema.Schema, doc []byte) error {
	t.Helper()
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatalf("unmarshal wire envelope: %v", err)
	}
	return sch.Validate(v)
}

// TestRenderWireEnvelope_ValidatesAgainstV1Schema pins the core GW-1.2
// contract: both the success and the error flavour of request.completed.v1
// built by the write path must validate against the frozen schema.
func TestRenderWireEnvelope_ValidatesAgainstV1Schema(t *testing.T) {
	sch := loadEventSchema(t)

	cost := 0.0012
	envSuccess, err := BuildRequestCompletedEventV3(
		"tenant-123", "session-abc", 1,
		"request-001", "corr-xyz", "request-001",
		"anthropic", "claude-3-5-sonnet-20241022", "succeeded",
		150, 80, 1250, &cost, true,
	)
	if err != nil {
		t.Fatalf("BuildRequestCompletedEventV3 failed: %v", err)
	}

	envError, err := BuildRequestCompletedEventV3(
		"tenant-123", "session-abc", 2,
		"request-002", "", "",
		"anthropic", "claude-3-5-sonnet-20241022", "upstream_timeout",
		0, 0, 30000, nil, false,
	)
	if err != nil {
		t.Fatalf("BuildRequestCompletedEventV3 failed: %v", err)
	}

	for name, env := range map[string]EventEnvelope{
		"success": envSuccess,
		"error":   envError,
	} {
		t.Run(name, func(t *testing.T) {
			wire, err := RenderWireEnvelope(env)
			if err != nil {
				t.Fatalf("RenderWireEnvelope failed: %v", err)
			}
			if err := validateAgainstSchema(t, sch, wire); err != nil {
				t.Errorf("wire envelope does not validate against gateway-event-schema-v1.json: %v\nwire: %s", err, wire)
			}
		})
	}
}

// TestRenderWireEnvelope_V1FieldShape asserts the envelope-level v1 fields
// (schema_version "1.0", type, correlation_id, request_id, session_id, occurred_at)
// without relying on the schema compiler's error strings.
func TestRenderWireEnvelope_V1FieldShape(t *testing.T) {
	env, err := BuildRequestCompletedEventV3(
		"tenant-123", "session-abc", 1,
		"request-001", "corr-xyz", "idem-abc",
		"anthropic", "claude-test", "succeeded",
		10, 5, 100, nil, true,
	)
	if err != nil {
		t.Fatalf("BuildRequestCompletedEventV3 failed: %v", err)
	}

	wire, err := RenderWireEnvelope(env)
	if err != nil {
		t.Fatalf("RenderWireEnvelope failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(wire, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed["schema_version"] != "1.0" {
		t.Errorf("schema_version = %v, want \"1.0\"", parsed["schema_version"])
	}
	if parsed["event_id"] != env.EventID {
		t.Errorf("event_id = %v, want %q", parsed["event_id"], env.EventID)
	}
	if !strings.HasPrefix(env.EventID, "evt-") {
		t.Errorf("event_id %q must match ^evt-[a-zA-Z0-9_-]+$", env.EventID)
	}
	if parsed["type"] != "request.completed.v1" {
		t.Errorf("type = %v, want request.completed.v1", parsed["type"])
	}
	if _, exists := parsed["event_type"]; exists {
		t.Error("wire envelope must use type, not event_type")
	}
	if parsed["tenant_id"] != "tenant-123" {
		t.Errorf("tenant_id = %v, want tenant-123", parsed["tenant_id"])
	}
	if parsed["session_id"] != "session-abc" {
		t.Errorf("session_id = %v, want session-abc", parsed["session_id"])
	}
	if parsed["request_id"] != "request-001" {
		t.Errorf("request_id = %v, want request-001", parsed["request_id"])
	}
	if parsed["correlation_id"] != "corr-xyz" {
		t.Errorf("correlation_id = %v, want corr-xyz", parsed["correlation_id"])
	}
	if _, ok := parsed["occurred_at"].(string); !ok {
		t.Errorf("occurred_at = %v, want RFC3339 string", parsed["occurred_at"])
	}
	if _, ok := parsed["payload"].(map[string]any); !ok {
		t.Error("payload must be an object")
	}
}

// TestRenderWireEnvelope_LegacyRows tests pre-v1 envelopes (IDs only inside
// the payload, like rows written by BuildRequestCompletedEventV2): the wire
// renderer must lift them to the envelope top level via the payload fallback
// so old outbox rows still dispatch with correlation info.
func TestRenderWireEnvelope_LegacyRows(t *testing.T) {
	env, err := BuildRequestCompletedEventV2(
		"tenant-123", "session-abc", 1,
		"request-legacy", "", "",
		"anthropic", "claude-test", "succeeded",
		10, 5, 100, true,
	)
	if err != nil {
		t.Fatalf("BuildRequestCompletedEventV2 failed: %v", err)
	}

	wire, err := RenderWireEnvelope(env)
	if err != nil {
		t.Fatalf("RenderWireEnvelope failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(wire, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed["schema_version"] != "1.0" {
		t.Errorf("schema_version = %v, want \"1.0\" for legacy rows too", parsed["schema_version"])
	}
	if parsed["request_id"] != "request-legacy" {
		t.Errorf("request_id fallback = %v, want request-legacy", parsed["request_id"])
	}
	if parsed["session_id"] != "session-abc" {
		t.Errorf("session_id fallback = %v, want session-abc", parsed["session_id"])
	}
	if parsed["correlation_id"] != "request-legacy" {
		t.Errorf("correlation_id fallback = %v, want request-legacy", parsed["correlation_id"])
	}
}

// TestRenderWireEnvelope_RejectsNonCompliantEvents sanity-checks the
// validator: an envelope violating the frozen contract (bad type /
// wrong schema_version / payload missing v1 required fields) must fail
// validation, so the passing tests above actually prove compliance.
func TestRenderWireEnvelope_RejectsNonCompliantEvents(t *testing.T) {
	sch := loadEventSchema(t)

	bad := []EventEnvelope{
		{
			EventID: "evt-bad-type", SchemaVersion: 1, TenantID: "t1",
			EventType: "request.completed.v2", AggregateID: "s1", AggregateVersion: 1,
			Payload: map[string]any{
				"model": "m", "provider": "p", "input_tokens": 0, "output_tokens": 0,
				"cost_usd": "0", "latency_ms": 0, "status": "success",
			},
		},
		{
			EventID: "evt-missing-payload-fields", SchemaVersion: 1, TenantID: "t1",
			EventType: "request.completed.v1", AggregateID: "s1", AggregateVersion: 1,
			Payload: map[string]any{"model": "m"}, // no input_tokens/cost_usd/status...
		},
		{
			EventID: "evt-bad-cost", SchemaVersion: 1, TenantID: "t1",
			EventType: "request.completed.v1", AggregateID: "s1", AggregateVersion: 1,
			Payload: map[string]any{
				"model": "m", "provider": "p", "input_tokens": 0, "output_tokens": 0,
				"cost_usd": "-1.5", "latency_ms": 0, "status": "success",
			},
		},
		{
			EventID: "evt-bad-event-id", SchemaVersion: 1, TenantID: "t1",
			EventType: "request.completed.v1", AggregateID: "s1", AggregateVersion: 1,
			Payload: map[string]any{
				"model": "m", "provider": "p", "input_tokens": 0, "output_tokens": 0,
				"cost_usd": "0", "latency_ms": 0, "status": "success",
			},
		},
	}

	for i, env := range bad {
		wire, err := RenderWireEnvelope(env)
		if err != nil {
			t.Fatalf("case %d: RenderWireEnvelope failed: %v", i, err)
		}
		// evt-bad-event-id does not match ^evt-[a-zA-Z0-9_-]+$ (contains dots).
		if env.EventID == "evt-bad-event-id" {
			wire = []byte(strings.Replace(string(wire), env.EventID, "evt.bad.id", 1))
		}
		if err := validateAgainstSchema(t, sch, wire); err == nil {
			t.Errorf("case %d (%s): expected schema validation failure, got pass", i, env.EventID)
		}
	}
}

// TestBuildRequestCompletedEventV3_PayloadContract pins the v1 payload field
// semantics: flat token counts, decimal-string cost, success|error status
// with error_code, and the legacy fields retained for existing consumers.
func TestBuildRequestCompletedEventV3_PayloadContract(t *testing.T) {
	cost := 0.0012
	env, err := BuildRequestCompletedEventV3(
		"tenant-1", "session-1", 3,
		"request-1", "corr-1", "idem-1",
		"anthropic", "claude-test", "succeeded",
		100, 50, 900, &cost, true,
	)
	if err != nil {
		t.Fatalf("BuildRequestCompletedEventV3 failed: %v", err)
	}
	p := env.Payload
	if p["input_tokens"] != 100 {
		t.Errorf("input_tokens = %v, want 100", p["input_tokens"])
	}
	if p["output_tokens"] != 50 {
		t.Errorf("output_tokens = %v, want 50", p["output_tokens"])
	}
	if p["cost_usd"] != "0.0012" {
		t.Errorf("cost_usd = %v, want \"0.0012\"", p["cost_usd"])
	}
	if p["status"] != "success" {
		t.Errorf("status = %v, want success", p["status"])
	}
	if _, exists := p["error_code"]; exists {
		t.Error("error_code must be absent on success")
	}
	if p["legacy_status"] != "succeeded" {
		t.Errorf("legacy_status = %v, want succeeded", p["legacy_status"])
	}
	if _, exists := p["token_usage"]; !exists {
		t.Error("legacy token_usage must be retained for existing consumers")
	}

	envErr, err := BuildRequestCompletedEventV3(
		"tenant-1", "session-1", 4,
		"request-2", "", "",
		"anthropic", "claude-test", "upstream_timeout",
		0, 0, 30000, nil, false,
	)
	if err != nil {
		t.Fatalf("BuildRequestCompletedEventV3 failed: %v", err)
	}
	if envErr.Payload["status"] != "error" {
		t.Errorf("status = %v, want error", envErr.Payload["status"])
	}
	if envErr.Payload["error_code"] != "upstream_timeout" {
		t.Errorf("error_code = %v, want upstream_timeout", envErr.Payload["error_code"])
	}
	if envErr.Payload["cost_usd"] != "0" {
		t.Errorf("nil cost must render \"0\", got %v", envErr.Payload["cost_usd"])
	}
	if envErr.Payload["legacy_status"] != "timeout" {
		t.Errorf("legacy_status = %v, want timeout", envErr.Payload["legacy_status"])
	}
}

// TestFormatCostUSD pins the cost_usd rendering rules of the v1 schema
// pattern ^[0-9]+(\.[0-9]+)?$.
func TestFormatCostUSD(t *testing.T) {
	cases := []struct {
		name string
		cost *float64
		want string
	}{
		{"nil", nil, "0"},
		{"zero", float64Ptr(0), "0"},
		{"negative clamped", float64Ptr(-0.5), "0"},
		{"small decimal", float64Ptr(0.0012), "0.0012"},
		{"integer cost", float64Ptr(42), "42"},
		{"no scientific notation", float64Ptr(1e-7), "0.0000001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatCostUSD(tc.cost); got != tc.want {
				t.Errorf("FormatCostUSD(%v) = %q, want %q", tc.cost, got, tc.want)
			}
		})
	}
}

func float64Ptr(v float64) *float64 { return &v }

// TestSchemaFileIsPresent guards against silently dropping the schema copy.
func TestSchemaFileIsPresent(t *testing.T) {
	if _, err := os.Stat(filepath.Join("testdata", "gateway-event-schema-v1.json")); err != nil {
		t.Fatalf("schema copy missing: %v", err)
	}
}
