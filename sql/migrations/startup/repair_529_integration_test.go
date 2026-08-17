//go:build integration

package startup

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestMigration529RepairsPartitionAndStickyConflictKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	defer func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	var pool *pgxpool.Pool
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil {
			err = pool.Ping(ctx)
			if err == nil {
				break
			}
			pool.Close()
			pool = nil
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		CREATE TABLE public.sticky_sessions (
			sticky_key TEXT NOT NULL,
			credential_id BIGINT NOT NULL,
			set_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at TIMESTAMPTZ NOT NULL,
			canonical_id BIGINT,
			last_request_id TEXT
		);
		CREATE TABLE public.request_logs_bodies (
			request_id TEXT NOT NULL,
			ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			request_body JSONB,
			outbound_body JSONB,
			response_body JSONB
		) PARTITION BY RANGE (ts);
	`)
	if err != nil {
		t.Fatalf("create baseline tables: %v", err)
	}

	migration, err := os.ReadFile("529_repair_shared_pg_sticky_and_bodies_2026_07.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration 529: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("reapply migration 529: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO public.sticky_sessions (sticky_key, credential_id, expires_at)
		VALUES ('tenant:model:profile', 1, NOW() + INTERVAL '1 hour')
		ON CONFLICT (sticky_key) DO UPDATE SET credential_id = EXCLUDED.credential_id;
		INSERT INTO public.sticky_sessions (sticky_key, credential_id, expires_at)
		VALUES ('tenant:model:profile', 2, NOW() + INTERVAL '1 hour')
		ON CONFLICT (sticky_key) DO UPDATE SET credential_id = EXCLUDED.credential_id;
		INSERT INTO public.request_logs_bodies (request_id, ts, request_body)
		VALUES ('req-529', '2026-07-15 12:00:00+08', '{}'::jsonb);
	`)
	if err != nil {
		t.Fatalf("exercise repaired schema: %v", err)
	}

	var stickyRows, partitionRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.sticky_sessions`).Scan(&stickyRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_logs_bodies_2026_07`).Scan(&partitionRows); err != nil {
		t.Fatal(err)
	}
	if stickyRows != 1 || partitionRows != 1 {
		t.Fatalf("repaired schema rows = sticky:%d partition:%d, want 1/1", stickyRows, partitionRows)
	}

	down, err := os.ReadFile("529_repair_shared_pg_sticky_and_bodies_2026_07.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply non-destructive down migration: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_logs_bodies_2026_07`).Scan(&partitionRows); err != nil {
		t.Fatalf("historical partition missing after down: %v", err)
	}
	if partitionRows != 1 {
		t.Fatalf("historical partition rows after down = %d, want 1", partitionRows)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.sticky_sessions (sticky_key, credential_id, expires_at)
		VALUES ('tenant:model:profile', 3, NOW() + INTERVAL '1 hour')
		ON CONFLICT (sticky_key) DO UPDATE SET credential_id = EXCLUDED.credential_id`); err != nil {
		t.Fatalf("sticky conflict arbiter missing after down: %v", err)
	}
}
