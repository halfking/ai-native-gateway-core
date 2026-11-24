package maas

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/bucket/credit"
)

func TestBackfillCreditConsumptionBuckets_PostedLedger_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_MAAS_CHARGE_IDEMPOTENCY_DATABASE_URL")
	if dsn == "" {
		t.Skip("an isolated MaaS fixture is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the telemetry fixture session-local, even in a test database
	// that already has the gateway's request log view.
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["timezone"] = "Asia/Kolkata"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var database string
	var occupied bool
	var hour time.Time
	if err := pool.QueryRow(ctx, `SELECT current_database(),
		EXISTS (SELECT 1 FROM public.credit_ledger_with_current_month)
		OR EXISTS (SELECT 1 FROM public.maas_credit_consumption_buckets),
		date_trunc('hour', now(), 'UTC') - interval '12 hours'`).Scan(&database, &occupied, &hour); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(database, "_test") || occupied {
		t.Fatal("a dedicated database with an empty ledger and empty buckets is required")
	}
	tenant := fmt.Sprintf("t_backfill_%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenants (code,name) VALUES ($1,$1)`, tenant); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := execBackfillFixtureCleanup(cleanup, pool, tenant); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
	}()
	if _, err := pool.Exec(ctx, `CREATE TEMP TABLE request_logs_with_current_month (
		tenant_id text, ts timestamptz, credits_charged bigint, prompt_tokens bigint, completion_tokens bigint,
		cache_read_tokens bigint, cache_write_tokens bigint, credits_rate_multiplier double precision)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pg_temp.request_logs_with_current_month VALUES
		($1,$2::timestamptz-interval '24 hours',13,1000,0,0,0,1),
		($1,$2+interval '2 hours',NULL,10000,0,0,0,1)`, tenant, hour); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.credit_ledger_hot
		(tenant_id,entry_type,amount,balance_after,ref_type,ref_id,created_at) VALUES
		($1,'consume',-5,0,'request','late-debit',$2::timestamptz+interval '1 minute'),
		($1,'consume',-8,0,'request','late-debit',$2+interval '1 minute'),
		($1,'refund',2,0,'request','late-debit',$2+interval '2 minutes'),
		($1,'consume',-2,0,'request',NULL,$2+interval '1 hour'),
		($1,'consume',-3,0,'request','existing',$2+interval '2 hours'),
		($1,'consume',-4,0,'request','unaligned',$2+interval '3 hours 45 minutes')`, tenant, hour); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.credit_ledger
		(id,tenant_id,entry_type,amount,balance_after,ref_type,ref_id,created_at)
		SELECT id,tenant_id,entry_type,amount,balance_after,ref_type,ref_id,created_at
		FROM public.credit_ledger_hot WHERE tenant_id=$1 AND ref_id='late-debit' AND amount=-5`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.maas_credit_consumption_buckets
		(tenant_id,bucket_start,credits,request_count) VALUES
		($1,date_trunc('hour',now(),'UTC')-interval '96 hours',1,1),
		($1,$2::timestamptz+interval '2 hours',99,7),
		($1,$2+interval '3 hours 30 minutes',11,1),
		($1,date_trunc('hour',now(),'UTC'),1,1)`, tenant, hour); err != nil {
		t.Fatal(err)
	}

	rows, err := NewService(pool).BackfillCreditConsumptionBuckets(ctx, 3)
	if err != nil || rows != 1 {
		t.Fatalf("backfill rows=%d err=%v, want one retained missing hour", rows, err)
	}
	if rows, err := credit.NewServiceAdapter(pool, nil).BackfillCreditConsumptionBuckets(ctx, 3); err != nil || rows != 0 {
		t.Fatalf("provider replay rows=%d err=%v, want zero", rows, err)
	}
	var credits, requests int64
	if err := pool.QueryRow(ctx, `SELECT credits,request_count FROM public.maas_credit_consumption_buckets
		WHERE tenant_id=$1 AND bucket_start=$2`, tenant, hour).Scan(&credits, &requests); err != nil || credits != 13 || requests != 1 {
		t.Fatalf("late debit credits=%d requests=%d err=%v", credits, requests, err)
	}
	if err := pool.QueryRow(ctx, `SELECT credits,request_count FROM public.maas_credit_consumption_buckets
		WHERE tenant_id=$1 AND bucket_start=$2::timestamptz+interval '2 hours'`, tenant, hour).Scan(&credits, &requests); err != nil || credits != 99 || requests != 7 {
		t.Fatalf("retained bucket credits=%d requests=%d err=%v", credits, requests, err)
	}
	var fabricated int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.maas_credit_consumption_buckets
		WHERE tenant_id <> $1 OR bucket_start IN (
			$2::timestamptz-interval '24 hours', $2+interval '1 hour', $2+interval '3 hours')`, tenant, hour).Scan(&fabricated); err != nil || fabricated != 0 {
		t.Fatalf("buckets without complete debit evidence=%d err=%v", fabricated, err)
	}
}

func execBackfillFixtureCleanup(ctx context.Context, pool *pgxpool.Pool, tenant string) error {
	for _, sql := range []string{
		`DELETE FROM public.maas_credit_consumption_buckets WHERE tenant_id=$1`,
		`DELETE FROM public.credit_ledger_hot WHERE tenant_id=$1`,
		`DELETE FROM public.credit_ledger WHERE tenant_id=$1`,
		`DELETE FROM public.tenants WHERE code=$1`,
	} {
		if _, err := pool.Exec(ctx, sql, tenant); err != nil {
			return err
		}
	}
	return nil
}
