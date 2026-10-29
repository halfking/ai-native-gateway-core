package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

const sanitizeRulesPath = "/api/admin/sanitize-rules"

func (h *Handler) registerSanitizeRuleRoutes(mux *http.ServeMux) {
	mux.HandleFunc(sanitizeRulesPath, h.admin(h.handleSanitizeRules))
	mux.HandleFunc(sanitizeRulesPath+"/", h.admin(h.handleSanitizeRules))
}

// handleSanitizeRules authorizes each operation after the shared JWT middleware.
// AdminMiddleware authenticates ordinary users as well, so role checks belong here.
func (h *Handler) handleSanitizeRules(w http.ResponseWriter, r *http.Request) {
	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, 401, "authentication required")
		return
	}
	super := IsSuperAdminOrLegacy(r)
	path := strings.TrimPrefix(r.URL.Path, sanitizeRulesPath)
	eventRead := path == "/events"
	eventReview := strings.HasPrefix(path, "/events/") && strings.HasSuffix(path, "/review")
	// r1039（D4）：租户管理员要能**看**自己租户的放行情况（「为什么我被拦了」），
	// 但**写**（授予/撤销放行）只给超管 —— 判定在 handleEgressReleases 内部。
	egressReleaseRead := path == "/egress-releases"
	// 2026-10-28 拦截审核轮：租户管理员可读自己租户的拦截审核队列；批准/拒绝
	// 与硬阻断清单读写仍只给超管（判定在各 handler 内部）。
	//
	// ★ 2026-10-29 输出合规误报轮：读面扩到「规则级复核读数」
	//（/blocked-reviews/insights）—— 租户管理员同样能看自己租户的误报分布
	//（那是「我为什么老被拦」的答案）。
	blockedReviewRead := path == "/blocked-reviews" || path == "/blocked-reviews/" ||
		path == "/blocked-reviews/insights"
	if !super && !(auth.Role == "tenant_admin" && auth.TenantID != "" && (eventRead || eventReview || egressReleaseRead || blockedReviewRead)) {
		writeError(w, 403, "insufficient permission for rule management")
		return
	}
	if path == "/egress-releases" {
		// 放行表不依赖规则管理器 ⇒ 放在这个 503 之前。
		h.handleEgressReleases(w, r, auth)
		return
	}
	if path == "/hardblock" {
		// 硬阻断清单同样不依赖规则管理器（进程内清单）。
		h.handleHardBlockRules(w, r)
		return
	}
	if path == "/output-check" {
		// ★ 2026-10-29 输出合规误报轮：输出合规**全局开关**的专属入口
		//（写的是同一个设置键 sanitize.output_check_enabled，不另立真值）。
		h.handleOutputCheckSwitch(w, r)
		return
	}
	if strings.HasPrefix(path, "/blocked-reviews") {
		h.handleBlockedReviews(w, r, auth)
		return
	}
	if h.sanitizeRuleManager == nil {
		writeError(w, 503, "sanitize rule management unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	switch path {
	case "", "/":
		if !sanitizeRuleMethod(w, r, http.MethodGet) {
			return
		}
		snap := h.sanitizeRuleManager.Current()
		// ★ 2026-10-29 输出合规误报轮：★ 补上规则**说明**（★ 事实源 = YAML +
		// 内建目录；★ 历史规则快照里没有说明 ⇒ 不补的话管理面只能显示规则 ID）。
		writeJSON(w, 200, map[string]any{"version": snap.Version, "config": sanitizerules.ApplyRuleDescriptions(snap.Config()), "config_digest": snap.ConfigDigest, "status": h.sanitizeRuleManager.Status()})
	case "/validate":
		if !sanitizeRuleMethod(w, r, http.MethodPost) {
			return
		}
		var cfg sanitizerules.Config
		if !decodeSanitizeRuleBody(w, r, &cfg) {
			return
		}
		snap, err := sanitizerules.Compile(cfg, 0)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"valid": true, "config_digest": snap.ConfigDigest})
	case "/publish":
		h.publishSanitizeRules(w, r)
	case "/rollback":
		if !sanitizeRuleMethod(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Base    *int64 `json:"base_version"`
			Target  *int64 `json:"target_version"`
			Comment string `json:"comment"`
		}
		if !decodeSanitizeRuleBody(w, r, &req) {
			return
		}
		if req.Base == nil || *req.Base < 0 || req.Target == nil || *req.Target < 1 {
			writeError(w, 400, "base_version and published target_version are required")
			return
		}
		v, err := h.sanitizeRuleManager.Rollback(ctx, *req.Base, *req.Target, sanitizeRuleActor(auth), req.Comment)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, 200, v)
	case "/versions":
		if !sanitizeRuleMethod(w, r, http.MethodGet) {
			return
		}
		limit, ok := sanitizeRuleLimit(w, r)
		if !ok {
			return
		}
		versions, err := h.sanitizeRuleManager.Store().ListVersions(ctx, limit)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"versions": versions})
	case "/events":
		h.listSanitizeRuleEvents(w, r)
	case "/instances":
		if !sanitizeRuleMethod(w, r, http.MethodGet) {
			return
		}
		instances, err := h.sanitizeRuleManager.Store().ListInstances(ctx)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		target, err := h.sanitizeRuleManager.Store().Current(ctx)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"target_version": target.ID, "instances": instances})
	default:
		if eventReview {
			h.reviewSanitizeRuleEvent(w, r)
			return
		}
		writeError(w, 404, "sanitize rule endpoint not found")
	}
}
func (h *Handler) publishSanitizeRules(w http.ResponseWriter, r *http.Request) {
	if !sanitizeRuleMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		Base           *int64               `json:"base_version"`
		Config         sanitizerules.Config `json:"config"`
		Comment        string               `json:"comment"`
		ReviewEventIDs []int64              `json:"review_event_ids"`
	}
	if !decodeSanitizeRuleBody(w, r, &req) {
		return
	}
	if req.Base == nil || *req.Base < 0 || len(req.ReviewEventIDs) > 100 {
		writeError(w, 400, "valid base_version and at most 100 review events are required")
		return
	}
	links := make([]sanitizerules.ReviewLink, 0, len(req.ReviewEventIDs))
	seen := map[int64]bool{}
	for _, id := range req.ReviewEventIDs {
		if id < 1 || seen[id] {
			writeError(w, 400, "review event IDs must be positive and unique")
			return
		}
		seen[id] = true
		e, err := h.sanitizeRuleManager.Store().GetEvent(r.Context(), id)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		if e.Status == "pending" {
			writeError(w, 400, "review events must be reviewed before publication")
			return
		}
		links = append(links, sanitizerules.ReviewLink{ID: id, TenantID: e.TenantID})
	}
	v, err := h.sanitizeRuleManager.Publish(r.Context(), *req.Base, req.Config, sanitizeRuleActor(GetAuthContext(r)), req.Comment, links...)
	if err != nil {
		writeSanitizeRuleError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (h *Handler) listSanitizeRuleEvents(w http.ResponseWriter, r *http.Request) {
	if !sanitizeRuleMethod(w, r, http.MethodGet) {
		return
	}
	limit, ok := sanitizeRuleLimit(w, r)
	if !ok {
		return
	}
	f := sanitizerules.EventFilter{Status: r.URL.Query().Get("status"), Action: r.URL.Query().Get("action"), Limit: limit}
	if IsTenantAdmin(r) {
		f.TenantID = GetAuthContext(r).TenantID
	}
	if f.Status != "" && f.Status != "pending" && f.Status != "confirmed_sensitive" && f.Status != "false_positive" && f.Status != "ignored" {
		writeError(w, 400, "invalid event status filter")
		return
	}
	if f.Action != "" && f.Action != "mask" && f.Action != "block" && f.Action != "log" && f.Action != "exempted" && f.Action != "pending" {
		writeError(w, 400, "invalid event action filter")
		return
	}
	events, err := h.sanitizeRuleManager.Store().ListEvents(r.Context(), f)
	if err != nil {
		writeSanitizeRuleError(w, err)
		return
	}
	// Enrich with matched_rule snapshots so reviewers (incl. tenant_admin)
	// can confirm and correct without loading the full rules editor.
	writeJSON(w, 200, map[string]any{"events": enrichSanitizeRuleEvents(r.Context(), h.sanitizeRuleManager.Store(), events)})
}
func (h *Handler) reviewSanitizeRuleEvent(w http.ResponseWriter, r *http.Request) {
	if !sanitizeRuleMethod(w, r, http.MethodPost) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, sanitizeRulesPath+"/events/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "review" {
		writeError(w, 404, "sanitize rule endpoint not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid event ID")
		return
	}
	var req struct {
		Status  string `json:"status"`
		Comment string `json:"comment"`
	}
	if !decodeSanitizeRuleBody(w, r, &req) {
		return
	}
	auth := GetAuthContext(r)
	tenant := auth.TenantID
	if IsSuperAdminOrLegacy(r) {
		e, err := h.sanitizeRuleManager.Store().GetEvent(r.Context(), id)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		tenant = e.TenantID
	}
	event, err := h.sanitizeRuleManager.Store().ReviewEvent(r.Context(), tenant, id, req.Status, sanitizeRuleActor(auth), req.Comment)
	if err != nil {
		writeSanitizeRuleError(w, err)
		return
	}
	writeJSON(w, 200, event)
}
func sanitizeRuleActor(auth *AuthContext) string {
	if auth.Username != "" {
		return auth.Username
	}
	if auth.CanonicalUserID != "" {
		return auth.CanonicalUserID
	}
	if auth.UserID > 0 {
		return "user:" + strconv.Itoa(auth.UserID)
	}
	return "admin_key"
}
func sanitizeRuleMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeError(w, 405, "method not allowed")
	return false
}
func sanitizeRuleLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return 100, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 1000 {
		writeError(w, 400, "limit must be within 1..1000")
		return 0, false
	}
	return limit, true
}
func writeSanitizeRuleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sanitizerules.ErrInvalid):
		writeError(w, 400, err.Error())
	case errors.Is(err, sanitizerules.ErrConflict):
		writeError(w, 409, "sanitize rule version changed; reload the current version")
	case errors.Is(err, sanitizerules.ErrNotFound):
		writeError(w, 404, "sanitize rule record not found")
	default:
		writeError(w, 503, "sanitize rule storage unavailable")
	}
}
func decodeSanitizeRuleBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		writeError(w, 400, "invalid rule request body (maximum 2 MiB)")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(dst); err != nil {
		writeError(w, 400, "invalid rule request body")
		return false
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		writeError(w, 400, "request must contain one JSON object")
		return false
	}
	return true
}
