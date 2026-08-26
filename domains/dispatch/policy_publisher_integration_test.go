//go:build integration

package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const policyPublisherSchema = `
CREATE TABLE public.credentials (
	id BIGINT PRIMARY KEY,
	provider_id BIGINT NOT NULL,
	concurrency_mode TEXT,
	concurrency_limit INT,
	rpm_limit INT,
	tpm_limit INT,
	revision BIGINT NOT NULL
);`

func newPolicyPublisherTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("policy_publisher"),
		postgres.WithUsername("policy_publisher"),
		postgres.WithPassword("policy_publisher"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() {
		terminateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	var pool *pgxpool.Pool
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil {
			pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
			pingErr := pool.Ping(pingCtx)
			pingCancel()
			if pingErr == nil {
				break
			}
			err = pingErr
			pool.Close()
			pool = nil
		}
		if ctx.Err() != nil {
			t.Fatalf("open postgres pool: %v", ctx.Err())
		}
		select {
		case <-ctx.Done():
			t.Fatalf("open postgres pool: %v", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	if pool == nil {
		t.Fatalf("open postgres pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, policyPublisherSchema); err != nil {
		t.Fatalf("create credentials table: %v", err)
	}
	return pool
}

func TestPolicyPublisherPublishCatchUpIntegrationAppliesMaterializedPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := newPolicyPublisherTestPool(t, ctx)

	const credentialID = 101
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.credentials
			(id, provider_id, concurrency_mode, concurrency_limit, rpm_limit, tpm_limit, revision)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		credentialID, 3, ModeConcurrency, 12, 0, 0, 9); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	pipeline := NewPipeline(Deps{})
	t.Cleanup(pipeline.Stop)
	forwarder := pipeline.getOrCreateForwarder(cred(credentialID, ModeConcurrency, 2))
	original, ok := forwarder.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("initial governor type = %T, want *concurrencyGovernor", forwarder.govLocked())
	}

	publisher := NewPolicyPublisher(pool, pipeline)
	if err := publisher.publishCatchUp(ctx); err != nil {
		t.Fatalf("publishCatchUp: %v", err)
	}
	if got := publisher.lastRevision.Load(); got != 9 {
		t.Fatalf("publisher last revision = %d, want 9", got)
	}
	if got := pipeline.ActiveRevision(); got != 9 {
		t.Fatalf("pipeline active revision = %d, want 9", got)
	}
	updated, ok := forwarder.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("updated governor type = %T, want *concurrencyGovernor", forwarder.govLocked())
	}
	if updated == original {
		t.Fatal("publishCatchUp did not replace the live governor")
	}
	if got := updated.cap; got != 12 {
		t.Fatalf("updated governor cap = %d, want 12", got)
	}
}

func TestPolicyPublisherPublishCatchUpIntegrationFailureLeavesRevisionsPinned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := newPolicyPublisherTestPool(t, ctx)

	const credentialID = 102
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.credentials
			(id, provider_id, concurrency_mode, concurrency_limit, rpm_limit, tpm_limit, revision)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		credentialID, 4, ModeConcurrency, 15, 0, 0, 7); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	pipeline := NewPipeline(Deps{})
	t.Cleanup(pipeline.Stop)
	if err := pipeline.ApplyPolicy(ctx, GovernorPolicy{Revision: 6}); err != nil {
		t.Fatalf("seed pipeline revision: %v", err)
	}
	forwarder := pipeline.getOrCreateForwarder(cred(credentialID, ModeConcurrency, 2))
	original, ok := forwarder.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("initial governor type = %T, want *concurrencyGovernor", forwarder.govLocked())
	}

	want := errors.New("redis unavailable")
	pipeline.SetGovernorBackend(&fakeBackend{
		kind:      BackendRedisEnforce,
		name:      "redis",
		notifyErr: want,
	})
	publisher := NewPolicyPublisher(pool, pipeline)
	publisher.lastRevision.Store(6)

	err := publisher.publishCatchUp(ctx)
	if !errors.Is(err, want) {
		t.Fatalf("publishCatchUp error = %v, want %v", err, want)
	}
	if got := publisher.lastRevision.Load(); got != 6 {
		t.Fatalf("publisher last revision after failure = %d, want 6", got)
	}
	if got := pipeline.ActiveRevision(); got != 6 {
		t.Fatalf("pipeline active revision after failure = %d, want 6", got)
	}
	current, ok := forwarder.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("current governor type = %T, want *concurrencyGovernor", forwarder.govLocked())
	}
	if current != original {
		t.Fatalf("live governor swapped despite failure: was %p now %p", original, current)
	}
}
