package alerting

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Severity 告警级别
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Alert 告警
type Alert struct {
	ID          string                 `json:"id"`
	Title       string                 `json:"title"`
	Message     string                 `json:"message"`
	Severity    Severity               `json:"severity"`
	Labels      map[string]string      `json:"labels"`
	Annotations map[string]string      `json:"annotations"`
	CreatedAt   time.Time              `json:"created_at"`
	ResolvedAt  *time.Time             `json:"resolved_at,omitempty"`
	Status      string                 `json:"status"` // firing | resolved
}

// Rule 告警规则
type Rule struct {
	Name        string
	Description string
	Condition   func() bool
	Severity    Severity
	Interval    time.Duration
	Labels      map[string]string
}

// Notifier 通知器接口
type Notifier interface {
	Notify(ctx context.Context, alert *Alert) error
	Name() string
}

// Manager 告警管理器
type Manager struct {
	rules     []*Rule
	notifiers []Notifier
	alerts    map[string]*Alert
	mu        sync.RWMutex
	stopChan  chan struct{}
	wg        sync.WaitGroup
}

// NewManager 创建告警管理器
func NewManager() *Manager {
	return &Manager{
		rules:     make([]*Rule, 0),
		notifiers: make([]Notifier, 0),
		alerts:    make(map[string]*Alert),
		stopChan:  make(chan struct{}),
	}
}

// AddRule 添加规则
func (m *Manager) AddRule(rule *Rule) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = append(m.rules, rule)
}

// AddNotifier 添加通知器
func (m *Manager) AddNotifier(notifier Notifier) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifiers = append(m.notifiers, notifier)
}

// Start 启动告警管理器
func (m *Manager) Start() {
	m.mu.RLock()
	rules := make([]*Rule, len(m.rules))
	copy(rules, m.rules)
	m.mu.RUnlock()

	// 为每个规则启动一个 goroutine
	for _, rule := range rules {
		m.wg.Add(1)
		go m.runRule(rule)
	}
}

// Stop 停止告警管理器
func (m *Manager) Stop() {
	close(m.stopChan)
	m.wg.Wait()
}

// runRule 运行规则
func (m *Manager) runRule(rule *Rule) {
	defer m.wg.Done()

	ticker := time.NewTicker(rule.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.evaluateRule(rule)
		case <-m.stopChan:
			return
		}
	}
}

// evaluateRule 评估规则
//
// 2026-08-08 audit review: 此函数使用 Lock/Unlock 配对（非 defer 形式），
// 适用于简单的 read-modify-write 短临界区。当前代码没有早期返回，
// 未来修改时若添加 `return` 必须把对应 Unlock 改为 defer Unlock。
func (m *Manager) evaluateRule(rule *Rule) {
	alertID := rule.Name

	// 检查条件
	firing := rule.Condition()

	m.mu.Lock()
	existingAlert, exists := m.alerts[alertID]
	m.mu.Unlock()

	if firing {
		// 条件满足
		if !exists || existingAlert.Status == "resolved" {
			// 创建新告警
			alert := &Alert{
				ID:       alertID,
				Title:    rule.Name,
				Message:  rule.Description,
				Severity: rule.Severity,
				Labels:   rule.Labels,
				Annotations: map[string]string{
					"rule": rule.Name,
				},
				CreatedAt: time.Now(),
				Status:    "firing",
			}

			m.mu.Lock()
			m.alerts[alertID] = alert
			m.mu.Unlock()

			// 发送通知
			m.notify(alert)
		}
	} else {
		// 条件不满足
		if exists && existingAlert.Status == "firing" {
			// 解决告警
			now := time.Now()
			existingAlert.ResolvedAt = &now
			existingAlert.Status = "resolved"

			m.mu.Lock()
			m.alerts[alertID] = existingAlert
			m.mu.Unlock()

			// 发送解决通知
			m.notify(existingAlert)
		}
	}
}

// notify 发送通知
func (m *Manager) notify(alert *Alert) {
	m.mu.RLock()
	notifiers := make([]Notifier, len(m.notifiers))
	copy(notifiers, m.notifiers)
	m.mu.RUnlock()

	ctx := context.Background()

	for _, notifier := range notifiers {
		go func(n Notifier) {
			if err := n.Notify(ctx, alert); err != nil {
				// 记录通知失败（实际应该记录日志）
				fmt.Printf("Failed to notify via %s: %v\n", n.Name(), err)
			}
		}(notifier)
	}

	// 记录指标
	RecordAlert(alert.Severity, alert.Status)
}

