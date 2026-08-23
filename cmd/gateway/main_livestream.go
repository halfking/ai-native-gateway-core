// Live stream / incident converter helpers used by main() at startup.
//
// Extracted from main.go as part of the P0 main.go split refactor.
// See docs/refactor-plans/main-go-split.md for the full plan.
//
// All declarations here are package-private; behaviour is unchanged
// from the original implementation in main.go.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/routeincident"
)

// derefStr safely returns the value of a *string or "" if nil.
// 2026-07-27: used to pass optional telemetry fields (canonical_model,
// agent_name, agent_type, client_protocol) into LiveRequestFromTelemetry.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func valueOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// adminLiveRequestFromEntry adapts a freshly-persisted telemetry
// RequestLogEntry into the dashboard's swim-lane LiveRequest shape.
// Called on the telemetry worker goroutine, so the implementation
// MUST be cheap — the only I/O is the provider_id → catalog_code
// sync.Map lookup (with a 200ms-timeout DB fallback on miss).
// 2026-07-06: now resolves provider through credential_id when available.
func adminLiveRequestFromEntry(entry *telemetry.RequestLogEntry, hub *admin.LiveStreamSSEHub) admin.LiveRequest {
	clientModel := ""
	if entry.ClientModel != nil {
		clientModel = strings.TrimSpace(*entry.ClientModel)
	}
	outboundModel := ""
	if entry.OutboundModel != nil {
		outboundModel = strings.TrimSpace(*entry.OutboundModel)
	}
	status := ""
	if entry.RequestStatus != nil {
		status = strings.TrimSpace(*entry.RequestStatus)
	}
	var totalTokens *int
	if entry.PromptTokens != nil && entry.CompletionTokens != nil {
		t := *entry.PromptTokens + *entry.CompletionTokens
		totalTokens = &t
	}

	if hub != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		// Diagnose why provider info is sometimes missing. Both IDs being
		// empty means the request never reached credential selection
		// (e.g. auth/routing failure), which is expected for some flows;
		// one ID present but resolution returning empty points at a stale
		// cache entry or a missing providers/credentials row.
		hasCred := entry.CredentialID != nil && *entry.CredentialID > 0
		hasProv := entry.ProviderID != nil && *entry.ProviderID > 0
		if !hasCred && !hasProv {
			slog.Debug("live stream: request has no credential_id/provider_id",
				"request_id", entry.RequestID, "status", status, "tenant_id", entry.TenantID)
		}

		// Prefer credential_id resolution (accurate provider even when telemetry provider_id is stale/missing)
		providerCode := ""
		if hasCred {
			providerCode = hub.ProviderCodeForCredential(ctx, *entry.CredentialID)
		}
		// Fallback to provider_id
		if providerCode == "" && hasProv {
			providerCode = hub.ProviderCodeFor(ctx, *entry.ProviderID)
		}
		if providerCode == "" && (hasCred || hasProv) {
			slog.Debug("live stream: provider resolution returned empty",
				"request_id", entry.RequestID, "credential_id", entry.CredentialID,
				"provider_id", entry.ProviderID, "tenant_id", entry.TenantID)
		}

		// Extract canonical_id for model name resolution and aggregation
		canonicalID := 0
		if entry.CanonicalID != nil && *entry.CanonicalID > 0 {
			canonicalID = *entry.CanonicalID
		}

		return hub.LiveRequestFromTelemetry(
			ctx,
			entry.RequestID,
			liveStreamEventTime(entry),
			entry.TenantID,
			clientModel,
			outboundModel,
			canonicalID,
			derefStr(entry.CanonicalModel), // 2026-07-27: 标准模型名(migration 458)
			providerCode,
			status,
			entry.Success,
			entry.ErrorKind,
			entry.LatencyMs,
			entry.PromptTokens,
			entry.CompletionTokens,
			totalTokens,
			entry.CostUSD,
			entry.FailureStage,
			derefStr(entry.AgentName),
			derefStr(entry.AgentType),
			derefStr(entry.ClientProtocol),
			entry,
		)
	}
	// Fallback when hub is nil (defensive; unreachable in normal operation
	// because SetOnRequestLogEmitted is only wired when hub != nil).
	//
	// 2026-07-16: model fallback order matches the canonical fix in
	// LiveRequestFromTelemetry: client → outbound. Without the hub we
	// cannot resolve canonical_id (need DB), so client_supplied model
	// (usually the standard form like "claude-sonnet-4-5") wins over
	// the vendor raw name ("claude-sonnet-4-5-20251001" or similar).
	fallbackModel := clientModel
	if fallbackModel == "" {
		fallbackModel = outboundModel
	}
	return admin.LiveRequest{
		RequestID:        entry.RequestID,
		Ts:               liveStreamEventTime(entry).Format(time.RFC3339),
		TenantID:         entry.TenantID,
		Model:            fallbackModel,
		CanonicalName:    fallbackModel, // best-effort: prefer client over vendor raw
		ModelCategory:    "",            // cannot resolve without hub
		ProviderCode:     "",            // cannot resolve without hub
		Status:           status,
		LatencyMs:        entry.LatencyMs,
		PromptTokens:     entry.PromptTokens,
		CompletionTokens: entry.CompletionTokens,
		TotalTokens:      totalTokens,
		CostUSD:          entry.CostUSD,
		ErrorKind:        entry.ErrorKind,
	}
}

