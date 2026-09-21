//go:build integration

// DispatchPostgresContainer — shared helper for testcontainer-based
// integration tests in `package bg`.
//
// Used by:
//   - auto_route_realtime_listener_integration_test.go (C3 refactor)
//   - policy_publisher_e2e_test.go (Stage F / C4, when added)
//
// This helper is intentionally:
//
//   - Gated behind //go:build integration so it never enters regular builds.
//   - Private to `package bg` (lower-case identifier) so the dispatch package
//     does not gain a compile-time dependency on testcontainers.
//   - Built around `pgxpool` + the `postgres:16-alpine` testcontainer, the
//     same combo the integration tests already use.
//
// Behaviour preserved from the original inline code in
// auto_route_realtime_listener_integration_test.go:
//
//   - TEST_PG_URL bypass: when set, skip the container entirely and return a
//     pool against the supplied DSN. Cleanup only closes the pool.
//   - 30-attempt retry around pgxpool.New + Ping, matching the convention
//     used by domains/hooks/handoff/migration_527_*. Docker Desktop's
//     mapped TCP socket occasionally resets the first probe while
//     postgres is still firing its "ready" log.
//   - Caller-supplied DDL: `extraSchema` is executed once after the pool is
//     reachable (CREATE TABLE / FUNCTION / TRIGGER definitions, etc.).
//   - On container-start failure, any partially-started container is
//     terminated before t.Fatalf so the next test isn't blocked by a
//     dangling docker resource.
package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// DispatchPostgresContainer returns an isolated Postgres pool with the
// caller-provided `extraSchema` already applied. The returned cleanup
// closes the pool and terminates the container (or just closes the pool
// when TEST_PG_URL is supplied).
//
// pgxpool.New does a single ping during construction; on Docker Desktop
// the mapped TCP socket occasionally resets the first probe while the
// container's "ready" log fires before postgres fully accepts
// connections. Retry on connection-level errors until either we succeed
// or the context expires.
func DispatchPostgresContainer(t *testing.T, ctx context.Context, extraSchema string) (*pgxpool.Pool, func()) {
	t.Helper()
	openPool := func(dsn string) (*pgxpool.Pool, error) {
		var (
			pool *pgxpool.Pool
			err  error
		)
		for attempt := 0; attempt < 30; attempt++ {
			pool, err = pgxpool.New(ctx, dsn)
			if err == nil {
				pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
				pingErr := pool.Ping(pingCtx)
				pingCancel()
				if pingErr == nil {
					return pool, nil
				}
				pool.Close()
				err = pingErr
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}
		return nil, err
	}

	execSchema := func(pool *pgxpool.Pool) {
		t.Helper()
		if extraSchema == "" {
			return
		}
		if _, err := pool.Exec(ctx, extraSchema); err != nil {
			pool.Close()
			t.Fatalf("exec schema: %v", err)
		}
	}

	if dsn := os.Getenv("TEST_PG_URL"); dsn != "" {
		pool, err := openPool(dsn)
		if err != nil {
			t.Fatalf("connect TEST_PG_URL: %v", err)
		}
		execSchema(pool)
		return pool, func() { pool.Close() }
	}
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("auto_route_listener"),
		postgres.WithUsername("auto_route_listener"),
		postgres.WithPassword("auto_route_listener"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("connection string: %v", err)
	}
	pool, err := openPool(dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("pgxpool.New: %v", err)
	}
	execSchema(pool)
	cleanup := func() {
		pool.Close()
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}
	return pool, cleanup
}
