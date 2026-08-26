//go:build integration

package bg

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

const policyPublisherE2EPrerequisites = `
CREATE TABLE public.credentials (
	id BIGINT PRIMARY KEY,
	provider_id BIGINT NOT NULL,
	concurrency_mode TEXT NOT NULL DEFAULT 'concurrency',
	concurrency_limit INT NOT NULL DEFAULT 0,
	rpm_limit INT NOT NULL DEFAULT 0,
	tpm_limit INT NOT NULL DEFAULT 0,
	fp_slot_limit INT NOT NULL DEFAULT 0,
	max_queue_depth INT NOT NULL DEFAULT 0,
	max_queue_wait_ms INT NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'active',
	availability_state TEXT NOT NULL DEFAULT 'available',
	quota_state TEXT NOT NULL DEFAULT 'ok',
	circuit_state TEXT NOT NULL DEFAULT 'closed',
	lifecycle_status TEXT NOT NULL DEFAULT 'live',
	manual_disabled BOOLEAN NOT NULL DEFAULT false
);

CREATE OR REPLACE FUNCTION public.notify_auto_route_refresh()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	PERFORM pg_notify('auto_route_refresh', TG_TABLE_NAME || ':' || TG_OP);
	RETURN COALESCE(NEW, OLD);
END;
$$;
`

type policyPublisherForwardGate struct {
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	closed  bool
}

func (g *policyPublisherForwardGate) set(started chan struct{}, release chan struct{}) {
	g.mu.Lock()
	g.started = started
	g.release = release
	g.closed = false
	g.mu.Unlock()
}

func (g *policyPublisherForwardGate) wait(ctx context.Context) {
	g.mu.Lock()
	started := g.started
	release := g.release
	g.mu.Unlock()

	select {
	case started <- struct{}{}:
	case <-ctx.Done():
		return
	}
	select {
	case <-release:
	case <-ctx.Done():
	}
}

func (g *policyPublisherForwardGate) unblock() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.release != nil && !g.closed {
		close(g.release)
		g.closed = true
	}
}

