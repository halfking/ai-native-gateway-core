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

func TestInboxConsumerLeaseFencingIntegration(t *testing.T) {
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
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	var conn *pgx.Conn
	for attempt := 0; attempt < 30; attempt++ {
		conn, err = pgx.ConnectConfig(ctx, cfg)
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
	occurred := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := conn.Exec(ctx, `
		INSERT INTO stats_event_inbox (event_id, occurred_at, request_id, event_type, status)
		VALUES ('fenced-event', $1, 'fenced-request', 'request_succeeded', 'success')`, occurred); err != nil {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	first := NewInboxConsumer(pool, InboxConfig{Owner: "worker-a", Lease: time.Millisecond})
	second := NewInboxConsumer(pool, InboxConfig{Owner: "worker-b", Lease: time.Minute})
	firstClaim, err := first.claim(ctx, 1)
	if err != nil || len(firstClaim) != 1 {
		t.Fatalf("first claim = %d, %v", len(firstClaim), err)
	}
	time.Sleep(5 * time.Millisecond)
	secondClaim, err := second.claim(ctx, 1)
	if err != nil || len(secondClaim) != 1 {
		t.Fatalf("second claim after lease expiry = %d, %v", len(secondClaim), err)
	}
	if firstClaim[0].FencingToken >= secondClaim[0].FencingToken {
		t.Fatalf("fencing token did not advance: first=%d second=%d", firstClaim[0].FencingToken, secondClaim[0].FencingToken)
	}
	if err := first.projectAndMarkProcessed(ctx, firstClaim[0]); err == nil {
		t.Fatal("expired worker must not project or mark a reclaimed event")
	}
	var facts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM usage_facts WHERE event_id = 'fenced-event'`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 0 {
		t.Fatalf("expired worker wrote %d usage facts", facts)
	}
	if err := second.projectAndMarkProcessed(ctx, secondClaim[0]); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM usage_facts WHERE event_id = 'fenced-event'`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("current worker facts = %d, want 1", facts)
	}

	writer := NewEventWriter(pool, 1)
	event := Event{
		EventID: "sync-projection", OccurredAt: time.Now().UTC().Truncate(time.Microsecond),
		RequestID: "sync-request", EventType: EventRequestSucceeded, TenantID: "default", Status: "success",
	}
	if err := writer.persist(ctx, []Event{event}); err != nil {
		t.Fatal(err)
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT processing_status FROM stats_event_inbox WHERE event_id = $1`, event.EventID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "processed" {
		t.Fatalf("processing status = %q, want processed", status)
	}
}
