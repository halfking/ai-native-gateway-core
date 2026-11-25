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
			if err != nil {
				state = projectionReadiness{Source: state.Source}
			} else if state.BackfillComplete && state.UpdatedAt != nil {
				status = http.StatusOK
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(state)
	}
}
