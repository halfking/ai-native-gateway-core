package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/redis/go-redis/v9"
)

var activeRuleDetector atomic.Pointer[sanitize.PatternDetector]
var sanitizeRulesStop func()

// loadHardBlockPolicy 装载「硬阻断清单」并在启动日志里落下生效值。
//
// owner 口径（2026-10-20）：默认**不**硬阻断；真要终止回合的规则由清单显式开
// （LLM_GATEWAY_HARD_BLOCK_RULES）。清单错了必须在启动日志里看得见——否则线上
// 「为什么没拦 / 为什么拦了」无从查起，只能翻代码猜。
func loadHardBlockPolicy() {
	allows, err := sanitizerules.LoadHardBlockAllowsFromEnv()
	if err != nil {
		slog.Error("hard block allowlist invalid; keeping default (no egress/output hard block)",
			"error", err, "env", sanitizerules.HardBlockAllowEnv)
		return
	}
	items := make([]string, 0, len(allows))
	for _, a := range allows {
		items = append(items, a.Scope+":"+a.Rule)
	}
	slog.Info("hard block allowlist loaded",
		"env", sanitizerules.HardBlockAllowEnv,
		"allows", items,
		"count", len(items),
	)
}

func managedOutputRuleSource() outputcompliance.ManagedRuleCheck {
	if d := activeRuleDetector.Load(); d != nil && d.RuleManager() != nil {
		return d.CheckManagedOutput
	}
	return nil
}
func installSanitizeRuleManager(h *streaming.ChatHandler, d *sanitize.PatternDetector, db *sql.DB, rc *redis.Client) {
	if db == nil {
		return
	}
	opts := sanitizerules.Options{
		PollInterval: func() time.Duration {
			return time.Duration(settings.GetPlatformInt("sanitize.rules.refresh_interval_seconds", 5)) * time.Second
		},
		AuditEnabled: func() bool { return settings.GetPlatformBool("sanitize.rules.audit_enabled", true) },
		EventLimit:   func() int { return settings.GetPlatformInt("sanitize.rules.event_limit", 10000) },
		OnError:      func(err error) { slog.Warn("sanitize rules background operation failed", "error", err) },
	}
	if rc != nil {
		opts.Notifications = sanitizerules.NewRedisNotifications(rc, "llm-gateway:sanitize-rules:published")
	}
	m, err := newSanitizeRuleManager(d, db, sanitizerules.NewPostgresStore(db), opts)
	if err != nil {
		slog.Error("sanitize rules baseline invalid; version manager unavailable", "error", err)
		return
	}
	// Install the local protective snapshot before any DB I/O. Run keeps trying
	// after a transient import/read failure; existing heads bypass legacy import.
	d.SetRuleManager(m)
	activeRuleDetector.Store(d)
	h.SetRuleSnapshotPin(m.Pin)
	refreshCtx, cancelRefresh := context.WithTimeout(context.Background(), 5*time.Second)
	if err = m.Refresh(refreshCtx); err != nil {
		slog.Warn("sanitize rules using local baseline until database recovers")
	}
	cancelRefresh()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	sanitizeRulesStop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			slog.Warn("sanitize rules shutdown timed out")
		}
	}
}

func newSanitizeRuleManager(d *sanitize.PatternDetector, db *sql.DB, store sanitizerules.Store, opts sanitizerules.Options) (*sanitizerules.Manager, error) {
	local := d.RuleConfig()
	fallback := sanitizerules.Config{Rules: append([]sanitizerules.Rule(nil), local.Rules...)}
	fallback.Rules = append(fallback.Rules, outputcompliance.NewBuiltinChecker().RuleConfig().Rules...)
	opts.BaselineLoader = func(ctx context.Context) (sanitizerules.Config, error) {
		return loadSanitizeRulesBaseline(ctx, db, local)
	}
	return sanitizerules.NewManager(store, fallback, opts)
}

// loadSanitizeRulesBaseline imports only legacy PII policy, never creates schema
// or loads unrelated toxic-word data. Context cancellation bounds startup retry.
func loadSanitizeRulesBaseline(ctx context.Context, db *sql.DB, local sanitizerules.Config) (sanitizerules.Config, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,pattern_name,pattern_type,regex_pattern,enabled FROM pii_patterns ORDER BY id`)
	if err != nil {
		return sanitizerules.Config{}, errors.New("sanitize rules legacy baseline unavailable")
	}
	defer rows.Close()
	cfg := sanitizerules.Config{Rules: append([]sanitizerules.Rule(nil), local.Rules...)}
	for rows.Next() {
		var id int
		var name, typ, pattern string
		var enabled bool
		if err := rows.Scan(&id, &name, &typ, &pattern, &enabled); err != nil {
			return sanitizerules.Config{}, errors.New("sanitize rules legacy baseline unavailable")
		}
		cfg.Rules = append(cfg.Rules, legacyPIIRule(id, name, typ, pattern, enabled))
	}
	if rows.Err() != nil {
		return sanitizerules.Config{}, errors.New("sanitize rules legacy baseline unavailable")
	}
	for _, rule := range outputcompliance.NewBuiltinChecker().RuleConfig().Rules {
		if rule.ID == "compliance.internal_ip" {
			cfg.Rules = append(cfg.Rules, rule)
		}
	}
	return cfg, nil
}

// legacyPIIRule 把 legacy `pii_patterns` 的一行造造成托管规则。
//
// r1038（2026-10-20）：地址族（server_ip / internal_ip）**只记录、不遮蔽**。
//
// 之前所有 legacy 行一律 action=mask，于是 316 播种的 `ip_address` 行
// （正则 `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`，连版本号与环回地址一并命中）
// 会把**每个** IPv4 在脱敏面遮成 `***.***.***.***`，
// 「export https_proxy=http://127.0.0.1:7890」这类可复制执行的命令变成坏命令。
//
// 851 迁移把那一行停用了，但那是**数据**修复：基线快照不可变，851 之前发布的
// 部署拿到的快照里那行仍然是 enabled —— 遮蔽会一直存在直到有人手工重发一版
// 规则。改在这里之后，新发布的基线形状本身就正确；security/sanitize 的
// DetectionScope 另有一道代码级保证（地址族在脱敏面一律放行），两层都不依赖
// 运维记得停用哪一行。
//
// 抽成独立函数是为了让它**可测**：这个形状是「数据修复失效时唯一的兜底」，
// 必须有一道门钉住它，否则没人会注意到它被改回 mask。
func legacyPIIRule(id int, name, typ, pattern string, enabled bool) sanitizerules.Rule {
	// Keep the same canonical type mapping as Checker.RuleConfig.
	switch typ {
	case "bank_card":
		typ = "credit_card"
	case "ip", "ip_address", "ipv6":
		typ = "server_ip"
	case "phone", "id_card", "email", "credit_card", "secret", "account", "name", "internal_ip", "server_ip", "custom":
	default:
		typ = "custom"
	}
	action := "mask"
	if typ == "server_ip" || typ == "internal_ip" {
		action = "log"
	}
	return sanitizerules.Rule{
		ID: "compliance.pii." + strconv.Itoa(id), Name: name, Type: typ,
		Kind: "pattern", Pattern: pattern, Scopes: []string{"output"}, Action: action, Enabled: enabled,
	}
}
func stopSanitizeRules() {
	if sanitizeRulesStop != nil {
		sanitizeRulesStop()
	}
}
