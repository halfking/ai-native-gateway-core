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

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// gatewayDispatchPipeline holds the constructed pipeline so the admin
// /api/admin/dispatch/queues handler can read live snapshots. nil when the
// pipeline could not be built (e.g. routingExec absent).
var gatewayDispatchPipeline *dispatch.Pipeline

// gatewayLiveActionsEmitter (2026-08-15, V3.3-OBS OBS-B1) is the shared
// request-lifecycle action-event emitter, constructed in main.go next to the
// trace recorder and injected here into the dispatch pipeline
// (model_enqueued / node_enqueued / node_switch / model_switch / no_route).
var gatewayLiveActionsEmitter *liveactions.Emitter

var gatewayRequestJourneySink dispatch.EventSink

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
// routingExec is nil the pipeline is skipped (the executor falls back to the
// legacy synchronous loop regardless of the gate).
func wireDispatchPipeline(routingExec *executors.Executor) *dispatch.Pipeline {
	if routingExec == nil {
		slog.Warn("dispatch: routingExec nil, V2 pipeline not wired")
		return nil
	}
	p := routingExec.NewDispatchPipeline()
	p.SetEventSink(gatewayRequestJourneySink)
	// V3.3-OBS OBS-B1 (2026-08-15): 动作事件发射器注入 dispatch pipeline。
	// nil 安全（发射点全部 no-op），发射器本身旁路异步、满即丢。
	p.SetLiveActions(gatewayLiveActionsEmitter)
	p.Start()
	routingExec.SetDispatchPipeline(p)
	gatewayDispatchPipeline = p
	slog.Info("dispatch_v2 pipeline wired",
		"allow_model_change", dispatch.IsModelChangeEnabled(),
		"enabled", dispatch.IsDispatchEnabled())
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
	resp := map[string]any{
		"enabled":     dispatch.IsDispatchEnabled(),
		"wired":       gatewayDispatchPipeline != nil,
		"models":      []dispatch.QueueSnapshot{},
		"credentials": []dispatch.QueueSnapshot{},
	}
	if gatewayDispatchPipeline != nil {
		models, creds := gatewayDispatchPipeline.Snapshot()
		if models == nil {
			models = []dispatch.QueueSnapshot{}
		}
		if creds == nil {
			creds = []dispatch.QueueSnapshot{}
		}
		resp["models"] = models
		resp["credentials"] = creds
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
	if gatewayDispatchPipeline != nil {
		snap = gatewayDispatchPipeline.SnapshotWaterfall(limit, model, credID)
	} else {
		snap = dispatch.WaterfallSnapshot{
			Requests: []dispatch.WaterfallRequest{},
			Enabled:  dispatch.IsDispatchEnabled(),
			Wired:    false,
			BottleneckDiagnosis: dispatch.BottleneckDiagnosis{
				Bottleneck: "none",
				Message:    "dispatch pipeline not wired",
			},
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}
