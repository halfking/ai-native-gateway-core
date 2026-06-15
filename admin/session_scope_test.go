package admin

import (
	"net/http/httptest"
	"testing"
)

func TestParseSessionScope_Defaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/system/session-messages/default", nil)
	sc := parseSessionScope(r)
	if sc.Hours != 24 {
		t.Fatalf("expected hours=24, got %d", sc.Hours)
	}
	if sc.SessionID != "" {
		t.Fatalf("expected empty session_id, got %q", sc.SessionID)
	}
}

func TestParseSessionScope_QueryParams(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?hours=72&session_id=sess-1", nil)
	sc := parseSessionScope(r)
	if sc.Hours != 72 {
		t.Fatalf("expected hours=72, got %d", sc.Hours)
	}
	if sc.SessionID != "sess-1" {
		t.Fatalf("expected session_id=sess-1, got %q", sc.SessionID)
	}
}

func TestSessionLogsWhere_WithSession(t *testing.T) {
	clause, args := sessionLogsWhere("default", sessionScope{Hours: 24, SessionID: "abc"}, nil)
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d", len(args))
	}
	if args[0] != "default" || args[1] != 24 || args[2] != "abc" {
		t.Fatalf("unexpected args: %v", args)
	}
	if clause == "" || clause[:5] != "WHERE" {
		t.Fatalf("expected WHERE clause, got %q", clause)
	}
}