func liveStreamEventTime(entry *telemetry.RequestLogEntry) time.Time {
	if entry != nil && entry.EventAt != nil && !entry.EventAt.IsZero() {
		return entry.EventAt.UTC()
	}
	return time.Now().UTC()
}

// incidentUpdateFromResult converts a route-incident transition
// result into the SSE envelope body. It is the single bridge
// between the observer (domain) and the dashboard hub (admin), so
// the conversion is centralised here to keep both packages free of
// mutual dependencies. All sanitization (kind / stage length) is
// already done in domains/routeincident/redact.go.
func incidentUpdateFromResult(r *routeincident.TransitionResult) *admin.LiveIncidentUpdate {
	if r == nil || r.Incident == nil {
		return nil
	}
	u := r.Update
	out := &admin.LiveIncidentUpdate{
		Type:           "incident_update",
		TenantID:       u.RouteKey.TenantID,
		IncidentID:     u.IncidentID,
		State:          string(u.State),
		FailureStreak:  u.FailureStreak,
		RecoveryStreak: u.RecoveryStreak,
		Visible:        u.Visible,
		UpdatedAt:      u.UpdatedAt,
		RouteKey: admin.LiveRouteKey{
			Protocol:     u.RouteKey.Protocol,
			Model:        u.RouteKey.Model,
			ProviderID:   u.RouteKey.ProviderID,
			CredentialID: u.RouteKey.CredentialID,
		},
	}
	if u.LastError != nil {
		out.LastError = &admin.LiveSanitizedError{
			Kind:  u.LastError.Kind,
			Stage: u.LastError.Stage,
		}
	}
	// Affected lanes are derived from the route key. The frontend
	// uses the model dimension as the primary aggregation key; the
	// provider dimension is a secondary key. We omit the
	// value here because the hub knows how to render the
	// dimensions it cares about (see useSwimLane.ts). Phase 2
	// will populate these from the canonical → vendor mapping.
	if u.RouteKey.Model != "" {
		out.AffectedLanes = append(out.AffectedLanes, admin.AffectedLane{
			Dimension: "model",
			Value:     u.RouteKey.Model,
		})
	}
	if u.RouteKey.ProviderID != nil {
		out.AffectedLanes = append(out.AffectedLanes, admin.AffectedLane{
			Dimension: "provider",
			Value:     fmt.Sprintf("provider-%d", *u.RouteKey.ProviderID),
		})
	}
	return out
}

// newIncidentPublishFn returns the Publish callback for the route-incident
// observer. It gates nil/empty/no-op results and forwards the rest to the
// hub for SSE delivery. Behaviour is identical to the inline closure that
// previously lived in main(); the closure-captured `liveStreamHub` was
// the only local dependency and is now an explicit parameter so the
// publisher can be tested / reused.
func newIncidentPublishFn(hub *admin.LiveStreamSSEHub) func(*routeincident.TransitionResult) {
	return func(r *routeincident.TransitionResult) {
		if r == nil || r.NoOp || hub == nil {
			return
		}
		hub.PublishIncidentUpdate(incidentUpdateFromResult(r))
	}
}

