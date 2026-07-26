package providerprofile

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Collector 采集器接口
type Collector interface {
	// CollectMetrics 采集指标
	CollectMetrics(ctx context.Context) error
}

// LightweightCollector 轻量级采集器
// 职责：收集网络延迟、可用性、稳定性、规模指标
type LightweightCollector struct {
	metricsStore    MetricsStore
	networkProber   NetworkProber
	requestAnalyzer RequestAnalyzer
	scaleProvider   ScaleProvider
	credentialLister CredentialLister
	concurrency     int
}

// NetworkProber 网络探测器接口
type NetworkProber interface {
	// ProbeLatency 探测网络延迟，返回多次探测结果（ms）
	ProbeLatency(ctx context.Context, credentialID int64, probeCount int) ([]int, error)
}

// RequestAnalyzer 请求分析器接口
type RequestAnalyzer interface {
	// AnalyzeRequests 分析最近N小时的请求统计
	AnalyzeRequests(ctx context.Context, credentialID int64, hours int) (*RequestStats, error)
}

// RequestStats 请求统计数据
type RequestStats struct {
	TotalRequests   int
	SuccessRequests int
	ErrorCount      int
	ErrorTypes      map[string]int
	AvgTTFTMs       int
	AvgDurationMs   int
}

// ScaleProvider 规模数据提供者接口
type ScaleProvider interface {
	// GetModelScale 获取供应商的模型规模数据
	GetModelScale(ctx context.Context, credentialID int64) (*ScaleData, error)
}

// ScaleData 规模数据
type ScaleData struct {
	ProviderID      int64
	TotalModels     int
	AvailableModels int
}

// CredentialLister 凭证列表提供者接口
type CredentialLister interface {
	// ListActiveCredentials 获取所有活跃的凭证
	ListActiveCredentials(ctx context.Context) ([]int64, error)
}

// NewLightweightCollector 创建轻量级采集器
func NewLightweightCollector(
	metricsStore MetricsStore,
	networkProber NetworkProber,
	requestAnalyzer RequestAnalyzer,
	scaleProvider ScaleProvider,
	credentialLister CredentialLister,
) *LightweightCollector {
	return &LightweightCollector{
		metricsStore:     metricsStore,
		networkProber:    networkProber,
		requestAnalyzer:  requestAnalyzer,
		scaleProvider:    scaleProvider,
		credentialLister: credentialLister,
		concurrency:      10, // 最多10个并发
	}
}

// CollectMetrics 采集指标（并发执行）
func (c *LightweightCollector) CollectMetrics(ctx context.Context) error {
	// 1. 获取所有活跃凭证
	credentialIDs, err := c.credentialLister.ListActiveCredentials(ctx)
	if err != nil {
		return fmt.Errorf("list active credentials: %w", err)
	}

	if len(credentialIDs) == 0 {
		return nil // 没有活跃凭证
	}

	// 2. 并发采集（最多10个并发）
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, c.concurrency)
	errChan := make(chan error, len(credentialIDs))

	for _, credID := range credentialIDs {
		wg.Add(1)
		go func(credentialID int64) {
			defer wg.Done()

			// 获取信号量
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// 采集单个凭证的指标
			if err := c.collectForCredential(ctx, credentialID); err != nil {
				errChan <- fmt.Errorf("collect for credential %d: %w", credentialID, err)
			}
		}(credID)
	}

	wg.Wait()
	close(errChan)

	// 3. 收集错误（如果有）
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		return fmt.Errorf("collected with %d errors: first error: %w", len(errors), errors[0])
	}

	return nil
}

// collectForCredential 采集单个凭证的指标
func (c *LightweightCollector) collectForCredential(ctx context.Context, credentialID int64) error {
	now := time.Now()

	// 1. 探测网络延迟（探测3次）
	latencies, err := c.networkProber.ProbeLatency(ctx, credentialID, 3)
	if err != nil {
		return fmt.Errorf("probe latency: %w", err)
	}

	networkMetrics := c.calculateNetworkMetrics(latencies)

	// 2. 分析最近2小时的请求统计
	requestStats, err := c.requestAnalyzer.AnalyzeRequests(ctx, credentialID, 2)
	if err != nil {
		return fmt.Errorf("analyze requests: %w", err)
	}

	availabilityMetrics := &AvailabilityMetrics{
		TotalRequests:   requestStats.TotalRequests,
		SuccessRequests: requestStats.SuccessRequests,
		AvgTTFTMs:       requestStats.AvgTTFTMs,
		AvgDurationMs:   requestStats.AvgDurationMs,
	}

	stabilityMetrics := &StabilityMetrics{
		ErrorCount: requestStats.ErrorCount,
	}
	// Always initialize ErrorTypes to a non-nil empty map so json.Marshal
	// produces "{}" instead of "null". PostgreSQL JSONB rejects bare null.
	if requestStats.ErrorTypes != nil {
		stabilityMetrics.ErrorTypes = requestStats.ErrorTypes
	} else {
		stabilityMetrics.ErrorTypes = map[string]int{}
	}

	// 3. 获取规模数据
	scaleData, err := c.scaleProvider.GetModelScale(ctx, credentialID)
	if err != nil {
		return fmt.Errorf("get model scale: %w", err)
	}

	scaleMetrics := &ScaleMetrics{
		TotalModels:     scaleData.TotalModels,
		AvailableModels: scaleData.AvailableModels,
	}

	// 4. 组装快照
	snapshot := &MetricSnapshot{
		CredentialID:        credentialID,
		ProviderID:          scaleData.ProviderID,
		MetricTime:          now,
		TimeSlot:            DetermineTimeSlot(now),
		NetworkMetrics:      networkMetrics,
		AvailabilityMetrics: availabilityMetrics,
		StabilityMetrics:    stabilityMetrics,
		ScaleMetrics:        scaleMetrics,
	}

	// 5. 保存到数据库
	if err := c.metricsStore.SaveSnapshot(ctx, snapshot); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}

	return nil
}

// calculateNetworkMetrics 计算网络延迟的P50/P95/P99
func (c *LightweightCollector) calculateNetworkMetrics(latencies []int) *NetworkMetrics {
	if len(latencies) == 0 {
		return &NetworkMetrics{}
	}

	// 排序以计算百分位
	sorted := make([]int, len(latencies))
	copy(sorted, latencies)
	sort.Ints(sorted)

	return &NetworkMetrics{
		P50: c.percentile(sorted, 50),
		P95: c.percentile(sorted, 95),
		P99: c.percentile(sorted, 99),
	}
}

// percentile 计算百分位数
func (c *LightweightCollector) percentile(sorted []int, p int) int {
	if len(sorted) == 0 {
		return 0
	}

	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}

	// 使用最近排名法
	index := int(float64(len(sorted)-1) * float64(p) / 100.0)
	return sorted[index]
}
