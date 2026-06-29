package bg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pickDB is the subset of *pgxpool.Pool that PickProbeModelForCredential needs.
// Declaring it as an interface lets us unit-test the picker with pgxmock
// while leaving the production call sites (which pass *pgxpool.Pool)
// unchanged — *pgxpool.Pool satisfies this interface via its embedded
// *pgxpool.ConnPool / pgxpool.Pool method set.
type pickDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// PickProbeResult is the value type returned by PickProbeModelForCredential.
type PickProbeResult struct {
	Model  string
	Source string
}

// PickProbeModelForCredential implements the 5-level fallback algorithm
// (manual > request_logs > domestic_featured > domestic_random_fallback > empty).
// Skips credentials with source='manual' (already pinned by admin).
//
// Priority 2 "domestic_featured" — domestic providers only — picks a binding
// model whose standardized_name (with raw_model_name fallback) appears in
// routing_policy.featured_models. We deliberately bias toward "hot / mainstream"
// models for the credential-level health probe because a single obscure /
// deprecated binding that happens to be available should not be allowed to
// flip the whole credential to unreachable. This matches the Layer 4
// featuredCycle in model_probe.go so the two probe layers agree on what
// counts as a "featured" model.
//
// If no featured match exists, we fall back to the previous random pick
// over the credential's available bindings (source='auto:domestic_random')
// — that branch is the safety net for credentials whose model set is
// entirely non-featured (e.g. legacy / sandbox providers).
//
// Used by both:
//   - admin.ProviderDetailView.pickDefaultProbeModel (admin on-demand pick)
//   - bg.DefaultProbePicker.repickAll (daily 0:00 batch)
func PickProbeModelForCredential(ctx context.Context, db pickDB, credID int) (PickProbeResult, error) {
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

	// Priority 2: domestic provider — featured-first pick.
	var domestic bool
	err = db.QueryRow(ctx, `
		SELECT p.domestic
		FROM credentials c JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
	`, credID).Scan(&domestic)
	if err != nil {
		return PickProbeResult{}, err
	}
	if !domestic {
		return PickProbeResult{}, nil
	}

	// Priority 2a: featured match. routing_policy.featured_models holds
	// standardized model names (e.g. "claude-3-5-sonnet-20241022",
	// "gpt-4o"). We compare on standardized_name first (the canonical
	// representation); the OR with raw_model_name is a defensive
	// compatibility fallback for providers whose bindings predate
	// standardized_name backfill. Ordered by standardized_name so the
	// pick is stable across repickAll cycles — randomness was the
	// whole reason this priority existed in the first place, and
	// stability is desirable for audit-trace reasons.
	rows, qerr := db.Query(ctx, `
		SELECT COALESCE(pm.outbound_model_name, pm.raw_model_name) AS probe_model
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		CROSS JOIN routing_policy pol
		WHERE pol.tenant_id = 'default'
		  AND cmb.credential_id = $1
		  AND cmb.available = TRUE
		  AND cmb.unavailable_reason IS DISTINCT FROM 'manual'
		  AND pm.available = TRUE
		  AND pm.unavailable_reason IS DISTINCT FROM 'manual'
		  AND (
		    pm.standardized_name = ANY(pol.featured_models)
		    OR pm.raw_model_name = ANY(pol.featured_models)
		  )
		ORDER BY COALESCE(pm.standardized_name, pm.raw_model_name)
		LIMIT 1
	`, credID)
	if qerr != nil {
		return PickProbeResult{}, qerr
	}
	if rows.Next() {
		var pick string
		if scanErr := rows.Scan(&pick); scanErr == nil && pick != "" {
			rows.Close()
			return PickProbeResult{Model: pick, Source: "auto:domestic_featured"}, nil
		}
	}
	rows.Close()

	// Priority 2b: safety-net random pick across all available bindings.
	// Only reached when no featured model is bound to this credential.
	// Preserved from the original 4-level design so providers with no
	// featured bindings still get a sensible default_probe_model.
	rows, qerr = db.Query(ctx, `
		SELECT COALESCE(pm.outbound_model_name, pm.raw_model_name) AS probe_model
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
	var candidates []string
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

	return PickProbeResult{}, nil
}

func bindingAvailableForModel(ctx context.Context, db pickDB, credID int, model string) bool {
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
