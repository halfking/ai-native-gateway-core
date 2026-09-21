package admin

// credential_models_dto.go — shared offer list SQL/DTO for provider + credential
// model catalog endpoints.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// offerListSQLColumns are the shared SELECT columns for provider-wide and
// credential-scoped model lists. Two columns are resolved against the live
// schema at startup:
//
//   - `provider_models.source` was added by domain migration 361 (2026-08-22)
//     and may not be present on installs that have not yet applied the rollout.
//   - `model_offers.provider_modality` exists in the baseline schema and in
//     every fresh install, but the model_offers view on upgraded installs was
//     last rebuilt by migrations whose view body predates that alias (678's
//     rebuild, the newest, does not carry it). Querying it unconditionally
//     500s those environments with SQLSTATE 42703 — observed live on the
//     local upgraded install as
//     GET /api/providers/36994/credentials/77/models → "column
//     mo.provider_modality does not exist" (2026-09-11).
//
// We probe both columns once at startup and pick the matching constants.
const offerListSQLColumns = `
		SELECT mo.id, mo.credential_id, COALESCE(c.label,'') AS credential_label,
		       COALESCE(mo.raw_model_name,''), COALESCE(mo.standardized_name,''),
		       mo.canonical_id, COALESCE(mo.outbound_model_name,''),
		       mo.available, mo.unavailable_reason, mo.unavailable_at,
		       mo.p95_latency_ms, mo.success_rate::float8,
		       mo.unit_price_in_per_1m::float8 AS input_price,
		       mo.unit_price_out_per_1m::float8 AS output_price,
		       mo.last_seen_at, COALESCE(mo.routing_tier::text,''),
		       mc.standard_iq::float8,
		       niq.overall_score::float8, niq.avg_score::float8,
		       COALESCE(niq.sample_count, 0), niq.tested_at,
		       -- 2026-09-11 audit: same NULL guard as getProviderModels — a
		       -- never-matched offer (canonical_id NULL + standardized_name
		       -- NULL) must not NULL this non-pointer string column.
		       COALESCE(NULLIF(mc.canonical_name,''), mo.standardized_name, ''),
		       COALESCE(mo.context_window_override, mc.context_window_override, mc.context_window) AS context_window,
		       mo.context_window_override,
		       COALESCE(NULLIF(TRIM(mc.modality), ''), __MO_MODALITY__),
		       COALESCE(mc.multimodal_caps, '{}'::text[]),
		       mc.reasoning_caps,
		       COALESCE(mc.status, 'active'),
		       COALESCE(mo.admin_protected, FALSE),
		       __PM_SOURCE__
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN credential_model_bindings cmb ON cmb.id = mo.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		LEFT JOIN node_iq_latest niq
		       ON niq.credential_id = mo.credential_id
		      AND lower(niq.raw_model_name) = lower(mo.raw_model_name)
`

// offerListSQLWithSource is used after migration 361 has added
// `provider_models.source` (defaulting 'discovery'). Earlier installs that
// pre-date the migration fall back to offerListSQLCompat to keep the endpoint
// returning 200 instead of 500. The modality pair follows the same contract:
// the with-column form keeps the view-level fallback, the compat form leans
// on the canonical modality alone (always present on models_canonical).
const (
	offerListSQLWithSource = `COALESCE(pm.source, '')`
	offerListSQLCompat     = `''::text`

	moModalityWithColumn = `COALESCE(NULLIF(TRIM(mo.provider_modality), ''), 'text')`
	moModalityCompat     = `'text'`
)

// renderOfferListSQL substitutes both schema-dependent placeholders. The
// tokens are distinct so the two strings.Replace calls cannot collide.
func renderOfferListSQL(pmSource, moModality string) string {
	return strings.Replace(
		strings.Replace(offerListSQLColumns, "__PM_SOURCE__", pmSource, 1),
		"__MO_MODALITY__", moModality, 1,
	)
}

var (
	offerListSQLOnce sync.Once
	offerListSQLVal  atomic.Value // string
)

