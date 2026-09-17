package streaming

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// mockDB implements the recorder exec interface for testing.
type mockDB struct {
	execCalled bool
	execQuery  string
	execArgs   []any
	execError  error
}

func (m *mockDB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	m.execCalled = true
	m.execQuery = query
	m.execArgs = args
	return pgconn.CommandTag{}, m.execError
}

func TestFormatAnomalyRecorder_RecordAnomaly(t *testing.T) {
	tests := []struct {
		name    string
		record  AnomalyRecord
		wantErr bool
	}{
		{
			name: "record zero completion tokens anomaly",
			record: AnomalyRecord{
				RequestID:      "test-req-123",
				ProviderCode:   ptrStr("minimax"),
				ClientModel:    ptrStr("minimax-m3"),
				OutboundModel:  ptrStr("MiniMax-M3"),
				AnomalyType:    AnomalyZeroCompletion,
				Severity:       SeverityMedium,
				UsageSource:    ptrStr("estimated"),
				ExpectedTokens: ptrInt(0),
				ContentSize:    ptrInt(1234),
				Structure: map[string]any{
					"has_choices": true,
					"has_usage":   false,
				},
				ResponseSample: ptrStr(`{"choices":[{"message":{"content":"test"}}]}`),
				TenantID:       ptrStr("default"),
			},
			wantErr: false,
		},
		{
			name: "record extraction failed anomaly",
			record: AnomalyRecord{
				RequestID:      "test-req-456",
				ProviderCode:   ptrStr("custom-provider"),
				ClientModel:    ptrStr("gpt-4"),
				AnomalyType:    AnomalyExtractionFailed,
				Severity:       SeverityHigh,
				ExpectedTokens: ptrInt(0),
				ActualTokens:   nil,
				ContentSize:    ptrInt(2048),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &mockDB{}
			recorder := NewFormatAnomalyRecorder(db)

			err := recorder.RecordAnomaly(context.Background(), tt.record)
			if (err != nil) != tt.wantErr {
				t.Errorf("RecordAnomaly() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !db.execCalled {
				t.Error("expected Exec to be called")
			}
			if len(db.execArgs) != 14 {
				t.Errorf("expected 14 args, got %d", len(db.execArgs))
			}
			if db.execArgs[0] != tt.record.RequestID {
				t.Errorf("first arg should be request_id, got %v", db.execArgs[0])
			}
		})
	}
}

// TestFormatAnomalyRecorder_StructureArgMustBeString is the regression guard
// for the 2026-09-17 PG log audit finding: response_format_anomalies stayed
// at 0 rows forever because the recorder passed json.Marshal's []byte as the
// response_structure arg. pgx's simple protocol (db.Open forces
// QueryExecModeSimpleProtocol) text-encodes []byte as bytea hex ("\x7b..."),
// which the jsonb column rejects with 22P02 on every insert. The arg must be
// a Go string so it lands as a JSON text literal.
func TestFormatAnomalyRecorder_StructureArgMustBeString(t *testing.T) {
	db := &mockDB{}
	recorder := NewFormatAnomalyRecorder(db)
	err := recorder.RecordAnomaly(context.Background(), AnomalyRecord{
		RequestID:   "req-structure-string",
		AnomalyType: AnomalyZeroCompletion,
		Severity:    SeverityMedium,
		Structure:   map[string]any{"has_usage": false},
	})
	if err != nil {
		t.Fatalf("RecordAnomaly() error = %v", err)
	}
	// response_structure is $12 in the 14-arg insert.
	structureArg, ok := db.execArgs[11].(string)
	if !ok {
		t.Fatalf("response_structure arg must be string (pgx simple protocol encodes []byte as bytea hex), got %T", db.execArgs[11])
	}
	if structureArg != `{"has_usage":false}` {
		t.Errorf("unexpected structure payload: %q", structureArg)
	}
	if !strings.Contains(db.execQuery, "$12::text::jsonb") {
		t.Errorf("insert must cast response_structure via $12::text::jsonb, got: %s", db.execQuery)
	}
}

func TestFormatAnomalyRecorder_DataAnomalyStructureArgMustBeString(t *testing.T) {
	db := &mockDB{}
	recorder := NewFormatAnomalyRecorder(db)
	err := recorder.RecordDataAnomaly(context.Background(),
		"persistence_failed", "high", "req-data-structure-string",
		"request_wal_hot persist failed", map[string]any{"batch_index": 3})
	if err != nil {
		t.Fatalf("RecordDataAnomaly() error = %v", err)
	}
	// response_structure is $4 in the 6-arg insert.
	structureArg, ok := db.execArgs[3].(string)
	if !ok {
		t.Fatalf("response_structure arg must be string (pgx simple protocol encodes []byte as bytea hex), got %T", db.execArgs[3])
	}
	if !strings.Contains(structureArg, `"batch_index"`) {
		t.Errorf("unexpected structure payload: %q", structureArg)
	}
	if !strings.Contains(db.execQuery, "$4::text::jsonb") {
		t.Errorf("insert must cast response_structure via $4::text::jsonb, got: %s", db.execQuery)
	}
}

func TestAnalyzeResponseStructure(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantKeys []string
	}{
		{name: "empty response", response: "", wantKeys: []string{"empty"}},
		{name: "invalid json", response: "{invalid", wantKeys: []string{"parse_error", "error"}},
		{
			name:     "standard openai format",
			response: `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantKeys: []string{"has_choices", "choices_count", "has_usage", "usage_fields"},
		},
		{
			name:     "missing usage block",
			response: `{"choices":[{"message":{"content":"hello"}}]}`,
			wantKeys: []string{"has_choices", "choices_count", "has_usage"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AnalyzeResponseStructure([]byte(tt.response))
			for _, key := range tt.wantKeys {
				if _, ok := result[key]; !ok {
					t.Errorf("expected key %q in result, got %+v", key, result)
				}
			}
		})
	}
}

func TestShouldRecordAnomaly(t *testing.T) {
	for _, tc := range []struct {
		name         string
		anomalyType  AnomalyType
		providerCode string
	}{
		{name: "extraction failed", anomalyType: AnomalyExtractionFailed, providerCode: "any-provider"},
		{name: "unexpected structure", anomalyType: AnomalyUnexpectedStructure, providerCode: "any-provider"},
		{name: "zero completion minimax", anomalyType: AnomalyZeroCompletion, providerCode: "minimax"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = ShouldRecordAnomaly(tc.anomalyType, tc.providerCode)
		})
	}
}

func TestTruncateForSample(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{name: "short string unchanged", input: "hello", maxLen: 10, want: "hello"},
		{name: "exact length unchanged", input: "hello", maxLen: 5, want: "hello"},
		{name: "long string truncated", input: "hello world this is a test", maxLen: 10, want: "hello worl...[truncated]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateForSample(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("TruncateForSample() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatAnomalyRecorder_NilDB(t *testing.T) {
	recorder := NewFormatAnomalyRecorder(nil)
	err := recorder.RecordAnomaly(context.Background(), AnomalyRecord{
		RequestID:   "test",
		AnomalyType: AnomalyZeroCompletion,
		Severity:    SeverityLow,
	})
	if err != nil {
		t.Errorf("expected nil error when DB is nil, got %v", err)
	}
}

func TestToFaultSeverity(t *testing.T) {
	tests := map[string]string{
		"critical": "critical",
		"high":     "error",
		"medium":   "warning",
		"low":      "info",
		"unknown":  "warning",
	}
	for input, want := range tests {
		if got := toFaultSeverity(input); got != want {
			t.Errorf("toFaultSeverity(%q) = %q, want %q", input, got, want)
		}
	}
}

func ptrStr(s string) *string { return &s }
func ptrInt(i int) *int       { return &i }

// recorder 的主行为（actual_tokens 回填 + usage_source 修正为 llm）由
// format_anomaly_backfill_test.go 覆盖；此处只补错误传播路径。
// 回填失败返回错误由调用方告警，不影响请求主链路。
func TestFormatAnomalyRecorder_BackfillActualTokens_ExecErrorPropagates(t *testing.T) {
	db := &mockDB{execError: errors.New("db down")}
	recorder := NewFormatAnomalyRecorder(db)

	err := recorder.BackfillActualTokens(context.Background(), "req-backfill", 456)
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}
