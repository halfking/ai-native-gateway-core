package siem

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// mockRecord is a minimal EventRecord for tests (avoids importing audit).
type mockRecord struct {
	requestID, tenantID, clientModel, outboundModel, errorKind, eventType string
	providerID, credentialID, latencyMs, promptTokens, completionTokens   int
	success                                                               bool
	costUSD                                                               float64
	ts                                                                    time.Time
}

func (m mockRecord) GetRequestID() string         { return m.requestID }
func (m mockRecord) GetTimestamp() time.Time       { return m.ts }
func (m mockRecord) GetTenantID() string           { return m.tenantID }
func (m mockRecord) GetClientModel() string        { return m.clientModel }
func (m mockRecord) GetOutboundModel() string      { return m.outboundModel }
func (m mockRecord) GetProviderID() int            { return m.providerID }
func (m mockRecord) GetCredentialID() int          { return m.credentialID }
func (m mockRecord) GetLatencyMs() int             { return m.latencyMs }
func (m mockRecord) GetSuccess() bool              { return m.success }
func (m mockRecord) GetErrorKind() string          { return m.errorKind }
func (m mockRecord) GetPromptTokens() int          { return m.promptTokens }
func (m mockRecord) GetCompletionTokens() int      { return m.completionTokens }
func (m mockRecord) GetCostUSD() float64           { return m.costUSD }
func (m mockRecord) GetEventType() string          { return m.eventType }

func sampleRecord() mockRecord {
	return mockRecord{
		requestID: "req-123", tenantID: "acme",
		clientModel: "glm-4.7", outboundModel: "zhipu/glm-4-plus",
		providerID: 3, credentialID: 42, latencyMs: 850,
		success: true, promptTokens: 120, completionTokens: 80,
		costUSD: 0.0021, ts: time.UnixMilli(1719200000000),
	}
}

// TestFormatCEF_HeaderShape: the CEF header must match the spec regex.
func TestFormatCEF_HeaderShape(t *testing.T) {
	line := FormatCEF(sampleRecord())
	// CEF:Version|Vendor|Product|DevVersion|SignatureID|Name|Severity|Extension
	re := regexp.MustCompile(`^CEF:0\|[^|]+\|[^|]+\|[^|]+\|[^|]+\|[^|]+\|[0-9]+\|`)
	if !re.MatchString(line) {
		t.Errorf("CEF header malformed:\n%s", line)
	}
}

// TestFormatCEF_VendorProductStable: downstream SIEM parsers key on these.
func TestFormatCEF_VendorProductStable(t *testing.T) {
	line := FormatCEF(sampleRecord())
	if !strings.HasPrefix(line, "CEF:0|Kaixuan|LLM Gateway Go|") {
		t.Errorf("vendor/product prefix changed (SIEM parsers will break): %s", line[:60])
	}
}

// TestFormatCEF_NoContentLeak: CRITICAL — no prompt/completion body in CEF.
func TestFormatCEF_NoContentLeak(t *testing.T) {
	line := FormatCEF(sampleRecord())
	for _, forbidden := range []string{"prompt=", "completion=", "content=", "message="} {
		if strings.Contains(line, forbidden) {
			t.Errorf("CEF line leaks content via %q:\n%s", forbidden, line)
		}
	}
}

// TestFormatCEF_ContainsKeyMetadata: the security-relevant fields must appear.
func TestFormatCEF_ContainsKeyMetadata(t *testing.T) {
	line := FormatCEF(sampleRecord())
	required := []string{
		"requestId=req-123", "tenantId=acme", "clientModel=glm-4.7",
		"outboundModel=zhipu/glm-4-plus", "providerId=3", "credentialId=42",
		"latencyMs=850", "success=true", "errorKind=-",
		"promptTokens=120", "completionTokens=80", "ts=1719200000000",
	}
	for _, want := range required {
		if !strings.Contains(line, want) {
			t.Errorf("CEF line missing %q:\n%s", want, line)
		}
	}
}