// resolveOfferListSQL probes the live schema once for the presence of
// `provider_models.source` and `model_offers.provider_modality`, caching the
// matching SQL. Safe to call before the first query — the value is then
// served from the atomic cache. The modality probe is what keeps the
// credential-scoped list (and every other consumer of this shared DTO) on a
// 200 on upgraded installs whose model_offers view predates the alias.
func resolveOfferListSQL(ctx context.Context, db offerQuerier) string {
	offerListSQLOnce.Do(func() {
		if db == nil {
			offerListSQLVal.Store(renderOfferListSQL(offerListSQLCompat, moModalityCompat))
			return
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var hasSource, hasProviderModality bool
		err := db.QueryRow(probeCtx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'provider_models'
				  AND column_name = 'source'
			),
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'model_offers'
				  AND column_name = 'provider_modality'
			)
		`).Scan(&hasSource, &hasProviderModality)
		if err != nil {
			slog.Warn("resolveOfferListSQL: probe failed; using compat SQL", "error", err)
			offerListSQLVal.Store(renderOfferListSQL(offerListSQLCompat, moModalityCompat))
			return
		}
		pmSource := offerListSQLCompat
		if hasSource {
			pmSource = offerListSQLWithSource
		} else {
			slog.Info("resolveOfferListSQL: provider_models.source missing; using compat SQL (migration 361 not applied)")
		}
		moModality := moModalityCompat
		if hasProviderModality {
			moModality = moModalityWithColumn
		} else {
			slog.Info("resolveOfferListSQL: model_offers.provider_modality missing; using compat SQL (view predates the modality alias)")
		}
		offerListSQLVal.Store(renderOfferListSQL(pmSource, moModality))
	})
	v, _ := offerListSQLVal.Load().(string)
	if v == "" {
		// First call before Do() entered (race-safe fallback).
		return renderOfferListSQL(offerListSQLCompat, moModalityCompat)
	}
	return v
}

// offerListSQLFor returns the offer-list SQL using the given pool to probe the
// schema once. The offerQuerier seam (instead of *pgxpool.Pool) lets the
// credential-scoped handler run against pgxmock like its provider-wide
// sibling. Use this from handler methods that have access to h.db.
func offerListSQLFor(ctx context.Context, db offerQuerier) string {
	return resolveOfferListSQL(ctx, db)
}

// offerListSQL is the shared SELECT used by provider-wide and credential-scoped
// model lists. Extra capability columns feed the unified UI.
//
// Kept as a `var` (not const) because we resolve the placeholders at first
// access via offerListSQLFor. Existing call sites that concatenate this with
// a WHERE clause continue to work transparently — the const was renamed to
// offerListSQLColumns above so this symbol now points at the runtime-resolved
// value through the legacy default (compat SQL: no pm.source, no view-level
// provider_modality).
//
// DEPRECATED: prefer offerListSQLFor(ctx, h.db) inside handler methods so the
// schema probe uses the live pool.
var offerListSQL = renderOfferListSQL(offerListSQLCompat, moModalityCompat)

type modelOfferDTO struct {
	ID                    int             `json:"id"`
	CredentialID          int             `json:"credential_id"`
	CredentialLabel       string          `json:"credential_label"`
	RawModelName          string          `json:"raw_model_name"`
	StandardizedName      string          `json:"standardized_name"`
	CanonicalID           *int            `json:"canonical_id"`
	OutboundModelName     string          `json:"outbound_model_name"`
	DisplayName           string          `json:"display_name"`
	Available             bool            `json:"available"`
	UnavailableReason     *string         `json:"unavailable_reason"`
	UnavailableAt         *time.Time      `json:"unavailable_at"`
	P95LatencyMs          *int            `json:"p95_latency_ms"`
	SuccessRate           *float64        `json:"success_rate"`
	InputPrice            *float64        `json:"input_price"`
	OutputPrice           *float64        `json:"output_price"`
	LastSeenAt            *time.Time      `json:"last_seen_at"`
	RoutingTier           string          `json:"routing_tier"`
	AvailabilitySource    string          `json:"availability_source"`
	CanonicalStandardIQ   *float64        `json:"canonical_standard_iq"`
	CanonicalName         string          `json:"canonical_name"`
	NodeIQ                *float64        `json:"node_iq"`
	NodeIQAvg             *float64        `json:"node_iq_avg"`
	NodeIQSampleCount     int             `json:"node_iq_sample_count"`
	NodeIQTestedAt        *time.Time      `json:"node_iq_tested_at"`
	ContextWindow         *int            `json:"context_window"`
	ContextWindowOverride *int            `json:"context_window_override"`
	Modality              string          `json:"modality"`
	MultimodalCaps        []string        `json:"multimodal_caps"`
	ReasoningCaps         json.RawMessage `json:"reasoning_caps"`
	CanonicalStatus       string          `json:"canonical_status"`
	AdminProtected        bool            `json:"admin_protected"`
	Source                string          `json:"source"`
}

type createCredentialModelReq struct {
	RawModelName      string          `json:"raw_model_name"`
	StandardizedName  *string         `json:"standardized_name"`
	CanonicalID       *int            `json:"canonical_id"`
	OutboundModelName *string         `json:"outbound_model_name"`
	Available         *bool           `json:"available"`
	ContextWindow     *int            `json:"context_window"`
	Modality          *string         `json:"modality"`
	MultimodalCaps    []string        `json:"multimodal_caps"`
	ReasoningCaps     json.RawMessage `json:"reasoning_caps"`
	CanonicalStatus   *string         `json:"canonical_status"`
}

func scanModelOfferDTO(scan func(dest ...any) error) (modelOfferDTO, error) {
	var o modelOfferDTO
	var multimodal []string
	var reasoning []byte
	err := scan(
		&o.ID, &o.CredentialID, &o.CredentialLabel,
		&o.RawModelName, &o.StandardizedName,
		&o.CanonicalID, &o.OutboundModelName,
		&o.Available, &o.UnavailableReason, &o.UnavailableAt,
		&o.P95LatencyMs, &o.SuccessRate,
		&o.InputPrice, &o.OutputPrice,
		&o.LastSeenAt, &o.RoutingTier,
		&o.CanonicalStandardIQ,
		&o.NodeIQ, &o.NodeIQAvg,
		&o.NodeIQSampleCount, &o.NodeIQTestedAt,
		&o.CanonicalName,
		&o.ContextWindow, &o.ContextWindowOverride,
		&o.Modality, &multimodal, &reasoning,
		&o.CanonicalStatus, &o.AdminProtected, &o.Source,
	)
	if err != nil {
		return o, err
	}
	if multimodal == nil {
		multimodal = []string{}
	}
	o.MultimodalCaps = multimodal
	if len(reasoning) > 0 {
		o.ReasoningCaps = json.RawMessage(reasoning)
	}
	o.DisplayName = o.OutboundModelName
	o.AvailabilitySource = classifyAvailability(o.Available, o.UnavailableReason)
	return o, nil
}

func (h *Handler) patchCanonicalCaps(ctx context.Context, canonicalID int, req createCredentialModelReq) error {
	if req.Modality != nil {
		mod := stringsTrimDefault(*req.Modality, "text")
		if _, err := h.db.Exec(ctx, `UPDATE models_canonical SET modality = $1, updated_at = NOW() WHERE id = $2`, mod, canonicalID); err != nil {
			return err
		}
	}
	if req.MultimodalCaps != nil {
		if _, err := h.db.Exec(ctx, `UPDATE models_canonical SET multimodal_caps = $1, updated_at = NOW() WHERE id = $2`, req.MultimodalCaps, canonicalID); err != nil {
			return err
		}
	}
	if req.ReasoningCaps != nil {
		if string(req.ReasoningCaps) == "null" || len(req.ReasoningCaps) == 0 {
			if _, err := h.db.Exec(ctx, `UPDATE models_canonical SET reasoning_caps = NULL, updated_at = NOW() WHERE id = $1`, canonicalID); err != nil {
				return err
			}
		} else if _, err := h.db.Exec(ctx, `UPDATE models_canonical SET reasoning_caps = $1, updated_at = NOW() WHERE id = $2`, []byte(req.ReasoningCaps), canonicalID); err != nil {
			return err
		}
	}
	if req.CanonicalStatus != nil {
		st := strings.TrimSpace(*req.CanonicalStatus)
		if st != "" {
			if _, err := h.db.Exec(ctx, `UPDATE models_canonical SET status = $1, updated_at = NOW() WHERE id = $2`, st, canonicalID); err != nil {
				return err
			}
		}
	}
	return nil
}

func stringsTrimDefault(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}
