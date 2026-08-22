package admin

// credential_models_dto.go — shared offer list SQL/DTO for provider + credential
// model catalog endpoints.

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// offerListSQL is the shared SELECT used by provider-wide and credential-scoped
// model lists. Extra capability columns feed the unified UI.
const offerListSQL = `
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
		       COALESCE(pm.source, '')
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN credential_model_bindings cmb ON cmb.id = mo.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		LEFT JOIN node_iq_latest niq
		       ON niq.credential_id = mo.credential_id
		      AND lower(niq.raw_model_name) = lower(mo.raw_model_name)
`

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
