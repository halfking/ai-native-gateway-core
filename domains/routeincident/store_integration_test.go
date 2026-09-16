//go:build integration

// store_integration_test.go — routeincident store 真库行为验证。
//
// 单元测试只能钉 DecideState 的纯函数语义；真库才能钉 store 的 SQL
// 行为。这里复现并钉死 2026-09-17 252 pg 日志里的生产事故：
//
//	I-1  同一路由第二条失败必须 UPDATE 715 引入的 pending 行
//	     （lockOpen 的状态集与部分唯一索引对齐），而不是裸 INSERT
//	     撞 uq_route_incidents_active_route (23505)；
//	I-2  并发首败（多个 observer goroutine 同时开事件）不得把
//	     23505 泄漏给调用方：ON CONFLICT DO NOTHING + 重试后所有
//	     失败都计入同一行；
//	I-3  pending + success → recovered（不可见收尾）；
//	I-4  NULL provider/credential 的路由键全程走通（COALESCE 匹配）。
//
// 门控（沿用 migration_715 的 TEST_PG_URL 约定，指向一次性测试库）：
//
//	TEST_PG_URL=postgres://user:pass@localhost:5432/dbname?sslmode=disable \
//	go test -tags=integration ./domains/routeincident/ -run TestStore -v -count=1
package routeincident

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const fixtureDDLRouteIncident = `
CREATE TABLE IF NOT EXISTS route_incidents (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           TEXT NOT NULL,
    endpoint_protocol   TEXT NOT NULL,
    model               TEXT NOT NULL,
    provider_id         BIGINT,
    credential_id       BIGINT,
    state               TEXT NOT NULL
        CHECK (state IN ('pending', 'active', 'recovering', 'recovered')),
    failure_streak      INT  NOT NULL DEFAULT 0,
    recovery_streak     INT  NOT NULL DEFAULT 0,
    first_failure_at    TIMESTAMPTZ NOT NULL,
    last_failure_at     TIMESTAMPTZ,
    last_success_at     TIMESTAMPTZ,
    recovered_at        TIMESTAMPTZ,
    total_failures      BIGINT NOT NULL DEFAULT 0,
    total_successes     BIGINT NOT NULL DEFAULT 0,
    last_error_kind     TEXT,
    last_failure_stage  TEXT,
    resolution_source   TEXT,
    resolved_by_user    TEXT,
    resolved_reason     TEXT,
    version             BIGINT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_route_incidents_active_route
    ON route_incidents (
        tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0)
    )
    WHERE state IN ('pending', 'active', 'recovering');

CREATE OR REPLACE FUNCTION touch_route_incidents_updated_at()
RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS route_incidents_touch ON route_incidents;
CREATE TRIGGER route_incidents_touch
    BEFORE UPDATE ON route_incidents
    FOR EACH ROW EXECUTE FUNCTION touch_route_incidents_updated_at();

CREATE TABLE IF NOT EXISTS route_incident_events (
    id                  BIGSERIAL PRIMARY KEY,
    incident_id         UUID NOT NULL REFERENCES route_incidents(id) ON DELETE CASCADE,
    event_type          TEXT NOT NULL
        CHECK (event_type IN (
            'opened', 'failure_observed', 'recovery_progress',
            'recovered', 'diagnostic_run', 'operator_action'
        )),
    request_id          TEXT,
    terminal_status     TEXT,
    failure_kind        TEXT,
    failure_stage       TEXT,
    failure_streak      INT,
    recovery_streak     INT,
    evidence            JSONB NOT NULL DEFAULT '{}'::jsonb,
    actor               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_route_incident_events_idem
    ON route_incident_events (incident_id, request_id, terminal_status)
    WHERE request_id IS NOT NULL;
`

func newTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; store integration test requires a live PostgreSQL (convention: tests/integration gating)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("TEST_PG_URL unreachable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("TEST_PG_URL unreachable: %v", err)
	}
	if _, err := pool.Exec(ctx, fixtureDDLRouteIncident); err != nil {
		pool.Close()
		t.Fatalf("apply fixture DDL: %v", err)
	}
	prefix := fmt.Sprintf("storetest-%d", time.Now().UnixNano())
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, "DELETE FROM route_incidents WHERE model LIKE $1", prefix+"%")
		pool.Close()
	}
	return NewStore(pool), cleanup
}

// transition builds a failure input against the test route key.
func failInput(prefix, model, reqID string, providerID, credentialID *int64) TransitionInput {
	in := TransitionInput{
		TenantID:       "default",
		Protocol:       "openai_chat_completions",
		Model:          prefix + model,
		ProviderID:     providerID,
		CredentialID:   credentialID,
		RequestID:      reqID,
		TerminalStatus: TerminalFailure,
		FailureKind:    "upstream_5xx",
		FailureStage:   "upstream",
		OccurredAt:     time.Now().UTC(),
	}
	return in
}

func succeedInput(prefix, model, reqID string, providerID, credentialID *int64) TransitionInput {
	in := failInput(prefix, model, reqID, providerID, credentialID)
	in.TerminalStatus = TerminalSuccess
	in.FailureKind = ""
	in.FailureStage = ""
	return in
}

