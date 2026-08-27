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
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	streaming "github.com/kaixuan/llm-gateway-go/domains/streaming" //nolint:depguard
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

// gatewayActionBridge (会话优化 v4 T4/R3.2, FR-3 操作事件思考帧桥接) 是
// 共享的 liveactions → 客户端思考帧桥。源 = gatewayLiveActionsEmitter 的
// 进程内订阅，目标 = 共享 connectionRegistry（在 main.go 顶部构造）。
// 运营开关 llmgw_action_bridge_enabled 默认 false（灰阶上线），走
// settings_kv 热更新无需重启即可开启；nil 表示未装配（降级为 no-op）。
var gatewayActionBridge *streaming.ActionBridge

var gatewayRequestJourneySink dispatch.ObservationSink
var gatewayMinuteStats *dispatch.MinuteStatsAggregator

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

// handleDispatchWaterfallByRequest serves
// GET /api/admin/dispatch/waterfall/request/{request_id}
func handleDispatchWaterfallByRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const prefix = "/api/admin/dispatch/waterfall/request/"
	requestID := strings.TrimPrefix(r.URL.Path, prefix)
	requestID = strings.Trim(requestID, "/")
	if requestID == "" || strings.Contains(requestID, "/") {
		http.Error(w, "request_id required", http.StatusBadRequest)
		return
	}
	tenantID := admin.EffectiveTenantIDAll(r)
	var mem dispatch.WaterfallRequest
	memOK := false
	if projection := gatewayQueueProjection.Load(); projection != nil {
		mem, memOK = projection.FindWaterfallByRequestID(requestID, tenantID)
	}
	item, source, ok := resolveWaterfallByRequest(r.Context(), mem, memOK, requestID, tenantID)
	if !ok {
		http.Error(w, "waterfall request not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"request": item,
		"source":  source,
	})
}

// handleDispatchMinuteStats serves the Redis-backed immediate operational
// projection. Persistent financial reporting remains under /api/admin/stats.
func handleDispatchMinuteStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if gatewayMinuteStats == nil {
		http.Error(w, "minute stats projection unavailable", http.StatusServiceUnavailable)
		return
	}
	bucket := time.Now().UTC()
	if raw := strings.TrimSpace(r.URL.Query().Get("bucket")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "bucket must be RFC3339", http.StatusBadRequest)
			return
		}
		bucket = parsed
	}
	stats, err := gatewayMinuteStats.List(r.Context(), bucket)
	if err != nil {
		http.Error(w, "minute stats unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"bucket": bucket.UTC().Truncate(time.Minute), "items": stats})
}

// handleDispatchDimensions serves GET /api/admin/dispatch/dimensions — the
// per-dimension request membership index (v6 G-Ⅳ, 分维队列). Requests stay in
// their model/credential/provider rings after completion until TTL/capacity
// eviction, answering "which requests ran or are waiting on this node".
//
// Query:
//   - kind: model|credential|provider (default model)
//   - id:   dimension id (model name / credential id / provider id)
//   - limit (default 50, max 200)
//   - no id → returns the per-dimension key summary with live entry counts
func handleDispatchDimensions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pipeline := gatewayDispatchPipeline
	if pipeline == nil || pipeline.DimensionIndex() == nil {
		http.Error(w, "dispatch pipeline not wired", http.StatusServiceUnavailable)
		return
	}
	index := pipeline.DimensionIndex()
	q := r.URL.Query()
	kind := dispatch.DimensionKind(strings.TrimSpace(q.Get("kind")))
	switch kind {
	case dispatch.DimensionModel, dispatch.DimensionCredential, dispatch.DimensionProvider:
	default:
		http.Error(w, "kind must be model|credential|provider", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(q.Get("id"))
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	w.Header().Set("Content-Type", "application/json")
	if id == "" {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind":       kind,
			"dimensions": index.Dimensions()[kind],
		})
		return
	}
	snap := index.Snapshot(kind, id, limit)
	if snap.Entries == nil {
		snap.Entries = []dispatch.DimensionEntry{}
	}
	_ = json.NewEncoder(w).Encode(snap)
}

// handleDispatchRequestDimensions serves
// GET /api/admin/dispatch/request-dimensions/{request_id} (V6-W1.6 R10,
// scope-corrected 2026-08-27): the request's dimension MEMBERSHIP entries
// (model/credential/provider + state/outcome/last action). The execution
// trace (AttemptJournal) is attached to the request itself — post-hoc path
// queries go to the request's own journey projection, NOT this endpoint.
func handleDispatchRequestDimensions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const prefix = "/api/admin/dispatch/request-dimensions/"
	requestID := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if requestID == "" || strings.Contains(requestID, "/") {
		http.Error(w, "request_id required", http.StatusBadRequest)
		return
	}
	pipeline := gatewayDispatchPipeline
	if pipeline == nil || pipeline.DimensionIndex() == nil {
		http.Error(w, "dispatch pipeline not wired", http.StatusServiceUnavailable)
		return
	}
	entries, ok := pipeline.DimensionIndex().EntriesByRequest(requestID)
	if !ok {
		http.Error(w, "request not found in dimension index (TTL window expired or unknown request)", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"request_id": requestID,
		"entries":    entries,
	})
}
