package streaming

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // matches handler.go's existing import of the same package
)

// ModelAlternative is one entry in the "pick a different model" list returned
// when the requested model has no routable node.
type ModelAlternative struct {
	// Model is the canonical name the client should send back in `model`.
	Model string `json:"model"`
	// DisplayName is human-facing (e.g. "Claude Sonnet 4.6"); falls back to
	// Model when the catalog has no display name.
	DisplayName string `json:"display_name,omitempty"`
	// Family groups entries in a picker ("claude", "gpt", "gemini").
	Family string `json:"family,omitempty"`
	// ContextWindow is in tokens; omitted when unknown, so a client cannot
	// mistake "unknown" for zero.
	ContextWindow *int `json:"context_window,omitempty"`
	// Featured marks membership in routing_policy.featured_models — the
	// operator-curated set, as opposed to something merely reachable.
	Featured bool `json:"featured"`
	// Reason explains why this entry is being offered. Values:
	// "task_match" (matches the session's task type), "featured",
	// "popular" (recent successful usage).
	Reason string `json:"reason,omitempty"`
}

// ModelAlternativesResult is what the handler embeds in the 503 body.
type ModelAlternativesResult struct {
	// RequestedModel is the model that had no routable node.
	RequestedModel string `json:"requested_model"`
	// TaskType is the session's inferred task type when one was available,
	// so the client can explain WHY these models were suggested.
	TaskType string `json:"task_type,omitempty"`
	// Alternatives is ordered best-first and may be empty, which is a
	// meaningful answer: nothing at all is routable right now, so the
	// client should wait rather than retry a different name.
	Alternatives []ModelAlternative `json:"alternatives"`
}

// maxModelAlternatives bounds the list. Long lists are unusable in a picker and
// make the error body large enough to matter on mobile links; ~8 is the most a
// person will read before giving up.
const maxModelAlternatives = 8

// alternativesQueryTimeout bounds the lookup. This runs on an already-failing
// request, so it must not add meaningful latency: better to return the plain
// 503 than to make a stalled client wait longer for a nicety.
const alternativesQueryTimeout = 800 * time.Millisecond

// ModelAlternativesFinder resolves "what CAN this caller use right now".
//
// It is deliberately separate from the admin available-models catalog
// (admin/routing.go): that endpoint is a near-static inventory gated on
// mo.available / c.status / p.enabled, and it never consults
// v_routable_credential_models. A model whose only credentials are
// quota-exhausted, cooling, or in probe backoff still appears "available"
// there — precisely the state we are trying to route around here. Offering
// such a model as an alternative would send the client straight into a
// second failure.
type ModelAlternativesFinder struct {
	db *pgxpool.Pool
	// intentCache is autoroute's per-session task-type cache. Optional: nil
	// simply means no session-level task type, and the result falls back to
	// featured + popularity ordering.
	intentCache *autoroute.SessionIntentCache
}

func NewModelAlternativesFinder(db *pgxpool.Pool) *ModelAlternativesFinder {
	return &ModelAlternativesFinder{db: db}
}

// SetIntentCache wires autoroute's session intent cache so task-type-aware
// suggestions work on ordinary (non-`model=auto`) requests.
func (f *ModelAlternativesFinder) SetIntentCache(c *autoroute.SessionIntentCache) {
	if f != nil {
		f.intentCache = c
	}
}

// Find returns routable alternatives to requestedModel, ordered best-first.
//
// Ordering: entries matching the session's task type come first, then other
// featured models, then models with recent successful traffic. Within a group,
// higher recent usage wins.
//
// taskType may be "" when nothing is known about the session; the query then
// degrades to featured-then-popular. Errors are logged and reported as an empty
// list, never propagated: the caller is already writing an error response and
// must not be derailed by a failure in the suggestion path.
func (f *ModelAlternativesFinder) Find(
	ctx context.Context,
	requestedModel, tenantID, taskType string,
) ModelAlternativesResult {
	result := ModelAlternativesResult{
		RequestedModel: requestedModel,
		TaskType:       taskType,
		Alternatives:   []ModelAlternative{},
	}
	if f == nil || f.db == nil {
		return result
	}

	ctx, cancel := context.WithTimeout(ctx, alternativesQueryTimeout)
	defer cancel()

	rows, err := f.db.Query(ctx, alternativesSQL,
		strings.ToLower(strings.TrimSpace(requestedModel)),
		tenantID,
		taskType,
		maxModelAlternatives,
	)
	if err != nil {
		slog.WarnContext(ctx, "model alternatives lookup failed; returning plain no_candidate",
			"error", err, "requested_model", requestedModel, "tenant_id", tenantID)
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var (
			alt           ModelAlternative
			displayName   *string
			family        *string
			contextWindow *int
		)
		if err := rows.Scan(&alt.Model, &displayName, &family, &contextWindow,
			&alt.Featured, &alt.Reason); err != nil {
			slog.WarnContext(ctx, "model alternatives row scan failed", "error", err)
			continue
		}
		if displayName != nil {
			alt.DisplayName = *displayName
		}
		if family != nil {
			alt.Family = *family
		}
		alt.ContextWindow = contextWindow
		result.Alternatives = append(result.Alternatives, alt)
	}
	if err := rows.Err(); err != nil {
		slog.WarnContext(ctx, "model alternatives iteration failed", "error", err)
	}

	return result
}

