//go:build integration

package bg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

type managerProbeSink struct{ manager *ursmv2.Manager }

func (s managerProbeSink) ApplyProbeForTenant(ctx context.Context, tenant string, credentialID int, rawModel string, success bool, latencyMs int) error {
	return s.manager.ApplyProbeForTenant(ctx, tenant, api.ProbeOutcome{
		CredentialID: credentialID, RawModel: rawModel, Success: success, LatencyMs: latencyMs,
	})
}

func TestStrictCanaryProbeQueueIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg, err := postgres.Run(ctx, "postgres:16-alpine", postgres.WithDatabase("canary"), postgres.WithUsername("canary"), postgres.WithPassword("canary"))
	if err != nil {
		t.Fatalf("start isolated postgres: %v", err)
	}
	defer func() { _ = pg.Terminate(ctx) }()
	connString, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("connect isolated postgres: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		CREATE EXTENSION IF NOT EXISTS pgcrypto;
		CREATE TABLE credential_probe_queue (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, credential_id BIGINT NOT NULL,
			provider_id BIGINT, tenant_id TEXT NOT NULL, canonical_model TEXT, raw_model TEXT NOT NULL,
			outbound_model TEXT, probe_command TEXT NOT NULL, probe_mode TEXT NOT NULL, priority SMALLINT NOT NULL,
			reason_code TEXT, reason_detail TEXT, max_attempts INT NOT NULL, source TEXT NOT NULL,
			source_event_id TEXT, parent_request_id TEXT, dedup_key TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
			status TEXT NOT NULL DEFAULT 'ready', attempt INT NOT NULL DEFAULT 0, next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			lease_until TIMESTAMPTZ, lease_token UUID, started_at TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			result_http_status INT, result_latency_ms INT, result_body_preview TEXT, finished_at TIMESTAMPTZ
		);
		CREATE UNIQUE INDEX queue_active_dedup ON credential_probe_queue (dedup_key) WHERE status IN ('ready', 'running');
		CREATE TABLE node_probe_runs (
			credential_id BIGINT, raw_model_name TEXT, trigger_kind TEXT, attempt INT, next_retry_seconds INT,
			direct_ok BOOLEAN, direct_http_status INT, direct_err_code TEXT, direct_latency_ms INT, direct_err_detail TEXT,
			gateway_ok BOOLEAN, gateway_http_status INT, gateway_err_code TEXT, gateway_latency_ms INT, gateway_err_detail TEXT,
			success BOOLEAN, started_at TIMESTAMPTZ, completed_at TIMESTAMPTZ, duration_ms INT,
			api_model TEXT, outbound_model TEXT, provider_id BIGINT, request_url TEXT, request_headers JSONB,
			request_body TEXT, response_body TEXT, timeout_at_ms INT, via_proxy BOOLEAN
		);`); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.StrictCanary = true
	cfg.CanaryTenants = []string{"canary-tenant"}
	cfg.CanaryCredentials = []int{101}
	cfg.CanaryModels = []string{"glm-5.2"}
	cfg.RedisKeyPrefix = "ursm:v2:canary-integration:"
	manager := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	if err := manager.SetReady(ctx, true); err != nil {
		t.Fatalf("open isolated URSM ready gate: %v", err)
	}

	queue := NewProbeQueue(pool)
	queue.SetScope(manager)
	allowed := ProbeQueueTask{CredentialID: 101, TenantID: "canary-tenant", RawModel: "glm-5.2", Command: "node_probe", Mode: "multi_round", Source: "admin", MaxAttempts: 1, DedupKey: "canary-probe"}
	if _, _, err := queue.Enqueue(ctx, allowed); err != nil {
		t.Fatalf("enqueue allowed probe: %v", err)
	}
	denied := allowed
	denied.CredentialID = 102
	denied.DedupKey = "denied-probe"
	if _, _, err := queue.Enqueue(ctx, denied); !errors.Is(err, ErrProbeOutOfScope) {
		t.Fatalf("enqueue denied probe error=%v, want ErrProbeOutOfScope", err)
	}

	tasks, err := queue.Claim(ctx, 1, ProbeQueueLeaseDefault)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("claim allowed probe tasks=%v err=%v", len(tasks), err)
	}
	worker := &NodeProbeWorker{db: pool, stateSink: managerProbeSink{manager: manager}}
	service := NewProbeService(worker, nil)
	service.SetProbeQueue(queue)
	service.SetScope(manager)
	service.directRoundFn = func(context.Context, int, string) nodeProbeRoundResult {
		return nodeProbeRoundResult{ok: true, providerID: 1, outboundModel: "glm-5.2", httpStatus: 200, latencyMs: 7}
	}
	service.gatewayRoundFn = func(context.Context, int, string) gatewayProbeResult {
		return gatewayProbeResult{round: nodeProbeRoundResult{ok: true, httpStatus: 200, latencyMs: 9}, pinned: true}
	}
	service.applyOutcomeFn = func(context.Context, probeOutcome) {}
	result, err := service.Run(ctx, tasks[0])
	if err != nil || result.Status != ProbeQueueSuccess {
		t.Fatalf("run allowed probe result=%+v err=%v", result, err)
	}
	if err := queue.Complete(ctx, tasks[0], result); err != nil {
		t.Fatalf("complete allowed probe: %v", err)
	}

	var runs int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM node_probe_runs WHERE credential_id=101 AND direct_ok AND gateway_ok AND success").Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("node_probe_runs=%d err=%v, want one successful audit row", runs, err)
	}
	if !mr.Exists("ursm:v2:canary-integration:node:canary-tenant:101:glm-5.2") {
		t.Fatal("allowed probe did not write the isolated tenant-scoped URSM key")
	}
	if mr.Exists("ursm:v2:canary-integration:node:canary-tenant:102:glm-5.2") {
		t.Fatal("denied probe wrote an isolated Redis key")
	}
}
