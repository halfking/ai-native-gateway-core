// Package dispatch — state_transition_logger.go
//
// V3.2 (2026-08-13) BE-B1: 请求状态变更历史记录。
//
// KEEP: V3.2 BE-B1 logger — 路由注册与 route_resolve / streamretry 埋点
// 尚未接入（见 docs/会话优化v3/08-执行记录.md 2026-08-13 条目的 known_gaps）。
// 当前无调用方，属"已就绪待 wire"状态。@v3-team 2026-Q3 review。
//
// 把请求生命周期的关键状态变更（路由决策 / 节点切换 / 重试 / 终态错误）
// 旁路异步写入 request_state_transitions 表（migration 511），供
// GET /api/admin/requests/{id}/transitions 查询，并让首页实时请求流
// 能展示"到达→路由→选节点→切换节点→重试🔄→回复/错误"的完整状态流转。
//
// 设计约束（ADR-V3-001/104）：
//   - 旁路异步：写入不阻塞 relay 主链路；失败只记日志。
//   - 批量：内存缓冲 + 定时 flush，减少 DB 往返。
//   - 关联键：复用 request_id（ADR-V3-102），不造新 ID。
package dispatch

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StateTransition 是一条状态变更记录（对应 request_state_transitions 一行）。
type StateTransition struct {
	RequestID      string	TenantID       string         // 租户 ID (T0: admin, T1-T4: 正常租户)	TransitionType string // route | node_switch | retry | error | state
	FromState      string
	ToState        string
	Metadata       map[string]any // 决策原因/候选列表/retry_seq/reason_class 等
}

// StateTransitionLogger 异步批量写状态变更历史。
// 零值不可用；用 NewStateTransitionLogger 构造。
type StateTransitionLogger struct {
	db *pgxpool.Pool

	mu  sync.Mutex
	buf []StateTransition

	flushInterval time.Duration
	batchSize     int
	stopCh        chan struct{}
	done          chan struct{}
}

// NewStateTransitionLogger 构造 logger。db 为 nil 时所有 Log* 变为 no-op
// （优雅降级：DB 不可用不影响 relay）。
func NewStateTransitionLogger(db *pgxpool.Pool) *StateTransitionLogger {
	l := &StateTransitionLogger{
		db:            db,
		buf:           make([]StateTransition, 0, 64),
		flushInterval: 2 * time.Second,
		batchSize:     100,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
	}
	if db != nil {
		go l.run()
	}
	return l
}

// LogRouteDecision 记录路由决策（route_resolve/route_credential 阶段）。
// metadata 建议含 candidates / block_reason / chosen_credential_id。
func (l *StateTransitionLogger) LogRouteDecision(requestID, tenantID, fromState, toState string, metadata map[string]any) {
	l.add(StateTransition{RequestID: requestID, TenantID: tenantID, TransitionType: "route", FromState: fromState, ToState: toState, Metadata: metadata})
}

// LogNodeSwitch 记录节点切换（failover/sibling 切换）。
// metadata 建议含 from_node / to_node / reason。
func (l *StateTransitionLogger) LogNodeSwitch(requestID, tenantID, fromNode, toNode string, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["from_node"] = fromNode
	metadata["to_node"] = toNode
	l.add(StateTransition{RequestID: requestID, TenantID: tenantID, TransitionType: "node_switch", FromState: fromNode, ToState: toNode, Metadata: metadata})
}

// LogRetry 记录重试（前端特殊标 🔄）。
// metadata 必须含 retry_seq / reason_class（供前端标记）。
func (l *StateTransitionLogger) LogRetry(requestID, tenantID string, retrySeq int, reasonClass string, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["retry_seq"] = retrySeq
	metadata["reason_class"] = reasonClass
	l.add(StateTransition{RequestID: requestID, TenantID: tenantID, TransitionType: "retry", ToState: "retry", Metadata: metadata})
}

// LogError 记录终态错误。
func (l *StateTransitionLogger) LogError(requestID, tenantID, fromState string, metadata map[string]any) {
	l.add(StateTransition{RequestID: requestID, TenantID: tenantID, TransitionType: "error", FromState: fromState, ToState: "error", Metadata: metadata})
}

// add 追加到缓冲；满 batchSize 触发同步 flush（仍旁路：调用方是 relay 的
// 异步钩子，不在请求关键路径上）。
func (l *StateTransitionLogger) add(t StateTransition) {
	if l == nil || l.db == nil || t.RequestID == "" {
		return
	}
	l.mu.Lock()
	l.buf = append(l.buf, t)
	full := len(l.buf) >= l.batchSize
	l.mu.Unlock()
	if full {
		l.Flush()
	}
}

// Flush 把缓冲批量写入 DB。旁路：失败只记日志。
func (l *StateTransitionLogger) Flush() {
	if l == nil || l.db == nil {
		return
	}
	l.mu.Lock()
	if len(l.buf) == 0 {
		l.mu.Unlock()
		return
	}
	batch := make([]StateTransition, len(l.buf))
	copy(batch, l.buf)
	l.buf = l.buf[:0]
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := l.db.Begin(ctx)
	if err != nil {
		slog.Warn("state_transition: begin failed (non-fatal)", "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, t := range batch {
		var metaJSON []byte
		if t.Metadata != nil {
			metaJSON, _ = json.Marshal(t.Metadata)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO request_state_transitions
			  (request_id, tenant_id, transition_type, from_state, to_state, metadata)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			t.RequestID, t.TenantID, t.TransitionType, nullStr(t.FromState), nullStr(t.ToState), metaJSON); err != nil {
			slog.Warn("state_transition: insert failed (non-fatal)",
				"request_id", t.RequestID, "tenant_id", t.TenantID, "type", t.TransitionType, "error", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("state_transition: commit failed (non-fatal)", "error", err)
	}
}

// run 定时 flush。
func (l *StateTransitionLogger) run() {
	ticker := time.NewTicker(l.flushInterval)
	defer ticker.Stop()
	defer close(l.done)
	for {
		select {
		case <-l.stopCh:
			l.Flush()
			return
		case <-ticker.C:
			l.Flush()
		}
	}
}

// Stop 停止后台 flush 并落盘剩余缓冲。
func (l *StateTransitionLogger) Stop() {
	if l == nil || l.db == nil {
		return
	}
	close(l.stopCh)
	<-l.done
}

// CleanupOldTransitions 删除 7 天前的历史（由清理任务周期调用）。
func (l *StateTransitionLogger) CleanupOldTransitions(ctx context.Context) (int64, error) {
	if l == nil || l.db == nil {
		return 0, nil
	}
	tag, err := l.db.Exec(ctx, `
		DELETE FROM request_state_transitions
		WHERE created_at < NOW() - INTERVAL '7 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// nullStr 把空字符串转为 nil（写 NULL）。
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