// TestCEFEscape_SpecialChars: | = \ \n must be escaped.
func TestCEFEscape_SpecialChars(t *testing.T) {
	cases := map[string]string{
		"|":      "\\|",
		"=":      "\\=",
		"\\":     "\\\\",
		"a|b=c":  "a\\|b\\=c",
		"line\n": "line\\n",
		"":       "-",
	}
	for in, want := range cases {
		if got := cefEscape(in); got != want {
			t.Errorf("cefEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSeverityOf_FailureHigherThanSuccess: failures rank higher for SOC alerting.
func TestSeverityOf_FailureHigherThanSuccess(t *testing.T) {
	r := sampleRecord()
	r.success = true
	sevOK := severityOf(r)
	r.success = false
	r.errorKind = "auth"
	sevAuth := severityOf(r)
	r.errorKind = "rate_limit"
	sevRL := severityOf(r)
	if sevOK >= sevAuth {
		t.Errorf("auth failure (%d) should outrank success (%d)", sevAuth, sevOK)
	}
	if sevAuth < sevRL {
		t.Errorf("auth (%d) should outrank rate_limit (%d)", sevAuth, sevRL)
	}
}

// TestSeverityOf_StateChange: link-layer events get mid severity.
func TestSeverityOf_StateChange(t *testing.T) {
	r := sampleRecord()
	r.eventType = "credential_switch"
	if sev := severityOf(r); sev != 4 {
		t.Errorf("state change severity = %d, want 4", sev)
	}
}

// TestClassNameOf: class name reflects event nature.
func TestClassNameOf(t *testing.T) {
	r := sampleRecord()
	if got := classNameOf(r); got != "LLMRequest" {
		t.Errorf("success class = %q, want LLMRequest", got)
	}
	r.success = false
	if got := classNameOf(r); got != "LLMRequestFailed" {
		t.Errorf("failed class = %q, want LLMRequestFailed", got)
	}
	r.success = true
	r.eventType = "credential_switch"
	if got := classNameOf(r); got != "GatewayStateChange" {
		t.Errorf("state-change class = %q, want GatewayStateChange", got)
	}
}

// TestFileSink_WriteRead: write a line, read it back, verify content.
func TestFileSink_WriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "siem.log")
	sink, err := NewFileSink(path)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	defer sink.Close()

	line := FormatCEF(sampleRecord())
	if err := sink.Write(context.Background(), line); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// second write to confirm append + concurrency-safe
	if err := sink.Write(context.Background(), line); err != nil {
		t.Fatalf("Write2: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(data)
	if lines := strings.Count(body, "\n"); lines != 2 {
		t.Errorf("expected 2 lines, got %d", lines)
	}
	if !strings.Contains(body, "requestId=req-123") {
		t.Error("file content missing the CEF line")
	}
}

// TestFileSink_CloseIdempotent: double-close must not panic.
func TestFileSink_CloseIdempotent(t *testing.T) {
	sink, _ := NewFileSink(filepath.Join(t.TempDir(), "x.log"))
	if err := sink.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestFileSink_WriteAfterClose: writing to a closed sink errors.
func TestFileSink_WriteAfterClose(t *testing.T) {
	sink, _ := NewFileSink(filepath.Join(t.TempDir(), "x.log"))
	sink.Close()
	if err := sink.Write(context.Background(), "x"); err == nil {
		t.Error("Write after Close must error")
	}
}

// TestFileSink_DefaultPath: empty path falls back to the safe local default.
func TestFileSink_DefaultPath(t *testing.T) {
	// We can't write to /var/log in a test; just verify the constant is a
	// local path, not a URL (the security invariant).
	if strings.HasPrefix(DefaultLocalEndpoint, "http") {
		t.Errorf("DefaultLocalEndpoint must be a local file, got %q", DefaultLocalEndpoint)
	}
}

// TestFileSink_ContextCancel: a cancelled ctx aborts the write.
func TestFileSink_ContextCancel(t *testing.T) {
	sink, _ := NewFileSink(filepath.Join(t.TempDir(), "x.log"))
	defer sink.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sink.Write(ctx, "x"); err == nil {
		t.Error("Write with cancelled ctx must error")
	}
}

// TestFormatCEF_EmptyFields: empty strings render as "-" not empty (CEF safe).
func TestFormatCEF_EmptyFields(t *testing.T) {
	r := mockRecord{ts: time.UnixMilli(1)} // all string fields empty
	line := FormatCEF(r)
	if strings.Contains(line, "errorKind="+"\n") || strings.Contains(line, "errorKind= ") {
		t.Errorf("empty errorKind should render as '-', got: %s", line)
	}
	if !strings.Contains(line, "errorKind=-") {
		t.Errorf("expected errorKind=- for empty, got: %s", line)
	}
}
