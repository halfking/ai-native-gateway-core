package admin

// 2026-09-29 stream 按模型分组 dashboard 上游污染治理。
//
// 根因：admin/routing.go:handleRoutingResolve 把
//   modelname.NormalizeRouteKeyAliases(model)
// 的变体矩阵（含 removableWrapperTokens 的 flash / turbo / air /
// highspeed / preview / thinking / reasoning / vision / audio 包装词剥离）
// 喂给 SQL `lower(...) = ANY($1)`。这把 base 模型的 raw_model_name 也命中
// 了，导致 candidates 跨 canonical_id：
//   resolve("glm-5.3-flash") -> canonical_id=null,
//                              candidates 来自 [2716170 glm-5.3-flash,
//                                              2422803 glm-5.3]
// glm-5.3 与 glm-5.3-flash 是两个不同商品，凭据不该混在一起。
//
// 治理：不动 removableWrapperTokens（GenerateAliasVariants 与真实选路匹配
// 都消费它）。在 resolve 末端按输入模型的 resolved canonical_id 二次过滤。
// 不动 SQL，不破坏 alias 展开路径（dot↔dash、provider 前缀仍生效）。
//
// 实现：
//   - resolveInputCanonicalID：用 *不*剥包装词的变体矩阵查 models_canonical
//     拿"用户输入"对应的 canonical_id（不污染）。
//   - filterResolveCandidatesByCid：candidates.canonical_id 必须等于输入
//     canonical_id 或为 NULL（legacy 绑定未挂 canonical）。
//   - persistResolveProbe：把 expected canonical_id 透传过去，过滤
//     decision_trace.planned_candidates 不再写入污染凭据。
//
// 回归口径（见 docs/changelogs/2026-09-29-stream-pollution-and-universe-audit.md
// "回归口径" 节）：修后 resolve("X") 的 candidates 集合中
// 所有 candidate.canonical_id 都等于 resolve_input("X") 的 canonical_id
// （或 NULL legacy）。

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// resolveInputCanonicalID looks up the canonical_id that the *input* model
// string resolves to under a STRICT alias matrix — no wrapper-token stripping.
// Returns (canonical_id, found). canonical_id == 0 means "no canonical row
// matched; resolve will keep all candidates unchanged" (legacy fallback).
//
// Why this helper: handleRoutingResolve already feeds the wrapper-stripped
// variants to SQL to keep the cross-form alias expansion intact (so
// "glm-5.3-flash" can still find bindings registered as "glm-5.3"). But
// using the same wrapper-stripped matrix to ALSO look up the input's own
// canonical_id is what causes the pollution — base glm-5.3's canonical_id
// (2422803) gets reported as the answer for "glm-5.3-flash". This helper
// looks up only against the strict variants (no wrapper strip), giving the
// authoritative answer for "what model did the user type".
// 2026-10-01 audit: rewritten so the variant-priority decision is a PURE,
// unit-testable function. The previous single query
//
//	SELECT id FROM models_canonical
//	WHERE lower(canonical_name) = ANY($1) ORDER BY id LIMIT 1
//
// had two defects that the 5 existing unit tests could not see, because every
// one of them exercised filterResolveCandidatesByCid (a pure function) and
// never this DB path:
//
//  1. `= ANY()` is set membership and carries no order, so `ORDER BY id`
//     answered with the numerically smallest id among ALL matched spellings —
//     discarding the exact-form-first priority that
//     NormalizeRouteKeyAliasesNoStrip exists to express. With both
//     'glm-5.3-flash' (id=100) and 'glm-5-3-flash' (id=50) registered, input
//     'glm-5.3-flash' resolved to 50. filterResolveCandidatesByCid would then
//     keep the WRONG canonical's candidates and drop the right ones. That is
//     strictly worse than the pollution being fixed, and the fail-open branch
//     does not cover it because the lookup *succeeded*.
//  2. The function took a *pgxpool.Pool, so it had zero test coverage — the
//     "invariant" the 2026-09-29 changelog claimed could not actually be
//     asserted by anything in the repo. It was only ever checked by an ad-hoc
//     script in /tmp against 11 hand-picked models.
//
// Now the query fetches every matched row and canonicalIDByVariantPriority
// (pure, below) makes the decision, so the collision case is a red-able unit
// test rather than a latent production surprise.
func resolveInputCanonicalID(ctx context.Context, db *pgxpool.Pool, model string) (int64, bool) {
	if db == nil {
		return 0, false
	}
	variants := modelname.NormalizeRouteKeyAliasesNoStrip(model)
	if len(variants) == 0 {
		return 0, false
	}
	// models_canonical stores canonical_name lowercased by migration 396;
	// lower() is a safety net. We deliberately do NOT match model_aliases —
	// the input model must resolve to its OWN canonical, not to an alias of a
	// sibling product (that is exactly the pollution being fixed).
	rows, err := db.Query(ctx, `
		SELECT id, lower(canonical_name)
		FROM models_canonical
		WHERE lower(canonical_name) = ANY($1)
	`, variants)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	matches := make([]canonicalNameID, 0, 4)
	for rows.Next() {
		var r canonicalNameID
		if err := rows.Scan(&r.id, &r.name); err != nil {
			return 0, false
		}
		matches = append(matches, r)
	}
	if err := rows.Err(); err != nil {
		return 0, false
	}
	return canonicalIDByVariantPriority(variants, matches)
}

