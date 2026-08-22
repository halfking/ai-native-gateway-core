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

	"github.com/jackc/pgx/v5/pgxpool"
)

// offerListSQLColumns are the shared SELECT columns for provider-wide and
// credential-scoped model lists. The base list omits `provider_models.source`
// because that column was added by domain migration 361 (2026-08-22) and may
// not be present on installs that have not yet applied the rollout. We probe
// the schema once at startup and pick the matching constant below.
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
		       COALESCE(NULLIF(mc.canonical_name,''), mo.standardized_name),
		       COALESCE(mo.context_window_override, mc.context_window_override, mc.context_window) AS context_window,
		       mo.context_window_override,
		       COALESCE(NULLIF(TRIM(mc.modality), ''), COALESCE(NULLIF(TRIM(mo.provider_modality), ''), 'text')),
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
// returning 200 instead of 500.
const (
	offerListSQLWithSource = `COALESCE(pm.source, '')`
	offerListSQLCompat     = `''::text`
)

var (
	offerListSQLOnce sync.Once
	offerListSQLVal atomic.Value // string
)

// resolveOfferListSQL probes the live schema once for the presence of
// `provider_models.source` and caches the matching SQL. Safe to call before
// the first query — the value is then served from the atomic cache.
//
// Migration 361 (sql/migrations/domain/361_standard_provider_models.sql,
// 2026-08-22) added the column with `ADD COLUMN IF NOT EXISTS`. Until that
// migration runs on a given environment, `pm.source` does not exist and the
// pre-migration SQL would error out with "column pm.source does not exist",
// which surfaced as 500 on GET /api/providers/{id}/models. Probing here keeps
// the endpoint functional on both pre- and post-migration schemas.
func resolveOfferListSQL(ctx context.Context, db *pgxpool.Pool) string {
	offerListSQLOnce.Do(func() {
		if db == nil {
			offerListSQLVal.Store(strings.Replace(offerListSQLColumns, "__PM_SOURCE__", offerListSQLCompat, 1))
			return
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var hasSource bool
		err := db.QueryRow(probeCtx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'provider_models'
				  AND column_name = 'source'
			)
		`).Scan(&hasSource)
		if err != nil {
			slog.Warn("resolveOfferListSQL: probe failed; using compat SQL", "error", err)
			offerListSQLVal.Store(strings.Replace(offerListSQLColumns, "__PM_SOURCE__", offerListSQLCompat, 1))
			return
		}
		if hasSource {
			offerListSQLVal.Store(strings.Replace(offerListSQLColumns, "__PM_SOURCE__", offerListSQLWithSource, 1))
		} else {
			slog.Info("resolveOfferListSQL: provider_models.source missing; using compat SQL (migration 361 not applied)")
			offerListSQLVal.Store(strings.Replace(offerListSQLColumns, "__PM_SOURCE__", offerListSQLCompat, 1))
		}
	})
	v, _ := offerListSQLVal.Load().(string)
	if v == "" {
		// First call before Do() entered (race-safe fallback).
		return strings.Replace(offerListSQLColumns, "__PM_SOURCE__", offerListSQLCompat, 1)
	}
	return v
}

// offerListSQLFor returns the offer-list SQL using the given pool to probe the
// schema once. Use this from handler methods that have access to h.db.
func offerListSQLFor(ctx context.Context, db *pgxpool.Pool) string {
	return resolveOfferListSQL(ctx, db)
}

// offerListSQL is the shared SELECT used by provider-wide and credential-scoped
// model lists. Extra capability columns feed the unified UI.
//
// Kept as a `var` (not const) because we resolve the placeholder at first
// access via offerListSQLFor. Existing call sites that concatenate this with
// a WHERE clause continue to work transparently — the const was renamed to
// offerListSQLColumns above so this symbol now points at the runtime-resolved
// value through the legacy default (compat SQL, no pm.source).
//
// DEPRECATED: prefer offerListSQLFor(ctx, h.db) inside handler methods so the
// schema probe uses the live pool.
var offerListSQL = strings.Replace(offerListSQLColumns, "__PM_SOURCE__", offerListSQLCompat, 1)

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
