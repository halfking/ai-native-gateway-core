//go:build integration

package routeincident

// store_pending_lifecycle_integration_test.go — R33 (2026-09-17), finding
// from the incident-fixes re-audit: 715 widened the partial unique index to
// include 'pending', but lockActive still filtered
// `state IN ('active','recovering')`. Consequence: the second failure on a
// pending route re-ran DecideState(nil,...) → insertNew → 23505 on
// uq_route_incidents_active_route (observer retries exhausted), the
// threshold never accumulated, incidents never became visible, and the
// route key was poisoned forever (every later failure = 5 doomed retries).
//
// The pin drives the REAL Store.Transition against a scratch database with
// the 715 table shape and replays the poisoning scenario end-to-end:
//
//	export ROUTEINCIDENT_IT_DSN='postgres://llm_gateway:<pwd>@127.0.0.1:5432/llm_routeincident_it?sslmode=disable'
//	go test -tags integration ./domains/routeincident/ -run TestITPendingLifecycle -v -count=1
//
// DSN always via env — credentials never in the repo (no-credential rule).
// Cleanup: DROP DATABASE llm_routeincident_it.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func itSetup(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("ROUTEINCIDENT_IT_DSN")
	if dsn == "" {
		t.Skip("ROUTEINCIDENT_IT_DSN 未设置：需要指向一次性 scratch 库（见文件头注释）")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// 389 base shape + 715 deltas — exactly what ensureRouteIncidentSchema +
	// ensureRouteIncidentPendingState leave behind on a fresh install.
	_, err = pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS route_incidents (
			id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id           TEXT NOT NULL,
			endpoint_protocol   TEXT NOT NULL,
			model               TEXT NOT NULL,
			provider_id         BIGINT,
			credential_id       BIGINT,
			state               TEXT NOT NULL,
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
		ALTER TABLE route_incidents DROP CONSTRAINT IF EXISTS route_incidents_state_check;
		ALTER TABLE route_incidents ADD CONSTRAINT route_incidents_state_check
			CHECK (state IN ('pending', 'active', 'recovering', 'recovered'));
		CREATE UNIQUE INDEX IF NOT EXISTS uq_route_incidents_active_route
			ON route_incidents (
				tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0)
			)
			WHERE state IN ('pending', 'active', 'recovering');
		CREATE OR REPLACE FUNCTION touch_route_incidents_updated_at()
		RETURNS trigger AS $$
		BEGIN
			NEW.updated_at := now();
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		DROP TRIGGER IF EXISTS route_incidents_touch ON route_incidents;
		CREATE TRIGGER route_incidents_touch BEFORE UPDATE ON route_incidents
			FOR EACH ROW EXECUTE FUNCTION touch_route_incidents_updated_at();
		CREATE TABLE IF NOT EXISTS route_incident_events (
			id                  BIGSERIAL PRIMARY KEY,
			incident_id         UUID NOT NULL REFERENCES route_incidents(id) ON DELETE CASCADE,
			event_type          TEXT NOT NULL CHECK (event_type IN (
				'opened', 'failure_observed', 'recovery_progress',
				'recovered', 'diagnostic_run', 'operator_action')),
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
		TRUNCATE route_incident_events, route_incidents;
	`)
	require.NoError(t, err)
	return NewStore(pool)
}

func failureIn(route string, n int) TransitionInput {
	return TransitionInput{
		TenantID:       "t-it",
		Protocol:       "openai_chat_completions",
		Model:          route,
		RequestID:      route + "-fail-" + time.Now().UTC().Format("150405.000000000") + "-" + nLabel(n),
		TerminalStatus: TerminalFailure,
		FailureKind:    "upstream_5xx",
		FailureStage:   "upstream",
		OccurredAt:     time.Now().UTC(),
	}
}

func nLabel(n int) string { return string(rune('a' + n)) }

// TestITPendingLifecycle_ThresholdAccumulates replays the exact poisoning
// scenario: three consecutive failures on one route must walk
// pending(1) → pending(2) → active(3, visible). Pre-fix, failure #2 hit
// 23505 on the unique index and the route was stuck forever.
func TestITPendingLifecycle_ThresholdAccumulates(t *testing.T) {
	s := itSetup(t)
	ctx := context.Background()

	// Failure #1: opens pending (threshold is 3).
	r1, err := s.Transition(ctx, failureIn("route-threshold", 0))
	require.NoError(t, err)
	require.False(t, r1.NoOp)
	require.Equal(t, StatePending, r1.Incident.State)
	require.Equal(t, 1, r1.Incident.FailureStreak)
	require.False(t, r1.Visible, "pending must stay below the visibility radar")

	// Failure #2: THE regression — must UPDATE the pending row, not
	// collide with it.
	r2, err := s.Transition(ctx, failureIn("route-threshold", 1))
	require.NoError(t, err, "second failure must not 23505 on uq_route_incidents_active_route")
	require.Equal(t, StatePending, r2.Incident.State)
	require.Equal(t, 2, r2.Incident.FailureStreak)

	// Failure #3: threshold reached → active and visible.
	r3, err := s.Transition(ctx, failureIn("route-threshold", 2))
	require.NoError(t, err)
	require.Equal(t, StateActive, r3.Incident.State)
	require.Equal(t, 3, r3.Incident.FailureStreak)
	require.True(t, r3.Visible, "streak 3 crosses FailureToActive — incident must be visible")
}

// TestITPendingLifecycle_SuccessResetsPending: a success while pending must
// mark the row recovered (the incident never became visible) and free the
// route for a fresh incident afterwards.
func TestITPendingLifecycle_SuccessResetsPending(t *testing.T) {
	s := itSetup(t)
	ctx := context.Background()

	r1, err := s.Transition(ctx, failureIn("route-reset", 0))
	require.NoError(t, err)
	require.Equal(t, StatePending, r1.Incident.State)

	ok := failureIn("route-reset", 1)
	ok.TerminalStatus = TerminalSuccess
	r2, err := s.Transition(ctx, ok)
	require.NoError(t, err)
	require.Equal(t, StateRecovered, r2.Incident.State)
	require.False(t, r2.Visible)

	// Next failure opens a fresh pending row (recovered is excluded from
	// the unique index).
	r3, err := s.Transition(ctx, failureIn("route-reset", 2))
	require.NoError(t, err)
	require.Equal(t, StatePending, r3.Incident.State)
	require.Equal(t, 1, r3.Incident.FailureStreak)
	require.NotEqual(t, r1.Incident.ID, r3.Incident.ID, "recovered row must not be reused")
}
