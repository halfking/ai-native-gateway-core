package admin

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// 会话时间线读端（会话存储解耦 v3 · S3 读端迁移）
//
// 这条查询此前在两处各有一份逐列相同的实现：
//   - admin/session_analytics_handler.go  HandleSessionAnalyticsDetail
//   - admin/session_panorama_handler.go   loadSessionDetailDataInTx
//
// 两份都直读 request_logs 物理表。S4 停写门控
// （storage.request_logs_write_enabled=false）关停后，新会话在这张表里一行都
// 没有，timeline 恒空 —— 但同一响应里的 summary 仍来自 session_summaries 照常
// 返回，接口还是 200。表现是「详情页时间线空白、统计数字却对」，同一响应内自相
// 矛盾，比整体报错更难定位。重复实现还意味着修一处漏一处会留半个故障。
//
// 读 734 视图而非物理表：视图体 = session_turns_hot ∪ session_turns
// （各自 LEFT JOIN 733 特征层）∪ v1 冻结分支 NOT EXISTS 反连接，session 分支已
// 拼装在内，停写后仍供数；镜像链启用之前的历史窗口仍由 v1 分支兜住。列契约经
// 真库 information_schema 核验，15 个投影列 + gw_session_id/tenant_id 全部存在。
//
// S6（DROP request_logs 族）时视图本身会消失，届时需改走
// db.SessionFamilyTurnsSourceSQL() 原生源，并接受镜像链启用前的历史窗口不可见。
// 这一点与 admin/logs_turns_source.go 登记的波1边界同源。
const sessionTimelineQuery = `
	SELECT request_id, ts, success, client_model, outbound_model,
	       COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0),
	       COALESCE(cost_usd,0), COALESCE(latency_ms,0),
	       work_type, compression_strategy, cache_read_tokens,
	       error_kind, request_preview, response_preview
	FROM request_logs_with_current_month
	WHERE gw_session_id = $1`

// sessionTimelineLimit 是两条调用线共用的取数上限。
const sessionTimelineLimit = 100

// loadSessionTimelineInTx 取单个会话的轮次时间线。tenantID 非空时附加租户过滤
// （super admin 传空）。必须在调用方已开启的租户事务里执行，RLS 才有意义。
//
// 无行时返回 nil（不是空切片）—— 两个调用点历史上对空结果的 JSON 形态并不一致
// （analytics 序列化成 null，panorama 序列化成 []），由调用方各自还原，不要在这里
// 统一，否则会悄悄改掉其中一条 API 的响应契约。
func loadSessionTimelineInTx(ctx context.Context, tx pgx.Tx, gwSessionID, tenantID string) ([]RequestEvent, error) {
	query := sessionTimelineQuery
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " ORDER BY ts ASC LIMIT " + strconv.Itoa(sessionTimelineLimit)

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	timeline := []RequestEvent(nil)
	for rows.Next() {
		var e RequestEvent
		var ts time.Time
		if err := rows.Scan(&e.RequestID, &ts, &e.Success, &e.ClientModel, &e.UpstreamModel,
			&e.PromptTokens, &e.CompletionTokens, &e.CostUSD, &e.LatencyMs,
			&e.WorkType, &e.CompressionStrategy, &e.CacheReadTokens,
			&e.ErrorMessage, &e.RequestPreview, &e.ResponsePreview); err != nil {
			return nil, err
		}
		e.CreatedAt = ts
		timeline = append(timeline, e)
	}
	return timeline, rows.Err()
}
