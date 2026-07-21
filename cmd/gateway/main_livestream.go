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
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/routeincident"
)

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