// I-1: the 2026-09-17 production regression. The second failure on a
// route whose incident sits in 'pending' MUST update the row (streak
// 2), never surface a unique-violation error, and the third failure
// must make the incident active.
func TestStore_SecondFailureUpdatesPendingRow(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	p := int64(587)
	c := int64(59)
	for i := 1; i <= 3; i++ {
		res, err := store.Transition(ctx, failInput("pend1", "-m", fmt.Sprintf("req-%d", i), &p, &c))
		if err != nil {
			t.Fatalf("failure %d: Transition returned error: %v", i, err)
		}
		if res == nil || res.NoOp {
			t.Fatalf("failure %d: expected applied transition", i)
		}
		inc := res.Incident
		switch i {
		case 1:
			if inc.State != StatePending || inc.FailureStreak != 1 {
				t.Fatalf("failure 1: want pending/1, got %s/%d", inc.State, inc.FailureStreak)
			}
			if res.Visible {
				t.Fatalf("failure 1: pending incident must not be visible")
			}
		case 2:
			if inc.State != StatePending || inc.FailureStreak != 2 {
				t.Fatalf("failure 2: want pending/2 (regression: 23505 on pending INSERT), got %s/%d", inc.State, inc.FailureStreak)
			}
		case 3:
			if inc.State != StateActive || inc.FailureStreak != 3 {
				t.Fatalf("failure 3: want active/3, got %s/%d", inc.State, inc.FailureStreak)
			}
			if !res.Visible {
				t.Fatalf("failure 3: active incident must be visible")
			}
		}
	}
	live := storeRows(t, store, "pend1")
	if len(live) != 1 {
		t.Fatalf("want exactly 1 row for the route, got %d", len(live))
	}
}

// I-3: a success while the incident is pending closes it as recovered
// (never visible), freeing the route for a fresh incident later.
func TestStore_SuccessOnPendingRecovers(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := store.Transition(ctx, failInput("pendrec", "-m", "req-f1", nil, nil)); err != nil {
		t.Fatalf("failure 1: %v", err)
	}
	res, err := store.Transition(ctx, succeedInput("pendrec", "-m", "req-s1", nil, nil))
	if err != nil {
		t.Fatalf("success on pending: %v", err)
	}
	if res.NoOp || res.Incident == nil {
		t.Fatalf("success on pending must apply a transition, got %+v", res)
	}
	if res.Incident.State != StateRecovered || res.Visible {
		t.Fatalf("want recovered & invisible, got %s visible=%v", res.Incident.State, res.Visible)
	}
	// success with no live incident is a NoOp.
	res2, err := store.Transition(ctx, succeedInput("pendrec", "-m", "req-s2", nil, nil))
	if err != nil {
		t.Fatalf("success with no incident: %v", err)
	}
	if !res2.NoOp {
		t.Fatalf("success with no incident must be a NoOp, got %+v", res2)
	}
}

// I-2: N concurrent first failures on a fresh route — none may see a
// 23505, and every failure must land on the single live row.
func TestStore_ConcurrentFirstFailuresSingleRow(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Transition(ctx, failInput("race", "-m", fmt.Sprintf("req-%d", i), nil, nil))
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent failure %d returned error (unique violation must never leak): %v", i, err)
		}
	}
	rows := storeRows(t, store, "race")
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 live row after %d concurrent failures, got %d", n, len(rows))
	}
	r := rows[0]
	if r.TotalFailures != n {
		t.Fatalf("want total_failures=%d on the single row, got %d", n, r.TotalFailures)
	}
	if r.FailureStreak != n {
		t.Fatalf("want failure_streak=%d, got %d", n, r.FailureStreak)
	}
	if r.State != StateActive {
		t.Fatalf("want state=active at streak %d, got %s", n, r.State)
	}
}

// I-4 + full lifecycle: threshold → active → recovering → recovered,
// then a new failure opens a FRESH row (recovered rows are retained).
func TestStore_RecoveryLifecycleReopensFreshRow(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	p := int64(90008)
	c := int64(60)
	// 3 failures → active.
	for i := 1; i <= 3; i++ {
		res, err := store.Transition(ctx, failInput("cycle", "-m", fmt.Sprintf("f%d", i), &p, &c))
		if err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
		if i < 3 && res.Incident.State != StatePending {
			t.Fatalf("failure %d: want pending, got %s", i, res.Incident.State)
		}
		if i == 3 && res.Incident.State != StateActive {
			t.Fatalf("failure 3: want active, got %s", res.Incident.State)
		}
	}
	// 4 successes → recovering; 5th → recovered.
	for i := 1; i <= 5; i++ {
		res, err := store.Transition(ctx, succeedInput("cycle", "-m", fmt.Sprintf("s%d", i), &p, &c))
		if err != nil {
			t.Fatalf("success %d: %v", i, err)
		}
		want := StateRecovering
		if i == 5 {
			want = StateRecovered
		}
		if res.Incident.State != want {
			t.Fatalf("success %d: want %s, got %s", i, want, res.Incident.State)
		}
	}
	// A new failure opens a fresh pending row alongside the recovered one.
	res, err := store.Transition(ctx, failInput("cycle", "-m", "f-reopen", &p, &c))
	if err != nil {
		t.Fatalf("reopen failure: %v", err)
	}
	if res.Incident.State != StatePending {
		t.Fatalf("reopen: want fresh pending row, got %s", res.Incident.State)
	}
	rows := storeRows(t, store, "cycle")
	total := 0
	for _, r := range rows {
		if r.State != StateRecovered {
			total++
		}
	}
	if total != 1 {
		t.Fatalf("want exactly 1 live (non-recovered) row after reopen, got %d", total)
	}
}

// storeRows returns every row whose model starts with the prefix.
func storeRows(t *testing.T, store *Store, prefix string) []Incident {
	t.Helper()
	incs, err := store.List(context.Background(), ListFilter{TenantID: "default", Limit: 500})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var out []Incident
	for _, inc := range incs {
		if len(inc.RouteKey.Model) >= len(prefix) && inc.RouteKey.Model[:len(prefix)] == prefix {
			out = append(out, inc)
		}
	}
	return out
}
