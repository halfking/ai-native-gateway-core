//go:build !integration

package admin

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// Audit §9.160 / admin session detail: the request list is assembled by
// scanning the canonical view's rows. Two defects are pinned here.
//
//  1. `success` was scanned into a bare bool. The column is nullable and the
//     view preserves that, so a NULL made rows.Scan fail, the row was dropped
//     by warnRowSkip, and the response simply came back 200 with one request
//     missing. Nothing surfaces it.
//  2. `request_status` was never selected even though the view has carried the
//     column all along. It is the only field that separates "the upstream
//     failed" from "we refused the request for rate limiting" — and
//     rate-limited turns are 25.9% of the session family (437,402 of 1,688,629,
//     measured). `success=false` alone cannot tell them apart.

// fakeRow reproduces the one behaviour that matters here: pgx refuses to
// assign SQL NULL into a destination whose type cannot hold it. Everything else
// is a no-op. Using the real sql.Null* types (rather than a hand-rolled
// "isValid" flag) is deliberate — it keeps the test honest about what the
// production destinations are.
type fakeRow struct {
	values []any
	err    error
}

func (f fakeRow) Scan(dest ...any) error {
	if f.err != nil {
		return f.err
	}
	if len(dest) != len(f.values) {
		return errors.New("fakeRow: destination count mismatch")
	}
	for i, d := range dest {
		if err := assignNull(d, f.values[i]); err != nil {
			return err
		}
	}
	return nil
}

// assignNull mirrors database/sql's convertAssign behaviour closely enough for
// this test: a nil source into a non-nullable destination is an error.
func assignNull(dest, src any) error {
	if src == nil {
		switch d := dest.(type) {
		case *sql.NullString:
			d.Valid = false
			return nil
		case *sql.NullInt32:
			d.Valid = false
			return nil
		case *sql.NullBool:
			d.Valid = false
			return nil
		case *string:
			return errors.New("sql: Scan error on column: converting NULL to string is unsupported")
		case *bool:
			return errors.New("sql: Scan error on column: converting NULL to bool is unsupported")
		case *int:
			return errors.New("sql: Scan error on column: converting NULL to int is unsupported")
		case *float64:
			return errors.New("sql: Scan error on column: converting NULL to float64 is unsupported")
		case *time.Time:
			return errors.New("sql: Scan error on column: converting NULL to time.Time is unsupported")
		default:
			return errors.New("fakeRow: unsupported destination type")
		}
	}
	switch d := dest.(type) {
	case *sql.NullString:
		s, ok := src.(string)
		if !ok {
			return errors.New("fakeRow: expected string")
		}
		d.String, d.Valid = s, true
	case *sql.NullInt32:
		i, ok := src.(int32)
		if !ok {
			return errors.New("fakeRow: expected int32")
		}
		d.Int32, d.Valid = i, true
	case *sql.NullBool:
		b, ok := src.(bool)
		if !ok {
			return errors.New("fakeRow: expected bool")
		}
		d.Bool, d.Valid = b, true
	case *string:
		s, _ := src.(string)
		*d = s
	case *bool:
		b, _ := src.(bool)
		*d = b
	case *int:
		i, _ := src.(int)
		*d = i
	case *float64:
		f, _ := src.(float64)
		*d = f
	case *time.Time:
		ts, _ := src.(time.Time)
		*d = ts
	default:
		return errors.New("fakeRow: unsupported destination type")
	}
	return nil
}

func rowFixture(requestID string, success any, requestStatus any) fakeRow {
	ts := time.Date(2026, 10, 4, 7, 0, 0, 0, time.UTC)
	return fakeRow{values: []any{requestID, ts, "gpt-4o", "hello", success, 128, 0.0021, int32(430), requestStatus}}
}

// TestScanSessionRequestBrief_KeepsRowsWithNullSuccess is the regression test
// for defect 1. A row whose success is NULL must survive the scan and come back
// as a brief with Success=false — not as a dropped row.
func TestScanSessionRequestBrief_KeepsRowsWithNullSuccess(t *testing.T) {
	brief, err := scanSessionRequestBrief(rowFixture("req-null-success", nil, nil))
	if err != nil {
		t.Fatalf("a row with success=NULL must not fail the scan: %v\n"+
			"returning an error here makes the caller skip the row, so the request silently "+
			"disappears from a 200 response", err)
	}
	if brief.RequestID != "req-null-success" {
		t.Errorf("RequestID = %q, want req-null-success", brief.RequestID)
	}
	if brief.Success {
		t.Error("Success = true for a NULL success column; want false")
	}
	if brief.RequestStatus != nil {
		t.Errorf("RequestStatus = %v, want nil — the view emits a NULL label when success is NULL", *brief.RequestStatus)
	}
}

