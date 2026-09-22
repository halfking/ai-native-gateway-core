// Package discovery — canonical_folded_resolve.go
//
// 2026-09-23 252 PG SQL 日志审计轮：迁移 735
// （uq_models_canonical_active_folded_name，2026-09-21 上线）的表达式唯一
// 索引只保护"折叠名相同"的 active 行，而两个 canonical upsert 点的
// ON CONFLICT (canonical_name) 只能接住 raw 名精确冲突——raw 名不同但折叠名
// 相同的 INSERT 以 23505 爆掉（252 快照 05:06 窗口 32s×18，doubao-1-5-ui-tars
// 折叠对实证），discovery/provider_refresh 的模型同步在该窗口停摆。
// 本文件在 upsert 前按折叠名预解析到既有行，让 upsert 走 ON CONFLICT
// DO UPDATE 的既有合并逻辑（family/tags/disabled 守卫全在那一支）。
package discovery

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/modelcatalog"
	"github.com/kaixuan/llm-gateway-go/modelname"
)

// adoptFoldedExisting 就地重定向 canonicalName/family 到折叠名相等的既有
// active 行（若有）。查询失败 fail-open：保持原值走普通 upsert，最坏情况
// 回落到 ON CONFLICT (canonical_name) 或既有 23505 行为，不阻塞发现流水线。
// 23505 仍可能在并发窗口出现（预解析与 INSERT 之间他方先插）——这与
// admin createModel 的 23505→409 契约同型，属已知残余而非缺陷。
func adoptFoldedExisting(ctx context.Context, db modelcatalog.Querier, canonicalName, family *string) {
	if db == nil || *canonicalName == "" {
		return
	}
	var existing string
	err := db.QueryRow(ctx, modelname.FoldedActiveLookupSQL, *canonicalName).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("folded canonical pre-resolve failed; falling back to plain upsert",
			"canonical_name", *canonicalName, "error", err)
		return
	}
	if existing == "" || existing == *canonicalName {
		return
	}
	slog.Info("folded canonical duplicate resolved to existing row (migration 735)",
		"incoming", *canonicalName, "existing", existing)
	*canonicalName = existing
	if family != nil {
		*family = InferFamily(existing)
	}
}
