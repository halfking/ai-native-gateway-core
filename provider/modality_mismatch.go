package provider

import (
	"context"
	"strings"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// ModalityMismatch describes why a request found no candidate even though the
// model itself is known and routable.
//
// The zero-candidate exit has two very different causes that a client must be
// able to tell apart:
//
//   - the model has no usable provider at all right now (quota, probe state,
//     tenant scope) — retrying later, or switching model, are both sensible;
//   - the model IS routable, but it is registered as a text-only model while
//     this request carries images/audio — no amount of waiting helps, and the
//     only two real remedies are "send a text-only request" or "send this
//     request to a model that can read media".
//
// The second case is the one that used to be reported as a bare 503
// "No available provider for model 'X'", which reads as "the gateway is
// broken or busy". The client then retries the identical request forever,
// because nothing in the response says the request itself is the problem.
//
// ModalityMismatch carries the facts the error response needs to say that out
// loud: which modality the request carried, and which modality the model is
// registered as.
type ModalityMismatch struct {
	// RequestedModality is the modality detected from the request body
	// ("vision", "audio", "video", ...). Empty when the request was text-only.
	RequestedModality string
	// RegisteredModality is the modality the model is registered as in
	// models_canonical (usually "text").
	RegisteredModality string
	// CanonicalModel is the catalog name of the requested model, when known.
	CanonicalModel string
}

// IsZero reports whether no mismatch was detected.
func (m *ModalityMismatch) IsZero() bool {
	return m == nil || m.RequestedModality == ""
}

// ModelModalityMismatch reports whether the request's modality is what
// excluded the model from the candidate set.
//
// It answers the question by asking the database two things that must both
// hold:
//
//  1. the model IS routable when the modality filter is dropped — i.e. it has
//     a live (credential, model) binding, so the model itself is fine;
//  2. the model is registered as a modality that cannot serve the request.
//
// Only then is the zero-candidate exit a modality mismatch rather than a
// "nothing is routable" situation. When the model is not routable at all, the
// answer is "no candidates for now", which the caller must keep reporting as
// a retryable 503.
//
// The modality acceptance rule mirrors candidateQuerySQLBase's filter
// exactly — vision/audio/video are served by rows registered as that
// modality or as "multimodal" — so the diagnosis can never disagree with the
// gate that produced the zero candidates. The two must agree, or the error
// message would describe a cause the router never had.
func (c *Client) ModelModalityMismatch(ctx context.Context, model, profile, tenantID, requestModality string) (*ModalityMismatch, error) {
	if !c.Enabled() || c.dbPool == nil {
		return nil, nil
	}
	requestModality = strings.TrimSpace(strings.ToLower(requestModality))
	if requestModality == "" || requestModality == "text" {
		// A text request is never excluded by the modality gate.
		return nil, nil
	}
	lookup := modelname.CanonicalizeClientModel(model)
	if lookup == "" {
		return nil, nil
	}

	var (
		registeredModality       string
		canonicalModel           string
		routableIgnoringModality bool
	)
	err := c.dbPool.QueryRow(ctx, modalityMismatchSQL, lookup, tenantID).Scan(
		&registeredModality, &canonicalModel, &routableIgnoringModality)
	if err != nil {
		return nil, err
	}
	if !routableIgnoringModality {
		// The model has no live binding at all — nothing to do with modality.
		return nil, nil
	}
	if ModalityAccepts(registeredModality, requestModality) {
		// The model could serve this modality; it was excluded for some other
		// reason (probe verdict, tenant scope, ...). Not a modality mismatch.
		return nil, nil
	}
	return &ModalityMismatch{
		RequestedModality:  requestModality,
		RegisteredModality: registeredModality,
		CanonicalModel:     canonicalModel,
	}, nil
}

// ModalityAccepts reports whether a model registered as registeredModality can
// serve a request carrying requestedModality.
//
// This is the same rule the candidate query applies: media requests are
// served by rows registered for that media modality or as "multimodal"; a
// "multimodal" request is served only by "multimodal" rows.
//
// ★ Keeping this in one place matters: the diagnosis and the gate must use
// the same acceptance set, otherwise the error message can name a cause that
// the router never had. The candidate query's own copy lives in
// candidateQuerySQLBase; provider/candidate_query_sql_shape_test.go holds the
// two in lockstep.
func ModalityAccepts(registeredModality, requestedModality string) bool {
	registeredModality = strings.TrimSpace(strings.ToLower(registeredModality))
	if registeredModality == "" {
		registeredModality = "text"
	}
	requestedModality = strings.TrimSpace(strings.ToLower(requestedModality))
	switch requestedModality {
	case "":
		// An empty request modality means "no modality filter" — the router
		// admits everything in that case ($3 = '' in candidateQuerySQLBase).
		// Treating it as an unknown media modality would report a mismatch
		// for a request the router never filtered.
		return true
	case "text":
		return true
	case "multimodal":
		return registeredModality == "multimodal"
	case "image":
		return registeredModality == "image" || registeredModality == "vision" || registeredModality == "multimodal"
	default:
		// vision / audio / video
		return registeredModality == requestedModality || registeredModality == "multimodal"
	}
}

// modalityMismatchSQL answers, in one round trip:
//
//	registered_modality — how the model is registered in models_canonical
//	canonical_model     — its catalog name
//	routable           — whether it has a live binding when the modality
//	                     filter is dropped
//
// The routable check deliberately reuses the candidate query's own name
// resolution (offers / aliases / canonical) so "the model is routable" means
// exactly what it means to the router. It differs from the router only in
// dropping the modality predicate — which is the whole point: the pair
// (routable=true, modality cannot serve) is the definition of the mismatch.
//
// v_routable_credential_models is the same availability gate the router uses
// (is_routable), so "routable here" and "routable for the router" cannot
// drift apart.
const modalityMismatchSQL = `
WITH matched AS MATERIALIZED (
    SELECT mo.canonical_id, mo.canonical_raw_name, mo.standardized_name,
           mc.canonical_name, mc.modality
    FROM model_offers mo
    LEFT JOIN model_name_mapping mnm
           ON mnm.raw_model_name = mo.canonical_raw_name
    LEFT JOIN model_aliases ma
           ON ma.raw_name = mo.canonical_raw_name
          AND COALESCE(ma.status, 'active') = 'active'
    LEFT JOIN models_canonical mc ON mc.id = COALESCE(mo.canonical_id, ma.canonical_id)
    WHERE (
          mo.canonical_raw_name = $1
          OR mo.standardized_name = $1
          OR mnm.standardized_name = $1
          OR EXISTS (
              SELECT 1 FROM model_aliases ma2
              WHERE ma2.raw_name = $1
                AND COALESCE(ma2.status, 'active') = 'active'
                AND (
                    (mo.canonical_id IS NOT NULL AND ma2.canonical_id = mo.canonical_id)
                    OR (mo.canonical_id IS NULL AND ma2.canonical_id IS NULL)
                )
          )
          OR lower(mc.canonical_name) = $1
      )
      AND COALESCE(mc.status, 'active') != 'disabled'
),
resolved AS (
    SELECT m.canonical_name, m.modality, m.canonical_id
    FROM matched m
    WHERE m.canonical_id IS NOT NULL
    ORDER BY m.canonical_name
    LIMIT 1
)
SELECT
    COALESCE(
        (SELECT r.modality FROM resolved r WHERE r.modality IS NOT NULL LIMIT 1),
        'text'
    ),
    COALESCE(
        (SELECT r.canonical_name FROM resolved r LIMIT 1),
        $1
    ),
    EXISTS (
        SELECT 1
        FROM matched m
        JOIN provider_models pm ON pm.canonical_id = m.canonical_id
        JOIN v_routable_credential_models v
             ON v.provider_model_id = pm.id
        WHERE v.is_routable = TRUE
          AND (v.tenant_id = $2 OR v.tenant_id = 'default')
    )
`