// TestScanSessionRequestBrief_SurfacesRequestStatus is the regression test for
// defect 2, and the reason this field exists: success=false covers both genuine
// upstream failures and rate-limit rejections, and only request_status separates
// them.
func TestScanSessionRequestBrief_SurfacesRequestStatus(t *testing.T) {
	cases := []struct {
		name   string
		status any
		want   string
	}{
		{"rate limited is not a failure", "rate_limited", "rate_limited"},
		{"genuine failure", "failure", "failure"},
		{"success", "success", "success"},
		{"in flight", "in_progress", "in_progress"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			brief, err := scanSessionRequestBrief(rowFixture("req-1", false, c.status))
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if brief.RequestStatus == nil {
				t.Fatalf("RequestStatus is nil; the four-state label is the whole point of the field " +
					"(a rate-limited rejection and an upstream failure are both success=false)")
			}
			if *brief.RequestStatus != c.want {
				t.Errorf("RequestStatus = %q, want %q", *brief.RequestStatus, c.want)
			}
		})
	}
}

// TestScanSessionRequestBrief_NullSuccessStillCarriesStatus guards the
// interaction of the two fields: a NULL success must not cost the row its
// request_status. The view computes the label independently of whether the
// caller bothered to read `success`.
func TestScanSessionRequestBrief_NullSuccessStillCarriesStatus(t *testing.T) {
	brief, err := scanSessionRequestBrief(rowFixture("req-x", nil, "rate_limited"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if brief.RequestStatus == nil || *brief.RequestStatus != "rate_limited" {
		t.Errorf("RequestStatus = %v, want rate_limited even when success is NULL", brief.RequestStatus)
	}
}

// TestScanSessionRequestBrief_ScansNineColumns pins the arity and order of the
// Scan call. Column-count drift is silent in production otherwise: pgx errors on
// every row, warnRowSkip eats them all, and the panel comes back empty — the
// exact "silent blank requests list" symptom the view migration was meant to fix.
func TestScanSessionRequestBrief_ScansNineColumns(t *testing.T) {
	// request_id / ts / total_tokens / cost_usd are non-nullable destinations,
	// so the "clean row" must actually carry values for them.
	nine := fakeRow{values: []any{
		"req-1", time.Date(2026, 10, 4, 7, 0, 0, 0, time.UTC), nil, nil, false, 128, 0.0021, nil, nil,
	}}
	if _, err := scanSessionRequestBrief(nine); err != nil {
		t.Fatalf("a 9-column row must scan cleanly: %v", err)
	}
	short := fakeRow{values: []any{
		"req-1", time.Date(2026, 10, 4, 7, 0, 0, 0, time.UTC), nil, nil, false, 128, 0.0021, nil,
	}}
	if _, err := scanSessionRequestBrief(short); err == nil {
		t.Error("an 8-column row must fail the scan; silently accepting a short row would hide " +
			"a SELECT/Scan arity mismatch behind an empty request list")
	}
}

// TestSessionDetailQuerySelectsRequestStatus is a text-level guard on the SQL
// itself. The scan test above proves the code can carry the field; this proves
// the query actually asks for it, which is the half that regressed.
func TestSessionDetailQuerySelectsRequestStatus(t *testing.T) {
	q := strings.ToLower(sessionDetailRequestsBaseSQL)
	if !strings.Contains(q, "rl.request_status") {
		t.Error("the session-detail request query does not select request_status; the brief's " +
			"RequestStatus field would be permanently nil")
	}
	// Column order must match scanSessionRequestBrief.
	want := []string{
		"rl.request_id", "rl.ts", "rl.client_model", "rl.request_preview", "rl.success",
		"rl.total_tokens", "rl.cost_usd", "rl.latency_ms", "rl.request_status",
	}
	at := -1
	for _, col := range want {
		i := strings.Index(q, col)
		if i < 0 {
			t.Fatalf("query no longer selects %s", col)
		}
		if i <= at {
			t.Errorf("column %s is out of the order scanSessionRequestBrief expects (offset %d)", col, i)
		}
		at = i
	}
}
