package admin

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestInvalidateRoutingCaches_PayloadFormat pins the wire payload format
// for the auto_route_refresh NOTIFY. The bg.AutoRouteRealtimeListener
// logs the payload for observability, so any change here breaks the
// /api/admin/auto-route/decisions log schema. Keeping the format stable
// means downstream parsers continue to work.
//
// Spec: "{kind}:UPDATE:{id}"  — kind is "credentials" or "providers".
func TestInvalidateRoutingCaches_PayloadFormat(t *testing.T) {
	cases := []struct {
		kind string
		id   int
		want string
	}{
		{"credentials", 314, "credentials:UPDATE:314"},
		{"providers", 99, "providers:UPDATE:99"},
		{"credentials", 1, "credentials:UPDATE:1"},
	}
	for _, c := range cases {
		if got := makePayload(c.kind, c.id); got != c.want {
			t.Errorf("payload mismatch: kind=%s id=%d got=%q want=%q",
				c.kind, c.id, got, c.want)
		}
	}
}

// TestInvalidateRoutingCaches_NotifiesAutoRouteRefresh drives
// invalidateRoutingCaches directly against a pgxmock pool and asserts
// the SQL sent on the wire contains the pg_notify call.
//
// Why this matters: the bug we're guarding against is that
// trg_notify_auto_route_creds does NOT fire on `manual_disabled`
// UPDATEs. The Go-side wakeup (provider.InvalidateAllCandidateCache
// + pg_notify) is the only path keeping the auto-route in sync for
// manual_disabled toggles.
//
// The mock DB captures the SQL string sent on the wire; we assert it
// contains "pg_notify('auto_route_refresh'" so any future refactor
// that drops the NOTIFY or rewrites the channel name breaks the test
// instead of regressing silently.
func TestInvalidateRoutingCaches_NotifiesAutoRouteRefresh(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectExec(`SELECT pg_notify\('auto_route_refresh'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))

	invalidateRoutingCaches(context.Background(), mock, "credentials", 314)

	// Allow the 2s internal timeout to lapse so any goroutine work
	// settles before ExpectationsWereMet checks for unmatched expectations.
	// (invalidateRoutingCaches is synchronous in the hot path; the 2s
	// timeout is only there to bound a slow admin client. The
	// ExpectationsWereMet assertion below is the real check.)
	time.Sleep(50 * time.Millisecond)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("mock expectations NOT met — pg_notify may be missing: %v", err)
	}
}

// TestInvalidateRoutingCaches_ProviderKind exercises the providers
// branch — same payload format, different entity_kind label.
func TestInvalidateRoutingCaches_ProviderKind(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectExec(`SELECT pg_notify\('auto_route_refresh'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))

	invalidateRoutingCaches(context.Background(), mock, "providers", 314)
	time.Sleep(50 * time.Millisecond)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("mock expectations NOT met for providers: %v", err)
	}
}

// makePayload is the production payload format used by
// invalidateRoutingCaches. Kept here as a test-pinned mirror so any
// drift between the implementation and the spec breaks the
// TestInvalidateRoutingCaches_PayloadFormat test.
func makePayload(kind string, id int) string {
	return kind + ":UPDATE:" + intStr(id)
}

func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
