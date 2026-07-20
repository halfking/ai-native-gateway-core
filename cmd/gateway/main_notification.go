// Notification init + DingTalk helpers used by main() at startup.
//
// Extracted from main.go as part of the P0 main.go split refactor.
// See docs/refactor-plans/main-go-split.md for the full plan.
//
// All declarations here are package-private; behaviour is unchanged
// from the original implementation in main.go.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/notification"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// initApprovalNotifier 初始化审批通知器（从 DB 加载路由规则 + 创建 IM 渠道）。
//
// 返回 (notifier, larkChannel, error)：
//   - notifier：审批通知器，可为 nil（渠道未配置）
//   - larkChannel：飞书渠道实例，单独返回以便 feishubot 模块复用（2026-07-09）
//   - error：初始化失败
func initApprovalNotifier(pool *pgxpool.Pool, approvalMgr *sessionaudit.ApprovalManager) (*notification.ApprovalNotifier, *notification.LarkBotChannel, error) {
	if pool == nil || approvalMgr == nil {
		return nil, nil, fmt.Errorf("init approval notifier: nil pool or approval manager")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. 加载路由规则
	routingTable := notification.NewEmptyRoutingTable()
	loader := notification.NewPgxRoutingLoader(pool)
	if err := routingTable.LoadFromDB(ctx, loader); err != nil {
		return nil, nil, fmt.Errorf("load routing rules: %w", err)
	}

	// 2. 创建渠道实例（从环境变量配置）
	channels := make(map[notification.ChannelType]notification.NotificationChannel)
	var larkCh *notification.LarkBotChannel // 2026-07-09: 暴露给 feishubot 模块复用

	// 飞书渠道
	if larkAppID := os.Getenv("LARK_APP_ID"); larkAppID != "" {
		larkAppSecret := os.Getenv("LARK_APP_SECRET")
		larkCfg := notification.LarkBotConfig{
			AppID:     larkAppID,
			AppSecret: larkAppSecret,
		}
		larkCh = notification.NewLarkBotChannel(larkCfg)
		channels[notification.ChannelLark] = larkCh
		slog.Info("lark channel initialized", "app_id", larkAppID)
	}

	// 钉钉渠道
	// 优先读取 dingtalk_bot.* 模块设置（使模块开关真正生效），回退到环境变量以兼容旧部署。
	if dingCfg, ok := dingTalkConfigFromSettings(); ok {
		channels[notification.ChannelDingTalk] = notification.NewDingTalkChannel(dingCfg)
		slog.Info("dingtalk channel initialized from module settings",
			"webhook", dingCfg.WebhookURL != "", "app_mode", dingCfg.AppKey != "")
	} else if dingWebhook := os.Getenv("DINGTALK_WEBHOOK_URL"); dingWebhook != "" {
		dingCfg := notification.DingTalkConfig{
			WebhookURL: dingWebhook,
			SignSecret: os.Getenv("DINGTALK_SIGN_SECRET"),
		}
		channels[notification.ChannelDingTalk] = notification.NewDingTalkChannel(dingCfg)
		slog.Info("dingtalk channel initialized from env webhook")
	} else if dingAppKey := os.Getenv("DINGTALK_APP_KEY"); dingAppKey != "" {
		dingAppSecret := os.Getenv("DINGTALK_APP_SECRET")
		dingCfg := notification.DingTalkConfig{
			AppKey:    dingAppKey,
			AppSecret: dingAppSecret,
		}
		channels[notification.ChannelDingTalk] = notification.NewDingTalkChannel(dingCfg)
		slog.Info("dingtalk channel initialized", "app_key", dingAppKey)
	}

	// 企业微信渠道
	if wechatCorpID := os.Getenv("WECHAT_CORP_ID"); wechatCorpID != "" {
		wechatCorpSecret := os.Getenv("WECHAT_CORP_SECRET")
		wechatCfg := notification.WeChatConfig{
			CorpID:     wechatCorpID,
			CorpSecret: wechatCorpSecret,
		}
		channels[notification.ChannelWeChat] = notification.NewWeChatChannel(wechatCfg)
		slog.Info("wechat channel initialized", "corp_id", wechatCorpID)
	}

	// 如果没有配置任何渠道，返回 nil（不报错，只是不发通知）
	if len(channels) == 0 {
		slog.Warn("no notification channels configured, approval notifications disabled")
		return nil, nil, nil
	}

	// 3. 构造 ApprovalNotifier
	notifier, err := notification.NewApprovalNotifier(notification.NotifierConfig{
		Channels:    channels,
		Routing:     routingTable,
		ApprovalMgr: approvalMgr,
		Timeout:     30 * time.Second,
	})
	if err != nil {
		return nil, larkCh, fmt.Errorf("create approval notifier: %w", err)
	}

	return notifier, larkCh, nil
}

// dingTalkConfigFromSettings 从 dingtalk_bot.* 模块设置构造钉钉渠道配置。
//
// 仅当模块启用（dingtalk_bot.enabled=true）且至少配置了 Webhook 或 App 凭证时返回
// (config, true)；否则返回 (零值, false)。读取失败时安全回退到环境变量，保证旧部署兼容。
//
// 这样钉钉机器人模块的「开关 + 配置」在管理后台真正生效，而非仅依赖环境变量启动参数。
func dingTalkConfigFromSettings() (notification.DingTalkConfig, bool) {
	if settings.Global == nil {
		return notification.DingTalkConfig{}, false
	}
	enabled := readBoolSettingValue("dingtalk_bot.enabled")
	if !enabled {
		return notification.DingTalkConfig{}, false
	}

	get := func(key string) string {
		sp := settings.Global.Spec(key)
		if sp == nil {
			return ""
		}
		raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
		if err != nil || len(raw) == 0 {
			return ""
		}
		return strings.Trim(string(raw), `"`)
	}

	cfg := notification.DingTalkConfig{
		WebhookURL: get("dingtalk_bot.webhook_url"),
		SignSecret: get("dingtalk_bot.sign_secret"),
		AppKey:     get("dingtalk_bot.app_key"),
		AppSecret:  get("dingtalk_bot.app_secret"),
		AgentID:    get("dingtalk_bot.agent_id"),
		BaseURL:    get("dingtalk_bot.base_url"),
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://oapi.dingtalk.com"
	}

	// 回退到环境变量（兼容未迁移到模块设置的旧部署）
	if cfg.WebhookURL == "" && cfg.AppKey == "" {
		if envWebhook := os.Getenv("DINGTALK_WEBHOOK_URL"); envWebhook != "" {
			cfg.WebhookURL = envWebhook
		}
		if cfg.SignSecret == "" {
			cfg.SignSecret = os.Getenv("DINGTALK_SIGN_SECRET")
		}
		if cfg.AppKey == "" {
			cfg.AppKey = os.Getenv("DINGTALK_APP_KEY")
		}
		if cfg.AppSecret == "" {
			cfg.AppSecret = os.Getenv("DINGTALK_APP_SECRET")
		}
		if cfg.AgentID == "" {
			cfg.AgentID = os.Getenv("DINGTALK_AGENT_ID")
		}
	}

	if cfg.WebhookURL == "" && cfg.AppKey == "" {
		return notification.DingTalkConfig{}, false
	}
	return cfg, true
}

func hasCompleteDingTalkAppConfig(cfg notification.DingTalkConfig) bool {
	return cfg.AppKey != "" && cfg.AppSecret != "" && cfg.AgentID != ""
}

func dingTalkAllowedUsersFromSettings() []string {
	sp := settings.Global.Spec("dingtalk_bot.allowed_users")
	if sp == nil {
		return nil
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, sp.Key, "")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var users []string
	for _, userID := range strings.Split(strings.Trim(string(raw), `"`), ",") {
		if userID = strings.TrimSpace(userID); userID != "" {
			users = append(users, userID)
		}
	}
	return users
}

func dingTalkCallbackSecretFromSettings() string {
	if !readBoolSettingValue("dingtalk_bot.verify_signature") {
		return ""
	}
	cfg, ok := dingTalkConfigFromSettings()
	if !ok {
		return ""
	}
	if cfg.SignSecret != "" {
		return cfg.SignSecret
	}
	return cfg.AppSecret
}

func dingTalkUserIsAllowed(userID string) bool {
	users := dingTalkAllowedUsersFromSettings()
	if len(users) == 0 {
		return true
	}
	for _, allowedUserID := range users {
		if userID == allowedUserID {
			return true
		}
	}
	return false
}
