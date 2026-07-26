package providerprofile

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GatewayNetworkProber 网关网络探测器适配器
// 复用网关的 /v1/models 端点探测功能
type GatewayNetworkProber struct {
	httpClient *http.Client
	baseURL    string // 网关自身的URL，例如 "http://localhost:8080"
}

// NewGatewayNetworkProber 创建网关网络探测器
func NewGatewayNetworkProber(baseURL string) *GatewayNetworkProber {
	return &GatewayNetworkProber{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		baseURL: baseURL,
	}
}

// ProbeLatency 探测网络延迟
func (p *GatewayNetworkProber) ProbeLatency(ctx context.Context, credentialID int64, probeCount int) ([]int, error) {
	latencies := make([]int, 0, probeCount)

	// TODO: 实际实现需要：
	// 1. 根据 credentialID 查询对应的 provider 和 credential
	// 2. 构造带认证的 /v1/models 请求
	// 3. 测量往返时间
	// 
	// 当前为占位实现，返回模拟数据
	for i := 0; i < probeCount; i++ {
		start := time.Now()
		
		// 模拟探测 - 实际应该发送真实的 HTTP 请求
		// req, _ := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/v1/models", nil)
		// req.Header.Set("Authorization", "Bearer "+token)
		// resp, err := p.httpClient.Do(req)
		// ...
		
		elapsed := time.Since(start)
		latencies = append(latencies, int(elapsed.Milliseconds()))
	}

	return latencies, nil
}

// GatewayRequestAnalyzer 请求分析器适配器
// 从 request_logs 或 sessions 表聚合请求统计
type GatewayRequestAnalyzer struct {
	db *pgxpool.Pool
}

// NewGatewayRequestAnalyzer 创建请求分析器
func NewGatewayRequestAnalyzer(db *pgxpool.Pool) *GatewayRequestAnalyzer {
	return &GatewayRequestAnalyzer{db: db}
}

// AnalyzeRequests 分析最近N小时的请求统计
func (a *GatewayRequestAnalyzer) AnalyzeRequests(ctx context.Context, credentialID int64, hours int) (*RequestStats, error) {
	// 从 request_logs 表聚合数据
	// TODO: 根据实际的表结构调整查询
	query := `
		SELECT 
			COUNT(*) as total_requests,
			COUNT(*) FILTER (WHERE status_code < 400) as success_requests,
			COUNT(*) FILTER (WHERE status_code >= 400) as error_count,
			AVG(EXTRACT(EPOCH FROM (first_token_at - created_at)) * 1000) as avg_ttft_ms,
			AVG(EXTRACT(EPOCH FROM (completed_at - created_at)) * 1000) as avg_duration_ms
		FROM request_logs
		WHERE credential_id = $1
		  AND created_at >= NOW() - INTERVAL '1 hour' * $2
	`

	var stats RequestStats
	var avgTTFT, avgDuration *float64

	err := a.db.QueryRow(ctx, query, credentialID, hours).Scan(
		&stats.TotalRequests,
		&stats.SuccessRequests,
		&stats.ErrorCount,
		&avgTTFT,
		&avgDuration,
	)
	if err != nil {
		return nil, fmt.Errorf("query request stats: %w", err)
	}

	if avgTTFT != nil {
		stats.AvgTTFTMs = int(*avgTTFT)
	}
	if avgDuration != nil {
		stats.AvgDurationMs = int(*avgDuration)
	}

	// 查询错误类型分布
	errorTypeQuery := `
		SELECT status_code::text, COUNT(*)
		FROM request_logs
		WHERE credential_id = $1
		  AND created_at >= NOW() - INTERVAL '1 hour' * $2
		  AND status_code >= 400
		GROUP BY status_code
	`

	rows, err := a.db.Query(ctx, errorTypeQuery, credentialID, hours)
	if err != nil {
		return nil, fmt.Errorf("query error types: %w", err)
	}
	defer rows.Close()

	stats.ErrorTypes = make(map[string]int)
	for rows.Next() {
		var errorType string
		var count int
		if err := rows.Scan(&errorType, &count); err != nil {
			return nil, fmt.Errorf("scan error type: %w", err)
		}
		stats.ErrorTypes[errorType] = count
	}

	return &stats, nil
}

// GatewayScaleProvider 规模数据提供者适配器
// 从 provider_models 表查询模型规模信息
type GatewayScaleProvider struct {
	db *pgxpool.Pool
}

// NewGatewayScaleProvider 创建规模数据提供者
func NewGatewayScaleProvider(db *pgxpool.Pool) *GatewayScaleProvider {
	return &GatewayScaleProvider{db: db}
}

// GetModelScale 获取供应商的模型规模数据
func (p *GatewayScaleProvider) GetModelScale(ctx context.Context, credentialID int64) (*ScaleData, error) {
	// 首先获取 provider_id
	var providerID int64
	err := p.db.QueryRow(ctx, `
		SELECT provider_id FROM credentials WHERE id = $1
	`, credentialID).Scan(&providerID)
	if err != nil {
		return nil, fmt.Errorf("get provider_id: %w", err)
	}

	// 查询模型规模
	// TODO: 根据实际的 provider_models 表结构调整
	query := `
		SELECT 
			COUNT(*) as total_models,
			COUNT(*) FILTER (WHERE enabled = true) as available_models
		FROM provider_models
		WHERE provider_id = $1
	`

	var data ScaleData
	data.ProviderID = providerID

	err = p.db.QueryRow(ctx, query, providerID).Scan(
		&data.TotalModels,
		&data.AvailableModels,
	)
	if err != nil {
		return nil, fmt.Errorf("query model scale: %w", err)
	}

	return &data, nil
}

// GatewayCredentialLister 凭证列表提供者适配器
// 从 credentials 表获取活跃凭证列表
type GatewayCredentialLister struct {
	db *pgxpool.Pool
}

// NewGatewayCredentialLister 创建凭证列表提供者
func NewGatewayCredentialLister(db *pgxpool.Pool) *GatewayCredentialLister {
	return &GatewayCredentialLister{db: db}
}

// ListActiveCredentials 获取所有活跃的凭证
func (l *GatewayCredentialLister) ListActiveCredentials(ctx context.Context) ([]int64, error) {
	// TODO: 根据实际的 credentials 表结构调整
	query := `
		SELECT id 
		FROM credentials
		WHERE enabled = true
		  AND deleted_at IS NULL
		ORDER BY id
	`

	rows, err := l.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query active credentials: %w", err)
	}
	defer rows.Close()

	var credentialIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan credential id: %w", err)
		}
		credentialIDs = append(credentialIDs, id)
	}

	return credentialIDs, rows.Err()
}
