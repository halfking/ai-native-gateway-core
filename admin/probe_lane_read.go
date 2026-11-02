package admin

// probe_lane_read.go —— 探测流量物理隔离车道的**读侧**（2026-11-02）。
//
// # 为什么需要这个文件
//
// 物理隔离车道开启（LLM_GATEWAY_PROBE_LANE_PHYSICAL=1）后，探测流量的
// 落点从业务表换成了探测族表：
//
//	写侧：domains/hooks/observability/telemetry/client.go
//	      → internal/probelane.PersistTurn → session_turns_probe_hot
//	统计：domains/stats/minute_accumulator.go
//	      → request_stats_probe_minute
//	实时流：admin/live_stream_redis_store.go → llmgw:live:probe:* 键空间
//
// ★ 审计发现（2026-11-02）：这三个产物**都只有写、没有读** ⇒
//   探测流量被物理隔离出去之后，运维**没有任何路径**能再看到它：
//   · /api/logs?lane=probe 读的是**业务表**（rl 别名）上的历史探测行
//     （admin/analytics.go 的 probeRequestFilter 注释自陈「物理分表开启前
//     读业务表上的历史探测行」）⇒ 隔离开启后新探测**不在业务表** ⇒ 读不到；
//   · 现有 /api/admin/probe/* 端点族读的是**探测运行器自己的状态表**
//     （node_probe_state / credential_probe_queue / model_probe_runs_hot）
//     ⇒ 那是「探针任务状态」，不是「探针产生的请求流水」；
//   · 实时流的 Replay / discoverDimensionQueues 只认业务键
//     （llmgw:live:main / llmgw:live:dim:）⇒ 探测键空间写进去也取不出。
//
// ⇒ 本文件补**最小读侧**：一个只读端点，把 session_turns_probe_hot 的行
//   暴露给运维。刻意**不做**的事（避免动业务读路径的冻结契约）：
//   · 不把探测族并进 /api/logs —— 它的读面走 session_turns 两臂原生投影
//     （118 列冻结 + 分区裁剪 + api_keys JOIN，见 admin/logs_turns_source.go），
//     而探测族是**窄列 25 列、无 session_id / partition_date** ⇒ 塞进去会
//     牵动一整套被冻结的读面机制；
//   · 不读实时流的探测键空间 —— 那是 SSE 频道分离（P3 的另一件事），
//     与本端点的「查探测流水」用途不同，混在一起会让两者的失败语义纠缠。
//
// ★ 口径：这里的行集合 = 写侧的分流集合（internal/probelane.ShouldIsolate
//   的判定）⇒ 读到的行与「被判定为探测而分流出去的行」严格一致。
//   判定口径的 SSOT 在 internal/probekind，写侧与 stats 两臂同源，
//   见 internal/probelane/probelane.go 的 ShouldIsolate 注释。

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/probelane"
)

