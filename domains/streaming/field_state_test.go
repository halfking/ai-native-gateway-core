package streaming

import (
	"encoding/json"
	"testing"
)

func TestGetFieldState(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected FieldState
	}{
		{
			name:     "field not provided",
			input:    `{"model":"gpt-4"}`,
			expected: FieldNotProvided,
		},
		{
			name:     "field explicitly null",
			input:    `{"model":"gpt-4","messages":null}`,
			expected: FieldNull,
		},
		{
			name:     "field with empty array",
			input:    `{"model":"gpt-4","messages":[]}`,
			expected: FieldProvided,
		},
		{
			name:     "field with empty object",
			input:    `{"model":"gpt-4","messages":{}}`,
			expected: FieldProvided,
		},
		{
			name:     "field with valid array",
			input:    `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			expected: FieldProvided,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req struct {
				Model    string          `json:"model"`
				Messages json.RawMessage `json:"messages,omitempty"`
			}
			
			if err := json.Unmarshal([]byte(tt.input), &req); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			
			state := GetFieldState(req.Messages)
			if state != tt.expected {
				t.Errorf("GetFieldState() = %v, want %v", state, tt.expected)
			}
		})
	}
}

func TestFieldStateString(t *testing.T) {
	tests := []struct {
		state    FieldState
		expected string
	}{
		{FieldNotProvided, "not_provided"},
		{FieldNull, "null"},
		{FieldProvided, "provided"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestToStringPtr(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantNil  bool
		wantStr  string
	}{
		{
			name:    "not provided",
			input:   `{"model":"gpt-4"}`,
			wantNil: true,
		},
		{
			name:    "explicit null",
			input:   `{"model":"gpt-4","messages":null}`,
			wantNil: false,
			wantStr: "null",
		},
		{
			name:    "empty array",
			input:   `{"model":"gpt-4","messages":[]}`,
			wantNil: false,
			wantStr: "[]",
		},
		{
			name:    "empty object",
			input:   `{"model":"gpt-4","messages":{}}`,
			wantNil: false,
			wantStr: "{}",
		},
		{
			name:    "valid value",
			input:   `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			wantNil: false,
			wantStr: `[{"role":"user","content":"hi"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req struct {
				Model    string          `json:"model"`
				Messages json.RawMessage `json:"messages,omitempty"`
			}
			
			if err := json.Unmarshal([]byte(tt.input), &req); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			
			ptr := ToStringPtr(req.Messages)
			
			if tt.wantNil {
				if ptr != nil {
					t.Errorf("ToStringPtr() = %v, want nil", *ptr)
				}
			} else {
				if ptr == nil {
					t.Errorf("ToStringPtr() = nil, want %v", tt.wantStr)
				} else if *ptr != tt.wantStr {
					t.Errorf("ToStringPtr() = %v, want %v", *ptr, tt.wantStr)
				}
			}
		})
	}
}

func TestValidateRequiredField(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		fieldName   string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "not provided",
			input:       `{"model":"gpt-4"}`,
			fieldName:   "messages",
			expectError: true,
			errorMsg:    "messages field is required",
		},
		{
			name:        "explicit null",
			input:       `{"model":"gpt-4","messages":null}`,
			fieldName:   "messages",
			expectError: true,
			errorMsg:    "messages cannot be null",
		},
		{
			name:        "empty array is valid",
			input:       `{"model":"gpt-4","messages":[]}`,
			fieldName:   "messages",
			expectError: false,
		},
		{
			name:        "empty object is valid for validation",
			input:       `{"model":"gpt-4","messages":{}}`,
			fieldName:   "messages",
			expectError: false,
		},
		{
			name:        "valid value",
			input:       `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			fieldName:   "messages",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req struct {
				Model    string          `json:"model"`
				Messages json.RawMessage `json:"messages,omitempty"`
			}
			
			if err := json.Unmarshal([]byte(tt.input), &req); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			
			errMsg := ValidateRequiredField(req.Messages, tt.fieldName)
			
			if tt.expectError {
				if errMsg == "" {
					t.Errorf("ValidateRequiredField() expected error, got none")
				} else if errMsg != tt.errorMsg {
					t.Errorf("ValidateRequiredField() = %v, want %v", errMsg, tt.errorMsg)
				}
			} else {
				if errMsg != "" {
					t.Errorf("ValidateRequiredField() = %v, want empty string", errMsg)
				}
			}
		})
	}
}

func TestValidateNonEmptyArray(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
		errorContains string
	}{
		{
			name:          "not provided",
			input:         `{"model":"gpt-4"}`,
			expectError:   true,
			errorContains: "field is required",
		},
		{
			name:          "explicit null",
			input:         `{"model":"gpt-4","messages":null}`,
			expectError:   true,
			errorContains: "cannot be null",
		},
		{
			name:          "empty array",
			input:         `{"model":"gpt-4","messages":[]}`,
			expectError:   true,
			errorContains: "cannot be empty",
		},
		{
			name:          "empty object",
			input:         `{"model":"gpt-4","messages":{}}`,
			expectError:   true,
			errorContains: "must be a valid JSON array",
		},
		{
			name:          "valid non-empty array",
			input:         `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			expectError:   false,
		},
		{
			name:          "string instead of array",
			input:         `{"model":"gpt-4","messages":"invalid"}`,
			expectError:   true,
			errorContains: "must be a valid JSON array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req struct {
				Model    string          `json:"model"`
				Messages json.RawMessage `json:"messages,omitempty"`
			}
			
			if err := json.Unmarshal([]byte(tt.input), &req); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			
			errMsg := ValidateNonEmptyArray(req.Messages, "messages")
			
			if tt.expectError {
				if errMsg == "" {
					t.Errorf("ValidateNonEmptyArray() expected error, got none")
				} else if tt.errorContains != "" && !contains(errMsg, tt.errorContains) {
					t.Errorf("ValidateNonEmptyArray() = %v, want error containing %v", errMsg, tt.errorContains)
				}
			} else {
				if errMsg != "" {
					t.Errorf("ValidateNonEmptyArray() = %v, want empty string", errMsg)
				}
			}
		})
	}
}

func TestHasUserMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "no messages",
			input:    `{"model":"gpt-4"}`,
			expected: false,
		},
		{
			name:     "null messages",
			input:    `{"model":"gpt-4","messages":null}`,
			expected: false,
		},
		{
			name:     "empty array",
			input:    `{"model":"gpt-4","messages":[]}`,
			expected: false,
		},
		{
			name:     "only system message",
			input:    `{"model":"gpt-4","messages":[{"role":"system","content":"you are helpful"}]}`,
			expected: false,
		},
		{
			name:     "has user message",
			input:    `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			expected: true,
		},
		{
			name:     "system and user messages",
			input:    `{"model":"gpt-4","messages":[{"role":"system","content":"helpful"},{"role":"user","content":"hi"}]}`,
			expected: true,
		},
		{
			name:     "invalid format",
			input:    `{"model":"gpt-4","messages":{}}`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req struct {
				Model    string          `json:"model"`
				Messages json.RawMessage `json:"messages,omitempty"`
			}
			
			if err := json.Unmarshal([]byte(tt.input), &req); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			
			result := HasUserMessage(req.Messages)
			if result != tt.expected {
				t.Errorf("HasUserMessage() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && 
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || 
		 findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
