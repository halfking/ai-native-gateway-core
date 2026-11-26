package metricsstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

type projectionReadiness struct {
	Source           string     `json:"source"`
	BackfillComplete bool       `json:"backfill_complete"`
	CheckedAt        *time.Time `json:"checked_at"`
	UpdatedAt        *time.Time `json:"updated_at"`
	PendingSince     *time.Time `json:"pending_since"`
	Stale            bool       `json:"stale"`
	Reason           string     `json:"reason,omitempty"`
}

// projectionStaleThreshold 是「投影器停摆」的判定阈值：水位 60 分钟没动过、
// 或同一条 pending 积压超过 60 分钟没被消化 ⇒ 判 stale、降 503。
// （★ 2026-11-26 12h 审计轮 P2-2：原先只看 backfill_complete && updated_at
//   ⇒ 投影器停摆 / pending 无界积压时长期 200 假绿。）
const projectionStaleThreshold = 60 * time.Minute

// projectionIsStale 判「水位是否停滞」：
//   - updated_at 距今超过阈值（投影器一轮都没跑成）；
//   - 或 pending 积压头入队时间距今超过阈值（积压没人消化）。
func projectionIsStale(now time.Time, updatedAt, pendingSince *time.Time) (bool, string) {
	if updatedAt != nil && now.Sub(*updatedAt) > projectionStaleThreshold {
		return true, "projection watermark not advanced for over 60m"
	}
	if pendingSince != nil && now.Sub(*pendingSince) > projectionStaleThreshold {
		return true, "pending backlog older than 60m not consumed"
	}
	return false, ""
}

// ProjectionReadinessHandler reports the shared projection watermark without
// aggregating any requests. The gateway protects it with the ops token.
func ProjectionReadinessHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		state := projectionReadiness{Source: "request_metrics"}
		status := http.StatusServiceUnavailable
		if db != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			err := db.QueryRowContext(ctx, `SELECT backfill_complete, statement_timestamp(), updated_at,
				(SELECT enqueued_at FROM public.request_metrics_pending ORDER BY enqueued_at LIMIT 1)
				FROM public.request_metrics_projection_state WHERE singleton`).
				Scan(&state.BackfillComplete, &state.CheckedAt, &state.UpdatedAt, &state.PendingSince)
			switch {
			case err != nil:
				state = projectionReadiness{Source: state.Source}
			case !state.BackfillComplete || state.UpdatedAt == nil:
				// backfill 未完成 ⇒ 原口径 503（不变）。
			default:
				if stale, reason := projectionIsStale(derefTime(state.CheckedAt), state.UpdatedAt, state.PendingSince); stale {
					// 水位停滞 ⇒ 投影器停摆 / 积压没人消化 ⇒ 503 + 原因（不再假绿）。
					state.Stale = true
					state.Reason = reason
				} else {
					status = http.StatusOK
				}
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(state)
	}
}

func derefTime(t *time.Time) time.Time {
	if t != nil {
		return *t
	}
	return time.Now()
}
