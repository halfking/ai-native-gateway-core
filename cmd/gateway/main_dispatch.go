// Dispatch pipeline wiring for the gateway binary.
// See docs/会话优化v2/57-多层队列调度架构设计方案.md.
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

// gatewayDispatchPipeline holds the constructed pipeline so the admin
// /api/admin/dispatch/queues handler can read live snapshots. nil when the
// pipeline could not be built (e.g. routingExec absent).
var gatewayDispatchPipeline *dispatch.Pipeline

// wireDispatchPipeline builds the V2 dispatch pipeline from the executor,
// starts its worker pools, injects it into the executor, and returns it. If
// routingExec is nil the pipeline is skipped (the executor falls back to the
// legacy synchronous loop regardless of the gate).
func wireDispatchPipeline(routingExec *executors.Executor) *dispatch.Pipeline {
	if routingExec == nil {
		slog.Warn("dispatch: routingExec nil, V2 pipeline not wired")
		return nil
	}
	allowModelChange := readBoolSettingValue("dispatch_v2.allow_model_change")
	p := routingExec.NewDispatchPipeline(allowModelChange)
	p.Start()
	routingExec.SetDispatchPipeline(p)
	gatewayDispatchPipeline = p
	slog.Info("dispatch_v2 pipeline wired",
		"allow_model_change", allowModelChange,
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
		"enabled":  dispatch.IsDispatchEnabled(),
		"wired":    gatewayDispatchPipeline != nil,
		"models":   []dispatch.QueueSnapshot{},
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
