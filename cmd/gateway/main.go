// Command gateway is the LLM Gateway Go data-plane entry point.
//
// Usage:
//
//	LLM_GATEWAY_LISTEN=:8781 LLM_GATEWAY_API_KEY=... go run ./cmd/gateway
//
// Configuration (priority: env vars > YAML file > defaults):
//   - Environment variables (see each var below)
//   - YAML config file (LLM_GATEWAY_CONFIG_FILE or ./config.yml)
//
// Hot-reload: POST /admin/config/reload to reload YAML config at runtime.
// Only YAML-sourced values are reloaded; env vars keep their process-level values.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/api"
	"github.com/kaixuan/llm-gateway-go/apihub"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/bg/systemmonitor"
	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/disguise"
	"github.com/kaixuan/llm-gateway-go/domains/analysis"                            //nolint:depguard // M3 embedding shadow adapter
	"github.com/kaixuan/llm-gateway-go/domains/analysis/bus"                        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/approval"                            //nolint:depguard // D1: approval config management
	"github.com/kaixuan/llm-gateway-go/domains/assets"                              //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/attachments"                         //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/authentication"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credential"                          //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"                       //nolint:depguard // 数据库降级模块
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"                         //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"                   //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	sessionaudithook "github.com/kaixuan/llm-gateway-go/domains/hooks/sessionaudit" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/integration"                         //nolint:depguard // clientprofile worker wiring
	"github.com/kaixuan/llm-gateway-go/domains/notification"                        //nolint:depguard // 审批通知器
	"github.com/kaixuan/llm-gateway-go/domains/routeincident"                       //nolint:depguard // 2026-07-13 route incident diagnosis (Phase 1)
	"github.com/kaixuan/llm-gateway-go/domains/routingstate"
	"github.com/kaixuan/llm-gateway-go/domains/session"      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/stats"
	"github.com/kaixuan/llm-gateway-go/domains/stats/boardcache"
	streaming "github.com/kaixuan/llm-gateway-go/domains/streaming" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation"      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"      //nolint:depguard // URSM v2 wiring (T20)
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/persist"     //nolint:depguard // URSM v2 persist writer
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/fault"
	"github.com/kaixuan/llm-gateway-go/hotconfig"
	"github.com/kaixuan/llm-gateway-go/internal/attachmentmirror"
	"github.com/kaixuan/llm-gateway-go/internal/centeragent"
	"github.com/kaixuan/llm-gateway-go/internal/collector"
	"github.com/kaixuan/llm-gateway-go/internal/handlers"
	"github.com/kaixuan/llm-gateway-go/internal/ir" //nolint:depguard // 诊断组件：语义分析器
	"github.com/kaixuan/llm-gateway-go/internal/logging"
	"github.com/kaixuan/llm-gateway-go/internal/modelpolicy"
	"github.com/kaixuan/llm-gateway-go/internal/observability"
	"github.com/kaixuan/llm-gateway-go/internal/quality"
	"github.com/kaixuan/llm-gateway-go/internal/sessionv2mirror"
	gwtrace "github.com/kaixuan/llm-gateway-go/internal/trace"
	"github.com/kaixuan/llm-gateway-go/licensing"
	"github.com/kaixuan/llm-gateway-go/maas"
	"github.com/kaixuan/llm-gateway-go/metatools"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/registry"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/kaixuan/llm-gateway-go/security/armor"
	"github.com/kaixuan/llm-gateway-go/security/ipblocklist"
	"github.com/kaixuan/llm-gateway-go/security/sensitive"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/kaixuan/llm-gateway-go/tenantops"
	upstream "github.com/kaixuan/llm-gateway-go/upstream"
	"github.com/kaixuan/llm-gateway-go/vibecoding"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

// positiveDurationEnv, positiveIntEnv, envBoolOff, liveStreamCachedDurationsFromEnv,
// sessionAuditApprovalTimeoutFromEnv, parseModelFallbackEnv and useNewProbeMode
// were moved to main_helpers.go as part of the P0 main.go split refactor.
// See docs/refactor-plans/main-go-split.md.

// ── 模块装配后置状态 ──────────────────────────────────────────
// 2026-07-09: 这些变量由 initApprovalNotifier 写入，由 router 注册阶段读取
// 以完成 feishubot 模块的 late-binding。包级可见避免传递整条 init 链。
var (
	gAuditBus    *eventbus.MemoryBus
	gLarkCh      *notification.LarkBotChannel
	gApprovalMgr *sessionaudit.ApprovalManager
	// gRestrictedMode 在 license 校验失败时设为 true；router 注册阶段
	// 据此决定是否启用 licensing.RestrictedModeMiddleware。
	gRestrictedMode bool
)

