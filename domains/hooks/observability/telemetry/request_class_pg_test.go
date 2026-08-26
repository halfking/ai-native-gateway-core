package telemetry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// V6-W1.6 R8 / migration 608 local integration test (真实 PG 写入 + 回查).
//
// Gated on LLM_GATEWAY_TEST_PG_DSN so CI stays offline-green; run locally:
//
//	LLM_GATEWAY_TEST_PG_DSN='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./domains/hooks/observability/telemetry/ -run TestRequestClassPGRoundTrip -count=1 -v
//
// Precondition: migration 608 applied (the test applies it from the repo SQL
// file when the DSN is set, so a fresh local DB also works).

func TestRequestClassPGRoundTrip(t *testing.T) {
	dsn := os.Getenv("LLM_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("LLM_GATEWAY_TEST_PG_DSN not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Apply migration 608 (idempotent) straight from the repo SQL file.
	sqlBytes, err := os.ReadFile("../../../../sql/migrations/startup/608_request_class_due_at.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("apply 608 (pass 1): %v", err)
	}
	if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("apply 608 (pass 2, idempotency): %v", err)
	}

	// Column presence on hot + parent + view exposure.
	for _, table := range []string{"request_logs_hot", "request_logs", "request_logs_with_current_month"} {
		var n int
		err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			 WHERE table_name = $1 AND column_name IN ('request_class','due_at')`, table).Scan(&n)
		if err != nil {
			t.Fatalf("columns on %s: %v", table, err)
		}
		if n != 2 {
			t.Fatalf("%s exposes %d/2 of the 608 columns", table, n)
		}
	}

	// Real write path: EmitRequestLogUpdate drives the 100-column upsert.
	c := NewClient()
	defer c.Stop()
	c.SetDB(pool)
	if !c.Enabled() {
		t.Fatalf("client not enabled")
	}

	reqID := "608-test-" + time.Now().Format("150405.000000000")
	cls := "scheduled"
	due := time.Unix(1800000123, 0).UTC()
	scheduled := "scheduled"
	c.EmitRequestLogUpdate(&RequestLogEntry{
		RequestID:    reqID,
		TenantID:     "t608",
		ClientModel:  &scheduled,
		RequestClass: &cls,
		DueAt:        &due,
	})
	deadline := time.Now().Add(10 * time.Second)
	var gotClass string
	var gotDue *time.Time
	// The client writes asynchronously (worker goroutine); poll briefly.
	for time.Now().Before(deadline) {
		err = pool.QueryRow(ctx,
			`SELECT request_class, due_at FROM request_logs_hot WHERE request_id = $1`, reqID,
		).Scan(&gotClass, &gotDue)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("select back: %v", err)
	}
	if gotClass != "scheduled" || gotDue == nil || !gotDue.UTC().Equal(due) {
		t.Fatalf("round-trip = %q/%v, want scheduled/%v", gotClass, gotDue, due)
	}

	// Completion UPDATE path keeps the class (COALESCE guard) even when the
	// follow-up entry omits it.
	c.EmitRequestLogUpdate(&RequestLogEntry{RequestID: reqID, TenantID: "t608"})
	err = pool.QueryRow(ctx,
		`SELECT request_class, due_at FROM request_logs_hot WHERE request_id = $1`, reqID,
	).Scan(&gotClass, &gotDue)
	if err != nil {
		t.Fatalf("select after update: %v", err)
	}
	if gotClass != "scheduled" || gotDue == nil {
		t.Fatalf("class regressed after completion update: %q/%v", gotClass, gotDue)
	}

	// NULL-class insert falls back to the column default 'immediate'.
	if _, err := pool.Exec(ctx, `
		INSERT INTO request_logs_hot (request_id, ts, tenant_id, success)
		VALUES ('608-default', now(), 't608', TRUE)
		ON CONFLICT (request_id) DO NOTHING`); err != nil {
		t.Fatalf("default insert: %v", err)
	}
	var defClass string
	if err := pool.QueryRow(ctx,
		`SELECT request_class FROM request_logs_hot WHERE request_id = '608-default'`).Scan(&defClass); err != nil {
		t.Fatalf("default select: %v", err)
	}
	if defClass != "immediate" {
		t.Fatalf("default class = %q, want immediate", defClass)
	}

	// Cleanup test rows.
	_, _ = pool.Exec(ctx, `DELETE FROM request_logs_hot WHERE request_id LIKE '608-%'`)
}
