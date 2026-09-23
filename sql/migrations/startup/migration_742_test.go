package startup

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Migration 742 (hosted-task-delegation-design §3.3/§4.3, R65) extends the
// hosted_task_events type whitelist with 'recalled' for the recall lightweight
// handoff path. This file pins the SQL contract statically; the DB-backed
// fresh up/down/up matrix runs only when TEST_PG_URL points at a disposable
// database — without it the integration portion skips and must not be
// reported as PASS (acceptance matrix D).
func TestMigration742RecalledEventContract(t *testing.T) {
	up, err := os.ReadFile("742_hosted_task_recalled_event.sql")
	if err != nil {
		t.Fatalf("read migration failed: %v", err)
	}
	down, err := os.ReadFile("742_hosted_task_recalled_event.down.sql")
	if err != nil {
		t.Fatalf("read down migration failed: %v", err)
	}
	for name, sql := range map[string]string{"up": string(up), "down": string(down)} {
		if !strings.Contains(sql, "BEGIN;") || !strings.Contains(sql, "COMMIT;") {
			t.Errorf("742 %s migration must be transactional", name)
		}
	}
	upText := string(up)

	// The migration must swap hosted_task_events_type_check and admit
	// 'recalled' (three-place sync: this CHECK + types.go EventRecalled +
	// design doc §4.3).
	if !strings.Contains(upText, "DROP CONSTRAINT hosted_task_events_type_check") {
		t.Error("742 must replace hosted_task_events_type_check")
	}
	if !strings.Contains(upText, "ADD CONSTRAINT hosted_task_events_type_check") {
		t.Error("742 must recreate hosted_task_events_type_check")
	}
	if !strings.Contains(upText, "'recalled'") {
		t.Error("742 CHECK must admit 'recalled'")
	}
	for _, ev := range []string{
		"accepted", "dispatch_degraded", "running", "progress", "cancel_requested",
		"completed", "failed", "expired", "cancelled", "callback_delivered", "callback_dlq",
	} {
		if !strings.Contains(upText, "'"+ev+"'") {
			t.Errorf("742 CHECK dropped 711 event %q", ev)
		}
	}

	// Down must clear recalled rows before restoring the 711 whitelist.
	downText := string(down)
	if !strings.Contains(downText, "DELETE FROM hosted_task_events WHERE event_type = 'recalled'") {
		t.Error("742 down must purge recalled rows before restoring the constraint")
	}
	if !strings.Contains(downText, "ADD CONSTRAINT hosted_task_events_type_check") {
		t.Error("742 down must restore the original CHECK whitelist")
	}
}

// TestMigration742FreshUpDownUp applies 711 → 742 against a disposable DB,
// proves the recalled event type is accepted and an unknown type still
// rejected, then cycles 742 down → up (constraint restored and re-extended).
// Convention (migration_532/711): TEST_PG_URL must point at a disposable DB;
// unset env → skip (never a fake PASS).
func TestMigration742FreshUpDownUp(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; migration 742 integration test requires a live PostgreSQL (convention: tests/integration gating)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_PG_URL: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol // multi-statement files
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect TEST_PG_URL: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(raw)
	}
	up711 := read("711_hosted_tasks.sql")
	down711 := read("711_hosted_tasks.down.sql")
	up742 := read("742_hosted_task_recalled_event.sql")
	down742 := read("742_hosted_task_recalled_event.down.sql")

	apply := func(label, sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("apply %s failed: %v", label, err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), down742)
		_, _ = conn.Exec(context.Background(), down711)
	})

	apply("711 up", up711)
	apply("742 up#1", up742)

	// Fixture task row (events FK → hosted_tasks).
	apply("fixture", `
		INSERT INTO hosted_tasks (id, tenant_id, goal, deadline_at, idempotency_key)
		VALUES ('ht_742_probe', 'tenant-A', 'goal-742', NOW() + INTERVAL '1 hour', 'idem-742')`)

	// recalled is admitted post-742.
	if _, err := conn.Exec(ctx, `
		INSERT INTO hosted_task_events (task_id, seq, event_type, payload)
		VALUES ('ht_742_probe', 1, 'recalled', '{"recall_status":"already_terminal"}'::jsonb)`); err != nil {
		t.Fatalf("recalled event must be admitted after 742: %v", err)
	}
	// Unknown types are still rejected (constraint not accidentally dropped).
	if _, err := conn.Exec(ctx, `
		INSERT INTO hosted_task_events (task_id, seq, event_type, payload)
		VALUES ('ht_742_probe', 2, 'bogus_type', '{}'::jsonb)`); err == nil {
		t.Fatal("bogus event_type must still be rejected by hosted_task_events_type_check")
	}

	// down → back to the 711 whitelist (recalled rows purged, type rejected).
	apply("742 down", down742)
	var n int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM hosted_task_events WHERE event_type = 'recalled'`).Scan(&n); err != nil {
		t.Fatalf("count recalled after down: %v", err)
	}
	if n != 0 {
		t.Fatalf("742 down must purge recalled rows, got %d", n)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO hosted_task_events (task_id, seq, event_type, payload)
		VALUES ('ht_742_probe', 3, 'recalled', '{}'::jsonb)`); err == nil {
		t.Fatal("recalled must be rejected again after 742 down")
	}

	// up#2 (fresh up/down/up): constraint re-extended, recalled admitted.
	apply("742 up#2 (fresh up/down/up)", up742)
	if _, err := conn.Exec(ctx, `
		INSERT INTO hosted_task_events (task_id, seq, event_type, payload)
		VALUES ('ht_742_probe', 4, 'recalled', '{"recall_status":"cancelled"}'::jsonb)`); err != nil {
		t.Fatalf("recalled must be admitted after fresh up/down/up: %v", err)
	}
}
