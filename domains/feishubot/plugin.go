// Package feishubot 实现飞书机器人模块的核心逻辑。
//
// 飞书机器人模块通过现有 Hook 架构集成到 llm-gateway-go：
//   - Plugin（v4 governance 阶段）：从 PipelineRequest 提取告警/审批事件，
//     转发到 LarkBotChannel
//   - AlertRouter（eventbus 订阅）：监听 sessionaudit / promptinjection 等
//     领域事件，统一路由与去重
//   - CallbackHandler（HTTP 入口）：处理飞书回调签名校验、命令路由、白名单
//
// 设计原则：
//   - 完全可插拔：未启用时所有组件降级为 no-op
//   - 配置驱动：所有行为通过 settings.Global 读取 feishu_bot.* 配置
//   - 安全第一：默认强制签名校验、时间戳防重放、admin-only 命令
//   - 复用现有：直接使用 notification.LarkBotChannel 发送消息，
//     不重复实现飞书 API 客户端
package feishubot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// Plugin 是 feishu_bot 模块的主入口。
//
// 生命周期：
//  1. main.go 在启动时构造 NewPlugin()
//  2. 若 feishu_bot.enabled=true，调用 Plugin.Start() 启动后台订阅/路由
//  3. 关闭时调用 Plugin.Stop()
type Plugin struct {
	cfg     Config
	mu      sync.RWMutex
	stopCh  chan struct{}
	stopped bool

	// 依赖（注入）
	channel LarkChannel // 复用 notification.LarkBotChannel

	// 2026-07-09: 可选 DB 连接池。nil → 走 settings_kv 兼容模式。
	// 注入 DB 后，feishu_bot.allowed_users 优先读 feishu_bot_routing_rules 表。
	db *pgxpool.Pool
}

// SetDBPool 注入 DB 连接池（main.go 在 dbConn 可用时调用）。
//
// nil 是合法的，Plugin 仍可工作（走 settings_kv）。
// 设计：DB 池在 Plugin 启动期注入，ReloConfig 期间读取，避免热加载时连接池已关闭。
func (p *Plugin) SetDBPool(pool *pgxpool.Pool) {
	p.mu.Lock()
	p.db = pool
	p.mu.Unlock()
}

// LarkChannel 是发送消息的最小接口（便于测试 mock）。
type LarkChannel interface {
	Name() string
	Send(ctx context.Context, msg *Message) error
	SendCard(ctx context.Context, card *Card) error
}

// Message 通用文本消息（与 notification.Message 解耦以避免包循环）。
type Message struct {
	ID         string
	Title      string
	Content    string
	Recipients []string
	Priority   string // urgent/high/normal/low
}

// Card 通用交互式卡片。
type Card struct {
	Header   CardHeader
	Elements []CardElement
	Actions  []CardAction
	Metadata map[string]any // 至少包含 recipients
}

// CardHeader 卡片头。
type CardHeader struct {
	Title    string
	Template string // blue/green/red/orange/grey
}

// CardElement 卡片元素。
type CardElement struct {
	Type   string // text/field/divider/note
	Text   string
	Fields []CardField
}

// CardField 键值对字段。
type CardField struct {
	Key   string
	Value string
	Short bool
}

// CardAction 卡片按钮。
type CardAction struct {
	ID    string
	Text  string
	Style string // primary/danger/default
	Value map[string]any
}

// NewPlugin 创建 Plugin 实例（不启动后台 goroutine）。
func NewPlugin(channel LarkChannel) *Plugin {
	return &Plugin{
		channel: channel,
		stopCh:  make(chan struct{}),
	}
}

// Config 读取 feishu_bot.* 配置。
type Config struct {
	Enabled          bool
	WebhookURL       string
	VerifyToken      string
	EncryptKey       string
	ConnectionMode   string // webhook/app
	NotifyOnAlert    bool
	NotifyOnApproval bool
	AllowedUsers     []string // 解析后的 OpenID 切片

	// 告警
	AlertSeverityMin     string // low/medium/high/critical
	AlertRateLimitPerMin int
	AlertDedupWindow     time.Duration
	QuietHoursEnabled    bool
	QuietHoursStart      string // HH:MM
	QuietHoursEnd        string // HH:MM
	CardTemplate         string // compact/standard/verbose

	// 审批
	ApprovalExpiryReminder      time.Duration
	ApprovalAutoMentionCritical bool

	// 命令
	CommandsEnabled   bool
	CommandsAdminOnly bool

	// 安全
	SignatureRequired      bool
	TimestampWindowSeconds int
}

