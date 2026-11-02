package admin

// session_closed_outbox.go —— serveStopSession 成功后的 session.closed.v1 发布
// （会话治理方案 2026-11 P0-2：ASM 投影据此把会话终态化为 closed）。
//
// 设计约束：
//   - fail-open：发布失败只 log WARN，绝不影响已成功的停止响应。事件的
//     at-least-once 语义由 outbox dispatcher 承接，event_id 确定性派生
//     （BuildSessionClosedEventV1）保证重复发布幂等（ON CONFLICT DO NOTHING）。
//   - tenant 解析走会话族 canonical 表 public.sessions；会话不存在（如从未
//     落库的纯 Redis 会话）时放弃发布并 log——ASM 侧该会话本就无投影行。
//   - 异步 goroutine + 3s 超时：HTTP 响应路径不做 DB 往返。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
)

// publishSessionClosedAsync fire-and-forget 发布 session.closed.v1。
// nil pool（no-DB / lite 模式）时静默跳过。
func publishSessionClosedAsync(pool *pgxpool.Pool, sessionID, reason string) {
	if pool == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := publishSessionClosed(ctx, pool, sessionID, reason); err != nil {
			slog.Warn("session closed outbox publish failed (fail-open)",
				"session_id", sessionID, "error", err)
		}
	}()
}

func publishSessionClosed(ctx context.Context, pool *pgxpool.Pool, sessionID, reason string) error {
	var tenantID string
	if err := pool.QueryRow(ctx,
		`SELECT tenant_id FROM public.sessions WHERE session_id = $1 LIMIT 1`,
		sessionID,
	).Scan(&tenantID); err != nil {
		return fmt.Errorf("resolve tenant for session %s: %w", sessionID, err)
	}

	envelope, err := outbox.BuildSessionClosedEventV1(tenantID, sessionID, reason)
	if err != nil {
		return err
	}
	payloadJSON, err := json.Marshal(envelope.Payload)
	if err != nil {
		return fmt.Errorf("marshal session closed payload: %w", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO outbox_events (
			event_id, event_type, schema_version, tenant_id,
			aggregate_id, aggregate_version, occurred_at, payload, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
		ON CONFLICT (event_id) DO NOTHING
	`,
		envelope.EventID,
		envelope.EventType,
		envelope.SchemaVersion,
		envelope.TenantID,
		envelope.AggregateID,
		envelope.AggregateVersion,
		envelope.OccurredAt,
		// jsonb 参数必须 string 化（SimpleProtocol 下 []byte 必炸，
		// 同 telemetry/client.go insertSessionOpenedEvent 的 R11 FIX-C 注记）。
		string(payloadJSON),
	)
	if err != nil {
		return fmt.Errorf("insert session closed event for session %s: %w", sessionID, err)
	}
	return nil
}
