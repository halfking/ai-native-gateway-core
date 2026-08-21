package summarystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestUpsert_NilPoolIsError — nil pool must not panic; must return error.
// This is the cheap safety net that lets unit tests construct Store{} and
// verify other code paths without needing a real DB.
//
// 2026-08-06: signature changed to (UpsertResult, error). Nil-pool path
// must return a zero UpsertResult alongside the error so callers don't
// dereference nil fields.
func TestUpsert_NilPoolIsError(t *testing.T) {
	var s *Store
	res, err := s.Upsert(context.Background(), Summary{SessionKey: "gw_x", TenantID: "tA"})
	if err == nil {
		t.Fatal("expected error from nil Store")
	}
	if res.Version != 0 || res.Updated {
		t.Fatalf("nil Store Upsert returned %+v, want zero UpsertResult", res)
	}
	s2 := &Store{}
	res2, err2 := s2.Upsert(context.Background(), Summary{SessionKey: "gw_x", TenantID: "tA"})
	if err2 == nil {
		t.Fatal("expected error from Store with nil pool")
	}
	if res2.Version != 0 || res2.Updated {
		t.Fatalf("nil-pool Store Upsert returned %+v, want zero UpsertResult", res2)
	}
}

// TestUpsertResult_ZeroValueValid — UpsertResult{} is the documented
// "no result / error" sentinel. Generators should be able to declare
// one without pre-declaring every field.
func TestUpsertResult_ZeroValueValid(t *testing.T) {
	var r UpsertResult
	if r.Version != 0 || r.Updated {
		t.Fatalf("zero UpsertResult should be all-zero, got %+v", r)
	}
}

// TestLastSummarized_NilPoolIsError — same nil-safety contract for the
// reader path. NOTE: there are no DB integration tests for this package
// in-tree today (no tests/db_integration/ directory exists). The SQL is
// covered by manual deployment verification on 252.
func TestLastSummarized_NilPoolIsError(t *testing.T) {
	s := &Store{}
	if _, err := s.LastSummarized(context.Background(), "tA", "gw_x"); err == nil {
		t.Fatal("expected error from nil-pool LastSummarized")
	}
}

// TestCountNewTurns_NilPoolIsError — same nil-safety for the rolling gate.
func TestCountNewTurns_NilPoolIsError(t *testing.T) {
	s := &Store{}
	if _, err := s.CountNewTurns(context.Background(), "tA", "gw_x", time.Now()); err == nil {
		t.Fatal("expected error from nil-pool CountNewTurns")
	}
}

// TestCountTotalTurns_NilPoolIsError — same nil-safety for the rolling gate.
func TestCountTotalTurns_NilPoolIsError(t *testing.T) {
	s := &Store{}
	if _, err := s.CountTotalTurns(context.Background(), "tA", "gw_x"); err == nil {
		t.Fatal("expected error from nil-pool CountTotalTurns")
	}
}

// TestUpsertCAS_NilPoolIsError — strict CAS 接口的 nil-safety 与 Upsert 对齐。
func TestUpsertCAS_NilPoolIsError(t *testing.T) {
	s := &Store{}
	_, err := s.UpsertCAS(context.Background(), Summary{SessionKey: "gw_x", TenantID: "tA"}, 0)
	if err == nil {
		t.Fatal("expected error from nil-pool UpsertCAS")
	}
	_, err = s.UpsertCAS(context.Background(), Summary{SessionKey: "gw_x", TenantID: "tA"}, 5)
	if err == nil {
		t.Fatal("expected error from nil-pool UpsertCAS (expectedVersion>0)")
	}
}

// TestUpsertCAS_NegativeVersionRejected — expectedVersion 必须 >= 0。
func TestUpsertCAS_NegativeVersionRejected(t *testing.T) {
	s := &Store{}
	_, err := s.UpsertCAS(context.Background(), Summary{SessionKey: "gw_x", TenantID: "tA"}, -1)
	if err == nil {
		t.Fatal("expected error for negative expectedVersion")
	}
}

// TestErrStaleVersion_Is_Error — errors.Is 必须能识别 ErrStaleVersion，
// caller 用 errors.Is 判断走「重读 / 重试 / 放弃」分支。
func TestErrStaleVersion_Is_Error(t *testing.T) {
	// 直接 errors.Is(ErrStaleVersion, ErrStaleVersion) 必须为 true（self）
	if !errors.Is(ErrStaleVersion, ErrStaleVersion) {
		t.Fatal("errors.Is(ErrStaleVersion, ErrStaleVersion) must be true")
	}
	// 包成 fmt.Errorf("%w", ErrStaleVersion) 后仍可识别
	wrapped := fmtErrorf("%w", ErrStaleVersion)
	if !errors.Is(wrapped, ErrStaleVersion) {
		t.Fatal("errors.Is(wrapped, ErrStaleVersion) must be true")
	}
	// 与无关 error 必须区分
	if errors.Is(errors.New("other"), ErrStaleVersion) {
		t.Fatal("errors.Is(other, ErrStaleVersion) must be false")
	}
}

// fmtErrorf 是 fmt.Errorf 的本地别名（让测试代码读起来更短）。
func fmtErrorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// TestSummary_ZeroValueValid — Summary{} should be a usable zero value so
// generators can build one incrementally without pre-declaring every field.
func TestSummary_ZeroValueValid(t *testing.T) {
	var s Summary
	if s.SessionKey != "" || s.Title != "" || s.LastSummarized != (time.Time{}) {
		t.Fatalf("zero Summary should be all-empty, got %+v", s)
	}
}

// TestSanitiseUTF8_InvalidBytesReplaced verifies that invalid UTF-8 byte
// sequences (truncated CJK multibyte, as observed in production error
// 0xe5 0xe2 0x80) are replaced with U+FFFD so PostgreSQL accepts the row.
func TestSanitiseUTF8_InvalidBytesReplaced(t *testing.T) {
	// 0xe5 starts a 3-byte CJK sequence; 0xe2 0x80 is a truncated 3-byte
	// sequence. Both are invalid on their own.
	invalid := "\xe5\xe2\x80"
	sanitised := sanitiseUTF8(invalid)
	if strings.Contains(sanitised, "\xe5\xe2\x80") {
		t.Errorf("invalid bytes still present after sanitisation: %q", sanitised)
	}
	for _, r := range sanitised {
		if r == '\ufffd' {
			return // at least one replacement char, test passes
		}
	}
	t.Errorf("expected at least one U+FFFD replacement, got %q", sanitised)
}

// TestSanitiseUTF8_ValidUnchanged verifies that valid UTF-8 (including
// CJK and emoji) passes through untouched.
func TestSanitiseUTF8_ValidUnchanged(t *testing.T) {
	cases := []string{
		"hello world",
		"继续修复这个 bug",
		"🎉 emoji test",
		"", // empty stays empty
	}
	for _, s := range cases {
		if got := sanitiseUTF8(s); got != s {
			t.Errorf("valid UTF-8 changed: input %q → output %q", s, got)
		}
	}
}
