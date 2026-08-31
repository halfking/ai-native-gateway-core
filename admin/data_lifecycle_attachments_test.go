package admin

import (
	"context"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// audit-data-closure-B hotfix (2026-08-31): the previous implementation of
// uuidOrZero emitted raw hex (32 chars, no dashes), which PostgreSQL's uuid
// type rejected with `invalid input syntax for type uuid`. Every cleanup
// attempt then HTTP 500'd and rolled back, leaving zero audit rows. This
// test pins the contract: the returned value MUST be a valid RFC 4122 v4
// UUID in dashed form (the format uuid.NewString / uuid.Parse accept).
func TestUUIDOrZero_ReturnsDashedRFC4122(t *testing.T) {
	got := uuidOrZero(context.Background())
	if got == "" {
		t.Fatal("uuidOrZero returned empty string")
	}
	parsed, err := uuid.Parse(got)
	if err != nil {
		t.Fatalf("uuidOrZero returned %q, which is not a valid RFC 4122 uuid: %v", got, err)
	}
	if got[14] != '4' {
		t.Errorf("uuidOrZero returned %q; uuid.Parse succeeded but the version nibble is %q, want '4'", got, got[14])
	}
	if v := parsed.Version(); v != 4 {
		t.Errorf("uuidOrZero returned UUID version %d, want 4", v)
	}
	if len(got) != 36 {
		t.Errorf("uuidOrZero returned %d-char string %q, want 36", len(got), got)
	}
	for _, pos := range []int{8, 13, 18, 23} {
		if got[pos] != '-' {
			t.Errorf("uuidOrZero returned %q; expected '-' at position %d", got, pos)
		}
	}
	matched, _ := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, got)
	if !matched {
		t.Errorf("uuidOrZero returned %q; does not match RFC 4122 v4 dashed regex", got)
	}
}

func TestUUIDOrZero_EntropyFailureFallbackIsValidUUID(t *testing.T) {
	for i := 0; i < 100; i++ {
		got := uuidOrZero(context.Background())
		if _, err := uuid.Parse(got); err != nil {
			t.Fatalf("iteration %d: uuidOrZero returned %q, not a valid uuid: %v", i, got, err)
		}
	}
	if _, err := uuid.Parse("00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatalf("entropy-failure fallback literal is not a valid uuid: %v", err)
	}
}

// ── audit-data-closure-2 (2026-08-31) tests ─────────────────────────────────

// TestAttachmentCleanup_TenantScope_Helper exercises
// attachmentTenantScope, the helper that the cleanup execute handler
// (and its read-side siblings) use to derive a tenant predicate.
//
//   - tenant_admin gets ` AND tenant_id = $1` with their own tenant.
//   - super_admin without explicit tenant_id gets no predicate.
//   - super_admin with `?tenant_id=...` gets ` AND tenant_id = $1`.
func TestAttachmentCleanup_TenantScope_Helper(t *testing.T) {
	t.Run("tenant_admin", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", nil)
		req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant_x"})
		pred, args := attachmentTenantScope(req, "")
		if pred != " AND tenant_id = $1" {
			t.Errorf("pred = %q, want %q", pred, " AND tenant_id = $1")
		}
		if len(args) != 1 || args[0] != "tenant_x" {
			t.Errorf("args = %v, want [tenant_x]", args)
		}
	})
	t.Run("super_admin_no_filter", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", nil)
		req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default"})
		pred, args := attachmentTenantScope(req, "")
		if pred != "" {
			t.Errorf("pred = %q, want empty", pred)
		}
		if len(args) != 0 {
			t.Errorf("args = %v, want empty", args)
		}
	})
	t.Run("super_admin_explicit_tenant", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x?tenant_id=tenant_z", nil)
		req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "default"})
		pred, args := attachmentTenantScope(req, "tenant_z")
		if pred != " AND tenant_id = $1" {
			t.Errorf("pred = %q, want %q", pred, " AND tenant_id = $1")
		}
		if len(args) != 1 || args[0] != "tenant_z" {
			t.Errorf("args = %v, want [tenant_z]", args)
		}
	})
}

