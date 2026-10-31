// Package modelbinding resolves a caller-supplied model name to one raw
// provider-model binding before a credential-scoped state write.
package modelbinding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
		return exact[0].name, nil
	}
	if len(exact) > 1 {
		// 2026-09-06 P0.3 fix: when ambiguous, select the first candidate
		// instead of failing. This allows RestoreOnSuccess hot-path recovery
		// to proceed even when a credential has multiple similar model bindings
		// (e.g., MiniMax-M2.7 and MiniMax-M2.7-highspeed).
		slog.Warn("modelbinding: ambiguous exact model binding, using first candidate",
			"credential_id", credentialID,
			"requested_model", requestedModel,
			"candidates", candidateNames(exact),
			"selected", exact[0].name)
		return exact[0].name, nil
	}

	candidates, err := rawBindingCandidates(ctx, db, credentialID, modelname.NormalizeRouteKeyAliases(requestedModel), false)
	if err != nil {
		return "", err
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("resolve model binding failed: %w (context: credential_id=%d, model=%q)", ErrModelBindingNotFound, credentialID, requestedModel)
	case 1:
		return candidates[0].name, nil
	default:
		// 2026-10-31 批判式审计轮（24h 审计第 11 轮立项卡的实施）：211 生产
		// requested=glm-5.3-flash 每次命中两个绑定（z-ai/glm-5.3-flash 经最特异
		// 变体、z-ai/glm-5.3 经宽泛变体）——SQL 已按 best_rank 排序 ⇒ 首位恒为
		// 特异性胜出者，选择**从来是对的**，但 len>1 一律 WARN ⇒ 每天 200+ 条
		// 噪声告警（第 10 轮 743 条/5.7h），把真正的歧义淹没。第 11 轮实测
		// 「WARN 归零」只是 cred 19 流量退出，条件仍在 ⇒ 归零不可作完成证据。
		// 修法（语义不变，降噪）：首位 best_rank 严格小于次位 ⇒ 特异性确定性
		// 胜出，静默（Debug 留痕）；仅**同分**（best_rank 相等/缺失，归一化变体
		// 真的区分不开）才维持既有 WARN+取首行为。exact 路径（len>1）语义不动。
		if candidates[0].specificThan(candidates[1]) {
			slog.Debug("modelbinding: normalized binding resolved by specificity ranking",
				"credential_id", credentialID,
				"requested_model", requestedModel,
				"normalized", modelname.NormalizeRouteKeyAliases(requestedModel),
				"candidates", candidateNames(candidates),
				"selected", candidates[0].name)
			return candidates[0].name, nil
		}
		normalized := modelname.NormalizeRouteKeyAliases(requestedModel)
		slog.Warn("modelbinding: ambiguous normalized model binding, using first candidate",
			"credential_id", credentialID,
			"requested_model", requestedModel,
			"normalized", normalized,
			"candidates", candidateNames(candidates),
			"selected", candidates[0].name)
		return candidates[0].name, nil
	}
}

// bindingCandidate 是归一化路径的一个候选：raw 绑定名 + 它命中的最特异变体
// 下标（best_rank，1 起且越小越特异；missing 表示 SQL 侧没给出，即历史
// wire 形态/异常行——按「不比任何人特异」处理）。
type bindingCandidate struct {
	name  string
	rank  int
	hasOK bool
}

// specificThan 判断 a 是否以 best_rank 严格胜出 b（a 更特异或 b 无排名）。
// 相等（含双 missing）⇒ 不是特异性胜出 ⇒ 归一化真的区分不开 ⇒ 交回 WARN 路径。
func (a bindingCandidate) specificThan(b bindingCandidate) bool {
	if !a.hasOK {
		return false
	}
	if !b.hasOK {
		return true
	}
	return a.rank < b.rank
}

func candidateNames(cands []bindingCandidate) []string {
	names := make([]string, len(cands))
	for i, c := range cands {
		names[i] = c.name
	}
	return names
}

// rawBindingCandidates 返回归一化路径的候选（按 best_rank 升序、名字稳定序）。
// exact 路径不排 rank，返回 hasOK=false 的纯名字列表——wire 兼容既有形态。
func rawBindingCandidates(ctx context.Context, db DBQuerier, credentialID int, model any, exact bool) ([]bindingCandidate, error) {
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
		// 2026-10-31：wire 从「纯名字」升级为「name\x1frank」对（列表分隔符
		// \x1e）——Go 侧要能区分「特异性胜出」与「真同分」，纯名字序列给不出
		// 这个判据（第 11 轮立项卡的本体）。exact 路径保持纯名字不变。
		query = `
			SELECT COALESCE(array_to_string(ARRAY(
				SELECT ranked.name || E'\x1f' || ranked.best_rank FROM (
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
			), E'\x1e'), '')`
	}
	if err := db.QueryRow(ctx, query, credentialID, model).Scan(&encoded); err != nil {
		return nil, fmt.Errorf("resolve model binding query failed: %w (context: credential_id=%d)", err, credentialID)
	}
	if encoded == "" {
		return nil, nil
	}
	if exact {
		// exact wire：纯名字（\x1f 分隔），无 rank。
		names := strings.Split(encoded, "\x1f")
		out := make([]bindingCandidate, len(names))
		for i, n := range names {
			out[i] = bindingCandidate{name: n}
		}
		return out, nil
	}
	// normalized wire：name\x1frank 对，\x1e 分隔。rank 缺失/非数 ⇒ hasOK=false
	//（按「不比任何人特异」处理，绝不因解析失败放大权限）。
	pairs := strings.Split(encoded, "\x1e")
	out := make([]bindingCandidate, 0, len(pairs))
	for _, pair := range pairs {
		if pair == "" {
			continue
		}
		name, rank, ok := strings.Cut(pair, "\x1f")
		if !ok {
			// 异常形态：没有 rank 段。保守处理为无名次候选。
			out = append(out, bindingCandidate{name: pair})
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(rank))
		if err != nil {
			out = append(out, bindingCandidate{name: name})
			continue
		}
		out = append(out, bindingCandidate{name: name, rank: n, hasOK: true})
	}
	return out, nil
}
