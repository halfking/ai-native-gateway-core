// annotation/importer_test.go — 2026-09-06
//
// P2.1导入器单元测试

package annotation

import (
	"strings"
	"testing"
)

func TestValidateHeader(t *testing.T) {
	tests := []struct {
		name      string
		header    []string
		wantError bool
	}{
		{
			name: "valid header",
			header: []string{
				"request_id", "model_name", "task_type", "prompt_tokens",
				"is_streaming", "has_vision", "region", "profile",
				"auto_provider", "confidence",
				"human_provider", "is_correct", "reason", "annotator",
			},
			wantError: false,
		},
		{
			name: "missing human_provider",
			header: []string{
				"request_id", "auto_provider", "confidence",
				"is_correct", "reason", "annotator",
			},
			wantError: true,
		},
		{
			name: "missing is_correct",
			header: []string{
				"request_id", "auto_provider", "confidence",
				"human_provider", "reason", "annotator",
			},
			wantError: true,
		},
		{
			name: "missing annotator",
			header: []string{
				"request_id", "auto_provider", "confidence",
				"human_provider", "is_correct", "reason",
			},
			wantError: true,
		},
		{
			name:      "empty header",
			header:    []string{},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHeader(tt.header)
			if (err != nil) != tt.wantError {
				t.Errorf("validateHeader() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestParseCSVRow(t *testing.T) {
	header := []string{
		"request_id", "model_name", "task_type", "prompt_tokens",
		"auto_provider", "confidence",
		"human_provider", "is_correct", "reason", "annotator",
	}
	colIndex := buildColumnIndex(header)

	tests := []struct {
		name      string
		record    []string
		wantError bool
		checkFunc func(*testing.T, *CSVAnnotationRow)
	}{
		{
			name: "valid row",
			record: []string{
				"req_001", "gpt-4", "chat", "150",
				"openai", "0.65",
				"openai", "TRUE", "correct", "alice",
			},
			wantError: false,
			checkFunc: func(t *testing.T, row *CSVAnnotationRow) {
				if row.RequestID != "req_001" {
					t.Errorf("RequestID = %s, want req_001", row.RequestID)
				}
				if row.Confidence != 0.65 {
					t.Errorf("Confidence = %.3f, want 0.65", row.Confidence)
				}
				if row.HumanProvider != "openai" {
					t.Errorf("HumanProvider = %s, want openai", row.HumanProvider)
				}
				if row.Annotator != "alice" {
					t.Errorf("Annotator = %s, want alice", row.Annotator)
				}
			},
		},
		{
			name: "invalid confidence",
			record: []string{
				"req_002", "gpt-4", "chat", "150",
				"openai", "invalid",
				"openai", "TRUE", "correct", "alice",
			},
			wantError: true,
		},
		{
			name: "missing fields",
			record: []string{
				"req_003", "gpt-4",
			},
			wantError: false, // parseCSVRow不检查必填字段，由validateCSVRow检查
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row, err := parseCSVRow(tt.record, colIndex)
			if (err != nil) != tt.wantError {
				t.Errorf("parseCSVRow() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if err == nil && tt.checkFunc != nil {
				tt.checkFunc(t, row)
			}
		})
	}
}

func TestValidateCSVRow(t *testing.T) {
	tests := []struct {
		name      string
		row       *CSVAnnotationRow
		wantError bool
		errorMsg  string
	}{
		{
			name: "valid row",
			row: &CSVAnnotationRow{
				RequestID:      "req_001",
				AutoProvider:   "openai",
				Confidence:     0.65,
				HumanProvider:  "openai",
				IsCorrect:      "TRUE",
				Reason:         "correct",
				Annotator:      "alice",
			},
			wantError: false,
		},
		{
			name: "missing request_id",
			row: &CSVAnnotationRow{
				AutoProvider:  "openai",
				Confidence:    0.65,
				HumanProvider: "openai",
				IsCorrect:     "TRUE",
				Annotator:     "alice",
			},
			wantError: true,
			errorMsg:  "request_id is required",
		},
		{
			name: "missing human_provider",
			row: &CSVAnnotationRow{
				RequestID:     "req_001",
				AutoProvider:  "openai",
				Confidence:    0.65,
				IsCorrect:     "TRUE",
				Annotator:     "alice",
			},
			wantError: true,
			errorMsg:  "human_provider is required",
		},
		{
			name: "invalid is_correct",
			row: &CSVAnnotationRow{
				RequestID:     "req_001",
				AutoProvider:  "openai",
				Confidence:    0.65,
				HumanProvider: "openai",
				IsCorrect:     "yes",
				Annotator:     "alice",
			},
			wantError: true,
			errorMsg:  "is_correct must be TRUE or FALSE",
		},
		{
			name: "confidence out of range (too high)",
			row: &CSVAnnotationRow{
				RequestID:     "req_001",
				AutoProvider:  "openai",
				Confidence:    1.5,
				HumanProvider: "openai",
				IsCorrect:     "TRUE",
				Annotator:     "alice",
			},
			wantError: true,
			errorMsg:  "confidence must be in [0, 1]",
		},
		{
			name: "confidence out of range (negative)",
			row: &CSVAnnotationRow{
				RequestID:     "req_001",
				AutoProvider:  "openai",
				Confidence:    -0.1,
				HumanProvider: "openai",
				IsCorrect:     "TRUE",
				Annotator:     "alice",
			},
			wantError: true,
			errorMsg:  "confidence must be in [0, 1]",
		},
		{
			name: "invalid reason",
			row: &CSVAnnotationRow{
				RequestID:     "req_001",
				AutoProvider:  "openai",
				Confidence:    0.65,
				HumanProvider: "openai",
				IsCorrect:     "TRUE",
				Reason:        "invalid_reason",
				Annotator:     "alice",
			},
			wantError: true,
			errorMsg:  "invalid reason",
		},
		{
			name: "valid with FALSE is_correct",
			row: &CSVAnnotationRow{
				RequestID:     "req_001",
				AutoProvider:  "openai",
				Confidence:    0.65,
				HumanProvider: "anthropic",
				IsCorrect:     "FALSE",
				Reason:        "cost",
				Annotator:     "alice",
			},
			wantError: false,
		},
		{
			name: "valid with lowercase true",
			row: &CSVAnnotationRow{
				RequestID:     "req_001",
				AutoProvider:  "openai",
				Confidence:    0.65,
				HumanProvider: "openai",
				IsCorrect:     "true",
				Annotator:     "alice",
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCSVRow(tt.row)
			if (err != nil) != tt.wantError {
				t.Errorf("validateCSVRow() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if err != nil && tt.errorMsg != "" {
				if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("validateCSVRow() error = %v, want error containing %q", err, tt.errorMsg)
				}
			}
		})
	}
}

func TestGetColumn(t *testing.T) {
	record := []string{"value1", "value2", "value3"}
	colIndex := map[string]int{
		"col1": 0,
		"col2": 1,
		"col3": 2,
	}

	tests := []struct {
		name     string
		colName  string
		expected string
	}{
		{"existing column", "col1", "value1"},
		{"another existing column", "col2", "value2"},
		{"non-existing column", "col4", ""},
		{"out of range", "col5", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getColumn(record, colIndex, tt.colName)
			if result != tt.expected {
				t.Errorf("getColumn(%q) = %q, want %q", tt.colName, result, tt.expected)
			}
		})
	}
}

func TestBuildColumnIndex(t *testing.T) {
	header := []string{"request_id", "model_name", "confidence"}
	index := buildColumnIndex(header)

	expected := map[string]int{
		"request_id": 0,
		"model_name": 1,
		"confidence": 2,
	}

	if len(index) != len(expected) {
		t.Errorf("buildColumnIndex() returned %d columns, want %d", len(index), len(expected))
	}

	for col, expectedIdx := range expected {
		if idx, exists := index[col]; !exists || idx != expectedIdx {
			t.Errorf("buildColumnIndex()[%q] = %d, want %d", col, idx, expectedIdx)
		}
	}
}
