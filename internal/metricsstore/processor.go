package metricsstore

import (
	"context"
	"database/sql"
	"log"
	"math"
	"os"
	"time"
)

// Processor 负责从 usage_facts 读取并填充 request_metrics
type Processor struct {
	db          *sql.DB
	writer      *Writer
	gatewayNode string // 当前网关节点ID
}

// NewProcessor 创建 Processor
func NewProcessor(db *sql.DB) *Processor {
	// 获取网关节点ID（从环境变量或主机名）
	gatewayNode := os.Getenv("GATEWAY_NODE_ID")
	if gatewayNode == "" {
		hostname, _ := os.Hostname()
		gatewayNode = hostname
	}

	return &Processor{
		db:          db,
		writer:      NewWriter(db),
		gatewayNode: gatewayNode,
	}
}

// watermark 处理水位线（复合游标）
//
// ★ 为什么是复合游标而不是单一 MAX(occurred_at)：同一时刻可能有多条 usage_facts
//
//	（并发请求时间戳相同）。只用时间戳做 > 比较时，LIMIT 恰好切在两条同时间戳记录中间，
//	第二条会被**永久跳过**（时间戳不再 > 水位线）。加上 request_id 作为次级键后，
//	游标推进到 (t, req_id)，同时间戳里 request_id 更大的记录下一轮仍会被取到 ⇒ 不丢数。
type watermark struct {
	occurredAt time.Time
	requestID  string
	valid      bool
}

// watermarkSQL 读取已落库的最大复合游标。
//
// ★ ORDER BY (occurred_at DESC, request_id DESC) + LIMIT 1：与查询侧的
//
//	(occurred_at, request_id) 游标口径必须完全一致，否则会漏取或重复取。
//
// ★ 配套索引见迁移 983（idx_request_metrics_occurred_cursor），避免每批次全表排序。
const watermarkSQL = `
	SELECT occurred_at, request_id
	FROM request_metrics
	ORDER BY occurred_at DESC, request_id DESC
	LIMIT 1
`

// selectUsageFactsSQL 从 usage_facts 读取待处理记录。
//
// ★ DISTINCT ON (request_id) ... ORDER BY request_id, revision DESC：同一请求取**最大 revision**
//
//	（修订版），而不是只取 revision = 1 —— 否则修订后的用量/费用永远进不了统计表。
//
// ★ WHERE 里的行值比较 (occurred_at, request_id) > ($2, $3)：复合游标，避免同时间戳丢数。
// ★ $1 是「是否已有水位线」标志：首次运行为 false，走全量扫描。
const selectUsageFactsSQL = `
	SELECT
		uf.request_id,
		uf.tenant_id,
		uf.occurred_at,
		uf.provider_id,
		uf.canonical_id,
		uf.raw_model_name,
		uf.api_key_id,
		uf.application_id,
		uf.end_user_id,
		uf.prompt_tokens,
		uf.completion_tokens,
		uf.cache_read_tokens,
		uf.reasoning_tokens,
		uf.image_tokens,
		uf.audio_tokens,
		uf.video_tokens,
		uf.total_tokens,
		uf.cost_amount,
		uf.credits_charged,
		uf.status,
		uf.traffic_class,
		uf.error_kind,
		uf.latency_ms,
		uf.ttft_ms
	FROM (
		SELECT DISTINCT ON (src.request_id) src.*
		FROM usage_facts src
		WHERE (NOT $1::boolean
		       OR (src.occurred_at, src.request_id) > ($2::timestamptz, $3::text))
		ORDER BY src.request_id, src.revision DESC
	) uf
	ORDER BY uf.occurred_at, uf.request_id
	LIMIT $4
`

