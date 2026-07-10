package feishubot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/eventbus"
)

// AlertSeverity 统一告警严重度。
type AlertSeverity string

const (
	SeverityLow      AlertSeverity = "low"
	SeverityMedium   AlertSeverity = "medium"
	SeverityHigh     AlertSeverity = "high"
	SeverityCritical AlertSeverity = "critical"
)

// Alert 通用告警事件。
//
// 来源：plugin.go 通过订阅 eventbus 自动构造，或由其他 hook 显式调用。
type Alert struct {
	ID         string // 全局唯一
	Category   string // 大类：prompt_injection / rate_limit / compression_latency / ...
	Severity   AlertSeverity
	Title      string
	Summary    string      // 一句话描述
	Fields     []CardField // 附加字段（租户、模型、IP 等）
	Source     string      // 产生告警的插件/钩子
	Timestamp  time.Time
	Recipients []string // 显式指定接收人；空则使用 Config.AllowedUsers
}

// AlertRouter 把 Alert 路由到 LarkChannel（经过去重 + 限流 + 免打扰过滤）。
//
// 线程安全：可被多个 goroutine 并发调用 PushAlert。
type AlertRouter struct {
	cfgMu              sync.RWMutex
	cfg                Config
	deduper            *Deduper
	limiter            *RateLimiter
	channel            LarkChannel
	recipientsProvider func() []string // 动态接收人（如白名单）
}

// NewAlertRouter 构造 AlertRouter。
func NewAlertRouter(channel LarkChannel) *AlertRouter {
	return &AlertRouter{
		deduper:            NewDeduper(60 * time.Second), // 默认 60s，可被 Configure 覆盖
		limiter:            NewRateLimiter(20),
		channel:            channel,
		recipientsProvider: func() []string { return nil },
	}
}

// Configure 用最新配置更新路由参数。
func (r *AlertRouter) Configure(cfg Config) {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	r.cfg = cfg
	r.deduper = NewDeduper(cfg.AlertDedupWindow)
	r.limiter = NewRateLimiter(cfg.AlertRateLimitPerMin)
	if cfg.AllowedUsers != nil {
		users := make([]string, len(cfg.AllowedUsers))
		copy(users, cfg.AllowedUsers)
		r.recipientsProvider = func() []string { return users }
	} else {
		r.recipientsProvider = func() []string { return nil }
	}
}

// Snapshot 返回当前快照（测试 / 监控）。
func (r *AlertRouter) Snapshot() Config {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	return r.cfg
}

// PushAlert 推送一条告警（应用所有过滤规则）。
//
// 返回值说明：
//   - sent=true 表示已成功发送（或转发到 channel）
//   - reason：未发送时的原因（用于日志与监控）
func (r *AlertRouter) PushAlert(ctx context.Context, a Alert) (sent bool, reason string) {
	r.cfgMu.RLock()
	cfg := r.cfg
	r.cfgMu.RUnlock()

	if !cfg.Enabled {
		return false, "module_disabled"
	}
	if !cfg.NotifyOnAlert {
		return false, "notify_on_alert_off"
	}
	if !cfg.IsSeverityPassing(string(a.Severity)) {
		return false, "severity_below_threshold"
	}

	// 免打扰：仅 critical 可破例
	if cfg.IsInQuietHours(time.Now()) && a.Severity != SeverityCritical {
		return false, "quiet_hours"
	}

	// 接收人：显式 > 白名单 > 空
	recipients := a.Recipients
	if len(recipients) == 0 {
		recipients = r.recipientsProvider()
	}
	if len(recipients) == 0 {
		// 无白名单且非显式 → 退化为「所有已绑定用户」语义
		// 实际上通知包会自动处理空 recipients（如群机器人 webhook）
		recipients = nil
	}

	// 去重
	fp := Fingerprint(a.Category, a.Source, string(a.Severity), a.Title)
	if dup, last := r.deduper.Check(fp); dup {
		slog.Debug("feishu_bot: alert dedup hit",
			"category", a.Category, "source", a.Source,
			"severity", a.Severity, "title", a.Title,
			"last_seen", last)
		return false, "dedup"
	}

	// 限流
	if r.limiter.Allow() {
		slog.Warn("feishu_bot: alert rate-limited",
			"category", a.Category, "severity", a.Severity,
			"limit_per_min", cfg.AlertRateLimitPerMin)
		// 节流时不发原 alert，但发送一条节流摘要卡片（best-effort）
		r.sendThrottledSummary(ctx, cfg, a)
		return false, "rate_limited"
	}

	// 渲染卡片并发送
	card := r.renderCard(cfg, a)
	msg := &Card{
		Header:   card.Header,
		Elements: card.Elements,
		Metadata: map[string]any{
			"alert_id":   a.ID,
			"category":   a.Category,
			"severity":   string(a.Severity),
			"recipients": recipients,
		},
	}

	if err := r.channel.SendCard(ctx, msg); err != nil {
		slog.Error("feishu_bot: send alert failed",
			"alert_id", a.ID, "category", a.Category,
			"severity", a.Severity, "error", err)
		return false, fmt.Sprintf("send_failed: %v", err)
	}
	slog.Info("feishu_bot: alert sent",
		"alert_id", a.ID, "category", a.Category,
		"severity", a.Severity, "recipients", len(recipients))
	return true, ""
}

