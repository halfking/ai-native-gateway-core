package requestflow

import (
	"fmt"
	"testing"
)

func TestEventAttrs_IncludeDecisionFields(t *testing.T) {
	e := Event{
		Stage:        "survival",
		RequestID:    "req-1",
		Model:        "minimax-m3",
		ProviderID:   12,
		CredentialID: 34,
		Kind:         "network",
		Action:       "resume_blocked",
		Reason:       "committed_output",
		Retryable:    false,
		Committed:    true,
		Attempt:      2,
	}
	attrs := e.Attrs()
	got := map[string]any{}
	for i := 0; i+1 < len(attrs); i += 2 {
		got[fmt.Sprint(attrs[i])] = attrs[i+1]
	}
	want := map[string]any{
		"event":         "request_flow",
		"stage":         "survival",
		"request_id":    "req-1",
		"model":         "minimax-m3",
		"provider_id":   12,
		"credential_id": 34,
		"kind":          "network",
		"action":        "resume_blocked",
		"reason":        "committed_output",
		"retryable":     false,
		"committed":     true,
		"attempt":       2,
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("attr %s = %#v, want %#v", k, got[k], v)
		}
	}
}