// enrichDimensionsSQL 查询会话/任务/协议/附件维度。
//
// ★ 列名以活库为准：request_logs 里**没有** session_id、没有 gateway_instance；
//
//	会话键是 gw_session_id，任务键是 gw_task_id（早期版本写的 session_id/gateway_instance
//	会直接报「column does not exist」，于是整条增强静默失败 ⇒ session_id 永远写不进去）。
//
// ★ ORDER BY ts DESC：同一 request_id 可能有多行（重试/多次记录），取最新的一条。
//
// ★★★★ 2026-11-24（合并轮十 · 代上游修）：★ **本查询原先读的是 `request_logs`**
//
//	★ 而那是**迁移 890 已物理 DROP 的 v1 底表**（★ 890 的 DROP 清单含
//	★ `request_logs`／`request_logs_hot`／`request_logs_bodies*`／两张
//	★ `*_without_*` 包装视图）⇒ ★★ **这段代码一旦被接线、运行时必 42P01**
//	★ （relation does not exist）★★（★ 上游 `f18c59e0e` 修的是**列名**、
//	★ ★ **表仍是已退役的那张** ⇒ ★★ **⇒ 上游 `main` 当场仍红在那两道 v1 引用门上**）
//
// ★★ **⇒ 改指【活表】**：★ `request_context_attrs`（★ 直播、★ 不在 890 的 DROP 名单里、
// ★ ★ 带 `gw_session_id`／`gw_task_id`／`client_protocol`）＋ `session_turns`
// ★ （★ 附件数 `attachment_count`、★ ★ **必须读【两腿】**：★ `session_turns_hot` ∪
// ★ `session_turns` ⇒ ★ 只读父表会漏掉仍在 hot 面的行 ⇒ ★ 而且仓里有一道门专管这个
// ★ ⇒ ★★ `admin/session_family_two_surface_test.go` 的 `TestNoBareParentSessionFamilyRead`）★★
// ★ ★ **`upstream_protocol` 在【任何活表里都不存在】**（★ 只在已退役的 `request_logs`
// ★   及其月分区上）⇒ ★★ **本查询显式置 NULL**（★ 目标表该列可空）★ ★ ⇒ ★★ **诚实边界**：
// ★ ★ 上游的「协议：优先客户端协议、回落到上游协议」这条回落**在本仓当前数据面下已失效**
// ★ ★ （★ 客户端协议几乎总有值 ⇒ 回落本就极少触发）★ ★ **⇒ 若产品真的需要上游协议，
// ★ ★ 得先决定它【现在从哪来】** ⇒ ★★ **那是一个数据模型口径问题、★ 不是本轮能替上游定的** ★★。
const enrichDimensionsSQL = `
	SELECT
		rca.gw_session_id,
		rca.gw_task_id,
		(SELECT t.attachment_count FROM (
			SELECT attachment_count FROM public.session_turns_hot t WHERE t.request_id = $1
			UNION ALL
			SELECT attachment_count FROM public.session_turns t WHERE t.request_id = $1
		) t LIMIT 1),
		rca.client_protocol,
		NULL::text AS upstream_protocol
	FROM public.request_context_attrs rca
	WHERE rca.request_id = $1
	ORDER BY rca.ts DESC
	LIMIT 1
`

