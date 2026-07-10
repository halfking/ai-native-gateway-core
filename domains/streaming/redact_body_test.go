package streaming

import (
	"context"
	"database/sql"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

func TestBuildRedactBodyFn_NoChecker(t *testing.T) {
	fn := BuildRedactBodyFn(nil, nil)
	if fn != nil {
		t.Fatal("expected nil when checker is nil")
	}
}

func TestBuildRedactBodyFn_EmptyBody(t *testing.T) {
	// NewChecker requires DB and will panic with nil, so skip
	t.Skip("NewChecker requires real DB connection")
}

func TestBuildRedactBodyFn_NonJSON(t *testing.T) {
	// Skip tests that require DB
	t.Skip("requires real DB for checker initialization")
}

func TestExtractAssistantContent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"hello world"}}]}`)
	content, err := extractAssistantContent(body)
	if err != nil {
		t.Fatal(err)
	}
	if content != "hello world" {
		t.Fatalf("expected 'hello world', got %q", content)
	}
}

func TestExtractAssistantContent_NoChoices(t *testing.T) {
	body := []byte(`{"choices":[]}`)
	content, err := extractAssistantContent(body)
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Fatalf("expected empty content, got %q", content)
	}
}

func TestRewriteAssistantContentInJSON(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"original","role":"assistant"}}],"usage":{}}`)
	redacted, ok := rewriteAssistantContentInJSON(body, "redacted")
	if !ok {
		t.Fatal("rewrite failed")
	}

	// Verify content replaced
	content, _ := extractAssistantContent(redacted)
	if content != "redacted" {
		t.Fatalf("expected 'redacted', got %q", content)
	}
}

func TestRewriteAssistantContentInJSON_InvalidJSON(t *testing.T) {
	_, ok := rewriteAssistantContentInJSON([]byte("not json"), "redacted")
	if ok {
		t.Fatal("expected rewrite to fail on invalid JSON")
	}
}

// TestBuildRedactBodyFn_Integration 测试完整流程（需要真实 DB 和 patterns）
func TestBuildRedactBodyFn_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// 需要真实 DB connection 来初始化 checker
	// 这里仅作示例框架，实际需要 testcontainers 或 mock DB
	t.Skip("integration test requires real DB setup")

	db, _ := sql.Open("postgres", "postgres://...")
	defer db.Close()

	checker, err := outputcompliance.NewChecker(db)
	if err != nil {
		t.Fatal(err)
	}

	ownerFn := func(sessionID, tenantID string) (string, string) {
		return "caller1", "owner1"
	}

	fn := BuildRedactBodyFn(checker, ownerFn)
	if fn == nil {
		t.Fatal("expected non-nil fn")
	}

	// 包含 PII 的响应
	body := []byte(`{"choices":[{"message":{"content":"Contact me at test@example.com"}}]}`)
	
	// Mock context and check
	ctx := context.Background()
	result, _ := checker.Check(ctx, "default", "Contact me at test@example.com")
	if len(result.Issues) == 0 {
		t.Skip("no PII patterns loaded, skipping redaction test")
	}

	// 调用 redact
	redacted := fn(body, "sess1", "default")
	
	// 验证已脱敏
	content, _ := extractAssistantContent(redacted)
	if content == "Contact me at test@example.com" {
		t.Fatal("expected content to be redacted")
	}
	if content != result.RedactedOutput {
		t.Fatalf("expected redacted content %q, got %q", result.RedactedOutput, content)
	}
}
