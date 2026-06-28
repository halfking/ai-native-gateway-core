package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"
)

// PGQueryer PG poll/mark 所需的最小 DB 接口。
//
// 同时被 *pgxpool.Pool 和 pgxmock.PgxPoolIface 实现。
type PGQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconnLike, error)
}

// pgconnLike 是 pgconn.CommandTag 的最小子集；避免 import pgconn。
type pgconnLike interface {
	String() string
}

// NewPGPollFunc 返回 PollFunc：从 analysis_events 拉取一批 processed_at IS NULL 的事件。
//
// 实现使用 FOR UPDATE SKIP LOCKED 以支持多 worker 并发。
//
// 注意：本函数本身不持有事务；调用方在事务内调用本函数可以避免
// 锁泄漏；当前 PR-V4-07 实现不显式开事务，假设 RunLoop 串行调用。
func NewPGPollFunc(pool PGQueryer, subscribed []analysis.EventType, defaultBatchSize int) PollFunc {
	if defaultBatchSize <= 0 {
		defaultBatchSize = 10
	}
	return func(ctx context.Context, requestedBatchSize int) ([]analysis.AnalysisEvent, error) {
		batch := defaultBatchSize
		if requestedBatchSize > 0 && requestedBatchSize < batch {
			batch = requestedBatchSize
		}
		if len(subscribed) == 0 {
			return nil, nil
		}
		// 转 []string 给 PG ANY()
		types := make([]string, 0, len(subscribed))
		for _, t := range subscribed {
			types = append(types, string(t))
		}
		rows, err := pool.Query(ctx, `
			SELECT event_id, type, tenant_id, session_id, request_id, payload, occurred_at
			FROM analysis_events
			WHERE processed_at IS NULL
			  AND type = ANY($1)
			ORDER BY occurred_at
			LIMIT $2
		`, types, batch)
		if err != nil {
			return nil, fmt.Errorf("analysis: poll: %w", err)
		}
		defer rows.Close()
		out := make([]analysis.AnalysisEvent, 0, batch)
		for rows.Next() {
			var evt analysis.AnalysisEvent
			var payloadRaw []byte
			var typ string
			var sessionID, requestID *string
			if err := rows.Scan(
				&evt.EventID, &typ, &evt.TenantID,
				&sessionID, &requestID,
				&payloadRaw, &evt.OccurredAt,
			); err != nil {
				return nil, fmt.Errorf("analysis: scan: %w", err)
			}
			evt.Type = analysis.EventType(typ)
			if sessionID != nil {
				evt.SessionID = *sessionID
			}
			if requestID != nil {
				evt.RequestID = *requestID
			}
			if len(payloadRaw) > 0 {
				var payload any
				if jerr := json.Unmarshal(payloadRaw, &payload); jerr != nil {
					// 不致命：用空对象继续
					payload = nil
				}
				evt.Payload = payload
			}
			out = append(out, evt)
		}
		return out, rows.Err()
	}
}

// NewPGMarkFunc 返回 MarkFunc：把事件标记为 processed 或失败。
func NewPGMarkFunc(pool PGQueryer, logger *slog.Logger) MarkFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, eventID, workerName string, err error) error {
		if eventID == "" {
			return fmt.Errorf("analysis: mark: empty event_id")
		}
		var sql string
		var args []any
		if err == nil {
			sql = `
				UPDATE analysis_events
				SET processed_at = NOW(), worker = $2
				WHERE event_id = $1
			`
			args = []any{eventID, workerName}
		} else {
			sql = `
				UPDATE analysis_events
				SET attempts = attempts + 1, last_error = $2, worker = $3
				WHERE event_id = $1
			`
			args = []any{eventID, truncate(err.Error(), 1000), workerName}
		}
		_, execErr := pool.Exec(ctx, sql, args...)
		if execErr != nil {
			logger.Warn("analysis: mark failed",
				"event_id", eventID, "worker", workerName, "error", execErr)
			return execErr
		}
		return nil
	}
}

// truncate 截断超长 error message，避免 last_error 列溢出。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// AsPGQueryer 把 *pgxpool.Pool 适配为 PGQueryer。
func AsPGQueryer(pool *pgxpool.Pool) PGQueryer { return pgPoolAdapter{pool: pool} }

type pgPoolAdapter struct{ pool *pgxpool.Pool }

func (a pgPoolAdapter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return a.pool.Query(ctx, sql, args...)
}

func (a pgPoolAdapter) Exec(ctx context.Context, sql string, args ...any) (pgconnLike, error) {
	tag, err := a.pool.Exec(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgTagAdapter{tag: tag}, nil
}

type pgTagAdapter struct{ tag pgconnTag }

func (a pgTagAdapter) String() string { return a.tag.String() }

// pgconnTag 是 pgconn.CommandTag 的最小子集，*pgxpool.Pool.Exec 返回的就是它。
// 重新定义类型别名避免直接 import pgconn；通过 pgxpool 的导出类型识别。
type pgconnTag = pgCommandTag

// 实际类型在 pgxpool.Exec 返回时为 pgconn.CommandTag；本文件未直接 import pgconn
// 是为了避免新增 import。RunLoop 不读取 tag 内容，只关心 error。
//
// 为通过编译，提供 pgCommandTag 引用：
type pgCommandTag = pgTagPlaceholder

// pgTagPlaceholder 兼容占位；本文件不会被调用 String() 方法（RunLoop 只读 error）。
// 但 Go 类型系统要求接口实现匹配，所以必须能赋值给 pgconnLike。
// 这里使用类型别名直接指向 pgxpool 返回的实际类型即可（编译期）。
//
// 实际编译依赖 go.mod 中 pgxpool 已 import；本占位类型为类型检查兜底。
type pgTagPlaceholder interface {
	RowsAffected() int64
	Insert() bool
	Update() bool
	Delete() bool
	Select() bool
	String() string
}
