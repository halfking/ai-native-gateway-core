package metricsstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

type projectionReadiness struct {
	Source            string                      `json:"source"`
	BackfillComplete  bool                        `json:"backfill_complete"`
	CheckedAt         *time.Time                  `json:"checked_at"`
	UpdatedAt         *time.Time                  `json:"updated_at"`
	PendingSince      *time.Time                  `json:"pending_since"`
	PendingCount      int64                       `json:"pending_count"`
	OldestWaitSeconds float64                     `json:"oldest_wait_seconds"`
	Ready             bool                        `json:"ready"`
	Coverage          *metrics.ProjectionCoverage `json:"coverage,omitempty"`
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

		var start, end time.Time
		from, through := r.URL.Query().Get("from"), r.URL.Query().Get("through")
		if from != "" || through != "" {
			var startErr, endErr error
			start, startErr = time.Parse(time.RFC3339, from)
			end, endErr = time.Parse(time.RFC3339, through)
			if startErr != nil || endErr != nil || !end.After(start) || end.Sub(start) > 93*24*time.Hour {
				http.Error(w, "use a positive window of at most 93 days with RFC3339 from and through", http.StatusBadRequest)
				return
			}
		}
		state := projectionReadiness{Source: "request_metrics"}
		status := http.StatusServiceUnavailable
		if db != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			err := db.QueryRowContext(ctx, `SELECT backfill_complete, statement_timestamp(), updated_at,
				(SELECT enqueued_at FROM public.request_metrics_pending_events ORDER BY enqueued_at LIMIT 1),
				(SELECT count(*) FROM public.request_metrics_pending_events)
				FROM public.request_metrics_repair_state WHERE singleton`).
				Scan(&state.BackfillComplete, &state.CheckedAt, &state.UpdatedAt, &state.PendingSince, &state.PendingCount)
			if err != nil {
				state = projectionReadiness{Source: state.Source}
			} else {
				if state.PendingSince != nil {
					state.OldestWaitSeconds = max(0, state.CheckedAt.Sub(*state.PendingSince).Seconds())
				}
				state.Ready = state.BackfillComplete && state.UpdatedAt != nil &&
					state.CheckedAt.Sub(*state.UpdatedAt) <= 5*time.Minute &&
					state.OldestWaitSeconds <= (5*time.Minute).Seconds()
				if state.Ready {
					status = http.StatusOK
				}
			}
		}
		if db != nil && state.CheckedAt != nil && !start.IsZero() {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			coverage, err := readProjectionCoverage(ctx, db, start, end)
			if err != nil {
				state.Ready = false
				status = http.StatusServiceUnavailable
			} else {
				state.Coverage = &coverage
				if coverage.Current != coverage.Expected {
					state.Ready = false
					status = http.StatusServiceUnavailable
				}
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(state)
	}
}

func readProjectionCoverage(ctx context.Context, db *sql.DB, start, end time.Time) (metrics.ProjectionCoverage, error) {
	return metrics.ReadProjectionCoverage(ctx, db.QueryRowContext, start, end)
}
