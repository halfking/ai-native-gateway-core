package taskprofile

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// handler.go — admin API for the task-profile module.
//
// Endpoints (all under /api/admin/task-profile, registered by
// RegisterTaskProfileRoutes with the same middleware tier as the P2.1
// annotation endpoints):
//
//	GET  /api/admin/task-profile                     — consolidated registry
//	                                                  view (profiles + live
//	                                                  correction stats)
//	POST /api/admin/task-profile/corrections         — record one human
//	                                                  task-type correction
//	GET  /api/admin/task-profile/corrections/stats   — per-task stats
//	                                                  (+ recent corrections)
//	GET  /api/admin/task-profile/corrections/export  — CSV export
//	                                                  (formula-injection-safe)
//	POST /api/admin/task-profile/corrections/import  — CSV import (idempotent,
//	                                                  ≤10000 rows, 32MB cap)
//	POST /api/admin/task-profile/apply-tier-config   — write suggestions into
//	                                                  task_type_tier_config
//	                                                  (explicit operator action)
//	POST /api/admin/task-profile/reload              — re-apply the overlay
//	                                                  file (independent
//	                                                  upgrade operation)

// OverlayEnvVar names the environment variable holding the overlay file
// path. Kept here so cmd/gateway and the reload endpoint agree on the
// source of truth.
const OverlayEnvVar = "TASKPROFILE_OVERLAY"

// AuditEvent describes one operator mutation attempt on the task-profile
// module's four change endpoints (corrections create / import / reload /
// apply-tier-config), delivered to the optional audit hook.
//
// R43 §五#3 发现（apply-tier-config 零审计留痕）→ 本回调落地（R45 补做，
// 交接第 5 项）。设计约束：taskprofile 不 import admin（admin 依赖
// taskprofile，反向会成环）——审计 sink 由接线侧（admin/handler.go）经
// SetAuditHook 注入，event 携带原始 *http.Request 供 sink 提取操作者身份
// （admin.GetAuthContext）。首个生产 sink 是结构化 slog（journald/日志管道
// 可检索）；未来可换 DB 审计表 sink 而无需再动本包。
//
// 语义约定：
//   - 只有"真实变更尝试"才发事件——校验拒绝（400）与 nil-pool 503 不发
//     （没有变更意图落库，不属于审计面）。例外（R47 契约精确化）：
//     corrections-import 的 body 级失败（CSV 格式/表头错、32MB 超限）也投
//     failure——导入的载荷本身即变更内容，body 解析失败属"真实尝试被拒"，
//     与 create/apply 的 JSON 字段校验拒绝不同层；
//   - success 与 failure 都发（失败的操作同样需要留痕：谁在何时试了什么）；
//   - hook panic 隔离（审计绝不能打断端点），nil hook 零开销。
type AuditEvent struct {
	// Action 是端点级动作名：corrections-create / corrections-import /
	// reload / apply-tier-config。
	Action string
	// Outcome: "success" | "failure"。
	Outcome string
	// Detail 携带端点特定的结果摘要（计数/版本/错误串）；可为 nil。
	Detail map[string]any
	// Request 是触发端点的原始请求；sink 从中提取 actor/tenant。变更端点
	// 的 handler 路径上恒非 nil。
	// R46 F8⑨ 契约约束：sink 禁止记录原始请求的 headers/body（Authorization
	// 头会带出会话凭据）——只允许提取鉴权上下文字段。
	Request *http.Request
}

// 审计动作名（AuditEvent.Action 取值，接线侧与查询侧共用同一词表）。
const (
	AuditActionCorrectionCreate = "corrections-create"
	AuditActionCorrectionImport = "corrections-import"
	AuditActionReload           = "reload"
	AuditActionApplyTierConfig  = "apply-tier-config"
)

// Handlers wires the admin endpoints to a store.
type Handlers struct {
	store     *CorrectionStore
	auditHook func(AuditEvent)
}

// NewHandlers constructs the admin handlers over pool (may be nil → the
// endpoints answer 503, same convention as the annotation handlers).
func NewHandlers(pool *pgxpool.Pool) *Handlers {
	return &Handlers{store: NewCorrectionStore(pool)}
}

