//go:build integration

package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestStateTransitionJSONBInsertAndReplayAreIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pgContainer, err := postgres.Run(ctx, "postgres:16-alpine",
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
		if err := pgContainer.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}()

	connString, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get postgres connection string: %v", err)
	}
	var pool *pgxpool.Pool
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.New(ctx, connString)
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
		CREATE TABLE request_state_transitions (
			request_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			transition_type TEXT NOT NULL,
			from_state TEXT,
			to_state TEXT,
			metadata JSONB,
			seq BIGINT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (request_id, seq)
		)`)
	if err != nil {
		t.Fatalf("create transition table: %v", err)
	}

	logger := newTestLogger(stateTransitionPool{pool: pool}, fixedNow)
	transition := pendingTransition{
		t: StateTransition{
			RequestID:      "req-jsonb-replay",
			TenantID:       "tenant-integration",
			TransitionType: "route",
			FromState:      "arrived",
			ToState:        "routed",
			Metadata: map[string]any{
				"attempt": 2,
				"reason":  "timeout",
			},
		},
		seq: 7,
	}

	if err := logger.insertTransition(ctx, transition); err != nil {
		t.Fatalf("initial transition insert: %v", err)
	}
	if err := logger.insertTransition(ctx, transition); err != nil {
		t.Fatalf("idempotent transition replay: %v", err)
	}

	var count int
	var reason string
	var attempt int
	err = pool.QueryRow(ctx, `
		SELECT count(*), max(metadata->>'reason'), max((metadata->>'attempt')::int)
		FROM request_state_transitions
		WHERE request_id = $1 AND seq = $2`, transition.t.RequestID, transition.seq).
		Scan(&count, &reason, &attempt)
	if err != nil {
		t.Fatalf("query transition: %v", err)
	}
	if count != 1 {
		t.Errorf("transition rows after replay = %d, want 1", count)
	}
	if reason != "timeout" || attempt != 2 {
		t.Errorf("stored metadata = {reason:%q, attempt:%d}, want {reason:%q, attempt:%d}",
			reason, attempt, "timeout", 2)
	}
}