// liveQueueSnapshotProvider adapts the independent queue projection to the
// admin SSE wire type. It never reads or locks the dispatch Pipeline.
func liveQueueSnapshotProvider(projection *dispatch.QueueProjection) *admin.LiveQueueSnapshot {
	if projection == nil {
		return liveQueueSnapshotFromLanes(nil, nil, true, false)
	}
	view := projection.Snapshot()
	if view == nil {
		return liveQueueSnapshotFromLanes(nil, nil, true, false)
	}
	out := &admin.LiveQueueSnapshot{Enabled: view.Enabled, Wired: view.Wired, SourceVersion: view.SourceVersion,
		Models: make([]admin.LiveQueueLaneSnapshot, 0, len(view.Models)), Credentials: make([]admin.LiveQueueLaneSnapshot, 0, len(view.Credentials))}
	if view.Pipeline != nil {
		out.Pipeline = &admin.LiveQueuePipelineStats{Depth: view.Pipeline.Depth, WaitingMsP50: view.Pipeline.WaitingMsP50, WaitingMsP95: view.Pipeline.WaitingMsP95, InFlight: view.Pipeline.InFlight, Degraded: view.Pipeline.Degraded}
	}
	for _, lane := range view.Models {
		out.Models = append(out.Models, admin.LiveQueueLaneSnapshot{Model: lane.Model, Depth: lane.Depth})
	}
	for _, lane := range view.Credentials {
		out.Credentials = append(out.Credentials, admin.LiveQueueLaneSnapshot{Credential: lane.Credential, Mode: lane.Mode, Depth: lane.Depth, Limit: lane.Limit, Full: lane.Full})
	}
	return out
}

func liveQueueSnapshotFromLanes(models, credentials []dispatch.QueueSnapshot, enabled, wired bool) *admin.LiveQueueSnapshot {
	modelLanes := make([]admin.LiveQueueLaneSnapshot, 0, len(models))
	for _, lane := range models {
		modelLanes = append(modelLanes, admin.LiveQueueLaneSnapshot{
			Model: lane.Model,
			Depth: lane.Depth,
		})
	}
	credentialLanes := make([]admin.LiveQueueLaneSnapshot, 0, len(credentials))
	for _, lane := range credentials {
		credentialLanes = append(credentialLanes, admin.LiveQueueLaneSnapshot{
			Credential: lane.Credential,
			Mode:       lane.Mode,
			Depth:      lane.Depth,
			Limit:      lane.Limit,
			Full:       lane.Full,
		})
	}
	return &admin.LiveQueueSnapshot{
		Enabled:     enabled,
		Wired:       wired,
		Models:      modelLanes,
		Credentials: credentialLanes,
	}
}