// TestPolicyPublisherE2EAppliesRevisionToLiveCredForwarder proves the real
// migration-566 database-to-admission path. It covers startup catch-up before
// a forwarder exists, two notification-driven live governor swaps, and a
// non-governor update that preserves revision and emits no policy notification.
func TestPolicyPublisherE2EAppliesRevisionToLiveCredForwarder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	migration566, err := os.ReadFile("../sql/migrations/startup/566_credentials_governor_revision.sql")
	if err != nil {
		t.Fatalf("read migration 566: %v", err)
	}
	pool, cleanup := DispatchPostgresContainer(t, ctx, policyPublisherE2EPrerequisites+"\n"+string(migration566))
	defer cleanup()

	const (
		credentialID = 904
		providerID   = 77
		model        = "publisher-e2e-model"
	)
	var revision1 uint64
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.credentials
			(id, provider_id, concurrency_mode, concurrency_limit)
		VALUES ($1, $2, $3, $4)
		RETURNING revision`, credentialID, providerID, dispatch.ModeConcurrency, 1).Scan(&revision1); err != nil {
		t.Fatalf("seed migration-assigned policy: %v", err)
	}
	if revision1 == 0 {
		t.Fatal("migration 566 assigned revision 0")
	}

	gate := &policyPublisherForwardGate{}
	t.Cleanup(gate.unblock)
	ref := dispatch.CredentialRef{
		CredentialID:     credentialID,
		ProviderID:       providerID,
		ConcurrencyMode:  dispatch.ModeConcurrency,
		ConcurrencyLimit: 99,
	}
	pipeline := dispatch.NewPipeline(dispatch.Deps{
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) {
			return model, nil, nil
		},
		RouteFunc: func(context.Context, *dispatch.QueuedRequest) ([]dispatch.CredentialRef, error) {
			return []dispatch.CredentialRef{ref}, nil
		},
		ForwardFunc: func(forwardCtx context.Context, _ *dispatch.QueuedRequest, _ dispatch.CredentialRef) dispatch.ForwardOutcome {
			gate.wait(forwardCtx)
			return dispatch.ForwardOutcome{Result: "forwarded"}
		},
	})
	pipeline.Start()
	defer pipeline.Stop()

	publisher := dispatch.NewPolicyPublisher(pool, pipeline)
	publisher.Start(ctx)
	defer publisher.Stop()

	waitForPolicyRevision(t, pipeline, revision1, "startup catch-up")
	assertAdmissionLimit(t, ctx, pipeline, gate, model, "revision-1", 1)

	revision2 := updatePolicyConcurrency(t, ctx, pool, credentialID, 2)
	if revision2 <= revision1 {
		t.Fatalf("first governor update revision = %d, want > %d", revision2, revision1)
	}
	waitForPolicyRevision(t, pipeline, revision2, "first live swap")
	assertAdmissionLimit(t, ctx, pipeline, gate, model, "revision-2", 2)

	revision3 := updatePolicyConcurrency(t, ctx, pool, credentialID, 1)
	if revision3 <= revision2 {
		t.Fatalf("second governor update revision = %d, want > %d", revision3, revision2)
	}
	waitForPolicyRevision(t, pipeline, revision3, "second live swap")
	assertAdmissionLimit(t, ctx, pipeline, gate, model, "revision-3", 1)

	assertNonGovernorUpdateDoesNotPublish(t, ctx, pool, pipeline, credentialID, revision3)
}

func updatePolicyConcurrency(t *testing.T, ctx context.Context, pool *pgxpool.Pool, credentialID, limit int) uint64 {
	t.Helper()
	var revision uint64
	if err := pool.QueryRow(ctx, `
		UPDATE public.credentials
		SET concurrency_limit = $1
		WHERE id = $2
		RETURNING revision`, limit, credentialID).Scan(&revision); err != nil {
		t.Fatalf("update concurrency limit %d: %v", limit, err)
	}
	return revision
}

func assertAdmissionLimit(t *testing.T, ctx context.Context, pipeline *dispatch.Pipeline, gate *policyPublisherForwardGate, model, phase string, limit int) {
	t.Helper()
	started := make(chan struct{}, limit+1)
	release := make(chan struct{})
	gate.set(started, release)

	requests := make([]<-chan error, 0, limit+1)
	for i := 0; i < limit; i++ {
		requests = append(requests, submitPolicyPublisherRequest(ctx, pipeline, phase+"-admitted-"+string(rune('a'+i)), model))
	}
	for i := 0; i < limit; i++ {
		waitForForwardStart(t, started, phase+" admitted request")
	}

	blocked := submitPolicyPublisherRequest(ctx, pipeline, phase+"-blocked", model)
	assertNoForwardStart(t, started, 250*time.Millisecond, phase)
	gate.unblock()
	for _, done := range requests {
		waitForSubmit(t, done, phase+" admitted request")
	}
	waitForSubmit(t, blocked, phase+" blocked request")
}

func assertNonGovernorUpdateDoesNotPublish(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pipeline *dispatch.Pipeline, credentialID int, revision uint64) {
	t.Helper()
	listener, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire policy notification observer: %v", err)
	}
	defer listener.Release()
	if _, err := listener.Exec(ctx, "LISTEN credentials_revision"); err != nil {
		t.Fatalf("listen credentials_revision: %v", err)
	}

	var gotRevision uint64
	if err := pool.QueryRow(ctx, `
		UPDATE public.credentials
		SET status = 'cooling'
		WHERE id = $1
		RETURNING revision`, credentialID).Scan(&gotRevision); err != nil {
		t.Fatalf("update non-governor field: %v", err)
	}
	if gotRevision != revision {
		t.Fatalf("non-governor revision = %d, want %d", gotRevision, revision)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	if _, err := listener.Conn().WaitForNotification(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("non-governor update emitted credentials_revision: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := pipeline.ActiveRevision(); got != revision {
		t.Fatalf("pipeline revision after non-governor update = %d, want %d", got, revision)
	}
}

func waitForPolicyRevision(t *testing.T, pipeline *dispatch.Pipeline, revision uint64, phase string) {
	t.Helper()
	if !waitFor(t, 5*time.Second, func() bool { return pipeline.ActiveRevision() == revision }) {
		t.Fatalf("%s did not apply revision %d; active revision=%d", phase, revision, pipeline.ActiveRevision())
	}
}

func submitPolicyPublisherRequest(ctx context.Context, pipeline *dispatch.Pipeline, id, model string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := pipeline.Submit(ctx, dispatch.NewQueuedRequest(id, "policy-publisher-e2e", model, ctx, nil))
		done <- err
	}()
	return done
}

func waitForForwardStart(t *testing.T, started <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not reach ForwardFunc", name)
	}
}

func assertNoForwardStart(t *testing.T, started <-chan struct{}, duration time.Duration, phase string) {
	t.Helper()
	select {
	case <-started:
		t.Fatalf("%s admitted too many concurrent forwards", phase)
	case <-time.After(duration):
	}
}

func waitForSubmit(t *testing.T, done <-chan error, name string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s failed: %v", name, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not complete", name)
	}
}
