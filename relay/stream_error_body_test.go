package relay

import (
	"strings"
	"testing"
)

func TestIsJSONErrorBody_OpenAIStyleEnvelope(t *testing.T) {
	body := []byte(`{"error": {"type": "service_unavailable", "message": "Stream error: proxy stream error: 积分不足，请升级套餐"}}`)
	ok, kind, msg := isJSONErrorBody(body)
	if !ok {
		t.Fatalf("expected match, got false")
	}
	if kind != "service_unavailable" {
		t.Errorf("expected kind=service_unavailable, got %q", kind)
	}
	if !strings.Contains(msg, "积分不足") {
		t.Errorf("expected message to contain 积分不足, got %q", msg)
	}
}

func TestIsJSONErrorBody_CodeInsteadOfType(t *testing.T) {
	body := []byte(`{"error": {"code": "insufficient_quota", "message": "You have exceeded your quota"}}`)
	ok, kind, msg := isJSONErrorBody(body)
	if !ok {
		t.Fatalf("expected match, got false")
	}
	if kind != "insufficient_quota" {
		t.Errorf("expected kind=insufficient_quota, got %q", kind)
	}
	if !strings.Contains(msg, "exceeded") {
		t.Errorf("expected message to mention exceeded, got %q", msg)
	}
}

func TestIsJSONErrorBody_BareShapeNoWrapper(t *testing.T) {
	body := []byte(`{"type": "upstream_error", "message": "billing pool switched"}`)
	ok, kind, msg := isJSONErrorBody(body)
	if !ok {
		t.Fatalf("expected match, got false")
	}
	if kind != "upstream_error" {
		t.Errorf("expected kind=upstream_error, got %q", kind)
	}
	if msg != "billing pool switched" {
		t.Errorf("expected msg=billing pool switched, got %q", msg)
	}
}

func TestIsJSONErrorBody_EmptyErrorObject(t *testing.T) {
	body := []byte(`{"error": {}}`)
	ok, _, _ := isJSONErrorBody(body)
	if ok {
		t.Errorf("empty error object should NOT match (no signal to act on)")
	}
}

func TestIsJSONErrorBody_NoErrorKey(t *testing.T) {
	body := []byte(`{"model": "claude-opus-4-8", "choices": [{"index": 0, "delta": {"content": "hi"}}]}`)
	ok, _, _ := isJSONErrorBody(body)
	if ok {
		t.Errorf("valid success body should not be mis-classified as an error")
	}
}

func TestIsJSONErrorBody_Empty(t *testing.T) {
	for _, body := range [][]byte{nil, []byte(""), []byte("   \n\n  ")} {
		ok, _, _ := isJSONErrorBody(body)
		if ok {
			t.Errorf("empty body should not match: %q", string(body))
		}
	}
}

func TestIsJSONErrorBody_NotJSON(t *testing.T) {
	for _, body := range []string{
		"data: {\"id\":\"chat-1\"}\n\n",
		"event: ping\n\n",
		"plain text response",
		":heartbeat\n\n",
	} {
		ok, _, _ := isJSONErrorBody([]byte(body))
		if ok {
			t.Errorf("non-JSON body should not match: %q", body)
		}
	}
}

func TestIsJSONErrorBody_TrailingSSEArtifacts(t *testing.T) {
	// Upstream sometimes returns plain JSON error followed by a
	// stray "\n\n" — common when the proxy is itself an SSE
	// emitter and forgets to suppress the terminator on the
	// error path. The helper must accept the trailing whitespace.
	body := []byte("{\"error\": {\"type\": \"service_unavailable\", \"message\": \"Stream error: 当前算力池切换中，请重试即可\"}}\n\n")
	ok, kind, msg := isJSONErrorBody(body)
	if !ok {
		t.Fatalf("expected match despite trailing \\n\\n, got false")
	}
	if kind != "service_unavailable" {
		t.Errorf("expected kind=service_unavailable, got %q", kind)
	}
	if !strings.Contains(msg, "算力池") {
		t.Errorf("expected message to contain 算力池, got %q", msg)
	}
}

func TestIsJSONErrorBody_DefensiveStripDataPrefix(t *testing.T) {
	// Stream readers should never pass SSE-prefixed lines, but if
	// one slips through the helper must not crash and must still
	// detect the JSON error inside the data: envelope.
	body := []byte(`data: {"error": {"type": "billing_error", "message": "quota exhausted"}}`)
	ok, kind, msg := isJSONErrorBody(body)
	if !ok {
		t.Fatalf("expected match after stripping data: prefix, got false")
	}
	if kind != "billing_error" {
		t.Errorf("expected kind=billing_error, got %q", kind)
	}
	if !strings.Contains(msg, "quota exhausted") {
		t.Errorf("expected message to mention quota exhausted, got %q", msg)
	}
}
