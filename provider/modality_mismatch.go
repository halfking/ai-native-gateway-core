package provider

import (
	"context"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// modalityMismatchQueryTimeout bounds the diagnosis query. Same budget and
// same rationale as streaming.alternativesQueryTimeout: a nicety on an
// already-failing request must never extend the client's wait.
const modalityMismatchQueryTimeout = 800 * time.Millisecond

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

	// ★ Bounded like the alternatives lookup (800ms): this runs on a request
	// that has ALREADY failed candidate resolution. The diagnosis is an
	// improvement over the plain 503, never a reason to make a failing
	// client wait longer — a slow query here must degrade to the plain 503
	// (the caller swallows errors) rather than extend the failure latency.
	diagCtx, cancel := context.WithTimeout(ctx, modalityMismatchQueryTimeout)
	defer cancel()

	var (
		registeredModality       string
		canonicalModel           string
		routableIgnoringModality bool
	)
	err := c.dbPool.QueryRow(diagCtx, modalityMismatchSQL, lookup, tenantID).Scan(
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

// ★模态接受判据的实现**不在本文件**：它与判据的 SQL 形态同源，住在
// provider/modality_accept.go（ModalityAccepts + ModalityAcceptArmsSQL）。
// 本文件只负责「诊断」——用那条判据回答「这次零候选是不是模态造成的」。

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
    -- ★ The canonical comes from mc.id (COALESCE(mo.canonical_id, ma.canonical_id)),
    -- NOT from mo.canonical_id: an offer whose own canonical_id is NULL can
    -- still resolve through the alias join, and the router counts exactly
    -- those. Selecting mo.canonical_id here made every alias-resolved offer
    -- read as "unresolved" ⇒ the routable EXISTS came back false ⇒ the
    -- diagnosis stayed silent for models the router was happily excluding
    -- by modality (found in the 2026-10-28 critical audit; conservative
    -- miss, not a false positive — but the whole point of this query is to
    -- catch those).
    SELECT mc.id AS canonical_id, mo.canonical_raw_name, mo.standardized_name,
           mc.canonical_name, mc.modality,
           mo.id AS offer_id
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
    -- A request name can match offers bound to more than one canonical
    -- (the router then returns the union). The diagnosis only runs when the
    -- router returned ZERO candidates, which means every matched canonical
    -- failed the modality filter — so any single canonical answers the
    -- "what is it registered as" question; deterministic pick for stability.
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
        -- ★ Join by the OFFER's binding identity, NOT by pm.canonical_id:
        -- an alias-only offer's provider_models row has canonical_id = NULL —
        -- that is what "alias-only" means — so a canonical_id join drops
        -- exactly the rows this diagnosis exists to see. The test proved it
        -- twice: matched resolved the canonical correctly both times, and
        -- routable still came back false, because the EXISTS keyed on a
        -- column that alias-only offers leave NULL.
        --
        -- model_offers.id IS the credential_model_bindings id (the view is
        -- cmb ⋈ pm), so the direct route is the binding view itself:
        -- v_routable_credential_models carries binding_id /
        -- provider_model_id / credential_id and is the same availability
        -- gate the router consults.
        SELECT 1
        FROM v_routable_credential_models v
        WHERE v.binding_id IN (SELECT m.offer_id FROM matched m)
          AND v.is_routable = TRUE
          AND (v.tenant_id = $2 OR v.tenant_id = 'default')
    )
`
