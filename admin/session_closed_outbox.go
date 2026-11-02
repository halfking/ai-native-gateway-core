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
//
// ★ 2026-11-03（12h 审计 P1-2）两处收口：
//   ① 租户解析不再「LIMIT 1 随便取一行」——public.sessions 按月分区、
//      唯一键是 (session_id, partition_date) ⇒ session_id 不是全局唯一
//      （跨租户可同名、跨月可同名）⇒ 旧口径会把 session.closed.v1 挂到
//      **别的租户**名下（跨租户污染）。改口径：枚举 DISTINCT tenant_id，
//      唯一才采用；多租户同名**放弃发布**（宁可少一条事件，也不可写错租户）。
//   ② 异步 goroutine 补 defer recover()——goroutine 里的未捕获 panic 不像
//      HTTP handler 那样被 net/http 兜住 ⇒ 会**整进程退出**。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
		// ★ 未捕获的 panic 会带走整个进程 ⇒ 兜住并降级成 WARN。
		defer func() {
			if rec := recover(); rec != nil {
				slog.Warn("session closed outbox publish panicked (recovered)",
					"session_id", sessionID, "panic", fmt.Sprint(rec))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := publishSessionClosed(ctx, pool, sessionID, reason); err != nil {
			slog.Warn("session closed outbox publish failed (fail-open)",
				"session_id", sessionID, "error", err)
		}
	}()
}

func publishSessionClosed(ctx context.Context, pool *pgxpool.Pool, sessionID, reason string) error {
	tenantID, err := resolveSessionTenant(ctx, pool, sessionID)
	if err != nil {
		return err
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

// resolveSessionTenant 解析会话的归属租户。
//
// ★ public.sessions 按月分区，唯一键是 (session_id, partition_date)
// ⇒ session_id 不是全局唯一：跨租户可同名、同一租户跨月可同名。
// ⇒ 口径是「枚举 DISTINCT tenant_id」，不是 LIMIT 1 猜一行：
// 0 个候选 ⇒ 会话未落库（纯 Redis 会话）⇒ 返回错误，调用方 fail-open 放弃；
// 1 个候选 ⇒ 唯一归属 ⇒ 采用；
// >1 个候选 ⇒ 多租户同名 ⇒ 返回错误，放弃发布（不猜、不跨租户写）。
//
// 旧实现 `WHERE session_id=$1 LIMIT 1` 在多行时返回任意一行
// ⇒ session.closed.v1 会挂到别的租户名下（event_id 随之错、ASM 投影跟着错）
// ——这正是本函数被重写的原因。
func resolveSessionTenant(ctx context.Context, pool *pgxpool.Pool, sessionID string) (string, error) {
	rows, err := pool.Query(ctx,
		`SELECT DISTINCT tenant_id FROM public.sessions WHERE session_id = $1`,
		sessionID,
	)
	if err != nil {
		return "", fmt.Errorf("resolve tenant for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	var candidates []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return "", fmt.Errorf("scan tenant for session %s: %w", sessionID, err)
		}
		candidates = append(candidates, t)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate tenants for session %s: %w", sessionID, err)
	}
	return pickSessionTenant(sessionID, candidates)
}

// pickSessionTenant 从候选租户集合里挑唯一归属（纯函数 ⇒ 可单测）。
// 歧义与空集都返回错误：宁可少发一条事件，也不可把 A 租户的会话终态
// 写到 B 租户名下。
func pickSessionTenant(sessionID string, candidates []string) (string, error) {
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no tenant row for session %s (session not persisted)", sessionID)
	case 1:
		t := strings.TrimSpace(candidates[0])
		if t == "" {
			return "", fmt.Errorf("empty tenant for session %s", sessionID)
		}
		return t, nil
	default:
		return "", fmt.Errorf("ambiguous tenant for session %s: %v (session_id is not globally unique)",
			sessionID, candidates)
	}
}
