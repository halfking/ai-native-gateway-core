package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoTopicSessionKeyStable(t *testing.T) {
	a := noTopicSessionKey("sk-abc", "2026-06-19T10:00:00Z")
	b := noTopicSessionKey("sk-abc", "2026-06-19T10:00:00Z")
	if a != b {
		t.Fatalf("expected stable key, got %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "notopic:") {
		t.Fatalf("expected notopic prefix, got %q", a)
	}
	if a == noTopicSessionKey("sk-xyz", "2026-06-19T10:00:00Z") {
		t.Fatal("different prefix should yield different key")
	}
}

func TestNoTopicHourFilterEmpty(t *testing.T) {
	frag, args := noTopicHourFilter("", []any{"p", 24})
	if frag != "" {
		t.Fatalf("expected empty fragment, got %q", frag)
	}
	if len(args) != 2 {
		t.Fatalf("args should be unchanged, got %v", args)
	}
}

func TestNoTopicHourFilterWithHourStart(t *testing.T) {
	frag, args := noTopicHourFilter("2026-06-19T10:00:00Z", []any{"p", 24})
	if frag == "" {
		t.Fatal("expected hour filter fragment")
	}
	if !strings.Contains(frag, "DATE_TRUNC('hour', ts)") {
		t.Fatalf("expected hour truncation filter, got %q", frag)
	}
	if len(args) != 3 || args[2] != "2026-06-19T10:00:00Z" {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestNoTopicLogsWhereUsesNullTaskFilter(t *testing.T) {
	clause, args := noTopicLogsWhere("sk-test", 48, nil)
	if !strings.Contains(clause, "gw_task_id IS NULL") {
		t.Fatalf("expected null task filter, got %q", clause)
	}
	if !strings.Contains(clause, "api_key_prefix = $1") {
		t.Fatalf("expected prefix placeholder, got %q", clause)
	}
	if len(args) != 2 || args[0] != "sk-test" || args[1] != 48 {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestNoTopicLogsWhere_TenantAdminScoped(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{TenantID: "acme", Role: "tenant_admin", IsJWT: true})
	clause, args := noTopicLogsWhere("sk-test", 24, r)
	if len(args) != 3 || args[2] != "acme" {
		t.Fatalf("expected tenant in args, got %v", args)
	}
	if !strings.Contains(clause, "tenant_id = $3") {
		t.Fatalf("expected tenant filter in clause, got %q", clause)
	}
}