// liveNodeStatusProvider reads the stable credential/provider projection used
// by the node matrix. Runtime-only fields are intentionally left at their
// zero values until a non-blocking runtime source is available.
//
// OBS-BE4 (2026-08-15): the snapshot is additionally decorated with
// disable-kind / cooldown fields. Sources are queried per refresh (no new
// state is stored — ADR-V3-103 projection only):
//
//   - DB: credentials.availability_recover_at / quota_recover_at /
//     cooling_until → SystemRecoverAt (earliest) and, combined with
//     manual_disabled / availability_state / quota_state / circuit_state,
//     DisableKind.
//   - credentialfpslot NodeState (per (credential, raw_model)): ONE batch
//     MGET (GetNodeStatesBatch, the same interface the router hot path uses
//     since 63230d42) over every (credential × bound model) pair →
//     FPDisabled / FPDisabledUntil / LastErrorAt. Never a per-model GET
//     loop — that would reintroduce Redis RTT × N amplification.
func liveNodeStatusProvider(ctx context.Context, pool *pgxpool.Pool, fps *credentialfpslot.Manager) ([]admin.LiveNodeStatus, error) {
	if pool == nil {
		return []admin.LiveNodeStatus{}, nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	rows, err := pool.Query(queryCtx, `
		SELECT c.id, COALESCE(c.label, ''), c.provider_id,
		       COALESCE(NULLIF(p.display_name, ''), NULLIF(p.catalog_code, ''), p.code, ''),
		       COALESCE(c.circuit_state, ''),
		       COALESCE(c.availability_state, ''),
		       COALESCE(c.quota_state, ''),
		       COALESCE(c.health_status, ''),
		       COALESCE(c.manual_disabled, FALSE),
		       c.availability_recover_at,
		       c.quota_recover_at,
		       c.cooling_until
		FROM credentials c
		LEFT JOIN providers p ON p.id = c.provider_id
		ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]admin.LiveNodeStatus, 0, 16)
	for rows.Next() {
		var status admin.LiveNodeStatus
		var availRecoverAt, quotaRecoverAt, coolingUntil *time.Time
		if err := rows.Scan(
			&status.CredentialID,
			&status.CredentialLabel,
			&status.ProviderID,
			&status.ProviderCode,
			&status.CircuitState,
			&status.AvailabilityState,
			&status.QuotaState,
			&status.HealthStatus,
			&status.ManualDisabled,
			&availRecoverAt,
			&quotaRecoverAt,
			&coolingUntil,
		); err != nil {
			return nil, err
		}
		status.DisableKind = nodeDisableKind(status.ManualDisabled,
			status.AvailabilityState, status.QuotaState, status.CircuitState)
		status.SystemRecoverAt = earliestTime(availRecoverAt, quotaRecoverAt, coolingUntil)
		out = append(out, status)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if fps != nil && len(out) > 0 {
		if err := decorateFPNodeState(queryCtx, pool, fps, out); err != nil {
			// Decoration is best-effort: a Redis hiccup must not blank the
			// whole node matrix. The DB-sourced fields stay valid.
			slog.Warn("live stream: fp node state decoration failed", "error", err)
		}
	}
	return out, nil
}

// decorateFPNodeState fills the fpslot-derived OBS-BE4 fields on every node
// in-place. Both lookups are single round trips: one SQL query enumerating
// (credential_id, raw_model) bindings and one Redis MGET via the manager's
// batch interface.
func decorateFPNodeState(ctx context.Context, pool *pgxpool.Pool, fps *credentialfpslot.Manager, out []admin.LiveNodeStatus) error {
	rows, err := pool.Query(ctx, `
		SELECT cmb.credential_id, pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	modelsByCred := make(map[int][]string, len(out))
	for rows.Next() {
		var credID int
		var model string
		if err := rows.Scan(&credID, &model); err != nil {
			return err
		}
		modelsByCred[credID] = append(modelsByCred[credID], model)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	keys := make([]credentialfpslot.NodeStateKey, 0, len(modelsByCred)*4)
	for _, status := range out {
		for _, m := range modelsByCred[status.CredentialID] {
			keys = append(keys, credentialfpslot.NodeStateKey{CredentialID: status.CredentialID, Model: m})
		}
	}
	if len(keys) == 0 {
		return nil
	}
	states, err := fps.GetNodeStatesBatch(ctx, keys)
	if err != nil {
		return err
	}

	nowUnix := time.Now().Unix()
	idx := 0
	for i := range out {
		credID := out[i].CredentialID
		// 2026-08-17 (OBS-UI model-grouped nodes): publish the bound model
		// list to the wire so the frontend can group available nodes by
		// model. The SQL/Redis cost is unchanged (we already had
		// modelsByCred in scope).
		if names := modelsByCred[credID]; len(names) > 0 {
			dup := make([]string, len(names))
			copy(dup, names)
			out[i].RawModels = dup
		}
		count := len(modelsByCred[credID])
		proj := fpNodeProjection(nowUnix, states[idx:idx+count])
		idx += count
		out[i].FPDisabled = proj.fpDisabled
		out[i].FPDisabledUntil = proj.fpDisabledUntil
		out[i].LastErrorAt = proj.lastErrorAt
	}
	return nil
}

// fpNodeProjection aggregates the per-(credential, model) fpslot NodeStates
// of ONE node (credential) into the OBS-BE4 wire fields. Pure function —
// snapshot-tested without Redis.
//
//   - fpDisabled:      true when any bound model is currently inside its
//     cooldown window (Disabled with DisabledUntil in the
//     future; an expired cooldown counts as recovered, the
//     same semantics as NodeState.IsUsable's expiry check).
//   - fpDisabledUntil: the DisabledUntil belonging to the most recent
//     disable event (max LastDisabledAt; falls back to max
//     DisabledUntil when the P0 timestamp is absent).
//   - lastErrorAt:     max LastFailureAt across bound models.
func fpNodeProjection(nowUnix int64, states []*credentialfpslot.NodeState) (proj struct {
	fpDisabled      *bool
	fpDisabledUntil *time.Time
	lastErrorAt     *time.Time
}) {
	disabled := false
	var latestDisableAt, until, lastFailure int64
	for _, st := range states {
		if st == nil {
			continue
		}
		if st.Disabled && (st.DisabledUntil == 0 || st.DisabledUntil > nowUnix) {
			disabled = true
			// "最近一次禁用"：prefer the P0 LastDisabledAt timestamp; older
			// states (or zero timestamps) fall back to DisabledUntil itself.
			cand := st.LastDisabledAt
			if cand == 0 {
				cand = st.DisabledUntil
			}
			if cand > latestDisableAt {
				latestDisableAt = cand
				until = st.DisabledUntil
			}
		}
		if st.LastFailureAt > lastFailure {
			lastFailure = st.LastFailureAt
		}
	}
	if disabled {
		v := true
		proj.fpDisabled = &v
		if until > 0 {
			t := time.Unix(until, 0).UTC()
			proj.fpDisabledUntil = &t
		}
	}
	if lastFailure > 0 {
		t := time.Unix(lastFailure, 0).UTC()
		proj.lastErrorAt = &t
	}
	return proj
}

// nodeDisableKind maps the DB disable markers to the wire enum:
// manual_disabled → "manual"; availability/quota/circuit degraded →
// "system"; healthy → "" (omitted on the wire).
func nodeDisableKind(manualDisabled bool, availabilityState, quotaState, circuitState string) string {
	if manualDisabled {
		return "manual"
	}
	if (availabilityState != "" && availabilityState != "ready") ||
		(quotaState != "" && quotaState != "ok") ||
		(circuitState != "" && circuitState != "closed") {
		return "system"
	}
	return ""
}

// earliestTime returns the earliest non-nil timestamp, or nil when all are
// nil (omitted on the wire).
func earliestTime(ts ...*time.Time) *time.Time {
	var earliest *time.Time
	for _, t := range ts {
		if t == nil {
			continue
		}
		if earliest == nil || t.Before(*earliest) {
			earliest = t
		}
	}
	return earliest
}

// liveNodeStatusCache refreshes the DB projection off the SSE goroutine and
// retains the last good snapshot during transient database failures.
type liveNodeStatusCache struct {
	mu          sync.RWMutex
	snapshot    []admin.LiveNodeStatus
	lastErrorAt time.Time
}

func (c *liveNodeStatusCache) get() []admin.LiveNodeStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]admin.LiveNodeStatus{}, c.snapshot...)
}

