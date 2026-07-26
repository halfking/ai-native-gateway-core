package providerprofile

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// GatewayNetworkProber 网关网络探测器适配器
// 复用网关的 /v1/models 端点探测功能（GET，免费，不消耗token）
type GatewayNetworkProber struct {
	db         *pgxpool.Pool
	httpClient *http.Client
	fernetKey  []byte
	keyring    *secret.Keyring
}

// NewGatewayNetworkProber 创建网关网络探测器
// fernetKey/keyring 用于解密 credentials.secret_ciphertext（与 cmd/gateway/main.go
// 中派生凭证解密密钥的方式一致，参见 secret.FernetKeyFromSecret / secret.KeyringFromEnv）。
func NewGatewayNetworkProber(db *pgxpool.Pool, fernetKey []byte, keyring *secret.Keyring) *GatewayNetworkProber {
	return &GatewayNetworkProber{
		db: db,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		fernetKey: fernetKey,
		keyring:   keyring,
	}
}

// ProbeLatency 探测网络延迟：对 credential 对应的供应商发起 probeCount 次
// GET /v1/models 请求，返回每次请求的往返时延（ms）。
//
// 单次探测失败（网络错误等）会被跳过而不是直接失败整个探测；只有全部探测
// 都失败时才返回 error，避免瞬时抖动导致整轮采集失败。
func (p *GatewayNetworkProber) ProbeLatency(ctx context.Context, credentialID int64, probeCount int) ([]int, error) {
	var (
		baseURL      string
		protocol     string
		catalogCode  string
		secretCipher []byte
	)

	err := p.db.QueryRow(ctx, `
		SELECT COALESCE(pr.base_url, ''), COALESCE(pr.protocol, ''),
		       COALESCE(pr.catalog_code, ''), c.secret_ciphertext
		FROM credentials c
		JOIN providers pr ON pr.id = c.provider_id
		WHERE c.id = $1
	`, credentialID).Scan(&baseURL, &protocol, &catalogCode, &secretCipher)
	if err != nil {
		return nil, fmt.Errorf("query credential for probe: %w", err)
	}

	var apiKey string
	if len(secretCipher) > 0 {
		pt, _, derr := secret.DecryptAny(string(secretCipher), p.keyring, p.fernetKey)
		if derr != nil {
			return nil, fmt.Errorf("decrypt credential secret: %w", derr)
		}
		apiKey = string(pt)
	}

	desc := providercap.Resolve(protocol, catalogCode)
	candidates := providercap.ModelsURLCandidates(baseURL, nil, desc)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no models endpoint candidate for credential %d (base_url=%q)", credentialID, baseURL)
	}
	url := candidates[0]

	latencies := make([]int, 0, probeCount)
	for i := 0; i < probeCount; i++ {
		start := time.Now()

		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if rerr != nil {
			return nil, fmt.Errorf("build probe request: %w", rerr)
		}
		providercap.ApplyAuthHeaders(req, desc, apiKey)

		resp, derr := p.httpClient.Do(req)
		elapsedMs := int(time.Since(start).Milliseconds())
		if derr != nil {
			// 单次探测失败：跳过，不中断整轮探测（可能是瞬时网络抖动）
			continue
		}
		//nolint:errcheck // best-effort close
		resp.Body.Close()

		latencies = append(latencies, elapsedMs)
	}

	if len(latencies) == 0 {
		return nil, fmt.Errorf("all %d probes failed for credential %d", probeCount, credentialID)
	}

	return latencies, nil
}

// GatewayRequestAnalyzer 请求分析器适配器
// 从 request_logs_hot 表聚合请求统计（0-7天热数据，与 recent_success_rate()
// SQL 函数读取同一张表，参见 sql/objects/functions/recent_success_rate_*.sql）。
type GatewayRequestAnalyzer struct {
	db *pgxpool.Pool
}

// NewGatewayRequestAnalyzer 创建请求分析器
func NewGatewayRequestAnalyzer(db *pgxpool.Pool) *GatewayRequestAnalyzer {
	return &GatewayRequestAnalyzer{db: db}
}

// AnalyzeRequests 分析最近N小时的请求统计
func (a *GatewayRequestAnalyzer) AnalyzeRequests(ctx context.Context, credentialID int64, hours int) (*RequestStats, error) {
	query := `
		SELECT 
			COUNT(*) AS total_requests,
			COUNT(*) FILTER (WHERE success) AS success_requests,
			COUNT(*) FILTER (WHERE NOT success) AS error_count,
			AVG(stream_first_chunk_ms) FILTER (WHERE stream_first_chunk_ms IS NOT NULL) AS avg_ttft_ms,
			AVG(latency_ms) FILTER (WHERE latency_ms IS NOT NULL) AS avg_duration_ms
		FROM request_logs_hot
		WHERE credential_id = $1
		  AND ts >= NOW() - INTERVAL '1 hour' * $2
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

	// 错误类型分布：优先使用上游 HTTP 状态码（5xx/4xx），缺失时回退到
	// error_kind（如 network/timeout）。scorer.go 的稳定性评分依据错误
	// 类型字符串的首字符判断是否为 5xx，所以状态码优先。
	errorTypeQuery := `
		SELECT COALESCE(upstream_status_code::text, error_kind, 'unknown') AS error_type, COUNT(*)
		FROM request_logs_hot
		WHERE credential_id = $1
		  AND ts >= NOW() - INTERVAL '1 hour' * $2
		  AND NOT success
		GROUP BY 1
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

	return &stats, rows.Err()
}

// GatewayScaleProvider 规模数据提供者适配器
// 从 provider_models 表查询模型规模信息（可用性字段是 `available`，
// 不是 `enabled`）。
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

	query := `
		SELECT 
			COUNT(*) AS total_models,
			COUNT(*) FILTER (WHERE available) AS available_models
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
// 从 credentials 表获取活跃凭证列表。
//
// credentials 表没有 enabled 布尔字段，也没有 deleted_at 软删除字段
// （行是硬删除或通过 status 翻转为 'disabled'）。"活跃可用" 由三个字段
// 共同表达：
//   - status = 'active'          （非 cooling/degraded/quarantine/disabled 等）
//   - manual_disabled = false    （人工禁用开关）
//   - lifecycle_status = 'active'（非 disabled/suspended/retired）
type GatewayCredentialLister struct {
	db *pgxpool.Pool
}

// NewGatewayCredentialLister 创建凭证列表提供者
func NewGatewayCredentialLister(db *pgxpool.Pool) *GatewayCredentialLister {
	return &GatewayCredentialLister{db: db}
}

// ListActiveCredentials 获取所有活跃的凭证
func (l *GatewayCredentialLister) ListActiveCredentials(ctx context.Context) ([]int64, error) {
	query := `
		SELECT id 
		FROM credentials
		WHERE status = 'active'
		  AND manual_disabled = false
		  AND lifecycle_status = 'active'
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