// LoadConfig 从 settings.Global + 可选 DB 表读取 feishu_bot.* 当前生效值。
//
// 任何读失败都不 panic，返回零值 + 错误；上层决定如何处理。
//
// 数据源合并策略（2026-07-09）：
//   - feishu_bot.allowed_users：DB 表 feishu_bot_routing_rules 优先，settings_kv 兜底
//   - 其余配置：仅从 settings_kv 读取
//
// 合并去重规则：DB 行 + 逗号串中重复的 OpenID 去重（按字符串匹配）。
func LoadConfig() (Config, error) {
	cfg := Config{
		Enabled:                     false,
		AlertSeverityMin:            "high",
		AlertRateLimitPerMin:        20,
		AlertDedupWindow:            60 * time.Second,
		QuietHoursEnabled:           false,
		QuietHoursStart:             "22:00",
		QuietHoursEnd:               "08:00",
		CardTemplate:                "standard",
		ApprovalExpiryReminder:      5 * time.Minute,
		ApprovalAutoMentionCritical: true,
		CommandsEnabled:             true,
		CommandsAdminOnly:           true,
		SignatureRequired:           true,
		TimestampWindowSeconds:      300,
		ConnectionMode:              "webhook",
	}

	if settings.Global == nil {
		return cfg, fmt.Errorf("feishubot: settings.Global is nil")
	}

	readBool := func(key string, dst *bool) {
		raw, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
		if err != nil || raw == nil {
			return
		}
		_ = json.Unmarshal(raw, dst)
	}
	readString := func(key string, dst *string) {
		raw, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
		if err != nil || raw == nil {
			return
		}
		_ = json.Unmarshal(raw, dst)
	}
	readInt := func(key string, dst *int) {
		raw, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
		if err != nil || raw == nil {
			return
		}
		var v int
		if err := json.Unmarshal(raw, &v); err == nil {
			*dst = v
		}
	}

	readBool("feishu_bot.enabled", &cfg.Enabled)
	readString("feishu_bot.webhook_url", &cfg.WebhookURL)
	readString("feishu_bot.verify_token", &cfg.VerifyToken)
	readString("feishu_bot.encrypt_key", &cfg.EncryptKey)
	readString("feishu_bot.connection_mode", &cfg.ConnectionMode)
	readBool("feishu_bot.notify_on_alert", &cfg.NotifyOnAlert)
	readBool("feishu_bot.notify_on_approval", &cfg.NotifyOnApproval)

	var usersRaw string
	readString("feishu_bot.allowed_users", &usersRaw)
	cfg.AllowedUsers = parseUserList(usersRaw)

	readString("feishu_bot.alert.severity_min", &cfg.AlertSeverityMin)
	readInt("feishu_bot.alert.rate_limit_per_minute", &cfg.AlertRateLimitPerMin)
	var dedupSec int
	readInt("feishu_bot.alert.dedup_window_seconds", &dedupSec)
	if dedupSec > 0 {
		cfg.AlertDedupWindow = time.Duration(dedupSec) * time.Second
	}
	readBool("feishu_bot.alert.quiet_hours_enabled", &cfg.QuietHoursEnabled)
	readString("feishu_bot.alert.quiet_hours_start", &cfg.QuietHoursStart)
	readString("feishu_bot.alert.quiet_hours_end", &cfg.QuietHoursEnd)
	readString("feishu_bot.alert.card_template", &cfg.CardTemplate)

	var reminderMin int
	readInt("feishu_bot.approval.expiry_reminder_minutes", &reminderMin)
	if reminderMin > 0 {
		cfg.ApprovalExpiryReminder = time.Duration(reminderMin) * time.Minute
	}
	readBool("feishu_bot.approval.auto_mention_on_critical", &cfg.ApprovalAutoMentionCritical)

	readBool("feishu_bot.commands.enabled", &cfg.CommandsEnabled)
	readBool("feishu_bot.commands.admin_only", &cfg.CommandsAdminOnly)

	readBool("feishu_bot.signature_required", &cfg.SignatureRequired)
	readInt("feishu_bot.timestamp_window_seconds", &cfg.TimestampWindowSeconds)

	return cfg, nil
}

