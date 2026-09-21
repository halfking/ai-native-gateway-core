package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// serveSessionTurnsUnified is the V2 metadata list with the tree's child
// request projection. The list remains metadata-only; bodies are never encoded.
func (h *Handler) serveSessionTurnsUnified(w http.ResponseWriter, r *http.Request, sessionID string) {
	serveSessionTurnsUnifiedDB(h.db, h.secret, w, r, sessionID)
}

func serveSessionTurnsUnifiedDB(db sessionTurnsDB, secret string, w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := tenantFromQueryOrContext(r)
	limit := defaultTurnsListLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= maxTurnsListLimit {
		limit = n
	}
	before := int(^uint(0) >> 1)
	if c := r.URL.Query().Get("cursor"); c != "" {
		p, err := validateCursor(c, []byte(secret), tenantID, sessionID)
		if err != nil {
			if errors.Is(err, errCursorMismatch) {
				writeError(w, http.StatusBadRequest, "cursor mismatch")
			} else {
				writeError(w, http.StatusBadRequest, "invalid cursor")
			}
			return
		}
		before = p.TurnNo
	}
	rows, err := db.Query(r.Context(), `
        SELECT t.turn_no, t.ts, COALESCE(t.title,''), COALESCE(t.summary,''),
               COALESCE(t.prompt_tokens,0), COALESCE(t.completion_tokens,0), COALESCE(t.cost_usd,0),
               COALESCE(t.model,''), COALESCE(t.provider,''), COALESCE(t.status_code,0),
               COALESCE(t.submit_mode,''), COALESCE(t.injection_verdict,''), COALESCE(t.output_verdict,''),
               COALESCE(t.attachment_count,0), t.request_id,
			   COALESCE(t.cache_read_tokens,0), t.latency_ms, COALESCE(t.success,FALSE),
               t.error_kind, t.compression_applied, t.compression_tokens_saved, t.digest,
               NULL::jsonb, NULL::jsonb
        FROM public.session_turns_with_current_month t
        WHERE t.tenant_id=$1 AND t.session_id=$2 AND t.turn_no < $3
        ORDER BY t.turn_no DESC LIMIT $4`, tenantID, sessionID, before, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query turns failed")
		return
	}
	defer rows.Close()
	items := make([]TurnListItem, 0, limit)
	for rows.Next() {
		var it TurnListItem
		var requestID string
		var errorKind *string
		var cacheRead int
		var latency *int
		var success, compression bool
		var saved *int
		var digestRaw, requestRaw, responseRaw []byte
		if err := rows.Scan(&it.TurnNo, &it.Ts, &it.Title, &it.Summary, &it.RequestTokens, &it.ResponseTokens,
			&it.CostUSD, &it.Model, &it.Provider, &it.StatusCode, &it.SubmitMode, &it.InjectionVerdict,
			&it.OutputVerdict, &it.AttachmentCount, &requestID, &cacheRead, &latency, &success, &errorKind,
			&compression, &saved, &digestRaw, &requestRaw, &responseRaw); err != nil {
			writeError(w, http.StatusInternalServerError, "scan turn failed")
			return
		}
		reqBody := decodeStoredJSON("request_delta", requestID, requestRaw)
		respBody := decodeStoredJSON("response_delta", requestID, responseRaw)
		meta := map[string]any{"prompt_tokens": it.RequestTokens, "completion_tokens": it.ResponseTokens, "cost_usd": it.CostUSD, "latency_ms": intPtrValue(latency), "status_code": it.StatusCode, "success": success, "error_kind": stringPtrValue(errorKind)}
		gov := map[string]any{"submit_mode": it.SubmitMode, "injection_verdict": it.InjectionVerdict, "output_verdict": it.OutputVerdict, "compression_applied": compression, "compression_tokens_saved": intPtrValue(saved)}
		it.RequestID = requestID
		it.LatencyMs = latency
		it.Digest = persistedDigestOrFallback(digestRaw, reqBody, respBody, meta, gov)
		it.ChildRequests = []*SessionChildRequest{}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "iterate turns failed")
		return
	}
	// V2 shadow writes may not exist for older sessions. Read-time fallback keeps
	// the unified route useful without exposing the legacy tree cursor contract.
	if len(items) == 0 && r.URL.Query().Get("cursor") == "" {
		if fallback, ok := unifiedTurnsTreeFallback(r.Context(), db, sessionID, tenantID, limit); ok {
			writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "turns": fallback, "count": len(fallback), "has_more": false, "next_cursor": "", "source": "tree_fallback"})
			return
		}
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	if len(items) > 0 {
		ids := make([]string, 0, len(items))
		index := make(map[string]*TurnListItem, len(items))
		for i := range items {
			ids = append(ids, items[i].RequestID)
			index[items[i].RequestID] = &items[i]
		}
		crows, err := db.Query(r.Context(), `
			WITH ranked_children AS (
				SELECT parent_request_id, request_id, COALESCE(request_status,'') AS request_status, latency_ms,
				       COALESCE(request_type,'main') AS request_type, COALESCE(origin_actor,'') AS origin_actor,
				       ROW_NUMBER() OVER (PARTITION BY parent_request_id ORDER BY ts ASC, request_id ASC) AS child_no
				FROM request_logs_with_current_month
				WHERE tenant_id=$1 AND parent_request_id = ANY($2)
			)
			SELECT parent_request_id, request_id, request_status, latency_ms, request_type, origin_actor
			FROM ranked_children
			WHERE child_no <= `+strconv.Itoa(maxChildRequestsPerParent)+`
			ORDER BY parent_request_id ASC, request_id ASC
			LIMIT `+strconv.Itoa(maxChildRequestsPerPage+1), tenantID, ids)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query child requests failed")
			return
		}
		defer crows.Close()
		for crows.Next() {
			var parent string
			var c SessionChildRequest
			var typ, actor string
			if err := crows.Scan(&parent, &c.RequestID, &c.Status, &c.LatencyMs, &typ, &actor); err != nil {
				writeError(w, http.StatusInternalServerError, "scan child request failed")
				return
			}
			c.RequestType = normalizeChildRequestType(typ, actor)
			if parentItem := index[parent]; parentItem != nil && len(parentItem.ChildRequests) < maxChildRequestsPerParent {
				if totalChildRequests(items) >= maxChildRequestsPerPage {
					break
				}
				parentItem.ChildRequests = append(parentItem.ChildRequests, &c)
			}
		}
		if err := crows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "iterate child requests failed")
			return
		}

	}
	next := ""
	if hasMore && len(items) > 0 {
		next, _ = encodeCursor(cursorPayload{TenantID: tenantID, SessionID: sessionID, TurnNo: items[len(items)-1].TurnNo, TS: time.Now()}, []byte(secret))
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "turns": items, "count": len(items), "has_more": hasMore, "next_cursor": next, "source": "v2"})
}

