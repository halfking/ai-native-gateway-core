package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// stubQuerier is a minimal pgx-compatible QueryRow stub used to drive
// findActiveKeyConflict without a real database.
type stubQuerier struct {
	row  pgx.Row
	err  error
	sql  string
	args []any
}

func (s *stubQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	s.sql = sql
	s.args = args
	if s.err != nil {
		return errRow{err: s.err}
	}
	return s.row
}

// errRow always returns the wrapped error from Scan; it lets us assert that
// findActiveKeyConflict propagates non-ErrNoRows failures.
type errRow struct{ err error }

func (e errRow) Scan(dest ...any) error { return e.err }

func TestParseKeyActionRoute(t *testing.T) {
	tests := []struct {
		name      string
		remaining string
		want      keyActionRoute
	}{
		{
			name:      "root path",
			remaining: "",
			want:      keyActionRoute{kind: "root"},
		},
		{
			name:      "verify action without id",
			remaining: "verify",
			want:      keyActionRoute{kind: "action", subPath: "verify"},
		},
		{
			name:      "budget check action without id",
			remaining: "budget-check",
			want:      keyActionRoute{kind: "action", subPath: "budget-check"},
		},
		{
			name:      "apply action without id",
			remaining: "apply",
			want:      keyActionRoute{kind: "action", subPath: "apply"},
		},
		{
			name:      "lookup action without id",
			remaining: "lookup",
			want:      keyActionRoute{kind: "action", subPath: "lookup"},
		},
		{
			name:      "detail resource with id",
			remaining: "123/detail/123",
			want:      keyActionRoute{kind: "resource", idPart: "123", subPath: "detail/123"},
		},
		{
			name:      "reveal resource with id",
			remaining: "42/reveal",
			want:      keyActionRoute{kind: "resource", idPart: "42", subPath: "reveal"},
		},
		{
			name:      "plain numeric id",
			remaining: "88",
			want:      keyActionRoute{kind: "resource", idPart: "88", subPath: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseKeyActionRoute(tt.remaining)
			if got != tt.want {
				t.Fatalf("parseKeyActionRoute(%q) = %#v, want %#v", tt.remaining, got, tt.want)
			}
		})
	}
}

// row3 implements pgx.Row returning a fixed (id, prefix, isSystem) triple.
type row3 struct {
	id       int
	prefix   string
	isSystem bool
}

func (r row3) Scan(dest ...any) error {
	if len(dest) != 3 {
		return errors.New("row3: expected 3 scan targets")
	}
	*dest[0].(*int) = r.id
	*dest[1].(*string) = r.prefix
	*dest[2].(*bool) = r.isSystem
	return nil
}

func TestFindActiveKeyConflict_NoRowReturnsNil(t *testing.T) {
	q := &stubQuerier{row: errRow{err: pgx.ErrNoRows}}
	got, err := findActiveKeyConflict(context.Background(), q, 1, "default", "prod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil conflict, got %#v", got)
	}
}

func TestFindActiveKeyConflict_RegularKey(t *testing.T) {
	q := &stubQuerier{row: row3{id: 42, prefix: "sk-abc1234", isSystem: false}}
	got, err := findActiveKeyConflict(context.Background(), q, 1, "default", "prod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected conflict, got nil")
	}
	if got.ID != 42 || got.Prefix != "sk-abc1234" || got.IsSystem {
		t.Fatalf("unexpected conflict: %#v", got)
	}
}

func TestFindActiveKeyConflict_SystemKeyBlocks(t *testing.T) {
	q := &stubQuerier{row: row3{id: 7, prefix: "sk-sysXXXX", isSystem: true}}
	got, err := findActiveKeyConflict(context.Background(), q, 1, "default", "admin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected system-key conflict, got nil")
	}
	if !got.IsSystem {
		t.Fatalf("expected IsSystem=true, got %#v", got)
	}
	if got.ID != 7 {
		t.Fatalf("expected ID=7, got %d", got.ID)
	}
}

func TestFindActiveKeyConflict_DBErrorPropagates(t *testing.T) {
	dbErr := errors.New("connection reset")
	q := &stubQuerier{err: dbErr}
	_, err := findActiveKeyConflict(context.Background(), q, 1, "default", "prod")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected wrapped dbErr, got %v", err)
	}
}