// ProcessBatch 处理一批记录
func (p *Processor) ProcessBatch(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 100
	}

	// 1. 读取水位线（复合游标）
	wm, err := p.readWatermark(ctx)
	if err != nil {
		return 0, err
	}

	// 2. 从 usage_facts 读取待处理的记录
	rows, err := p.db.QueryContext(ctx, selectUsageFactsSQL,
		wm.valid, wm.occurredAt, wm.requestID, batchSize)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	processed := 0
	metrics := make([]*RequestMetric, 0, batchSize)
	var scanErr error

	for rows.Next() {
		var metric RequestMetric
		var costUSD float64
		var cachedTokens, reasoningTokens, imageTokens, audioTokens, videoTokens sql.NullInt64
		var latencyMs, ttftMs sql.NullInt64
		var promptTokens, completionTokens, totalTokens int64

		err := rows.Scan(
			&metric.RequestID,
			&metric.TenantID,
			&metric.OccurredAt,
			&metric.ProviderID,
			&metric.CanonicalID,
			&metric.OutboundModel,
			&metric.APIKeyID,
			&metric.ApplicationID,
			&metric.UserID,
			&promptTokens,
			&completionTokens,
			&cachedTokens,
			&reasoningTokens,
			&imageTokens,
			&audioTokens,
			&videoTokens,
			&totalTokens,
			&costUSD,
			&metric.CreditsCharged,
			&metric.Status,
			&metric.TrafficClass,
			&metric.ErrorKind,
			&latencyMs,
			&ttftMs,
		)
		if err != nil {
			// ★ 不再「记日志后继续」：继续会把坏行后面的好行写进库，
			//   水位线随之越过这条坏行 ⇒ 坏行被永久跳过（静默丢数）。
			//   这里中止本批（已收集的行照常写），下一轮从原水位线重来，坏行必被重试。
			log.Printf("Error scanning row (batch aborted at request_id=%s): %v", metric.RequestID, err)
			scanErr = err
			break
		}

		metric.PromptTokens = int32(promptTokens)
		metric.CompletionTokens = int32(completionTokens)
		metric.TotalTokens = int32(totalTokens)
		metric.CachedTokens = int32(cachedTokens.Int64)
		metric.ReasoningTokens = int32(reasoningTokens.Int64)
		metric.ImageTokens = int32(imageTokens.Int64)
		metric.AudioTokens = int32(audioTokens.Int64)
		metric.VideoTokens = int32(videoTokens.Int64)
		if latencyMs.Valid {
			v := int32(latencyMs.Int64)
			metric.LatencyMs = &v
		}
		if ttftMs.Valid {
			v := int32(ttftMs.Int64)
			metric.TTFTMs = &v
		}

		// 转换费用：美元 -> 分
		metric.CostCents = usdToCents(costUSD)

		// 设置日期字段（UTC 日历日，与查询侧的 UTC 日裁剪保持同一口径）
		occurredAtUTC := metric.OccurredAt.UTC()
		metric.OccurredDate = time.Date(
			occurredAtUTC.Year(),
			occurredAtUTC.Month(),
			occurredAtUTC.Day(),
			0, 0, 0, 0,
			time.UTC,
		)

		// 记账日期默认等于发生日期
		metric.BillingDate = metric.OccurredDate

		// 设置网关节点ID（从当前处理器）
		metric.GatewayNodeID = &p.gatewayNode

		// 2. 增强维度（查询 session_id、payer_id、team_id 等）
		if err := p.enrichDimensions(ctx, &metric); err != nil {
			log.Printf("Error enriching dimensions for request %s: %v", metric.RequestID, err)
			// 继续处理，不阻断整批
		}

		// 3. 添加到批处理列表
		metric.Source = "async_processor"
		metrics = append(metrics, &metric)
		processed++
	}

	if err := rows.Err(); err != nil {
		return processed, err
	}

	// 4. 批量写入 request_metrics（单事务）
	if len(metrics) > 0 {
		if err := p.writer.BatchUpsertRequestMetrics(ctx, metrics); err != nil {
			return 0, err
		}
	}

	// ★ 扫描错误要往外报：调用方（后台循环）据此记日志/告警。
	//   已成功解析的行已经落库，水位线随之推进；坏行下一轮会被重试。
	if scanErr != nil {
		return processed, scanErr
	}

	return processed, nil
}

// readWatermark 读取已落库的最大复合游标
func (p *Processor) readWatermark(ctx context.Context) (watermark, error) {
	var wm watermark
	var occurredAt time.Time
	var requestID string

	err := p.db.QueryRowContext(ctx, watermarkSQL).Scan(&occurredAt, &requestID)
	if err != nil {
		if err == sql.ErrNoRows {
			// 还没有任何落库记录 ⇒ 全量扫描
			return watermark{}, nil
		}
		return watermark{}, err
	}

	wm.occurredAt = occurredAt
	wm.requestID = requestID
	wm.valid = true
	return wm, nil
}

