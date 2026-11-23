package metricsstore

import (
	"context"
	"database/sql"
	"fmt"
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
// ★ 数据源是**双腿视图** request_logs_with_current_month（仓内 sqlreadguard 守卫指定的合法形态）：
//
//	迁移 890 已把 v1 族 request_logs（含母表 request_logs / _request_logs）整体删除，
//	⇒ 裸读 FROM request_logs 在生产上直接 42P01「relation does not exist」
//	⇒ 整条增强静默失败（会话/任务/协议维度全空，而请求照进库、计数照涨
//	⇒ 这是最难发现的一类坏：数据看着在涨，只是维度全没了）。
//	⇒ 改读双腿视图：视图名跨版本稳定（pre-890=v1 三臂体、post-890=会话族两臂体）。
//	★ 同时满足仓内守卫 internal/sqlreadguard（禁裸读 v1 母表）：
//	其正则要求 request_logs 后紧跟词边界，视图名下个字符是 "_" ⇒ 不命中。
//
// ★ 列名以活库为准：
//
//	会话键是 gw_session_id、任务键是 gw_task_id（早期版本写的 session_id/gateway_instance
//	会直接报「column does not exist」，于是整条增强静默失败 ⇒ session_id 永远写不进去）。
//	★ 协议列：视图里是 **egress_protocol**（**没有** upstream_protocol）；
//	  取值优先级 client_protocol（客户端协议）→ egress_protocol（出向协议）。
//
// ★ 语义边界：post-890 视图是「会话族两臂体」（session_turns_hot ∪ session_turns），
//
//	只有「进入了会话」的请求才有行 ⇒ 非会话类请求的 session_id 保持 NULL 是**语义正确**，
//	不是缺陷。
//
// ★ ORDER BY ts DESC：同一 request_id 可能有多行（重试/多次记录），取最新的一条。
//
// ★★★★ 2026-11-24（合并轮十一 · ★★ 订正合并轮十我方的结论 ★★）：
//
//	★ 合并轮十我方代上游改这个读点时，★ **结论是错的** ⇒ ★★ **本段改回上游
//	★ 的读法**（★ 双腿视图 `request_logs_with_current_month`）★★。
//	★ ★ **我方当时说「`upstream_protocol` 在【任何活表里都不存在】⇒ 显式
//	★ 置 NULL ⇒ 回落已失效」** ⇒ ★★ **这句话不成立**：
//	★   ① ★ 视图里**有**出向协议列 `egress_protocol`（★ 见上「列名以活库为
//	★      准」那段）⇒ ★★ **上游要的「上游协议」取得到** ⇒ ★★ **我方那版
//	★      把它置 NULL ＝ 白白丢了回落语义** ★★；
//	★   ② ★ 视图**本身**就是会话族两臂体（★ `session_turns_hot` ∪
//	★      `session_turns`）⇒ ★★ **附件数不需要再手写一次两腿子查询**
//	★      （★ 我方那版在 Go 里又拼了一遍 UNION ALL ⇒ ★★ **既冗余、★ 又绕开了
//	★      「仓里指定的 canonical 读点」**）★★。
//	★ ★★ **⇒ 通用式（★ 本轮最值钱的一条）★★**：★★ **改一个坏读点之前、
//	★ 先问「★ 仓里是不是已经【指定了读点】★」** ——★ ★ 本仓有
//	★ `internal/sqlreadguard`（★ 禁裸读 v1 母表）★ 且**明确点名**
//	★ `request_logs_with_current_month` 是合法形态（★ ★ 且该视图**跨版本稳定**：
//	★ pre-890 ＝ v1 三臂体、★ post-890 ＝ 会话族两臂体 ★★）⇒ ★★ **⇒ 我方
//	★ 改指 `request_context_attrs` 时【没先看这个指定】⇒ ★★ **多绕了一圈、
//	★ 还把回落语义弄丢了** ⇒ ★★ **⇒ 通用式：★ 「改指活表」是最后手段、★
//	★ 不是第一反应；★ 第一反应是【找到仓里已经钦定的那条读法】** ★★。
//	★ ★★ **诚实边界（★ 必须写下来）★★**：★ ★ 上游的「协议：优先客户端协议、
//	★ 回落到上游协议」这条回落**在本仓当前数据面下【极少触发】**（★ 客户端协议
//	★ 几乎总有值 ⇒ 回落几乎走不到）⇒ ★★ **⇒ 这不是缺陷、★ 只是「回落路径
//	★ 在实践中近乎不触发」** ⇒ ★★ **⇒ 若产品真需要【独立的】上游协议字段、
//	★ 得先定它【现在从哪来】** ⇒ ★★ **那仍是数据模型口径问题、★ 不是代码问题** ★★。
const enrichDimensionsSQL = `
	SELECT
		rl.gw_session_id,
		rl.gw_task_id,
		rl.attachment_count,
		rl.client_protocol,
		rl.egress_protocol
	FROM request_logs_with_current_month rl
	WHERE rl.request_id = $1
	ORDER BY rl.ts DESC
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
	// 维度增强失败计数（只影响维度、不影响计量 ⇒ 不阻断整批，但必须可见）
	enrichFailures := 0
	var firstEnrichErr error

	for rows.Next() {
		var metric RequestMetric
		var costUSD float64
		var cachedTokens, reasoningTokens, imageTokens, audioTokens, videoTokens sql.NullInt64
		var latencyMs, ttftMs sql.NullInt64
		var promptTokens, completionTokens, totalTokens int64
		var rawModelName sql.NullString

		err := rows.Scan(
			&metric.RequestID,
			&metric.TenantID,
			&metric.OccurredAt,
			&metric.ProviderID,
			&metric.CanonicalID,
			&rawModelName,
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

		// 处理模型名称：raw_model_name + 映射到canonical_name
		if rawModelName.Valid && rawModelName.String != "" {
			metric.RawModelName = &rawModelName.String
			// 查询模型映射获取canonical_id和canonical_name
			if err := p.enrichModelInfo(ctx, &metric, rawModelName.String); err != nil {
				// 映射失败不阻断处理，使用原始名称
				log.Printf("模型映射失败 (request_id=%s, raw_model=%s): %v", metric.RequestID, rawModelName.String, err)
			}
		}

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
			// ★ 只计数不逐行打印：失败 1000 行会刷 1000 条日志；
			//   批末汇总一条（含首条原因）——「静默失败」正是这次事故的根因。
			enrichFailures++
			if firstEnrichErr == nil {
				firstEnrichErr = err
			}
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

	// ★ 维度增强失败汇总：这是「静默失败」类故障的唯一可见出口 ——
	//   表/列被删（如 v1 族退役）时，请求照进库、计数照涨，只有维度全空，
	//   数据上完全看不出坏了 ⇒ 必须有一条明确的日志。
	if enrichFailures > 0 {
		log.Printf("MetricsProcessor: 本批 %d/%d 条维度增强失败（首条原因：%v）⇒ 这些行的会话/任务/协议维度为空",
			enrichFailures, processed, firstEnrichErr)
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
	var sessionID, taskID, clientProtocol, egressProtocol sql.NullString
	var attachmentCount sql.NullInt64

	scanErr := p.db.QueryRowContext(ctx, enrichDimensionsSQL, metric.RequestID).Scan(
		&sessionID,
		&taskID,
		&attachmentCount,
		&clientProtocol,
		&egressProtocol,
	)

	if scanErr == nil {
		if sessionID.Valid && sessionID.String != "" {
			metric.SessionID = &sessionID.String
		}
		if taskID.Valid && taskID.String != "" {
			metric.TaskID = &taskID.String
		}
		if attachmentCount.Valid {
			metric.AttachmentCount = int32(attachmentCount.Int64)
		}
		// 协议：优先客户端协议，回落到出向协议（视图里**没有** upstream_protocol）
		if clientProtocol.Valid && clientProtocol.String != "" {
			metric.Protocol = &clientProtocol.String
		} else if egressProtocol.Valid && egressProtocol.String != "" {
			metric.Protocol = &egressProtocol.String
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

	// ★ 增强失败必须往外报，**不能吞**。
	//   静默吞掉的形态最坏：表/列被删（如 v1 族退役）⇒ 请求照进库、计数照涨，
	//   只是所有会话/任务/协议维度全空 ⇒ 从数据上完全看不出坏了。
	//   这里把错误返回给 ProcessBatch，由它记日志（行仍会落库，计量不丢）。
	//
	// ★ 但「没有会话行」**不是失败**：post-890 的 canonical 视图是会话族两臂体
	//   ⇒ 非会话类请求（探测流量 / 健康检查 / 非会话 API 调用）本来就查不到行，
	//   维度留空是**语义正确** ⇒ ErrNoRows 不计入失败。
	//   ★ 不区分的后果：良性情况每批都刷「维度增强失败」汇总日志 ⇒
	//     而这条汇总日志是「真失败」的唯一可见出口 ⇒ 假警报把真警报淹没
	//     （等于把「静默失败」换成「假警报淹没真警报」）。
	if scanErr != nil && scanErr != sql.ErrNoRows {
		return fmt.Errorf("enrich dimensions for request %s: %w", metric.RequestID, scanErr)
	}
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

// enrichModelInfo 查询模型映射，填充canonical_id和outbound_model(canonical_name)
//
// ★ 策略：通过model_aliases表查询raw_model_name到canonical模型的映射
// ★ 失败不阻断：映射失败时保留原始canonical_id，outbound_model使用raw_model_name
func (p *Processor) enrichModelInfo(ctx context.Context, metric *RequestMetric, rawModelName string) error {
	var canonicalID sql.NullInt64
	var canonicalName sql.NullString

	query := `
		SELECT ma.canonical_id, mc.canonical_name
		FROM model_aliases ma
		JOIN models_canonical mc ON mc.id = ma.canonical_id
		WHERE ma.raw_name = $1 AND ma.status = 'active'
		LIMIT 1
	`

	err := p.db.QueryRowContext(ctx, query, rawModelName).Scan(&canonicalID, &canonicalName)
	if err != nil {
		if err == sql.ErrNoRows {
			// 没有映射，保持原始值
			return nil
		}
		return err
	}

	// 更新canonical_id和outbound_model
	if canonicalID.Valid {
		cid := canonicalID.Int64
		metric.CanonicalID = &cid
	}
	if canonicalName.Valid && canonicalName.String != "" {
		metric.OutboundModel = &canonicalName.String
	}

	return nil
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