// EnabledEffective 返回 feishu_bot 是否应在当前请求/事件路径上启用。
//
// 与 cfg.Enabled 不同：cfg.Enabled 是用户显式开关；
// EnabledEffective 同时考虑模块依赖（Requires）的所有其他模块是否启用。
func (p *Plugin) EnabledEffective(enabledMap map[string]bool) bool {
	p.mu.RLock()
	c := p.cfg
	p.mu.RUnlock()

	if !c.Enabled {
		return false
	}
	// 模块依赖：必须先开 compression/cache/prompt_injection/session_audit
	for _, dep := range requiredModules {
		if !enabledMap[dep] {
			return false
		}
	}
	return true
}

// requiredModules feishu_bot 强依赖的核心模块集合。
//
// 与 admin/modules.go 中 feishu_bot 的 Requires 字段保持同步。
// 任一依赖未启用时，插件主动降级为 no-op（不发送通知）。
var requiredModules = []string{"compression", "cache", "prompt_injection", "session_audit"}

// ReloadConfig 热加载配置（settings 变更后调用）。
//
// 2026-07-09: 同时重读 feishu_bot.allowed_users（DB + settings_kv 合并）。
func (p *Plugin) ReloadConfig() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	// ReloadAllowedUsers 走 DB 路径，与 cfg.AllowedUsers 合并
	if rerr := p.ReloadAllowedUsers(); rerr == nil {
		// 合并成功：用 DB 合并后的版本覆盖
		p.mu.RLock()
		cfg.AllowedUsers = p.cfg.AllowedUsers
		p.mu.RUnlock()
	}
	p.mu.Lock()
	p.cfg = cfg
	p.mu.Unlock()
	slog.Info("feishu_bot: config reloaded",
		"enabled", cfg.Enabled,
		"notify_on_alert", cfg.NotifyOnAlert,
		"notify_on_approval", cfg.NotifyOnApproval,
		"allowed_users", len(cfg.AllowedUsers),
	)
	return nil
}

// Snapshot 返回当前配置快照（用于 API 展示与测试）。
func (p *Plugin) Snapshot() Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

// Start 启动后台 goroutine（当前为空，预留扩展点）。
//
// 设计要点：插件本身不做长驻 goroutine，事件订阅由 AlertRouter 显式启动。
// 这样调用方（main.go）能清晰看到资源生命周期。
func (p *Plugin) Start(_ context.Context) error {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return fmt.Errorf("feishubot: plugin already stopped")
	}
	p.mu.Unlock()
	if err := p.ReloadConfig(); err != nil {
		slog.Warn("feishubot: initial config load failed", "error", err)
	}
	return nil
}

// Stop 标记插件停止（幂等）。
func (p *Plugin) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true
	close(p.stopCh)
}

// parseUserList 解析逗号分隔的 OpenID 列表。
//
// 容忍：多余空白、空白项、空字符串。
func parseUserList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IsUserAllowed 检查 OpenID 是否在白名单内。
//
// 白名单为空时：若 CommandsAdminOnly=true 则全部拒绝；否则全部放行。
// 这是 fail-secure 设计：管理员忘记配置白名单时不会意外放权。
func (c Config) IsUserAllowed(openID string) bool {
	if len(c.AllowedUsers) == 0 {
		return !c.CommandsAdminOnly
	}
	for _, u := range c.AllowedUsers {
		if u == openID {
			return true
		}
	}
	return false
}

// IsSeverityPassing 判断告警是否达到推送阈值。
func (c Config) IsSeverityPassing(severity string) bool {
	order := map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}
	min, ok1 := order[c.AlertSeverityMin]
	cur, ok2 := order[severity]
	if !ok1 || !ok2 {
		// 未知严重度：默认放行（避免漏报）
		return true
	}
	return cur >= min
}