// canonicalNameID is one (canonical row id, lowercased canonical_name) pair
// as returned by the models_canonical lookup.
type canonicalNameID struct {
	id   int64
	name string
}

// canonicalIDByVariantPriority picks the canonical whose name appears
// EARLIEST in the caller's variant list. variants comes from
// modelname.NormalizeRouteKeyAliasesNoStrip, whose first element is the exact
// normalized input form — so "earliest wins" is "exact form wins", which is
// the contract the whole variant matrix is built around.
//
// Ties (two catalog rows sharing one spelling) resolve to the lowest id, which
// is deterministic. production models_canonical has no duplicate
// canonical_name, so this branch is defensive only.
func canonicalIDByVariantPriority(variants []string, matches []canonicalNameID) (int64, bool) {
	if len(matches) == 0 {
		return 0, false
	}
	rank := make(map[string]int, len(variants))
	for i, v := range variants {
		if _, seen := rank[v]; !seen {
			rank[v] = i
		}
	}
	best := -1
	bestRank := len(variants) + 1
	for i, m := range matches {
		r, ok := rank[m.name]
		if !ok {
			continue // defensive: SQL already restricted to the variant set
		}
		if r < bestRank || (r == bestRank && (best < 0 || m.id < matches[best].id)) {
			best, bestRank = i, r
		}
	}
	if best < 0 {
		return 0, false
	}
	return matches[best].id, true
}

// filterResolveCandidatesByCid drops candidates whose canonical_id is set
// and does NOT match the input's canonical_id. Candidates with NULL
// canonical_id are kept — those are legacy rows whose provider_models row
// has not been linked through migration 541/566 yet.
//
// Returns the filtered slice (in place; aliasing the original backing array
// when nothing matches the drop predicate).
//
// When expectedCid <= 0 the filter is a no-op (no canonical row matched the
// input model; legacy fallback path). The caller must NOT treat expectedCid<=0
// as an error — it just means we cannot prove the pollution and fall back to
// the pre-fix "return all SQL matches" behavior. This keeps the change
// strictly fail-open: if anything goes wrong with the canonical_id lookup,
// the operator-facing diagnostic page still shows everything it showed
// before — operators only lose the new pollution filter, not the candidates.
func filterResolveCandidatesByCid(candidates []resolveCandidate, expectedCid int64) []resolveCandidate {
	if expectedCid <= 0 || len(candidates) == 0 {
		return candidates
	}
	kept := candidates[:0]
	dropped := 0
	for _, c := range candidates {
		if c.CanonicalID != nil && *c.CanonicalID != expectedCid {
			dropped++
			continue
		}
		kept = append(kept, c)
	}
	if dropped > 0 {
		slog.Info("routing resolve: filtered foreign candidates by canonical_id",
			"expected_cid", expectedCid,
			"kept", len(kept),
			"dropped", dropped,
		)
	}
	return kept
}