// alternativesSQL selects canonical models that have at least one routable
// (credential, model) binding right now.
//
// Availability comes from v_routable_credential_models.is_routable, the same
// gate the router itself uses, so anything offered here is genuinely reachable
// at this instant. The admin catalog's shallower filter would suggest models
// whose only credentials are quota-exhausted or cooling.
//
// Parameters:
//
//	$1 requested model (lowercased) — excluded from its own suggestion list
//	$2 tenant id                    — own-tenant plus shared 'default' providers
//	$3 task type ('' when unknown)  — drives the task_match tier
//	$4 row limit
//
// Ordering tiers (tier 1 first):
//
//	1 task_match — in task_default_routing for $3, primary tier before fallback
//	2 featured   — routing_policy.featured_models, operator-curated
//	3 popular    — recent successful traffic, as a last resort
//
// The 7-day usage count is a LEFT JOIN so a freshly-added featured model with
// no traffic still appears (at count 0) rather than being dropped.
const alternativesSQL = `
WITH policy AS (
    SELECT COALESCE(featured_models, ARRAY[]::TEXT[]) AS featured
    FROM routing_policy
    WHERE tenant_id = 'default'
    ORDER BY id
    LIMIT 1
),
routable AS (
    -- Distinct canonical models with >= 1 routable binding. GROUP BY because a
    -- model routable via several credentials must still be offered once, and
    -- 522 lets each (credential×model) binding carry its own context-window
    -- override; the advertised window is the most generous routable binding so
    -- the picker does not under-sell a model that one credential widened.
    SELECT mc.id AS canonical_id,
           mc.canonical_name,
           mc.display_name,
           mc.family,
           MAX(COALESCE(cmb.context_window_override, mc.context_window_override, mc.context_window)) AS context_window
    FROM v_routable_credential_models v
    JOIN provider_models pm ON pm.id = v.provider_model_id
    JOIN credential_model_bindings cmb ON cmb.credential_id = v.credential_id AND cmb.provider_model_id = v.provider_model_id
    JOIN models_canonical mc ON mc.id = pm.canonical_id
    WHERE v.is_routable = TRUE
      AND (v.tenant_id = $2 OR v.tenant_id = 'default')
      AND COALESCE(mc.status, 'active') = 'active'
      AND mc.canonical_name <> $1
    GROUP BY mc.id, mc.canonical_name, mc.display_name, mc.family
),
task_models AS (
    -- Empty when $3 is '' (unknown task type), which collapses tier 1.
    SELECT canonical_model, MIN(
               CASE tier WHEN 'primary' THEN 1 WHEN 'secondary' THEN 2 ELSE 3 END
           ) AS tier_rank
    FROM task_default_routing
    WHERE $3 <> ''
      AND task_type = $3
      AND (tenant_id = $2 OR tenant_id = 'default')
    GROUP BY canonical_model
),
usage_7d AS (
    -- request_logs stores the standardized name in canonical_model
    -- (migration 458), not canonical_name.
    SELECT canonical_model, COUNT(*) AS cnt
    FROM request_logs_with_current_month
    WHERE ts > now() - interval '7 days'
      AND success = TRUE
      AND canonical_model IS NOT NULL
    GROUP BY canonical_model
)
SELECT r.canonical_name,
       r.display_name,
       r.family,
       r.context_window,
       (LOWER(r.canonical_name) = ANY (SELECT LOWER(unnest(featured)) FROM policy)) AS featured,
       CASE
           WHEN t.canonical_model IS NOT NULL THEN 'task_match'
           WHEN LOWER(r.canonical_name) = ANY (SELECT LOWER(unnest(featured)) FROM policy) THEN 'featured'
           ELSE 'popular'
       END AS reason
FROM routable r
LEFT JOIN task_models t ON t.canonical_model = r.canonical_name
LEFT JOIN usage_7d u ON u.canonical_model = r.canonical_name
WHERE t.canonical_model IS NOT NULL
   OR LOWER(r.canonical_name) = ANY (SELECT LOWER(unnest(featured)) FROM policy)
   OR COALESCE(u.cnt, 0) > 0
ORDER BY
    CASE
        WHEN t.canonical_model IS NOT NULL THEN 1
        WHEN LOWER(r.canonical_name) = ANY (SELECT LOWER(unnest(featured)) FROM policy) THEN 2
        ELSE 3
    END,
    COALESCE(t.tier_rank, 9),
    COALESCE(u.cnt, 0) DESC,
    r.canonical_name
LIMIT $4
`

