package durable

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ActiveTaskCounts returns the PostgreSQL-authoritative number of non-terminal
// tasks per tenant. Durable workers use this to reconcile gauges after restarts
// and across instances instead of maintaining process-local increments.
func (s *Store) ActiveTaskCounts(ctx context.Context) (map[string]int64, error) {
	counts := map[string]int64{}
	// worker 指标读：跨租户聚合，包显式事务设旁路 GUC（rls.go）。
	err := s.queryWithBypassTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT tenant_id, COUNT(*)
			FROM durable_llm_tasks
			WHERE status NOT IN ('completed', 'failed', 'expired', 'canceled', 'resume_safety_blocked')
			GROUP BY tenant_id`)
		if err != nil {
			return fmt.Errorf("durable: active task counts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var tenant string
			var count int64
			if err := rows.Scan(&tenant, &count); err != nil {
				return fmt.Errorf("durable: scan active task count: %w", err)
			}
			counts[tenant] = count
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}
