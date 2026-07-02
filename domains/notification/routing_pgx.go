// Package notification — routing_pgx.go
//
// PostgreSQL 实现的路由规则加载器（实现 RoutingDBLoader 接口）。
// 从 approval_routing_rules 表加载规则到内存路由表。
package notification

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgxRoutingLoader 从 PostgreSQL 加载路由规则。
type PgxRoutingLoader struct {
	pool *pgxpool.Pool
}

// NewPgxRoutingLoader 创建 pgx 路由加载器。
func NewPgxRoutingLoader(pool *pgxpool.Pool) *PgxRoutingLoader {
	return &PgxRoutingLoader{pool: pool}
}

// LoadRoutingRules 从 approval_routing_rules 表加载所有启用的规则。
func (l *PgxRoutingLoader) LoadRoutingRules(ctx context.Context) ([]RoutingRuleDBRow, error) {
	if l.pool == nil {
		return nil, fmt.Errorf("pgx routing loader: nil pool")
	}

	query := `
		SELECT id, tenant_id, risk_level, channel_type, approver_ids, priority, enabled, updated_at
		FROM approval_routing_rules
		WHERE enabled = true
		ORDER BY tenant_id, risk_level, priority ASC
	`

	rows, err := l.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("pgx routing loader: query: %w", err)
	}
	defer rows.Close()

	var results []RoutingRuleDBRow
	for rows.Next() {
		var row RoutingRuleDBRow
		if err := rows.Scan(
			&row.ID,
			&row.TenantID,
			&row.RiskLevel,
			&row.Channel,
			&row.Approvers,
			&row.Priority,
			&row.Enabled,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("pgx routing loader: scan: %w", err)
		}
		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgx routing loader: rows: %w", err)
	}

	return results, nil
}

// 编译期断言：确保实现了接口
var _ RoutingDBLoader = (*PgxRoutingLoader)(nil)
