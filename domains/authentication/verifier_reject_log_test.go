package authentication

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// The verification predicate in callVerifyDB folds four different failures into
// one pgx.ErrNoRows — unknown key, enabled=false, revoked|disabled, expired.
// They all produce the same 401 body, and neither nginx nor request_logs keeps
// the credential, so an operator could not tell "the client rotated its key"
// from "we revoked it" (2026-10-05~07, ~3,200 failing requests/day).
//
// These gates pin the only thing that makes that question answerable: the
// rejection path must emit a WARN carrying the key_hash prefix of the key that
// was actually presented. Testing keyHashLogPrefix alone would be a constant
// assertion — the helper could pass while the call site never fires.

// captureRejectionLog runs fn with slog redirected to a JSON buffer and returns
// the decoded records.
func captureRejectionLog(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	fn()

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("rejection log line is not JSON: %v (line=%q)", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// findRejectRecord returns the "key verify: rejected" WARN, or nil.
func findRejectRecord(recs []map[string]any) map[string]any {
	for _, r := range recs {
		if r["msg"] == "key verify: rejected" {
			return r
		}
	}
	return nil
}

// TestKeyVerifier_RejectedLogsQueryableHashPrefix is the load-bearing gate: the
// ErrNoRows branch must log, and the logged prefix must equal the HMAC of the
// presented key so it can be looked up against api_keys.key_hash.
func TestKeyVerifier_RejectedLogsQueryableHashPrefix(t *testing.T) {
	const rawKey = "sk-the-key-a-client-actually-sent"
	const secret = "unit-test-secret"

	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, secret)

	// An empty result set makes pgx return ErrNoRows on Scan, which is the
	// single branch all four rejection causes funnel into.
	empty := pgxmock.NewRows([]string{
		"id", "tenant_id", "application_id", "application_code", "key_prefix",
		"default_client_profile", "owner_user", "rate_limit_rpm",
		"rate_limit_concurrent", "rate_limit_tpm", "key_tier", "budget_usd",
		"status", "key_alias", "customer_id", "expires_at",
	})
	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(empty)

	var gotErr error
	recs := captureRejectionLog(t, func() {
		_, gotErr = kv.Verify(context.Background(), rawKey)
	})
	if _, ok := gotErr.(*InvalidKeyError); !ok {
		t.Fatalf("err = %v (%T), want *InvalidKeyError", gotErr, gotErr)
	}

	rec := findRejectRecord(recs)
	if rec == nil {
		t.Fatalf("rejection path logged nothing with msg=%q; records=%v", "key verify: rejected", recs)
	}
	if lvl := rec["level"]; lvl != "WARN" {
		t.Fatalf("level = %v, want WARN", lvl)
	}

	got, _ := rec["key_hash_prefix"].(string)
	want := HashAPIKey(secret, rawKey)[:16]
	if got != want {
		t.Fatalf("key_hash_prefix = %q, want %q (HMAC of the presented key, queryable against api_keys.key_hash)", got, want)
	}

	// Security invariant: the raw credential must never reach the log. The
	// prefix is only useful precisely because it is not reversible.
	for _, r := range recs {
		if strings.Contains(line(r), rawKey) {
			t.Fatalf("rejection log leaked the raw key: %v", r)
		}
	}
}

// TestKeyVerifier_RejectedDoesNotLogOnSuccess guards the other direction: a
// successful verification must stay silent, otherwise the gate above could be
// satisfied by a log line that fires on every request.
func TestKeyVerifier_AcceptedStaysSilent(t *testing.T) {
	mp := newMockPool(t)
	defer mp.Close()

	kv := NewKeyVerifier()
	kv.setDBQuerier(mp, "unit-test-secret")

	// NULL for every pointer/nullable column, mirroring the existing
	// success-path test: several of these scan into *string/*int and pgx
	// rejects a bare string there.
	rows := pgxmock.NewRows([]string{
		"id", "tenant_id", "application_id", "application_code", "key_prefix",
		"default_client_profile", "owner_user", "rate_limit_rpm",
		"rate_limit_concurrent", "rate_limit_tpm", "key_tier", "budget_usd",
		"status", "key_alias", "customer_id", "expires_at",
	}).AddRow(7, "tenant-x", 1, "app1", "sk-7****", nil, nil, nil, nil, nil,
		"default", nil, "active", nil, nil, nil)
	mp.ExpectQuery(`SELECT`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)
	mp.ExpectExec(`UPDATE api_keys`).WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	recs := captureRejectionLog(t, func() {
		if _, err := kv.Verify(context.Background(), "sk-good"); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	})
	if rec := findRejectRecord(recs); rec != nil {
		t.Fatalf("successful verification logged a rejection: %v", rec)
	}
}

func TestKeyHashLogPrefix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"full HMAC-SHA256 hex", HashAPIKey("s", "sk-x"), HashAPIKey("s", "sk-x")[:16]},
		{"exactly 16 chars", "0123456789abcdef", "0123456789abcdef"},
		{"longer than 16", "0123456789abcdefZZZZ", "0123456789abcdef"},
		// Degenerate inputs must not leak length and must never look like a
		// real prefix — an empty hash would otherwise match nothing yet
		// read like a legitimate id.
		{"empty", "", "<short>"},
		{"one char", "a", "<short>"},
		{"15 chars", "0123456789abcde", "<short>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := keyHashLogPrefix(c.in)
			if got != c.want {
				t.Fatalf("keyHashLogPrefix(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// line re-renders a record for substring checks.
func line(rec map[string]any) string {
	b, err := json.Marshal(rec)
	if err != nil {
		return ""
	}
	return string(b)
}
