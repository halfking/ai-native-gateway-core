package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CredentialSuccessRateRow represents one (credential, model) pair with its
// recent success rate and sample count.
type CredentialSuccessRateRow struct {
	CredentialID      int64    `json:"credential_id"`
	CredentialLabel   string   `json:"credential_label"`
	RawModel          string   `json:"raw_model"`
	RecentRate        *float64 `json:"recent_rate"`
	RecentSamples     *int     `json:"recent_samples"`
	IsRoutable        bool     `json:"is_routable"`
	BelowThreshold    bool     `json:"below_threshold"` // rate < 0.5 && samples >= 20
	OldestRequestTime *string  `json:"oldest_request_time,omitempty"`
}

// HandleCredentialSuccessRates returns success rates for all (credential, model) pairs.
// GET /api/admin/credential-success-rates
func HandleCredentialSuccessRates(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		tenantID := "default" // TODO: support multi-tenant

		rows, err := db.Query(r.Context(), `
		SELECT
			c.id AS credential_id,
			c.label AS credential_label,
			mo.raw_model_name,
			rsr.rate AS recent_rate,
			rsr.samples AS recent_samples,
			v.is_routable,
			(rsr.samples >= 20 AND COALESCE(rsr.rate, 1.0) < 0.5) AS below_threshold,
			(SELECT MIN(ts) FROM request_logs rl
			 WHERE rl.credential_id = c.id
			   AND lower(COALESCE(rl.outbound_model, rl.client_model)) = lower(mo.raw_model_name)
			   AND rl.ts > NOW() - INTERVAL '3 hours'
			) AS oldest_request_time
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN v_routable_credential_models v
		       ON v.credential_id = mo.credential_id
		      AND v.raw_model_name = mo.raw_model_name
		CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50, 3) AS rsr
		WHERE p.tenant_id = $1
		ORDER BY c.id, mo.raw_model_name
	`, tenantID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var result []CredentialSuccessRateRow
		for rows.Next() {
			var row CredentialSuccessRateRow
			var oldestTime *time.Time
			err := rows.Scan(
				&row.CredentialID,
				&row.CredentialLabel,
				&row.RawModel,
				&row.RecentRate,
				&row.RecentSamples,
				&row.IsRoutable,
				&row.BelowThreshold,
				&oldestTime,
			)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if oldestTime != nil {
				ts := oldestTime.Format(time.RFC3339)
				row.OldestRequestTime = &ts
			}
			result = append(result, row)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// HandleResetCredentialSuccessRate manually resets success rate by deleting
// old failed requests for a (credential, model) pair.
// POST /api/admin/credential-success-rates/reset
// Body: {"credential_id": 17, "raw_model": "claude-sonnet-4-6"}
func HandleResetCredentialSuccessRate(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			CredentialID int64  `json:"credential_id"`
			RawModel     string `json:"raw_model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.CredentialID == 0 || req.RawModel == "" {
			http.Error(w, "credential_id and raw_model required", http.StatusBadRequest)
			return
		}

		// Delete failed requests older than 10 minutes to allow immediate recovery
		result, err := db.Exec(r.Context(), `
		DELETE FROM request_logs
		WHERE credential_id = $1
		  AND lower(COALESCE(outbound_model, client_model)) = lower($2)
		  AND success = false
		  AND ts < NOW() - INTERVAL '10 minutes'
	`, req.CredentialID, req.RawModel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		deleted := result.RowsAffected()

		// Return new success rate
		var newRate *float64
		var newSamples *int
		err = db.QueryRow(context.Background(), `
		SELECT * FROM recent_success_rate($1, $2, 50, 3)
	`, req.CredentialID, req.RawModel).Scan(&newRate, &newSamples)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		slog.Info("credential success rate reset",
			"credential_id", req.CredentialID,
			"raw_model", req.RawModel,
			"deleted", deleted,
			"new_rate", newRate,
			"new_samples", newSamples)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"deleted":     deleted,
			"new_rate":    newRate,
			"new_samples": newSamples,
			"success":     true,
			"message":     "Old failed requests deleted, success rate should recover",
		})
	}
}
