// Package modelbinding resolves a caller-supplied model name to one raw
// provider-model binding before a credential-scoped state write.
package modelbinding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/modelname"
)

var (
	ErrModelBindingNotFound  = errors.New("model binding not found")
	ErrAmbiguousModelBinding = errors.New("ambiguous model binding")
)

// DBQuerier is the small database seam needed for model binding resolution.
type DBQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

type ambiguousError struct {
	candidates []string
}

func (e *ambiguousError) Error() string {
	return fmt.Sprintf("resolve model binding failed: %v (context: candidate_raw_models=%s)", ErrAmbiguousModelBinding, strings.Join(e.candidates, ","))
}

func (e *ambiguousError) Unwrap() error { return ErrAmbiguousModelBinding }

// AmbiguousCandidates returns the raw binding names included in an ambiguity error.
func AmbiguousCandidates(err error) []string {
	var target *ambiguousError
	if !errors.As(err, &target) {
		return nil
	}
	return append([]string(nil), target.candidates...)
}

// ResolveRawBinding prefers an exact raw name. A standard, canonical, or alias
// name is accepted only when it identifies one binding for the credential.
func ResolveRawBinding(ctx context.Context, db DBQuerier, credentialID int, requestedModel string) (string, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return "", fmt.Errorf("resolve model binding failed: model name is empty (context: credential_id=%d)", credentialID)
	}

	exact, err := rawBindingCandidates(ctx, db, credentialID, requestedModel, true)
	if err != nil {
		return "", err
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		// 2026-09-06 P0.3 fix: when ambiguous, select the first candidate
		// instead of failing. This allows RestoreOnSuccess hot-path recovery
		// to proceed even when a credential has multiple similar model bindings
		// (e.g., MiniMax-M2.7 and MiniMax-M2.7-highspeed).
		slog.Warn("modelbinding: ambiguous exact model binding, using first candidate",
			"credential_id", credentialID,
			"requested_model", requestedModel,
			"candidates", exact,
			"selected", exact[0])
		return exact[0], nil
	}

	candidates, err := rawBindingCandidates(ctx, db, credentialID, modelname.NormalizeRouteKeyAliases(requestedModel), false)
	if err != nil {
		return "", err
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("resolve model binding failed: %w (context: credential_id=%d, model=%q)", ErrModelBindingNotFound, credentialID, requestedModel)
	case 1:
		return candidates[0], nil
	default:
		// 2026-09-06 P0.3 fix: when ambiguous after normalization, select
		// the first candidate. The normalized path is a fallback when exact
		// matching fails, so ambiguity here is even less critical.
		normalized := modelname.NormalizeRouteKeyAliases(requestedModel)
		slog.Warn("modelbinding: ambiguous normalized model binding, using first candidate",
			"credential_id", credentialID,
			"requested_model", requestedModel,
			"normalized", normalized,
			"candidates", candidates,
			"selected", candidates[0])
		return candidates[0], nil
	}
}

func rawBindingCandidates(ctx context.Context, db DBQuerier, credentialID int, model any, exact bool) ([]string, error) {
	var encoded string
	query := `
		SELECT COALESCE(array_to_string(ARRAY(
			SELECT DISTINCT pm.raw_model_name
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE cmb.credential_id = $1
			  AND pm.raw_model_name = $2
			ORDER BY pm.raw_model_name
		), E'\x1f'), '')`
	if !exact {
		// 2026-10-30 24h 审计轮（211 生产日志实锤）：requested=glm-5.3-flash 的
		// 归一化变体是 [glm-5.3-flash, glm-5-3-flash, glm-5.3, glm-5-3]，
		// 既有 ORDER BY pm.raw_model_name 让 `z-ai/glm-5.3`（经宽泛变体 glm-5.3
		// 命中）排在 `z-ai/glm-5.3-flash`（经最特异变体 glm-5.3-flash 命中）前面
		// ⇒ 字典序把**非 flash** 模型发给了要 flash 的用户（24h 48 次告警）。
		// 修法：候选按「命中的最特异变体下标」排序——变体表按特异性降序排列，
		// 下标越小越特异；同分再按名字稳定排序（不重排完全同名的既有形态）。
		query = `
			SELECT COALESCE(array_to_string(ARRAY(
				SELECT ranked.name FROM (
					SELECT DISTINCT ON (pm.raw_model_name)
						pm.raw_model_name AS name,
						(SELECT min(t.i) FROM unnest($2::text[]) WITH ORDINALITY AS t(v, i)
						  WHERE t.v = pm.raw_model_name
						     OR t.v = pm.standardized_name
						     OR t.v = pm.canonical_raw_name
						     OR t.v = mc.canonical_name
						     OR EXISTS (
						        SELECT 1 FROM model_aliases ma
						        WHERE ma.canonical_id = pm.canonical_id
						          AND ma.raw_name = t.v
						          AND ma.status = 'active'
						     )) AS best_rank
					FROM credential_model_bindings cmb
					JOIN provider_models pm ON pm.id = cmb.provider_model_id
					LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
					WHERE cmb.credential_id = $1
					  AND (
						pm.canonical_raw_name = ANY($2)
						OR pm.standardized_name = ANY($2)
						OR mc.canonical_name = ANY($2)
						OR EXISTS (
							SELECT 1 FROM model_aliases ma
							WHERE ma.canonical_id = pm.canonical_id
							  AND ma.raw_name = ANY($2)
							  AND ma.status = 'active'
						)
					  )
					ORDER BY pm.raw_model_name, best_rank
				) ranked
				ORDER BY ranked.best_rank NULLS LAST, ranked.name
			), E'\x1f'), '')`
	}
	if err := db.QueryRow(ctx, query, credentialID, model).Scan(&encoded); err != nil {
		return nil, fmt.Errorf("resolve model binding query failed: %w (context: credential_id=%d)", err, credentialID)
	}
	if encoded == "" {
		return nil, nil
	}
	return strings.Split(encoded, "\x1f"), nil
}
