//go:build integration

// work_types_stats_integration_test.go — R37 self-audit: the four 24h stats
// queries in work_types.go gained the R35-R2 synthetic-actor exclusion
// (`wtSyntheticExclude`) as string concatenation only — never executed
// against a real planner. This file runs the REAL handleStats against a real
// PostgreSQL and asserts the gateway-synthetic rounds (goal-% shadow rounds,
// internal title/summary loopbacks) are excluded from total_auto /
// by_work_type / by_l1_task / top_models.
//
// Run:
//
//	go test -tags=integration -timeout 5m -count=1 -run TestWorkTypesStats ./admin
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const workTypesStatsSchema = `
CREATE TABLE public.request_logs_hot (
	work_type text,
	task_type text,
	is_auto_request boolean,
	client_model text,
	outbound_model text,
	origin_actor text,
	tenant_id text,
	ts timestamptz NOT NULL DEFAULT NOW()
);
CREATE TABLE public.work_type_config (
	key text PRIMARY KEY,
	label text,
	category text,
	l1_task_type text,
	enabled boolean NOT NULL DEFAULT true,
	sort_order int NOT NULL DEFAULT 0,
	synced_from_acc_at timestamptz
);
INSERT INTO public.work_type_config (key, label, category, l1_task_type)
	VALUES ('code', 'Code', 'dev', 'code');
`

func TestWorkTypesStatsExcludeSyntheticActors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Self-contained container spin (no shared admin helper yet): mirrors
	// bg.DispatchPostgresContainer's testcontainers contract.
	var pool *pgxpool.Pool
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("work_types_stats"),
		postgres.WithUsername("work_types_stats"),
		postgres.WithPassword("work_types_stats"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	defer func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		_ = container.Terminate(terminateCtx)
	}()
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				break
			}
			pool.Close()
		}
		if ctx.Err() != nil {
			t.Fatalf("postgres never became reachable: %v", err)
		}
		time.Sleep(time.Second)
	}
	if pool == nil {
		t.Fatal("pool never initialized")
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, workTypesStatsSchema); err != nil {
		t.Fatalf("exec schema: %v", err)
	}

	// One real auto turn and one goal-audit shadow turn with IDENTICAL
	// business attributes: every stats surface must count exactly one.
	seed := `INSERT INTO public.request_logs_hot
		(work_type, task_type, is_auto_request, client_model, outbound_model, origin_actor, ts)
		VALUES
		('code', 'code', TRUE, '', 'm1', 'user-app', NOW()),
		('code', 'code', TRUE, '', 'm1', 'goal-audit', NOW())`
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := &WorkTypeHandlers{db: pool}
	rr := httptest.NewRecorder()
	// Plain request (no tenant-admin context) → tenantLogsClause is empty.
	h.handleStats(rr, httptest.NewRequest(http.MethodGet, "/api/admin/work-types/stats", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("handleStats status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var out struct {
		TotalAuto  int `json:"total_auto"`
		ByWorkType map[string]struct {
			CountDirect int `json:"count_direct"`
			CountL1     int `json:"count_l1_proxy"`
		} `json:"by_work_type"`
		ByL1Task  map[string]int `json:"by_l1_task"`
		TopModels []struct {
			Model string `json:"model"`
			Count int    `json:"count"`
		} `json:"top_models"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}

	if out.TotalAuto != 1 {
		t.Errorf("total_auto = %d, want 1 (the goal-audit round must be excluded; 2 means the synthetic-actor filter regressed)", out.TotalAuto)
	}
	if wt, ok := out.ByWorkType["code"]; !ok {
		t.Error("by_work_type['code'] missing")
	} else if wt.CountDirect != 1 {
		t.Errorf("by_work_type['code'].count_direct = %d, want 1", wt.CountDirect)
	}
	if got := out.ByL1Task["code"]; got != 1 {
		t.Errorf("by_l1_task['code'] = %d, want 1", got)
	}
	if len(out.TopModels) != 1 || out.TopModels[0].Model != "m1" || out.TopModels[0].Count != 1 {
		t.Errorf("top_models = %+v, want [m1 ×1]", out.TopModels)
	}
}