// IsInQuietHours 判断当前时间是否处于免打扰时段。
//
// 跨夜逻辑：start > end 时（如 22:00 → 08:00），处于 [start, 24:00) ∪ [00:00, end)。
func (c Config) IsInQuietHours(now time.Time) bool {
	if !c.QuietHoursEnabled {
		return false
	}
	start, err1 := parseHHMM(c.QuietHoursStart)
	end, err2 := parseHHMM(c.QuietHoursEnd)
	if err1 != nil || err2 != nil {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	if start <= end {
		return cur >= start && cur < end
	}
	// 跨夜
	return cur >= start || cur < end
}

// parseHHMM 解析 "HH:MM" 为分钟数。
func parseHHMM(s string) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid HH:MM: %q", s)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("invalid hour: %q", parts[0])
	}
	m, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid minute: %q", parts[1])
	}
	return h*60 + m, nil
}

// ── 签名校验 ────────────────────────────────────────────────────────

// VerifyLarkSignature 校验飞书加密回调签名。
//
// 飞书 v2 签名算法：HMAC-SHA256(timestamp + nonce + encrypt_key + body, encrypt_key) → base64。
//
// 返回 true 表示签名匹配。空签名或空 encrypt_key 一律拒绝（fail-secure）。
func VerifyLarkSignature(timestamp, nonce, body, signature, encryptKey string) bool {
	if signature == "" || encryptKey == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(encryptKey))
	mac.Write([]byte(timestamp))
	mac.Write([]byte(nonce))
	mac.Write([]byte(body))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// VerifyLarkTimestamp 检查 timestamp 与当前时间差是否在窗口内。
//
// 用于重放保护：飞书回调 X-Lark-Request-Timestamp 头携带秒级时间戳。
// windowSeconds <= 0 时使用默认值 300。
func VerifyLarkTimestamp(timestampStr string, windowSeconds int) bool {
	if windowSeconds <= 0 {
		windowSeconds = 300
	}
	ts, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return false
	}
	t := time.Unix(ts, 0)
	delta := time.Since(t)
	if delta < 0 {
		delta = -delta
	}
	return delta <= time.Duration(windowSeconds)*time.Second
}

// ── 测试 / 调试辅助 ────────────────────────────────────────────────

// SortedUserList 用于 API 展示 / 调试。
func (c Config) SortedUserList() []string {
	out := make([]string, len(c.AllowedUsers))
	copy(out, c.AllowedUsers)
	sort.Strings(out)
	return out
}

// AsJSON 用于 /api/admin/modules/{key} 响应中嵌入 debug 字段。
func (c Config) AsJSON() map[string]any {
	return map[string]any{
		"enabled":                c.Enabled,
		"webhook_url_set":        c.WebhookURL != "",
		"verify_token_set":       c.VerifyToken != "",
		"encrypt_key_set":        c.EncryptKey != "",
		"connection_mode":        c.ConnectionMode,
		"notify_on_alert":        c.NotifyOnAlert,
		"notify_on_approval":     c.NotifyOnApproval,
		"allowed_user_count":     len(c.AllowedUsers),
		"alert_severity_min":     c.AlertSeverityMin,
		"alert_rate_limit_min":   c.AlertRateLimitPerMin,
		"alert_dedup_window_sec": int(c.AlertDedupWindow / time.Second),
		"quiet_hours_enabled":    c.QuietHoursEnabled,
		"quiet_hours_window":     c.QuietHoursStart + "–" + c.QuietHoursEnd,
		"card_template":          c.CardTemplate,
		"approval_expiry_min":    int(c.ApprovalExpiryReminder / time.Minute),
		"approval_mention_crit":  c.ApprovalAutoMentionCritical,
		"commands_enabled":       c.CommandsEnabled,
		"commands_admin_only":    c.CommandsAdminOnly,
		"signature_required":     c.SignatureRequired,
		"timestamp_window_sec":   c.TimestampWindowSeconds,
	}
}

