package transformation

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCompressMessagesIfNeeded_LargeRequestAggressiveTrim verifies that
// requests > 1MB get aggressive trimming even when within soft limit.
//
// 2026-07-13: Test added to prevent regression of gpt-5.6-luna
// "context window exceeded" failures with 577+ message sessions.
func TestCompressMessagesIfNeeded_LargeRequestAggressiveTrim(t *testing.T) {
	messages := make([]map[string]interface{}, 0, 500)
	for i := 0; i < 500; i++ {
		messages = append(messages, map[string]interface{}{
			"role":    "user",
			"content": strings.Repeat("x", 4000),
		})
		messages = append(messages, map[string]interface{}{
			"role":    "assistant",
			"content": strings.Repeat("y", 4000),
		})
	}

	bodyMap := map[string]interface{}{
		"model":    "test-model",
		"messages": messages,
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		t.Fatal(err)
	}

	if len(body) < 1024*1024 {
		t.Fatalf("test setup error: body is only %d bytes, need >1MB", len(body))
	}

	out := CompressMessagesIfNeeded(body, 200*1024)

	if len(out) >= len(body) {
		t.Errorf("expected aggressive trim for >1MB request, got %d bytes (orig %d)",
			len(out), len(body))
	}
	if len(out) > len(body)/2 {
		t.Errorf("expected trim to >50%%, got %d/%d bytes", len(out), len(body))
	}
	t.Logf("Original: %d bytes → Trimmed: %d bytes (%.1f%% reduction)",
		len(body), len(out), 100*(1-float64(len(out))/float64(len(body))))
}
