package providerprofile

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/httputil"

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
		if _, bodyErr := httputil.ReadPrefixAndDrain(resp.Body, 0); bodyErr != nil {
			continue
		}

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
//
// 2026-08-07 audit fix: 之前只算成功率和错误类型分布，忽略限流命中和不可用
// 窗口。补充两项：
//   - RateLimitMetrics：429 命中次数 + 总请求，用于"限流命中率"维度
//   - AvailabilityWindow：按 5 分钟桶聚合成功率，识别连续低成功率段
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate error types: %w", err)
	}

	// 2026-08-07: 限流命中率（429）—— 单独聚合，避免被 ErrorTypes 的 5xx
	// 前缀判断"埋掉"。
	rlQuery := `
		SELECT COUNT(*) FILTER (WHERE upstream_status_code = 429) AS rl_hits,
		       COUNT(*) AS total
		FROM request_logs_hot
		WHERE credential_id = $1
		  AND ts >= NOW() - INTERVAL '1 hour' * $2
	`
	var rlHits, rlTotal int
	if err := a.db.QueryRow(ctx, rlQuery, credentialID, hours).Scan(&rlHits, &rlTotal); err != nil {
		return nil, fmt.Errorf("query rate limit hits: %w", err)
	}
	rl := &RateLimitMetrics{
		RateLimitHits: rlHits,
		TotalRequests: rlTotal,
	}
	if rlTotal > 0 {
		rl.HitsRatio = float64(rlHits) / float64(rlTotal)
	}
	stats.RateLimitMetrics = rl

	// 2026-08-07: 不可用窗口（连续低成功率段）。按 5 分钟桶聚合成功率，
	// 在 Go 端扫描找出连续 sr<90% 段。
	window, err := a.BucketSuccessRates(ctx, credentialID, hours, 5)
	if err != nil {
		return nil, fmt.Errorf("query availability window: %w", err)
	}
	stats.AvailabilityWindow = window

	return &stats, rows.Err()
}