func totalChildRequests(items []TurnListItem) int {
	total := 0
	for i := range items {
		total += len(items[i].ChildRequests)
	}
	return total
}

func latencyValue(value *int) *int {
	return value
}

func statusCodeForTreeStatus(status string) int {
	switch status {
	case "success", "ok", "completed":
		return http.StatusOK
	case "error", "failed", "timeout":
		return http.StatusInternalServerError
	default:
		return 0
	}
}

func unifiedTurnsTreeFallback(ctx context.Context, db sessionTurnsDB, sessionID, tenantID string, limit int) ([]TurnListItem, bool) {
	result, err := querySessionTurnsTree(ctx, db, sessionTurnsTreeParams{
		SessionID: sessionID,
		TenantID:  tenantID,
		Limit:     limit,
	})
	if err != nil || result.NotFound || result.Forbidden {
		return nil, false
	}
	items := make([]TurnListItem, 0, len(result.Turns))
	for _, turn := range result.Turns {
		if turn == nil {
			continue
		}
		items = append(items, TurnListItem{
			TurnNo:        turn.TurnNumber,
			RequestID:     turn.RequestID,
			Model:         turn.Model,
			StatusCode:    statusCodeForTreeStatus(turn.Status),
			LatencyMs:     latencyValue(turn.LatencyMs),
			ChildRequests: turn.ChildRequests,
		})
	}
	return items, true
}
func sessionTurnsListRouting() string {
	if settings.Global == nil {
		return "tree"
	}
	raw, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, "sessions_v2.turns_list_routing", "")
	if err != nil {
		return "tree"
	}
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return "tree"
	}
	switch v {
	case "dual", "v2":
		return v
	default:
		return "tree"
	}
}

func (h *Handler) handleSessionTurnsListRouted(w http.ResponseWriter, r *http.Request) {
	mode := sessionTurnsListRouting()
	if mode == "v2" || (mode == "dual" && r.URL.Query().Get("source") == "v2") {
		h.serveSessionTurnsUnified(w, r, r.PathValue("id"))
		return
	}
	if mode == "dual" {
		h.handleSessionTurnsDual(w, r)
		return
	}
	h.handleSessionTurnsTree(w, r)
}