// SetAuditHook attaches the optional operator-mutation audit sink. See
// AuditEvent for the delivery semantics. The hook must be cheap and
// non-blocking; panics inside it are isolated and never break the endpoint.
func (h *Handlers) SetAuditHook(fn func(AuditEvent)) { h.auditHook = fn }

// emitAudit delivers one audit event. nil hook / nil receiver = no-op; hook
// panics are isolated with a Warn (mirrors telemetry firePersistedHooks).
func (h *Handlers) emitAudit(action, outcome string, detail map[string]any, r *http.Request) {
	if h == nil || h.auditHook == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			slog.Warn("taskprofile audit hook panic",
				"action", action, "outcome", outcome, "panic", rec)
		}
	}()
	h.auditHook(AuditEvent{Action: action, Outcome: outcome, Detail: detail, Request: r})
}

// SetRecorder attaches the optional FeedbackRecorder to the handlers' store.
// R43 (2026-09-18): this is the ONLY production write path into the
// corrections store (POST /corrections + CSV import), so the recorder must
// be attached HERE — the routingopt side only ever reads CorrectionStats,
// which never fires RecordFeedback. Guard test:
// TestAdminWiring_AttachesFeedbackRecorder.
func (h *Handlers) SetRecorder(r FeedbackRecorder) { h.store.SetRecorder(r) }

// RegisterTaskProfileRoutes registers the endpoints on mux behind the given
// admin middleware wrapper.
func (h *Handlers) RegisterTaskProfileRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/admin/task-profile", wrap(h.handleProfile))
	mux.HandleFunc("POST /api/admin/task-profile/corrections", wrap(h.handleCreateCorrection))
	mux.HandleFunc("GET /api/admin/task-profile/corrections/stats", wrap(h.handleCorrectionStats))
	mux.HandleFunc("GET /api/admin/task-profile/corrections/export", wrap(h.handleExportCorrections))
	mux.HandleFunc("POST /api/admin/task-profile/corrections/import", wrap(h.handleImportCorrections))
	mux.HandleFunc("POST /api/admin/task-profile/apply-tier-config", wrap(h.handleApplyTierConfig))
	mux.HandleFunc("POST /api/admin/task-profile/reload", wrap(h.handleReload))
}

// handleProfile returns the consolidated view: registry version + profiles +
// current correction stats merged per task type (the "one module" answer to
// "task type identification + suggested tier" data).
func (h *Handlers) handleProfile(w http.ResponseWriter, r *http.Request) {
	if !h.ensurePool(w) {
		return
	}
	ctx := r.Context()
	version, profiles := Snapshot()

	stats, err := h.store.Stats(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		http.Error(w, "query correction stats: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type profileView struct {
		TaskProfile
		CorrectionStats *CorrectionStat `json:"correction_stats,omitempty"`
		Suggestion      Suggestion      `json:"suggestion"`
	}
	views := make([]profileView, 0, len(profiles))
	for _, p := range profiles {
		v := profileView{TaskProfile: p}
		if s, ok := stats[p.TaskType]; ok {
			sCopy := s
			v.CorrectionStats = &sCopy
		}
		// Representative suggestion at confidence 1.0 isolates the
		// correction-driven tier escalation (any lower value would conflate
		// the confidence-escalation rule into the view).
		v.Suggestion = Suggest(p.TaskType, 1.0, stats)
		views = append(views, v)
	}

	writeJSON(w, map[string]any{
		"registry_version": version,
		"schema_version":   SchemaVersion,
		"task_types":       TaskTypes(),
		"profiles":         views,
	})
}

// createCorrectionRequest is the POST /corrections body.
type createCorrectionRequest struct {
	RequestID     string `json:"request_id"`
	HumanTaskType string `json:"human_task_type"`
	Annotator     string `json:"annotator"`
	Reason        string `json:"reason"`
}

// handleCreateCorrection records one human task-type correction.
func (h *Handlers) handleCreateCorrection(w http.ResponseWriter, r *http.Request) {
	if !h.ensurePool(w) {
		return
	}
	var req createCorrectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.RequestID == "" || req.HumanTaskType == "" || req.Annotator == "" || req.Reason == "" {
		http.Error(w, "missing required fields: request_id, human_task_type, annotator, reason", http.StatusBadRequest)
		return
	}
	if !IsValidTaskType(req.HumanTaskType) {
		http.Error(w, "unknown human_task_type; valid: "+strconv.Quote(TaskTypesCSV()), http.StatusBadRequest)
		return
	}
	if !IsValidReason(req.Reason) {
		http.Error(w, "invalid reason; valid: "+strconv.Quote(reasonsCSV()), http.StatusBadRequest)
		return
	}

	correction, err := h.store.Record(r.Context(), CreateCorrectionInput{
		RequestID:     req.RequestID,
		HumanTaskType: req.HumanTaskType,
		Annotator:     req.Annotator,
		Reason:        req.Reason,
	})
	if err != nil {
		// Record 尝试后的失败（未知请求/重复修正/DB 错）都是真实审计面：
		// 谁、何时、试图把哪条请求改成什么类型。
		h.emitAudit(AuditActionCorrectionCreate, "failure",
			map[string]any{
				"error":           err.Error(),
				"request_id":      req.RequestID,
				"human_task_type": req.HumanTaskType,
				"annotator":       req.Annotator,
			}, r)
	} else {
		h.emitAudit(AuditActionCorrectionCreate, "success",
			map[string]any{
				"request_id":      req.RequestID,
				"human_task_type": req.HumanTaskType,
				"annotator":       req.Annotator,
			}, r)
	}
	switch {
	case err == nil:
		writeJSON(w, map[string]any{"success": true, "correction": correction})
	case errors.Is(err, ErrUnknownRequest):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrAlreadyCorrected):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "record correction: "+err.Error(), http.StatusInternalServerError)
	}
}