// resolveTaskTypeForAlternatives determines the session's task type using three
// increasingly expensive sources, stopping at the first hit:
//
//  1. X-Gw-Task-Hint header — the client told us outright
//  2. autoroute session intent cache — a previous `model=auto` request on this
//     session already classified it (Redis-backed, 10min TTL)
//  3. inline heuristic classification of this very request
//
// Step 3 matters because autoroute only classifies when model == "auto", so an
// ordinary request naming an explicit model has no task type on file. The
// heuristic classifier is pure keyword/threshold work with no LLM call and no
// database access, which is why it is acceptable on a request that is already
// failing.
//
// Returns "" when nothing can be determined; callers treat that as "order by
// featured then popularity".
func resolveTaskTypeForAlternatives(
	ctx context.Context,
	r httpHeaderGetter,
	cache *autoroute.SessionIntentCache,
	sigs autoroute.ClassificationSignals,
) string {
	if hint := strings.TrimSpace(r.Header(autoTaskHintHeader)); hint != "" {
		if isKnownTaskType(hint) {
			return hint
		}
	}

	if cache != nil {
		sessionID := strings.TrimSpace(r.Header("X-Gw-Session-Id"))
		if sessionID == "" {
			sessionID = strings.TrimSpace(r.Header("X-Session-Id"))
		}
		if sessionID != "" {
			if cached, ok := cache.Get(sessionID); ok && cached.TaskType != "" {
				return string(cached.TaskType)
			}
		}
	}

	// Inline heuristic. Defaults are used rather than the tuned thresholds
	// because this path has no TuningStore handy and a suggestion list does
	// not warrant threading one through; misclassification here costs an
	// imperfectly-ordered list, not a wrong route.
	classifier := autoroute.NewHeuristicClassifier(
		autoroute.DefaultHeuristicThresholds(),
		autoroute.DefaultKeywords(),
	)
	classification, err := classifier.Classify(ctx, sigs)
	if err != nil || classification == nil {
		return ""
	}
	return string(classification.Primary)
}

// SetModelAlternativesFinder wires the zero-candidate suggestion path. Leaving
// it unset keeps the historical bare 503.
func (h *ChatHandler) SetModelAlternativesFinder(f *ModelAlternativesFinder) {
	h.altFinder = f
}

// findModelAlternatives resolves the session's task type and returns routable
// alternatives for a request whose model has no candidate.
//
// Never returns an error: this runs while the handler is already committed to
// writing a 503, so every failure degrades to an empty list and the client gets
// the plain error it would have received before.
func (h *ChatHandler) findModelAlternatives(
	r *http.Request,
	reqBody *chatRequestBody,
	bodyBytes []byte,
	clientModel string,
	keyInfo *authentication.KeyInfo,
) ModelAlternativesResult {
	if h == nil || h.altFinder == nil {
		return ModelAlternativesResult{
			RequestedModel: clientModel,
			Alternatives:   []ModelAlternative{},
		}
	}

	tenantID := "default"
	if keyInfo != nil && keyInfo.TenantID != "" {
		tenantID = keyInfo.TenantID
	}

	// Reuse autoroute's signal extraction so the inline heuristic sees exactly
	// what a model="auto" request would have produced.
	sigs := extractSignalsForAuto(reqBody, bodyBytes)
	taskType := resolveTaskTypeForAlternatives(
		r.Context(),
		requestHeaderGetter{h: r.Header},
		h.altFinder.intentCache,
		sigs,
	)

	return h.altFinder.Find(r.Context(), clientModel, tenantID, taskType)
}

// isKnownTaskType validates a client-supplied X-Gw-Task-Hint against
// autoroute.AllTaskTypes. An unrecognized hint is ignored rather than trusted,
// so a typo cannot silently collapse tier 1 of the suggestion query (which
// matches on exact task_type equality and would return nothing).
func isKnownTaskType(s string) bool {
	for _, t := range autoroute.AllTaskTypes {
		if string(t) == s {
			return true
		}
	}
	return false
}

// httpHeaderGetter is the narrow slice of *http.Request that
// resolveTaskTypeForAlternatives needs, so it can be tested without
// constructing a full request.
type httpHeaderGetter interface {
	Header(key string) string
}

// requestHeaderGetter adapts *http.Request to httpHeaderGetter.
type requestHeaderGetter struct {
	h interface{ Get(string) string }
}

func (g requestHeaderGetter) Header(key string) string {
	if g.h == nil {
		return ""
	}
	return g.h.Get(key)
}
