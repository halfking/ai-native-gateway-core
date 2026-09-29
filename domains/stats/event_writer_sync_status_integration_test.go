//go:build integration

package stats

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestEventWriterSyncProjectionFlipsStatusIntegration pins R28-HC-10: the
// sync-projection path (LLM_GATEWAY_STATS_INBOX_CONSUMER off) must leave the
// inbox row 'processed', not 'pending'-with-processed_at — the latter shape
// accumulated 1.19M fake-pending rows on the local shard and would replay as
// a storm the moment the async consumer flag is enabled.
func TestEventWriterSyncProjectionFlipsStatusIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("stats_test"),
		postgres.WithUsername("stats_test"),
		postgres.WithPassword("stats_test"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = container.Terminate(cleanupCtx)
	})
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}

	var conn *pgx.Conn
	for i := 0; i < 30; i++ {
		conn, err = pgx.Connect(ctx, dsn)
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	for _, name := range []string{
		"../../sql/migrations/startup/536_stats_analytics_foundation.sql",
		"../../sql/migrations/startup/537_usage_facts.sql",
		"../../sql/migrations/startup/540_stats_event_inbox_consumer.sql",
	} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// asyncProjection stays false (flag off): persist runs sync projection.
	writer := NewEventWriter(pool, 16)
	evt := Event{
		EventID:    "r28-sync-status",
		OccurredAt: time.Now().UTC().Truncate(time.Microsecond),
		RequestID:  "r28-req",
		EventType:  "request_succeeded",
		Traffic:    "chat",
		TenantID:   "tenant-r28",
		Status:     "success",
		Source:     "test",
	}
	if err := writer.persist(ctx, []Event{evt}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	var status string
	var processedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT processing_status, processed_at FROM stats_event_inbox
		 WHERE event_id = $1 AND occurred_at = $2`, evt.EventID, evt.OccurredAt).
		Scan(&status, &processedAt); err != nil {
		t.Fatalf("read back inbox row: %v", err)
	}
	if status != "processed" || processedAt == nil {
		t.Fatalf("sync projection must flip status: got status=%q processed_at=%v", status, processedAt)
	}
}
