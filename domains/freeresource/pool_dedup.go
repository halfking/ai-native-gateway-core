package freeresource

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// PoolDedupTotals 与 OmniRoute 的 FreeModelTotals 对齐 (open-sse/config/
// freeModelCatalog.ts FreeModelTotals), 仅保留本项目关心的字段.
//
// "Headline" 数 = 跨所有 pool 求和, 但同一 pool_key 内取 MAX 而不是 SUM,
// 这样 OpenRouter 那种 "几十个 :free 模型共享 openrouter-free-pool" 的
// 情况不会被错误累加成几十倍月度配额.
//
// "DedupedModelCount" = catalog 中实际行数, 仅供运维参考.
type PoolDedupTotals struct {
	PoolHeadlineMonthlyTokens int64            `json:"pool_headline_monthly_tokens"`
	PoolHeadlineDailyTokens   int64            `json:"pool_headline_daily_tokens"`
	PoolHeadlineCreditTokens  int64            `json:"pool_headline_credit_tokens"`
	PoolCount                 int              `json:"pool_count"`
	ModelCount                int              `json:"model_count"`
	ByPool                    map[string]int64 `json:"by_pool_monthly_tokens"`
	UncappedPoolCount         int              `json:"uncapped_pool_count"`
}

// ComputePoolDedupTotals 镜像 OmniRoute dedupedSum: 按 pool_key 分组,
// 每组取 MAX(monthly_tokens), 没有 pool_key 的行单独累加.
//
// 输入必须已经过 RLS 过滤 (调用方负责 SET LOCAL app.current_tenant);
// 不在事务内调用时, 函数会自己开一个 read-only tx 让 RLS 在 stdlib 池
// 连接上生效.
func ComputePoolDedupTotals(ctx context.Context, db *sql.DB, tenantID string) (*PoolDedupTotals, error) {
	if db == nil {
		return &PoolDedupTotals{ByPool: map[string]int64{}}, nil
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin tx for pool dedup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if tenantID != "" {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("SET LOCAL app.current_tenant = '%s'", escapeTenant(tenantID))); err != nil {
			return nil, fmt.Errorf("set tenant GUC: %w", err)
		}
	}

	rows, err := tx.QueryContext(ctx, `
        SELECT
            COALESCE(NULLIF(pool_key, ''), '__standalone__') AS pool_key,
            model_id,
            COALESCE(monthly_tokens, 0),
            COALESCE(daily_tokens, 0),
            COALESCE(credit_tokens, 0),
            COALESCE(free_type, '')
        FROM free_resource_catalog
        WHERE enabled = TRUE
        ORDER BY pool_key, model_id
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawRow struct {
		poolKey       string
		modelID       string
		monthlyTokens int64
		dailyTokens   int64
		creditTokens  int64
		freeType      string
	}

	var (
		poolMonthly = map[string]int64{} // pool_key -> MAX(monthly)
		poolDaily   = map[string]int64{}
		poolCredit  = map[string]int64{}
		uncapped    = map[string]struct{}{}
		modelCount  int
	)

	for rows.Next() {
		var r rawRow
		if err := rows.Scan(&r.poolKey, &r.modelID, &r.monthlyTokens, &r.dailyTokens,
			&r.creditTokens, &r.freeType); err != nil {
			return nil, err
		}
		modelCount++
		if r.monthlyTokens > poolMonthly[r.poolKey] {
			poolMonthly[r.poolKey] = r.monthlyTokens
		}
		if r.dailyTokens > poolDaily[r.poolKey] {
			poolDaily[r.poolKey] = r.dailyTokens
		}
		if r.creditTokens > poolCredit[r.poolKey] {
			poolCredit[r.poolKey] = r.creditTokens
		}
		if strings.EqualFold(r.freeType, "recurring-uncapped") {
			uncapped[r.poolKey] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &PoolDedupTotals{
		PoolCount:         len(poolMonthly),
		ModelCount:        modelCount,
		ByPool:            poolMonthly,
		UncappedPoolCount: len(uncapped),
	}
	for _, v := range poolMonthly {
		out.PoolHeadlineMonthlyTokens += v
	}
	for _, v := range poolDaily {
		out.PoolHeadlineDailyTokens += v
	}
	for _, v := range poolCredit {
		out.PoolHeadlineCreditTokens += v
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// SortedPoolKeys 返回按字母排序的 pool key, 给 admin 端点列示用.
func (t *PoolDedupTotals) SortedPoolKeys() []string {
	if t == nil || len(t.ByPool) == 0 {
		return nil
	}
	out := make([]string, 0, len(t.ByPool))
	for k := range t.ByPool {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}