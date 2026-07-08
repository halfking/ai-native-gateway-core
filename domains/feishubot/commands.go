package feishubot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Command 飞书卡片回调解析出的命令。
type Command struct {
	Action   string         // "status" / "help" / "stats" / "audit" / "test"
	UserID   string         // OpenID
	UserName string         // 用户名（可选）
	TenantID string         // 租户 ID（可选，从 metadata 取）
	Args     []string       // 命令参数（可选）
	Raw      map[string]any // 原始回调 Value（透传）
}

// CommandHandler 处理命令并返回回复卡片。
type CommandHandler func(ctx context.Context, cmd Command) (*Card, error)

// CommandRouter 把命令分发给注册的处理器。
//
// 安全：所有处理器在执行前都会经过白名单检查（除非 commands.admin_only=false）。
type CommandRouter struct {
	mu        sync.RWMutex
	handlers  map[string]CommandHandler
	whitelist WhitelistChecker
}

// WhitelistChecker 是白名单检查的最小接口。
//
// 实际由 feishubot.Plugin 的 Config.IsUserAllowed 提供。
type WhitelistChecker interface {
	IsAllowed(openID string) bool
}

// NewCommandRouter 构造路由器。
func NewCommandRouter(wl WhitelistChecker) *CommandRouter {
	return &CommandRouter{
		handlers:  make(map[string]CommandHandler),
		whitelist: wl,
	}
}

// Register 注册一个命令处理器。
func (r *CommandRouter) Register(action string, h CommandHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[action] = h
}

// Handle 处理一个命令。
//
// 返回值：
//   - card：回复卡片（nil 表示不回复，如未通过白名单）
//   - allowed：false 表示白名单拒绝（调用方应静默 200 OK，不推送任何消息，避免泄漏命令存在）
//   - err：处理错误
func (r *CommandRouter) Handle(ctx context.Context, cmd Command, adminOnly bool) (card *Card, allowed bool, err error) {
	if cmd.Action == "" {
		return nil, false, fmt.Errorf("feishubot: empty command action")
	}

	// 白名单检查
	if adminOnly && r.whitelist != nil && !r.whitelist.IsAllowed(cmd.UserID) {
		slog.Warn("feishubot: command rejected by whitelist",
			"action", cmd.Action, "user", cmd.UserID)
		return nil, false, nil
	}

	r.mu.RLock()
	h, ok := r.handlers[cmd.Action]
	r.mu.RUnlock()
	if !ok {
		return errorCard(fmt.Sprintf("未知命令: /%s", cmd.Action),
			"输入 /help 查看支持的命令列表"), true, nil
	}

	reply, err := h(ctx, cmd)
	if err != nil {
		return errorCard(fmt.Sprintf("命令执行失败: /%s", cmd.Action), err.Error()), true, nil
	}
	return reply, true, nil
}

// errorCard 构造错误回复卡片。
func errorCard(title, content string) *Card {
	return &Card{
		Header:   CardHeader{Title: title, Template: "red"},
		Elements: []CardElement{{Type: "text", Text: content}},
	}
}

// ── 内置命令实现 ────────────────────────────────────────────────────

// HelpCommand /help：列出可用命令。
func HelpCommand(_ context.Context, _ Command) (*Card, error) {
	return &Card{
		Header: CardHeader{Title: "📖 可用命令", Template: "blue"},
		Elements: []CardElement{
			{Type: "text", Text: "**系统状态与运维命令**"},
			{Type: "field", Fields: []CardField{
				{Key: "/status", Value: "查看系统整体运行状态", Short: true},
				{Key: "/stats", Value: "查看最近 1 小时请求统计", Short: true},
				{Key: "/audit", Value: "查询最近的审计事件（参数: tenant=<id>）", Short: false},
				{Key: "/test", Value: "测试机器人连通性", Short: true},
				{Key: "/help", Value: "显示本帮助", Short: true},
			}},
			{Type: "note", Text: fmt.Sprintf("生成时间: %s", time.Now().Format("2006-01-02 15:04:05"))},
		},
	}, nil
}

// StatusCommand /status：返回系统状态（注入检测器）。
//
// 实际数据由外部注入（Plugin.RegisterStatusProvider）以保持包低耦合。
func StatusCommand(_ context.Context, _ Command) (*Card, error) {
	// 这里只返回骨架，具体内容由 StatusProvider 提供
	return &Card{
		Header: CardHeader{Title: "📊 系统状态", Template: "green"},
		Elements: []CardElement{
			{Type: "text", Text: "系统运行中。详细指标请查询管理后台。"},
		},
	}, nil
}

// TestCommand /test：发送连通性测试。
func TestCommand(_ context.Context, cmd Command) (*Card, error) {
	user := cmd.UserName
	if user == "" {
		user = cmd.UserID
	}
	return &Card{
		Header: CardHeader{Title: "✅ 连通性测试成功", Template: "green"},
		Elements: []CardElement{
			{Type: "text", Text: fmt.Sprintf("你好 **%s**，飞书机器人已成功响应。", user)},
			{Type: "note", Text: fmt.Sprintf("OpenID: %s · %s", cmd.UserID, time.Now().Format("2006-01-02 15:04:05"))},
		},
	}, nil
}

// ParseCommandAction 从回调 Value 中解析 action 字符串。
//
// 飞书卡片按钮回调常见结构：
//
//	value: { action: "status", reason: "..." }
//
// 退化：value[0] 字符串本身也是 action
func ParseCommandAction(value map[string]any) string {
	if v, ok := value["action"].(string); ok {
		return strings.TrimPrefix(v, "/")
	}
	if v, ok := value["command"].(string); ok {
		return strings.TrimPrefix(v, "/")
	}
	return ""
}

// 把多行 string 按行拆分用于 note。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