// BucketSuccessRates 按指定分钟粒度聚合成功率，返回"不可用窗口"统计。
//
// DowntimeBucket 阈值：成功率 < 90%。文档里 "连续 5 分钟成功率 < 90% 视为
// downtime" 的定义（供应商画像设计文档 §3.1.3）保持一致。
func (a *GatewayRequestAnalyzer) BucketSuccessRates(
	ctx context.Context,
	credentialID int64,
	hours int,
	bucketSizeMin int,
) (*AvailabilityWindow, error) {
	if bucketSizeMin <= 0 {
		bucketSizeMin = 5
	}
	query := `
		SELECT
		  FLOOR(EXTRACT(EPOCH FROM ts) / ($3 * 60)) * ($3 * 60) AS bucket_epoch,
		  100.0 * COUNT(*) FILTER (WHERE success) / NULLIF(COUNT(*), 0) AS sr,
		  COUNT(*) AS req
		FROM request_logs_hot
		WHERE credential_id = $1
		  AND ts >= NOW() - INTERVAL '1 hour' * $2
		GROUP BY 1
		ORDER BY 1
	`

	rows, err := a.db.Query(ctx, query, credentialID, hours, bucketSizeMin)
	if err != nil {
		return nil, fmt.Errorf("query bucket success rates: %w", err)
	}
	defer rows.Close()

	type bucketRow struct {
		bucketEpoch int64
		sr          float64
		req         int
	}
	var buckets []bucketRow
	for rows.Next() {
		var b bucketRow
		var sr *float64
		if err := rows.Scan(&b.bucketEpoch, &sr, &b.req); err != nil {
			return nil, fmt.Errorf("scan bucket row: %w", err)
		}
		if sr != nil {
			b.sr = *sr
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bucket rows: %w", err)
	}

	// 在 Go 端扫描连续 sr<90% 段。注意：sr<90 但 req=0 的桶（无请求）不计为
	// downtime——空桶不能算"不可用"，否则冷启动的供应商都会被扣成 0 分。
	window := &AvailabilityWindow{
		TotalBuckets: len(buckets),
	}
	var run int
	for _, b := range buckets {
		if b.req > 0 && b.sr < 90.0 {
			window.DowntimeBuckets++
			run++
			if run > window.LongestRun {
				window.LongestRun = run
			}
		} else {
			run = 0
		}
	}
	if window.TotalBuckets > 0 {
		window.DowntimeRatio = float64(window.DowntimeBuckets) / float64(window.TotalBuckets)
	}
	return window, nil
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
//
// 2026-08-07 audit fix: 增加 ConcurrencyCapacity（来自 credentials 表的
// concurrency_limit / concurrency_limit_auto）。这是新"并发承载能力"维度
// 的数据源——之前的评分完全忽略了供应商的并发上限，导致容量不足的供应商
// 与无限流的供应商获得相同的可用性评分。
func (p *GatewayScaleProvider) GetModelScale(ctx context.Context, credentialID int64) (*ScaleData, error) {
	// 首先获取 provider_id 与并发限制
	var providerID int64
	var concLimit, concLimitAuto *int
	err := p.db.QueryRow(ctx, `
		SELECT provider_id, concurrency_limit, concurrency_limit_auto
		FROM credentials
		WHERE id = $1
	`, credentialID).Scan(&providerID, &concLimit, &concLimitAuto)
	if err != nil {
		return nil, fmt.Errorf("get provider_id and concurrency: %w", err)
	}

	capacity := &ConcurrencyCapacity{}
	if concLimit != nil {
		capacity.ConcurrencyLimit = *concLimit
	}
	if concLimitAuto != nil {
		capacity.ConcurrencyLimitAuto = *concLimitAuto
	}
	// EffLimit：auto 优先，但绝不能突破人工配置的硬上限。
	// auto 反映系统动态调整后的实际承载；人工上限存在时对其做 cap，
	// 防止历史脏值或手工写入的过大 auto 值绕过 credentials 的硬约束。
	effLimit := capacity.ConcurrencyLimitAuto
	if effLimit <= 0 {
		effLimit = capacity.ConcurrencyLimit
	}
	if capacity.ConcurrencyLimit > 0 && effLimit > capacity.ConcurrencyLimit {
		effLimit = capacity.ConcurrencyLimit
	}
	capacity.EffLimit = effLimit
	// IsCapped：auto 被压低，说明曾因 503 触发降级（Tuner.decreaseConcurrency）
	if capacity.ConcurrencyLimit > 0 && capacity.ConcurrencyLimitAuto > 0 && capacity.ConcurrencyLimitAuto < capacity.ConcurrencyLimit {
		capacity.IsCapped = true
	}
	if effLimit == 0 {
		// 未配置并发上限：标记缺失维度（scorer 看到 EffLimit=0 会给中性分）
		capacity = nil
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
	data.ConcurrencyCapacity = capacity

	err = p.db.QueryRow(ctx, query, providerID).Scan(
		&data.TotalModels,
		&data.AvailableModels,
	)
	if err != nil {
		return nil, fmt.Errorf("query model scale: %w", err)
	}

	// 2026-08-11: 节点智商维度。聚合该凭据下所有节点的最新智商（node_iq_latest），
	// 取 overall_score 的样本数加权平均。无数据（表为空 / 该凭据无测试记录）时
	// 返回 nil，scorer 视为缺失维度跳过，不影响冷启动总分。
	// 用 credential_id 而非 provider_id：品质评分是按凭据计算的。
	var iqSum, iqAvg float64
	var iqN int
	err = p.db.QueryRow(ctx, `
		SELECT COALESCE(sum(overall_score), 0),
		       CASE WHEN count(*) > 0 THEN sum(overall_score)/count(*) ELSE 0 END,
		       count(*)
		FROM node_iq_latest
		WHERE credential_id = $1 AND overall_score IS NOT NULL`, credentialID).
		Scan(&iqSum, &iqAvg, &iqN)
	if err == nil && iqN > 0 {
		data.ModelIQSignal = &ModelIQSignal{AvgIQ: iqAvg, SampleN: iqN}
	}
	// 查询失败（表不存在/临时不可用）非致命：留 nil，scorer 跳过该维度。

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
