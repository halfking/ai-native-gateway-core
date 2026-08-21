// Dispatch pipeline wiring for the gateway binary.
// See docs/会话优化v2/57-多层队列调度架构设计方案.md.
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// gatewayDispatchPipeline is retained by the executor wiring only. Admin and
// SSE reads use the independent projection below and never call Pipeline locks.
var gatewayDispatchPipeline *dispatch.Pipeline
var gatewayQueueProjection queueProjectionHolder

// gatewayLiveActionsEmitter (2026-08-15, V3.3-OBS OBS-B1) is the shared
// request-lifecycle action-event emitter, constructed in main.go next to the
// trace recorder and injected here into the dispatch pipeline
// (model_enqueued / node_enqueued / node_switch / model_switch / no_route).
var gatewayLiveActionsEmitter *liveactions.Emitter

var gatewayRequestJourneySink dispatch.ObservationSink

func stableGatewayInstanceID() string {
	if configured := strings.TrimSpace(os.Getenv("LLM_GATEWAY_INSTANCE_ID")); configured != "" {
		return configured
	}
	if hostname, err := os.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
		return strings.TrimSpace(hostname)
	}
	return "llm-gateway"
}

// wireDispatchPipeline builds the V2 dispatch pipeline from the executor,
// starts its worker pools, injects it into the executor, and returns it. If
// routingExec is nil the pipeline is skipped — and since AUDIT_24H B2b
// (2026-08-17) the executor has no fallback path, so a nil routingExec means
// every request fails with "dispatch pipeline not wired" (a wiring bug).
func wireDispatchPipeline(routingExec *executors.Executor) *dispatch.Pipeline {
	if routingExec == nil {
		slog.Warn("dispatch: routingExec nil, V2 pipeline not wired")
		return nil
	}
	p := routingExec.NewDispatchPipeline()
	p.SetObservationSink(gatewayRequestJourneySink)
	projection := dispatch.NewQueueProjection()
	gatewayQueueProjection.Store(projection)
	p.SetQueueObservationSink(projection)
	// V3.3-OBS OBS-B1 (2026-08-15): 动作事件发射器注入 dispatch pipeline。
	// nil 安全（发射点全部 no-op），发射器本身旁路异步、满即丢。
	p.SetLiveActions(gatewayLiveActionsEmitter)
	p.Start()
	routingExec.SetDispatchPipeline(p)
	gatewayDispatchPipeline = p
	slog.Info("dispatch_v2 pipeline wired (only execute path, AUDIT_24H B2b)",
		"allow_model_change", dispatch.IsModelChangeEnabled())
	return p
}

// handleDispatchQueues serves GET /api/admin/dispatch/queues — a live snapshot
// of the Tier-1 (model) and Tier-2 (credential) queue depths plus the
// dispatch_v2 gate state. Realizes the Tier-3 "display & statistics" layer
// alongside the Prometheus dispatch_* metrics.
func handleDispatchQueues(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	view := gatewayQueueProjectionSnapshot()
	resp := map[string]any{
		"enabled":     view.Enabled,
		"wired":       view.Wired,
		"models":      view.Models,
		"credentials": view.Credentials,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleDispatchWaterfall serves GET /api/admin/dispatch/waterfall — recent
// completed request 9-stage timelines for the admin waterfall UI.
//
// Query:
//   - limit (default 50, max 200)
//   - model (optional)
//   - credential_id (optional)
func handleDispatchWaterfall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	model := q.Get("model")
	credID := 0
	if v := q.Get("credential_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			credID = n
		}
	}

	var snap dispatch.WaterfallSnapshot
	tenantID := admin.EffectiveTenantIDAll(r)
	if projection := gatewayQueueProjection.Load(); projection != nil {
		snap = projection.SnapshotWaterfall(limit, model, credID, tenantID)
	} else {
		snap = dispatch.WaterfallSnapshot{Requests: []dispatch.WaterfallRequest{}, Enabled: true, Wired: false,
			BottleneckDiagnosis: dispatch.BottleneckDiagnosis{Bottleneck: "none", Message: "dispatch queue projection not wired"}}
	}
	snap = mergeWaterfallWithDB(r.Context(), snap, limit, model, credID, tenantID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}
