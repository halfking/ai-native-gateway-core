package anomaly

import (
	"context"
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus 指标
var (
	anomalyDetections = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_anomaly_detections_total",
			Help: "异常检测触发次数",
		},
		[]string{"code", "severity", "action"},
	)

	anomalyBlockedRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_anomaly_blocked_requests_total",
			Help: "被异常检测拦截的请求数",
		},
		[]string{"code", "api_key_id"},
	)
)

// Detector 统一检测器接口
type Detector interface {
	Check(ctx context.Context, req *Request) *AnomalyResult
}

// Manager 异常检测管理器
type Manager struct {
	detectors []Detector
	mu        sync.RWMutex
	enabled   bool
}

// NewManager 创建管理器
func NewManager() *Manager {
	return &Manager{
		detectors: make([]Detector, 0),
		enabled:   true,
	}
}

// AddDetector 添加检测器
func (m *Manager) AddDetector(d Detector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.detectors = append(m.detectors, d)
}

// Enable 启用检测
func (m *Manager) Enable() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = true
}

// Disable 禁用检测（用于调试）
func (m *Manager) Disable() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = false
}

// CheckAll 运行所有检测器
func (m *Manager) CheckAll(ctx context.Context, req *Request) []*AnomalyResult {
	m.mu.RLock()
	if !m.enabled {
		m.mu.RUnlock()
		return nil
	}
	detectors := m.detectors
	m.mu.RUnlock()

	results := make([]*AnomalyResult, 0)
	for _, detector := range detectors {
		if result := detector.Check(ctx, req); result != nil {
			results = append(results, result)

			// 指标埋点：记录检测结果。
			// 2026-11-15：ObserveOnly 面（频率检测）不进 blocked 指标——它
			// 不拦截；action=observed 供看板区分观察信号与执法面。
			apiKeyID := fmt.Sprintf("%d", req.APIKeyID)
			action := "passed"
			switch {
			case result.ObserveOnly:
				action = "observed"
			case ShouldBlock(result.Severity):
				action = "blocked"
				// 记录被拦截的请求
				anomalyBlockedRequests.WithLabelValues(result.Code, apiKeyID).Inc()
			case result.Severity == "medium":
				action = "warned"
			}
			anomalyDetections.WithLabelValues(result.Code, result.Severity, action).Inc()
		}
	}

	return results
}

// SelectHighestSeverity 选择最高严重级别的异常
func SelectHighestSeverity(results []*AnomalyResult) *AnomalyResult {
	if len(results) == 0 {
		return nil
	}

	severityOrder := map[string]int{
		"critical": 4,
		"high":     3,
		"medium":   2,
		"low":      1,
	}

	highest := results[0]
	highestScore := severityOrder[highest.Severity]

	for _, result := range results[1:] {
		score := severityOrder[result.Severity]
		if score > highestScore {
			highest = result
			highestScore = score
		}
	}

	return highest
}

// FormatAnomalyError 格式化异常为错误响应
func FormatAnomalyError(result *AnomalyResult) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"type":     "gateway_anomaly_detected",
			"code":     result.Code,
			"message":  result.Message,
			"severity": result.Severity,
			"metrics":  result.Metrics,
		},
	}
}

// GetHTTPStatus 根据异常严重程度返回 HTTP 状态码
func GetHTTPStatus(severity string) int {
	switch severity {
	case "critical", "high":
		return 429 // Too Many Requests
	case "medium":
		return 400 // Bad Request
	default:
		return 200 // 低级别只记录不拦截
	}
}

// ShouldBlock 判断是否应该拦截请求
func ShouldBlock(severity string) bool {
	return severity == "high" || severity == "critical"
}

// SessionRateDetector 包装器，实现 Detector 接口。
// 2026-11-15：窗口按 api key ID 分桶；调用方（wrapAnomalyDetector）负责在
// 无 key 身份时跳过（空桶共享缺陷的消除面）。
type SessionRateDetector struct {
	limiter *SessionRateLimiter
}

// NewSessionRateDetector 创建会话频率检测器
func NewSessionRateDetector() *SessionRateDetector {
	return &SessionRateDetector{
		limiter: NewSessionRateLimiter(),
	}
}

// Check 实现 Detector 接口
func (d *SessionRateDetector) Check(ctx context.Context, req *Request) *AnomalyResult {
	return d.limiter.Check(ctx, req.APIKeyID)
}

// GetStats 获取会话统计
func (d *SessionRateDetector) GetStats(keyID int64) map[string]interface{} {
	return d.limiter.GetStats(keyID)
}

// Clear 清除会话
func (d *SessionRateDetector) Clear(keyID int64) {
	d.limiter.Clear(keyID)
}

// NewDefaultManager 创建带默认检测器的管理器
func NewDefaultManager() (*Manager, error) {
	manager := NewManager()

	// 检测器 1：附件指纹
	fpDetector, err := NewAttachmentFingerprintDetector(1000)
	if err != nil {
		return nil, fmt.Errorf("create fingerprint detector: %w", err)
	}
	manager.AddDetector(fpDetector)

	// 检测器 2：媒体文本比
	manager.AddDetector(NewMediaTextRatioDetector())

	// 检测器 3：请求频率
	manager.AddDetector(NewSessionRateDetector())

	return manager, nil
}
