package metricsstore

import (
	"context"
	"database/sql"
	"log"
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

// ProcessBatch 处理一批记录
func (p *Processor) ProcessBatch(ctx context.Context, batchSize int) (int, error) {
	// 1. 从 usage_facts 读取待处理的记录
	//    条件：finalized_at > (SELECT MAX(occurred_at) FROM request_metrics)
	query := `
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
			uf.total_tokens,
			uf.cost_amount,
			uf.credits_charged,
			uf.status,
			uf.traffic_class,
			uf.error_kind,
			uf.latency_ms,
			uf.ttft_ms
		FROM usage_facts uf
		WHERE uf.occurred_at > COALESCE(
			(SELECT MAX(occurred_at) FROM request_metrics),
			'1970-01-01'::timestamptz
		)
		  AND uf.revision = 1  -- 只取首版记录，不取修订版
		ORDER BY uf.occurred_at
		LIMIT $1
	`
	
	rows, err := p.db.QueryContext(ctx, query, batchSize)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	
	processed := 0
	metrics := make([]*RequestMetric, 0, batchSize)
	
	for rows.Next() {
		var metric RequestMetric
		var costUSD float64
		var cachedTokens, reasoningTokens sql.NullInt64
		
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
			&metric.PromptTokens,
			&metric.CompletionTokens,
			&cachedTokens,
			&reasoningTokens,
			&metric.TotalTokens,
			&costUSD,
			&metric.CreditsCharged,
			&metric.Status,
			&metric.TrafficClass,
			&metric.ErrorKind,
			&metric.LatencyMs,
			&metric.TTFTMs,
		)
		if err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}
		
		// 处理可空字段
		if cachedTokens.Valid {
			metric.CachedTokens = int32(cachedTokens.Int64)
		}
		if reasoningTokens.Valid {
			metric.ReasoningTokens = int32(reasoningTokens.Int64)
		}
		
		// 转换费用：美元 -> 分
		metric.CostCents = int64(costUSD * 100)
		
		// 设置日期字段
		metric.OccurredDate = time.Date(
			metric.OccurredAt.Year(),
			metric.OccurredAt.Month(),
			metric.OccurredAt.Day(),
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
	
	// 4. 批量写入 request_metrics
	if len(metrics) > 0 {
		if err := p.writer.BatchUpsertRequestMetrics(ctx, metrics); err != nil {
			return 0, err
		}
	}
	
	return processed, nil
}

// enrichDimensions 增强维度信息
func (p *Processor) enrichDimensions(ctx context.Context, metric *RequestMetric) error {
	// 1. 查询 session_id（从 request_logs）
	var sessionID sql.NullString
	var gatewayInstance sql.NullString
	
	err := p.db.QueryRowContext(ctx, `
		SELECT session_id, gateway_instance
		FROM request_logs
		WHERE request_id = $1
		LIMIT 1
	`, metric.RequestID).Scan(&sessionID, &gatewayInstance)
	
	if err == nil {
		if sessionID.Valid {
			metric.SessionID = &sessionID.String
		}
		if gatewayInstance.Valid {
			metric.GatewayInstance = &gatewayInstance.String
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
