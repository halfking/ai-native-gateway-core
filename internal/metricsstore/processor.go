package metricsstore

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Processor 负责从 usage_facts 读取并填充 request_metrics
type Processor struct {
	db          *sql.DB
	writer      *Writer
	gatewayNode string // 当前网关节点ID
}

// ModelMapping 表示模型别名映射结果
type ModelMapping struct {
	RawName       string
	CanonicalID   int64
	CanonicalName string
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
// ★★★ 2026-11-24 审计轮**订正**：★ 本注释原写「配套索引见迁移 983
// ★★★ （idx_request_metrics_occurred_cursor），避免每批次全表排序」——
// ★★★ ★ **该索引从来不存在** ★（★ 迁移 983 的 11 个索引里没有它；
// ★★★   2026-11-24 生产只读实测：`to_regclass('public.idx_request_metrics_occurred_cursor')`
// ★★★   返回 NULL ⇒ 确认不存在）★★
// ★★★ ⇒ ⇒ **该「避免全表排序」的承诺从未兑现** ★★
// ★★★   ⇒ 2026-11-24 生产只读 EXPLAIN 实测（acc-pg17，request_metrics 340,904 行）：
// ★★★     Parallel Seq Scan on request_metrics_default + top-N heapsort
// ★★★     ⇒ Buffers: shared read=26,572 ⇒ Execution Time: **117.227 ms**
// ★★★   ⇒ ★ 这个查询**每批跑一次** ⇒ 空转成本随批次线性累积。
// ★★★ ★ 为什么会全表扫描：★ request_metrics 的主键是 (request_id, occurred_at)
// ★★★   （★ 生产实测 request_metrics_pkey 的定义）⇒ **列序与本查询的
// ★★★   (occurred_at, request_id) 相反** ⇒ 主键索引无法用于该排序 ⇒ 只能排序。
// ★★★
// ★★★ ⇒ **补索引的修法见迁移 983c**（★ 索引建在父表上 ⇒ 自动传播到 DEFAULT 分区；
// ★★★   ★ 注意：CREATE INDEX 在**父表**上会为已有分区自动建同名分区索引 ★★）。
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
// ★★ 批量形态（2026-11-24 审计修正轮）：原来是「按 request_id 逐行查」——每批 N 次查询。
//
//	而这恰恰是**重查询**（双腿视图＝session_turns_hot ∪ session_turns 全族），
//	上一轮「Phase 2 批量查询优化」只批量化了 model_aliases（轻查询），
//	把**真正的 N+1**（本条）原样留着 ⇒ 提交说明写的「查询次数从 N 次降低到 1 次」与实际不符。
//	现在整批一次：WHERE request_id = ANY($1)。
//
// ★ DISTINCT ON (request_id) + ORDER BY (request_id, ts DESC)：
//
//	等价于原来「每个 request_id 各自 ORDER BY ts DESC LIMIT 1」，
//	即同一请求取最新的一条（重试/多次记录）。
//
// ★ 「没有会话行」仍是语义正确：批量形态下「查不到」＝「结果集里没有这个 request_id」，
//
//	与逐行形态的 sql.ErrNoRows 同义 ⇒ 不计入增强失败（否则良性情况刷假警报淹没真警报）。
const enrichDimensionsSQL = `
	SELECT DISTINCT ON (rl.request_id)
		rl.request_id,
		rl.gw_session_id,
		rl.gw_task_id,
		rl.attachment_count,
		rl.client_protocol,
		rl.egress_protocol
	FROM request_logs_with_current_month rl
	WHERE rl.request_id = ANY($1)
	ORDER BY rl.request_id, rl.ts DESC
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
	// 收集所有需要映射的模型名称
	modelNamesToMap := make(map[string]bool)
	// 收集所有需要增强维度的请求 ID（批量增强用）
	requestIDsToEnrich := make([]string, 0, batchSize)

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

		// 收集需要映射的模型名称（不立即查询，稍后批量查询）
		if rawModelName.Valid && rawModelName.String != "" {
			metric.RawModelName = &rawModelName.String
			modelNamesToMap[rawModelName.String] = true
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

		// 2. 收集需要增强维度的请求 ID（批量增强：每批 1 次查询，取代逐行 N 次）
		requestIDsToEnrich = append(requestIDsToEnrich, metric.RequestID)

		// 3. 添加到批处理列表
		metric.Source = "async_processor"
		metrics = append(metrics, &metric)
		processed++
	}

	if err := rows.Err(); err != nil {
		return processed, err
	}

	// 3. 批量增强维度（每批 1 次查询，取代逐行 N 次）
	//
	// ★ 维度增强失败**不阻断**计量落库（计量不能丢），但必须**可见**：
	//   静默吞掉的形态最坏：表/列被删 ⇒ 请求照进库、计数照涨，只是维度全空
	//   ⇒ 从数据上完全看不出坏了。
	if len(metrics) > 0 {
		dims, err := p.batchEnrichDimensions(ctx, requestIDsToEnrich)
		if err != nil {
			// ★ 只计数不逐行打印：失败 1000 行会刷 1000 条日志；
			//   批末汇总一条（含首条原因）——「静默失败」正是这次事故的根因。
			enrichFailures += len(metrics)
			if firstEnrichErr == nil {
				firstEnrichErr = err
			}
		} else {
			for _, metric := range metrics {
				applyDimensionRow(metric, dims[metric.RequestID])
			}
		}

		// 派生维度（付款者归属 + 模态）：与增强成败无关，始终派生。
		// ★ 模态依赖附件数 ⇒ 必须在维度写回**之后**派生。
		for _, metric := range metrics {
			applyDerivedDimensions(metric)
		}
	}

	// 4. 批量查询模型映射（每批 1 次查询，取代逐行 N 次）
	mappingFailures := 0
	var firstMappingErr error
	if len(modelNamesToMap) > 0 {
		// 将map转换为slice
		modelNames := make([]string, 0, len(modelNamesToMap))
		for name := range modelNamesToMap {
			modelNames = append(modelNames, name)
		}

		// 一次性批量查询所有映射
		mappings, err := p.batchEnrichModelInfo(ctx, modelNames)
		if err != nil {
			// ★ 映射失败同样**不阻断**计量，但必须**可见**：
			//   失败 ⇒ 本批带 raw_model_name 的行 canonical_id / outbound_model 维度为空，
			//   而行照样落库、水位线照样推进 ⇒ 这是**永久**的维度丢失 ⇒ 不可静默。
			for _, metric := range metrics {
				if metric.RawModelName != nil {
					mappingFailures++
				}
			}
			firstMappingErr = err
		} else {
			// 应用映射到所有metrics
			for _, metric := range metrics {
				if metric.RawModelName != nil {
					if mapping, ok := mappings[*metric.RawModelName]; ok {
						metric.CanonicalID = &mapping.CanonicalID
						metric.OutboundModel = &mapping.CanonicalName
					}
				}
			}
		}
	}

	// 5. 批量写入 request_metrics（单事务）
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

	// ★ 模型映射失败汇总：与上面同源的问题（维度全空而数据看着在涨），
	//   但丢的是**标准模型维度** ⇒ 按模型聚合时同一模型会被拆成两个口径。
	//   失败 ⇒ 行已落库、水位线已推进 ⇒ **永久**丢失 ⇒ 必须有明确日志。
	if mappingFailures > 0 {
		log.Printf("MetricsProcessor: 本批 %d/%d 条模型映射失败（%d 个模型名）⇒ 这些行的标准模型维度（canonical_id/outbound_model）为空：%v",
			mappingFailures, processed, len(modelNamesToMap), firstMappingErr)
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

// dimensionRow 会话/任务/协议/附件维度的一行（批量查询的扫描目标）
type dimensionRow struct {
	sessionID       sql.NullString
	taskID          sql.NullString
	attachmentCount sql.NullInt64
	clientProtocol  sql.NullString
	egressProtocol  sql.NullString
}

// batchEnrichDimensions 批量查询会话/任务/协议/附件维度（每批 1 次查询）
//
// ★ 2026-11-24 审计修正轮：原来是「按 request_id 逐行查」（每批 N 次），
//
//	而双腿视图是**重查询**（session_turns_hot ∪ session_turns 全族）
//	⇒ 上一轮「Phase 2 批量查询优化」只批量化了轻查询（model_aliases），
//	  把**真正的 N+1** 留在原地 ⇒ 现在整批一次。
//
// ★ 返回值里**没有**某个 request_id ＝ 该请求在视图里没有行
//
//	（post-890 是会话族两臂体 ⇒ 非会话类请求查不到是语义正确）⇒ 不算失败。
func (p *Processor) batchEnrichDimensions(ctx context.Context, requestIDs []string) (map[string]*dimensionRow, error) {
	if len(requestIDs) == 0 {
		return map[string]*dimensionRow{}, nil
	}

	rows, err := p.db.QueryContext(ctx, enrichDimensionsSQL, pq.Array(requestIDs))
	if err != nil {
		return nil, fmt.Errorf("enrich dimensions for %d request(s), first=%s: %w", len(requestIDs), requestIDs[0], err)
	}
	defer rows.Close()

	out := make(map[string]*dimensionRow, len(requestIDs))
	for rows.Next() {
		var requestID string
		var row dimensionRow
		if err := rows.Scan(
			&requestID,
			&row.sessionID,
			&row.taskID,
			&row.attachmentCount,
			&row.clientProtocol,
			&row.egressProtocol,
		); err != nil {
			return nil, fmt.Errorf("scan enrich dimensions for %d request(s), first=%s: %w", len(requestIDs), requestIDs[0], err)
		}
		out[requestID] = &row
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enrich dimensions for %d request(s), first=%s: %w", len(requestIDs), requestIDs[0], err)
	}

	return out, nil
}

// applyDimensionRow 把一行维度写回 metric（批量与单行共用同一套判定）
//
// ★ row 为 nil ＝ 视图里没有这个请求的行 ⇒ 非会话类请求 ⇒ 维度留空是语义正确。
func applyDimensionRow(metric *RequestMetric, row *dimensionRow) {
	if row == nil {
		return
	}
	if row.sessionID.Valid && row.sessionID.String != "" {
		metric.SessionID = &row.sessionID.String
	}
	if row.taskID.Valid && row.taskID.String != "" {
		metric.TaskID = &row.taskID.String
	}
	if row.attachmentCount.Valid {
		metric.AttachmentCount = int32(row.attachmentCount.Int64)
	}
	// 协议：优先客户端协议，回落到出向协议（视图里**没有** upstream_protocol）
	if row.clientProtocol.Valid && row.clientProtocol.String != "" {
		metric.Protocol = &row.clientProtocol.String
	} else if row.egressProtocol.Valid && row.egressProtocol.String != "" {
		metric.Protocol = &row.egressProtocol.String
	}
}

// applyDerivedDimensions 派生不属于 DB 行的维度：付款者归属 + 模态
//
// ★ 模态依赖附件数 ⇒ 必须在 applyDimensionRow **之后**调用。
func applyDerivedDimensions(metric *RequestMetric) {
	// 查询付款者信息（根据业务逻辑）
	// TODO: 实现付款者逻辑
	// 目前默认：租户付费
	if metric.TenantID != "" {
		payerType := "tenant"
		metric.PayerID = &metric.TenantID
		metric.PayerType = &payerType
	}

	// 查询团队信息（从 user_teams 或 session 关联）
	// TODO: 实现团队查询逻辑

	// 查询任务信息（如果是任务发起的请求）
	// TODO: 实现任务查询逻辑

	// 模态：由 token 计量 + 附件数派生（库里没有独立的 modality 源列）
	modality := deriveModality(metric.ImageTokens, metric.AudioTokens, metric.VideoTokens, metric.AttachmentCount)
	metric.Modality = &modality
}

// enrichDimensions 单条增强（保留给直接调用者与测试；批处理走 batchEnrichDimensions）
//
// ★ 增强失败必须往外报，**不能吞**。
//
//	静默吞掉的形态最坏：表/列被删（如 v1 族退役）⇒ 请求照进库、计数照涨，
//	只是所有会话/任务/协议维度全空 ⇒ 从数据上完全看不出坏了。
//	这里把错误返回给 ProcessBatch，由它记日志（行仍会落库，计量不丢）。
//
// ★ 但「没有会话行」**不是失败**：post-890 的 canonical 视图是会话族两臂体
//
//	⇒ 非会话类请求（探测流量 / 健康检查 / 非会话 API 调用）本来就查不到行，
//	维度留空是**语义正确** ⇒ 结果集里没有它不计入失败。
//	★ 不区分的后果：良性情况每批都刷「维度增强失败」汇总日志 ⇒
//	  而这条汇总日志是「真失败」的唯一可见出口 ⇒ 假警报把真警报淹没
//	  （等于把「静默失败」换成「假警报淹没真警报」）。
func (p *Processor) enrichDimensions(ctx context.Context, metric *RequestMetric) error {
	rows, err := p.batchEnrichDimensions(ctx, []string{metric.RequestID})
	if err != nil {
		// ★ 失败时**仍然**派生不依赖 DB 的维度（付款者/模态）——与逐行形态一致：
		//   度量与派生维度不因增强失败而丢。
		applyDerivedDimensions(metric)
		return err
	}
	applyDimensionRow(metric, rows[metric.RequestID])
	applyDerivedDimensions(metric)
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

// ★ 2026-11-24 审计修正轮：原逐行版 enrichModelInfo（按 raw_model_name 单查）已**删除**。
//
//	删除理由（不是「顺手清理」，是它本身有两条硬伤）：
//	① 它是**包内私有**方法（首字母小写）⇒ 提交说明里写的「保持向后兼容，
//	   可能有其他地方调用」在语言层面就不可能成立 ⇒ 那条理由是假的；
//	   全仓检索确认零调用方。
//	② 它与批量版是**同一语义的第二份实现** ⇒ 两份会各自漂移
//	   （批量版已带 status='active' + 确定性选取，逐行版没有）
//	   ⇒ 同一份口径有两个答案，且逐行版那份永远不会被执行。
//	批量版 batchEnrichModelInfo 是唯一实现。

// batchEnrichModelInfoSQL 批量查询模型别名映射。
//
// ★ DISTINCT ON (raw_name) + ORDER BY (raw_name, canonical_id)：确定性选取，
//
//	理由见 batchEnrichModelInfo 的注释（model_aliases 的唯一约束是
//	UNIQUE(canonical_id, raw_name) ⇒ 同名可有多条 active 映射）。
const batchEnrichModelInfoSQL = `
		SELECT DISTINCT ON (ma.raw_name)
			ma.raw_name, ma.canonical_id, mc.canonical_name
		FROM model_aliases ma
		JOIN models_canonical mc ON mc.id = ma.canonical_id
		WHERE ma.raw_name = ANY($1) AND ma.status = 'active'
		ORDER BY ma.raw_name, ma.canonical_id
	`

// batchEnrichModelInfo 批量查询模型映射（每批 1 次查询）
//
// ★ 策略：收集所有需要映射的模型名称，一次查询获取所有映射。
//
// ★★ 确定性（2026-11-24 审计修正轮补）：model_aliases 的唯一约束是
//
//	UNIQUE (canonical_id, raw_name) —— **不是** UNIQUE(raw_name)
//	⇒ 同一个 raw_name 合法地对应**多个** canonical_id
//	（索引 idx_model_aliases_raw_name_active 本身就是 (raw_name, canonical_id)）。
//	原批量写法没有 LIMIT 也没有 DISTINCT ON ⇒ 同名多行时 map 被**后扫到的行覆盖**
//	⇒ 胜者取决于 PG 的扫描顺序 ⇒ **同一个模型在不同批次可能落到不同 canonical_id**
//	⇒ 按模型聚合时同一模型被拆成两个口径（且不可复现）。
//	现在用 DISTINCT ON (raw_name) + ORDER BY (raw_name, canonical_id)
//	⇒ 每个 raw_name 恰好一行、胜者恒为最小 canonical_id（可复现），
//	且该 ORDER BY 正好能被 idx_model_aliases_raw_name_active 支撑。
func (p *Processor) batchEnrichModelInfo(ctx context.Context, modelNames []string) (map[string]*ModelMapping, error) {
	if len(modelNames) == 0 {
		return make(map[string]*ModelMapping), nil
	}

	// 使用pq.Array进行批量查询
	query := batchEnrichModelInfoSQL

	rows, err := p.db.QueryContext(ctx, query, pq.Array(modelNames))
	if err != nil {
		return nil, fmt.Errorf("batch query model mappings: %w", err)
	}
	defer rows.Close()

	// 构建映射结果
	mappings := make(map[string]*ModelMapping)
	for rows.Next() {
		var rawName, canonicalName string
		var canonicalID int64

		if err := rows.Scan(&rawName, &canonicalID, &canonicalName); err != nil {
			log.Printf("扫描模型映射失败: %v", err)
			continue
		}

		mappings[rawName] = &ModelMapping{
			RawName:       rawName,
			CanonicalID:   canonicalID,
			CanonicalName: canonicalName,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model mappings: %w", err)
	}

	return mappings, nil
}

// Config 是 request_metrics 处理器的运行参数。
//
// ★ 2026-11-24 吞吐优化轮：原实现把「每 5 分钟 100 条」硬编码在
//
//	cmd/gateway/main.go 的 Run(ctx, 5*time.Minute, 100) 调用里。
//
//	⇒ 那是**占位常量**，不是容量测算：生产实测写入速率约 1,100 条/小时
//	（近 1 小时均值 18.1 条/分钟，峰值 63 条/分钟），
//	而「5 分钟 100 条」的上限只有 **1,200 条/小时** ⇒ 余量仅 7%。
//	⇒ 实测 61/61 个批次全部处理满 100 条 ⇒ 处理器**长期贴着天花板跑**
//	⇒ 任何流量增长都会立刻转化为**数据滞后**（统计表落后真实用量）。
//
// ★ 三条修法（缺一不可）：
//
//	① 间隔与批量**可配**（环境变量）⇒ 不用改代码发版就能调容量。
//	② **自适应追平**：每轮不只处理一批，而是持续处理直到
//	   「本批不满」或「达到本轮上限」⇒ 积压时自动加速，
//	   空闲时自动降频（不会空转打库）。
//	③ 单批写入改为**单条多行 INSERT** ⇒ 批内 N 次网络往返变 1 次。
type Config struct {
	// Interval 是轮询间隔（两次追平之间的静默期）。
	Interval time.Duration
	// BatchSize 是单批最大条数。
	BatchSize int
	// MaxRoundsPerTick 是单轮内最多连续追多少批。
	//
	// ★ 上限存在的原因：追平是「一直处理到不满批为止」，
	//   若上游写入速度 > 单批处理速度 ⇒ 无限循环 ⇒ 永不让出连接池。
	//   有了上限，最坏情况退化成「每轮处理 MaxRoundsPerTick × BatchSize 条」。
	MaxRoundsPerTick int
}

// DefaultConfig 返回默认配置。
//
// ★ 默认值来自**生产实测**（211，2026-11-24）：
//
//   - BatchSize=500：单批写入实测 8.5ms（多行 INSERT）；维度增强查询
//     随 ID 数增长很慢（100 个 ID → 30ms 执行 + 37ms 规划；
//     1000 个 ID → 65ms 执行 + 34ms 规划 ⇒ 规划时间几乎不随批量增长）。
//   - MaxRoundsPerTick=20 ⇒ 单轮上限 10,000 条，
//     按实测单批 ~150ms 算 ⇒ 最坏占满一轮约 3s（远小于 5 分钟间隔）。
//   - Interval=1 分钟：实测写入速率约 1,100 条/小时（18 条/分钟）
//     ⇒ 1 分钟间隔 + 500 条批量 ⇒ 单批即可覆盖 5 分钟的写入量
//     ⇒ 数据滞后从「最多 5 分钟」降到「最多 1 分钟」。
func DefaultConfig() Config {
	return Config{
		Interval:         time.Minute,
		BatchSize:        500,
		MaxRoundsPerTick: 20,
	}
}

// ConfigFromEnv 在默认配置基础上套用环境变量覆盖。
//
// ★ 三个环境变量（都可省略，省略即用默认值）：
//
//	LLM_GATEWAY_METRICS_INTERVAL         轮询间隔，如 "30s"、"2m"
//	LLM_GATEWAY_METRICS_BATCH_SIZE       单批最大条数
//	LLM_GATEWAY_METRICS_MAX_ROUNDS       单轮内最多连续追多少批
//
// ★ 为什么做成环境变量而不是改代码发版：
//
//	处理器容量必须随流量调，而流量是运行时事实 ⇒ 改代码发版才能调的参数，
//	在流量高峰时是**来不及**的 ⇒ 必须能在不改代码的前提下调整。
//
// ★ 解析失败的处置：**回落默认值并打日志**，不静默、不启动失败。
//
//	理由：后台 worker 的配置项写错不该让整个网关起不来
//	（那会把「统计表延迟」升级成「网关不可用」）。
func ConfigFromEnv(base Config) Config {
	cfg := base

	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_METRICS_INTERVAL")); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			log.Printf("MetricsProcessor: LLM_GATEWAY_METRICS_INTERVAL=%q 解析失败（%v）⇒ 回落默认值 %v", raw, err, cfg.Interval)
		case d <= 0:
			log.Printf("MetricsProcessor: LLM_GATEWAY_METRICS_INTERVAL=%q 非正数 ⇒ 回落默认值 %v", raw, cfg.Interval)
		default:
			cfg.Interval = d
		}
	}

	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_METRICS_BATCH_SIZE")); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			log.Printf("MetricsProcessor: LLM_GATEWAY_METRICS_BATCH_SIZE=%q 解析失败（%v）⇒ 回落默认值 %d", raw, err, cfg.BatchSize)
		case n <= 0:
			log.Printf("MetricsProcessor: LLM_GATEWAY_METRICS_BATCH_SIZE=%q 非正数 ⇒ 回落默认值 %d", raw, cfg.BatchSize)
		default:
			cfg.BatchSize = n
		}
	}

	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_METRICS_MAX_ROUNDS")); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			log.Printf("MetricsProcessor: LLM_GATEWAY_METRICS_MAX_ROUNDS=%q 解析失败（%v）⇒ 回落默认值 %d", raw, err, cfg.MaxRoundsPerTick)
		case n <= 0:
			log.Printf("MetricsProcessor: LLM_GATEWAY_METRICS_MAX_ROUNDS=%q 非正数 ⇒ 回落默认值 %d", raw, cfg.MaxRoundsPerTick)
		default:
			cfg.MaxRoundsPerTick = n
		}
	}

	return cfg
}

// Run 启动后台任务（阻塞式循环）。
//
// ★ 2026-11-24 吞吐优化轮：每轮**自适应追平**——
//
//	旧形态：每 tick 只处理一批（batchSize 条）⇒ 上限 = batchSize/interval
//	  ⇒ 「5 分钟 100 条」⇒ 1,200 条/小时 ⇒ 实测流量已占 92%。
//	新形态：每 tick 连续处理多批，直到「本批不满」（追平了）或
//	  「达到 MaxRoundsPerTick」（让出资源）⇒
//	  ★ 空闲时：一批就满不了 ⇒ 只查一次库（不空转）。
//	  ★ 积压时：自动连续追 ⇒ 吞吐从「固定上限」变成「按积压自适应」。
func (p *Processor) Run(ctx context.Context, cfg Config) {
	log.Printf("MetricsProcessor started (gateway_node=%s, interval=%v, batch_size=%d, max_rounds=%d)",
		p.gatewayNode, cfg.Interval, cfg.BatchSize, cfg.MaxRoundsPerTick)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("MetricsProcessor stopped")
			return
		case <-ticker.C:
			p.drain(ctx, cfg)
		}
	}
}

// drain 在一轮内连续追平积压。
//
// ★ 退出条件有两个（缺一不可）：
//
//	① 本批不满（processed < batchSize）⇒ 说明已追平（没有更多数据了）⇒ 停。
//	② 达到 MaxRoundsPerTick ⇒ 让出连接池/IO，下一轮继续追。
//
// ★ 为什么不用「processed == 0」当停止条件：processed==0 既可能是
//
//	「追平了」也可能是「读失败但没报错」，用满批与否判断更贴合语义。
func (p *Processor) drain(ctx context.Context, cfg Config) {
	total := 0
	rounds := 0

	for rounds < cfg.MaxRoundsPerTick {
		if ctx.Err() != nil {
			return
		}

		processed, err := p.ProcessBatch(ctx, cfg.BatchSize)
		if err != nil {
			log.Printf("Error processing batch (round %d, total %d): %v", rounds+1, total, err)
			return
		}

		rounds++
		total += processed

		// 本批不满 ⇒ 已追平（没有更多待处理数据）⇒ 停止本轮。
		if processed < cfg.BatchSize {
			break
		}
	}

	if total > 0 {
		log.Printf("Processed %d metrics in %d round(s)", total, rounds)
	}
}

// RunOnce 执行一次处理（用于测试或手动触发）
func (p *Processor) RunOnce(ctx context.Context, batchSize int) (int, error) {
	return p.ProcessBatch(ctx, batchSize)
}
