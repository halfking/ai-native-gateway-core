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

// liveQueueSnapshotProvider adapts the dispatch package's queue snapshot to
// the admin SSE wire type. Snapshot acquires the pipeline's own locks, so the
// provider is safe to call from the hub ticker without blocking dispatch work.
func liveQueueSnapshotProvider(p *dispatch.Pipeline) *admin.LiveQueueSnapshot {
	if p == nil {
		return liveQueueSnapshotFromLanes(nil, nil, dispatch.IsDispatchEnabled(), false)
	}
	models, credentials := p.Snapshot()
	return liveQueueSnapshotFromLanes(models, credentials, dispatch.IsDispatchEnabled(), true)
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
func liveNodeStatusProvider(ctx context.Context, pool *pgxpool.Pool) ([]admin.LiveNodeStatus, error) {
	if pool == nil {
		return []admin.LiveNodeStatus{}, nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	rows, err := pool.Query(queryCtx, `
		SELECT c.id, c.provider_id,
		       COALESCE(NULLIF(p.display_name, ''), NULLIF(p.catalog_code, ''), p.code, ''),
		       COALESCE(c.circuit_state, ''),
		       COALESCE(c.availability_state, ''),
		       COALESCE(c.quota_state, ''),
		       COALESCE(c.health_status, ''),
		       COALESCE(c.manual_disabled, FALSE)
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
		if err := rows.Scan(
			&status.CredentialID,
			&status.ProviderID,
			&status.ProviderCode,
			&status.CircuitState,
			&status.AvailabilityState,
			&status.QuotaState,
			&status.HealthStatus,
			&status.ManualDisabled,
		); err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
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

func (c *liveNodeStatusCache) refresh(ctx context.Context, pool *pgxpool.Pool) error {
	return c.refreshWith(func(ctx context.Context) ([]admin.LiveNodeStatus, error) {
		return liveNodeStatusProvider(ctx, pool)
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

func startLiveNodeStatusRefresh(pool *pgxpool.Pool, interval time.Duration) (*liveNodeStatusCache, func()) {
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
		refresh := func() { _ = cache.refresh(context.Background(), pool) }
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
