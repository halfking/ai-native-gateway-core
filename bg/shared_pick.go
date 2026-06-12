package bg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PickProbeResult is the value type returned by PickProbeModelForCredential.
type PickProbeResult struct {
	Model  string
	Source string
}

// PickProbeModelForCredential implements the 4-level fallback algorithm
// (manual > request_logs > domestic_random > empty).
// Skips credentials with source='manual' (already pinned by admin).
//
// Used by both:
//   - admin.ProviderDetailView.pickDefaultProbeModel (admin on-demand pick)
//   - bg.DefaultProbePicker.repickAll (daily 0:00 batch)
func PickProbeModelForCredential(ctx context.Context, db *pgxpool.Pool, credID int) (PickProbeResult, error) {
	var (
		currModel  string
		currSource string
		status     string
		lifecycle  string
		manual     bool
	)
	err := db.QueryRow(ctx, `
		SELECT COALESCE(default_probe_model, ''),
		       COALESCE(default_probe_model_source, ''),
		       status, lifecycle_status, COALESCE(manual_disabled, FALSE)
		FROM credentials WHERE id = $1
	`, credID).Scan(&currModel, &currSource, &status, &lifecycle, &manual)
	if err != nil {
		return PickProbeResult{}, err
	}

	if currSource == "manual" && currModel != "" {
		return PickProbeResult{Model: currModel, Source: "manual"}, nil
	}
	if status != "active" || lifecycle != "active" || manual {
		return PickProbeResult{}, nil
	}

	// Priority 1: most-used client_model in request_logs (7d)
	var topModel string
	err = db.QueryRow(ctx, `
		SELECT client_model
		FROM request_logs
		WHERE credential_id = $1
		  AND ts > now() - interval '7 days'
		  AND status_code = 200
		  AND client_model IS NOT NULL
		GROUP BY client_model
		ORDER BY count(*) DESC
		LIMIT 1
	`, credID).Scan(&topModel)
	if err == nil && topModel != "" {
		if bindingAvailableForModel(ctx, db, credID, topModel) {
			return PickProbeResult{Model: topModel, Source: "auto:request_log"}, nil
		}
	}

	// Priority 2: domestic provider random
	var domestic bool
	err = db.QueryRow(ctx, `
		SELECT p.domestic
		FROM credentials c JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
	`, credID).Scan(&domestic)
	if err != nil {
		return PickProbeResult{}, err
	}
	if domestic {
		var candidates []string
		rows, qerr := db.Query(ctx, `
			SELECT pm.raw_model_name
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE cmb.credential_id = $1
			  AND cmb.available = TRUE
			  AND cmb.unavailable_reason IS DISTINCT FROM 'manual'
			  AND pm.available = TRUE
			  AND pm.unavailable_reason IS DISTINCT FROM 'manual'
		`, credID)
		if qerr != nil {
			return PickProbeResult{}, qerr
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				continue
			}
			candidates = append(candidates, name)
		}
		if len(candidates) > 0 {
			pick := candidates[time.Now().UnixNano()%int64(len(candidates))]
			return PickProbeResult{Model: pick, Source: "auto:domestic_random"}, nil
		}
	}

	return PickProbeResult{}, nil
}

func bindingAvailableForModel(ctx context.Context, db *pgxpool.Pool, credID int, model string) bool {
	var ok bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM credential_model_bindings cmb
		    JOIN provider_models pm ON pm.id = cmb.provider_model_id
		    WHERE cmb.credential_id = $1
		      AND pm.raw_model_name = $2
		      AND cmb.available = TRUE
		      AND cmb.unavailable_reason IS DISTINCT FROM 'manual'
		      AND pm.available = TRUE
		      AND pm.unavailable_reason IS DISTINCT FROM 'manual'
		)
	`, credID, model).Scan(&ok)
	return err == nil && ok
}
