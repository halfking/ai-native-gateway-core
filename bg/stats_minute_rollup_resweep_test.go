package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStatsResweepWindowOnlyIncludesWholeMinutes(t *testing.T) {
	for _, second := range []int{0, 30, 59} {
		for _, nano := range []int{0, 123456789} {
			now := time.Date(2026, 11, 9, 10, 5, second, nano, time.UTC)
			since, until := statsResweepWindow(now, 2*time.Minute, 30*time.Minute)
			wantSince := time.Date(2026, 11, 9, 9, 33, 0, 0, time.UTC)
			wantUntil := time.Date(2026, 11, 9, 10, 2, 59, 999999000, time.UTC)
			if !since.Equal(wantSince) || !until.Equal(wantUntil) {
				t.Errorf("tick second=%d: window=[%s,%s], want [%s,%s]", second,
					since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano),
					wantSince.Format(time.RFC3339Nano), wantUntil.Format(time.RFC3339Nano))
			}
		}
	}
}

// A delayed terminal row must be recovered without replacing a newer, already
// complete minute with its first half. Temporary tables shadow the real tables
// only inside this rolled-back transaction; no production data is touched.
func TestStatsResweepRecoversLateRowWithoutShrinkingNextMinute(t *testing.T) {
	dsn := os.Getenv("LLM_GATEWAY_ROLLUP_TEST_DSN")
	if dsn == "" {
		t.Skip("LLM_GATEWAY_ROLLUP_TEST_DSN unset: real PostgreSQL resweep not verified")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
CREATE TEMP TABLE request_logs_with_current_month (
 ts timestamptz, tenant_id text DEFAULT 'default', provider_id bigint DEFAULT 1,
 canonical_id bigint DEFAULT 1, request_status text DEFAULT 'success',
 prompt_tokens bigint DEFAULT 0, completion_tokens bigint DEFAULT 0,
 cost_usd numeric DEFAULT 0, latency_ms bigint DEFAULT 0,
 quality_flags text[], task_type text, origin_actor text
) ON COMMIT DROP;
CREATE TEMP TABLE request_stats_minute (
 bucket timestamptz, tenant_id text, provider_id bigint, canonical_id bigint,
 requests bigint, success_count bigint, failure_count bigint,
 prompt_tokens bigint, completion_tokens bigint, total_tokens bigint,
 credits_charged bigint, cost_usd numeric, latency_ms_sum bigint,
 PRIMARY KEY(bucket, tenant_id, provider_id, canonical_id)
) ON COMMIT DROP;`)
	if err != nil {
		t.Fatal(err)
	}
	older := time.Date(2026, 11, 9, 10, 2, 0, 0, time.UTC)
	next := older.Add(time.Minute)
	now := older.Add(3*time.Minute + 30*time.Second)
	_, err = tx.Exec(ctx, `INSERT INTO request_logs_with_current_month(ts) VALUES ($1),($2),($3)`,
		older.Add(10*time.Second), next, next.Add(45*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, rollupMainStatement("0"), older, now); err != nil {
		t.Fatal(err)
	}
	// Terminal data arrives after the forward cursor has passed its minute.
	if _, err := tx.Exec(ctx, `INSERT INTO request_logs_with_current_month(ts) VALUES ($1)`, older.Add(59*time.Second+999999*time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	since, until := statsResweepWindow(now, 2*time.Minute, 30*time.Minute)
	for pass := 0; pass < 2; pass++ {
		if _, err := tx.Exec(ctx, rollupMainStatement("0"), since, until); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, retireClosedMainMinuteSQL, since, until); err != nil {
			t.Fatal(err)
		}
		for _, bucket := range []time.Time{older, next} {
			var got int64
			if err := tx.QueryRow(ctx, `SELECT requests FROM request_stats_minute WHERE bucket=$1`, bucket).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != 2 {
				t.Errorf("pass %d bucket %s: requests=%d, want 2 (recover late data, preserve next minute)", pass, bucket, got)
			}
		}
	}
}
