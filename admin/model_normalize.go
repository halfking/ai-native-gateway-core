package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/discovery"
)

// normalizeModelName maps raw outbound/client model strings to a stable
// canonical key for analytics grouping (heatmap cols, funnel, decisions).
func normalizeModelName(raw string) string {
	return discovery.NormalizeModelName(strings.TrimSpace(raw))
}

type modelAliasIndex struct {
	rawToCanon  map[string]string
	canonToRaws map[string][]string
}

func loadModelAliasIndex(ctx context.Context, db *pgxpool.Pool) (*modelAliasIndex, error) {
	idx := &modelAliasIndex{
		rawToCanon:  map[string]string{},
		canonToRaws: map[string][]string{},
	}
	rows, err := db.Query(ctx, `
		SELECT mc.canonical_name, COALESCE(ma.raw_name, mc.canonical_name) AS raw_name
		FROM models_canonical mc
		LEFT JOIN model_aliases ma ON ma.canonical_id = mc.id AND ma.status = 'active'
		WHERE mc.status = 'active'
	`)
	if err != nil {
		return idx, err
	}
	defer rows.Close()
	for rows.Next() {
		var canon, raw string
		if err := rows.Scan(&canon, &raw); err != nil {
			// R67-A 修正：这里**不能**上抛。两个调用方（analytics.go:356/:435）
			// 都是 `aliasIdx, _ :=` 丢弃错误并继续用 idx，于是上抛的实际效果
			// 不是「让调用方知道」，而是**在第一行坏数据处把整个别名索引截断**，
			// 且这个截断对调用方完全不可见 —— 比原来的「跳过一行」更差：
			// analytics 的 canonical 列会静默退化成 raw 名，看起来像
			// 「模型换了名字」。
			//
			// 正确形态：跳行留痕。迭代中断（下面的 rows.Err()）仍上抛，
			// 因为那是另一种故障——调用方的降级容忍覆盖不到它，且它同样会被
			// 丢弃方吞掉，故在下方注释说明。
			warnRowSkip("modelNormalize.loadModelAliasIndex", err)
			continue
		}
		canon = strings.TrimSpace(canon)
		raw = strings.TrimSpace(raw)
		if canon == "" {
			continue
		}
		idx.canonToRaws[canon] = appendUnique(idx.canonToRaws[canon], raw)
		if raw != "" {
			idx.rawToCanon[strings.ToLower(raw)] = canon
			norm := normalizeModelName(raw)
			if norm != "" {
				idx.rawToCanon[norm] = canon
			}
		}
	}
	if err := rows.Err(); err != nil {
		return idx, fmt.Errorf("load model alias index: iterate rows: %w", err)
	}
	return idx, nil
}

func appendUnique(slice []string, v string) []string {
	for _, s := range slice {
		if s == v {
			return slice
		}
	}
	return append(slice, v)
}

func (idx *modelAliasIndex) canonicalFor(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if idx != nil {
		if c, ok := idx.rawToCanon[strings.ToLower(raw)]; ok {
			return c
		}
		if c, ok := idx.rawToCanon[normalizeModelName(raw)]; ok {
			return c
		}
	}
	return normalizeModelName(raw)
}

func (idx *modelAliasIndex) aliasesFor(canon string) []string {
	if idx == nil {
		return nil
	}
	return idx.canonToRaws[canon]
}

// expandModelFilter returns all raw + canonical names that should match
// a user-supplied model filter (canonical or alias).
func expandModelFilter(ctx context.Context, db *pgxpool.Pool, model string) ([]string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, nil
	}
	idx, err := loadModelAliasIndex(ctx, db)
	if err != nil {
		return []string{model, normalizeModelName(model)}, nil
	}
	canon := idx.canonicalFor(model)
	if canon == "" {
		canon = normalizeModelName(model)
	}
	seen := map[string]struct{}{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		seen[s] = struct{}{}
		seen[normalizeModelName(s)] = struct{}{}
	}
	add(model)
	add(canon)
	for _, raw := range idx.aliasesFor(canon) {
		add(raw)
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out, nil
}
