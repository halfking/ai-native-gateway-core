package durable

import (
	"context"
	"fmt"
)

// ActiveTaskCounts returns the PostgreSQL-authoritative number of non-terminal
// tasks per tenant. Durable workers use this to reconcile gauges after restarts
// and across instances instead of maintaining process-local increments.
func (s *Store) ActiveTaskCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.Query(ctx, `
		SELECT tenant_id, COUNT(*)
		FROM durable_llm_tasks
		WHERE status NOT IN ('completed', 'failed', 'expired', 'canceled', 'resume_safety_blocked')
		GROUP BY tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("durable: active task counts: %w", err)
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var tenant string
		var count int64
		if err := rows.Scan(&tenant, &count); err != nil {
			return nil, fmt.Errorf("durable: scan active task count: %w", err)
		}
		counts[tenant] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("durable: active task count rows: %w", err)
	}
	return counts, nil
}
