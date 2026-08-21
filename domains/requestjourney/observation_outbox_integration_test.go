//go:build integration

package requestjourney

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestObservationOutboxRecoversAfterRedisRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := startObservationOutboxPostgres(t, ctx)
	redisContainer, redisAddr := startObservationOutboxRedis(t, ctx)
	redisTerminated := false
	t.Cleanup(func() {
		if redisTerminated {
			return
		}
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := redisContainer.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate failed Redis: %v", err)
		}
	})
	firstRedis := redis.NewClient(&redis.Options{
		Addr: redisAddr, DialTimeout: 100 * time.Millisecond, ReadTimeout: 100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond, MaxRetries: 0,
	})
	t.Cleanup(func() { _ = firstRedis.Close() })
	if err := firstRedis.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping redis before outage: %v", err)
	}
	if err := redisContainer.Terminate(ctx); err != nil {
		t.Fatalf("stop redis for outage: %v", err)
	}
	redisTerminated = true

	event := testJourneyEvent("tenant-recovery", "request-recovery", 1)
	memory := NewProjection(DefaultConfig())
	firstRecorder := NewRecorder(memory, NewRedisStore(firstRedis, DefaultConfig()), NewPostgresRepository(pool))
	if firstRecorder.outbox == nil {
		t.Fatal("recorder did not select the PostgreSQL durable observation outbox")
	}
	if err := firstRecorder.Apply(ctx, event); err != nil {
		t.Fatalf("enqueue event before Redis outage: %v", err)
	}
	waitForObservationOutboxState(t, ctx, pool, event, "failed")

	var attempts int
	var lastError string
	if err := pool.QueryRow(ctx, `SELECT attempts, last_error
		FROM request_journey_observation_outbox
		WHERE tenant_id=$1 AND request_id=$2 AND seq=$3`, event.TenantID, event.RequestID, event.Seq).Scan(&attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts < 1 || lastError == "" {
		t.Fatalf("failed durable observation = attempts:%d error:%q, want retained failure", attempts, lastError)
	}
	assertObservationProjectionCount(t, ctx, pool, event, 1)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	if err := firstRecorder.Close(closeCtx); err != nil {
		t.Fatalf("close failed recorder: %v", err)
	}

	recoveredRedis, recoveredRedisAddr := startObservationOutboxRedis(t, ctx)
	t.Cleanup(func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := recoveredRedis.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate recovered redis: %v", err)
		}
	})
	recoveredClient := redis.NewClient(&redis.Options{Addr: recoveredRedisAddr})
	t.Cleanup(func() { _ = recoveredClient.Close() })
	recoveredStore := NewRedisStore(recoveredClient, DefaultConfig())
	secondRecorder := NewRecorder(NewProjection(DefaultConfig()), recoveredStore, NewPostgresRepository(pool))
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := secondRecorder.Close(shutdownCtx); err != nil {
			t.Errorf("close recovered recorder: %v", err)
		}
	})
	if secondRecorder.outbox == nil {
		t.Fatal("recovered recorder did not select the PostgreSQL durable observation outbox")
	}
	if err := secondRecorder.outbox.Replay(ctx, event.TenantID, event.RequestID, event.Seq); err != nil {
		t.Fatalf("replay durable observation: %v", err)
	}

	journey, err := recoveredStore.Detail(ctx, event.TenantID, event.RequestID)
	if err != nil {
		t.Fatalf("load replayed Redis journey: %v", err)
	}
	if len(journey.Events) != 1 || journey.Events[0].Seq != event.Seq {
		t.Fatalf("replayed Redis journey = %#v", journey)
	}
	assertObservationProjectionCount(t, ctx, pool, event, 1)
	var durableRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM request_journey_observation_outbox
		WHERE tenant_id=$1 AND request_id=$2 AND seq=$3`, event.TenantID, event.RequestID, event.Seq).Scan(&durableRows); err != nil {
		t.Fatal(err)
	}
	if durableRows != 0 {
		t.Fatalf("replayed durable observation rows = %d, want 0", durableRows)
	}
}

func startObservationOutboxPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("requestjourney"),
		postgres.WithUsername("requestjourney"),
		postgres.WithPassword("requestjourney"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	})
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for attempt := 0; attempt < 30; attempt++ {
		if err := pool.Ping(ctx); err == nil {
			break
		}
		if attempt == 29 {
			t.Fatalf("connect postgres: %v", err)
		}
		time.Sleep(time.Second)
	}
	for _, migration := range []string{
		"../../sql/migrations/startup/511_state_transitions_table.sql",
		"../../sql/migrations/startup/515_state_transitions_seq_unique.sql",
		"../../sql/migrations/startup/521_repair_state_transitions_tenant.sql",
		"../../sql/migrations/startup/530_request_journey_contract.sql",
		"../../sql/migrations/startup/531_request_journey_tenant_uniqueness.sql",
		"../../sql/migrations/startup/552_request_journey_durable_outbox.sql",
	} {
		body, err := os.ReadFile(migration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
	}
	return pool
}

func startObservationOutboxRedis(t *testing.T, ctx context.Context) (testcontainers.Container, string) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:           "redis:7-alpine",
			ExposedPorts:    []string{"6379/tcp"},
			WaitingFor:      wait.ForListeningPort("6379/tcp"),
			AlwaysPullImage: false,
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return container, fmt.Sprintf("%s:%s", host, port.Port())
}

func waitForObservationOutboxState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, event JourneyEvent, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		err := pool.QueryRow(ctx, `SELECT status FROM request_journey_observation_outbox
			WHERE tenant_id=$1 AND request_id=$2 AND seq=$3`, event.TenantID, event.RequestID, event.Seq).Scan(&status)
		if err == nil && status == want {
			return
		}
		if err != nil && err != pgx.ErrNoRows {
			t.Fatalf("load durable observation state: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("durable observation did not reach %q", want)
}

func assertObservationProjectionCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, event JourneyEvent, want int) {
	t.Helper()
	var actual int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM request_state_transitions
		WHERE tenant_id=$1 AND request_id=$2 AND seq=$3 AND event_type IS NOT NULL`, event.TenantID, event.RequestID, event.Seq).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("PostgreSQL observation projection rows = %d, want %d", actual, want)
	}
}
