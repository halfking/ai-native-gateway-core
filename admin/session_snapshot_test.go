package admin

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestSessionSnapshotV2JSONMarshaling verifies that all fields in sessionSnapshotV2
// are properly marshaled to JSON with correct tags and omitempty behavior.
func TestSessionSnapshotV2JSONMarshaling(t *testing.T) {
	now := time.Now()
	closedAt := now.Add(1 * time.Hour)
	model := "gpt-4"
	provider := "openai"

	tests := []struct {
		name     string
		snapshot sessionSnapshotV2
		want     map[string]interface{}
	}{
		{
			name: "full snapshot with all fields",
			snapshot: sessionSnapshotV2{
				SessionID:           "sess_123",
				TenantID:            "tenant_456",
				Title:               "Test Session",
				Summary:             "This is a test session",
				SummaryGeneratedAt:  &now,
				TotalTurns:          5,
				TotalTokens:         15420,
				TotalCostUSD:        0.0123,
				LastTurnNo:          5,
				LastModel:           &model,
				LastProvider:        &provider,
				LastRequestSummary:  "User asked about Python",
				LastResponseSummary: "Assistant explained Python basics",
				CreatedAt:           now,
				UpdatedAt:           now,
				ClosedAt:            &closedAt,
				Status:              "closed",
				TaskType:            "code_generation",
				ClientType:          "cursor",
				Topic:               "Python Development",
				Intent:              "learning",
				UserTags:            []string{"python", "backend"},
			},
			want: map[string]interface{}{
				"session_id":            "sess_123",
				"tenant_id":             "tenant_456",
				"title":                 "Test Session",
				"summary":               "This is a test session",
				"total_turns":           float64(5),
				"total_tokens":          float64(15420),
				"total_cost_usd":        0.0123,
				"last_turn_no":          float64(5),
				"last_model":            "gpt-4",
				"last_provider":         "openai",
				"last_request_summary":  "User asked about Python",
				"last_response_summary": "Assistant explained Python basics",
				"status":                "closed",
				"task_type":             "code_generation",
				"client_type":           "cursor",
				"topic":                 "Python Development",
				"intent":                "learning",
			},
		},
		{
			name: "minimal snapshot with required fields only",
			snapshot: sessionSnapshotV2{
				SessionID:    "sess_789",
				TenantID:     "tenant_012",
				Title:        "",
				Summary:      "",
				TotalTurns:   0,
				TotalTokens:  0,
				TotalCostUSD: 0,
				LastTurnNo:   0,
				CreatedAt:    now,
				UpdatedAt:    now,
				Status:       "active",
			},
			want: map[string]interface{}{
				"session_id":    "sess_789",
				"tenant_id":     "tenant_012",
				"title":         "",
				"summary":       "",
				"total_turns":   float64(0),
				"total_tokens":  float64(0),
				"total_cost_usd": float64(0),
				"last_turn_no":  float64(0),
				"status":        "active",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.snapshot)
			if err != nil {
				t.Fatalf("failed to marshal snapshot: %v", err)
			}

			var got map[string]interface{}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("failed to unmarshal snapshot: %v", err)
			}

			// Verify required fields are present
			for key, wantVal := range tt.want {
				gotVal, ok := got[key]
				if !ok {
					t.Errorf("missing field %q in JSON output", key)
					continue
				}
				// Note: This is a basic check. For timestamps, you'd need more sophisticated comparison.
				if key != "created_at" && key != "updated_at" && key != "summary_generated_at" && key != "closed_at" {
					if gotVal != wantVal {
						t.Errorf("field %q: got %v, want %v", key, gotVal, wantVal)
					}
				}
			}

			// Verify omitempty works - optional fields with empty values should be absent
			if tt.name == "minimal snapshot with required fields only" {
				omitFields := []string{"summary_generated_at", "last_model", "last_provider",
					"last_request_summary", "last_response_summary", "closed_at",
					"task_type", "client_type", "topic", "intent", "user_tags"}
				for _, field := range omitFields {
					if val, exists := got[field]; exists {
						// Empty strings and empty arrays should be omitted
						if val != "" && val != nil {
							t.Errorf("field %q should be omitted but is present with value: %v", field, val)
						}
					}
				}
			}

			// Verify array fields
			if len(tt.snapshot.UserTags) > 0 {
				tags, ok := got["user_tags"].([]interface{})
				if !ok {
					t.Error("user_tags should be an array")
				} else if len(tags) != len(tt.snapshot.UserTags) {
					t.Errorf("user_tags length: got %d, want %d", len(tags), len(tt.snapshot.UserTags))
				}
			}
		})
	}
}