func (c *liveNodeStatusCache) refresh(ctx context.Context, pool *pgxpool.Pool, fps *credentialfpslot.Manager) error {
	return c.refreshWith(func(ctx context.Context) ([]admin.LiveNodeStatus, error) {
		return liveNodeStatusProvider(ctx, pool, fps)
	}, ctx)
}

func (c *liveNodeStatusCache) refreshWith(fetch func(context.Context) ([]admin.LiveNodeStatus, error), ctx context.Context) error {
	snapshot, err := fetch(ctx)
	if err != nil {
		c.mu.Lock()
		shouldLog := time.Since(c.lastErrorAt) >= 30*time.Second
		if shouldLog {
			c.lastErrorAt = time.Now()
		}
		c.mu.Unlock()
		if shouldLog {
			slog.Warn("live stream: node status refresh failed", "error", err)
		}
		return err
	}
	c.mu.Lock()
	c.snapshot = append(c.snapshot[:0], snapshot...)
	c.mu.Unlock()
	return nil
}

func startLiveNodeStatusRefresh(pool *pgxpool.Pool, interval time.Duration, fps *credentialfpslot.Manager) (*liveNodeStatusCache, func()) {
	cache := &liveNodeStatusCache{snapshot: make([]admin.LiveNodeStatus, 0)}
	if pool == nil {
		return cache, func() {}
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	stopCh := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		refresh := func() { _ = cache.refresh(context.Background(), pool, fps) }
		refresh()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				refresh()
			case <-stopCh:
				return
			}
		}
	}()
	return cache, func() { stopOnce.Do(func() { close(stopCh) }) }
}
