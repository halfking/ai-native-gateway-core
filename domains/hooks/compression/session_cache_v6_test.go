package compression

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSessionStateV6Fields_RoundTrip 验证 v6 字段在 encode → decode 后保持一致。
func TestSessionStateV6Fields_RoundTrip(t *testing.T) {
	original := &SessionState{
		SchemaVersion:    schemaVersion,
		LastOutboundHash: "h1",
		MsgCount:         5,
		TokenEstimate:    100,
		// v6 fields
		AuditedAt:           1700000000,
		AuditScore:          7,
		SecurityScore:       3,
		SensitiveDetected:   true,
		PIIStripped:         true,
		ApprovalStatus:      "pending",
		ApprovalID:          "appr-123",
		OptimizationApplied: "strip_tools",
	}

	// encode
	fields := encodeSessionStateFields(original)
	fieldMap := fieldsToMap(fields)

	// 验证 v6 字段都在
	for _, key := range []string{
		"aud_at", "aud_sc", "sec_sc", "sen_det",
		"pii_strip", "app_st", "app_id", "opt_app",
	} {
		if _, ok := fieldMap[key]; !ok {
			t.Errorf("v6 field %q missing from encoded fields", key)
		}
	}

	// decode
	decoded := &SessionState{}
	if err := decodeSessionStateFields(fieldMap, decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// 验证一致
	if decoded.AuditedAt != original.AuditedAt {
		t.Errorf("AuditedAt: got %d want %d", decoded.AuditedAt, original.AuditedAt)
	}
	if decoded.AuditScore != original.AuditScore {
		t.Errorf("AuditScore: got %d want %d", decoded.AuditScore, original.AuditScore)
	}
	if decoded.SecurityScore != original.SecurityScore {
		t.Errorf("SecurityScore: got %d want %d", decoded.SecurityScore, original.SecurityScore)
	}
	if decoded.SensitiveDetected != original.SensitiveDetected {
		t.Errorf("SensitiveDetected: got %v want %v", decoded.SensitiveDetected, original.SensitiveDetected)
	}
	if decoded.PIIStripped != original.PIIStripped {
		t.Errorf("PIIStripped: got %v want %v", decoded.PIIStripped, original.PIIStripped)
	}
	if decoded.ApprovalStatus != original.ApprovalStatus {
		t.Errorf("ApprovalStatus: got %q want %q", decoded.ApprovalStatus, original.ApprovalStatus)
	}
	if decoded.ApprovalID != original.ApprovalID {
		t.Errorf("ApprovalID: got %q want %q", decoded.ApprovalID, original.ApprovalID)
	}
	if decoded.OptimizationApplied != original.OptimizationApplied {
		t.Errorf("OptimizationApplied: got %q want %q", decoded.OptimizationApplied, original.OptimizationApplied)
	}
}

// TestSessionStateV6Fields_ZeroValuesSkipped 验证空 v6 字段不写入 Redis hash。
// 这是向后兼容的关键：legacy reader 看不到这些字段也能正确解析。
func TestSessionStateV6Fields_ZeroValuesSkipped(t *testing.T) {
	state := &SessionState{SchemaVersion: schemaVersion}
	fields := encodeSessionStateFields(state)
	fieldMap := fieldsToMap(fields)

	for _, key := range []string{
		"aud_at", "aud_sc", "sec_sc", "sen_det",
		"pii_strip", "app_st", "app_id", "opt_app",
	} {
		if _, ok := fieldMap[key]; ok {
			t.Errorf("v6 field %q should be skipped when zero, got value=%q", key, fieldMap[key])
		}
	}
}

// TestSessionStateV6Fields_BackwardCompat 验证旧解码器（无 v6 字段）也能工作。
// 模拟 Redis 里只有 v1-v5 字段，decode 必须成功（v6 字段为 0 值）。
func TestSessionStateV6Fields_BackwardCompat(t *testing.T) {
	legacyFields := map[string]string{
		"v":    "1",
		"loh":  "legacyhash",
		"mc":   "10",
		"te":   "200",
		"smm":  "",
		"rcat": "0",
	}
	decoded := &SessionState{}
	if err := decodeSessionStateFields(legacyFields, decoded); err != nil {
		t.Fatalf("legacy decode failed: %v", err)
	}
	if decoded.AuditScore != 0 || decoded.ApprovalStatus != "" {
		t.Errorf("v6 fields should default to zero, got AuditScore=%d ApprovalStatus=%q",
			decoded.AuditScore, decoded.ApprovalStatus)
	}
	if decoded.SensitiveDetected {
		t.Error("SensitiveDetected should default to false")
	}
}

// TestSessionStateV6_JSONMarshaling 验证 JSON 标签正确（omitempty）。
func TestSessionStateV6_JSONMarshaling(t *testing.T) {
	state := &SessionState{SchemaVersion: 1}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		"aud_at", "aud_sc", "sec_sc", "sen_det",
		"pii_strip", "app_st", "app_id", "opt_app",
	} {
		if contains(string(b), key) {
			t.Errorf("zero v6 field %q should be omitted from JSON, got: %s", key, b)
		}
	}

	// 设置一些字段
	state.AuditScore = 5
	state.ApprovalStatus = "approved"
	b2, _ := json.Marshal(state)
	if !contains(string(b2), `"aud_sc":5`) {
		t.Errorf("non-zero v6 field should appear in JSON, got: %s", b2)
	}
	if !contains(string(b2), `"app_st":"approved"`) {
		t.Errorf("non-zero ApprovalStatus should appear in JSON, got: %s", b2)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helper methods tests
// ──────────────────────────────────────────────────────────────────────────────

func TestMarkAudited(t *testing.T) {
	s := &SessionState{}
	now := time.Unix(1700000000, 0)
	s.MarkAudited(now, 8, 2, true, false, true)

	if s.AuditedAt != 1700000000 {
		t.Errorf("AuditedAt: got %d want 1700000000", s.AuditedAt)
	}
	if s.AuditScore != 8 {
		t.Errorf("AuditScore: got %d want 8", s.AuditScore)
	}
	if s.SecurityScore != 2 {
		t.Errorf("SecurityScore: got %d want 2", s.SecurityScore)
	}
	if !s.SensitiveDetected {
		t.Error("SensitiveDetected should be true")
	}
	if s.PIIStripped {
		t.Error("PIIStripped should be false")
	}
	if s.ApprovalStatus != ApprovalStatePending {
		t.Errorf("ApprovalStatus: got %q want %q", s.ApprovalStatus, ApprovalStatePending)
	}

	// MarkAudited on nil receiver must not panic
	var nilS *SessionState
	nilS.MarkAudited(now, 1, 1, false, false, false)
}

func TestMarkAudited_NoApprovalPending(t *testing.T) {
	s := &SessionState{}
	s.MarkAudited(time.Now(), 1, 9, false, false, false)
	if s.ApprovalStatus != "" {
		t.Errorf("ApprovalStatus should remain empty when no approval needed, got %q", s.ApprovalStatus)
	}
}

func TestSetApprovalID(t *testing.T) {
	s := &SessionState{}
	s.SetApprovalID("appr-abc")
	if s.ApprovalID != "appr-abc" {
		t.Errorf("ApprovalID: got %q want %q", s.ApprovalID, "appr-abc")
	}
	if s.ApprovalStatus != ApprovalStatePending {
		t.Errorf("ApprovalStatus should be pending after SetApprovalID, got %q", s.ApprovalStatus)
	}

	// 设置空字符串不影响
	s.SetApprovalID("")
	if s.ApprovalID != "" {
		t.Errorf("ApprovalID should be empty after SetApprovalID(\"\"), got %q", s.ApprovalID)
	}

	// nil receiver 必须不 panic
	var nilS *SessionState
	nilS.SetApprovalID("x")
}

func TestSetApprovalResult(t *testing.T) {
	s := &SessionState{ApprovalStatus: ApprovalStatePending}
	s.SetApprovalResult(ApprovalStateApproved)
	if s.ApprovalStatus != ApprovalStateApproved {
		t.Errorf("ApprovalStatus: got %q want approved", s.ApprovalStatus)
	}

	// nil receiver 必须不 panic
	var nilS *SessionState
	nilS.SetApprovalResult(ApprovalStateRejected)
}

func TestApplyOptimization(t *testing.T) {
	s := &SessionState{}
	s.ApplyOptimization(OptStripTools)
	if s.OptimizationApplied != OptStripTools {
		t.Errorf("OptimizationApplied: got %q want %q", s.OptimizationApplied, OptStripTools)
	}

	// 空字符串无效
	s.ApplyOptimization("")
	if s.OptimizationApplied != OptStripTools {
		t.Errorf("OptimizationApplied should not change for empty tag, got %q", s.OptimizationApplied)
	}

	var nilS *SessionState
	nilS.ApplyOptimization(OptSummarize)
}

func TestIsApprovalPending(t *testing.T) {
	tests := []struct {
		name   string
		state  *SessionState
		expect bool
	}{
		{"nil receiver", nil, false},
		{"empty state", &SessionState{}, false},
		{"pending", &SessionState{ApprovalStatus: ApprovalStatePending}, true},
		{"approved", &SessionState{ApprovalStatus: ApprovalStateApproved}, false},
		{"rejected", &SessionState{ApprovalStatus: ApprovalStateRejected}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.IsApprovalPending(); got != tc.expect {
				t.Errorf("got %v want %v", got, tc.expect)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

// fieldsToMap 把 encodeSessionStateFields 返回的 [k1, v1, k2, v2, ...] 转成 map。
func fieldsToMap(fields []any) map[string]string {
	m := make(map[string]string, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		k, _ := fields[i].(string)
		switch v := fields[i+1].(type) {
		case string:
			m[k] = v
		default:
			m[k] = ""
		}
	}
	return m
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
