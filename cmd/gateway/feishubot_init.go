// File: cmd/gateway/feishubot_init.go
//
// 2026-07-09: 飞书机器人模块启动期装配。
//
// 目标：
//   - 把 domains/feishubot.Plugin 与现有 notification.LarkBotChannel 串联
//   - 订阅 eventbus 上的 sessionaudit 审批事件，转发到飞书
//   - 注册 HTTP 回调路由（签名校验 + 白名单 + 命令路由）
//   - 不破坏现有 initApprovalNotifier 的行为；模块未启用时全部降级为 no-op
//
// 设计：单一装配函数 InitFeishubotPlugin，所有副作用集中在此文件。
// 调用方仅需：
//
//	if plugin, err := InitFeishubotPlugin(bus, larkChannel, approvalMgr); err != nil {
//	    slog.Warn("feishubot init failed", "error", err)
//	}
//
// 与 cmd/gateway/main.go 的集成点：
//   1. 在 initApprovalNotifier 之后调用（复用同一个 LarkChannel 实例）
//   2. eventBus 由 chatHandler.SetSessionAuditHook(auditHook) 时创建
//
// 失败语义：装配失败仅记日志，不影响主进程启动（feishubot 是 best-effort 通知）。

package main

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/feishubot"
	"github.com/kaixuan/llm-gateway-go/domains/notification"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/eventbus"
)

// InitFeishubotPlugin 装配飞书机器人模块。
//
// 参数：
//   - bus：session audit 的事件总线（订阅 EventTypeApprovalNeeded/Decided）
//   - larkChannel：飞书通知渠道（来自 initApprovalNotifier，可为 nil）
//   - approvalMgr：审批管理器（命令路由审批操作时使用，可为 nil）
//   - mux：HTTP 路由（注册 /api/webhooks/feishu/callback）
//
// 返回：
//   - *feishubot.Plugin 实例（启动期已 ReloadConfig）
//   - error：装配失败（如 settings 缺失）；主进程应记日志但不退出
//
// 行为：
//   - feishu_bot.enabled=false → 全部降级为 no-op，立即返回
//   - larkChannel==nil → 装配失败（无发送通道）
//   - mux==nil → 跳过 HTTP 路由注册（仅事件订阅生效）
func InitFeishubotPlugin(
	bus *eventbus.MemoryBus,
	larkChannel *notification.LarkBotChannel,
	approvalMgr *sessionaudit.ApprovalManager,
	mux *http.ServeMux,
) (*feishubot.Plugin, error) {
	cfg, err := feishubot.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("feishubot: load config: %w", err)
	}
	if !cfg.Enabled {
		slog.Info("feishubot: module disabled, skipping init")
		return nil, nil
	}

	if larkChannel == nil {
		return nil, fmt.Errorf("feishubot: lark channel not initialized (LARK_APP_ID env var missing?)")
	}

	adapter := feishubot.NewLarkChannelAdapter(larkChannel)
	plugin := feishubot.NewPlugin(adapter)
	if err := plugin.Start(nil); err != nil {
		return nil, fmt.Errorf("feishubot: start plugin: %w", err)
	}

	// 1. AlertRouter：订阅审批事件，转发为飞书告警
	if bus != nil {
		router := feishubot.NewAlertRouter(adapter)
		router.Configure(cfg)
		bus.Subscribe(sessionaudit.EventTypeApprovalNeeded, router.OnApprovalNeeded)
		bus.Subscribe(sessionaudit.EventTypeApprovalDecided, router.OnApprovalDecided)
		slog.Info("feishubot: alert router subscribed",
			"events", []string{sessionaudit.EventTypeApprovalNeeded, sessionaudit.EventTypeApprovalDecided})
	}

	// 2. CallbackHandler + CommandRouter：HTTP 入口处理命令
	if mux != nil {
		whitelist := &feishubot.CfgWhitelist{Cfg: &cfg}
		cmdRouter := feishubot.NewCommandRouter(whitelist)
		// 注册内置命令（handler 主体是骨架，生产环境应注入真实数据源）
		cmdRouter.Register("help", feishubot.HelpCommand)
		cmdRouter.Register("status", feishubot.StatusCommand)
		cmdRouter.Register("test", feishubot.TestCommand)
		// "audit" / "stats" 留给后续 PR：需要外部数据源

		handler := feishubot.NewCallbackHandler(plugin, cmdRouter, adapter)
		mux.Handle("/api/webhooks/feishu/callback", handler.AsHTTPHandler())
		slog.Info("feishubot: callback route registered at /api/webhooks/feishu/callback")
	}

	slog.Info("feishubot: plugin initialized",
		"webhook_set", cfg.WebhookURL != "",
		"verify_token_set", cfg.VerifyToken != "",
		"encrypt_key_set", cfg.EncryptKey != "",
		"allowed_users", len(cfg.AllowedUsers),
		"notify_on_alert", cfg.NotifyOnAlert,
		"notify_on_approval", cfg.NotifyOnApproval,
		"commands_enabled", cfg.CommandsEnabled,
		"signature_required", cfg.SignatureRequired,
	)
	return plugin, nil
}
