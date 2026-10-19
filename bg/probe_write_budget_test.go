package bg

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type probeWriteTrace struct{ acquire func(context.Context) }

func (t probeWriteTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (probeWriteTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (t probeWriteTrace) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	t.acquire(ctx)
	return ctx
}
func (probeWriteTrace) TraceAcquireEnd(context.Context, *pgxpool.Pool, pgxpool.TraceAcquireEndData) {}

// Observe the real pool-acquisition boundary without contacting a database.
// SQL persistence is deliberately not claimed by this context-budget test.
func probeWriteTestPool(t *testing.T, acquire func(context.Context)) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://test@127.0.0.1:1/test?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MinConns, cfg.MaxConns = 0, 1
	cfg.ConnConfig.Tracer = probeWriteTrace{acquire: acquire}
	cfg.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("test forbids database connection")
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestWriteHealthGetsBudgetAfterProbeDeadline(t *testing.T) {
	for _, observation := range []bool{true, false} {
		t.Run(map[bool]string{true: "observation", false: "conclusive"}[observation], func(t *testing.T) {
			var calls atomic.Int32
			pool := probeWriteTestPool(t, func(ctx context.Context) {
				calls.Add(1)
				deadline, bounded := ctx.Deadline()
				if ctx.Err() != nil || !bounded || time.Until(deadline) < 4*time.Second || time.Until(deadline) > 5*time.Second {
					t.Errorf("persistence inherited exhausted budget: err=%v bounded=%v remaining=%s", ctx.Err(), bounded, time.Until(deadline))
				}
			})
			ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			defer cancel()
			(&CredentialProbeV2{db: pool}).writeHealth(ctx, 7, probeResult{ObservationOnly: observation, HealthStatus: "healthy", AvailabilityState: "ready"})
			if calls.Load() != 1 {
				t.Fatalf("pool acquisitions=%d, want one real writer attempt", calls.Load())
			}
		})
	}
}

func TestWriteHealthExplicitCancellationStopsWrites(t *testing.T) {
	var calls atomic.Int32
	pool := probeWriteTestPool(t, func(context.Context) { calls.Add(1) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	(&CredentialProbeV2{db: pool}).writeHealth(ctx, 7, probeResult{HealthStatus: "healthy", AvailabilityState: "ready"})
	if calls.Load() != 0 {
		t.Fatalf("cancelled probe entered persistence %d times", calls.Load())
	}
}

func TestWriteHealthPropagatesCancellationDuringPersistence(t *testing.T) {
	entered, done := make(chan struct{}), make(chan struct{})
	pool := probeWriteTestPool(t, func(ctx context.Context) { close(entered); <-ctx.Done() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		defer close(done)
		(&CredentialProbeV2{db: pool}).writeHealth(ctx, 7, probeResult{ObservationOnly: true})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer never entered pool")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("explicit cancellation did not stop persistence")
	}
}

type probeAuditBudgetDB struct{ inspect func(context.Context) error }

func (db probeAuditBudgetDB) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("INSERT 0 1"), db.inspect(ctx)
}

func TestSyncAuditHasOwnBudgetAfterSlowGateway(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	var inserts int
	w := &NodeProbeWorker{baseURL: srv.URL, apiKey: "test", client: srv.Client(), auditDB: probeAuditBudgetDB{inspect: func(ctx context.Context) error {
		inserts++
		deadline, bounded := ctx.Deadline()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !bounded || time.Until(deadline) < 4*time.Second || time.Until(deadline) > 5*time.Second {
			t.Errorf("audit budget bounded=%v remaining=%s", bounded, time.Until(deadline))
		}
		return nil
	}}}
	bookCtx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	gw := w.probeGateway(bookCtx, 7, "test")
	if !errors.Is(bookCtx.Err(), context.DeadlineExceeded) || gw.ok {
		t.Fatalf("slow gateway failed to consume bookkeeping context: ctx=%v round=%+v", bookCtx.Err(), gw)
	}
	if err := w.emitSyncAudit(bookCtx, 7, "test", nodeProbeRoundResult{ok: true}, gw, time.Now(), "request"); err != nil {
		t.Fatal(err)
	}
	if inserts != 1 {
		t.Fatalf("audit inserts=%d", inserts)
	}
}

func TestSyncAuditRetainsCompletedEvidenceAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w := &NodeProbeWorker{auditDB: probeAuditBudgetDB{inspect: func(ctx context.Context) error { return ctx.Err() }}}
	if err := w.emitSyncAudit(ctx, 7, "test", nodeProbeRoundResult{ok: true}, nodeProbeRoundResult{}, time.Now(), "request"); err != nil {
		t.Fatal(err)
	}
}