// handleCorrectionStats answers with per-task stats and the recent feed.
func (h *Handlers) handleCorrectionStats(w http.ResponseWriter, r *http.Request) {
	if !h.ensurePool(w) {
		return
	}
	since := time.Now().Add(-30 * 24 * time.Hour)
	if v := r.URL.Query().Get("since_days"); v != "" {
		days, err := strconv.Atoi(v)
		if err != nil || days <= 0 || days > 365 {
			http.Error(w, "since_days must be an integer in [1,365]", http.StatusBadRequest)
			return
		}
		since = time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	}
	limit := 100
	if v := r.URL.Query().Get("recent_limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 500 {
			http.Error(w, "recent_limit must be an integer in [1,500]", http.StatusBadRequest)
			return
		}
		limit = n
	}

	stats, err := h.store.Stats(r.Context(), since)
	if err != nil {
		http.Error(w, "query stats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	recent, err := h.store.Recent(r.Context(), limit)
	if err != nil {
		http.Error(w, "query recent: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Suggestions per corrected task type at a neutral confidence (0.75):
	// shows the escalation the stats currently drive.
	suggestions := make(map[string]Suggestion, len(stats))
	for taskType := range stats {
		suggestions[taskType] = Suggest(taskType, 0.75, stats)
	}

	writeJSON(w, map[string]any{
		"since":       since.UTC().Format(time.RFC3339),
		"stats":       stats,
		"suggestions": suggestions,
		"recent":      recent,
	})
}

// handleExportCorrections streams corrections as CSV (P2.1-style offline
// annotation loop: export → human review → import).
func (h *Handlers) handleExportCorrections(w http.ResponseWriter, r *http.Request) {
	if !h.ensurePool(w) {
		return
	}
	since := time.Now().Add(-30 * 24 * time.Hour)
	if v := r.URL.Query().Get("since_days"); v != "" {
		days, err := strconv.Atoi(v)
		if err != nil || days <= 0 || days > 365 {
			http.Error(w, "since_days must be an integer in [1,365]", http.StatusBadRequest)
			return
		}
		since = time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	}
	limit := 10000
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 50000 {
			http.Error(w, "limit must be an integer in [1,50000]", http.StatusBadRequest)
			return
		}
		limit = n
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="task-type-corrections-`+time.Now().UTC().Format("20060102")+".csv\"")
	if _, err := h.store.ExportCorrectionsCSV(r.Context(), w, since, limit); err != nil {
		// Headers may already be written; the truncated body signals failure.
		http.Error(w, "export: "+err.Error(), http.StatusInternalServerError)
	}
}

// handleImportCorrections ingests a corrections CSV (raw text/csv body).
// Response reports imported/skipped/per-row errors; existing request_ids are
// skipped so re-import is idempotent.
func (h *Handlers) handleImportCorrections(w http.ResponseWriter, r *http.Request) {
	if !h.ensurePool(w) {
		return
	}
	if r.ContentLength == 0 {
		http.Error(w, "empty body: POST the CSV text with Content-Type: text/csv", http.StatusBadRequest)
		return
	}
	// R43 (2026-09-18): cap the body — csv.Reader buffers whole records, so
	// an authenticated user could otherwise POST a multi-GB single line
	// (10k-row cap alone doesn't bound record size). 32MB ≫ any real
	// 10k-row export of 9 short columns.
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	summary, err := h.store.ImportCorrectionsCSV(r.Context(), r.Body, 10000)
	if err != nil {
		h.emitAudit(AuditActionCorrectionImport, "failure",
			map[string]any{"error": err.Error()}, r)
		http.Error(w, "import: "+err.Error(), http.StatusBadRequest)
		return
	}
	h.emitAudit(AuditActionCorrectionImport, "success",
		map[string]any{
			"total_rows": summary.TotalRows,
			"imported":   summary.Imported,
			"skipped":    summary.Skipped,
			"row_errors": len(summary.RowErrors),
		}, r)
	writeJSON(w, map[string]any{"success": true, "summary": summary})
}

// applyTierConfigRequest is the POST /apply-tier-config body.
type applyTierConfigRequest struct {
	// TaskTypes optionally restricts the apply set; empty applies every type
	// whose current suggestion is correction-driven escalation.
	TaskTypes []string `json:"task_types"`
}

// handleApplyTierConfig writes correction-driven tier suggestions into
// task_type_tier_config. Explicit operator action by design — see
// taskprofile/csv.go ApplySuggestions. R43 note: the table currently has no
// runtime reader (autoroute.NewTierSelector is not constructed anywhere in
// production; design doc §5.2 keeps consumption as a future opt-in), so this
// endpoint persists operator-approved suggestions without changing routing.
func (h *Handlers) handleApplyTierConfig(w http.ResponseWriter, r *http.Request) {
	if !h.ensurePool(w) {
		return
	}
	var req applyTierConfigRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	applied, err := h.store.ApplySuggestions(r.Context(), req.TaskTypes)
	if err != nil {
		if ErrTierConfigMissing(err) {
			// R43: ensureTaskTypeTierConfig (db.go) creates the table at
			// startup, so a missing table means the ensure chain itself is
			// disabled/broken — not "run the migration" (the original
			// 202609_02 file was unexecutable PG DDL; fixed in R43).
			h.emitAudit(AuditActionApplyTierConfig, "failure",
				map[string]any{"error": "tier_config_table_missing", "task_types": req.TaskTypes}, r)
			http.Error(w, "task_type_tier_config table not available (startup ensure chain did not create it; see db.ensureTaskTypeTierConfig)", http.StatusServiceUnavailable)
			return
		}
		h.emitAudit(AuditActionApplyTierConfig, "failure",
			map[string]any{"error": err.Error(), "task_types": req.TaskTypes}, r)
		http.Error(w, "apply tier config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.emitAudit(AuditActionApplyTierConfig, "success",
		map[string]any{"applied": applied, "task_types": req.TaskTypes}, r)
	writeJSON(w, map[string]any{"success": true, "applied": applied})
}

// handleReload re-applies the overlay file (or resets to defaults when
// TASKPROFILE_OVERLAY is unset). This is the module's independent-upgrade
// operation: profile data changes without a redeploy.
func (h *Handlers) handleReload(w http.ResponseWriter, r *http.Request) {
	overlayPath := os.Getenv(OverlayEnvVar)
	version, err := ReloadOverlay(overlayPath)
	if err != nil {
		// R46 F8⑧: detail 记实际 overlay 路径（仅路径，无敏感风险），
		// 恒为 env 名时取证不知道坏的是哪个文件。
		h.emitAudit(AuditActionReload, "failure",
			map[string]any{"error": err.Error(), "overlay_env": OverlayEnvVar, "overlay_path": overlayPath}, r)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.emitAudit(AuditActionReload, "success",
		map[string]any{"registry_version": version}, r)
	writeJSON(w, map[string]any{"success": true, "registry_version": version})
}

func (h *Handlers) ensurePool(w http.ResponseWriter) bool {
	if h.store.Pool() == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
