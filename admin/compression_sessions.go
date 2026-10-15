package admin

// NOTE: All SELECTs on tenant-scoped tables (_request_logs etc.) in this file
// rely on tenantLogsClause() (admin/session_tenant.go) to inject
// "AND tenant_id = $N" for tenant_admin callers on non-default tenants.
// Super-admin / legacy admin_key / default-tenant callers see all rows.

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// compressionOutboundBodyPredicate 是「这一轮出站消息体存在」的**跨族**判据，
// 替代原先写在 whereClause 里的 `rb.outbound_body IS NOT NULL`（S4 停写轮）。
//
// # 为什么原形态在停写后整体失效（真库实测，本地库 2026-10-14）
//
// 原形态是 `LEFT JOIN request_logs_bodies_with_current_month rb … WHERE
// rb.outbound_body IS NOT NULL` —— LEFT JOIN 的 IS NOT NULL 在 WHERE 里等价于
// 内连接，所以它同时是**过滤器**。而 bodies 腿只指向 v1：停写后新流量只在
// `session_bodies` 家族，于是 `rb` 对新会话恒 NULL、整行被过滤掉。实测口径：
//
//	v1 bodies 带 outbound_body 的行	 7,076（全部停写前）
//	session bodies 带 outbound_body 的行	20,326（全部停写后）
//	710 视图总行				44,121
//	命中旧 whereClause 的行			  811  ← 1.8%，且**没有一行是停写后的**
//
// ⇒ `/api/admin/compression/sessions` 在停写后不是「少一个字段」，而是**整个
// 压缩会话页只剩停写前的历史**，post-flip 会话 100% 不可见。
//
// # 为什么用 EXISTS 而不是把两条 bodies 源 UNION 起来 JOIN
//
// 双写期同一个 request_id 在 v1 bodies 与 session bodies 里**同时存在**
// （本地实测 11,711 个双生 request_id）。UNION ALL 后按 request_id 连接会
// 一对多，直接虚增主查询里的 `COUNT(*) AS request_count`——用 EXISTS 就没有
// 这个问题：判据只回答「有没有」，不改变行的基数。
//
// 两条腿的分工：v1 腿服务停写前历史，会话腿服务停写后新流量；
// 会话腿取源用 db.SessionFamilyBodiesSourceSQL()（hot ∪ parent 单键源，
// 投影里 request_delta 已改名回 request_body，见 §9.231 列合同）。
var compressionOutboundBodyPredicate = `(EXISTS (SELECT 1 FROM request_logs_bodies_with_current_month v
		      WHERE v.request_id = rl.request_id AND v.outbound_body IS NOT NULL)
		 OR EXISTS (SELECT 1 FROM ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` sb
		      WHERE sb.request_id = rl.request_id AND ` + dbpkg.SessionBodiesHasOutboundPredicate("sb") + `))`

// compressionLatestOrigMsgsLATERAL 取该会话「最后一轮带出站正文」的原始消息数。
//
// 语义与停写前**逐字一致**：仍然是 `ORDER BY ts DESC LIMIT 1` 取最新一轮，
// 不是 MAX 聚合（MAX 会把 pre-flip 会话的返回值一并改掉，违反前后一致性）。
// 变化只在候选集从「v1 bodies 单族」扩成「v1 + 会话族」两族。
//
// 真库实测（本地库 2026-10-14，翻转点 2026-10-13 07:01:04）：
//
//	会话数		v1 腿命中	会话腿命中	并集命中
//	停写前 12,467	    811（6.5%）	 11,646	   12,457
//	停写后  7,412	      0（0.0%）	   7,412	    7,412
//
// 并集在两个方向上都不低于原腿；且在原有 811 个已取值会话上**逐个同值**
// （same_value 811 / different_value 0 / 因并集变 NULL 的 0）⇒ 只增不减。
//
// 两族各按自己的键走：v1 腿是 ts 等值（v1 侧 ts 与 request_id 同步，本地
// 实测 35,506/35,506 全等）；会话腿是 request_id 单键（`session_bodies`
// 的 ts 是落库侧时刻，与 turns 侧 99.85% 不等，**禁止**等值，见 §9.231）。
// 会话腿直接查 `session_bodies` 的 session_id 而不是 turns 家族：前者有
// `idx_session_bodies_session (session_id, turn_no DESC)` 供索引，
// LATERAL 每行一次探测；走 turns 家族会退化成每行全族扫描。
var compressionLatestOrigMsgsLATERAL = `
		SELECT jsonb_array_length(COALESCE(c.request_body::jsonb->'messages', '[]'::jsonb)) AS orig_msg_count
		  FROM (
			SELECT rb2.ts AS ts, rb2.request_body AS request_body, 0 AS fam
			  FROM request_logs_with_current_month rl2
			  JOIN request_logs_bodies_with_current_month rb2
			    ON rb2.request_id = rl2.request_id AND rb2.ts = rl2.ts
			 WHERE rl2.gw_session_id = s.gw_session_id
			   AND rb2.outbound_body IS NOT NULL
			UNION ALL
			SELECT sb.ts AS ts, sb.request_delta AS request_body, 1 AS fam
			  FROM (
				SELECT session_id, ts, outbound_body, request_delta FROM public.session_bodies_hot
				UNION ALL
				SELECT session_id, ts, outbound_body, request_delta FROM public.session_bodies
			  ) sb
			 WHERE sb.session_id = s.gw_session_id
			   AND ` + dbpkg.SessionBodiesHasOutboundPredicate("sb") + `
		  ) c
		 ORDER BY c.ts DESC, (c.request_body IS NOT NULL) DESC, c.fam DESC
		 LIMIT 1`

type compressionSessionItem struct {
	GwSessionID         string    `json:"gw_session_id"`
	CompressionStrategy string    `json:"compression_strategy"`
	RequestCount        int       `json:"request_count"`
	FirstTs             time.Time `json:"first_ts"`
	LastTs              time.Time `json:"last_ts"`
	OutboundMsgCount    *int      `json:"outbound_msg_count"`
	OutboundTokenEst    *int      `json:"outbound_token_est"`
	EstimatedOrigMsgs   *int      `json:"estimated_original_msgs"`
	MsgReduction        *int      `json:"msg_reduction"`
	SampleRequestID     string    `json:"sample_request_id"`
}

type compressionSessionsResponse struct {
	Items []compressionSessionItem `json:"items"`
	Count int                      `json:"count"`
}

// compressionSessionsCountSQL / compressionSessionsMainSQL 把两条语句的
// 组合提到包级，理由与本族其它形状钉桩一致：**判据必须与被测逻辑同层**。
// 这两条字符串就是发给 PostgreSQL 的文本（pgx 不解析中间形态），
// 留在 handler 里就只能靠端到端 HTTP 打流量来断言，那是另一种量具。
func compressionSessionsCountSQL(whereClause string) string {
	return `SELECT COUNT(DISTINCT rl.gw_session_id) FROM request_logs_with_current_month rl WHERE ` + whereClause
}

// compressionSessionsMainSQL 见 handleCompressionSessions 里「分页必须排在
// LATERAL 之前」那段实测注释。limitIdx/offsetIdx 是 page_size/offset 的
// 占位符序号（$n / $n+1）。
func compressionSessionsMainSQL(whereClause string, limitIdx int) string {
	return `
		SELECT
			s.gw_session_id,
			s.compression_strategy,
			s.request_count,
			s.first_ts,
			s.last_ts,
			s.outbound_msg_count,
			s.outbound_token_est,
			s.sample_request_id,
			COALESCE(latest.orig_msg_count, 0) AS estimated_original_msgs
		FROM (
			SELECT paged.*
			FROM (
				SELECT
					rl.gw_session_id,
					MAX(rl.compression_strategy) AS compression_strategy,
					COUNT(*) AS request_count,
					MIN(rl.ts) AS first_ts,
					MAX(rl.ts) AS last_ts,
					MAX(rl.outbound_msg_count) AS outbound_msg_count,
					MAX(rl.outbound_token_est) AS outbound_token_est,
					MAX(rl.request_id) AS sample_request_id
				FROM request_logs_with_current_month rl
				WHERE ` + whereClause + `
				GROUP BY rl.gw_session_id
			) paged
			ORDER BY paged.last_ts DESC
			LIMIT $` + strconv.Itoa(limitIdx) + ` OFFSET $` + strconv.Itoa(limitIdx+1) + `
		) s
		LEFT JOIN LATERAL (` + compressionLatestOrigMsgsLATERAL + `
		) latest ON true
		ORDER BY s.last_ts DESC`
}

func (h *Handler) handleCompressionSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	hours := queryInt(r, "hours", 24)
	if hours < 1 {
		hours = 1
	}
	if hours > 720 {
		hours = 720
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	strategyFilter := r.URL.Query().Get("strategy")
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 50)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var from, to time.Time
	if fromStr != "" {
		var err error
		from, err = time.Parse(time.RFC3339, fromStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid from")
			return
		}
	} else {
		to = time.Now().UTC()
		from = to.Add(-time.Duration(hours) * time.Hour)
	}
	if toStr != "" {
		var err error
		to, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid to")
			return
		}
	} else if fromStr != "" {
		to = time.Now().UTC()
	}

	tenantFilter := IsTenantAdmin(r)

	whereClause := `rl.ts >= $1 AND rl.ts <= $2 AND ` + compressionOutboundBodyPredicate + ` AND rl.gw_session_id IS NOT NULL AND ($3 OR rl.success)`
	args := []any{from, to, !tenantFilter}
	argIdx := 4
	tenantFrag, tenantArgs, nextArg := tenantLogsClause(r, argIdx)
	if tenantFrag != "" {
		whereClause += tenantFrag
		args = append(args, tenantArgs...)
		argIdx = nextArg
	}

	if strategyFilter != "" {
		whereClause += ` AND rl.compression_strategy = $` + strconv.Itoa(argIdx)
		args = append(args, strategyFilter)
		argIdx++
	}

	// Count distinct sessions
	countSQL := compressionSessionsCountSQL(whereClause)
	var totalCount int
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&totalCount); err != nil {
		slog.Warn("compression_sessions count query failed", "error", err)
		writeJSON(w, http.StatusOK, compressionSessionsResponse{Items: make([]compressionSessionItem, 0), Count: 0})
		return
	}

	// Main query with LATERAL to get latest request's orig msg count
	//
	// 分页**必须排在 LATERAL 之前**（S4 停写轮实测修正）：原形态是
	// `… ) s LEFT JOIN LATERAL (…) latest ON true ORDER BY s.last_ts DESC
	// LIMIT $n`，于是 LATERAL 要对**全部分组会话**求值才轮到 LIMIT 生效。
	// 停写前过滤后只剩千级会话、看起来够快；停写后谓词改跨族（见
	// compressionOutboundBodyPredicate），24h 窗内候选从 1,125 涨到 9,255，
	// 真库实测 MAIN 从 ~0.3s 涨到 ~9.9s——已经贴着本端点 10s 超时。
	//
	// LATERAL 是 `ON true` 的一对一连接，不改变行的基数与排序，所以
	// 「先分页再连接」与「先连接再分页」结果**逐字相同**，只是把探测次数
	// 从全量分组数降到 page_size。实测同窗 MAIN 9.9s → 0.28s。
	mainSQL := compressionSessionsMainSQL(whereClause, argIdx)
	args = append(args, pageSize, offset)

	rows, err := h.db.Query(ctx, mainSQL, args...)
	if err != nil {
		slog.Warn("compression_sessions query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	items := make([]compressionSessionItem, 0)
	for rows.Next() {
		var item compressionSessionItem
		var firstTs, lastTs time.Time
		if err := rows.Scan(
			&item.GwSessionID,
			&item.CompressionStrategy,
			&item.RequestCount,
			&firstTs,
			&lastTs,
			&item.OutboundMsgCount,
			&item.OutboundTokenEst,
			&item.SampleRequestID,
			&item.EstimatedOrigMsgs,
		); err != nil {
			slog.Warn("compression_sessions scan failed", "error", err)
			continue
		}
		item.FirstTs = firstTs
		item.LastTs = lastTs
		if item.EstimatedOrigMsgs != nil && item.OutboundMsgCount != nil {
			red := *item.EstimatedOrigMsgs - *item.OutboundMsgCount
			if red < 0 {
				red = 0
			}
			item.MsgReduction = &red
		}
		if item.GwSessionID != "" {
			items = append(items, item)
		}
	}
	// 压缩会话页少一截 → count 与 items 对不上，分页到底判断失真。
	if writeAggRowsErr(w, "compression.sessions", rows.Err()) {
		return
	}

	if items == nil {
		items = make([]compressionSessionItem, 0)
	}

	writeJSON(w, http.StatusOK, compressionSessionsResponse{
		Items: items,
		Count: totalCount,
	})
}
