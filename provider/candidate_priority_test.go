package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCandidatePriorityContract(t *testing.T) {
	field, ok := reflect.TypeOf(Candidate{}).FieldByName("Priority")
	if !ok {
		t.Fatal("Candidate must expose Priority")
	}
	if field.Type.Kind() != reflect.Bool {
		t.Fatalf("Candidate.Priority type = %s, want bool", field.Type)
	}
	if got := field.Tag.Get("json"); got != "priority" {
		t.Fatalf("Candidate.Priority json tag = %q, want priority", got)
	}

	payload, err := json.Marshal(Candidate{Priority: true})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	if !strings.Contains(string(payload), `"priority":true`) {
		t.Fatalf("marshaled candidate does not expose priority: %s", payload)
	}
}

func TestCandidateNativeResponsesCapabilityContract(t *testing.T) {
	field, ok := reflect.TypeOf(Candidate{}).FieldByName("SupportsNativeResponses")
	if !ok {
		t.Fatal("Candidate must expose SupportsNativeResponses")
	}
	if field.Type.Kind() != reflect.Bool {
		t.Fatalf("Candidate.SupportsNativeResponses type = %s, want bool", field.Type)
	}
	if got := field.Tag.Get("json"); got != "supports_native_responses,omitempty" {
		t.Fatalf("Candidate.SupportsNativeResponses json tag = %q", got)
	}
	if (Candidate{}).SupportsNativeResponses {
		t.Fatal("native Responses capability must default to false")
	}
	payload, err := json.Marshal(Candidate{SupportsNativeResponses: true})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	if !strings.Contains(string(payload), `"supports_native_responses":true`) {
		t.Fatalf("marshaled candidate omits native Responses capability: %s", payload)
	}
}

func TestCandidateNativeResponsesStreamCapabilityContract(t *testing.T) {
	field, ok := reflect.TypeOf(Candidate{}).FieldByName("SupportsNativeResponsesStream")
	if !ok {
		t.Fatal("Candidate must expose SupportsNativeResponsesStream")
	}
	if field.Type.Kind() != reflect.Bool {
		t.Fatalf("Candidate.SupportsNativeResponsesStream type = %s, want bool", field.Type)
	}
	if field.Tag.Get("json") != "supports_native_responses_stream,omitempty" {
		t.Fatalf("unexpected JSON tag %q", field.Tag.Get("json"))
	}
	if (Candidate{}).SupportsNativeResponsesStream {
		t.Fatal("native Responses stream capability must default to false")
	}
}

func TestCandidateNativeResponsesCapabilityDBRead(t *testing.T) {
	src := readProviderFile(t, "client.go")
	for _, needle := range []string{
		"LEFT JOIN credential_model_capabilities cmcap",
		"cmcap.credential_model_binding_id = mo.id",
		"cmcap.capability = 'native_responses_nonstream'",
		"COALESCE(cmcap.supported, FALSE) AS supports_native_responses",
		"LEFT JOIN credential_model_capabilities cmstream",
		"cmstream.capability = 'native_responses_stream'",
		"COALESCE(cmstream.supported, FALSE) AS supports_native_responses_stream",
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("candidate query must contain %s", needle)
		}
	}
	supportsAt := strings.Index(src, "&cand.SupportsNativeResponses")
	cacheAt := strings.Index(src, "&cand.CacheMode")
	if supportsAt < 0 || cacheAt < 0 || supportsAt > cacheAt {
		t.Fatal("candidate row scan must bind native Responses capability before cache mode")
	}
}

func TestCandidatePriorityDBReadAndOrdering(t *testing.T) {
	src := readProviderFile(t, "client.go")

	selectNeedle := "COALESCE(mo.priority, FALSE) AS priority"
	if !strings.Contains(src, selectNeedle) {
		t.Fatalf("candidate query must read %s", selectNeedle)
	}

	manualAt := strings.Index(src, "&cand.ManualPriority")
	priorityAt := strings.Index(src, "&cand.Priority")
	activeAt := strings.Index(src, "&cand.ActiveSessions")
	if manualAt < 0 || priorityAt < 0 || activeAt < 0 || !(manualAt < priorityAt && priorityAt < activeAt) {
		t.Fatalf("candidate row scan must bind priority between manual priority and active sessions")
	}

	orderNeedle := "CASE WHEN COALESCE(mo.priority, FALSE) AND COALESCE(c.quota_state, 'ok') = 'ok' THEN 0 ELSE 1 END"
	orderAt := strings.Index(src, orderNeedle)
	if orderAt < 0 {
		t.Fatalf("candidate query must prioritize priority bindings with quota_state=ok")
	}
	billingAt := strings.Index(src[orderAt:], "CASE COALESCE(mo.billing_mode, 'per_token')")
	if billingAt < 0 {
		t.Fatalf("candidate query must retain billing-mode ordering after priority ordering")
	}
}