func (h *Handler) handleSessionTurnsDual(w http.ResponseWriter, r *http.Request) {
	treeRecorder := httptest.NewRecorder()
	h.handleSessionTurnsTree(treeRecorder, r)
	if treeRecorder.Code < 200 || treeRecorder.Code >= 300 || treeRecorder.Body.Len() == 0 {
		copyRecordedResponse(w, treeRecorder)
		return
	}

	v2Recorder := httptest.NewRecorder()
	shadowCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 500*time.Millisecond)
	defer cancel()
	h.serveSessionTurnsUnified(v2Recorder, r.Clone(shadowCtx), r.PathValue("id"))
	shadow := buildTurnsV2Shadow(treeRecorder.Body.Bytes(), v2Recorder.Body.Bytes(), v2Recorder.Code)

	var payload map[string]any
	if json.Unmarshal(treeRecorder.Body.Bytes(), &payload) != nil {
		copyRecordedResponse(w, treeRecorder)
		return
	}
	payload["v2_shadow"] = shadow
	applyTurnsV2Shadow(payload, v2Recorder.Body.Bytes(), shadow)
	for key, values := range treeRecorder.Header() {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	writeJSON(w, treeRecorder.Code, payload)
}

func copyRecordedResponse(w http.ResponseWriter, r *httptest.ResponseRecorder) {
	for key, values := range r.Header() {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(r.Code)
	_, _ = w.Write(r.Body.Bytes())
}

type turnsV2Shadow struct {
	Status      string   `json:"status"`
	TreeCount   int      `json:"tree_count"`
	V2Count     int      `json:"v2_count"`
	MissingInV2 []string `json:"missing_in_v2,omitempty"`
	ExtraInV2   []string `json:"extra_in_v2,omitempty"`
	StatusCode  int      `json:"v2_status_code"`
}

func applyTurnsV2Shadow(payload map[string]any, v2Raw []byte, summary turnsV2Shadow) {
	// The comparison is attached to each tree turn so consumers can reconcile
	// durable turn numbers and digest availability without parsing aggregate IDs.
	var v2 struct {
		Turns []struct {
			RequestID string          `json:"request_id"`
			TurnNo    int             `json:"turn_no"`
			Digest    json.RawMessage `json:"digest"`
		} `json:"turns"`
	}
	if summary.Status == "unavailable" || json.Unmarshal(v2Raw, &v2) != nil {
		return
	}
	byID := make(map[string]SessionTurnV2Shadow, len(v2.Turns))
	for _, turn := range v2.Turns {
		byID[turn.RequestID] = SessionTurnV2Shadow{
			TurnNo:          turn.TurnNo,
			DigestAvailable: len(turn.Digest) > 0 && string(turn.Digest) != "null",
		}
	}
	rawTurns, ok := payload["turns"].([]any)
	if !ok {
		return
	}
	for _, raw := range rawTurns {
		turn, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		requestID, _ := turn["request_id"].(string)
		if shadow, exists := byID[requestID]; exists {
			turn["v2_shadow"] = shadow
		} else {
			turn["v2_shadow"] = nil
		}
	}
}

func buildTurnsV2Shadow(treeRaw, v2Raw []byte, v2Status int) turnsV2Shadow {
	shadow := turnsV2Shadow{Status: "unavailable", StatusCode: v2Status}
	var tree, v2 struct {
		Turns []struct {
			RequestID string `json:"request_id"`
		} `json:"turns"`
	}
	if json.Unmarshal(treeRaw, &tree) != nil || json.Unmarshal(v2Raw, &v2) != nil || v2Status < 200 || v2Status >= 300 {
		return shadow
	}
	shadow.Status = "match"
	shadow.TreeCount, shadow.V2Count = len(tree.Turns), len(v2.Turns)
	treeIDs, v2IDs := map[string]bool{}, map[string]bool{}
	for _, turn := range tree.Turns {
		treeIDs[turn.RequestID] = true
	}
	for _, turn := range v2.Turns {
		v2IDs[turn.RequestID] = true
	}
	for id := range treeIDs {
		if !v2IDs[id] {
			shadow.MissingInV2 = append(shadow.MissingInV2, id)
		}
	}
	for id := range v2IDs {
		if !treeIDs[id] {
			shadow.ExtraInV2 = append(shadow.ExtraInV2, id)
		}
	}
	if len(shadow.MissingInV2) > 0 || len(shadow.ExtraInV2) > 0 || shadow.TreeCount != shadow.V2Count {
		shadow.Status = "mismatch"
	}
	return shadow
}