// avoid unused import linter when hex not directly referenced
var _ = hex.EncodeToString

// ── AllowedUsers 双源合并（DB + settings_kv）──────────────────────

// loadAllowedUsersFromDB 从 feishu_bot_routing_rules 表读启用中的 open_id 列表。
//
// 返回值：去重后的 open_id 列表（保持 priority 升序）。
// 错误：nil 表 / 列不存在 / 连接失败 都会返回 error，由调用方决定 fallback。
//
// 上下文：3 秒超时（plugin 启动期，不阻塞 alert 路径）。
func loadAllowedUsersFromDB(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	if pool == nil {
		return nil, fmt.Errorf("feishubot: nil db pool")
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
	}
	rows, err := pool.Query(ctx,
		`SELECT open_id FROM feishu_bot_routing_rules
		 WHERE tenant_id = 'default' AND enabled = true
		 ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]struct{})
	out := make([]string, 0, 16)
	for rows.Next() {
		var oid string
		if err := rows.Scan(&oid); err != nil {
			return out, fmt.Errorf("scan: %w", err)
		}
		if oid == "" {
			continue
		}
		if _, ok := seen[oid]; ok {
			continue
		}
		seen[oid] = struct{}{}
		out = append(out, oid)
	}
	return out, rows.Err()
}

// mergeAllowedUsers 合并 DB 表 + settings_kv 兜底列表，去重保持原顺序。
//
// 优先级：DB 行优先（按 priority ASC 排），settings_kv 增量追加未出现的 open_id。
// 设计：settings_kv 仍可作为应急通道，运维可通过 /admin/feishubot/allowed-users
// API 单条更新 DB 行，或临时回退到 settings_kv 加新用户。
//
// 防御：自动跳过空字符串（DB 偶发空列 / settings_kv 残留尾逗号）。
func mergeAllowedUsers(fromDB, fromSettings []string) []string {
	if len(fromDB) == 0 && len(fromSettings) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(fromDB)+len(fromSettings))
	out := make([]string, 0, len(fromDB)+len(fromSettings))
	for _, oid := range fromDB {
		if oid == "" {
			continue
		}
		if _, ok := seen[oid]; ok {
			continue
		}
		seen[oid] = struct{}{}
		out = append(out, oid)
	}
	for _, oid := range fromSettings {
		if oid == "" {
			continue
		}
		if _, ok := seen[oid]; ok {
			continue
		}
		seen[oid] = struct{}{}
		out = append(out, oid)
	}
	return out
}

// ReloadAllowedUsers 仅刷新 AllowedUsers 字段（不重读其他 settings_kv 配置）。
//
// 用途：管理面 POST/DELETE /api/admin/feishubot/routing-rules 后，
// 通过 main.go 调 Plugin.ReloadAllowedUsers() 让 AlertRouter 立即生效。
//
// 与 ReloadConfig 的区别：ReloadConfig 重读全部 settings + DB，代价大；
// ReloadAllowedUsers 仅重读 DB（O(N)），不涉及 settings_kv 调用。
func (p *Plugin) ReloadAllowedUsers() error {
	p.mu.Lock()
	db := p.db
	p.mu.Unlock()

	fromDB, err := loadAllowedUsersFromDB(context.Background(), db)
	if err != nil {
		slog.Warn("feishubot: ReloadAllowedUsers DB query failed; keeping current list",
			"error", err)
		return err
	}

	// 读 settings_kv 兜底
	settingsRaw, _, _ := settings.Global.EffectiveValue(settings.ScopePlatform, "feishu_bot.allowed_users", "")
	var usersRaw string
	if settingsRaw != nil {
		_ = json.Unmarshal(settingsRaw, &usersRaw)
	}
	fromSettings := parseUserList(usersRaw)
	merged := mergeAllowedUsers(fromDB, fromSettings)

	p.mu.Lock()
	p.cfg.AllowedUsers = merged
	p.mu.Unlock()

	slog.Info("feishubot: AllowedUsers reloaded",
		"from_db", len(fromDB), "from_settings", len(fromSettings), "merged", len(merged))
	return nil
}
