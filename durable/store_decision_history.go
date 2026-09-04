// store_decision_history.go — durable 任务的 DecisionHistory 持久化
// （2026-09-05 审计闭环3）。
//
// 契约：durable_recovery_worker 每次 detached attempt 结束后把追加后的
// 有界 DecisionHistory 写回任务行；下一次（可能在重启后的）接管先读回，
// 作为 AggregateTaskOutcomeWithHistory 的循环检测输入。读写都是
// best-effort：历史丢失/损坏退化为空历史（等价旧行为），不影响任务
// 正确性终态判定。
//
// 写入带 (lease_owner, fencing_token) 条件：历史必须与任务当前持有者
// 一致，0 行影响返回 ErrLeaseLost，旧 worker 不得覆盖新持有者的历史。

package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// LoadDecisionHistory 读回任务的有界决策历史。行不存在或列为 NULL 返回
// nil（空历史）。仅诊断/审计用途的解析错误按空历史处理（fail-open，
// 历史不是正确性门）。
func (s *Store) LoadDecisionHistory(ctx context.Context, taskID string) (json.RawMessage, error) {
	var raw []byte
	err := s.db.QueryRow(ctx,
		`SELECT decision_history FROM durable_llm_tasks WHERE id = $1`,
		taskID).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("durable: load decision history failed: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return json.RawMessage(raw), nil
}

// SaveDecisionHistory 以 fencing 条件保存追加后的决策历史。0 行影响返回
// ErrLeaseLost（调用方必须放弃，不得覆盖新持有者的历史）。
func (s *Store) SaveDecisionHistory(ctx context.Context, taskID, leaseOwner string, fencingToken int64, history json.RawMessage) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE durable_llm_tasks
		    SET decision_history = $4
		  WHERE id = $1 AND lease_owner = $2 AND fencing_token = $3`,
		taskID, leaseOwner, fencingToken, history)
	if err != nil {
		return fmt.Errorf("durable: save decision history failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}
