package memora

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskID_FromHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Task-Id", "fix-1234")
	got := TaskID(r, []byte(`{"model":"m"}`), 42)
	if got != "fix-1234" {
		t.Fatalf("got %q want fix-1234", got)
	}
}

func TestTaskID_FromBodyMetadata(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	body := []byte(`{"model":"m","metadata":{"task_id":"from-body"}}`)
	got := TaskID(r, body, 42)
	if got != "m:from-body" {
		t.Fatalf("got %q want m:from-body", got)
	}
}

func TestTaskID_AutoDerived(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got := TaskID(r, body, 42)
	if !strings.HasPrefix(got, "gateway:auto:42:") {
		t.Fatalf("got %q want gateway:auto:42:...", got)
	}
}

func TestTaskID_EmptyWhenNoSignal(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if got := TaskID(r, nil, 42); got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

func TestUserID_Namespacing(t *testing.T) {
	if got := UserID(42, "fix-1234"); got != "k:42:fix-1234" {
		t.Fatalf("got %q want k:42:fix-1234", got)
	}
	if got := UserID(42, ""); got != "" {
		t.Fatalf("empty task should yield empty user_id, got %q", got)
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("a\nb\rc\td", 10); got != "a b c d" {
		t.Fatalf("got %q", got)
	}
}