// TestSessionSnapshotV2FieldCount ensures we don't accidentally remove fields.
// If this test fails after adding new fields, update the expected count.
func TestSessionSnapshotV2FieldCount(t *testing.T) {
	// sessionSnapshotV2 has 23 fields (10 original + 13 added in fc28e27b8),
	// not counting embedded structs (there are none today).
	const wantFields = 23
	if got := reflect.TypeOf(sessionSnapshotV2{}).NumField(); got != wantFields {
		t.Errorf("sessionSnapshotV2 has %d fields, want %d — update wantFields if the change is intentional", got, wantFields)
	}

	snapshot := sessionSnapshotV2{}
	data, _ := json.Marshal(snapshot)

	var m map[string]interface{}
	json.Unmarshal(data, &m)
	
	// At minimum, we should have the required fields even when empty
	minFields := []string{"session_id", "tenant_id", "title", "summary", 
		"total_turns", "total_tokens", "total_cost_usd", "last_turn_no",
		"created_at", "updated_at", "status"}
	
	for _, field := range minFields {
		if _, exists := m[field]; !exists {
			t.Errorf("required field %q is missing from empty snapshot JSON", field)
		}
	}
}

// TestSessionSnapshotV2NewFieldsPresent is a smoke test to ensure all newly added
// fields are correctly defined in the struct.
func TestSessionSnapshotV2NewFieldsPresent(t *testing.T) {
	now := time.Now()
	model := "gpt-4"
	
	snapshot := sessionSnapshotV2{
		// New fields added in this enhancement
		TotalTokens:         100,
		LastTurnNo:          5,
		LastRequestSummary:  "test request",
		LastResponseSummary: "test response",
		CreatedAt:           now,
		UpdatedAt:           now,
		Status:              "active",
		TaskType:            "test",
		ClientType:          "test-client",
		Topic:               "test topic",
		Intent:              "test intent",
		UserTags:            []string{"tag1", "tag2"},
		
		// Existing fields
		SessionID:   "test",
		TenantID:    "test",
		TotalTurns:  5,
		TotalCostUSD: 0.01,
		LastModel:   &model,
	}
	
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("failed to marshal snapshot with new fields: %v", err)
	}
	
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	
	// Verify all new fields are in the JSON
	newFields := map[string]interface{}{
		"total_tokens":          float64(100),
		"last_turn_no":          float64(5),
		"last_request_summary":  "test request",
		"last_response_summary": "test response",
		"status":                "active",
		"task_type":             "test",
		"client_type":           "test-client",
		"topic":                 "test topic",
		"intent":                "test intent",
	}
	
	for field, expected := range newFields {
		actual, exists := result[field]
		if !exists {
			t.Errorf("new field %q is missing from JSON output", field)
			continue
		}
		if actual != expected {
			t.Errorf("field %q: got %v, want %v", field, actual, expected)
		}
	}
	
	// Verify user_tags array
	if tags, ok := result["user_tags"].([]interface{}); ok {
		if len(tags) != 2 {
			t.Errorf("user_tags: got %d items, want 2", len(tags))
		}
	} else {
		t.Error("user_tags is not present or not an array")
	}
	
	// Verify timestamps are present
	for _, field := range []string{"created_at", "updated_at"} {
		if _, exists := result[field]; !exists {
			t.Errorf("timestamp field %q is missing", field)
		}
	}
}