// renderCard 按 CardTemplate 渲染告警卡片。
func (r *AlertRouter) renderCard(cfg Config, a Alert) *Card {
	tpl := pickHeaderTpl(a.Severity)
	header := CardHeader{Title: a.Title, Template: tpl}
	elements := []CardElement{
		{Type: "text", Text: a.Summary},
	}
	if len(a.Fields) > 0 {
		elements = append(elements, CardElement{Type: "field", Fields: a.Fields})
	}
	elements = append(elements, CardElement{
		Type: "note",
		Text: fmt.Sprintf("来源: %s · 时间: %s", a.Source, a.Timestamp.Format("2006-01-02 15:04:05")),
	})
	return &Card{Header: header, Elements: elements}
}

// sendThrottledSummary 发送节流摘要（best-effort，失败仅日志）。
func (r *AlertRouter) sendThrottledSummary(ctx context.Context, cfg Config, a Alert) {
	msg := &Card{
		Header: CardHeader{Title: "⚠️ 告警节流提示", Template: "orange"},
		Elements: []CardElement{
			{Type: "text", Text: fmt.Sprintf("最近 60 秒已超过速率限制 (%d 条/分钟)。", cfg.AlertRateLimitPerMin)},
			{Type: "note", Text: fmt.Sprintf("被丢弃的最新告警: %s [%s]", a.Title, a.Severity)},
		},
		Metadata: map[string]any{"recipients": nil},
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.channel.SendCard(cctx, msg); err != nil {
		slog.Warn("feishu_bot: throttled summary send failed", "error", err)
	}
}

// pickHeaderTpl 按严重度返回卡片配色。
func pickHeaderTpl(s AlertSeverity) string {
	switch s {
	case SeverityCritical:
		return "red"
	case SeverityHigh:
		return "orange"
	case SeverityMedium:
		return "blue"
	default:
		return "grey"
	}
}

// ── eventbus 订阅器 ─────────────────────────────────────────────────

// SubscribeApprovalEvents 订阅 sessionaudit 审批事件并转为告警。
//
// 由 main.go 在启动时调用：
//
//	bus.Subscribe(sessionaudit.EventTypeApprovalNeeded, router.OnApprovalNeeded)
func (r *AlertRouter) OnApprovalNeeded(_ context.Context, e eventbus.Event) error {
	// 简化：直接构造 critical 告警推送
	// 生产环境应该解析 event 字段（session_id、approval_id、risk_level 等）
	if e.Type() != "session.approval_needed" {
		return nil
	}
	alert := Alert{
		ID:        fmt.Sprintf("approval-%d", time.Now().UnixNano()),
		Category:  "approval",
		Severity:  SeverityHigh,
		Title:     "🔐 新的审批请求",
		Summary:   "检测到高风险操作需要人工审批，请尽快处理。",
		Source:    "sessionaudit.approval",
		Timestamp: time.Now(),
	}
	_, _ = r.PushAlert(context.Background(), alert)
	return nil
}

func (r *AlertRouter) OnApprovalDecided(_ context.Context, e eventbus.Event) error {
	if e.Type() != "session.approval_decided" {
		return nil
	}
	alert := Alert{
		ID:        fmt.Sprintf("approval-decided-%d", time.Now().UnixNano()),
		Category:  "approval",
		Severity:  SeverityLow,
		Title:     "✅ 审批已处理",
		Summary:   "审批请求已处理完成。",
		Source:    "sessionaudit.approval",
		Timestamp: time.Now(),
	}
	_, _ = r.PushAlert(context.Background(), alert)
	return nil
}
