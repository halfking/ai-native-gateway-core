package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/redis/go-redis/v9"
)

// request_actions.go — 会话优化 v4 §13.5（T4/T9）
//
//	GET /api/admin/requests/{id}/actions
//
// Short-term REST fallback for the request action timeline (frontend
// ActionTimeline currently consumes the live SSE only; a page refresh or a
// request that already finished has no replay without this endpoint).
//
// Source: the bounded liveactions Redis LIST (llmgw:live:actions, LPUSH+LTRIM
// 5000) — the same read contract as the admin live-stream hub. Events are
// filtered by request_id and deduplicated by (request_id, seq) so the SSE
// initial_data replay and this endpoint compose without double rows.
// Long-term history stays in request journey (§13.5: this endpoint must not
// scan Redis as the long-term solution).

// requestActionsScanTimeout bounds one Redis read.
const requestActionsScanTimeout = 2 * time.Second

// handleRequestActions serves the request action history fallback.
func (h *Handler) handleRequestActions(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("id")
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "missing request id")
		return
	}
	rc := h.liveActionsRedisClient()
	if rc == nil {
		writeError(w, http.StatusServiceUnavailable, "live actions store not wired")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestActionsScanTimeout)
	defer cancel()

	entries, err := rc.LRange(ctx, liveactions.RedisKey, 0, liveactions.RedisMaxLen-1).Result()
	if err != nil {
		slog.Debug("request actions: redis read failed", "request_id", requestID, "err", err.Error())
		writeError(w, http.StatusBadGateway, "live actions store unavailable")
		return
	}

	// Filter + (request_id, seq) dedup. Node-dimension rows (empty id) are
	// skipped by construction; seq collisions across process restarts keep
	// the first occurrence (list head = newest first scan order).
	actions := make([]map[string]any, 0, 16)
	seen := make(map[int64]struct{}, 16)
	for _, raw := range entries {
		ev, ok := decodeStoredAction(raw)
		if !ok || ev.RequestID != requestID {
			continue
		}
		if _, dup := seen[ev.Seq]; dup {
			continue
		}
		seen[ev.Seq] = struct{}{}
		actions = append(actions, flattenActionEvent(ev))
	}
	// Redis LIST is newest-first (LPUSH); serve oldest → newest (replay
	// contract order, live_stream_lifecycle.go actionLess).
	sort.SliceStable(actions, func(i, j int) bool {
		return actionEntryLess(actions[i], actions[j])
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id": requestID,
		"actions":    actions,
		"count":      len(actions),
	})
}

// decodeStoredAction unmarshals one stored list entry (json ActionEvent).
func decodeStoredAction(raw string) (liveactions.ActionEvent, bool) {
	var ev liveactions.ActionEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return ev, false
	}
	if ev.Action == "" {
		return ev, false
	}
	return ev, true
}

// actionEntryLess orders two flattened entries by (ts, seq) — mirrors
// actionLess for the map shape produced by flattenActionEvent.
func actionEntryLess(a, b map[string]any) bool {
	at, aok := a["ts"].(string)
	bt, bok := b["ts"].(string)
	if aok && bok && at != bt {
		return at < bt
	}
	af, _ := a["seq"].(float64)
	bf, _ := b["seq"].(float64)
	return af < bf
}

// liveActionsRedisClient resolves the Redis client backing liveactions:
// the handler's wired client (SetRedisClient) or the live-stream hub's
// config client. nil when Redis is not wired at all.
func (h *Handler) liveActionsRedisClient() *redis.Client {
	if h.redisClient != nil {
		if rc, ok := h.redisClient.(*redis.Client); ok {
			return rc
		}
	}
	if h.liveStreamHub != nil {
		return h.liveStreamHub.cfg.RedisClient
	}
	return nil
}
