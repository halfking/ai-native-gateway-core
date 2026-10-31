package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
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
	store := sanitizerules.NewPostgresStore(db)
	m, err := newSanitizeRuleManager(d, db, store, opts)
	if err != nil {
		slog.Error("sanitize rules baseline invalid; version manager unavailable", "error", err)
		return
	}
	// 2026-10-29 拦截持久化轮：审核记录 / 放行表 / 硬阻断清单管理面状态落库
	// （迁移 917）。启动即装载放行与管理面清单；装载失败只告警（放行缺失的
	// 后果是误报复发，比网关起不来轻）。
	sanitizerules.InstallReviewPersistence(store)
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
		cfg.Rules = append(cfg.Rules, sanitizerules.LegacyPIIRule(id, name, typ, pattern, enabled))
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

// legacyPIIRule 已下沉到 domains/sanitizerules.LegacyPIIRule（r1038b，2026-10-27）。
//
// 下沉理由：这段映射原先住在 package main 里，**测试无法直接调用** ⇒ 任何
// 「验证生产真实规则集」的判据都只能手抄一份映射，抄件会与生产漂移。
// 下沉后生产侧与判据侧调用**同一个函数**，判据锚在事实源上。
func stopSanitizeRules() {
	if sanitizeRulesStop != nil {
		sanitizeRulesStop()
	}
}
