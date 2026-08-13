// Command gateway - main_v32_wiring.go (2026-08-14)
//
// V3.2 backend wire helpers: StateTransitionLogger + SSE providers
// (queue_snapshot / node_status). Keep all V3.2 concerns in one small file
// so the V3.2 commit is reviewable without touching unrelated wiring.

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

// wireStateTransitionLogger constructs the V3.2 state-transition logger
// and registers it as the process-wide singleton (consumed by
// streaming/handler.go via dispatch.LogRouteDecisionGlobal and
// streamretry/wrapper.go via dispatch.LogRetryGlobal).
//
// nil-safe: if db is nil, the constructor still returns a logger whose
// add() is a no-op (see domains/dispatch/state_transition_logger.go).
func wireStateTransitionLogger(db *pgxpool.Pool) *dispatch.StateTransitionLogger {
	l := dispatch.NewStateTransitionLogger(db)
	dispatch.SetGlobalStateTransitionLogger(l)
	slog.Info("V3.2: state-transition logger wired",
		"db_enabled", db != nil,
		"flush_interval", 2*time.Second,
		"batch_size", 100)
	return l
}

// wireQueueSnapshotProvider builds the closure handed to LiveStreamSSEHub
// for V3.2 BE-A1. The closure converts dispatch.Pipeline.Snapshot()
// (model + credential queues) into the dashboard wire shape
// (LiveQueueSnapshot). Wired: false when pipeline is not constructed
// (pre-deployment / test mode).
func wireQueueSnapshotProvider(pipeline *dispatch.Pipeline) func() *admin.LiveQueueSnapshot {
	return func() *admin.LiveQueueSnapshot {
		if pipeline == nil {
			return &admin.LiveQueueSnapshot{Enabled: dispatch.IsDispatchEnabled(), Wired: false}
		}
		models, creds := pipeline.Snapshot()
		out := &admin.LiveQueueSnapshot{
			Enabled:     dispatch.IsDispatchEnabled(),
			Wired:       true,
			Models:      make([]admin.LiveQueueLaneSnapshot, 0, len(models)),
			Credentials: make([]admin.LiveQueueLaneSnapshot, 0, len(creds)),
		}
		for _, m := range models {
			out.Models = append(out.Models, admin.LiveQueueLaneSnapshot{
				Model: m.Model,
				Depth: m.Depth,
			})
		}
		for _, c := range creds {
			out.Credentials = append(out.Credentials, admin.LiveQueueLaneSnapshot{
				Credential: c.Credential,
				Mode:       c.Mode,
				Depth:      c.Depth,
			})
		}
		return out
	}
}

// wireNodeStatusProvider builds the closure handed to LiveStreamSSEHub for
// V3.2 BE-A4. It is a projection of the credentialhealth / circuit / quota
// state (ADR-V3-103: those packages remain the single source of truth —
// this struct only carries what the UI needs). No retries on SQL errors
// — the SSE tick is 2s and the next tick will refresh; missing data
// during a transient DB blip is acceptable for the dashboard.
func wireNodeStatusProvider(db *pgxpool.Pool, peak *bg.ConcurrencyPeakCollector) func() []admin.LiveNodeStatus {
	return func() []admin.LiveNodeStatus {
		if db == nil {
			return nil
		}
		// 2s budget — the SSE tick is 2s; hanging the goroutine longer
		// would back-pressure the broadcast loop.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		rows, err := db.Query(ctx, `
			SELECT
				c.id            AS credential_id,
				c.provider_id   AS provider_id,
				COALESCE(p.display_name, p.catalog_code, p.code, '') AS provider_code,
				COALESCE(c.circuit_state, 'closed')  AS circuit_state,
				COALESCE(c.availability_state, 'unknown') AS availability_state,
				COALESCE(c.quota_state, 'unknown')  AS quota_state,
				COALESCE(c.health_status, 'unknown') AS health_status,
				COALESCE(c.manual_disabled, false) AS manual_disabled,
				c.last_latency_ms,
				c.last_error
			FROM credentials c
			LEFT JOIN providers p ON p.id = c.provider_id
			WHERE c.status = 'active'
			ORDER BY c.id
		`)
		if err != nil {
			slog.Debug("V3.2 node status: query failed", "err", err.Error())
			return nil
		}
		defer rows.Close()

		out := make([]admin.LiveNodeStatus, 0, 16)
		for rows.Next() {
			var n admin.LiveNodeStatus
			var lastLatency *int
			var lastError *string
			if err := rows.Scan(
				&n.CredentialID, &n.ProviderID, &n.ProviderCode,
				&n.CircuitState, &n.AvailabilityState, &n.QuotaState, &n.HealthStatus,
				&n.ManualDisabled, &lastLatency, &lastError,
			); err != nil {
				continue
			}
			n.LastLatencyMs = lastLatency
			if lastError != nil {
				n.LastError = *lastError
			}
			// In-flight count from the peak collector (model-agnostic — use
			// empty model key to get the credential's aggregate). Falls
			// back to 0 when peak collector is nil (test mode).
			if peak != nil {
				n.InFlight = peak.GetLiveConcurrent(int64(n.CredentialID), "")
			}
			out = append(out, n)
		}
		return out
	}
}