// ProbeLaneRequestRow 是 session_turns_probe_hot 的一行（窄投影）。
//
// 字段名与该表列名一一对应；可空列用指针表达（nil = SQL NULL）。
type ProbeLaneRequestRow struct {
	RequestID        string    `json:"request_id"`
	TS               time.Time `json:"ts"`
	TenantID         string    `json:"tenant_id"`
	CredentialID     *string   `json:"credential_id,omitempty"`
	RawModelName     *string   `json:"raw_model_name,omitempty"`
	ClientModel      *string   `json:"client_model,omitempty"`
	OutboundModel    *string   `json:"outbound_model,omitempty"`
	Success          bool      `json:"success"`
	StatusCode       *int      `json:"status_code,omitempty"`
	ErrorKind        *string   `json:"error_kind,omitempty"`
	LatencyMs        *int      `json:"latency_ms,omitempty"`
	PromptTokens     *int      `json:"prompt_tokens,omitempty"`
	CompletionTokens *int      `json:"completion_tokens,omitempty"`
	TotalTokens      *int      `json:"total_tokens,omitempty"`
	OriginStage      *string   `json:"origin_stage,omitempty"`
	OriginActor      *string   `json:"origin_actor,omitempty"`
	TaskType         *string   `json:"task_type,omitempty"`
	APIKeyID         *string   `json:"api_key_id,omitempty"`
	ProviderID       *int      `json:"provider_id,omitempty"`
	ClientIP         *string   `json:"client_ip,omitempty"`
	RequestPreview   *string   `json:"request_preview,omitempty"`
	GwSessionID      *string   `json:"gw_session_id,omitempty"`
	AgentName        *string   `json:"agent_name,omitempty"`
	QualityFlags     []string  `json:"quality_flags,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// probeLaneRequestLimitMax 是 limit 参数的上限。
//
// 刻意设上限：这个端点扫的是**热表**（无分区裁剪、无 TTL 清理作业的
// 物化视图），无界 limit 会把一次运维查询变成一次全表扫描。
const probeLaneRequestLimitMax = 500

// probeLaneDefaultWindow 是未显式给 from 时的默认回看窗口。
const probeLaneDefaultWindow = 24 * time.Hour

// handleProbeLaneRequests serves GET /api/admin/probe/lane/requests.
//
// Query params:
//   - from  RFC3339 下界（缺省 = now - 24h）
//   - to    RFC3339 上界（缺省 = now）
//   - limit 单次返回上限（缺省 100，上限 500）
//   - tenant_id  可选，精确匹配
//   - stage     可选，按 origin_stage 精确匹配
//
// 返回 {"rows": [...], "total": N, "lane_enabled": bool}。
//
// ★ lane_enabled 显式回传：隔离开关**默认关** ⇒ 表恒为空 ⇒
//
//	「查到 0 行」有两种完全不同的含义（车道没开 / 车道开了但最近没探测）。
//	不回传这个字段，运维会把「0 行」读成「没有探测发生」——而真相是
//	车道没开时探测流量**根本不在这张表里**（它在业务表里）。
func (h *Handler) handleProbeLaneRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	now := time.Now().UTC()
	from := now.Add(-probeLaneDefaultWindow)
	if v := strings.TrimSpace(r.URL.Query().Get("from")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
		from = t.UTC()
	}
	to := now
	if v := strings.TrimSpace(r.URL.Query().Get("to")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be RFC3339")
			return
		}
		to = t.UTC()
	}
	if from.After(to) {
		writeError(w, http.StatusBadRequest, "from must not be after to")
		return
	}

	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > probeLaneRequestLimitMax {
			n = probeLaneRequestLimitMax
		}
		limit = n
	}

	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	stage := strings.TrimSpace(r.URL.Query().Get("stage"))

	rows, err := queryProbeLaneRequests(r.Context(), h.db, from, to, tenantID, stage, limit)
	if err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"rows":         rows,
		"total":        len(rows),
		"lane_enabled": probelane.PhysicalEnabled(),
		"from":         from,
		"to":           to,
	})
}

// handleProbeLaneLiveRequests serves GET /api/admin/probe/lane/live.
//
// 读侧的第二半：实时流 Redis 的探测键空间。
//
// ★ 为什么需要它：物理隔离开启时探测 tile 写进独立键空间
//
//	（llmgw:live:probe:* / llmgw:live:tenant:<id>:probe:*，见
//	admin/live_stream_redis_store.go 的 liveRequestQueueKeys）⇒
//	读业务 main 队列**读不到探测 tile** ⇒ 探测在实时流里一进不出。
//
// ★ 详情 hash 键**不分流**（llmgw:live:req:<id> 与租户 req:<id> 无条件写）
//
//	⇒ 只需切队列键，详情解析完全复用（LiveStreamRedisStore.ReplayLane）。
//
// Query params:
//   - tenant_id 可选（租户视角必填；超管可省略读全局）
//   - limit    可选，上限 500
//
// 返回 {"requests": [...], "total": N, "lane_enabled": bool}。
func (h *Handler) handleProbeLaneLiveRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.liveStreamRedisStore == nil {
		writeError(w, http.StatusServiceUnavailable, "live stream store not configured")
		return
	}

	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > probeLaneRequestLimitMax {
			n = probeLaneRequestLimitMax
		}
		limit = n
	}

	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	isSuper := !IsTenantAdmin(r)

	items, err := h.liveStreamRedisStore.ReplayLane(r.Context(), tenantID, isSuper, limit, "probe")
	if err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}
	if items == nil {
		items = []LiveRequest{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"requests":     items,
		"total":        len(items),
		"lane_enabled": probelane.PhysicalEnabled(),
	})
}

// handleProbeLaneMinuteStats serves GET /api/admin/probe/lane/stats.
//
// 读侧第三半：探测分钟聚合表 request_stats_probe_minute。
//
// ★ 为什么需要它：它是 request_stats_minute 的**平行表**（同形状、同键
//
//	(bucket, tenant_id, provider_id, canonical_id)），业务看板读的是业务表
//	⇒ 隔离开启后探测的 QPS / 成功率**在看板上完全看不到**（探测流量被
//	分流出去，而看板只读业务表）⇒ 运维无法回答「探测还活着吗」。
//
// ★ 口径：本端点返回的 requests / success_count / failure_count /
//
//	latency_ms_sum 与业务表同义（同一套列、同一个键）⇒ 两者可直接对比。
//	但**不可**与业务表相加（那是两种流量，物理隔离的目的正是分开）。
func (h *Handler) handleProbeLaneMinuteStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	now := time.Now().UTC()
	from := now.Add(-probeLaneDefaultWindow)
	if v := strings.TrimSpace(r.URL.Query().Get("from")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
		from = t.UTC()
	}
	to := now
	if v := strings.TrimSpace(r.URL.Query().Get("to")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be RFC3339")
			return
		}
		to = t.UTC()
	}
	if from.After(to) {
		writeError(w, http.StatusBadRequest, "from must not be after to")
		return
	}

	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))

	stats, err := queryProbeLaneMinuteStats(r.Context(), h.db, from, to, tenantID)
	if err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"stats":        stats,
		"lane_enabled": probelane.PhysicalEnabled(),
		"from":         from,
		"to":           to,
	})
}

// ProbeLaneMinuteStats is the aggregate over request_stats_probe_minute.
type ProbeLaneMinuteStats struct {
	Requests         int64   `json:"requests"`
	SuccessCount     int64   `json:"success_count"`
	FailureCount     int64   `json:"failure_count"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CreditsCharged   int64   `json:"credits_charged"`
	CostUSD          float64 `json:"cost_usd"`
	LatencyMsSum     int64   `json:"latency_ms_sum"`
	// AvgLatencyMs 是单请求平均延迟（latency_ms_sum / requests）。
	// 分母为 0 时为 0（不是 NaN/Inf）。
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// queryProbeLaneMinuteStats aggregates request_stats_probe_minute over a window.
func queryProbeLaneMinuteStats(
	ctx context.Context,
	db *pgxpool.Pool,
	from, to time.Time,
	tenantID string,
) (ProbeLaneMinuteStats, error) {
	const q = `
		SELECT COALESCE(SUM(requests), 0)::bigint,
		       COALESCE(SUM(success_count), 0)::bigint,
		       COALESCE(SUM(failure_count), 0)::bigint,
		       COALESCE(SUM(prompt_tokens), 0)::bigint,
		       COALESCE(SUM(completion_tokens), 0)::bigint,
		       COALESCE(SUM(total_tokens), 0)::bigint,
		       COALESCE(SUM(credits_charged), 0)::bigint,
		       COALESCE(SUM(cost_usd), 0),
		       COALESCE(SUM(latency_ms_sum), 0)::bigint
		  FROM request_stats_probe_minute
		 WHERE bucket >= $1 AND bucket <= $2
		   AND ($3 = '' OR tenant_id = $3)`

	var s ProbeLaneMinuteStats
	err := db.QueryRow(ctx, q, from, to, tenantID).Scan(
		&s.Requests, &s.SuccessCount, &s.FailureCount,
		&s.PromptTokens, &s.CompletionTokens, &s.TotalTokens,
		&s.CreditsCharged, &s.CostUSD, &s.LatencyMsSum,
	)
	if err != nil {
		return s, err
	}
	if s.Requests > 0 {
		s.AvgLatencyMs = float64(s.LatencyMsSum) / float64(s.Requests)
	}
	return s, nil
}

// queryProbeLaneRequests reads rows from session_turns_probe_hot.
//
// ★ 谓词只带 ts 窗口 + 可选 tenant/stage ⇒ 与业务读面**完全解耦**
//
//	（不 JOIN api_keys、不依赖视图列序、不碰 partition_date 冻结契约）。
//
// ★ 时间列用 ts（不是 created_at）：ts 是请求发生时间，
//
//	created_at 是行落库时间（默认 now()）⇒ 运维按「请求发生在什么时候」
//	查时用 ts 才准。
func queryProbeLaneRequests(
	ctx context.Context,
	db *pgxpool.Pool,
	from, to time.Time,
	tenantID, stage string,
	limit int,
) ([]ProbeLaneRequestRow, error) {
	const q = `
		SELECT request_id, ts, tenant_id, credential_id, raw_model_name,
		       client_model, outbound_model, success, status_code, error_kind,
		       latency_ms, prompt_tokens, completion_tokens, total_tokens,
		       origin_stage, origin_actor, task_type, api_key_id, provider_id,
		       client_ip, request_preview, gw_session_id, agent_name,
		       quality_flags, created_at
		  FROM session_turns_probe_hot
		 WHERE ts >= $1 AND ts <= $2
		   AND ($3 = '' OR tenant_id = $3)
		   AND ($4 = '' OR origin_stage = $4)
		 ORDER BY ts DESC
		 LIMIT $5`

	rows, err := db.Query(ctx, q, from, to, tenantID, stage, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ProbeLaneRequestRow, 0, limit)
	for rows.Next() {
		var r ProbeLaneRequestRow
		var qualityFlags []string
		if err := rows.Scan(
			&r.RequestID, &r.TS, &r.TenantID, &r.CredentialID, &r.RawModelName,
			&r.ClientModel, &r.OutboundModel, &r.Success, &r.StatusCode, &r.ErrorKind,
			&r.LatencyMs, &r.PromptTokens, &r.CompletionTokens, &r.TotalTokens,
			&r.OriginStage, &r.OriginActor, &r.TaskType, &r.APIKeyID, &r.ProviderID,
			&r.ClientIP, &r.RequestPreview, &r.GwSessionID, &r.AgentName,
			&qualityFlags, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		r.QualityFlags = qualityFlags
		out = append(out, r)
	}
	return out, rows.Err()
}