// TestKeyConflictPredicateIncludesSystemGuard documents the SQL invariant:
// the conflict query must refuse creation whenever an enabled+active+unexpired
// row exists OR a system key exists in the same (app, tenant, alias) group.
// If a future refactor drops the is_system branch, this test fails —
// preventing a regression of the "system key in use blocks duplicates" rule.
func TestKeyConflictPredicateIncludesSystemGuard(t *testing.T) {
	q := &stubQuerier{row: errRow{err: pgx.ErrNoRows}}
	_, _ = findActiveKeyConflict(context.Background(), q, 1, "default", "prod")
	if q.sql == "" {
		t.Fatal("stubQuerier did not record any SQL")
	}
	for _, marker := range []string{
		"COALESCE(is_system, FALSE) = TRUE", // system-key guard
		"enabled = TRUE",                    // active-key guard
		"COALESCE(status, 'active') = 'active'",
		"expires_at IS NULL OR expires_at > now()",
	} {
		if !strings.Contains(q.sql, marker) {
			t.Errorf("key conflict SQL missing %q\nfull SQL:\n%s", marker, q.sql)
		}
	}
}

// TestExtractLookupParams exercises the pure validation function that backs
// GET /api/keys/lookup.  The contract is:
//   - application_code and key_alias are required (whitespace-only fails)
//   - tenant_id is optional and defaults to "default"
//   - whitespace surrounding the values is trimmed
func TestExtractLookupParams(t *testing.T) {
	tests := []struct {
		name        string
		q           url.Values
		wantApp     string
		wantTenant  string
		wantAlias   string
		wantErrSubs string // substring expected in err message; empty = no error
	}{
		{
			name:       "happy path with explicit tenant",
			q:          url.Values{"application_code": {"default"}, "tenant_id": {"acme"}, "key_alias": {"prod"}},
			wantApp:    "default",
			wantTenant: "acme",
			wantAlias:  "prod",
		},
		{
			name:       "default tenant when omitted",
			q:          url.Values{"application_code": {"default"}, "key_alias": {"prod"}},
			wantApp:    "default",
			wantTenant: "default",
			wantAlias:  "prod",
		},
		{
			name:        "missing application_code",
			q:           url.Values{"key_alias": {"prod"}},
			wantErrSubs: "application_code",
		},
		{
			name:        "missing key_alias",
			q:           url.Values{"application_code": {"default"}},
			wantErrSubs: "key_alias",
		},
		{
			name:        "whitespace application_code",
			q:           url.Values{"application_code": {"   "}, "key_alias": {"prod"}},
			wantErrSubs: "application_code",
		},
		{
			name:        "whitespace key_alias",
			q:           url.Values{"application_code": {"default"}, "key_alias": {"\t\t"}},
			wantErrSubs: "key_alias",
		},
		{
			name:       "leading/trailing whitespace is trimmed",
			q:          url.Values{"application_code": {"  default  "}, "tenant_id": {"  "}, "key_alias": {"  prod  "}},
			wantApp:    "default",
			wantTenant: "default", // blank → default
			wantAlias:  "prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, tenant, alias, err := extractLookupParams(tt.q)
			if tt.wantErrSubs != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSubs)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubs) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErrSubs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if app != tt.wantApp || tenant != tt.wantTenant || alias != tt.wantAlias {
				t.Fatalf("got (app=%q tenant=%q alias=%q), want (app=%q tenant=%q alias=%q)",
					app, tenant, alias, tt.wantApp, tt.wantTenant, tt.wantAlias)
			}
		})
	}
}

// TestLookupKeyConflict_MethodAndNilDB guards the early-exit paths of
// lookupKeyConflict that don't need the database: wrong HTTP method → 405,
// no pool configured → 503.  These tests deliberately do NOT exercise the
// Postgres path; that coverage is provided indirectly by
// TestFindActiveKeyConflict_* on the helper itself.
func TestLookupKeyConflict_MethodAndNilDB(t *testing.T) {
	t.Run("POST not allowed", func(t *testing.T) {
		h := &Handler{}
		req := httptest.NewRequest(http.MethodPost, "/api/keys/lookup?application_code=default&key_alias=prod", nil)
		rr := httptest.NewRecorder()
		h.lookupKeyConflict(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("nil db rejected", func(t *testing.T) {
		h := &Handler{} // db is nil
		req := httptest.NewRequest(http.MethodGet, "/api/keys/lookup?application_code=default&key_alias=prod", nil)
		rr := httptest.NewRecorder()
		h.lookupKeyConflict(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d body=%s", rr.Code, rr.Body.String())
		}
	})
}