// enrichDimensions 增强维度信息
func (p *Processor) enrichDimensions(ctx context.Context, metric *RequestMetric) error {
	var sessionID, taskID, clientProtocol, upstreamProtocol sql.NullString
	var attachmentCount sql.NullInt64

	err := p.db.QueryRowContext(ctx, enrichDimensionsSQL, metric.RequestID).Scan(
		&sessionID,
		&taskID,
		&attachmentCount,
		&clientProtocol,
		&upstreamProtocol,
	)

	if err == nil {
		if sessionID.Valid && sessionID.String != "" {
			metric.SessionID = &sessionID.String
		}
		if taskID.Valid && taskID.String != "" {
			metric.TaskID = &taskID.String
		}
		if attachmentCount.Valid {
			metric.AttachmentCount = int32(attachmentCount.Int64)
		}
		// 协议：优先客户端协议，回落到上游协议
		if clientProtocol.Valid && clientProtocol.String != "" {
			metric.Protocol = &clientProtocol.String
		} else if upstreamProtocol.Valid && upstreamProtocol.String != "" {
			metric.Protocol = &upstreamProtocol.String
		}
	}

	// 2. 查询付款者信息（根据业务逻辑）
	// TODO: 实现付款者逻辑
	// 目前默认：租户付费
	if metric.TenantID != "" {
		payerType := "tenant"
		metric.PayerID = &metric.TenantID
		metric.PayerType = &payerType
	}

	// 3. 查询团队信息（从 user_teams 或 session 关联）
	// TODO: 实现团队查询逻辑

	// 4. 查询任务信息（如果是任务发起的请求）
	// TODO: 实现任务查询逻辑

	// 5. 模态：由 token 计量 + 附件数派生（库里没有独立的 modality 源列）
	modality := deriveModality(metric.ImageTokens, metric.AudioTokens, metric.VideoTokens, metric.AttachmentCount)
	metric.Modality = &modality

	return nil
}

// deriveModality 派生模态类型
//
// ★ request_metrics.modality 没有直接来源列（usage_facts 只有各模态的 token 计数），
//
//	所以按「出现过哪些模态」派生：单一模态取该模态，多模态或带附件取 multimodal。
func deriveModality(imageTokens, audioTokens, videoTokens int32, attachmentCount int32) string {
	hasImage := imageTokens > 0
	hasAudio := audioTokens > 0
	hasVideo := videoTokens > 0

	switch {
	case !hasImage && !hasAudio && !hasVideo && attachmentCount == 0:
		return "text"
	case hasImage && !hasAudio && !hasVideo && attachmentCount == 0:
		return "image"
	case hasAudio && !hasImage && !hasVideo && attachmentCount == 0:
		return "audio"
	case hasVideo && !hasImage && !hasAudio && attachmentCount == 0:
		return "video"
	default:
		return "multimodal"
	}
}

// usdToCents 把美元金额转换为分（四舍五入，不截断）
//
// ★ 为什么用 math.Round 而不是 int64(usd*100)：截断会把 1.15 美元变成 114 分（少 1 分），
//
//	账单对账时会出现「分位差」。
//
// ★ 已知边界：float64 只能近似十进制，恰好落在半分边界时（如 0.015 美元 = 1.5 分），
//
//	本函数给 1 而 PG 的 numeric round() 给 2。若要求与账本逐分对齐，
//	改用 SQL 侧 round(cost_amount*100)::bigint 计算。
func usdToCents(usd float64) int64 {
	return int64(math.Round(usd * 100))
}

// Run 启动后台任务（阻塞式循环）
func (p *Processor) Run(ctx context.Context, interval time.Duration, batchSize int) {
	log.Printf("MetricsProcessor started (gateway_node=%s, interval=%v, batch_size=%d)",
		p.gatewayNode, interval, batchSize)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("MetricsProcessor stopped")
			return
		case <-ticker.C:
			processed, err := p.ProcessBatch(ctx, batchSize)
			if err != nil {
				log.Printf("Error processing batch: %v", err)
			} else if processed > 0 {
				log.Printf("Processed %d metrics", processed)
			}
		}
	}
}

// RunOnce 执行一次处理（用于测试或手动触发）
func (p *Processor) RunOnce(ctx context.Context, batchSize int) (int, error) {
	return p.ProcessBatch(ctx, batchSize)
}
