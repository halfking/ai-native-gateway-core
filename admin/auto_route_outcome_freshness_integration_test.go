//go:build integration

package admin

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestOutcomeFreshnessAgainstLiveDatabase closes the gap the unit tests cannot:
// TestQueryOutcomeFreshness proves the RULE is right given a MAX(ts) value, but
// nothing proves the SQL actually runs on this schema and that the value it
// returns really is fresh. A rule can be perfectly implemented against a column
// that does not exist — §9.31 is exactly that shape (a gate pointed at a
// constraint name that was never a view).
//
// Deliberately NOT asserting `stale == false`. The local database is written by
// an external instance of unconfirmed identity, so "the newest v1 row is
// recent" is an observation about this machine, not a promise about the
// product. What this asserts is the invariant that must hold either way: a
// successful read yields a parseable as_of, and whatever age it reports, the
// stale flag agrees with the stated threshold. A machine mid-stop-write is a
// legitimate state here, not a failure.
//
// Skips loudly when TEST_PG_URL is unset — and a skip says so in the message,
// because "skipped" and "passed" are different claims.
func TestOutcomeFreshnessAgainstLiveDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL 未设置 —— 本门不构成证据（与 §9.34 的空库跳过同源）")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	now := time.Now()
	got := queryOutcomeFreshness(ctx, pool, now)

	if !got.Available {
		t.Fatalf("available=false, reason=%q —— 本机 v1 hot 关系应当可读；"+
			"若你正在本机演练退役并已 DROP 该表，请连库后单独核对，不要用这条门代替",
			got.Reason)
	}
	if got.Reason == outcomeReasonNoRows {
		t.Fatalf("reason=no_rows —— 关系可读但一行都没有，这说明本机 v1 hot 已被清空；" +
			"此时本门无法判断新鲜度（age 无从谈起），不构成证据")
	}
	if got.AsOf == nil {
		t.Fatalf("as_of 为 null，但 reason=%q 表示读到了行 —— 两者必须同时成立或同时不成立", got.Reason)
	}
	if got.AgeSeconds == nil {
		t.Fatalf("age_seconds 为 null，但 as_of=%s 非空", *got.AsOf)
	}

	// The one invariant that must hold no matter what the clock says.
	wantStale := *got.AgeSeconds > got.StaleAfterSeconds
	if got.Stale != wantStale {
		t.Errorf("stale=%v，但 age_seconds=%d 与 stale_after_seconds=%d 蕴含 stale=%v —— "+
			"标记与算式不一致，消费方会拿到自相矛盾的结论",
			got.Stale, *got.AgeSeconds, got.StaleAfterSeconds, wantStale)
	}

	// And the reason must agree with the flag, not merely be present.
	switch got.Reason {
	case outcomeReasonLive:
		if got.Stale {
			t.Errorf("reason=live 但 stale=true")
		}
	case outcomeReasonStale:
		if !got.Stale {
			t.Errorf("reason=stale 但 stale=false")
		}
	default:
		t.Errorf("有新鲜行时 reason 应为 live 或 stale，实得 %q", got.Reason)
	}

	t.Logf("outcome_source: as_of=%s age=%ds stale=%v reason=%s",
		*got.AsOf, *got.AgeSeconds, got.Stale, got.Reason)
}