// GetAlerts 获取所有告警
func (m *Manager) GetAlerts() []*Alert {
	m.mu.RLock()
	defer m.mu.RUnlock()

	alerts := make([]*Alert, 0, len(m.alerts))
	for _, alert := range m.alerts {
		alerts = append(alerts, alert)
	}
	return alerts
}

// GetFiringAlerts 获取激活的告警
func (m *Manager) GetFiringAlerts() []*Alert {
	m.mu.RLock()
	defer m.mu.RUnlock()

	alerts := make([]*Alert, 0)
	for _, alert := range m.alerts {
		if alert.Status == "firing" {
			alerts = append(alerts, alert)
		}
	}
	return alerts
}

// ConsoleNotifier 控制台通知器
type ConsoleNotifier struct{}

// NewConsoleNotifier 创建控制台通知器
func NewConsoleNotifier() *ConsoleNotifier {
	return &ConsoleNotifier{}
}

// Notify 发送通知
func (n *ConsoleNotifier) Notify(ctx context.Context, alert *Alert) error {
	fmt.Printf("[ALERT] [%s] %s: %s (status: %s)\n",
		alert.Severity, alert.Title, alert.Message, alert.Status)
	return nil
}

// Name 通知器名称
func (n *ConsoleNotifier) Name() string {
	return "console"
}

// WebhookNotifier Webhook 通知器
type WebhookNotifier struct {
	url string
}

// NewWebhookNotifier 创建 Webhook 通知器
func NewWebhookNotifier(url string) *WebhookNotifier {
	return &WebhookNotifier{
		url: url,
	}
}

// Notify 发送通知
func (n *WebhookNotifier) Notify(ctx context.Context, alert *Alert) error {
	// 实际实现应该发送 HTTP POST 请求
	fmt.Printf("[WEBHOOK] Sending alert to %s: %s\n", n.url, alert.Title)
	return nil
}

// Name 通知器名称
func (n *WebhookNotifier) Name() string {
	return "webhook"
}

// 预定义规则

// HighErrorRateRule 高错误率规则
func HighErrorRateRule(getErrorRate func() float64, threshold float64) *Rule {
	return &Rule{
		Name:        "high_error_rate",
		Description: fmt.Sprintf("Error rate exceeds %.1f%%", threshold*100),
		Condition: func() bool {
			return getErrorRate() > threshold
		},
		Severity: SeverityCritical,
		Interval: 1 * time.Minute,
		Labels: map[string]string{
			"type": "error_rate",
		},
	}
}

// HighLatencyRule 高延迟规则
func HighLatencyRule(getP99Latency func() time.Duration, threshold time.Duration) *Rule {
	return &Rule{
		Name:        "high_latency",
		Description: fmt.Sprintf("P99 latency exceeds %v", threshold),
		Condition: func() bool {
			return getP99Latency() > threshold
		},
		Severity: SeverityWarning,
		Interval: 1 * time.Minute,
		Labels: map[string]string{
			"type": "latency",
		},
	}
}

// LowAvailabilityRule 低可用性规则
func LowAvailabilityRule(getAvailability func() float64, threshold float64) *Rule {
	return &Rule{
		Name:        "low_availability",
		Description: fmt.Sprintf("Availability below %.1f%%", threshold*100),
		Condition: func() bool {
			return getAvailability() < threshold
		},
		Severity: SeverityCritical,
		Interval: 30 * time.Second,
		Labels: map[string]string{
			"type": "availability",
		},
	}
}

// HighMemoryUsageRule 高内存使用规则
func HighMemoryUsageRule(getMemoryUsage func() float64, threshold float64) *Rule {
	return &Rule{
		Name:        "high_memory_usage",
		Description: fmt.Sprintf("Memory usage exceeds %.1f%%", threshold*100),
		Condition: func() bool {
			return getMemoryUsage() > threshold
		},
		Severity: SeverityWarning,
		Interval: 2 * time.Minute,
		Labels: map[string]string{
			"type": "resource",
		},
	}
}