func main() {
	// migrate subcommand: connect DB, run migrations, exit.
	// Used by launcher to run forward-compatible migrations while old
	// version still serves traffic (zero-downtime upgrade).
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		os.Exit(runMigrate(config.Load().DatabaseURL))
	}

	processStartedAt := time.Now()
	// Round 39 (2026-06-16) — initialize OTel tracer.
	// Default-disabled; activates only when OTEL_EXPORTER_OTLP_ENDPOINT
	// is set. The shutdown function flushes pending spans before
	// process exit (called via os.Exit defer in the bg block).
	tracerShutdown := observability.InitTracer("llm-gateway-go", "1.0.0")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		tracerShutdown(ctx)
	}()

	// Package-level singletons declared early so the executor wiring
	// (lines ~196) and the shutdown sequence (lines ~500) can both
	// reference them. Actual construction happens in the bg block
	// after dbConn is initialized.
	var peakCollector *bg.ConcurrencyPeakCollector
	var weeklyPeakRollup *bg.WeeklyPeakRollup
	var statsMinuteAccumulator *stats.MinuteAccumulator
	var statsBoardCache *boardcache.Service
	var statsMinuteRollup *bg.StatsMinuteRollup
	var slotSuggester *bg.SlotSuggester
	var autoIndexRefresher *bg.AutoIndexRefresher
	// memorySvc holds the legacy memora concrete client/sink behind the
	// live memory.Reader / memory.Writer interfaces used by gateway runtime.
	var memorySvc *legacyMemoryServices
	var rawDataLogger *logging.AsyncRawDataLogger
	var anomalyReporter *logging.LockFreeAnomalyReporter

	// 2026-07-20: ringBuffer holds failed request_log INSERTs in memory
	// for online dump/replay via /internal/telemetry/fallback-buffer/*.
	// Declared at top-level so the HTTP handler (registered later in main)
	// can reach it; assigned in the dbConn != nil block below.
	var ringBuffer *dbdegradation.RingBuffer

	// ── Logging ───────────────────────────────────────────────────────────
	cfg := config.Load()

	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	// File-based rotation. When LLM_GATEWAY_LOG_FILE is set, slog's
	// default handler is replaced with one that writes JSON to the
	// rotated log file (and mirrors to stderr for safety). When the
	// env var is empty, logging.Init is a no-op and slog stays on
	// stderr. See internal/logging for the rotation policy and
	// config/config.go for the operator-spec defaults.
	logging.SetLevel(level)
	logCfg := logging.DefaultConfig()
	logCfg.File = cfg.LogFile
	logCfg.MaxSizeMB = cfg.LogMaxSizeMB
	logCfg.MaxBackups = cfg.LogMaxBackups
	logCfg.MaxAgeDays = cfg.LogMaxAgeDays
	logCfg.Compress = cfg.LogCompress
	if _, err := logging.Init(logCfg, level); err != nil {
		// Fall back to stderr so the service still starts and the
		// operator sees the misconfiguration in the log.
		fmt.Fprintf(os.Stderr, "logging: file rotation disabled: %v\n", err)
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: level,
		})))
	}

	// ── Optional YAML config file ─────────────────────────────────────────
	configFile := os.Getenv("LLM_GATEWAY_CONFIG_FILE")
	if configFile == "" {
		if _, err := os.Stat("./config.yml"); err == nil {
			configFile = "./config.yml"
		}
	}
	if configFile != "" {
		if err := cfg.LoadFile(configFile); err != nil {
			slog.Warn("config: failed to load YAML file, using env-only", "path", configFile, "error", err)
		} else {
			slog.Info("config: loaded YAML file", "path", configFile)
		}
	}

	// V2-P8: 主读切换标记 — only the banner, the routing switch itself is
	// a follow-up PR so this commit stays a pure observable change.
	v2Primary := os.Getenv("SESSIONS_V2_PRIMARY_READ") == "true"
	if v2Primary {
		slog.Info("V2 PRIMARY READ ENABLED — V1 read-only compatibility layer")
	} else {
		slog.Info("V2 SHADOW WRITE — V1 remains primary (read + write)")
	}

	// ── Auth fail-closed guard (rule 20 §8) ───────────────────────────────
	// In production, the three auth secrets must be set; otherwise the
	// process refuses to start rather than running fail-open. dev/local
	// keeps fail-open but logs a prominent warning.
	if cfg.IsProduction() {
		if missing := cfg.ValidateAuthSecrets(); missing != "" {
			panic(fmt.Sprintf("auth fail-closed (LLM_GATEWAY_ENV=production): missing required secret %s", missing))
		}
		slog.Info("auth: production fail-closed mode active, all auth secrets present")
	} else if missing := cfg.ValidateAuthSecrets(); missing != "" {
		slog.Warn("auth: INSECURE fail-open mode — missing secret in non-production env", "missing", missing,
			"hint", "set LLM_GATEWAY_ENV=production to enforce fail-closed")
	}

	// Rule 20 §3: ops token SHOULD carry the ops_ prefix (soft check, non-blocking)
	if cfg.AdminAPIKey != "" && !strings.HasPrefix(cfg.AdminAPIKey, "ops_") {
		slog.Warn("auth: ops token should use ops_ prefix per rule 20 §3",
			"hint", "rotate to ops_-prefixed value at next maintenance window")
	}

	cfgStore := config.NewStore(cfg)
	streaming.SetConfigStore(cfgStore)
	slog.Info("gateway starting", "listen", cfg.Listen, "log_level", cfg.LogLevel)

	// ── Dependencies ──────────────────────────────────────────────────────
	dbConn, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		slog.Warn("postgres disabled", "error", err)
		dbConn = nil // Prevent using closed connection pool
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("main: panic during startup", "panic", fmt.Sprintf("%v", r), "stack", string(debug.Stack()))
			if dbConn != nil {
				dbConn.Close()
			}
			os.Exit(1)
		}
		if dbConn != nil {
			dbConn.Close()
		}
	}()

	// ── Columnar invariant check (Phase 23 / 03, 2026-07-02) ────────
	// Run once at startup. Surfaces drift between expected and actual
	// access method. The event trigger + daily cron handle repair; we
	// never block startup on this.
	if dbConn != nil {
		_, _ = bg.ColumnarInvariantCheck(context.Background(), dbConn.Pool())
	}

	// ── License enforcement (2026-07-12) ─────────────────────────────
	// Verify license at startup. Failure enters restricted mode (warn only).
	//
	// LICENSE_MODE controls offline vs online verification:
	//   - "offline" (M1): runs OfflineVerificationDaemon (6h cron), no heartbeat
	//   - "online" (default): runs StartTokenRefreshDaemon for token refresh

	licenseMode := os.Getenv("LICENSE_MODE")
	if licenseMode == "" {
		licenseMode = "online"
	}

	if licenseMode == "offline" {
		// M1 offline mode: verify license.dat locally, no network calls
		slog.Info("license mode: offline (M1)", "interval", "6h", "heartbeat", "disabled")

		// Initial verification at startup
		if err := licensing.EnforceAtStartup(
			"/var/lib/kx-gateway/license.dat",
			"/var/lib/kx-gateway/server.pub",
			"/var/lib/kx-gateway",
		); err != nil {
			slog.Warn("license verification failed, entering community mode", "error", err)
			if enterErr := licensing.EnterCommunityMode(); enterErr != nil {
				slog.Error("failed to enter community mode", "error", enterErr)
			}
		} else {
			slog.Info("license verification successful")
		}

		// Start offline verification daemon (6h interval)
		go licensing.OfflineVerificationDaemon(
			context.Background(),
			licensing.DefaultOfflineVerificationInterval,
			"/var/lib/kx-gateway/license.dat",
			"/var/lib/kx-gateway/server.pub",
			"/var/lib/kx-gateway",
		)
		slog.Info("offline verification daemon started", "interval", "6h")
	} else {
		// Online mode: verify and start token refresh daemon
		if err := licensing.EnforceAtStartup(
			"/var/lib/kx-gateway/license.dat",
			"/var/lib/kx-gateway/server.pub",
			"/var/lib/kx-gateway",
		); err != nil {
			slog.Warn("license enforcement failed, entering restricted mode", "error", err)
			// 2026-07-21: 设置全局受限模式标志，router 注册阶段会据此
			// 启用 licensing.RestrictedModeMiddleware 阻止非白名单请求。
			gRestrictedMode = true
		} else {
			slog.Info("license verification successful")
		}

		// Token refresh daemon (online mode only)
		if masterURL := os.Getenv("LICENSE_AUTHORITY_URL"); masterURL != "" {
			homeDir, _ := os.UserHomeDir()
			tokenDir := filepath.Join(homeDir, ".kx-gateway")
			_ = tokenDir // 保留以兼容后续逻辑

			// 2026-07-21: 启用 token 自动续期守护进程。
			// - 首次延迟 1 小时（避免启动风暴）
			// - 之后每 6 天执行一次刷新
			// - 失败不中断，自动按 BackoffConfig 退避
			// - 守护进程使用独立 context，与主进程生命周期解耦
			//   （关闭时由 daemon 内部的 ticker.Stop 自动回收）。
			refreshTokenPath := filepath.Join(tokenDir, "refresh_token")
			instanceTokenPath := filepath.Join(tokenDir, "instance_token")
			daemonCtx, daemonCancel := context.WithCancel(context.Background())
			defer daemonCancel() // 进程退出时通知守护进程退出
			go licensing.StartTokenRefreshDaemon(
				daemonCtx,
				masterURL,
				refreshTokenPath,
				instanceTokenPath,
				6*24*time.Hour, // 518400 秒 = 6 天
			)
			slog.Info("token refresh daemon started",
				"master_url", masterURL,
				"interval", "6d",
				"initial_delay", "1h")
		} else {
			slog.Info("token refresh daemon disabled (LICENSE_AUTHORITY_URL not set)")
		}
	}

	if dbConn != nil && dbConn.Enabled() {
		collector.MaybeStart(context.Background(), collector.StartupConfig{
			Pool:         dbConn.Pool(),
			DataDir:      "/var/lib/kx-gateway",
			AuthorityURL: os.Getenv("LICENSE_AUTHORITY_URL"),
			Version:      Version(),
			StartTime:    processStartedAt,
		})
		centeragent.MaybeStart(context.Background(), centeragent.StartupConfig{
			Pool:         dbConn.Pool(),
			Version:      Version(),
			BuildSeq:     BuildSeqInt(),
			StartTime:    processStartedAt,
			Region:       os.Getenv("OPS_NODE_REGION"),
			DataDir:      strings.TrimSpace(os.Getenv("OPS_DATA_DIR")),
			AuthorityURL: os.Getenv("LICENSE_AUTHORITY_URL"),
		})
		go center.MonitorInstances(context.Background(), center.NewPgxStore(dbConn.Pool()), 30*time.Second)
	}

	// Check if community mode is active
	if licensing.IsCommunityMode() {
		slog.Warn("running in community mode",
			"max_tenants", licensing.MaxCommunityTenants,
			"features", "basic_api_only",
			"restrictions", licensing.GetCommunityModeRestrictions()["restrictions"])
		// 2026-07-21: 社区模式的租户数量限制通过 admin API 主动检查
		// （创建租户时校验），不在 middleware 层强制（middleware 难以
		// 高效查询租户总数）。完整方案见 docs/TODO_COMMUNITY_MODE.md。
		// 短期先暴露状态供 ops 仪表盘告警。
		_ = licensing.GetCommunityModeRestrictions
	}

	cm := credential.NewManager()
	lim := credential.NewLimiter()

	matrixPath := transformation.DefaultMatrixPath()
	matrix := transformation.New(matrixPath)

	resolver := resolve.NewResolver("", 120*time.Second)

	auditSink := audit.NewMultiSink(
		&audit.LogSink{},
		audit.NewJSONSink(10000),
	)

	// OPT-4 (2026-07-12): use 0 internal retries so retry decisions are
	// owned entirely by the routing executor (which can switch credentials,
	// skip client-bug kinds, and update health state). The previous default
	// of 2 inner retries × N candidate retries could amplify a single
	// failure to 6×N upstream dials.
	upClient := upstream.NewWithRetries(0)
	slog.Info("upstream proxy resolver initialised",
		"proxy_configured", upClient.ProxyStatus()["proxy"] != "",
		"domestic_hosts", len(upClient.ProxyStatus()["domestic"].([]string)),
	)

	pools := pool.NewPoolManager(upClient.Proxy().ProxyFunc())

	chatHandler := streaming.NewChatHandler(cm, lim, matrix, pools, resolver, auditSink)
	if len(cfg.SessionIDBodyKeys) > 0 {
		streaming.SetSessionIDBodyKeys(cfg.SessionIDBodyKeys)
	}

	// Health handler with database and Redis status checking (2026-07-08)
	// Pass db and redis connections for health checks (will be updated with redis later)
	var dbPinger interface{ Ping(context.Context) error }
	var redisPinger interface{ Ping(context.Context) error }
	if dbConn != nil && dbConn.Pool() != nil {
		dbPinger = dbConn.Pool()
	}
	healthHandler := streaming.NewHealthHandler(cm, lim, upClient.Proxy(), dbPinger, redisPinger)

	modelsHandler := streaming.NewModelsHandler()
	messagesHandler := streaming.NewMessagesHandler(chatHandler)
	responsesHandler := streaming.NewResponsesHandler(chatHandler)
	// EmbeddingsHandler 在 providerClient 就绪后初始化（见 ~SetAuth 区）。
	var embeddingsHandler *streaming.EmbeddingsHandler

	// ── Tenant model policy (Round 48, 2026-06-21) ─────────────────
	// Single Checkerr singleton shared by streaming.ChatHandler (hot
	// path enforcement) and admin.Handler (write-path Invalidate).
	// Pre-warm at startup so the first request doesn't pay the
	// singleflight reload cost.
	var modelPolicy *modelpolicy.Checker
	if dbConn != nil && dbConn.Enabled() {
		modelPolicy = modelpolicy.New(dbConn.Pool())
		chatHandler.SetModelPolicy(modelPolicy)
		warmupCtx, warmupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := modelPolicy.ReloadAll(warmupCtx); err != nil {
			slog.Warn("model_policy: startup pre-warm failed (will lazy-load)",
				"error", err)
		} else {
			slog.Info("model_policy: pre-warm complete")
		}
		warmupCancel()
	} else {
		slog.Info("model_policy: disabled (no DB)")
	}

	// ── Redis (sessions + credential fp slots + pending response cache) ─
	var sessionMgr *session.Manager
	var fpSlotRedis *redis.Client
	var pendingStore *pending.Store
	var redisClientForCache *session.RedisClient
	var routingExec *executors.Executor
	var stateManager *credentialstate.Manager // 2026-06-30: credential×model state manager
	var lastSystemSession *session.LastSystemSessionIndex
	var sessionPref *session.SessionPreference
	if cfg.RedisAddr != "" {
		redisClient := session.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
		pingErr := redisClient.Ping(pingCtx)
		pingCancel()
		if pingErr == nil {
			sessionTTL := time.Duration(cfg.SessionTTLHours) * time.Hour
			pendingTTL := time.Duration(cfg.PendingTTLSeconds) * time.Second
			sessionMgr = session.NewManager(redisClient, sessionTTL)
			chatHandler.SetSessionGetter(sessionMgr)
			redisClientForCache = redisClient
			fpSlotRedis = redis.NewClient(&redis.Options{
				Addr:     cfg.RedisAddr,
				Password: cfg.RedisPassword,
				DB:       cfg.RedisDB,
			})
			pendingStore = pending.NewStore(fpSlotRedis, pendingTTL)
			lastSystemSession = session.NewLastSystemSessionIndex(redisClient)
			sessionPref = session.NewSessionPreference(redisClient)
			slog.Info("session manager enabled", "redis", cfg.RedisAddr, "ttl_hours", cfg.SessionTTLHours)

			// Update health handler with Redis connection (2026-07-08)
			healthHandler.SetRedis(redisClient)
		} else {
			slog.Warn("session manager: redis ping failed", "error", err)
		}
	} else {
		slog.Warn("session manager disabled (no LLM_GATEWAY_REDIS_ADDR)")
	}

	// 2026-07-21, URSM v2 plan T20: 接入 v2 Manager。LoadFromEnv 默认 mode=off，
	// 整个 v2 路径在生产环境（URSM_V2_MODE 未设）下保持 dead：URSMv2 != nil 走
	// fast path 但 Manager 内部 Mode()==off 时 Plan/FilterAndScore 立即返回 nil。
	// 当且仅当环境变量显式设为 shadow/canary/authoritative 时才 SetReady(true)，
	// 把 v2 recovery gate 打开；off 模式下连 SetReady 都不调，保持 store 未初始化
	// 状态。这是 T8 / T16 / T20 一脉相承的"opt-in 启用"约定。
	var ursmV2Mgr *ursmv2.Manager
	if redisClientForCache != nil {
		ursmV2Mgr = ursmv2.New(ursmv2.Dependencies{
			Redis:  redisClientForCache.Client(),
			Config: ursmv2.LoadFromEnv(),
		})
		if env := os.Getenv("URSM_V2_MODE"); env == "shadow" || env == "canary" || env == "authoritative" {
			if err := ursmV2Mgr.SetReady(context.Background(), true); err != nil {
				slog.Warn("ursm.v2: ready set failed", "error", err)
			}
		}
		slog.Info("ursm.v2 manager constructed",
			"mode", ursmV2Mgr.Mode(),
			"ready", ursmV2Mgr.Ready(context.Background()))
	} else {
		slog.Info("ursm.v2 manager disabled (no redis client)")
	}

	// URSM v2 persist writer (T15+L6): snapshot v2 Redis state to DB periodically
	// 2026-07-22: Only run in canary/full modes. Shadow mode never calls RecordRequest,
	// so Redis will be empty and persist writer just wastes CPU logging "collect empty".
	var persistWriterStop context.CancelFunc
	if ursmV2Mgr != nil && dbConn != nil && dbConn.Enabled() {
		v2Cfg := ursmv2.LoadFromEnv()

		// Skip persist writer in shadow mode (no data to persist)
		if v2Cfg.Mode != "shadow" {
			persistWriter := persist.New(redisClientForCache.Client(), v2Cfg.RedisKeyPrefix, dbConn.Pool())
			persistInterval := time.Duration(v2Cfg.PersistIntervalSec) * time.Second
			if persistInterval == 0 {
				persistInterval = 60 * time.Second // default 1 minute
			}

			// 2026-07-22: 使用可取消的 context 支持 graceful shutdown
			persistCtx, cancel := context.WithCancel(context.Background())
			persistWriterStop = cancel

			go func() {
				defer cancel()
				ticker := time.NewTicker(persistInterval)
				defer ticker.Stop()

				for {
					select {
					case <-ticker.C:
						ctx, timeoutCancel := context.WithTimeout(persistCtx, 30*time.Second)
						rows, err := persistWriter.Collect(ctx)
						if err != nil {
							slog.Warn("ursm.v2: persist collect failed", "error", err)
							timeoutCancel()
							continue
						}
						if err := persistWriter.Flush(ctx, rows); err != nil {
							slog.Warn("ursm.v2: persist flush failed", "error", err)
						} else {
							slog.Debug("ursm.v2: persist flushed", "rows", len(rows))
						}
						timeoutCancel()

					case <-persistCtx.Done():
						slog.Info("ursm.v2: persist writer stopped")
						return
					}
				}
			}()
			slog.Info("ursm.v2: persist writer started", "interval_sec", persistInterval.Seconds())
		} else {
			slog.Info("ursm.v2: persist writer disabled in shadow mode")
		}
	}

	// V2-P3.2: turn_logs aggregator — flushes 24h-TTL per-stage logs into
	// gateway.sessions.turn_logs_summary and deletes the source rows.
	// 5-minute polling, runs only when DB is enabled.
	if dbConn != nil && dbConn.Enabled() {
		turnLogsCtx, turnLogsCancel := context.WithCancel(context.Background())
		defer turnLogsCancel()
		turnLogsAgg := NewTurnLogsAggregator(dbConn.Pool())
		go func() {
			defer turnLogsCancel()
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-turnLogsCtx.Done():
					slog.Info("turn_logs aggregator stopped")
					return
				case <-ticker.C:
					rows, err := dbConn.Pool().Query(turnLogsCtx, `
						SELECT tenant_id, session_id
						FROM gateway.session_turn_logs
						WHERE expires_at > NOW()
						GROUP BY tenant_id, session_id
						LIMIT 100
					`)
					if err != nil {
						slog.Warn("turn_logs aggregator: poll query failed", "err", err)
						continue
					}
					for rows.Next() {
						var t, s string
						if scanErr := rows.Scan(&t, &s); scanErr != nil {
							continue
						}
						if aggErr := turnLogsAgg.AggregateAndFlush(turnLogsCtx, t, s); aggErr != nil {
							slog.Warn("turn_logs aggregator: flush failed", "tenant", t, "session", s, "err", aggErr)
						}
					}
					rows.Close()
				}
			}
		}()
		slog.Info("turn_logs aggregator started", "interval_sec", (5 * time.Minute).Seconds())
	}

	fpSlots := credentialfpslot.New(credentialfpslot.Config{
		DefaultLimit:       cfg.DefaultCredentialConcurrency,
		Enabled:            cfg.EnableCredentialFpSlots,
		ActiveGateSeconds:  cfg.CredentialFpSlotActiveGateSeconds,
		ReclaimIdleSeconds: cfg.CredentialFpSlotReclaimIdleSeconds,
	}, fpSlotRedis)

	// 2026-06-23: enable background idle-slot reclaim.
	// 2026-06-24: reclaim config is now derived from Config.ReclaimIdleSeconds
	// (independent from the in-line Acquire-time active gate). Without
	// reclaim, slots that have been silent past ReclaimIdleSeconds but
	// have no incoming traffic would stick around for the full 30-min
	// Redis TTL, blocking later arrivals that would otherwise be eligible
	// for an in-line Acquire-time preempt.
	fpSlots.StartReclaim(context.Background())

	// ── Settings registry (Q1: B, Q2: A) ───────────────────────────────
	// Initialise the unified runtime-config registry. Specs registered here
	// become readable via settings.Global.EffectiveValue(scope, key, tenantID).
	// Order matters: must run BEFORE any code that calls LoadMode/LoadFraction
	// (e.g. compression.NewCompressor).
	var providerSettingsResolver *settings.ProviderSettingsResolver
	if dbConn != nil && dbConn.Enabled() {
		settingsDB := settings.NewStoreDB(dbConn.Pool())
		settings.Init(settingsDB)
		for _, sp := range settings.PlatformSpecs() {
			settings.Global.MustRegisterSpec(sp)
		}
		for _, sp := range settings.TenantSpecs() {
			settings.Global.MustRegisterSpec(sp)
		}
		// 2026-07-09: handoff / goal / audit auto-control settings.
		// These are registered UNCONDITIONALLY (regardless of bgDataPlaneOnly)
		// so admins can configure handoff.* / goal.* / auto_control.* via
		// /api/admin/settings + ModulesView even in data-plane-only mode.
		// The actual response-interceptor chain (initGoalControl) is still
		// gated on !bgDataPlaneOnly because it consumes request hot-path
		// CPU; the spec registration here is purely metadata + validation.
		for _, sp := range settings.AutoControlSpecs() {
			s := sp // take a stable address
			if err := settings.Global.RegisterSpec(&s); err != nil {
				slog.Debug("settings: auto_control spec register skipped",
					"key", sp.Key, "error", err)
			}
		}
		slog.Info("settings: registry initialised",
			"platform_specs", len(settings.Global.AllSpecs()),
			"auto_control_specs", len(settings.AutoControlSpecs()))

		// AUDIT-2 / AUDIT-3 (2026-07-12): 启动时同步 rate_limit.enabled 到
		// ratelimit 包内的 atomic.Bool 缓存，热路径不再读 settings KV。
		// 后续通过 admin /api/settings 更新后，由对应的 onChange 回调触发
		// ratelimit.SetRateLimitEnabled(v) 更新缓存。
		syncRateLimitGateFromSettings()

		// 2026-07-02: apply persisted log.* settings after the registry is
		// initialized. Keep this independent from the rate-limit gate sync.
		applyLogSettingsToLogging()

		// Phase 3.2: Provider-level settings resolver
		providerSettingsResolver = settings.NewProviderSettingsResolver(dbConn.Pool(), settings.Global)
		slog.Info("settings: provider-level resolver initialised")
	} else {
		slog.Info("settings: registry disabled (no DB)")
	}

	// ── Dynamic Timeout Config (Phase 2, 2026-07-23) ────────────────────
	// Initialize TimeoutConfig for adaptive timeout calculation based on
	// context size, historical latency, and network conditions.
	// Hot-reloads config from system_settings table every 30 seconds.
	var timeoutConfig *config.TimeoutConfig
	if dbConn != nil && dbConn.Enabled() {
		timeoutConfig = config.NewTimeoutConfigWithPool(dbConn.Pool(), slog.Default())
		// Graceful shutdown on exit
		defer timeoutConfig.Stop()

		slog.Info("timeout config initialized",
			"mode", timeoutConfig.GetCurrentMode(),
			"base_timeout", timeoutConfig.GetBaseTimeout(),
			"hot_reload_interval", "30s")
	} else {
		slog.Warn("timeout config disabled (no DB), using static timeout from env")
	}

	// Keep executor continuation/retry settings backed by the same hot-reload
	// source used by the gateway runtime. Defaults remain active without DB.
	var executorHotConfig *hotconfig.Config
	if dbConn != nil && dbConn.Enabled() {
		executorHotConfig = hotconfig.New(dbConn.Pool())
		if err := executorHotConfig.Start(context.Background()); err != nil {
			slog.Warn("executor hotconfig disabled", "error", err)
			executorHotConfig = nil
		} else {
			defer executorHotConfig.Stop()
		}
	}
	executors.SetHotConfig(executorHotConfig)

	// ── Routing executor (multi-candidate P2C) ──────────────────────────
	providerClient := provider.NewClient()
	if fpSlotRedis != nil {
		providerClient.SetAvailabilityRedis(fpSlotRedis)
	}
	if dbConn != nil && dbConn.Enabled() {
		providerClient.SetDB(dbConn.Pool(), cfg.SecretKey, cfg.CredentialEncryptionKey)
		resolver.SetDB(dbConn.Pool())
	}
	if providerClient.Enabled() {
		stickyCache := executors.NewStickyCache()
		if dbConn != nil && dbConn.Enabled() {
			stickyCache.SetDB(dbConn.Pool())
			if err := stickyCache.RestoreFromDB(context.Background()); err != nil {
				slog.Warn("sticky restore from DB failed", "error", err)
			}
		}
		router := executors.NewRouter(stickyCache, lim)

		// Connect FpSlots to Router for load-aware P2C selection
		router.FpSlots = fpSlots

		// 2026-07-24 Phase 2.3: 启用压力感知路由（通过环境变量控制）
		if os.Getenv("PRESSURE_AWARE_ROUTING") == "true" {
			router.PressureAwareEnabled = true
			slog.Info("pressure-aware routing enabled", "feature", "phase2.3")
		}

		// 2026-07-21, URSM v2 plan T20: 把 v2 Manager 注入 Router，使
		// PlanCandidates 在 mode=authoritative 时按 v2 FilterAndScore 过滤候选。
		// URSM_V2_MODE=off 时 Manager.Mode() == off，PlanCandidates 里的 v2 分支
		// 不会进入（见 router.go:URSMv2.Mode() == ModeAuthoritative 守卫）。
		router.URSMv2 = ursmV2Mgr

		// 2026-06-30: Credential×model state manager — provides
		// real-time (<1s) availability for routing decisions via a
		// memory→redis→db cache hierarchy. Created early so it can be
		// wired into healthTracker; started later after probe services
		// are initialised.
		if dbConn != nil && dbConn.Enabled() {
			stateManager = credentialstate.NewManager(dbConn.Pool(), fpSlotRedis)

			// Phase 2 (2026-07-01): Enable model popularity tracking.
			// Hot models (>100 req/h) → 10s probe; cold models (<10 req/h) → 10m probe.
			// Requires LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true (default: false).
			if os.Getenv("LLM_GATEWAY_ENABLE_POPULARITY_TRACKING") == "true" {
				stateManager.EnablePopularityTracking()
				slog.Info("popularity tracking enabled (Phase 2)",
					"hot_interval", "10s",
					"warm_interval", "2m",
					"cold_interval", "10m")
			}

			router.StateManager = stateManager
			slog.Info("credential state manager created",
				"redis_enabled", fpSlotRedis != nil)
		}

		// Wire TimeoutConfig to Router (Phase 2, 2026-07-23)
		if timeoutConfig != nil {
			router.TimeoutConfig = executors.NewTimeoutConfigAdapter(timeoutConfig)
			slog.Info("timeout config wired to router")
		}

		// Phase 1 Bandit Scoring (2026-06-26): Initialize Thompson Sampling scorer
		// for intelligent credential selection based on historical performance.
		// Flushes state to database every 10s or when 100 credentials are dirty.
		// Controlled by LLM_GATEWAY_ENABLE_BANDIT_SCORING (default: false).
		//
		// NET-012 fix: 此段在 main 分支上构建断裂（cfg.EnableBanditScoring
		// / banditScorer.LoadFromDB 符号不存在）。WIP feature，临时注释掉。
		// 修复 tracked in: <TBD>
		_ = "bandit scoring disabled (WIP build break) — re-enable when BanditScorer API stabilizes"
		// if cfg.EnableBanditScoring && dbConn != nil && dbConn.Enabled() {
		// 	banditScorer := credential.NewBanditScorer()
		//
		// 	// Load historical state from database (cold start recovery)
		// 	if err := banditScorer.LoadFromDB(context.Background(), dbConn.Pool()); err != nil {
		// 		slog.Warn("bandit: failed to load state from database", "error", err)
		// 	}
		//
		// 	banditFlusher := credential.NewBanditFlusher(
		// 		dbConn.Pool(),
		// 		banditScorer,
		// 		10*time.Second, // flush interval
		// 		100,            // batch size
		// 	)
		// 	banditFlusher.Start()
		// 	defer banditFlusher.Stop()
		//
		// 	router.Bandit = banditScorer
		// 	router.BanditFlusher = banditFlusher
		// 	slog.Info("bandit_scoring", "enabled", true, "flush_interval", "10s", "batch_size", 100)
		// } else if cfg.EnableBanditScoring {
		// 	slog.Warn("bandit_scoring", "enabled", false, "reason", "database not available")
		// }

		norm := streaming.NewNormalizer()
		routingExec = executors.NewExecutor(
			router, cm, lim, pools, upClient,
			norm.NormalizeChunk,
			func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, catalogCode string, normFunc executors.NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) executors.StreamOutcome {
				tenantID := extractTenantIDFromUpstreamResp(resp)
				var pc *streaming.PendingCapturer
				if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
					pc = streaming.NewPendingCapturer(0)
					markCapturedPendingInProgress(pendingStore, resp, tenantID)
				}
				var stripFn func([]byte) []byte
				switch catalogCode {
				case "doubao":
					stripFn = streaming.StripDoubaoFieldsBody
				case "minimax":
					stripFn = streaming.StripMinimaxFieldsBody
				}
				diagnostics := &streaming.DiagnosticContext{
					RawLogger: routingExec.RawDataLogger,
					Anomaly:   routingExec.AnomalyReporter,
					Semantic:  routingExec.SemanticAnalyzer,
				}
				outcome := streaming.StreamChatWithPendingCaptureAndDiagnostics(
					w, resp, clientModel, outboundModel, norm, capture, toolsRequested, stripFn, pc, diagnostics,
				)
				saveCapturedPending(pendingStore, pc, resp, tenantID)
				return outcome
			},
			auditSink,
		)
		routingExec.XMLCoerceNonStream = streaming.CoerceXMLToolCallsInChatResponse
		routingExec.QualityProcessNonStream = streaming.WrapQualityProcessNonStream()
		routingExec.QualitySetMode = streaming.WrapSetQualityFixModeOnContext()
		routingExec.AnthropicPassthroughStream = func(
			w http.ResponseWriter,
			resp *http.Response,
			clientModel, outboundModel, requestID string,
			cap *audit.StreamCapture,
			pcAny any,
		) executors.StreamOutcome {
			tenantID := extractTenantIDFromUpstreamResp(resp)
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
				markCapturedPendingInProgress(pendingStore, resp, tenantID)
			}
			diagnostics := &streaming.DiagnosticContext{
				RawLogger: routingExec.RawDataLogger,
				Anomaly:   routingExec.AnomalyReporter,
				Semantic:  routingExec.SemanticAnalyzer,
			}
			outcome := streaming.StreamAnthropicPassthroughWithDiagnostics(w, resp, clientModel, outboundModel, requestID, cap, pc, diagnostics)
			saveCapturedPending(pendingStore, pc, resp, tenantID)
			return outcome
		}
		routingExec.ChatToAnthropic = streaming.ConvertChatRequestToAnthropic
		routingExec.AnthropicToOpenAI = streaming.ConvertAnthropicBodyToOpenAI

		// Phase B (2026-06-22): IR-based protocol converter.
		if os.Getenv("LLM_GATEWAY_IR_CONVERTER") == "true" {
			routingExec.IR = &irAdapter{}
			slog.Info("ir_converter", "enabled", true)
			if os.Getenv("LLM_GATEWAY_TRANSPORT_IR") == "true" {
				routingExec.IR = transformation.NewTransportIRConverter(&irAdapter{})
				slog.Info("transport_ir", "enabled", true, "features", "extensions-roundtrip,circuit-breaker")
			}
		}

		// ── 诊断与监控组件初始化 (2026-07-26) ──────────────────────────
		// 根据环境变量按需启用原始数据日志、异常报告和语义分析功能。
		// 这些组件在 USRM v2 架构下与 Executor 集成，可选地记录请求/响应数据。
		{
			// 原始数据日志
			if os.Getenv("LLM_GATEWAY_RAW_LOG_ENABLED") == "true" {
				logDir := os.Getenv("LLM_GATEWAY_RAW_LOG_DIR")
				if logDir == "" {
					logDir = "./logs/raw_data"
				}
				maxSizeStr := os.Getenv("LLM_GATEWAY_RAW_LOG_MAX_SIZE")
				maxSize := int64(200 * 1024 * 1024) // 200MB default
				if maxSizeStr != "" {
					if parsed, err := strconv.ParseInt(maxSizeStr, 10, 64); err == nil && parsed > 0 {
						maxSize = parsed
					} else if err != nil || parsed <= 0 {
						slog.Warn("raw_data_logger: invalid max size, using default", "value", maxSizeStr)
					}
				}

				asyncRawLogger, err := logging.NewAsyncRawDataLogger(logDir, maxSize, true, 10000)
				if err != nil {
					slog.Error("raw_data_logger: failed to initialize", "err", err)
				} else {
					rawDataLogger = asyncRawLogger
					routingExec.RawDataLogger = executors.NewRawDataLoggerAdapter(asyncRawLogger)
					slog.Info("raw_data_logger: initialized", "dir", logDir, "max_size", maxSize)
				}
			}

			// 异常报告器
			if os.Getenv("LLM_GATEWAY_ANOMALY_REPORTER_ENABLED") == "true" {
				endpoint := os.Getenv("LLM_GATEWAY_ANOMALY_ENDPOINT")
				if endpoint == "" {
					endpoint = "https://llmgo.kxpms.cn/format-anomalies"
				}

				anomalyReporter = logging.NewLockFreeAnomalyReporter(endpoint, true, 1000)
				routingExec.AnomalyReporter = executors.NewAnomalyReporterAdapter(anomalyReporter)
				slog.Info("anomaly_reporter: initialized", "endpoint", endpoint)
			}

			// 语义分析器
			if os.Getenv("LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED") == "true" {
				semanticAnalyzer := ir.NewSemanticAnalyzer(true)
				routingExec.SemanticAnalyzer = executors.NewSemanticAnalyzerAdapter(semanticAnalyzer)
				slog.Info("semantic_analyzer: initialized")
			}
		}

		// ── Cross-provider model fallback chain (Phase 2, 2026-07-19) ──
		// When all credentials for the client-requested model are exhausted,
		// try equivalent models from other providers. Format:
		//
		//	LLM_GATEWAY_MODEL_FALLBACK="primary=fb1,fb2;..."
		//
		// Example:
		//	LLM_GATEWAY_MODEL_FALLBACK="claude-sonnet-4-20250514=gpt-4o-2024-11-20;claude-haiku-3-20240307=gpt-4o-mini-2024-07-18"
		//
		// When unset, a sensible default chain for common models is used.
		// Set to empty to disable fallback entirely.
		{
			if raw := os.Getenv("LLM_GATEWAY_MODEL_FALLBACK"); raw != "" {
				routingExec.ModelFallbackChain = parseModelFallbackEnv(raw)
			} else {
				routingExec.ModelFallbackChain = executors.DefaultFallbackChain()
			}
		}

		// Q3 streaming
		routingExec.AnthropicToOpenAIStream = func(
			w http.ResponseWriter,
			resp *http.Response,
			clientModel, outboundModel, requestID string,
			cap *audit.StreamCapture,
			pcAny any,
		) executors.StreamOutcome {
			tenantID := extractTenantIDFromUpstreamResp(resp)
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
				markCapturedPendingInProgress(pendingStore, resp, tenantID)
			}
			diagnostics := &streaming.DiagnosticContext{
				RawLogger: routingExec.RawDataLogger,
				Anomaly:   routingExec.AnomalyReporter,
				Semantic:  routingExec.SemanticAnalyzer,
			}
			outcome := streaming.StreamAnthropicSSEToOpenAIWithDiagnostics(w, resp, clientModel, outboundModel, requestID, cap, pc, diagnostics)
			saveCapturedPending(pendingStore, pc, resp, tenantID)
			return outcome
		}
		routingExec.AnthropicToChatResponse = streaming.ConvertAnthropicResponseToChat
		// 2026-06-29: Q2 (anthropic client ← openai upstream) **response**
		// conversion hooks. Closes the Q2 rows of the protocol conversion
		// matrix audit (docs/2026-06-29-protocol-conversion-matrix.md).
		// Pre-fix, the executor wrote the raw OpenAI body / SSE bytes
		// back to the Anthropic client.
		routingExec.ChatResponseToAnthropic = streaming.ConvertChatResponseToAnthropic
		routingExec.OpenAIToAnthropicStream = func(
			w http.ResponseWriter,
			resp *http.Response,
			clientModel, outboundModel, requestID string,
			cap *audit.StreamCapture,
			pcAny any,
		) executors.StreamOutcome {
			tenantID := extractTenantIDFromUpstreamResp(resp)
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
				markCapturedPendingInProgress(pendingStore, resp, tenantID)
			}
			diagnostics := &streaming.DiagnosticContext{
				RawLogger: routingExec.RawDataLogger,
				Anomaly:   routingExec.AnomalyReporter,
				Semantic:  routingExec.SemanticAnalyzer,
			}
			outcome := streaming.StreamOpenAIToAnthropicSSEWithDiagnostics(w, resp, clientModel, outboundModel, requestID, cap, pc, diagnostics)
			saveCapturedPending(pendingStore, pc, resp, tenantID)
			return outcome
		}
		// Phase E (2026-07-01): Responses API client target. Wired only
		// when the handler sets ClientProtocol == "openai-responses" —
		// currently set by domains/streaming/responses.go for /v1/responses.
		routingExec.AnthropicToResponsesStream = func(
			w http.ResponseWriter,
			resp *http.Response,
			clientModel, outboundModel, requestID string,
			cap *audit.StreamCapture,
			pcAny any,
		) executors.StreamOutcome {
			tenantID := extractTenantIDFromUpstreamResp(resp)
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
				markCapturedPendingInProgress(pendingStore, resp, tenantID)
			}
			diagnostics := &streaming.DiagnosticContext{
				RawLogger: routingExec.RawDataLogger,
				Anomaly:   routingExec.AnomalyReporter,
				Semantic:  routingExec.SemanticAnalyzer,
			}
			outcome := streaming.StreamAnthropicSSEToResponsesWithDiagnostics(w, resp, clientModel, outboundModel, requestID, cap, pc, diagnostics)
			saveCapturedPending(pendingStore, pc, resp, tenantID)
			return outcome
		}
		routingExec.OpenAIToResponsesStream = func(
			w http.ResponseWriter,
			resp *http.Response,
			clientModel, outboundModel, requestID string,
			cap *audit.StreamCapture,
			pcAny any,
		) executors.StreamOutcome {
			tenantID := extractTenantIDFromUpstreamResp(resp)
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
				markCapturedPendingInProgress(pendingStore, resp, tenantID)
			}
			diagnostics := &streaming.DiagnosticContext{
				RawLogger: routingExec.RawDataLogger,
				Anomaly:   routingExec.AnomalyReporter,
				Semantic:  routingExec.SemanticAnalyzer,
			}
			outcome := streaming.StreamOpenAIToResponsesSSEWithDiagnostics(w, resp, clientModel, outboundModel, requestID, cap, pc, diagnostics)
			saveCapturedPending(pendingStore, pc, resp, tenantID)
			return outcome
		}
		routingExec.SanitizeAnthropicTools = streaming.SanitizeAnthropicToolsInBody
		routingExec.NormalizeOpenAITools = streaming.NormalizeToolsInChatBody
		routingExec.StripMinimaxFields = streaming.StripMinimaxFieldsBody
		routingExec.StripZhipuFields = streaming.StripZhipuFieldsBody
		routingExec.StripDeepSeekFields = streaming.StripDeepSeekFieldsBody
		routingExec.StripDoubaoFields = streaming.StripDoubaoFieldsBody
		// Write-time 客户端可见脱敏（2026-07-09，增强 1）
		routingExec.RedactBodyFn = buildRedactBodyFn(dbConn.Stdlib())
		routingExec.StreamTimeout = time.Duration(cfg.StreamTimeout) * time.Second
		routingExec.UpstreamTimeout = time.Duration(cfg.UpstreamTimeout) * time.Second
		routingExec.StreamRetryThreshold = cfg.StreamRetryThreshold
		// 2026-06-21: 同步重试超时（全候选失败后保持客户端连接继续重试）
		// 2026-07-18: 设为60s，给慢节点足够时间，同时配合单节点快速恢复机制
		routingExec.SyncRetryTimeout = 60 * time.Second
		if v := os.Getenv("LLM_GATEWAY_SYNC_RETRY_TIMEOUT"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				routingExec.SyncRetryTimeout = time.Duration(n) * time.Second
				slog.Warn("sync_retry_timeout_overridden",
					"timeout", routingExec.SyncRetryTimeout)
			}
		}
		slog.Info("sync_retry", "timeout", routingExec.SyncRetryTimeout)
		// MnfStreak (Step 6, 2026-06-18): client hot-path breaker
		// for persistent model_not_found. When the same sticky
		// session accumulates M MnfStickyBreakThreshold
		// model_not_found responses from the same credential, the
		// sticky binding is deleted so the next request re-picks.
		// See routing/mnf_streak.go.
		//
		// Defaults: enabled, threshold 3, cap 10000. Override via
		// env (LLM_GATEWAY_MNF_STREAK_ENABLED / _THRESHOLD /
		// _CAPACITY) for emergency rollback — set _ENABLED=false
		// to disable without removing the code path.
		mnfStreakCap := 10000
		if v := os.Getenv("LLM_GATEWAY_MNF_STREAK_CAPACITY"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				mnfStreakCap = n
			}
		}
		routingExec.MnfStreak = executors.NewMnfStreak(mnfStreakCap)
		routingExec.MnfStickyBreakThreshold = 3
		routingExec.MnfStreakEnabled = true
		if v := os.Getenv("LLM_GATEWAY_MNF_STREAK_ENABLED"); v == "false" || v == "0" {
			routingExec.MnfStreakEnabled = false
			slog.Warn("mnf_streak_disabled_via_env")
		}
		if v := os.Getenv("LLM_GATEWAY_MNF_STREAK_THRESHOLD"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				routingExec.MnfStickyBreakThreshold = n
			}
		}
		slog.Info("mnf_streak_enabled",
			"threshold", routingExec.MnfStickyBreakThreshold,
			"capacity", mnfStreakCap,
		)
		// BUG-4 fix: mnf_cooling temporarily disables a binding when
		// it accumulates too many model_not_found errors in 10 min.
		routingExec.MnfCoolThreshold = 5
		routingExec.MnfCoolMinutes = 2
		if v := os.Getenv("LLM_GATEWAY_MNF_COOL_THRESHOLD"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				routingExec.MnfCoolThreshold = n
			}
		}
		if v := os.Getenv("LLM_GATEWAY_MNF_COOL_MINUTES"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				routingExec.MnfCoolMinutes = n
			}
		}
		slog.Info("mnf_cooling_enabled",
			"threshold", routingExec.MnfCoolThreshold,
			"cool_minutes", routingExec.MnfCoolMinutes,
		)
		// Track C C4 (2026-06-18): wire the pending response cache
		// into the executor so it can demote a slow synchronous
		// walk to async mode. Defaults: 15s short (synchronous
		// budget), 300s long (async total deadline), 2 fallback
		// credentials. Override via env for emergency rollback.
		if pendingStore != nil {
			routingExec.PendingStore = pendingStore
			routingExec.AsyncShortTimeout = 15 * time.Second
			routingExec.AsyncLongTimeout = 300 * time.Second
			routingExec.AsyncMaxFallbackCreds = 2
			if v := os.Getenv("LLM_GATEWAY_ASYNC_SHORT_TIMEOUT"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					routingExec.AsyncShortTimeout = time.Duration(n) * time.Second
				}
			}
			if v := os.Getenv("LLM_GATEWAY_ASYNC_LONG_TIMEOUT"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					routingExec.AsyncLongTimeout = time.Duration(n) * time.Second
				}
			}
			if v := os.Getenv("LLM_GATEWAY_ASYNC_MAX_FALLBACK_CREDS"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 {
					routingExec.AsyncMaxFallbackCreds = n
				}
			}
			slog.Info("async_pending_enabled",
				"short_timeout", routingExec.AsyncShortTimeout,
				"long_timeout", routingExec.AsyncLongTimeout,
				"max_fallback_creds", routingExec.AsyncMaxFallbackCreds,
			)
		}
		// Round 47 compression v7 T16: build the unified compression dispatcher.
		// The Compressor reads LLM_GATEWAY_COMPRESSION_MODE (default=on_4xx per
		// user Q1) and LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION (default=0.8).
		// All three modes (off / auto_threshold / on_4xx) are nil-safe so a
		// misconfigured install degrades gracefully to ModeOff.
		routingExec.Compressor = compression.NewCompressor()
		slog.Info("compressor initialized",
			"mode", routingExec.Compressor.Mode().String(),
			"window_fraction", routingExec.Compressor.Estimator().Fraction(),
		)

		// Phase 3.2: Wire provider-level settings resolver into executor and compressor
		if providerSettingsResolver != nil {
			routingExec.ProviderSettings = providerSettingsResolver
			routingExec.Compressor.ProviderSettings = providerSettingsResolver
			slog.Info("provider-level settings resolver wired to executor")
		}

		// Memora: optional context-compression oracle. When the
		// LLM_GATEWAY_MEMORA_BASE_URL env is set, the executor can ask
		// Memora for L1 session facts on context overflow and rebuild
		// the body around them. Disabled by default (no env var).
		if memoraBase := os.Getenv("LLM_GATEWAY_MEMORA_BASE_URL"); memoraBase != "" {
			memorySvc = newLegacyMemoryServicesFromEnv(memoraBase)
			routingExec.Memora = memorySvc.Reader()
			// Async sink: fire-and-forget write buffer for L1 session
			// memory persistence. 2 workers / 2048-deep queue is enough
			// for the write volume (one enqueue per successful request).
			memorySvc.Start()
			routingExec.MemoraSink = memorySvc.Writer()
			smartSearchBase := os.Getenv("LLM_GATEWAY_MEMORA_SMART_SEARCH_BASE_URL")
			slog.Info("memora context-compression oracle enabled",
				"base_url", memoraBase,
				"smart_search_url", smartSearchBase,
			)

			// 2026-07-09: DLQ and FallbackCache initialization
			// TODO: enable when memorySvc.DLQ()/Client()/Sink()/SetDLQ/SetFallbackCache are implemented
			slog.Debug("memora DLQ/FallbackCache: skipped (not yet implemented)")
		} else {
			slog.Info("memora context-compression oracle disabled (set LLM_GATEWAY_MEMORA_BASE_URL to enable)")
		}
		if dbConn != nil && dbConn.Enabled() {
			routingExec.State = credential.NewWriter(dbConn.Pool())
			routingExec.DB = dbConn
			routingExec.HeaderProfiles = executors.NewHeaderProfileCache(dbConn.Pool())
		}
		routingExec.FpSlots = fpSlots

		// Health tracking (2026-06-22): sliding window recorder + concurrency tuner + continuous failure checker
		if fpSlotRedis != nil && dbConn != nil {
			healthTracker := executors.NewHealthTracker(
				fpSlotRedis,
				dbConn.Pool(),
				2*time.Hour, // window TTL
				100,         // max size
			)
			routingExec.HealthTracker = healthTracker
			slog.Info("health_tracker initialized", "window", "1h", "max_size", 100)
		}

		// 2026-06-28: Wire UnifiedProbeScheduler for real-time request feedback.
		// This enables <30s failure detection and adaptive health tracking.
		// The unifiedProbe is initialized in the bg services block below.
		// We set a placeholder here and update it after bg services start.
		routingExec.UnifiedProbeScheduler = nil // will be set after bg services start

		// 2026-06-23 Phase 2 (P1): per-candidate failure logger. Writes one
		// row to candidate_failure_logs per failed (request, credential,
		// model, attempt) tuple so operators can see WHICH credentials
		// failed in a sequence (request_logs only records the LAST one).
		if dbConn != nil {
			routingExec.FailureLogger = executors.NewCandidateFailureWriter(dbConn.Pool())
			slog.Info("candidate_failure_logger initialized")
		}

		routingExec.Provider = providerClient
		// Inject peak collector (after bg workers have started it).
		if peakCollector != nil {
			routingExec.PeakCollector = peakCollector
		}
		// Enable disguise mode if configured.
		if cfg.EnableDisguise {
			if fpSlotRedis != nil {
				routingExec.DisguisePool = disguise.NewRedisPool(fpSlotRedis, 30*time.Minute)
			} else {
				routingExec.DisguisePool = disguise.DefaultPool
			}
			slog.Info("disguise mode enabled")
		}
		// V3.1 (2026-06-26): wire route node recorder + session routing
		routingExec.Recorder = executors.NewRouteNodeRecorder(fpSlots)

		// 2026-07-01 Phase 2.x: wire credential state observer for real request feedback
		if stateManager != nil {
			routingExec.StateObserver = stateManager
			slog.Info("credential state observer enabled (Phase 2.x real request feedback)")
		}

		// 2026-07-21, URSM v2 plan T20: 把 v2 Manager 注入 Executor，使
		// 请求完成后能调 Manager.RecordRequest 把结果回写到 v2 store（影子/金丝雀
		// 模式下由 Manager 内部 ShouldUseV2 决定是否真的写入）。Manager 自身 nil-safe，
		// 这条赋值在 URSM_V2_MODE=off 时也安全——RecordRequest 在 off 模式下走 no-op。
		routingExec.URSMv2 = ursmV2Mgr

		// 2026-07-15: shadow-only state/probe observer. It reads the hot
		// platform settings on each event and cannot alter routing, state,
		// cache invalidation, or probe dispatch while in shadow mode.
		routingExec.RoutingStateShadow = routingstate.NewShadowObserver()
		slog.Info("routing state shadow observer initialized")

		// 2026-07-07 Phase 1: wire FpSlot degradation tracker.
		// Unconditional — it is in-memory only (Prometheus counters + a
		// 1-minute sliding window) and has no external dependencies, so it
		// is safe to enable in every environment. When nil the executor
		// silently skips recording, so this also documents the contract.
		routingExec.DegradationTracker = executors.NewDegradationTracker()
		slog.Info("fp_slot_degradation_tracker enabled (Phase 1 monitoring)")

		// 2026-07-16: 自适应超时与状态管理增强
		// Phase 2 (2026-07-23): Use TimeoutConfigAdapter if available
		if timeoutConfig != nil {
			routingExec.TimeoutAdapter = executors.NewTimeoutConfigAdapter(timeoutConfig)
			slog.Info("using TimeoutConfigAdapter from Phase 2")

			// Phase 3 (2026-07-23): Configure keepalive interval
			routingExec.KeepaliveInterval = timeoutConfig.GetKeepaliveInterval()
			slog.Info("keepalive interval configured from TimeoutConfig",
				"interval_seconds", routingExec.KeepaliveInterval)
		}
		routingExec.TTFBTracker = executors.NewTTFBTracker()
		routingExec.PreRequestValidator = executors.NewRequestValidator(false) // non-strict mode
		if routingExec.State != nil {
			routingExec.PostExecutionHook = executors.NewExecutionRecorder(routingExec.State)
		}
		slog.Info("adaptive_timeout_and_state_management enabled",
			"base_timeout", "30s",
			"min_timeout", "15s",
			"max_timeout", "120s",
			"validator_strict", false,
		)

		chatHandler.SetExecutor(routingExec, providerClient, stickyCache)
		chatHandler.SetSessionRouting(lastSystemSession, sessionPref)

		// ── 2026-07-17: 请求链路追踪 ──────────────────────────────────────
		// 创建 trace.Recorder (Redis 暂存 + JSONB 持久化),注入到 ChatHandler
		// 与 routingExec。Redis 不可用时降级 NoopRecorder (零开销)。
		var traceRec gwtrace.Recorder = gwtrace.NewRedisRecorder(redisClientForCache.Client())
		chatHandler.SetTraceRecorder(traceRec)
		routingExec.SetTraceRecorder(traceRec)
		slog.Info("request_trace_recorder: enabled",
			"redis_connected", redisClientForCache != nil,
			"recorder_type", fmt.Sprintf("%T", traceRec))
		// 2026-06-26: configurable recent-session reuse window. Default
		// is 5m (session.LastSystemSessionTTL). Operators can shorten it
		// to reduce the chance of two unrelated clients being merged.
		chatHandler.SetSessionReuseWindow(streaming.ParseSessionReuseWindow())
		// Track C C5 (2026-06-18): wire the idempotent dedup cache.
		// Default 100 entries / 5 min TTL; override via env.
		idempotentCap := 100
		idempotentTTL := 300 // seconds
		if v := os.Getenv("LLM_GATEWAY_IDEMPOTENT_CAP"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				idempotentCap = n
			}
		}
		if v := os.Getenv("LLM_GATEWAY_IDEMPOTENT_TTL"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				idempotentTTL = n
			}
		}
		chatHandler.SetIdempotentCache(streaming.NewIdempotentCache(idempotentCap, time.Duration(idempotentTTL)*time.Second))
		slog.Info("idempotent_cache_enabled",
			"cap", idempotentCap,
			"ttl_seconds", idempotentTTL,
		)

		slog.Info("routing executor enabled")
	} else {
		slog.Warn("routing executor disabled (no database connection)")
	}

	// ── Auth + Rate Limiting ──────────────────────────────────────────────
	keyVerifier := authentication.NewKeyVerifier()
	if dbConn != nil && dbConn.Enabled() {
		keyVerifier.SetDB(dbConn.Pool(), cfg.SecretKey)
	}
	if keyVerifier.Enabled() {
		slidingRL := ratelimit.NewRedisLimiterFromEnv()
		chatHandler.SetAuth(keyVerifier, slidingRL)
		// /v1/embeddings handler (22 章 §22.2). providerClient + upClient
		// are both ready by this point.
		embeddingsHandler = streaming.NewEmbeddingsHandler(providerClient, upClient)
		embeddingsHandler.SetAuth(keyVerifier, slidingRL)
		slog.Info("API key authentication + RPM rate limiting enabled")
	} else {
		slog.Warn("API key authentication disabled (no database connection)")
	}

	// ── Telemetry ─────────────────────────────────────────────────────────
	telemetryClient := telemetry.NewClient()
	if dbConn != nil && dbConn.Enabled() {
		telemetryClient.SetDB(dbConn.Pool())
	}
	if telemetryClient.Enabled() {
		chatHandler.SetTelemetry(telemetryClient)
		if embeddingsHandler != nil {
			embeddingsHandler.SetTelemetry(telemetryClient)
		}
	}

	// 2026-07-15: clientprofile 画像管线接通（消费 EventEmitter → ProfileWorker →
	// client_profiles / client_behavior_events 表）。Setup 内部启动 RunLoop goroutine。
	if dbConn != nil && dbConn.Enabled() {
		pub := bus.NewPGPublisher(dbConn.Pool(), slog.Default())
		profileBundle, profileErr := integration.SetupClientProfileIntegration(
			context.Background(),
			bus.AsPGDB(dbConn.Pool()),
			nil, // *sql.DB 桥接复用 main pipeline 已有的 nil-safe 模式（同位 detector/checker）
			pub,
			integration.ClientProfileLoopConfig{
				Interval:  5 * time.Second,
				BatchSize: 10,
				Logger:    slog.Default(),
			},
		)
		if profileErr == nil && profileBundle != nil {
			chatHandler.SetProfileEmitter(profileBundle.Emitter)
			slog.Info("clientprofile: bundle wired into chatHandler",
				"worker", profileBundle.Worker.Name())
			defer func() {
				if profileBundle.Cancel != nil {
					profileBundle.Cancel()
				}
			}()
		} else {
			slog.Warn("clientprofile: SetupClientProfileIntegration failed; profile disabled",
				"error", profileErr)
		}
	}

	if telemetryClient.Enabled() {
		// 2026-06-20: wire telemetry into the executor so that
		// runAsyncRetry can write success back to request_logs.
		// Without this, async-retry success leaves the original
		// in_progress / model_not_found row uncorrected (the sync
		// phase returns 202 + AsyncPendingError without calling
		// emitTelemetry).
		if routingExec != nil {
			routingExec.RequestLogEmitter = telemetryClient
		}
		slog.Info("telemetry emission enabled (chatHandler + routingExec)")
	}

	// ── Live request stream SSE hub (2026-07-03) ───────────────────
	// Fans out newly-persisted request_logs rows to dashboard clients
	// at GET /api/admin/live-stream. Created here (not in admin) so
	// we have access to the database pool and the telemetry client.
	// Without a DB the hub still relays live broadcasts from
	// telemetry — only the initial replay is empty.
	// 2026-07-06: RedisClient wired for 1-hour persistent cache.
	// 2026-07-09: cached snapshot TTL/cleanup interval are configurable.
	// Cleanup follows TTL unless explicitly overridden.
	// 2026-07-16: snapshot refresh interval (30 min default) — env LLM_GATEWAY_LIVE_STREAM_SNAPSHOT_REFRESH_INTERVAL.
	liveStreamCachedTTL, liveStreamCachedCleanup := liveStreamCachedDurationsFromEnv()
	liveStreamSnapshotRefresh := positiveDurationEnv(
		"LLM_GATEWAY_LIVE_STREAM_SNAPSHOT_REFRESH_INTERVAL",
		30*time.Minute,
	)
	var liveStreamHub *admin.LiveStreamSSEHub
	var anomalyHarvester *streaming.AnomalyHarvester
	if dbConn != nil && dbConn.Enabled() {
		liveStreamHub = admin.NewLiveStreamSSEHub(dbConn.Pool(), admin.LiveStreamConfig{
			BroadcastQueueSize:            2048,
			InitialReplayLimit:            200,
			IdleThreshold:                 admin.LiveStreamIdleThreshold,
			IdleTickInterval:              10 * time.Second,
			KeepaliveInterval:             25 * time.Second,
			RedisClient:                   fpSlotRedis, // reuse the existing Redis connection
			CachedSnapshotTTL:             liveStreamCachedTTL,
			CachedSnapshotCleanupInterval: liveStreamCachedCleanup,
			SnapshotRefreshInterval:       liveStreamSnapshotRefresh,
		})
		go liveStreamHub.Run()

		// Publish terminal live-stream updates only after the request log
		// transaction commits. Emitting before persistence allowed a green
		// tile to reach the dashboard before GET /api/logs/{id} could see it.
		// The provider_id→catalog_code resolution is bounded by the hub's
		// short lookup context, and Publish remains non-blocking for clients.
		if telemetryClient.Enabled() {
			hub := liveStreamHub
			telemetryClient.AddOnRequestLogPersisted(func(entry *telemetry.RequestLogEntry) {
				hub.Publish(adminLiveRequestFromEntry(entry, hub))
			})
			slog.Info("telemetry onPersisted wired → live stream SSE hub")
		}

		slog.Info("live request stream hub enabled (sse /api/admin/live-stream)",
			"cached_snapshot_ttl", liveStreamCachedTTL.String(),
			"cached_snapshot_cleanup_interval", liveStreamCachedCleanup.String())
	}

	// ── Request WAL (Request Logger) ───────────────────────────────────────
	// 2026-06-22: Synchronous initial log + async batch updates for request lifecycle.
	// Uses same DB pool as telemetryClient. Disabled if env var LLM_GATEWAY_REQUEST_WAL_DISABLE=true.
	var requestLogger *telemetry.RequestLogger
	if dbConn != nil && dbConn.Enabled() && os.Getenv("LLM_GATEWAY_REQUEST_WAL_DISABLE") != "true" {
		requestLogger = telemetry.NewRequestLogger(dbConn.Pool(), &telemetry.RequestLoggerConfig{
			QueueSize:    10000,
			BatchSize:    50,
			FlushTimeout: 100 * time.Millisecond,
			Enabled:      true,
		})
		chatHandler.SetRequestLogger(requestLogger)
		slog.Info("request WAL enabled", "queue_size", 10000, "batch_size", 50)
	}

	// 2026-07-15: Mirror request_logs.attachments JSONB into the relational
	// public.request_attachments table (migration 401). Best-effort hook:
	// any DB error is logged but never blocks the primary INSERT.
	if telemetryClient != nil && dbConn != nil && dbConn.Enabled() {
		attachmentRepo := attachments.NewRepository(dbConn.Pool())
		telemetryClient.AddOnRequestLogPersisted(attachmentmirror.PersistHook(attachmentRepo))
		slog.Info("attachment mirror enabled (request_attachments relational write)")
	}

	// 2026-07-21: V2 sessions shadow write (gateway.sessions / session_turns /
	// session_bodies / session_turn_logs). Feature-flagged via
	// sessions_v2.enabled + sessions_v2.shadow_write in the hot-reload
	// settings system. Best-effort hook: any DB error is logged but never
	// blocks the primary INSERT. Schema prerequisite: migration 430 must
	// have been applied on the target database.
	if telemetryClient != nil && dbConn != nil && dbConn.Enabled() {
		sessionV2Writer := initSessionV2Writer(dbConn.Pool())
		if sessionV2Writer != nil {
			telemetryClient.AddOnRequestLogPersisted(sessionv2mirror.PersistHook(sessionV2Writer))
			slog.Info("session V2 shadow write hook registered (gateway.sessions gateway.session_turns gateway.session_bodies gateway.session_turn_logs)")
		}
	}

	// v3 (2026-06-19) session-level intelligent compression.
	// Builds SessionCache (L1+L2+L3) and SessionCompressor (orchestrator),
	// then wires them into the chat handler. Feature-flagged via
	// LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE so the deploy can roll back
	// instantly without code change. Captures `exec` from the outer scope.
	var scCache *compression.SessionCache // 2026-07-03: outer-scope for approval integration
	if redisClientForCache != nil && dbConn != nil && dbConn.Enabled() && telemetryClient.Enabled() && !compressorSessionDisabled() {
		scCache = compression.NewSessionCache(redisBackendFromClient(redisClientForCache), dbBackendFromPool(dbConn))
		// Shared LLM-compaction dependencies: used by both the proactive
		// SessionCompressor and the reactive RecoveryCoordinator so both
		// paths run the same lossless summary. Built once here to avoid
		// constructing two adapter chains.
		compactionDeps := NewDependenciesFromExecutor(routingExec)
		scDeps := compression.SessionCompressorDeps{
			Cache:          scCache,
			CompactionDeps: compactionDeps,
		}
		chatHandler.SetSessionCompressor(compression.NewSessionCompressor(scDeps))
		slog.Info("v3 session-level compressor wired (L1 in-mem + L2 Redis + L3 PG)")

		// v5 (2026-06-25) session-aware smart recovery coordinator.
		// Summarizer is wired so the reactive (4xx context_length_exceeded)
		// path produces a structured LLM summary (preserving original goal,
		// progress, and plan) instead of degrading to mechanical trim.
		// When compactionDeps has no Provider/Memora, NewSummaryFunc returns
		// a func that always reports ok=false → falls back to mechanical trim,
		// so deployments without an LLM endpoint keep existing behaviour.
		rcDeps := compression.RecoveryDeps{
			Cache:      scCache,
			MaxRetries: 2,
			Summarizer: compression.NewSummaryFunc(compactionDeps),
		}
		routingExec.RecoveryCoord = compression.NewRecoveryCoordinator(rcDeps)
		slog.Info("v5 smart recovery coordinator wired (session-aware incremental compression)")
	} else {
		slog.Info("v3 session-level compressor disabled (no Redis / no DB / env flag off)")
	}

	// ── Prompt-cache optimization (rtk borrowing, 2026-07-06) ───────────────
	// (a) prefix.Stabilize: reorders messages by stability class to maximise
	//     upstream KV-prefix-cache hits. Default ON; the transform is
	//     idempotent + fail-open so it can never break a request. Disable
	//     with LLM_GATEWAY_PROMPT_CACHE_STABILIZE=0.
	// (b) CacheInjector: places cache_control markers on the stabilized
	//     boundary for candidates that declare SupportsPromptCache. Default
	//     OFF (opt-in via LLM_GATEWAY_PROMPT_CACHE_INJECT=1) because it
	//     depends on candidate.CacheMode data accuracy.
	stabilizeOn := envBool("LLM_GATEWAY_PROMPT_CACHE_STABILIZE", true)
	chatHandler.SetPromptCacheStabilize(stabilizeOn)
	if stabilizeOn {
		slog.Info("prompt-cache prefix stabilization enabled (cache/prefix.Stabilize)")
	}
	if sessionMgr != nil {
		chatHandler.SetCacheInjector(session.NewCacheInjector(sessionMgr))
		if envBool("LLM_GATEWAY_PROMPT_CACHE_INJECT", false) {
			chatHandler.SetPromptCacheInject(true)
			slog.Info("prompt-cache-control injection enabled (session.CacheInjector) — opt-in")
		}
	}

	// ── Phase 2: Meta-tools handler ─────────────────────────────────────
	if dbConn != nil && dbConn.Enabled() {
		metaHandler := metatools.NewHandler(dbConn.Pool())
		interceptor := streaming.NewMetaToolInterceptor(metaHandler)
		chatHandler.SetMetaToolInterceptor(interceptor)
		slog.Info("Phase 2 meta-tools interceptor wired (list_categories, load_tools)")
	} else {
		slog.Info("Phase 2 meta-tools disabled (no DB)")
	}

	// ── Phase 3: Tool Registry ──────────────────────────────────────────
	var toolRegistryAPI *admin.ToolRegistryAPI
	var toolRegistry *registry.ToolRegistry
	if dbConn != nil && dbConn.Enabled() {
		toolRegistry = registry.NewToolRegistry(dbConn.Pool(), slog.Default())
		adapter := registry.NewAdapter(toolRegistry)
		chatHandler.SetToolRegistry(adapter)
		toolRegistryAPI = admin.NewToolRegistryAPI(toolRegistry)
		slog.Info("Phase 3 tool registry wired (tool_ids expansion)")
	} else {
		slog.Info("Phase 3 tool registry disabled (no DB)")
	}

	if dbConn != nil && dbConn.Enabled() {
		maasSvc := maas.NewService(dbConn.Pool())
		chatHandler.SetMaas(maasSvc)
		slog.Info("maas credits billing enabled")

		// 2026-07-13: backfill hourly credit consumption buckets so the
		// admin dashboard "总积分消耗" KPI is ready immediately on startup.
		// Idempotent: ON CONFLICT DO UPDATE SET credits = EXCLUDED
		// converges to request_logs.credits_charged source-of-truth values.
		// Errors are logged but not fatal — dashboard degrades to zero hint
		// when the bucket table is empty.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if _, err := maasSvc.BackfillCreditConsumptionBuckets(ctx, 90); err != nil {
				slog.Warn("maas: startup backfill of credit consumption buckets failed",
					"error", err)
			}
		}()
	}

	// ── Attachment Extractor (2026-07-01) ───────────────────────────────
	// 从请求体中提取 base64/data-URI 附件并保存到存储后端。
	// 配置项：
	//   LLM_GATEWAY_STORAGE_TYPE          : "filesystem" | "oss" | "s3" | "minio" | "cloudreve"
	//                                       （未设置 = filesystem，旧行为）
	//   LLM_GATEWAY_ATTACHMENT_DIR        : 文件系统后端根目录 (默认 ./data/attachments)
	//                                       —— 当 STORAGE_TYPE=filesystem 时使用
	//   LLM_GATEWAY_ATTACHMENT_MAX_SIZE   : 单文件上限（字节，默认 10MB）
	//   LLM_GATEWAY_OSS_* / S3_* / CLOUDREVE_* : 对应后端的认证/端点变量
	//                                          （详见 domains/attachments/storage_config.go）
	//
	// initAttachmentStorage 是 boot 接线入口：
	//   - 默认行为不变（filesystem / unset 走 LocalStorageBackend）
	//   - 显式 opt-in 到 oss / s3 / cloudreve 时走对应后端构造
	//   - 任何错误退化到 filesystem + Warn，不阻塞启动
	// 详见 cmd/gateway/attachment_storage_init.go。
	defaultAttachmentDir := "./data/attachments"
	if envDir := os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR"); envDir != "" {
		defaultAttachmentDir = envDir
	}
	attachmentStorage, attachmentBackendType := initAttachmentStorage(defaultAttachmentDir)
	slog.Info("attachment extractor: storage backend selected",
		"type", attachmentBackendType,
		"dir", defaultAttachmentDir)
	if attachmentStorage != nil {
		attachmentExtractor := attachments.NewExtractor(attachmentStorage)
		chatHandler.SetAttachmentExtractor(attachmentExtractor)
		slog.Info("attachment extractor enabled",
			"type", attachmentBackendType,
			"max_size_mb", attachmentStorage.MaxSize/(1024*1024))
	}

	// ── Model Discovery ─────────────────────────────────────────────────
	bgDataPlaneOnly := strings.EqualFold(cfg.BGMode, "data-plane")
	var discoverySvc *discovery.Service
	var fernetKey []byte
	var keyring *secret.Keyring
	if dbConn != nil && dbConn.Enabled() {
		modelsHandler.SetDB(dbConn.Pool())

		// Derive credential decryption keys early so discovery can use them
		var ferr error
		fernetKey, ferr = secret.FernetKeyFromSecret(cfg.SecretKey, cfg.CredentialEncryptionKey)
		if ferr != nil {
			slog.Warn("fernet key unavailable", "error", ferr)
			fernetKey = nil
		}
		if cfg.CredentialEncryptionKey != "" {
			if kr, kErr := secret.KeyringFromEnv(cfg.SecretKey, cfg.CredentialEncryptionKey); kErr != nil {
				slog.Warn("AES-GCM keyring init failed, falling back to Fernet only", "error", kErr)
			} else {
				keyring = kr
				slog.Info("AES-GCM keyring initialized")
			}
		}

		if !bgDataPlaneOnly {
			discoverySvc = discovery.NewService(dbConn.Pool(), 1*time.Hour)
			discoverySvc.SetKeyring(keyring)
			discoverySvc.SetFernetKey(fernetKey)
			discoverySvc.Start(context.Background())
			slog.Info("model discovery service enabled")
			slog.Info("CHECKPOINT: discovery.Start() returned")
		} else {
			slog.Info("model discovery skipped (bg_mode=data-plane)")
		}
	}

	slog.Info("CHECKPOINT: after discovery section")

	// ── Admin API ───────────────────────────────────────────────────────
	// 2026-07-10 fix: 即使 dbConn=nil (DB 不可达) 也要注册 /api/auth/* 路由。
	// 否则 SPA 启动调 /api/auth/me 探测登录态时拿到 404（而非 401），
	// 永远认为未登录；username/password 登录 (/api/auth/token) 也无法用。
	// 修复：admin.NewHandler 在 db=nil 时仍能创建（handler 内部按需 db），
	// 保证 /api/auth/* 路由全部注册上，DB 相关 handler 在请求时再 503/500。
	var adminHandler *admin.Handler
	var promptInjectionHandler *admin.PromptInjectionHandler
	{
		var adminDB *pgxpool.Pool
		if dbConn != nil && dbConn.Enabled() {
			adminDB = dbConn.Pool()
		}
		adminHandler = admin.NewHandler(adminDB, cfg.SecretKey, fernetKey)
		// 注册 /api/admin/prompt-injection/* 路由(策略、规则、引擎、
		// Canary、严重度矩阵、检测日志、统计)。修复前端调用 404 的 bug。
		// 之前 handler 已实现但从未被 wire 到 main mux,导致 SPA 中所有
		// prompt-injection API 都返回 404 (page is empty)。
		promptInjectionHandler = admin.NewPromptInjectionHandler(adminDB, cfg.SecretKey)

		slog.Info("admin handler created", "db_enabled", adminDB != nil)
	}

	// 2026-07-24: Wire URSM v2 Manager to admin handler so emergency repair
	// operations can also clear Redis state (cooling/fail counters).
	if ursmV2Mgr != nil {
		adminHandler.SetURSMv2(ursmV2Mgr)
		slog.Info("ursm.v2 manager wired to admin handler for emergency repair")
	}

	var approvalMgr *sessionaudit.ApprovalManager // 2026-06-27: outer-scope so the timeout worker can read it
	if dbConn != nil && dbConn.Enabled() {
		slog.Info("CHECKPOINT: before admin.SetKeyring etc")
		// 2026-07-17: wire KeyVerifier so admin write endpoints
		// (updateKeyLimits/enable/disable/revoke/patchKey) invalidate the
		// cached KeyInfo and rate-limit changes apply immediately instead of
		// after the 60s TTL.
		if keyVerifier != nil && keyVerifier.Enabled() {
			adminHandler.SetKeyVerifier(keyVerifier)
		}
		if keyring != nil {
			adminHandler.SetKeyring(keyring)
		}
		if discoverySvc != nil {
			adminHandler.SetDiscoveryService(discoverySvc)
		}
		// 2026-07-01 (migration 325): 为 admin mux 注入附件下载/列表 handler，
		// 使 GET /api/attachments/{path...} 与 GET /api/logs/{id}/attachments 可用。
		// attachmentStorage 可能为 nil（启动时存储初始化失败），admin 端点对此 nil-safe。
		if attachmentStorage != nil {
			adminHandler.SetAttachmentStorage(attachmentStorage)
			adminHandler.SetAttachmentHandler(attachments.NewHandler(attachmentStorage, dbConn.Pool()))
			slog.Info("attachment download/list handler wired",
				"dir", attachmentStorage.BaseDir())
		}
		formatAnomalyRecorder := streaming.NewFormatAnomalyRecorderFromPool(dbConn.Pool())
		chatHandler.SetFormatAnomalyRecorder(formatAnomalyRecorder)
		if requestLogger != nil {
			requestLogger.SetAnomalyRecorder(formatAnomalyRecorder)
		}
		slog.Info("response format anomaly recorder wired")

		// ── Format Detection & Auto-Fix System (2026-07-26) ──
		// Initialize intelligent format detection system for automatic client
		// format identification and common issue fixes (empty objects, wrong types).
		formatRegistry := streaming.NewFormatRegistry()
		formatDetector := streaming.NewFormatDetector(formatRegistry)
		formatFixer := streaming.NewFormatFixer()

		// Use Redis cache if available for session-level format caching
		var formatCache streaming.FormatCache
		if redisClientForCache != nil && redisClientForCache.Client() != nil {
			formatCache = streaming.NewRedisFormatCache(
				redisClientForCache.Client(),
				24*time.Hour, // TTL: 24 hours
			)
			slog.Info("format detection: Redis cache enabled")
		} else {
			formatCache = streaming.NewNullFormatCache()
			slog.Info("format detection: cache disabled (Redis not available)")
		}

		chatHandler.SetFormatDetection(formatDetector, formatFixer, formatCache)
		slog.Info("format detection system initialized",
			"patterns", len(formatRegistry.List()),
			"cache_enabled", formatCache != nil)

		// 2026-06-27 session-audit: wire the approval manager so
		// /api/admin/session-{audit,approvals}/* endpoints can serve
		// queries and approve/reject decisions through the audit hook
		// pipeline (ApprovalGateHook → approval_queue).
		approvalTimeout := sessionAuditApprovalTimeoutFromEnv()
		approvalMgr = sessionaudit.NewApprovalManager(dbConn.Pool(), approvalTimeout)
		adminHandler.SetApprovalManager(approvalMgr)
		slog.Info("session audit approval manager wired",
			"timeout", approvalTimeout.String())

		// 2026-06-28: 在 v1 ChatHandler 集成 session-audit hook。
		// 之前 handoff 修复 G 只写到 cmd/gateway-v2/main.go（demo binary），
		// 184 生产跑的 v1 完全没有 chat-time hook — 补这个。
		// env LLM_GATEWAY_ENABLE_SESSION_AUDIT 控制是否启用 (默认 true)。
		enableSessionAudit := os.Getenv("LLM_GATEWAY_ENABLE_SESSION_AUDIT")
		if enableSessionAudit == "" {
			enableSessionAudit = "true" // 默认启用
		}
		if enableSessionAudit == "true" {
			auditDetector := sessionaudit.NewFastDetector(sessionaudit.DefaultDetectorConfig())
			auditBus := eventbus.NewMemoryBus(100)
			auditHook := sessionaudithook.NewSessionAuditHookV1(auditDetector, auditBus, approvalMgr)

			// 初始化审批通知器（从 DB 加载路由规则 + 创建 IM 渠道）
			// 2026-07-09: 多返回 larkCh 以便 feishubot 模块复用。
			if dbConn != nil && dbConn.Enabled() {
				notifier, lc, nerr := initApprovalNotifier(dbConn.Pool(), approvalMgr)
				if nerr != nil {
					slog.Error("init approval notifier failed", "error", nerr)
				} else if notifier != nil {
					auditHook.SetNotifier(notifier)
					slog.Info("approval notifier initialized and injected to audit hook")
					gLarkCh = lc
				}
			}
			gAuditBus = auditBus
			gApprovalMgr = approvalMgr

			chatHandler.SetSessionAuditHook(auditHook)
			slog.Info("session audit chat-time hook wired (v1)",
				"approval_timeout", approvalTimeout.String())
		} else {
			slog.Info("session audit chat-time hook disabled by env",
				"env", "LLM_GATEWAY_ENABLE_SESSION_AUDIT="+enableSessionAudit)
		}

		// 2026-07-03: approval resume handler wiring.
		//
		// 审批触发由现有 ApprovalGateHook (pipeline priority 105) 完成。
		// 此处只创建 ApprovalResumeHandler——管理员 approve 后，
		// POST /api/admin/approvals/:id/resume 从 DB record 中的 snapshot
		// 恢复 LLM 调用。
		if enableSessionAudit == "true" && scCache != nil && pendingStore != nil {
			resumeHandler, err := NewApprovalResumeHandler(
				scCache, approvalMgr, chatHandler, pendingStore, approvalTimeout)
			if err != nil {
				slog.Error("approval resume handler init failed", "error", err)
			} else {
				adminHandler.SetApprovalResumeHandler(resumeHandler)
				slog.Info("approval resume handler wired")
			}
		} else {
			slog.Info("approval resume handler skipped: missing dependencies",
				"session_audit_enabled", enableSessionAudit == "true",
				"scCache", scCache != nil,
				"pendingStore", pendingStore != nil)
		}

		// 2026-07-03: Tool Execution Tracking (migration 134)
		// 记录工具调用的生命周期（start/success/error/timeout），统计 P50/P95/P99 延迟。
		// 依赖：migration 134 (tool_executions, tool_usage_stats 表)
		var toolExecTracking *ToolExecutionTrackingComponents
		if dbConn != nil && dbConn.Enabled() && dbConn.Stdlib() != nil {
			var err error
			toolExecTracking, err = InitializeToolExecutionTracking(dbConn.Stdlib(), nil)
			if err != nil {
				slog.Error("tool execution tracking init failed", "error", err)
			} else if err := ValidateToolExecutionTracking(toolExecTracking); err != nil {
				slog.Error("tool execution tracking validation failed", "error", err)
			} else {
				slog.Info("tool execution tracking initialized successfully",
					"store", "postgres",
					"hook", toolExecTracking.Hook.Name(),
					"priority", toolExecTracking.Hook.Priority())
			}
		} else {
			slog.Info("tool execution tracking skipped: database disabled or Stdlib unavailable")
		}

		// settings-management: inject the DB-backed settings store so the
		// /api/admin/settings/* endpoints can read/write settings_kv.
		adminHandler.SetSettingsStore(settings.NewStoreDB(dbConn.Pool()))

		// 2026-07-03: wire the live request stream SSE hub into the
		// admin mux. The hub is created earlier in this file (so the
		// telemetry client can publish into it before adminHandler is
		// fully configured); this just registers the route.
		if liveStreamHub != nil {
			adminHandler.SetLiveStreamSSE(liveStreamHub)
		}

		// SystemMonitor SSE uses the same Redis client as the live request
		// stream. Mount it independently of the worker feature flag so legacy
		// probe workers can still feed the dashboard lane through telemetry.
		var systemMonitorSSE *admin.SystemMonitorSSEHub
		if fpSlotRedis != nil {
			systemMonitorSSE = admin.NewSystemMonitorSSEHub(fpSlotRedis)
			adminHandler.SetSystemMonitorSSE(systemMonitorSSE)
			if telemetryClient != nil {
				telemetryClient.AddOnRequestLogPersisted(func(entry *telemetry.RequestLogEntry) {
					// Only legacy probe rows belong in the system-monitor lane;
					// ordinary business requests stay in live-stream SSE.
					if entry == nil || entry.TaskType == nil || *entry.TaskType != "probe_triggered" {
						return
					}
					model := ""
					if entry.ClientModel != nil {
						model = *entry.ClientModel
					} else if entry.OutboundModel != nil {
						model = *entry.OutboundModel
					}
					status := ""
					if entry.RequestStatus != nil {
						status = *entry.RequestStatus
					}
					var credentialID, providerID int64
					if entry.CredentialID != nil {
						credentialID = int64(*entry.CredentialID)
					}
					if entry.ProviderID != nil {
						providerID = int64(*entry.ProviderID)
					}
					timestamp := time.Now().UTC()
					if entry.EventAt != nil {
						timestamp = entry.EventAt.UTC()
					}
					systemMonitorSSE.PublishTelemetry(admin.SystemMonitorTelemetry{
						RequestID: entry.RequestID, Model: model, Source: "legacy_worker",
						Status: status, CredentialID: credentialID, ProviderID: providerID,
						PromptTokens: valueOrZero(entry.PromptTokens), CompletionTokens: valueOrZero(entry.CompletionTokens),
						ErrorCode: valueOrEmpty(entry.ErrorKind), Timestamp: timestamp,
						IsInsert: entry.Op == telemetry.RequestLogInsert,
					})
				})
			}
		}

		// 2026-07-23: 系统监测模块 — 探测任务的唯一入口 (design docs/会话优化v2/32).
		// env LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true 才会启；Phase 1 默认关。
		// 旧 worker (NodeProbe / ActiveProbe / CredentialSelfcheck) 在启用
		// 后仍保留双写兼容；Phase 2 才切流。
		if os.Getenv("LLM_GATEWAY_SYSTEM_MONITOR_ENABLED") == "true" {
			smConcurrency := 5
			if v := os.Getenv("LLM_GATEWAY_SYSTEM_MONITOR_WORKERS_PER_NODE"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					smConcurrency = n
				}
			}
			sm, smErr := systemmonitor.NewSystemMonitor(systemmonitor.Config{
				DB:          dbConn.Pool(),
				Redis:       fpSlotRedis,
				Keyring:     nil,
				EncKey:      fernetKey,
				ProxyFunc:   upClient.Proxy().ProxyFunc(),
				TimeoutMs:   30000,
				Concurrency: smConcurrency,
				WorkerCount: smConcurrency,
			})
			if smErr != nil {
				slog.Error("system_monitor: construct failed", "error", smErr)
			} else {
				sm.Start(context.Background())
				adminHandler.SetSystemMonitor(newSystemMonitorAdapter(sm))
				slog.Info("system_monitor: started",
					"worker_count", smConcurrency,
					"redis_enabled", fpSlotRedis != nil,
				)
				// SSE 流：仅在 Redis 可用时挂载
				if systemMonitorSSE != nil {
					slog.Info("system_monitor sse hub: started")
				}
				// 5min 自动跳过规则需要 request_logs 行触发 RecentSuccessHook；
				// 挂到 telemetryClient.AddOnRequestLogPersisted。
				if telemetryClient != nil {
					hook := systemmonitor.NewRecentSuccessHook(sm.Dedup())
					telemetryClient.AddOnRequestLogPersisted(hook.Hook())
					slog.Info("system_monitor: recent_success hook wired to telemetry")
				}
			}
		}

		// ── 2026-07-17: 请求链路追踪查看 API ──────────────────────────────
		// 把 trace viewer 端点挂到 admin handler。复用现有 redis + db 连接。
		var traceRDB *redis.Client
		if redisClientForCache != nil {
			traceRDB = redisClientForCache.Client()
		}
		var traceDB *pgxpool.Pool
		if dbConn != nil && dbConn.Enabled() {
			traceDB = dbConn.Pool()
		}
		traceHandler := admin.NewRequestTraceHandler(traceRDB, traceDB)
		if traceHandler != nil {
			adminHandler.SetRequestTraceHandler(traceHandler)
			slog.Info("admin_handler: request_trace wired",
				"redis_connected", traceRDB != nil,
				"db_connected", traceDB != nil)
		}

		slog.Info("CHECKPOINT: before modelPolicy check")
		// model-policy: share the same Checker instance with the
		// relay ChatHandler so admin writes can invalidate the
		// per-tenant cache entry immediately (Round 48).
		if modelPolicy != nil {
			adminHandler.SetModelPolicy(modelPolicy)
		}

		// 2026-07-09: Wire Memora DLQ into admin handler
		// TODO: enable when memorySvc.DLQ() and adminHandler.SetDLQ are implemented
		slog.Debug("admin handler DLQ: skipped (not yet implemented)")

		slog.Info("CHECKPOINT: before EnsureUsersTable")
		// Ensure users table exists for multi-tenant admin auth
		migCtx, migCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := dbConn.EnsureUsersTable(migCtx); err != nil {
			slog.Error("failed to ensure users table", "error", err)
		}
		migCancel()

		slog.Info("CHECKPOINT: after EnsureUsersTable")
		// Seed initial admin user if table is empty
		admin.EnsureSeedAdmin(dbConn.Pool())

		slog.Info("CHECKPOINT: after EnsureSeedAdmin")

		// 2026-07-13: 启动 hot 表夜间自动迁移 cron。
		// 配置来源：环境变量 HOT_CRON_RUN_AT（"HH:MM"，默认 02:00），
		// HOT_CRON_DISABLED=1 可关掉；保留时长 HOT_CRON_RETENTION_HOURS（默认 24）。
		hotCronCfg := admin.HotCronConfigFromEnv()
		if hotCronCfg.Enabled {
			hotCron := admin.NewHotCronScheduler(adminHandler, hotCronCfg)
			// 用 shutdown root ctx（<-ctx.Done() 之后整个进程退出），所以这里
			// 直接用 adminHandler 生命周期内的 ctx 即可，shutdown 时显式 Stop。
			hotCron.Start(context.Background())
			adminHandler.SetHotCron(hotCron)
			defer hotCron.Stop()
			slog.Info("hot cron started via env config",
				"run_at", fmt.Sprintf("%02d:%02d", hotCronCfg.RunAtHour, hotCronCfg.RunAtMinute),
				"retention_hours", hotCronCfg.RetentionHours)
		} else {
			slog.Info("hot cron disabled via HOT_CRON_DISABLED env")
		}

		// Seed providers asynchronously to avoid blocking HTTP server startup (2026-06-22)
		go func() {
			seedCtx, seedCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer seedCancel()
			if created, err := admin.SeedProvidersFromCatalog(seedCtx, dbConn.Pool()); err != nil {
				slog.Warn("provider catalog seed failed", "error", err)
			} else if created > 0 {
				slog.Info("seeded providers from catalog", "created", created)
			}
		}()
	}

	slog.Info("CHECKPOINT: after admin handler init block")

	// Auto session title generator (2026-06-22).
	// Wire the admin handler's auto title generator into the chat handler
	// so it can trigger title generation after the first successful request.
	if adminHandler != nil {
		autoTitleGen := adminHandler.GetAutoTitleGenerator()
		if autoTitleGen != nil {
			chatHandler.SetAutoTitleGenerator(autoTitleGen)
			slog.Info("auto session title generator wired (async, fire-and-forget)")
		}
	}

	// ── Route incident diagnosis (2026-07-13, Phase 1 read-only) ───────
	// Wires the route-incident store, observer, read-only API, and the
	// SSE `incident_update` envelope publication. The store requires
	// a database; the SSE hub is required for the live publish path.
	// Either being nil degrades gracefully (the diagnose entry is
	// hidden, the API returns 503, the observer drops its events).
	// The observer is wired to telemetry.AddOnRequestLogPersisted (NOT
	// the "Emitted" hook) so it only runs on rows that are already
	// durable in request_logs.
	if adminHandler != nil && dbConn != nil && dbConn.Enabled() {
		incidentStore := routeincident.NewStore(dbConn.Pool())
		incidentHandler := admin.NewRouteIncidentsHandler(incidentStore, dbConn.Pool())
		adminHandler.SetRouteIncidentsHandler(incidentHandler)

		if telemetryClient.Enabled() {
			publishFn := newIncidentPublishFn(liveStreamHub)
			incidentObserver := routeincident.NewObserver(incidentStore, routeincident.ObserverConfig{
				QueueSize:    1024,
				MaxRetries:   4,
				RetryBackoff: 200 * time.Millisecond,
				MaxBackoff:   5 * time.Second,
				Publish:      publishFn,
			})
			if incidentObserver != nil {
				incidentObserver.Start(context.Background())
				telemetryClient.AddOnRequestLogPersisted(incidentObserver.AsHook())
				slog.Info("route incident observer enabled (telemetry onPersisted → store → SSE incident_update)")
			}
		}
	}

	// ── Background Services ─────────────────────────────────────────────
	var credRecovery *bg.CredentialRecovery
	var brokenProbeReviver *bg.BrokenProbeReviver
	var credCycler *bg.CredentialCycler
	var credProbeV2 *bg.CredentialProbeV2
	var pendingSweeper *bg.PendingSweeper
	var candidateFailureMonitor *bg.CandidateFailureMonitor

	slog.Info("CHECKPOINT: before bg services init")
	var defaultProbePicker *bg.DefaultProbePicker

	// Health tracking workers (2026-06-22)
	var callHistoryAggregator *bg.CallHistoryAggregator
	var concurrencyAutoScaleUp *bg.ConcurrencyAutoScaleUp
	var healthAutoRecover *bg.HealthAutoRecover
	var autoRouteListener *bg.AutoRouteRealtimeListener
	// v7 (2026-06-28): Unified probe scheduler replaces modelProbe + suspiciousProbe
	var unifiedProbe *bg.UnifiedProbeScheduler
	var modelProbe *bg.ModelProbeRunner           // TODO: remove after unifiedProbe validation
	var suspiciousProbe *bg.SuspiciousProbeRunner // TODO: remove after unifiedProbe validation
	var modelAvailabilityCache *bg.ModelAvailabilityCache
	var modelAvailabilityReader *bg.ModelAvailabilityReader
	var modelAvailabilityBackfill *bg.AvailabilityCacheBackfill
	var modelAvailabilityKeyCounter *bg.AvailabilityKeyCounter
	var passiveProbe *bg.PassiveProbeListener
	var activeProbe *bg.ActiveProbeWorker // 2026-07-13: 错误触发的主动探测
	var probeQueueWorker *bg.ProbeQueueWorker
	// 2026-07-14: 30s system-health monitor (GDRT H badge).
	var systemHealthWorker *bg.SystemHealthWorker
	var stickyCleaner *bg.StickyCleaner
	var envelopeCleaner *bg.EnvelopeCleaner
	var settingsAuditCleaner *bg.SettingsAuditCleaner
	var taxonomySync *bg.TaxonomySync
	var partitionManager *bg.PartitionManager
	var selfCheckWorker *bg.SelfCheckWorker
	// Provider Profile System (Phase 1, 2026-07-26)
	var profileWorkers *ProviderProfileWorkers
	// peakCollector / weeklyPeakRollup / slotSuggester are declared
	// at the top of main() so the executor can reference them.

	// Phase 3.7 (A3-1): apihub.Service is used both inside the
	// dbConn-enabled init block (for the AssetWatcher) and outside
	// (for the Agent Registry API routes registered in the router
	// section below). Declaring it at the outer scope avoids the
	// "undefined: apihubSvc" scoping bug at line ~1346.
	var apihubSvc *apihub.Service

	if dbConn != nil && dbConn.Enabled() {
		slog.Info("CHECKPOINT: inside bg services enabled block")
		credRecovery = bg.NewCredentialRecovery(dbConn.Pool())
		credRecovery.Start(context.Background())
		slog.Info("CHECKPOINT: credRecovery started")
		brokenProbeReviver = bg.NewBrokenProbeReviver(dbConn.Pool(), 0, 0)
		brokenProbeReviver.Start(context.Background())
		slog.Info("CHECKPOINT: brokenProbeReviver started")

		// Routing health checker — runs diagnostic checks every 15 min,
		// persists findings to routing_health_checks for admin review.
		routingHealthChecker := bg.NewRoutingHealthChecker(dbConn.Pool())
		routingHealthChecker.Start(context.Background())
		slog.Info("CHECKPOINT: routingHealthChecker started")

		// Provider Profile System (Phase 1, 2026-07-26)
		// Monitors provider quality across 7 dimensions with automated collection,
		// aggregation, and scoring. Feature-flagged via provider_profile.enabled.
		profileWorkers = initProviderProfile(dbConn.Pool(), fernetKey, keyring)
		if profileWorkers != nil {
			slog.Info("CHECKPOINT: provider profile system started")
		}

		// Self-check worker — runs periodic ping + tool-call smoke tests
		// against key models to verify gateway availability (2026-07-12).
		slog.Info("CHECKPOINT: before self-check worker init")

		// Try env var first, then generate system key
		selfCheckAPIKey := os.Getenv("LLM_GATEWAY_SELF_CHECK_API_KEY")
		if selfCheckAPIKey == "" {
			var err error
			selfCheckAPIKey, err = bg.EnsureSystemAPIKey(context.Background(), dbConn.Pool(), fernetKey, keyring, cfg.SecretKey)
			if err != nil {
				slog.Warn("self-check worker disabled: cannot get system API key", "error", err)
				selfCheckAPIKey = ""
			}
		}

		if selfCheckAPIKey != "" {
			// 2026-07-18: add explicit gate for legacy featured-model self-check.
			// The legacy worker (1-min tick, 3-model pool) is now DOUBLE-GATED:
			// 1. LLM_GATEWAY_USE_NEW_PROBE_MODE must be false (rollback mode)
			// 2. LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK must be explicitly "true"
			// This prevents accidental re-activation when rolling back the probe mode.
			// The new bg/credential_selfcheck.go handles per-credential daily checks.
			if useNewProbeMode() {
				slog.Info("selfCheckWorker (legacy featured) skipped: LLM_GATEWAY_USE_NEW_PROBE_MODE=true")
			} else if strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK"))) != "true" {
				slog.Info("selfCheckWorker (legacy featured) skipped: LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK != true (explicit opt-in required)")
			} else {
				// Use empty baseURL to trigger env var / default detection in NewSelfCheckWorker
				selfCheckWorker = bg.NewSelfCheckWorker(dbConn.Pool(), selfCheckAPIKey, "", keyring)
				selfCheckWorker.Start(context.Background())
				slog.Info("CHECKPOINT: selfCheckWorker started (LEGACY MODE - explicit opt-in)", "api_key_source", func() string {
					if os.Getenv("LLM_GATEWAY_SELF_CHECK_API_KEY") != "" {
						return "env_var"
					}
					return "generated"
				}())
			}
		}

		// Track C C6 (2026-06-18): pending entry sweeper. Marks
		// abandoned in_progress entries (e.g. a crashed async
		// goroutine, a client that never polls) as failed so
		// the GET endpoint can return a terminal response.
		// Default 10m stale / 60s interval; override via env.
		slog.Info("CHECKPOINT: before pendingStore check", "pendingStore", pendingStore != nil)
		if pendingStore != nil {
			slog.Info("CHECKPOINT: inside pendingStore block")
			pendingStaleTimeout := 10 * time.Minute
			pendingSweepInterval := 60 * time.Second
			if v := os.Getenv("LLM_GATEWAY_PENDING_STALE_TIMEOUT"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					pendingStaleTimeout = time.Duration(n) * time.Second
				}
			}
			if v := os.Getenv("LLM_GATEWAY_PENDING_SWEEP_INTERVAL"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					pendingSweepInterval = time.Duration(n) * time.Second
				}
			}
			slog.Info("CHECKPOINT: before NewPendingSweeper")
			pendingSweeper = bg.NewPendingSweeper(pendingStore, pendingStaleTimeout, pendingSweepInterval)
			slog.Info("CHECKPOINT: before pendingSweeper.Start")
			pendingSweeper.Start(context.Background())
			slog.Info("CHECKPOINT: after pendingSweeper.Start")
		}
		slog.Info("CHECKPOINT: after pendingStore block")
		slog.Info("CHECKPOINT: before credCycler check", "bgDataPlaneOnly", bgDataPlaneOnly, "fernetKey", fernetKey != nil)
		if !bgDataPlaneOnly && fernetKey != nil {
			slog.Info("CHECKPOINT: before NewCredentialCycler")
			credCycler = bg.NewCredentialCycler(dbConn.Pool(), fernetKey)
			if keyring != nil {
				credCycler.SetKeyring(keyring)
			}
			slog.Info("CHECKPOINT: before credCycler.Start")
			credCycler.Start(context.Background())
			slog.Info("CHECKPOINT: after credCycler.Start")
		} else if bgDataPlaneOnly {
			slog.Info("credential cycler skipped (bg_mode=data-plane)")
		}

		slog.Info("CHECKPOINT: after credCycler block")

		// 900-series: v2 mini-chat probe (spec §5) — independent of v1 cycler
		slog.Info("CHECKPOINT: before credProbeV2 block", "bgDataPlaneOnly", bgDataPlaneOnly)
		if !bgDataPlaneOnly {
			slog.Info("CHECKPOINT: before NewCredentialProbeV2")
			credProbeV2 = bg.NewCredentialProbeV2(dbConn.Pool(), fernetKey)
			if keyring != nil {
				credProbeV2.SetKeyring(keyring)
			}
			credProbeV2.SetAvailabilityCache(modelAvailabilityCache)
			// 2026-06-30: wire state manager so probe results update
			// the real-time state cache immediately.
			if stateManager != nil {
				credProbeV2.SetStateManager(stateManager)
			}
			slog.Info("CHECKPOINT: before credProbeV2.Start")
			if useNewProbeMode() {
				slog.Info("credProbeV2 (legacy 1h) skipped: LLM_GATEWAY_USE_NEW_PROBE_MODE=true")
			} else {
				credProbeV2.Start(context.Background())
			}
			slog.Info("CHECKPOINT: after credProbeV2.Start")

			// 900-series: default probe model picker (spec §4.2.1) — daily 0:00
			slog.Info("CHECKPOINT: before NewDefaultProbePicker")
			defaultProbePicker = bg.NewDefaultProbePicker(dbConn.Pool())
			slog.Info("CHECKPOINT: before defaultProbePicker.Start")
			defaultProbePicker.Start(context.Background())
			slog.Info("CHECKPOINT: after defaultProbePicker.Start")

			// 2026-06-18: per-model re-probe of failing bindings.  Runs
			// every 10 minutes; flips the binding back to routable as
			// soon as the upstream issue clears, but never overwrites
			// manual_disable.
			slog.Info("CHECKPOINT: before NewModelProbeRunner")
			modelProbe = bg.NewModelProbeRunner(dbConn.Pool(), fernetKey)
			if keyring != nil {
				modelProbe.SetKeyring(keyring)
			}
			modelProbe.SetAvailabilityCache(modelAvailabilityCache)
			slog.Info("CHECKPOINT: before modelProbe.Start")
			if useNewProbeMode() {
				slog.Info("modelProbe (legacy 5min consensus) skipped: LLM_GATEWAY_USE_NEW_PROBE_MODE=true")
			} else {
				modelProbe.Start(context.Background())
			}
			slog.Info("CHECKPOINT: after modelProbe.Start")

			// 2026-06-28 收口：当前 unified scheduler 与旧 probe 体系并行写
			// model_probe_state，会导致重复探测和状态覆盖。默认关闭，待
			// 单一 writer + Redis 状态层完全接管后再开启。
			//
			// 2026-06-29 状态更新：bg/unified_probe_scheduler.go 顶部已标记为
			// DEPRECATED / DEAD BRANCH。这个分支的实现：
			//   - 用 "healthy/failing/probing" 状态名（与系统其他地方的
			//     "healthy_confirmed/broken_confirmed/recovering/unknown/suspicious"
			//     冲突，迁移过程会损坏其他 reader）
			//   - 单次失败就标 binding 不可用，与 consensus 三次失败语义冲突
			//   - 写 binding 时 raw_model_name + LIMIT 1 会跨 provider 写错
			//   - 多 worker 并行写同一行
			// 在以上问题全部修复、并且有迁移计划把现有 state 名字对齐之前，
			// 不要打开这个分支。当前的 ModelProbeRunner + CredentialProbeV2 +
			// PassiveProbeListener 已经是单一 writer（见 bg/model_probe.go
			// 顶部状态机说明），Redis availability cache 已经在多个 admin
			// 端点消费。
			if os.Getenv("LLM_GATEWAY_ENABLE_UNIFIED_PROBE_SCHEDULER") == "true" {
				slog.Warn("LLM_GATEWAY_ENABLE_UNIFIED_PROBE_SCHEDULER is set but the scheduler is DEPRECATED; ignoring")
			} else {
				slog.Info("unified probe scheduler is disabled (DEPRECATED — see bg/unified_probe_scheduler.go header)")
			}

			// TODO: After validation, remove the old probe runners:
			// - modelProbe (bg.NewModelProbeRunner)
			// - suspiciousProbe (bg.NewSuspiciousProbeRunner)
			// Keep them for now for comparison/rollback safety.

			// v6 (2026-06-22): Layer 5 passive probe observer.
			// Scans request_logs every 30s for failures, promotes to
			// reviewing, and after the 5-min observation window resolves:
			// still-failing → mark unreachable; recovered → clear.
			// stateWriter lets it write availability_state='unreachable'.
			slog.Info("CHECKPOINT: before NewPassiveProbeListener")
			passiveProbe = bg.NewPassiveProbeListener(dbConn.Pool(), credential.NewWriter(dbConn.Pool()))
			passiveProbe.SetAvailabilityCache(modelAvailabilityCache)
			slog.Info("CHECKPOINT: before passiveProbe.Start")
			if useNewProbeMode() {
				slog.Info("passiveProbe (legacy 30s request_logs scan) skipped: LLM_GATEWAY_USE_NEW_PROBE_MODE=true")
			} else {
				passiveProbe.Start(context.Background())
			}
			slog.Info("CHECKPOINT: after passiveProbe.Start")
		}
		slog.Info("CHECKPOINT: after probe workers block")

		// 2026-07-13: 错误触发的主动探测 (active_probe)。
		// 连续失败 ≥2 次 → 立即直连上游探测，按 5s → 30s → 2m → 5m → 15m backoff
		// 多轮执行，每轮结果写 request_logs（task_type='probe_triggered'），
		// 自动出现在实时请求流中。
		if !bgDataPlaneOnly && dbConn != nil && dbConn.Enabled() {
			slog.Info("CHECKPOINT: before NewActiveProbeWorker")
			epEnabled := true
			epThreshold := 2
			epMaxAttempts := 5
			epTimeoutMs := 30000
			epWorkers := 1
			if envStr := os.Getenv("LLM_GATEWAY_ERROR_PROBE_ENABLED"); envStr == "false" || envStr == "0" {
				epEnabled = false
			}
			if envStr := os.Getenv("LLM_GATEWAY_ERROR_PROBE_CONSECUTIVE_THRESHOLD"); envStr != "" {
				if n, err := strconv.Atoi(envStr); err == nil && n > 0 {
					epThreshold = n
				}
			}
			if envStr := os.Getenv("LLM_GATEWAY_ERROR_PROBE_MAX_ATTEMPTS"); envStr != "" {
				if n, err := strconv.Atoi(envStr); err == nil && n > 0 {
					epMaxAttempts = n
				}
			}
			if envStr := os.Getenv("LLM_GATEWAY_ERROR_PROBE_TIMEOUT_MS"); envStr != "" {
				if n, err := strconv.Atoi(envStr); err == nil && n > 0 {
					epTimeoutMs = n
				}
			}
			if envStr := os.Getenv("LLM_GATEWAY_ERROR_PROBE_WORKERS"); envStr != "" {
				if n, err := strconv.Atoi(envStr); err == nil && n > 0 {
					epWorkers = n
				}
			}
			activeProbe = bg.NewActiveProbeWorker(bg.ActiveProbeWorkerConfig{
				DB:                   dbConn.Pool(),
				Keyring:              keyring,
				EncKey:               fernetKey,
				Telemetry:            telemetryClient,
				StateManager:         stateManager,
				Enabled:              epEnabled,
				ConsecutiveThreshold: epThreshold,
				MaxAttempts:          epMaxAttempts,
				TimeoutMs:            epTimeoutMs,
				Workers:              epWorkers,
			})
			slog.Info("CHECKPOINT: before activeProbe.Start")
			if stateManager != nil {
				activeProbe.SetStateManager(stateManager)
			}
			if useNewProbeMode() {
				slog.Info("activeProbe (legacy error-triggered) skipped: LLM_GATEWAY_USE_NEW_PROBE_MODE=true")
			} else {
				activeProbe.Start(context.Background())
			}
			if os.Getenv("LLM_GATEWAY_PROBE_QUEUE_ENABLED") == "true" {
				queueExecutor := bg.NewActiveProbeExecutor(dbConn.Pool(), keyring, fernetKey, epTimeoutMs)
				probeQueueWorker = bg.NewProbeQueueWorker(bg.ProbeQueueWorkerConfig{
					Queue:        bg.NewProbeQueue(dbConn.Pool()),
					Executor:     queueExecutor,
					Emitter:      bg.NewActiveProbeEmitter(telemetryClient),
					BatchSize:    epWorkers,
					Workers:      epWorkers,
					Lease:        30 * time.Second,
					PollInterval: 250 * time.Millisecond,
				})
				probeQueueWorker.Start(context.Background())
				slog.Info("durable probe queue worker started", "workers", epWorkers)
			}
			slog.Info("CHECKPOINT: after activeProbe.Start")
		}

		// 2026-06-30: Start the credential-state manager AFTER probe
		// services have been wired. The manager watches for state changes
		// from probes, requests, and admin actions, and triggers fast
		// re-probes when consecutive failures exceed threshold.
		if stateManager != nil {
			// Wire the fast-reprobe submitters so UpdateOnFailure can
			// trigger immediate verification after threshold breaches.
			if credProbeV2 != nil {
				stateManager.SetProbeSubmitter(
					credProbeV2.ProbeNowAsync,
					func(ctx context.Context, credID int, model string) error {
						if modelProbe != nil {
							return modelProbe.TriggerManual(ctx, credID, model)
						}
						return nil
					},
				)
			}
			// 2026-07-03: Bug #8 fix - wire candidate cache invalidation
			stateManager.SetInvalidateCandidateCache(provider.InvalidateCandidateCacheForCredential)
			// 2026-07-13: wire active_probe submitter so consecutive_fails >= threshold
			// immediately triggers a direct-to-provider probe (instead of waiting
			// for credProbeV2's 5-min delayed reprobe).
			if activeProbe != nil {
				activeProbeThresh := 2
				if envStr := os.Getenv("LLM_GATEWAY_ERROR_PROBE_CONSECUTIVE_THRESHOLD"); envStr != "" {
					if n, err := strconv.Atoi(envStr); err == nil && n > 0 {
						activeProbeThresh = n
					}
				}
				stateManager.SetActiveProbeSubmitter(activeProbe.Submit, activeProbeThresh)
				slog.Info("credstate: active_probe submitter wired",
					"consecutive_threshold", activeProbeThresh)
			}
			stateManager.Start(context.Background())
			slog.Info("credential state manager started")

			// 2026-07-14: launch the new probe workers from the
			// spec rewrite.  They replace the legacy selfCheck /
			// credProbeV2 / modelProbe / passiveProbe / activeProbe
			// stack, all of which are gated by useNewProbeMode() above.
			if useNewProbeMode() {
				// A. credential_selfcheck — 24h/cred daily check
				// (uses the same system api key as the legacy worker).
				credSelfcheck := bg.NewCredentialSelfcheckWorker(dbConn.Pool(), selfCheckAPIKey, "")
				credSelfcheck.Start(context.Background())
				slog.Info("CHECKPOINT: credential_selfcheck_worker started")

				// B. node_probe — error-triggered 5s/30s/60s/5m/1h/2h/24h
				// backoff, direct + gateway two rounds.
				nodeProbe := bg.NewNodeProbeWorker(dbConn.Pool(), fernetKey, keyring, selfCheckAPIKey, "", upClient.Proxy().ProxyFunc())
				nodeProbe.SetStateObserver(stateManager)
				nodeProbe.SetStateProvider(stateManager)
				nodeProbe.SetEmitter(bg.NewActiveProbeEmitter(telemetryClient))
				nodeProbe.SetInvalidateCandidateCache(provider.InvalidateCandidateCacheForCredential)
				if routingExec != nil && routingExec.Circuit != nil {
					nodeProbe.SetCircuitRecovery(routingExec.Circuit.RecordSuccess)
				}
				// 2026-07-17: 同步探测 hold 模式开关。env LLM_GATEWAY_SYNC_NO_CANDIDATE_PROBE
				// 取值 "0"/"false"/"off" 即关闭（默认开启）。关闭时 executor 走原 fire-and-forget
				// 路径，503 立即返回。该 kill-switch 用于紧急回滚，无需重新打包。
				if routingExec != nil {
					syncOn := !envBoolOff("LLM_GATEWAY_SYNC_NO_CANDIDATE_PROBE")
					routingExec.SyncNoCandidateProbe = syncOn
					routingExec.SyncNoCandidateTimeout = 5 * time.Second
					routingExec.ProbeSync = nodeProbe.ProbeSync
					routingExec.NodeProbeHealthy = func(ctx context.Context, credentialID int, rawModel string) error {
						return bg.MarkNodeProbeHealthy(ctx, dbConn.Pool(), credentialID, rawModel)
					}
					slog.Info("sync_no_candidate_probe", "enabled", syncOn, "timeout", routingExec.SyncNoCandidateTimeout)
				}

				nodeProbe.Start(context.Background())
				slog.Info("CHECKPOINT: node_probe_worker started")

				dailyProbeAudit := bg.NewDailyProbeAudit(dbConn.Pool(), nodeProbe)
				dailyProbeAudit.Start(context.Background())
				slog.Info("CHECKPOINT: daily_probe_audit started")
				// Wire stateManager → node_probe so consecutive
				// failures >= threshold trigger the new path
				// (replaces the legacy active_probe wiring above).
				stateManager.SetActiveProbeSubmitter(nodeProbe.Submit, 2)
				slog.Info("credstate: node_probe submitter wired",
					"consecutive_threshold", 2)

				// 2026-07-24 P0 fix: bg/credential_recovery's 60s tick
				// scans cmb.available=FALSE rows whose unavailable_recover_at
				// has elapsed and hands them to NodeProbeWorker. Without
				// this wiring those rows stay excluded from routing until
				// an operator manually clears and re-fetches the model list.
				// The probe worker is the authoritative writer of cmb.available
				// — it only flips when direct + gateway probe rounds succeed.
				if credRecovery != nil {
					credRecovery.SetProbeSubmitter(func(credID int, model string) {
						nodeProbe.Submit(credID, model, "default", "expired-binding-recovery")
					})
					credRecovery.SetInvalidateCandidateCache(provider.InvalidateCandidateCacheForCredential)
					slog.Info("credRecovery: expired-binding probe submitter wired")
				}

			}
			// C. system_health — 30s windowed success-rate monitor
			// for the GDRT H badge.  Runs unconditionally (outside
			// useNewProbeMode) so the homepage badge works even when
			// LLM_GATEWAY_USE_NEW_PROBE_MODE=false.
			systemHealthWorker = bg.NewSystemHealthWorker(dbConn.Pool())
			systemHealthWorker.Start(context.Background())
			slog.Info("CHECKPOINT: system_health_worker started")
		}

		slog.Info("CHECKPOINT: before NewStickyCleaner")
		stickyCleaner = bg.NewStickyCleaner(dbConn.Pool())
		slog.Info("CHECKPOINT: before stickyCleaner.Start")
		stickyCleaner.Start(context.Background())
		slog.Info("CHECKPOINT: after stickyCleaner.Start")

		// Partition manager: auto-creates monthly request_logs partitions
		// and archives 2+ months old data to columnar storage.
		// 2026-06-26: Added per storage-optimization plan.
		slog.Info("CHECKPOINT: before NewPartitionManager")
		partitionManager = bg.NewPartitionManager(dbConn.Pool(), 24*time.Hour)
		slog.Info("CHECKPOINT: before partitionManager.Start")
		partitionManager.Start(context.Background())
		slog.Info("CHECKPOINT: after partitionManager.Start")
		envelopeCleaner = bg.NewEnvelopeCleaner(dbConn.Pool())

		// settings-management: 7-day audit retention worker (Q6: C).
		slog.Info("CHECKPOINT: before NewSettingsAuditCleaner")
		settingsAuditCleaner := bg.NewSettingsAuditCleaner(dbConn.Pool())
		slog.Info("CHECKPOINT: before settingsAuditCleaner.Start")
		settingsAuditCleaner.Start(context.Background())
		slog.Info("CHECKPOINT: after settingsAuditCleaner.Start")
		envelopeCleaner.Start(context.Background())
		slog.Info("CHECKPOINT: after envelopeCleaner.Start")
		// 2026-07-05: wire the telemetry ingest worker so HTTP
		// /api/telemetry/request-log (and friends) actually persist
		// rows. Without this, the request handler queues into a
		// channel that no consumer ever drains.
		admin.StartIngester(dbConn.Pool())
		defer admin.StopIngester()
		slog.Info("CHECKPOINT: after StartIngester")
		// 2026-06-27: 启动审批超时扫描 worker。approvalMgr 在前面
		// 已通过 adminHandler.SetApprovalManager 注入；这里直接构造 worker
		// 并把 mgr 复用过去。
		if approvalMgr != nil {
			approvalTimeoutWorker := bg.NewApprovalTimeoutWorker(approvalMgr)
			approvalTimeoutWorker.Start(context.Background())
			defer approvalTimeoutWorker.Stop()
			slog.Info("approval timeout worker started")
		}
		if !bgDataPlaneOnly {
			taxonomySync = bg.NewTaxonomySync(dbConn.Pool(), "")
			taxonomySync.Start(context.Background())
		} else {
			slog.Info("taxonomy sync skipped (bg_mode=data-plane)")
		}

		// Peak concurrency tracking — runs in both full and data-plane
		// modes because it only needs read access to credentials.
		peakCollector = bg.NewConcurrencyPeakCollector(dbConn.Pool())
		peakCollector.Start(context.Background())

		// 2026-06-23 Phase 3 (P2): candidate_failure_monitor. Reads
		// candidate_failure_logs every minute, fires debounced alerts on
		// sustained failure patterns, and auto-cools credentials whose
		// recent failure ratio exceeds the configured threshold. Background
		// best-effort; failures here never affect request hot path.
		candidateFailureMonitor = bg.NewCandidateFailureMonitor(dbConn.Pool())
		candidateFailureMonitor.Start(context.Background())
		slog.Info("candidate_failure_monitor wired into main")

		// Health tracking workers (2026-06-22): sliding window aggregation,
		// auto-scaleup, and auto-recovery. Run in both modes.
		if fpSlotRedis != nil {
			modelAvailabilityCache = bg.NewModelAvailabilityCache(fpSlotRedis, 4*time.Hour)
			callHistoryAggregator = bg.NewCallHistoryAggregator(fpSlotRedis, dbConn.Pool(), 1*time.Minute)
			callHistoryAggregator.Start(context.Background())

			slog.Info("CHECKPOINT: before healthAutoRecover")
			healthAutoRecover = bg.NewHealthAutoRecover(dbConn.Pool(), 1*time.Minute)
			healthAutoRecover.Start(context.Background())
			slog.Info("CHECKPOINT: after healthAutoRecover.Start")

			// Wire the Redis availability reader so admin /api/admin/probe/cache-state
			// can serve cache-only views without touching PostgreSQL.
			if modelAvailabilityReader == nil {
				modelAvailabilityReader = bg.NewModelAvailabilityReader(fpSlotRedis)
			}

			// Periodic DB→Redis backfill so the cache recovers after Redis
			// flush / cold deploy. The on-demand trigger lives in
			// /api/admin/probe/cache-rebuild.
			if dbConn != nil && dbConn.Enabled() {
				modelAvailabilityBackfill = bg.NewAvailabilityCacheBackfill(
					dbConn.Pool(),
					modelAvailabilityCache,
					modelAvailabilityReader,
					bg.AvailabilityCacheBackfillConfig{},
				)
				if modelAvailabilityBackfill != nil {
					modelAvailabilityBackfill.Start(context.Background())
					defer modelAvailabilityBackfill.Stop()
				}
			}

			// Periodic SCAN-based key counter so the
			// llmgw_availability_keys_count gauge stays accurate even
			// after Redis failover or operator-initiated FLUSHDB.
			if fpSlotRedis != nil {
				modelAvailabilityKeyCounter = bg.NewAvailabilityKeyCounter(
					fpSlotRedis,
					5*time.Minute,
				)
				if modelAvailabilityKeyCounter != nil {
					modelAvailabilityKeyCounter.Start(context.Background())
					defer modelAvailabilityKeyCounter.Stop()
				}
			}
		}

		// Dashboard board cache: Redis online read path for /api/admin/dashboard/board.
		// Must run in both full and data-plane modes (245 uses bgDataPlaneOnly=true).
		if fpSlotRedis != nil && adminHandler != nil {
			statsBoardCache = boardcache.New(fpSlotRedis)
			statsBoardCache.SetBaselineBuilder(adminHandler.BuildBoardBaseline)
			statsBoardCache.Start(context.Background())
			adminHandler.SetBoardCache(statsBoardCache)
			if telemetryClient.Enabled() {
				telemetryClient.AddOnRequestLogPersisted(statsBoardCache.Record)
				slog.Info("boardcache wired (telemetry onPersisted)")
			}
			slog.Info("boardcache service started")

			// 2026-07-25: Body size tracker for real-time dashboard stats
			bodyTracker := stats.NewBodySizeTracker(fpSlotRedis)
			adminHandler.SetBodySizeTracker(bodyTracker)
			if telemetryClient.Enabled() {
				telemetryClient.AddOnRequestLogPersisted(bodyTracker.Record)
				slog.Info("body size tracker wired (telemetry onPersisted)")
			}
		}
		if dbConn.Pool() != nil && fpSlotRedis != nil && adminHandler != nil {
			blSvc := ipblocklist.NewService(dbConn.Pool(), fpSlotRedis)
			if err := blSvc.Warmup(context.Background()); err != nil {
				slog.Warn("ip blocklist warmup failed", "error", err)
			}
			adminHandler.SetIPBlocklist(blSvc)
			slog.Info("ip blocklist admin wired")
		}

		// Weekly rollup + auto-tune suggester require writes to
		// credentials/audit; only run in "full" mode.
		slog.Info("CHECKPOINT: before bgDataPlaneOnly check for weekly/auto-tune", "bgDataPlaneOnly", bgDataPlaneOnly)
		if !bgDataPlaneOnly {
			slog.Info("CHECKPOINT: inside auto-tune block")
			weeklyPeakRollup = bg.NewWeeklyPeakRollup(dbConn.Pool())
			weeklyPeakRollup.Start(context.Background())

			statsMinuteAccumulator = stats.NewMinuteAccumulator(dbConn.Pool())
			statsMinuteAccumulator.Start(context.Background())
			statsMinuteRollup = bg.NewStatsMinuteRollup(dbConn.Pool())
			statsMinuteRollup.Start(context.Background())
			if telemetryClient.Enabled() {
				telemetryClient.AddOnRequestLogPersisted(statsMinuteAccumulator.Record)
				slog.Info("stats minute accumulator wired (telemetry onPersisted)")
			}

			slog.Info("CHECKPOINT: after weeklyPeakRollup.Start")
			slotSuggester = bg.NewSlotSuggester(dbConn.Pool())
			slotSuggester.Start(context.Background())

			slog.Info("CHECKPOINT: after slotSuggester.Start")
			// Concurrency auto-scaleup (2026-06-22): increases limit for healthy high-load credentials
			concurrencyAutoScaleUp = bg.NewConcurrencyAutoScaleUp(dbConn.Pool(), 1*time.Hour)
			concurrencyAutoScaleUp.Start(context.Background())

			slog.Info("CHECKPOINT: after concurrencyAutoScaleUp.Start, before NewIndex")
			autoroute.InitFeatureFlags()
			autoIdx := autoroute.NewIndex()

			autoIdx.SetPool(dbConn.Pool())

			slog.Info("CHECKPOINT: before tuningStore.Reload")
			// v2.1: TuningStore provides dynamic keyword/threshold/weight
			// overrides from the tuning_params table. Reloaded on a 5-min
			// ticker aligned with auto_index_refresher. Falls back to
			// compiled defaults when the DB is empty (already seeded in
			// db.ensureTuningParamsSchema).
			tuningStore := autoroute.NewTuningStore(dbConn.Pool())
			if err := tuningStore.Reload(context.Background()); err != nil {
				slog.Warn("tuning_store initial reload failed, using defaults", "error", err)
			}
			slog.Info("CHECKPOINT: after tuningStore.Reload")
			tuningRefresher := bg.NewTuningStoreRefresher(tuningStore, dbConn.Pool())
			tuningRefresher.Start(context.Background())

			classifier := autoroute.NewHeuristicClassifierWithTuning(
				autoroute.DefaultHeuristicThresholds(),
				autoroute.DefaultKeywords(),
				tuningStore,
			)
			decider := autoroute.NewDecider(
				classifier,
				// v2.1: LLM fallback classifier. Default uses
				// DisabledCaller (no LLM call performed). Production
				// can swap in a real LLM endpoint via env var
				// LLMGatewayAutoLLMEndpoint; the wrapper is here
				// so the dependency-graph and metrics are wired
				// before the first low-confidence heuristic result.
				autoroute.NewLLMFallbackClassifierWithCaller(buildAutoLLMCaller()),
				autoIdx,
				// v2.0.3 audit fix #14: switch from in-memory
				// (process-local) sticky to DB-backed (cluster-wide).
				autoroute.NewDBProfileStore(dbConn.Pool()),
			)
			if fpSlotRedis != nil {
				decider.SetIntentCache(autoroute.NewRedisSessionIntentCache(fpSlotRedis, 10*time.Minute))
			} else {
				decider.SetIntentCache(nil)
			}
			// v2.1: Decider reads the LLM-fallback threshold from
			// tuningStore dynamically (atomic.Pointer load, no lock).
			decider.SetTuningStore(tuningStore)
			if flags := autoroute.GetFeatureFlags(); flags != nil && flags.AutoEmbeddingRoute {
				analysisBaseURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANALYSIS_BASE_URL"))
				if analysisBaseURL == "" {
					analysisBaseURL = strings.TrimSpace(os.Getenv("LLM_ANALYSIS_BASE_URL"))
				}
				analysisAPIKey := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANALYSIS_API_KEY"))
				if analysisAPIKey == "" {
					analysisAPIKey = strings.TrimSpace(os.Getenv("LLM_ANALYSIS_API_KEY"))
				}
				analysisModel := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EMBEDDING_MODEL"))
				if analysisModel == "" {
					analysisModel = strings.TrimSpace(os.Getenv("LLM_GATEWAY_SA_MODEL_EMBEDDING"))
				}
				if analysisModel == "" || analysisModel == "auto" {
					analysisModel = analysis.NewLLMStageConfig(nil).ModelFor(analysis.StageEmbedding)
				}
				if analysisModel == "" || analysisModel == "auto" {
					analysisModel = "text-embedding-3-small"
				}
				allowInsecureLocal := os.Getenv("LLM_GATEWAY_ANALYSIS_ALLOW_INSECURE_LOCAL") == "true"
				if analysisBaseURL == "" || analysisAPIKey == "" {
					slog.Warn("autoroute: embedding shadow disabled, analysis client is not configured")
				} else if validatedURL, validateErr := validateAnalysisEndpoint(analysisBaseURL, allowInsecureLocal); validateErr != nil {
					slog.Warn("autoroute: embedding shadow disabled, analysis endpoint rejected", "error", validateErr)
				} else {
					oc := analysis.NewOpenAIClientWithNetworkPolicy(validatedURL, analysisAPIKey, 10*time.Second, allowInsecureLocal)
					embedClf := autoroute.NewEmbeddingClassifier(dbConn.Pool(), &embedAdapter{oc: oc, model: analysisModel}, analysisModel, 1024)
					decider.SetShadowClassifier(embedClf)
					decider.SetShadowSampleRate(0.01)
					slog.Info("autoroute: embedding shadow enabled", "sample_rate", 0.01, "model", analysisModel, "base_url", validatedURL)
				}
			}
			// v2.1: Score() also reads profile weights from tuningStore.
			autoroute.SetTuningStore(tuningStore)
			chatHandler.SetAutoRoute(decider)
			// /v1/embeddings model=auto resolution (22 章 §22.2).
			// Shares the same index as chat; AutoOnEmbeddings flag gates it.
			// NOTE: embeddingsHandler may be nil if auth is disabled.
			if embeddingsHandler != nil {
				embeddingsHandler.SetAutoIndex(autoIdx)
			}

			// ── Goal-mode auto control (2026-07-06) ───────────────────────
			// Wire the goal/audit response interceptors. Safe-by-default:
			// disabled unless LLM_GATEWAY_GOAL_ENABLED / goal.enabled is set,
			// and the whole chain is a no-op until SetResponseInterceptor is
			// called. Runs after autoroute so the audit follow-up can reuse
			// the autoroute model selection. The goal stores use database/sql,
			// so bridge the app's pgxpool via dbConn.Stdlib().
			initGoalControl(dbConn.Stdlib(), chatHandler)

			autoIndexRefresher = bg.NewAutoIndexRefresher(dbConn.Pool(), autoIdx)
			autoIndexRefresher.Start(context.Background())

			// v2.0.1: realtime listener for sub-second index refresh
			// (PG LISTEN/NOTIFY trigger on credential_model_bindings /
			// credentials / api_keys / model_offers).
			autoRouteListener = bg.NewAutoRouteRealtimeListener(dbConn.Pool(), autoIndexRefresher)
			autoRouteListener.Start(context.Background())

			// v2.1 (P7.5): TuningViewRefresher keeps the materialised
			// views (tuning_signals_5m + daily) up to date.
			tuningViewRefresher := bg.NewTuningViewRefresher(dbConn.Pool())
			tuningViewRefresher.Start(context.Background())
			defer func() {
				tuningViewRefresher.Stop()
			}()

			// v2.1 (P7.7): OverrideStoreRefresher keeps the
			// routing_overrides snapshot up to date so admin
			// POST/PATCH/DELETE operations take effect within
			// 1 min on the hot path. 1-min cadence (vs 5-min
			// for tuning signals) because overrides are
			// operational levers, not analytical.
			overrideStore := autoroute.NewOverrideStore(dbConn.Pool())
			if err := overrideStore.Reload(context.Background()); err != nil {
				slog.Warn("override store initial reload failed", "error", err)
			}
			overrideRefresher := bg.NewOverrideStoreRefresher(dbConn.Pool(), overrideStore)
			overrideRefresher.Start(context.Background())
			defer func() {
				overrideRefresher.Stop()
			}()

			// 2026-07-02: 统一存储水位 worker。
			// 定期检查磁盘水位，超阈值时按策略自动清理附件和日志。
			// 受 storage.auto_cleanup_enabled 开关控制（默认关闭）。
			// 注入 attachmentStorage（而非目录字符串快照），worker 每次 sweep
			// 读 BaseDir() 以跟随运行时目录热切换（迁移）。
			var logDirForWorker string
			if lc := logging.ActiveConfig(); lc.File != "" {
				logDirForWorker = filepath.Dir(lc.File)
			}
			retentionCfgProvider := newStorageRetentionConfigProvider()
			retentionWorker := bg.NewStorageRetentionWorker(attachmentStorage, logDirForWorker, retentionCfgProvider)
			if ttl, _ := readIntSettingPublic("storage.attachment_ttl_days"); ttl > 0 {
				retentionWorker.AttachmentTTLDays = ttl
			} else {
				retentionWorker.AttachmentTTLDays = 30
			}
			if v, _ := readIntSettingPublic("log.delete_days"); v > 0 {
				retentionWorker.LogDeleteDays = v
			}
			retentionWorker.Start(context.Background())
			defer retentionWorker.Stop()
			// Wire the store into the Decider so ban/pin logic
			// runs on every decision.
			decider.SetOverrideStore(overrideStore)

			defaultRoutingStore := autoroute.NewDefaultRoutingStore(dbConn.Pool())
			if err := defaultRoutingStore.Reload(context.Background()); err != nil {
				slog.Warn("default routing store initial reload failed", "error", err)
			}
			defaultRoutingRefresher := bg.NewDefaultRoutingStoreRefresher(dbConn.Pool(), defaultRoutingStore)
			defaultRoutingRefresher.Start(context.Background())
			defer func() { defaultRoutingRefresher.Stop() }()
			decider.SetDefaultRoutingStore(defaultRoutingStore)
			// 2026-07-17 (audit H1): wire the apiKeyID -> tenantID resolver so
			// tenant-scoped default routing rows can actually match. Without
			// this, TenantResolver stays nil, tenantID is always empty, and every
			// tenant-level rule an operator configures silently never resolves
			// (Resolve falls back to platform-level rows). Best-effort: a
			// missing/disabled key resolves to an empty code (platform-level).
			decider.SetTenantResolver(func(apiKeyID int) string {
				if apiKeyID <= 0 {
					return ""
				}
				var tid string
				lookupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = dbConn.Pool().QueryRow(lookupCtx,
					`SELECT COALESCE(tenant_id, '') FROM api_keys WHERE id = $1`,
					apiKeyID).Scan(&tid)
				return tid
			})

			// v2.2 (P8.8): AuditTrimmer caps growth of the two
			// audit tables (routing_overrides_audit from P7.9
			// trigger, routing_audit_log from P7.9.1 app-level
			// log) at 90 days. Daily cadence; bounded
			// LIMIT 5000 per batch to avoid long locks.
			auditTrimmer := bg.NewAuditTrimmer(dbConn.Pool())
			auditTrimmer.Start(context.Background())
			defer func() {
				auditTrimmer.Stop()
			}()

			// v2.1: FeedbackAnalyzer — daily worker that generates
			// tuning_proposals from tuning_signals. Skipped in data-plane
			// mode to avoid write load on the secondary instance.
			feedbackAnalyzer := bg.NewFeedbackAnalyzer(dbConn.Pool())
			feedbackAnalyzer.Start(context.Background())
			if adminHandler != nil {
				adminHandler.SetFeedbackAnalyzer(feedbackAnalyzer)
			}

			// v2.1: tuning_signals async writer. Wired with the same PG
			// pool the rest of the system uses; runs an independent
			// batching goroutine so request_logs is unaffected.
			telemetry.Adapter.PoolExec = func(ctx context.Context, sql string, args ...any) (telemetry.PgxTag, error) {
				return dbConn.Pool().Exec(ctx, sql, args...)
			}
			telemetry.StartTuningWriter()
			defer telemetry.StopTuningWriter()

			// Wire LLM HTTP status counter indirection so the
			// HTTPLlmCaller (in autoroute) can emit status codes
			// without importing the telemetry package.
			autoroute.RecordLLMHTTPStatus = telemetry.RecordLLMHTTPStatus

			slog.Info("auto-route decider enabled (with realtime LISTEN/NOTIFY + tuning feedback loop)")
		}

		if adminHandler != nil {
			slog.Info("CHECKPOINT: before SetBackgroundServices")
			adminHandler.SetBackgroundServices(credCycler, credRecovery, envelopeCleaner, stickyCleaner, taxonomySync)
			slog.Info("CHECKPOINT: after SetBackgroundServices")
			adminHandler.SetProbeServices(credProbeV2, defaultProbePicker)
			slog.Info("CHECKPOINT: after SetProbeServices")
			if modelProbe != nil {
				adminHandler.SetModelProbeRunner(modelProbe)
			}
			slog.Info("CHECKPOINT: after SetModelProbeRunner")
			// 2026-06-30: wire state manager for /api/credentials/*/state
			// and /api/credentials/*/test endpoints.
			if stateManager != nil {
				adminHandler.SetStateManager(stateManager)
			}
			adminHandler.SetFpSlots(fpSlots)
			slog.Info("CHECKPOINT: after SetFpSlots")
			adminHandler.SetPeakCollector(peakCollector)
			slog.Info("CHECKPOINT: after SetPeakCollector")
			// Wire redis for credential monitor endpoints (2026-06-22).
			if fpSlotRedis != nil {
				adminHandler.SetRedisClient(fpSlotRedis)
				if modelAvailabilityReader == nil {
					modelAvailabilityReader = bg.NewModelAvailabilityReader(fpSlotRedis)
				}
				adminHandler.SetAvailabilityReader(modelAvailabilityReader)
				if modelAvailabilityBackfill != nil {
					adminHandler.SetAvailabilityBackfill(modelAvailabilityBackfill)
				}
				if modelAvailabilityKeyCounter != nil {
					adminHandler.SetAvailabilityKeyCounter(modelAvailabilityKeyCounter)
				}
			}
			slog.Info("CHECKPOINT: after SetRedisClient")
		}

		// 2026-07-06: Session State Management runtime wiring
		// 初始化会话状态管理组件（Manager, DBWriter, CleanupWorker, RotationHook）
		// 并注入到 adminHandler，使 /api/admin/sessions* 端点可用。
		if fpSlotRedis != nil && adminHandler != nil && dbConn != nil && dbConn.Enabled() {
			sessionState, ssErr := InitializeSessionState(
				context.Background(),
				dbConn.Pool(),
				fpSlotRedis,
				adminHandler,
				sessionMgr, // 传入已初始化的 sessionMgr
			)
			if ssErr != nil {
				slog.Error("session state init failed", "error", ssErr)
			} else if sessionState != nil {
				defer sessionState.Shutdown()
				// 将 RotationHook 注入到 ChatHandler，使请求链路自动检测凭据轮换
				if sessionState.RotationHook != nil {
					chatHandler.SetRotationHook(sessionState.RotationHook)
					slog.Info("session rotation hook wired into chat handler")
				}
				slog.Info("session state management initialized")
			}
		}

		// 2026-07-10: 数据库降级和备份恢复模块
		// 当数据库离线时，系统自动切换到降级模式，会话数据备份到文件（gzip 压缩）
		if dbConn != nil && dbConn.Enabled() && sessionMgr != nil && fpSlotRedis != nil {
			// 配置备份目录
			backupDir := os.Getenv("LLM_GATEWAY_BACKUP_DIR")
			if backupDir == "" {
				backupDir = "./data/backups"
			}

			// 1. 初始化数据库监控器
			dbMonitor := dbdegradation.NewMonitor(dbConn.Pool(), dbdegradation.MonitorConfig{
				CheckInterval:    10 * time.Second,
				FailThreshold:    3,
				RecoverThreshold: 3,
			})

			// 2. 初始化文件写入器（支持 gzip 压缩）+ 内存 ring buffer（在线 dump/replay）
			// 2026-07-20: 双写。FileWriter 继续写磁盘（重启恢复），ring buffer 是快速访问层。
			// ring buffer 容量走 env TELEMETRY_FALLBACK_BUFFER_CAP，默认 10000。
			fileWriter := dbdegradation.NewFileWriter(backupDir)
			ringBuffer = dbdegradation.NewRingBuffer(positiveIntEnv("TELEMETRY_FALLBACK_BUFFER_CAP", 10000))
			fallbackWriter := dbdegradation.NewMultiBackupWriter(fileWriter, ringBuffer)
			telemetryClient.SetFallbackWriter(fallbackWriter)
			if requestLogger != nil {
				requestLogger.SetFallbackWriter(fallbackWriter)
			}
			defer fileWriter.Close()

			// 3. 初始化文件读取器
			fileReader := dbdegradation.NewFileReader(backupDir)

			// 4. 初始化数据恢复管理器
			recovery := dbdegradation.NewRecovery(dbConn.Pool(), fileReader, 100)
			genericRecovery := dbdegradation.NewGenericRecovery(fileReader, func(ctx context.Context, record dbdegradation.BackupRecord) error {
				if record.Type == "request_log" {
					return telemetryClient.ReplayFallback(ctx, record)
				}
				if requestLogger != nil {
					return requestLogger.ReplayFallback(ctx, record)
				}
				return fmt.Errorf("request WAL logger not configured")
			})

			// 5. 初始化 TTL 管理器
			sessionRedisClient := session.NewRedisClientFromClient(fpSlotRedis)
			ttlManager := dbdegradation.NewTTLManager(
				sessionRedisClient,
				7*24*time.Hour,  // 正常 TTL
				30*24*time.Hour, // 降级 TTL
			)
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := ttlManager.Stop(stopCtx); err != nil {
					slog.Warn("failed to stop TTL manager", "error", err)
				}
			}()

			// 6. 注册状态变更监听器
			dbMonitor.AddListener(func(event dbdegradation.StatusChangeEvent) {
				slog.Info("database status changed",
					"old_status", event.OldStatus.String(),
					"new_status", event.NewStatus.String(),
					"message", event.Message,
				)

				switch event.NewStatus {
				case dbdegradation.DBStatusDegraded:
					// 进入降级模式
					sessionMgr.SetDegradedMode(true)
					telemetryClient.SetDegraded(true)
					if requestLogger != nil {
						requestLogger.SetDegraded(true)
					}

					if err := ttlManager.EnterDegradedMode(context.Background()); err != nil {
						slog.Error("failed to enter degraded mode", "error", err)
					}
					slog.Warn("⚠️  ENTERED DEGRADED MODE - sessions will be backed up to compressed files",
						"backup_dir", backupDir,
						"ttl_extended_to", "30 days")

				case dbdegradation.DBStatusAvailable:
					// 退出降级模式
					sessionMgr.SetDegradedMode(false)
					telemetryClient.SetDegraded(false)
					if requestLogger != nil {
						requestLogger.SetDegraded(false)
					}

					if err := ttlManager.ExitDegradedMode(context.Background()); err != nil {
						slog.Error("failed to exit degraded mode", "error", err)
					}
					slog.Info("✓ EXITED DEGRADED MODE - database available, normal operations resumed")
				}
			})

			// 7. 将文件写入器注入到 session manager
			sessionMgr.SetFileWriter(fileWriter)

			// 8. 启动数据库监控
			dbMonitor.Start(context.Background())
			defer dbMonitor.Stop()

			// 9. 注入到管理 Handler
			if adminHandler != nil {
				adminHandler.WireDBDegradation(dbMonitor, fileReader, recovery, ttlManager)
				adminHandler.WireDegradationControl(dbMonitor, ttlManager, fileWriter, fileReader, genericRecovery,
					func(context.Context) error {
						sessionMgr.SetDegradedMode(true)
						telemetryClient.SetDegraded(true)
						if requestLogger != nil {
							requestLogger.SetDegraded(true)
						}
						return nil
					},
					func(context.Context) error {
						sessionMgr.SetDegradedMode(false)
						telemetryClient.SetDegraded(false)
						if requestLogger != nil {
							requestLogger.SetDegraded(false)
						}
						return nil
					},
				)
			}

			slog.Info("database degradation module initialized",
				"backup_dir", backupDir,
				"check_interval", "10s",
				"compression", "gzip",
				"normal_ttl", "7 days",
				"degraded_ttl", "30 days")
		} else {
			slog.Info("database degradation module skipped (missing dependencies)",
				"db", dbConn != nil && dbConn.Enabled(),
				"session_mgr", sessionMgr != nil,
				"redis", fpSlotRedis != nil)
		}

		slog.Info("CHECKPOINT: before memoraClient check")
		if memorySvc != nil {
			adminHandler.SetMemoraServices(memorySvc.AdminClient(), memorySvc.AdminSink())
		}

		slog.Info("CHECKPOINT: before autoIndexRefresher check")

		// v2.0.2 audit fix #6: admin auto-route refresh endpoint needs
		// the live AutoIndexRefresher wired in. Without this, /refresh
		// returns 503 "index refresher not wired".
		if autoIndexRefresher != nil && adminHandler != nil {
			adminHandler.SetAutoIndexRefresher(autoIndexRefresher)
		}
		// Track C C7 (2026-06-18): wire the pending response cache
		// into the admin handler so the /api/admin/pending-responses*
		// endpoints can list, inspect, and manually clear entries.
		if pendingStore != nil && adminHandler != nil {
			adminHandler.SetPendingStore(pendingStore)
		}

		bg.StartWorkTypeACCSync(context.Background(), dbConn.Pool(), func(ctx context.Context) error {
			return admin.SyncWorkTypesFromACCForBG(ctx, dbConn.Pool())
		})

		// ── APIHub AssetWatcher (Track A A1-1 / A1-2) ──────────────────
		// Periodically syncs model_offers + tool_registry.tools into the
		// unified assets table. RLS is enforced via PGStore per-query.
		// Best-effort: if DB is misconfigured the gateway still serves
		// traffic; we only log.
		apihubStore := apihub.NewPGStore(dbConn.Pool())
		apihubSvc = apihub.New(apihubStore, apihub.WithLogger(slog.Default()))
		apihubSvc.StartRefresh(context.Background())
		apihubWatcher := bg.NewAssetWatcher(apihubSvc, bg.NewPGSyncer(dbConn.Pool()))
		apihubWatcher.WithInterval(60 * time.Second)
		apihubWatcher.Start(context.Background())
		defer apihubWatcher.Stop()
		slog.Info("apihub watcher initialized", "interval", "60s")

		// Phase 7: Asset Health Probe — marks stale assets as degraded,
		// missing-from-source as down. Default 6h stale / 1h tick.
		healthProbe := bg.NewAssetHealthProbe(apihubSvc, bg.NewPGSyncer(dbConn.Pool()))
		healthProbe.Start(context.Background())
		defer healthProbe.Stop()

		// ── Armor Logger (Track A B1-4) ─────────────────────────────────
		// Writes armor judgments to armor_judgments table. Used by relay
		// handlers (when armor is enabled) to audit every judge decision.
		// Safe for concurrent use; failures are logged, never block relay.
		armorLogger := armor.NewLogger(dbConn.Pool())
		slog.Info("armor logger initialized")

		// ── Armor Judge (Track A B1-5) ──────────────────────────────────
		// HTTP judge client calls external LLM to score prompts. v1 observe-only mode.
		var armorJudge armor.Judge
		judgeEndpoint := os.Getenv("ARMOR_JUDGE_ENDPOINT") // e.g. "https://api.openai.com/v1"
		judgeModel := os.Getenv("ARMOR_JUDGE_MODEL")       // e.g. "gpt-4o-mini"
		judgeAPIKey := os.Getenv("ARMOR_JUDGE_API_KEY")    // OpenAI-compatible API key
		if judgeEndpoint != "" && judgeModel != "" && judgeAPIKey != "" {
			var err error
			armorJudge, err = armor.NewHTTPJudge(armor.HTTPOptions{
				BaseURL: judgeEndpoint,
				Model:   judgeModel,
				APIKey:  judgeAPIKey,
			})
			if err != nil {
				slog.Error("armor judge init failed", "error", err)
				armorJudge = armor.NewMockJudge(0.0, "mock") // fallback: always safe
			} else {
				slog.Info("armor judge initialized", "endpoint", judgeEndpoint, "model", judgeModel)
			}
		} else {
			armorJudge = armor.NewMockJudge(0.0, "mock") // fallback: always safe
			slog.Warn("armor judge not configured, using mock judge (always safe)")
		}

		// Wire armor into chat handler
		chatHandler.SetArmor(armorJudge, armorLogger)
	}

	slog.Info("CHECKPOINT: before static handler init")

	// ── Static files (Vue SPA) ───────────────────────────────────────────
	staticHandler := streaming.NewStaticHandler(cfg.StaticDir)

	// maintain-web SPA + assets. Optional: only configured when
	// MAINTAIN_WEB_DIST points at the maintain-web build. When absent the
	// /maintain/* and /maintain-assets/* routes simply aren't registered.
	maintainStatic := NewMaintainStaticHandler(os.Getenv("MAINTAIN_WEB_DIST"))
	if maintainStatic != nil {
		slog.Info("maintain-web static handler configured", "dir", os.Getenv("MAINTAIN_WEB_DIST"))
	}

	slog.Info("CHECKPOINT: before router init")

	// ── Router ────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	slog.Info("CHECKPOINT: before healthz registration")

	// ── Sensitive Word Engine (2026-07-18) ───────────────────────────────
	// AC automaton for multi-pattern sensitive word detection. Lifted out
	// of buildV2DispatchPipeline so it's always available regardless of
	// v2 pipeline flag. Admin API can reload the word list at runtime.
	// If configs/sensitive_words.json is missing or invalid, engine stays
	// empty (all checks pass) — best-effort, non-blocking.
	swEngine := sensitive.NewSensitiveWordEngine()
	swCfgPath := "configs/sensitive_words.json"
	if err := swEngine.BuildFromFile(swCfgPath); err != nil {
		slog.Warn("sensitive word engine init skipped", "path", swCfgPath, "error", err)
	} else {
		slog.Info("sensitive word engine ready", "words", swEngine.LoadedWordCount())
	}
	// Wire into admin handler immediately so /api/admin/sensitive-words/*
	// endpoints are always available.
	if adminHandler != nil {
		adminHandler.SetSensitiveWordEngine(swEngine)
		slog.Info("admin: sensitive word engine wired", "words", swEngine.LoadedWordCount())
	}

	// 2026-07-09: 飞书机器人模块 late-binding（在 mux 创建后注入 callback 路由）。
	// 复用 initApprovalNotifier 阶段创建的 LarkBotChannel 与 auditBus；
	// 装配失败仅记日志，不影响主进程启动（best-effort）。
	if dbConn != nil && dbConn.Enabled() && dbConn.Pool() != nil {
		if _, ferr := InitFeishubotPlugin(gAuditBus, gLarkCh, gApprovalMgr, mux, dbConn.Pool()); ferr != nil {
			slog.Warn("v2 pipeline: feishubot init failed (best-effort)", "error", ferr)
		}
	} else {
		// 兼容模式：无 db pool，Plugin 走 settings_kv 兜底
		if _, ferr := InitFeishubotPlugin(gAuditBus, gLarkCh, gApprovalMgr, mux, nil); ferr != nil {
			slog.Warn("v2 pipeline: feishubot init failed (best-effort)", "error", ferr)
		}
	}

	// NET-007 fix: /healthz 拆分两 path：
	//   - /healthz            匿名基础探测（K8s liveness 用）
	//   - /healthz/full       admin token 才能访问的详细状态（替换 ?full=true）
	//
	// 历史 /healthz?full=true 仍被 healthHandler 接住，但 server 端会因
	// query 参数含 full=true 且 Authorization 不存在而 401（见
	// domains/streaming/handler.go ServeHTTP 改动）。
	mux.Handle("/healthz", healthHandler)
	mux.Handle("/healthz/full",
		middleware.NewAdminTokenMiddleware(cfg.AdminAPIKey).Wrap(healthHandler))
	// 2026-07-14: 30s system-health JSON for the GDRT H badge on the
	// homepage. CORS open (no auth) so the SPA login page can show
	// the indicator. Returns "suspect" when the worker is not
	// configured (db disabled).
	mux.HandleFunc("/api/health/system", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if systemHealthWorker == nil {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"suspect","detail":"worker not configured"}`))
			return
		}
		s := systemHealthWorker.Last()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(s)
	})

	// NET-008 fix: /metrics 必须 admin 鉴权（暴露所有 prometheus 注册
	// 指标含 provider / credential 等敏感标签）。使用
	// LLM_GATEWAY_ADMIN_API_KEY 静态 token（与 AdminTokenMiddleware 配合）。
	mux.Handle("/metrics", middleware.NewAdminTokenMiddleware(cfg.AdminAPIKey).Wrap(middleware.MetricsHandler()))

	// 2026-07-20: telemetry fallback ring buffer 暴露面。
	// 路径: /internal/telemetry/fallback-buffer/{stats,dump,clear,replay}
	// 鉴权: 与 /healthz/full 一致（LLM_GATEWAY_ADMIN_API_KEY）。
	// 用途: 当 telemetry worker 写 DB 失败时，ring buffer 保留最近 N 条
	//       BackupRecord 在内存中；运维可通过 dump 拉快照，replay 重放到 DB。
	// 注: ringBuffer 在 dbConn != nil 块里赋值；db 关闭模式（ringBuffer == nil）
	//     下不挂路由，运维接口自动 fail-closed。
	if ringBuffer != nil {
		fbHandler := NewTelemetryFallbackBufferHandler(ringBuffer, telemetryClient.ReplayFallback)
		mux.Handle("/internal/telemetry/fallback-buffer/",
			middleware.NewAdminTokenMiddleware(cfg.AdminAPIKey).Wrap(fbHandler))
	}

	slog.Info("CHECKPOINT: healthz and metrics registered")

	// plugin-runtime: scan installed plugins and serve /api/v1/plugin-nav.
	// nav is admin-auth-gated; viewer is derived from the real auth context
	// (replaces the P0 super-admin placeholder).
	pluginRegistry := pluginruntime.NewRegistry()
	pluginsDir := os.Getenv("LLM_GATEWAY_PLUGINS_DIR")
	var sup *pluginruntime.Supervisor // P12: hoisted so the install handler can use it when plugins are enabled
	var pluginManifests []*pluginruntime.Manifest
	if pluginsDir != "" {
		var err error
		pluginManifests, err = ScanPlugins(pluginsDir, pluginRegistry)
		if err != nil {
			slog.Warn("plugin scan failed", "error", err, "dir", pluginsDir)
		}
		registerPluginStaticRoutes(mux, pluginsDir)

		// P5: start plugin processes + register API proxy wired to the
		// supervisor's actual unix socket paths. pluginBaseFor returns
		// "unix://<socketPath>" for a running plugin, or "" if the plugin
		// failed to start — the proxy then responds 502 Bad Gateway.
		sup = pluginruntime.NewSupervisor(pluginruntime.SupervisorConfig{
			SocketDir:     filepath.Join(pluginsDir, ".sockets"),
			ContextSecret: []byte(cfg.SecretKey),
			SigningPubkey: os.Getenv("LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY"),
		})
		pluginBases := ScanAndStartPlugins(sup, pluginsDir, pluginManifests)
		registerPluginAPIProxy(mux, []byte(cfg.SecretKey), func(pluginID string) string {
			return pluginBases[pluginID] // "" if not running → apiproxy returns 502
		}, dbConn.Pool(), cfg.SecretKey)

		// P6: health loop — precise HTTP health check as the liveness signal.
		// Each tick GETs manifest.Runtime.HealthPath over the plugin's unix
		// socket; consecutive failures (default 2) mark the plugin degraded in
		// the registry, which surfaces in /api/v1/plugin-nav. P8: switched from
		// socket-only dial (which missed wedged-but-listening plugins) to a
		// real GET that requires a 200 response.
		healthCheck := newPluginHealthCheck(sup)
		healthLoop := pluginruntime.NewHealthLoop(pluginRegistry, healthCheck, pluginruntime.HealthLoopConfig{
			Interval:         30 * time.Second,
			FailureThreshold: 2,
			Restarter:        sup.Restart, // P7: auto-restart crashed plugins
			MaxRestarts:      5,
			BackoffStart:     5 * time.Second,
			BackoffMax:       2 * time.Minute,
		})
		healthLoop.Start()
		defer healthLoop.Stop() // graceful shutdown: stop the loop on gateway exit
		// graceful shutdown: SIGTERM each plugin process (health loop already stopped above)
		defer func() {
			for pluginID := range pluginBases {
				_ = sup.Stop(pluginID)
			}
		}()
	}
	wirePluginAuthExtractor()
	mux.Handle("/api/v1/plugin-nav", admin.AdminMiddleware(
		func(w http.ResponseWriter, r *http.Request) {
			pluginruntime.NavHandler(pluginRegistry, pluginruntime.ViewerFromRequest).ServeHTTP(w, r)
		},
		dbConn.Pool(), cfg.SecretKey,
	))

	// P12: plugin installer — manual install trigger via admin API.
	// Catalog base URL + platform/arch come from env; gateway version from version.json.
	// Disabled (503 on the endpoint) when LLM_GATEWAY_MAINTAIN_URL is unset OR
	// pluginsDir is unset (sup == nil).
	maintainURL := os.Getenv("LLM_GATEWAY_MAINTAIN_URL")
	var installer pluginInstaller
	if maintainURL != "" && sup != nil {
		catalogClient := pluginruntime.NewMaintainCatalogClient(
			maintainURL, runtime.GOOS, runtime.GOARCH, Version(),
		)
		installer = pluginruntime.NewInstaller(pluginruntime.InstallerConfig{
			PluginsDir:    pluginsDir,
			Client:        catalogClient,
			Supervisor:    sup,
			Registry:      pluginRegistry,
			SigningPubkey: os.Getenv("LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY"),
		})
		slog.Info("plugin installer enabled", "maintain_url", maintainURL, "platform", runtime.GOOS, "arch", runtime.GOARCH)
	} else {
		slog.Info("plugin installer disabled", "maintain_url_set", maintainURL != "", "supervisor_set", sup != nil)
	}
	mux.Handle("POST /api/v1/plugins/install", admin.AdminMiddleware(
		makePluginInstallHandler(installer),
		dbConn.Pool(), cfg.SecretKey,
	))

	// canonical API for plugin processes (signed context, not admin cookie).
	// Reuses admin.HandleSessionAnalyticsList under a tenant-scoped AuthContext.
	// Only mount when the admin handler is available (requires DB). The plugin
	// side (Task 8) signs with AI_SESSION_MANAGER_GATEWAY_CONTEXT_SECRET, which
	// must equal cfg.SecretKey for verification to pass.
	//
	// Replay protection: a single NonceCache backs the canonical routes. Its
	// TTL (10 min) deliberately exceeds the VerifyPluginContext ±5min skew
	// window, so a signed token cannot fall out of the cache before its
	// freshness window closes — every token within that window is seen at most
	// once and a replayed (pluginID|tenantID|ts|nonce) is rejected with 401.
	//
	// compareAPI is hoisted to function scope (declared here, assigned below at
	// the Phase 3.5 block inside `if dbConn != nil && toolRegistry != nil`)
	// because the canon turns route — registered here at ~line 294x — needs to
	// close over it via HandleCompare, but that block runs before the Phase 3.5
	// block where the admin /api/admin/session-compare route is mounted.
	//
	// Pool extraction mirrors the adminHandler wiring above (line ~1468): when
	// dbConn is nil/disabled the pool is nil, the API is constructed against a
	// nil pool (same as every other admin handler in that mode), and requests
	// fail at query time rather than crashing at startup. This preserves the
	// original `if adminHandler != nil` semantics — canon routes still mount
	// even when DB is unavailable, matching list/detail.
	var canonPool *pgxpool.Pool
	if dbConn != nil && dbConn.Enabled() {
		canonPool = dbConn.Pool()
	}
	var compareAPI *admin.SessionCompareAPI
	if adminHandler != nil {
		compareAPI = admin.NewSessionCompareAPI(canonPool)
		pluginNonceCache := pluginruntime.NewNonceCache(10 * time.Minute)
		registerPluginCanonRoutes(mux, []byte(cfg.SecretKey), CanonHandlers{
			List:       http.HandlerFunc(adminHandler.HandleSessionAnalyticsList),
			Detail:     http.HandlerFunc(adminHandler.HandleSessionAnalyticsDetail),
			Turns:      http.HandlerFunc(compareAPI.HandleCompare),
			Panorama:   http.HandlerFunc(adminHandler.HandleSessionPanorama),
			Breakdown:  http.HandlerFunc(adminHandler.HandleModelBreakdown),
			Timeseries: http.HandlerFunc(adminHandler.HandleCostTrend),
			Top:        http.HandlerFunc(adminHandler.HandleTopSessions),
			Clusters:   http.HandlerFunc(adminHandler.HandleSessionClustersList),
		}, pluginruntime.WithCanonNonceCache(pluginNonceCache))
	}

	// v2 Pipeline feature flag (R1.12). Opt-in via LLM_GATEWAY_V2_ENABLED.
	// Default OFF → no-op; production v1 routes are untouched. When ON,
	// a parallel /v2/* route group is mounted on the same mux. See
	// cmd/gateway/main_v2_pipeline.go for the wiring.
	registerV2PipelineRoutes(mux, dbConn.Pool())

	// R1.12 (2026-06-26): v1 dispatch Pipeline wrapper. When
	// LLM_GATEWAY_USE_V2_PIPELINE=true, the 4 v1 chat endpoints
	// (/v1/chat/completions, /v1/completions, /v1/messages,
	// /v1/responses) are wrapped through the v2 Hook Pipeline
	// (tracing, security, audit, observability) before reaching
	// the existing relay.ChatHandler. Default OFF keeps the v1
	// dispatch unchanged. See cmd/gateway/main_pipeline.go for
	// the wiring.
	//
	// The v1 routes are registered FIRST (so they bind), then the
	// v2 wrappers are registered LAST (so they win — Go's
	// http.ServeMux picks the last-registered exact-path match).
	v2DispatchEnabled := v2UsePipeline()
	if v2DispatchEnabled {
		slog.Info("v2 pipeline: LLM_GATEWAY_USE_V2_PIPELINE=true; 4 v1 endpoints will be Pipeline-wrapped")
	} else {
		slog.Info("v2 pipeline: flag not set; 4 v1 endpoints stay on v1 chatHandler (production default)")
	}

	mux.Handle("/v1/chat/completions", chatHandler)
	mux.Handle("/v1/completions", chatHandler)
	mux.Handle("/v1/messages", messagesHandler)
	mux.Handle("/v1/responses", responsesHandler)
	mux.Handle("/v1/embeddings", embeddingsHandler)
	mux.Handle("/v1/models", modelsHandler)

	// audit-gateway-gemini (2026-07-13): Gemini native API endpoints.
	// Both /v1beta/models/{m}:generateContent and /v1/models/{m}:generateContent
	// shapes are routed through a single GeminiHandler which parses the
	// Gemini-native body via the IR layer, dispatches through ChatHandler,
	// and converts the response back to Gemini native format. This gives
	// Gemini clients full compatibility without a separate executor —
	// credentials, stickiness, retries, audit, telemetry all reuse the
	// OpenAI infrastructure via IR translation.
	geminiHandler := streaming.NewGeminiHandler(chatHandler)
	mux.Handle("/v1beta/models/", geminiHandler)
	mux.Handle("/v1/models/", geminiHandler) // also catch Gemini :generateContent on newer paths

	// Overlay the v2 Pipeline wrapper on top of the 4 v1 endpoints
	// when the flag is on. The wrapper is a Pipeline preflight
	// (tracing/security/audit/...) → chatHandler.ServeHTTP →
	// postflight. The 4 v1 handlers above stay registered as the
	// fallback inside v2DispatchHandler; the Pipeline re-routes
	// through them on a stage error or feature-flag off path.
	if v2DispatchEnabled {
		if _, v2Deps, ok := v2DispatchMux(chatHandler, messagesHandler, responsesHandler); ok && v2Deps != nil {
			// 2026-07-18 P1 fix: Wire the sensitive word engine created above
			// into v2Deps so the Pipeline plugins can reference it.
			v2Deps.SensitiveWordEngine = swEngine

			// PR-V4-09 / PR-V4-10: 注入 DB pool + ApprovalManager + Publisher +
			// IntentStore 后再启动 Loop 和 Flusher。
			if dbConn != nil && dbConn.Pool() != nil {
				pool := dbConn.Pool()
				pub := bus.NewPGPublisher(pool, slog.Default())
				intentStore := assets.NewPGIntentAggregateStore(pool, slog.Default())
				// PR-V4-11: detector / checker / summarizer 当前传 nil；
				// 它们需要 *sql.DB（不是 pgxpool），后续 PR 再桥接。
				SetV2DispatchAnalysisResources(v2Deps, pool, approvalMgr, pub, intentStore, nil, nil, nil, fpSlotRedis)
			}
			StartV2DispatchAnalysisLoop(v2Deps)
			defer v2ShutdownPipeline(v2Deps)
			mux.Handle("/v1/chat/completions", v2DispatchHandler(v2Deps, chatHandler))
			mux.Handle("/v1/completions", v2DispatchHandler(v2Deps, chatHandler))
			// /v1/messages and /v1/responses internally call
			// chatHandler.ServeHTTP, so wrapping chatHandler is
			// enough to put the Pipeline in front of all 4.
			mux.Handle("/v1/messages", v2DispatchHandler(v2Deps, messagesHandler))
			mux.Handle("/v1/responses", v2DispatchHandler(v2Deps, responsesHandler))
			slog.Info("v2 pipeline: 4 v1 endpoints overridden with Pipeline wrappers")
		}
	}

	if sessionMgr != nil {
		sessionHandler := session.NewHandler(sessionMgr)
		if keyVerifier.Enabled() {
			sessionHandler.SetAuth(sessionAuthAdapter{kv: keyVerifier})
		}
		// Track C (2026-06-18): wire the pending response cache.
		// The adapter lives in main.go (the only place that can
		// import both sessions and pending without a cycle). nil
		// pendingStore leaves the endpoint returning 503 gracefully.
		if pendingStore != nil {
			sessionHandler.SetPendingStore(newPendingStoreAdapter(pendingStore))
		}
		mux.Handle("/v1/sessions", sessionHandler)
		mux.Handle("/v1/sessions/", sessionHandler)
		mux.Handle("/v1/gw/sessions", sessionHandler)
		mux.Handle("/v1/gw/sessions/", sessionHandler)
		slog.Info("session endpoints enabled", "paths", []string{"/v1/sessions", "/v1/gw/sessions"})
	}

	// ── Config reload endpoint ──────────────────────────────────────────
	//
	// NET-003 fix:
	//   1. 必须 admin token（LLM_GATEWAY_ADMIN_API_KEY），未授权返回 401
	//   2. 错误响应脱敏：只返回通用 "config reload failed"，详细 err 保留
	//      在服务端 slog 日志（供运维排查）
	//   3. 成功响应保持 {status: ok}
	if configFile != "" {
		configPath := configFile
		reloadHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := cfgStore.ReloadFile(configPath); err != nil {
				slog.Error("config: hot-reload failed", "path", configPath, "error", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				//nolint:errcheck // HTTP write error non-recoverable
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "config reload failed"})
				return
			}
			slog.Info("config: hot-reload succeeded", "path", configPath)
			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck // HTTP write error non-recoverable
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		})
		mux.Handle("/admin/config/reload",
			middleware.NewAdminTokenMiddleware(cfg.AdminAPIKey).Wrap(reloadHandler))
		slog.Info("config: hot-reload endpoint enabled (admin auth required)", "path", configFile)
	}

	// Static files / SPA fallback
	if staticHandler != nil {
		mux.Handle("/", staticHandler)
		slog.Info("serving Vue SPA", "dir", cfg.StaticDir)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				//nolint:errcheck // HTTP write error non-recoverable
				//nolint:errcheck // HTTP write error non-recoverable
				w.Write([]byte(fmt.Sprintf(`{"service":"llm-gateway-go","version":"%s","git_sha":"%s","build_seq":"%s"}`,
					Version(), GitCommit(), BuildNumber())))
				return
			}
			http.NotFound(w, r)
		})
	}

	// Admin API routes
	if adminHandler != nil {
		slog.Info("CHECKPOINT: before admin RegisterRoutes")
		adminHandler.RegisterRoutes(mux)
		// /api/admin/prompt-injection/* — 策略、规则、引擎、Canary、严重度矩阵、检测日志。
		if promptInjectionHandler != nil {
			promptInjectionHandler.RegisterRoutes(mux)
			slog.Info("prompt-injection API registered")
		}
		// Routing health check endpoints (2026-07-10)
		if dbConn != nil && dbConn.Enabled() {
			healthCheckHandler := admin.NewHealthCheckHandler(dbConn.Pool())
			healthCheckHandler.RegisterRoutes(mux)
			slog.Info("health-check API registered")

			// Self-check admin endpoints (2026-07-12)
			selfCheckHandler := admin.NewSelfCheckHandler(dbConn.Pool())
			if selfCheckWorker != nil {
				selfCheckHandler.SetWorker(selfCheckWorker)
			}
			adminMw := newAdminMiddleware(dbConn.Pool(), cfg.SecretKey)
			superAdminMw := newSuperAdminMiddleware(dbConn.Pool(), cfg.SecretKey)
			selfCheckHandler.RegisterRoutes(mux, adminMw, superAdminMw)
			// 2026-07-23: 系统监测 REST 端点（systemMonitor 已通过 SetSystemMonitor 注入）。
			adminHandler.RegisterSystemMonitorRoutes(mux, adminMw, superAdminMw)
			slog.Info("self-check API registered")
		}
		// 2026-06-23 Phase 3: wire candidate_failure_monitor alert ring.
		if candidateFailureMonitor != nil {
			adminHandler.SetCandidateFailureHandlers(candidateFailureMonitor.RecentAlerts)
		}
		slog.Info("CHECKPOINT: after admin RegisterRoutes - admin API enabled")
	}

	// ── 运维平台 API (Phase 1-8) ───────────────────────────────────────
	// Licensing, Fault Management, Auto-update, Center Ops, VibeCoding
	if dbConn != nil && dbConn.Enabled() {
		// 创建 Echo 实例用于运维平台路由
		e := echo.New()
		e.HideBanner = true
		e.HidePort = true

		// Customer-facing routes must use a separate Echo instance. Echo's
		// e.Use middleware applies to every route in the instance, so mounting
		// public customer routes on the admin instance would still require JWT.
		customerEcho := echo.New()
		customerEcho.HideBanner = true
		customerEcho.HidePort = true

		jwtSecret := resolveJWTSecret(os.Getenv("LLM_GATEWAY_JWT_SECRET"), cfg.SecretKey)
		jwtMiddleware := newJWTMiddleware(jwtSecret)
		e.Use(jwtMiddleware)

		// 2026-07-21: 受限模式中间件必须在 jwtMiddleware 之前注册，
		// 这样受限请求会先被白名单检查拦截，避免无效的 JWT 校验。
		// 中间件内部已经包含 /api/healthz 等健康检查的放行逻辑。
		if gRestrictedMode {
			slog.Warn("enabling restricted mode middleware — only license/health/public endpoints are reachable")
			e.Pre(licensing.RestrictedModeMiddleware())
		}

		requireSuperAdmin := newRequireSuperAdminMiddleware()

		if jwtSecret == "" {
			slog.Warn("JWT secret not configured — admin Echo routes will reject requests")
		}

		pool := dbConn.Pool()

		// Phase 2: Licensing (License管理)
		licensingStore := licensing.NewPgxStore(pool)

		// Where the v2 grace-period FailureMarker lives. We use the
		// same path the offline / token-refresh enforcement path uses
		// so the grace and the daemon agree on where state is rooted.
		// Default path mirrors the offline license check; can be
		// overridden via LLM_GATEWAY_LICENSE_DATA_DIR for tests.
		licenseDataDir := os.Getenv("LLM_GATEWAY_LICENSE_DATA_DIR")
		if licenseDataDir == "" {
			licenseDataDir = "/var/lib/kx-gateway"
		}

		// 从配置加载 License 加密密钥
		licensingCrypto := &licensing.CryptoConfig{
			JWTSecret:     []byte(jwtSecret),
			DefaultExpiry: 24 * time.Hour,
		}
		if cfg.LicensePrivateKey != "" {
			key, err := licensing.LoadPrivateKeyFromPEM(cfg.LicensePrivateKey)
			if err != nil {
				slog.Error("failed to load license RSA private key", "error", err)
			} else {
				licensingCrypto.PrivateKey = key
				slog.Info("license RSA private key loaded")
			}
		} else {
			slog.Warn("LLM_GATEWAY_LICENSE_PRIVATE_KEY not set — license signing disabled")
		}
		publicKeyConfigured := false
		if cfg.LicensePublicKey != "" {
			key, err := licensing.LoadPublicKeyFromPEM(cfg.LicensePublicKey)
			if err != nil {
				slog.Error("failed to load license RSA public key", "error", err)
			} else {
				licensingCrypto.PublicKey = key
				publicKeyConfigured = true
				slog.Info("license RSA public key loaded")
			}
		} else {
			slog.Warn("LLM_GATEWAY_LICENSE_PUBLIC_KEY not set — license verification disabled")
		}
		if cfg.LicenseAESKey != "" {
			licensingCrypto.AESKey = []byte(cfg.LicenseAESKey)
			slog.Info("license AES key loaded")
		} else {
			slog.Warn("LLM_GATEWAY_LICENSE_AES_KEY not set — offline activation encryption disabled")
		}
		licensingValidator := licensing.NewValidator(licensingCrypto, licensingStore)
		licensingDeviceManager := licensing.NewDeviceManager(licensingStore, licensingValidator)
		licensingActivator := licensing.NewActivator(licensingCrypto, licensingStore, licensingDeviceManager)
		licensingOffline := licensing.NewOfflineManager(licensingCrypto, licensingStore)
		licensingHandler := licensing.NewAdminHandler(licensingStore, licensingCrypto, licensingActivator, licensingOffline, licensingValidator)
		adminGroup := e.Group("/api/admin", requireSuperAdmin)
		licensingHandler.RegisterRoutes(adminGroup)
		licensing.RegisterModuleRoutes(adminGroup, licensingStore)
		slog.Info("Phase 2: Licensing API enabled (/api/admin/licenses, /api/admin/modules)")

		// 2026-07-13: Customer-facing license endpoints. Unauthenticated by
		// design — the customer must query status & activate before any
		// admin login. Mounted under /api/system/license/ on the same mux.
		licensingCustomerAPI := licensing.NewCustomerAPI(licensingStore, licensingActivator, licensingOffline)
		licensingCustomerAPI.SetTrialAuthorityURL(os.Getenv("LICENSE_AUTHORITY_URL"))
		customerLicenseGroup := customerEcho.Group("/api/system/license")
		customerLicenseGroup.Use(noAuthCustomerMiddleware())
		licensingCustomerAPI.RegisterRoutes(customerLicenseGroup)
		licensingCustomerAPI.RegisterTelemetryPreferenceRoutes(e.Group("/api/tenant/telemetry-preference"))

		// First-boot bootstrap: status / fingerprint / activate / activate-quick (issue+import).
		licensingBootstrap := licensing.NewBootstrapHandler(licensingValidator, licensingActivator, licensingOffline, licensingStore, publicKeyConfigured)
		customerBootstrapGroup := customerEcho.Group("/api/system/bootstrap")
		customerBootstrapGroup.Use(noAuthCustomerMiddleware())
		licensingBootstrap.RegisterRoutes(customerBootstrapGroup)
		slog.Info("Bootstrap API enabled (/api/system/bootstrap/*)")

		// v2 Phase 3B-1: License health endpoints for ops dashboards
		// (DaemonHealth snapshot + grace state). No auth — these
		// expose only metadata about the licensing subsystem itself.
		licensing.NewLicenseHealthHandler(licenseDataDir, licensing.DefaultGracePeriod).RegisterHealthRoutes(customerLicenseGroup)
		slog.Info("Phase 3B-1: License health endpoints enabled (/api/system/license/health, /api/system/license/status)")

		// Phase 3: Fault Management (故障自愈)
		faultStore := fault.NewPgxStore(pool)
		faultActionExecutor := fault.NewActionExecutor()
		faultRuleEngine := fault.NewRuleEngine(faultStore, faultActionExecutor)
		faultDetector := fault.NewDetector(faultStore, faultRuleEngine)
		faultHandler := fault.NewAdminHandler(faultStore, faultDetector, faultRuleEngine)
		faultHandler.RegisterRoutes(adminGroup.Group("/faults"))
		slog.Info("Phase 3: Fault Management API enabled (/api/admin/faults/*)")

		// Phase 4: Auto-update (自动升级)
		autoupdateStore := autoupdate.NewPgxStore(pool)
		autoupdateDownloader := autoupdate.NewDownloader("/tmp/downloads")
		autoupdateInstaller := autoupdate.NewInstaller("/usr/local/bin/llm-gateway-go", "/var/backups/llm-gateway", "/var/lib/llm-gateway")
		autoupdateRollback := autoupdate.NewRollback("/usr/local/bin/llm-gateway-go", "/var/backups/llm-gateway", "/var/lib/llm-gateway")
		autoupdateAPI := autoupdate.NewAdminAPI(autoupdateStore, autoupdateDownloader, autoupdateInstaller, autoupdateRollback)
		// 前端使用 /api/admin/releases，前端期望不含 /autoupdate 前缀
		autoupdateAPI.RegisterRoutes(adminGroup.Group("/releases"))
		slog.Info("Phase 4: Auto-update API enabled (/api/admin/releases/*)")

		autoupdateAPI.RegisterRoutes(adminGroup.Group("/autoupdate"))

		// 2026-07-13: Customer-facing upgrade endpoints. Read-only — actual
		// upgrade execution stays on /api/admin/releases/*. These allow the
		// customer's browser to see "an update is available" before login.
		autoupdateCustomerAPI := autoupdate.NewCustomerAPI(autoupdateStore, currentGatewayVersionProvider(), autoupdate.ChannelStable)
		customerUpgradeGroup := customerEcho.Group("/api/system/upgrade")
		customerUpgradeGroup.Use(noAuthCustomerMiddleware())
		autoupdateCustomerAPI.RegisterRoutes(customerUpgradeGroup)
		slog.Info("Phase 4: Customer upgrade API enabled (/api/system/upgrade/*)")

		// Phase 5: Center Ops (中心运维)
		centerStore := center.NewPgxStore(pool)
		centerServer := center.NewServer(centerStore)
		centerAPI := center.NewAdminAPI(centerServer, centerStore)
		centerAPI.RegisterRoutes(adminGroup.Group("/center"))
		slog.Info("Phase 5: Center Ops API enabled (/api/admin/center/*)")

		// Phase 7: VibeCoding
		vibecodingStore := vibecoding.NewPgxStore(pool)
		vibecodingProjectManager := vibecoding.NewProjectManager(vibecodingStore)
		vibecodingSessionManager := vibecoding.NewSessionManager(vibecodingStore)
		vibecodingReviewManager := vibecoding.NewReviewManager(vibecodingStore)
		vibecodingAPI := vibecoding.NewAdminAPI(vibecodingProjectManager, vibecodingSessionManager, vibecodingReviewManager)
		vibecodingAPI.RegisterRoutes(adminGroup.Group("/vibecoding"))
		tenantops.NewHandler(pool).RegisterRoutes(e.Group("/api/tenant", jwtMiddleware))
		slog.Info("Phase 7: VibeCoding API enabled (/api/admin/vibecoding/*)")

		// Phase 8: Distribution was here. The whole distribution/ package has
		// been retired to _to_be_deleted/distribution-v1/ (B1 of the maintain
		// migration). The /api/downloads/*, /api/donations/*,
		// /api/public/offline-activation/* and /llm-gateway-go/* (artifact)
		// paths are now served by ai-native-maintain via the
		// newMaintainGatewayHandler reverse proxy registered in B0, which also
		// tags those legacy prefixes with a Deprecation header.
		slog.Info("Phase 8: Distribution retired to maintain (see _to_be_deleted/distribution-v1/)")

		// 将 Echo 挂载到 http.ServeMux
		mux.Handle("/api/admin/", e)
		mux.Handle("/api/tenant/", e)
		// Customer-facing endpoints use an Echo instance without the admin JWT
		// middleware. Keep this mount separate from the admin instance so public
		// activation and status checks remain reachable before login.
		mux.Handle("/api/system/license/", customerEcho)
		mux.Handle("/api/system/upgrade/", customerEcho)
		mux.Handle("/api/system/bootstrap/", customerEcho)
		slog.Info("运维平台 API 已注册 (4 modules via Echo bridge; distribution → maintain)")
	}

	slog.Info("CHECKPOINT: before middleware chain")
	// wrapAdmin wraps a handler with admin JWT/API-key authentication.
	// Used for Phase 2/3 admin endpoints registered outside RegisterRoutes.
	var wrapAdmin func(http.HandlerFunc) http.HandlerFunc
	if dbConn != nil {
		pool := dbConn.Pool()
		secret := cfg.SecretKey
		wrapAdmin = newWrapAdmin(pool, secret)
	}
	var wrapSessionAnalytics func(http.HandlerFunc) http.HandlerFunc
	if dbConn != nil {
		pool := dbConn.Pool()
		secret := cfg.SecretKey
		wrapSessionAnalytics = newWrapSessionAnalytics(pool, secret)
	}

	// ── 质量画像更新器（先创建，后续启动和注册 API）───────────────────
	var profileUpdater *quality.ProfileUpdater
	if dbConn != nil && dbConn.Enabled() {
		profileUpdater = quality.NewProfileUpdater(
			dbConn.Stdlib(),
			quality.WithUpdateInterval(1*time.Hour),
			quality.WithUpdateTimeout(5*time.Minute),
		)
	}

	// Phase 2: Meta-tools API routes
	if dbConn != nil && dbConn.Enabled() {
		metaHandler := metatools.NewHandler(dbConn.Pool())
		metaAPI := admin.NewMetaToolsHandler(metaHandler)
		mux.HandleFunc("/api/meta-tools/definitions", wrapAdmin(metaAPI.GetMetaToolDefinitions))
		mux.HandleFunc("/api/meta-tools/categories", wrapAdmin(metaAPI.ListCategories))
		mux.HandleFunc("/api/meta-tools/load", wrapAdmin(metaAPI.LoadTools))
		slog.Info("Phase 2 meta-tools API enabled (/api/meta-tools/*)")
	}

	// Phase 3: Tool Registry Admin API routes
	if toolRegistryAPI != nil && wrapAdmin != nil {
		mux.HandleFunc("/api/admin/tools/reload", wrapAdmin(toolRegistryAPI.HandleReload))
		mux.HandleFunc("/api/admin/tools/list", wrapAdmin(toolRegistryAPI.HandleList))
		mux.HandleFunc("/api/admin/tools/get", wrapAdmin(toolRegistryAPI.HandleGet))
		slog.Info("Phase 3 tool registry admin API enabled (/api/admin/tools/*)")
	}

	// ── 质量画像 API ───────────────────────────────────────────────────
	if dbConn != nil && dbConn.Enabled() && profileUpdater != nil {
		qualityHandler := handlers.NewQualityHandler(dbConn.Stdlib(), profileUpdater)
		mux.Handle("/api/quality/", qualityHandler)
		slog.Info("质量画像 API 已启用", "routes", []string{
			"GET /api/quality/summary",
			"GET /api/quality/providers/:id",
			"GET /api/quality/ranking",
			"POST /api/quality/providers/:id/recalculate",
		})
	}

	// Phase 3.4: Tool Policy Admin API routes
	if dbConn != nil && toolRegistry != nil {
		policyAPI := admin.NewPolicyAPI(dbConn.Pool(), toolRegistry)
		mux.HandleFunc("/api/admin/policies", wrapAdmin(policyAPI.HandleCreate))
		mux.HandleFunc("/api/admin/policies/list", wrapAdmin(policyAPI.HandleList))
		mux.HandleFunc("/api/admin/policies/delete", wrapAdmin(policyAPI.HandleDelete))
		mux.HandleFunc("/api/admin/policies/check", wrapAdmin(policyAPI.HandleCheck))
		slog.Info("Phase 3.4 tool policy admin API enabled (/api/admin/policies/*)")

		// Phase 3.3: Usage Statistics API
		statsAPI := admin.NewUsageStatsAPI(dbConn.Pool())
		mux.HandleFunc("/api/admin/tools/stats", wrapAdmin(statsAPI.HandleStats))
		mux.HandleFunc("/api/admin/tools/top", wrapAdmin(statsAPI.HandleTopTools))
		slog.Info("Phase 3.3 tool usage stats API enabled (/api/admin/tools/stats, /top)")

		// Phase 3.5: Session Compare & Handoff API
		// compareAPI was hoisted to function scope for the canonical plugin
		// turns route (registered earlier). If adminHandler was nil at that
		// point (canon block skipped), compareAPI is still nil — construct it
		// here so the admin /api/admin/session-compare route still works.
		if compareAPI == nil {
			compareAPI = admin.NewSessionCompareAPI(dbConn.Pool())
		}
		mux.HandleFunc("/api/admin/session-compare", wrapAdmin(compareAPI.HandleCompare))
		handoffAPI := admin.NewHandoffAPI(dbConn.Pool())
		mux.HandleFunc("/api/admin/session-handoff", wrapAdmin(handoffAPI.HandleHandoff))
		// Phase 3.6 (2026-07-09): Handoff logs read-only endpoints.
		// /api/admin/handoff/logs[/{id}] + /api/admin/handoff/stats — supply
		// operator UI with the trigger history written by the new handoff hook
		// (domains/hooks/handoff/trigger_hook.go).
		handoffLogsAPI := admin.NewHandoffLogsHandler(dbConn.Pool())
		handoffLogsAPI.RegisterRoutes(mux, wrapAdmin)
		slog.Info("Phase 3.5 session compare & handoff API enabled (/api/admin/session-compare, /session-handoff)")
		slog.Info("Phase 3.6 handoff logs API enabled (/api/admin/handoff/logs, /stats)")

		// 会话迁移方案：Session Export / Import / Pack API
		// GET  /api/admin/session-export?id=<gw_session_id>&tenant=<t>      导出迁移包
		// POST /api/admin/session-export/import?tenant=<t>                  导入迁移包到 staging
		// GET  /api/admin/session-export/pack?id=<pack_id>&tenant=<t>       拉取已导入的迁移包
		exportAPI := admin.NewSessionExportAPI(dbConn.Pool())
		mux.HandleFunc("/api/admin/session-export", wrapAdmin(exportAPI.ServeHTTP))
		mux.HandleFunc("/api/admin/session-export/import", wrapAdmin(exportAPI.ServeHTTP))
		mux.HandleFunc("/api/admin/session-export/pack", wrapAdmin(exportAPI.ServeHTTP))
		slog.Info("session migration API enabled (/api/admin/session-export{,/import,/pack})")

		// Phase 3.5: Session List & Detail API
		// NOTE (2026-07-06): removed duplicate registration here. admin/handler.go
		// already registers /api/admin/sessions via handleListSessions + handleSessionSubrouter
		// (which is a strict superset including detail, cred-rotations, stop, recover,
		// annotation, etc.). Keeping both caused ServeMux panic at startup.

		// Phase 4: Session Analytics API (会话全景分析)
		// 350 迁移修复 session_summaries 聚合链路后启用。
		if adminHandler != nil {
			mux.HandleFunc("/api/admin/session-analytics", wrapSessionAnalytics(adminHandler.HandleSessionAnalyticsList))
			mux.HandleFunc("/api/admin/session-analytics/", wrapSessionAnalytics(adminHandler.RouteSessionAnalytics))
			mux.HandleFunc("/api/admin/session-clusters", wrapSessionAnalytics(adminHandler.HandleSessionClustersList))
			mux.HandleFunc("/api/admin/session-clusters/", wrapSessionAnalytics(adminHandler.RouteSessionClusters))
			// Task T1.1: 时间序列分析端点 (2026-07-06)
			mux.HandleFunc("/api/admin/session-analytics/activity", wrapSessionAnalytics(adminHandler.HandleActivityTrend))
			mux.HandleFunc("/api/admin/session-analytics/cost-trend", wrapSessionAnalytics(adminHandler.HandleCostTrend))
			mux.HandleFunc("/api/admin/session-analytics/latency-trend", wrapSessionAnalytics(adminHandler.HandleLatencyTrend))
			mux.HandleFunc("/api/admin/session-analytics/health-trend", wrapSessionAnalytics(adminHandler.HandleHealthTrend))

			slog.Info("Phase 4 session analytics API enabled (/api/admin/session-analytics)")

			// Task T1.3: 会话健康评分后台 worker (2026-07-06)
			// 每小时扫描 last_request_at < now-1h 且 health_score IS NULL 的会话，
			// 批量计算并写入 session_summaries.health_score/grade/outcome。
			// 缺失此 worker 则未主动停止的会话永远不会有健康分。
			if dbConn != nil {
				healthWorker := bg.NewSessionHealthWorker(dbConn.Pool())
				healthWorker.Start(context.Background())
				slog.Info("session health worker started (hourly)")
			}

			// Phase 3.11: Anomaly Harvester (TTL cleanup + fault-event bridge).
			if dbConn != nil {
				ahCfg := streaming.DefaultAnomalyHarvesterConfig()
				anomalyHarvester = streaming.NewAnomalyHarvester(dbConn.Pool(), ahCfg)
				anomalyHarvester.Start()
				slog.Info("anomaly harvester started (phase 3.11)",
					"cleanup_interval", ahCfg.CleanupInterval,
					"retention_days", ahCfg.RetentionDays,
					"bridge_interval", ahCfg.BridgeInterval)
			}
		}

		// Task T1.4: Usage Cost Enhanced API 注册已在 admin/handler.go:572 完成
		// (避免与 admin 包的双重注册 panic, 与 33d9d4fe fix 同型)

		// Phase 3.6: Credential Success Rate Management (2026-06-23)
		mux.HandleFunc("/api/admin/credential-success-rates", wrapAdmin(admin.HandleCredentialSuccessRates(dbConn.Pool())))
		mux.HandleFunc("/api/admin/credential-success-rates/reset", wrapAdmin(admin.HandleResetCredentialSuccessRate(dbConn.Pool())))
		slog.Info("Phase 3.6 credential success rate management enabled (/api/admin/credential-success-rates)")

		// Phase 3.6.5 (2026-07-24): Sessions V2 Detail & Summary API
		// Provides session detail query from gateway.session_* tables and LLM-powered session summary
		sessionDetailAPI := admin.NewSessionDetailV2API(dbConn.Pool())
		sessionSummaryAPI := admin.NewSessionSummaryV2API(dbConn.Pool())
		mux.HandleFunc("/api/admin/sessions/detail", wrapAdmin(sessionDetailAPI.ServeHTTP))
		mux.HandleFunc("/api/admin/sessions/summary", wrapAdmin(sessionSummaryAPI.ServeHTTP))
		slog.Info("Phase 3.6.5 sessions v2 API enabled (/api/admin/sessions/detail, /summary)")

		// Phase 3.7 (A3-1): Agent Registry API (Track A APIHub)
		agentsAPI := admin.NewAgentsHandler(apihubSvc)
		mux.HandleFunc("/api/agents", wrapAdmin(agentsAPI.List))
		mux.HandleFunc("/api/agents/stats", wrapAdmin(agentsAPI.Stats))
		mux.HandleFunc("/api/agents/health", wrapAdmin(agentsAPI.Health))
		mux.HandleFunc("/api/agents/", func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/link") && r.Method == http.MethodPost:
				wrapAdmin(agentsAPI.Link)(w, r)
			case strings.HasSuffix(r.URL.Path, "/neighbors") && r.Method == http.MethodGet:
				wrapAdmin(agentsAPI.Neighbors)(w, r)
			case r.Method == http.MethodGet:
				wrapAdmin(agentsAPI.Get)(w, r)
			default:
				http.NotFound(w, r)
			}
		})
		slog.Info("Phase 3.7+6 agent registry API enabled (/api/agents, /:id, /:id/link, /:id/neighbors, /stats)")

		// Phase 3.8 (2026-06-28): Probe Health Dashboard API
		adminHandler.RegisterProbeDashboardRoutes(mux, wrapAdmin)
		slog.Info("Phase 3.8 probe health dashboard API enabled (/api/admin/probe/*)")

		// Phase 3.9 (2026-07-02, Task D2): Approval Request Query API
		// Provides REST API for querying, approving, and rejecting approval requests
		// with statistics support. Complements the existing admin approval handlers.
		if approvalMgr != nil {
			approvalAPI := api.NewApprovalHandler(approvalMgr, api.NewAdminAuthAdapter())
			mux.HandleFunc("/api/v1/approvals/", func(w http.ResponseWriter, r *http.Request) {
				// Route to appropriate handler based on path suffix
				path := r.URL.Path
				switch {
				case strings.HasSuffix(path, "/approve"):
					wrapAdmin(approvalAPI.ApproveApproval)(w, r)
				case strings.HasSuffix(path, "/reject"):
					wrapAdmin(approvalAPI.RejectApproval)(w, r)
				case strings.HasSuffix(path, "/resume"):
					wrapAdmin(adminHandler.HandleApprovalResume)(w, r)
				case strings.Contains(path, "/approvals/") && !strings.HasSuffix(path, "/approvals/"):
					wrapAdmin(approvalAPI.GetApproval)(w, r)
				default:
					http.NotFound(w, r)
				}
			})
			mux.HandleFunc("/api/admin/approvals", wrapAdmin(approvalAPI.ListApprovals))
			mux.HandleFunc("/api/admin/approvals/stats", wrapAdmin(approvalAPI.GetApprovalStats))
			slog.Info("Phase 3.9 approval query API enabled (/api/v1/approvals/*, /api/admin/approvals/stats)")

			// DingTalk approval callback (钉钉机器人审批回调)
			// 签名校验密钥优先读取 dingtalk_bot.* 模块设置，回退到环境变量，
			// 保证模块开关与配置真正控制回调验签。
			dingSignSecret := os.Getenv("DINGTALK_SIGN_SECRET")
			if dingSignSecret == "" {
				dingSignSecret = os.Getenv("DINGTALK_APP_SECRET")
			}
			if cfg, ok := dingTalkConfigFromSettings(); ok {
				if cfg.SignSecret != "" {
					dingSignSecret = cfg.SignSecret
				} else if cfg.AppSecret != "" {
					dingSignSecret = cfg.AppSecret
				}
			}
			if dingSignSecret != "" {
				api.RegisterDingTalkRoutes(mux, approvalMgr, dingSignSecret)
				slog.Info("dingtalk approval callback enabled (/api/webhooks/dingtalk/approval-callback)")
			} else {
				slog.Warn("DINGTALK_SIGN_SECRET not set, dingtalk approval callback disabled")
			}
		}

		// Phase 3.10 (2026-07-03, Task D1): Approval Configuration Management API
		// Provides REST API for managing approval configuration, approvers, and rules.
		// Enables tenant admins to configure approval workflows.
		if dbConn != nil && dbConn.Enabled() && redisClientForCache != nil {
			approvalStore := approval.NewPGApprovalStore(dbConn.Pool(), redisClientForCache.Client())
			approvalConfigMgr := approval.NewConfigManager(approvalStore, redisClientForCache.Client())
			approvalConfigHandler := admin.NewApprovalConfigHandler(approvalConfigMgr)

			// Configuration endpoints
			// (2026-07-03 fix) Changed path from /api/admin/tenants/ to
			// /api/admin/tenant-approval-config/ to avoid conflict with the
			// legacy tenant handler at admin/handler.go:407 which also
			// owns /api/admin/tenants/. Both paths map to the same URL
			// pattern in net/http.ServeMux, so registering both caused
			// a runtime panic during startup (caught by the new recover
			// in main's top-level defer).
			mux.HandleFunc("/api/admin/tenant-approval-config/", func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.Path
				switch {
				case strings.Contains(path, "/approval-config/stats"):
					wrapAdmin(approvalConfigHandler.GetConfigStats)(w, r)
				case strings.Contains(path, "/approval-config") && r.Method == http.MethodGet:
					wrapAdmin(approvalConfigHandler.GetConfig)(w, r)
				case strings.Contains(path, "/approval-config") && r.Method == http.MethodPut:
					wrapAdmin(approvalConfigHandler.UpdateConfig)(w, r)
				case strings.Contains(path, "/approvers/") && r.Method == http.MethodPut:
					wrapAdmin(approvalConfigHandler.UpdateApprover)(w, r)
				case strings.Contains(path, "/approvers/") && r.Method == http.MethodDelete:
					wrapAdmin(approvalConfigHandler.DeleteApprover)(w, r)
				case strings.Contains(path, "/approvers") && r.Method == http.MethodGet:
					wrapAdmin(approvalConfigHandler.GetApprovers)(w, r)
				case strings.Contains(path, "/approvers") && r.Method == http.MethodPost:
					wrapAdmin(approvalConfigHandler.AddApprover)(w, r)
				case strings.Contains(path, "/approval-rules/") && r.Method == http.MethodDelete:
					wrapAdmin(approvalConfigHandler.DeleteRule)(w, r)
				case strings.Contains(path, "/approval-rules") && r.Method == http.MethodGet:
					wrapAdmin(approvalConfigHandler.GetRules)(w, r)
				case strings.Contains(path, "/approval-rules") && r.Method == http.MethodPost:
					wrapAdmin(approvalConfigHandler.AddRule)(w, r)
				default:
					http.NotFound(w, r)
				}
			})
			slog.Info("Phase 3.10 approval configuration API enabled (/api/admin/tenant-approval-config/{id}/approval-config, /approvers, /approval-rules)")
		}
	}

	slog.Info("CHECKPOINT: before middleware stack build")
	// ── Middleware stack (declarative chain) ─────────────────────────────
	//
	// NET-005 fix: 新增 SecurityHeadersMiddleware 在 chain 最内侧（紧贴
	// mux），保证所有响应（包括 panic 兜底、SSE 流、metrics scrape）都附
	// 加安全响应头。位置选择：紧贴 mux 确保 Recovery 的 panic 响应也带
	// 头；选在 Cors/Prometheus 之外避免被后续中间件覆盖。
	handler := middleware.NewBuilder().
		Add(middleware.NewRecoveryMiddleware()).
		Add(middleware.NewRequestIDMiddleware()).
		Add(middleware.NewLocaleMiddleware(cfg.DefaultLanguage)). // i18n: before auth so auth errors localize too
		Add(middleware.NewCORSMiddleware(cfg.CORSOrigins)).
		Add(middleware.NewPrometheusMiddleware()).
		Add(middleware.NewAuthMiddleware(cfg.APIKey)).
		Add(middleware.NewOriginMiddleware()).
		Add(middleware.NewLoggingMiddleware()).
		Add(middleware.NewSecurityHeadersMiddleware()).
		Build().
		Then(mux)

	slog.Info("CHECKPOINT: after middleware build, before http.Server init")

	// Wrap the final handler with the maintain gateway: reverse-proxies
	// /maintain-api/* (canonical) and legacy /api/* ops prefixes (with
	// Deprecation headers) to the maintain backend, and serves maintain-web
	// under /maintain/*. When MAINTAIN_SERVICE_URL is unset this is a no-op
	// pass-through, preserving the pre-migration rollback path. This call
	// was previously missing — the proxy existed but was never mounted.
	finalHandler := newMaintainGatewayHandler(handler, maintainStatic)

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: finalHandler,
		// ReadHeaderTimeout: headers only. ReadTimeout covers the full request
		// body window from connection accept (see net/http readRequest). The
		// previous 10s total caused body_read_error when clients uploaded
		// large chat payloads slowly — production logs showed latency_ms ≈ 10001.
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout 从 120s → 300s: LLM 推理（特别是 reasoning models）
		// 可能超过 2 分钟。SSE streaming 期间持续有数据传输，不受 IdleTimeout 限制，
		// 但 ReadTimeout 是整个请求的上限（从接受连接到最后一个 byte）。
		ReadTimeout:  300 * time.Second,
		WriteTimeout: 0,
		// IdleTimeout 从 60s → 300s: 匹配 ReadTimeout，防止 keepalive 连接
		// 在 LLM 推理期间被提前关闭。IdleTimeout 是指连接完全空闲（无读写）的时间。
		IdleTimeout:    300 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	// Wrap handler with h2c so the same listener accepts BOTH HTTP/1.1 and
	// HTTP/2 cleartext. h2c.NewHandler falls back to HTTP/1.1 when the
	// client doesn't send the HTTP/2 preface, so existing HTTP/1.1 clients
	// (older curl, most monitoring agents) keep working unchanged.
	//
	// Why this matters (2026-07-16):
	//   252 nginx fronts llm.kxpms.cn with `http2 on;` (HTTP/2 to client)
	//   and proxies to gateway over HTTP/1.1 with `proxy_buffering off;`.
	//   That combination caused `HTTP/2 stream ... INTERNAL_ERROR`
	//   surfaced as `net::ERR_HTTP2_PROTOCOL_ERROR` in VSCode Copilot.
	//   With h2c on the gateway, 252 nginx can speak HTTP/2 to the backend
	//   (`proxy_http_version 1.1` becomes optional) and HTTP/2 frame
	//   conversion stays correct end-to-end.
	srv.Handler = h2c.NewHandler(handler, &http2.Server{
		MaxConcurrentStreams: 250,
		MaxReadFrameSize:     1 << 20,
		// IdleTimeout 从 60s → 300s: 匹配 http.Server.IdleTimeout。
		// 对于 LLM reasoning models (o1/o3/deepseek-reasoner)，推理时间可能
		// 超过 2 分钟。HTTP/2 connection 在 SSE streaming 期间有持续 DATA frame，
		// 不算 idle，但 connection 复用的 keepalive 窗口需要足够长。
		IdleTimeout: 300 * time.Second,
	})
	var pprofSrv *http.Server
	if pprofAddr := strings.TrimSpace(os.Getenv("LLM_GATEWAY_PPROF_LISTEN")); pprofAddr != "" {
		var enabled bool
		pprofSrv, enabled = newPprofServer(pprofAddr)
		if !enabled {
			slog.Error("LLM_GATEWAY_PPROF_LISTEN must be a loopback address", "listen", pprofAddr)
			os.Exit(2)
		}
		go func() {
			slog.Info("pprof diagnostic server listening", "listen", pprofAddr)
			if err := pprofSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("pprof diagnostic server failed", "error", err)
			}
		}()
	}

	// ── 启动质量指标采集器 ───────────────────────────────────────────────
	if dbConn != nil && dbConn.Enabled() {
		qualityCollectorEnabled := os.Getenv("QUALITY_COLLECTOR_ENABLED")
		if qualityCollectorEnabled == "" || qualityCollectorEnabled == "true" {
			slog.Info("启动质量指标采集器")
			qualityCollector := quality.New(
				dbConn.Stdlib(),
				quality.WithMinuteInterval(1*time.Minute),
				quality.WithHourInterval(1*time.Hour),
				quality.WithTimeout(30*time.Second),
			)
			go func() {
				if err := qualityCollector.Start(context.Background()); err != nil {
					slog.Error("质量采集器退出", "error", err)
				}
			}()
		} else {
			slog.Info("质量指标采集器已禁用", "QUALITY_COLLECTOR_ENABLED", qualityCollectorEnabled)
		}
	}

	// ── 启动质量画像更新器 ───────────────────────────────────────────────
	if profileUpdater != nil {
		profileUpdaterEnabled := os.Getenv("PROFILE_UPDATER_ENABLED")
		if profileUpdaterEnabled == "" || profileUpdaterEnabled == "true" {
			slog.Info("启动质量画像更新器")

			go func() {
				if err := profileUpdater.Start(context.Background()); err != nil {
					slog.Error("质量画像更新器退出", "error", err)
				}
			}()
		} else {
			slog.Info("质量画像更新器已禁用", "PROFILE_UPDATER_ENABLED", profileUpdaterEnabled)
		}
	}

	slog.Info("CHECKPOINT: HTTP server configured, about to start", "listen", cfg.Listen)

	// ── Graceful shutdown ─────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("gateway listening", "listen", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("gateway listen failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("gateway shutting down")

	// 1. Stop accepting new connections — in-flight requests drain naturally
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("gateway shutdown error", "error", err)
	}
	if pprofSrv != nil {
		if err := pprofSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("pprof diagnostic server shutdown error", "error", err)
		}
	}

	// ── Stop background services with a global timeout ──
	// systemd TimeoutStopSec is 25s; srv.Shutdown uses ~5s for
	// in-flight HTTP drain, leaving ~20s for all Stop() calls.
	// If they exceed this budget the process exits anyway (SIGKILL
	// from systemd), but a clean(ish) log is better than a silent kill.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopCancel()
	stopDone := make(chan struct{}, 1)

	go func() {
		// 2026-07-22: 停止 URSM v2 persist writer（如果已启动）
		if persistWriterStop != nil {
			persistWriterStop()
		}

		// Stop probe/state services before closing their shared dependencies.
		if activeProbe != nil {
			activeProbe.Stop()
		}
		if probeQueueWorker != nil {
			probeQueueWorker.Stop()
		}
		if credProbeV2 != nil {
			credProbeV2.Stop()
		}
		if stateManager != nil {
			stateManager.Stop()
		}

		// 2. Stop hub/background producers before closing their dependencies.
		if liveStreamHub != nil {
			liveStreamHub.Stop()
		}
		if anomalyHarvester != nil {
			anomalyHarvester.Stop()
		}
		telemetryClient.Stop()
		lim.Stop()
		pools.Stop()
		pools.CloseAll()
		upClient.Stop()

		// 3. Stop background services last
		if discoverySvc != nil {
			discoverySvc.Stop()
		}
		if credRecovery != nil {
			credRecovery.Stop()
		}
		if brokenProbeReviver != nil {
			brokenProbeReviver.Stop()
		}
		if pendingSweeper != nil {
			pendingSweeper.Stop()
		}
		if credCycler != nil {
			credCycler.Stop()
		}
		if modelProbe != nil {
			modelProbe.Stop()
		}
		if suspiciousProbe != nil {
			suspiciousProbe.Stop()
		}
		if unifiedProbe != nil {
			unifiedProbe.Stop()
		}
		if passiveProbe != nil {
			passiveProbe.Stop()
		}
		if taxonomySync != nil {
			taxonomySync.Stop()
		}
		if stickyCleaner != nil {
			stickyCleaner.Stop()
		}
		if partitionManager != nil {
			partitionManager.Stop()
		}
		if envelopeCleaner != nil {
			envelopeCleaner.Stop()
		}
		if settingsAuditCleaner != nil {
			settingsAuditCleaner.Stop()
		}
		if peakCollector != nil {
			peakCollector.Stop()
		}
		if weeklyPeakRollup != nil {
			weeklyPeakRollup.Stop()
		}
		if statsBoardCache != nil {
			statsBoardCache.Stop()
		}
		if statsMinuteAccumulator != nil {
			statsMinuteAccumulator.Stop()
		}
		if statsMinuteRollup != nil {
			statsMinuteRollup.Stop()
		}
		if slotSuggester != nil {
			slotSuggester.Stop()
		}
		if autoIndexRefresher != nil {
			autoIndexRefresher.Stop()
			if autoRouteListener != nil {
				autoRouteListener.Stop()
			}
			if healthAutoRecover != nil {
				healthAutoRecover.Stop()
			}
		}
		// Provider Profile System shutdown (Phase 1, 2026-07-26)
		stopProviderProfile(profileWorkers)
		// Drain the Memora sink queue on shutdown so in-flight writes
		// are not lost. Bounded to 5s so shutdown is not held hostage
		// to a slow Memora.
		if memorySvc != nil {
			memStopCtx, memStopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			memorySvc.Stop(memStopCtx)
			memStopCancel()
		}
		if anomalyReporter != nil {
			if err := anomalyReporter.Close(); err != nil {
				slog.Warn("anomaly_reporter: shutdown failed", "error", err)
			}
		}
		if rawDataLogger != nil {
			if err := rawDataLogger.Close(); err != nil {
				slog.Warn("raw_data_logger: shutdown failed", "error", err)
			}
		}

		close(stopDone)
	}()

	select {
	case <-stopDone:
		slog.Info("background services stopped cleanly")
	case <-stopCtx.Done():
		slog.Warn("background service stop timed out, forcing shutdown")
	}

	// Flush + close the rotated log file last so the final
	// "gateway stopped" record (and any deferred background-task
	// shutdown logs above) make it to disk.
	if err := logging.Shutdown(); err != nil {
		fmt.Fprintf(os.Stderr, "logging: shutdown error: %v\n", err)
	}

	slog.Info("gateway stopped")
}

// pendingStoreAdapter, sessionAuthAdapter, extractTenantIDFromUpstreamResp,
// markCapturedPendingInProgress, saveCapturedPending, buildAutoLLMCaller,
// and irAdapter were moved to main_types.go as part of the P0 main.go split
// refactor. See docs/refactor-plans/main-go-split.md.

// syncRateLimitGateFromSettings, applyLogSettingsToLogging, readIntSettingPublic,
// readBoolSettingPublic, parseIntSetting, parseBoolSetting, and readBoolSettingValue
// were moved to main_settings.go as part of the P0 main.go split refactor.
// See docs/refactor-plans/main-go-split.md.

// initApprovalNotifier, dingTalkConfigFromSettings, hasCompleteDingTalkAppConfig,
// dingTalkAllowedUsersFromSettings, dingTalkCallbackSecretFromSettings, and
// dingTalkUserIsAllowed were moved to main_notification.go as part of the P0
// main.go split refactor. See docs/refactor-plans/main-go-split.md.

// adminLiveRequestFromEntry, liveStreamEventTime, and incidentUpdateFromResult
// were moved to main_livestream.go as part of the P0 main.go split refactor.
// See docs/refactor-plans/main-go-split.md.