// TestAttachmentCleanup_RetryableClassification pins the pgconn error
// codes the audit-data-closure-2 retry helper considers transient. A
// future edit that adds a new code (or removes one) must update this
// test alongside the change so production behaviour does not silently
// drift.
func TestAttachmentCleanup_RetryableClassification(t *testing.T) {
	cases := []struct {
		code     string
		retryable bool
	}{
		{"40001", true},  // serialization_failure
		{"40P01", true},  // deadlock_detected
		{"40XL1", true},  // serialization class
		{"57P03", true},  // cannot_connect_now
		{"53300", true},  // too_many_connections
		{"23505", false}, // unique_violation
		{"42P01", false}, // undefined_table
		{"42703", false}, // undefined_column
	}
	for _, c := range cases {
		got := isRetryablePgError(&pgconn.PgError{Code: c.code})
		if got != c.retryable {
			t.Errorf("code %s: isRetryablePgError = %v, want %v", c.code, got, c.retryable)
		}
	}
	if isRetryablePgError(nil) {
		t.Error("nil error must not be retryable")
	}
	if isRetryablePgError(stringError("plain")) {
		t.Error("non-pg error must not be retryable")
	}
}

// TestAttachmentCleanup_RunWithRetry verifies the call-counting
// semantics: a retryable error causes re-invocation; a non-retryable
// error returns immediately; success on first try returns no error;
// exhausted retries return the last error.
func TestAttachmentCleanup_RunWithRetry(t *testing.T) {
	t.Run("success_first_try", func(t *testing.T) {
		calls := 0
		err := runWithRetry(context.Background(), 3, time.Millisecond, func() error {
			calls++
			return nil
		})
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})
	t.Run("retries_until_success", func(t *testing.T) {
		calls := 0
		err := runWithRetry(context.Background(), 3, time.Millisecond, func() error {
			calls++
			if calls < 3 {
				return &pgconn.PgError{Code: "40001", Message: "serialization_failure"}
			}
			return nil
		})
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})
	t.Run("terminal_error_no_retry", func(t *testing.T) {
		calls := 0
		err := runWithRetry(context.Background(), 3, time.Millisecond, func() error {
			calls++
			return &pgconn.PgError{Code: "23505", Message: "duplicate key"}
		})
		if err == nil {
			t.Error("expected non-nil")
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})
	t.Run("exhausted_returns_last_error", func(t *testing.T) {
		calls := 0
		err := runWithRetry(context.Background(), 2, time.Millisecond, func() error {
			calls++
			return &pgconn.PgError{Code: "40001"}
		})
		if err == nil {
			t.Error("expected non-nil after exhaustion")
		}
		if calls != 2 {
			t.Errorf("calls = %d, want 2", calls)
		}
	})
}

// TestAttachmentCleanup_MalformedHashSQLFragment pins the SQL
// fragment that the audit-data-closure-2 cleanup execute handler uses
// to defend against malformed-JSON elements. We do not exercise the
// full SQL path here (that requires a live PG with migration 629 +
// 632 tables; see scripts/audit/verify-migrations-627-631.sh) — we
// only assert that the source file still contains the NOT-NULL
// guards so a future regression to the bare COALESCE chain (which
// would re-introduce the literal "NULL" string audit row) is caught
// at code review.
func TestAttachmentCleanup_MalformedHashSQLFragment(t *testing.T) {
	body, err := os.ReadFile("data_lifecycle_attachments.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	src := string(body)
	if !regexp.MustCompile(`NULLIF\(COALESCE\(att->>'hash'`).MatchString(src) {
		t.Error("malformed-hash defense missing: NULLIF(COALESCE(att->>'hash'...) IS NOT NULL guard is required")
	}
	if !regexp.MustCompile(`att \? 'hash' OR att \? 'sha256' OR att \? 'id' OR att \? 'url'`).MatchString(src) {
		t.Error("malformed-hash defense missing: identity-key check (att ? 'hash' OR ...) is required")
	}
}

// TestAttachmentCleanup_FSAuditRowInserted exercises the parallel
// audit_attachments_filesystem_cleanup INSERT that the cleanup
// execute handler issues when the request body includes
// filesystem_paths. We do not exercise the SQL path end-to-end
// (see the SQL-fragment test above) but assert that the handler
// source contains the INSERT statement and the cleanup_run_id
// reference so a future refactor that drops the FS row will be
// caught at code review.
func TestAttachmentCleanup_FSAuditRowInserted(t *testing.T) {
	body, err := os.ReadFile("data_lifecycle_attachments.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	src := string(body)
	if !regexp.MustCompile(`INSERT INTO audit_attachments_filesystem_cleanup`).MatchString(src) {
		t.Error("cleanup execute handler must INSERT into audit_attachments_filesystem_cleanup when filesystem_paths is provided")
	}
	if !regexp.MustCompile(`filesystem_paths`).MatchString(src) {
		t.Error("cleanup execute handler must read filesystem_paths from the request body")
	}
	if !regexp.MustCompile(`__fs_only__`).MatchString(src) {
		t.Error("FS-only entries must use a sentinel request_id so the UNIQUE (request_id, file_path, cleanup_run_id) constraint holds")
	}
}

type stringError string

func (e stringError) Error() string { return string(e) }
