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
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/api"
	"github.com/kaixuan/llm-gateway-go/apihub"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/disguise"
	"github.com/kaixuan/llm-gateway-go/domains/analysis/bus"                        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/approval"                            //nolint:depguard // D1: approval config management
	"github.com/kaixuan/llm-gateway-go/domains/assets"                              //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/attachments"                         //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/authentication"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credential"                          //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"                         //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"                   //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	sessionaudithook "github.com/kaixuan/llm-gateway-go/domains/hooks/sessionaudit" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/notification"                        //nolint:depguard // 审批通知器
	"github.com/kaixuan/llm-gateway-go/domains/session"                             //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"                        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	streaming "github.com/kaixuan/llm-gateway-go/domains/streaming"                 //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
	"github.com/kaixuan/llm-gateway-go/internal/modelpolicy"
	"github.com/kaixuan/llm-gateway-go/internal/observability"
	"github.com/kaixuan/llm-gateway-go/maas"
	"github.com/kaixuan/llm-gateway-go/metatools"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/registry"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/kaixuan/llm-gateway-go/security/armor"
	"github.com/kaixuan/llm-gateway-go/settings"
	upstream "github.com/kaixuan/llm-gateway-go/upstream"
	"github.com/redis/go-redis/v9"
)

// sessionAuditApprovalTimeoutFromEnv 读取 SESSION_AUDIT_APPROVAL_TIMEOUT
// 环境变量并解析为 time.Duration。支持 "30s" / "15m" / "1h" 格式。
// 无效输入或缺失时退化为 15m。2026-06-27 audit fix。
func sessionAuditApprovalTimeoutFromEnv() time.Duration {
	const def = 15 * time.Minute
	v := os.Getenv("SESSION_AUDIT_APPROVAL_TIMEOUT")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		slog.Warn("invalid SESSION_AUDIT_APPROVAL_TIMEOUT, using default",
			"value", v, "default", def.String())
		return def
	}
	return d
}

func main() {
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
	var slotSuggester *bg.SlotSuggester
	var autoIndexRefresher *bg.AutoIndexRefresher
	// memorySvc holds the legacy memora concrete client/sink behind the
	// live memory.Reader / memory.Writer interfaces used by gateway runtime.
	var memorySvc *legacyMemoryServices

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

	cm := credential.NewManager()
	lim := credential.NewLimiter()

	matrixPath := transformation.DefaultMatrixPath()
	matrix := transformation.New(matrixPath)

	resolver := resolve.NewResolver("", 120*time.Second)

	auditSink := audit.NewMultiSink(
		&audit.LogSink{},
		audit.NewJSONSink(10000),
	)

	upClient := upstream.New()
	slog.Info("upstream proxy resolver initialised",
		"proxy_configured", upClient.ProxyStatus()["proxy"] != "",
		"domestic_hosts", len(upClient.ProxyStatus()["domestic"].([]string)),
	)

	pools := pool.NewPoolManager(upClient.Proxy().ProxyFunc())

	chatHandler := streaming.NewChatHandler(cm, lim, matrix, pools, resolver, auditSink)
	if len(cfg.SessionIDBodyKeys) > 0 {
		streaming.SetSessionIDBodyKeys(cfg.SessionIDBodyKeys)
	}
	healthHandler := streaming.NewHealthHandler(cm, lim, upClient.Proxy())
	modelsHandler := streaming.NewModelsHandler()
	messagesHandler := streaming.NewMessagesHandler(chatHandler)
	responsesHandler := streaming.NewResponsesHandler(chatHandler)

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
			ttl := time.Duration(cfg.SessionTTLHours) * time.Hour
			sessionMgr = session.NewManager(redisClient, ttl)
			chatHandler.SetSessionGetter(sessionMgr)
			redisClientForCache = redisClient
			fpSlotRedis = redis.NewClient(&redis.Options{
				Addr:     cfg.RedisAddr,
				Password: cfg.RedisPassword,
				DB:       cfg.RedisDB,
			})
			pendingStore = pending.NewStore(fpSlotRedis, ttl)
			lastSystemSession = session.NewLastSystemSessionIndex(redisClient)
			sessionPref = session.NewSessionPreference(redisClient)
			slog.Info("session manager enabled", "redis", cfg.RedisAddr, "ttl_hours", cfg.SessionTTLHours)
		} else {
			slog.Warn("session manager: redis ping failed", "error", err)
		}
	} else {
		slog.Warn("session manager disabled (no LLM_GATEWAY_REDIS_ADDR)")
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
		slog.Info("settings: registry initialised",
			"platform_specs", len(settings.Global.AllSpecs()))

		// 2026-07-02: 打通 settings_kv ↔ logging。
		// 启动时 settings 已注册 log.* spec，读取 DB 中的覆盖值并应用到
		// 已初始化的 lumberjack writer（热加载，无需重启）。
		// 这样运维在 UI 改 log.max_size_mb 等配置后，重启即生效；
		// 运行时改则通过 /api/admin/logs/config 的 Reconfigure 即时生效。
		applyLogSettingsToLogging()

		// Phase 3.2: Provider-level settings resolver
		providerSettingsResolver = settings.NewProviderSettingsResolver(dbConn.Pool(), settings.Global)
		slog.Info("settings: provider-level resolver initialised")
	} else {
		slog.Info("settings: registry disabled (no DB)")
	}

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
			func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, normFunc executors.NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) executors.StreamOutcome {
				var pc *streaming.PendingCapturer
				if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
					pc = streaming.NewPendingCapturer(0)
				}
				outcome := streaming.StreamChatWithPendingCapture(w, resp, clientModel, outboundModel, norm, capture, toolsRequested, streaming.StripMinimaxFieldsBody, pc)
				saveCapturedPending(pendingStore, pc, resp)
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
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
			}
			outcome := streaming.StreamAnthropicPassthrough(w, resp, clientModel, outboundModel, requestID, cap, pc)
			saveCapturedPending(pendingStore, pc, resp)
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

		// Q3 streaming
		routingExec.AnthropicToOpenAIStream = func(
			w http.ResponseWriter,
			resp *http.Response,
			clientModel, outboundModel, requestID string,
			cap *audit.StreamCapture,
			pcAny any,
		) executors.StreamOutcome {
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
			}
			outcome := streaming.StreamAnthropicSSEToOpenAI(w, resp, clientModel, outboundModel, requestID, cap, pc)
			saveCapturedPending(pendingStore, pc, resp)
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
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
			}
			outcome := streaming.StreamOpenAIToAnthropicSSE(w, resp, clientModel, outboundModel, requestID, cap, pc)
			saveCapturedPending(pendingStore, pc, resp)
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
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
			}
			outcome := streaming.StreamAnthropicSSEToResponses(w, resp, clientModel, outboundModel, requestID, cap, pc)
			saveCapturedPending(pendingStore, pc, resp)
			return outcome
		}
		routingExec.OpenAIToResponsesStream = func(
			w http.ResponseWriter,
			resp *http.Response,
			clientModel, outboundModel, requestID string,
			cap *audit.StreamCapture,
			pcAny any,
		) executors.StreamOutcome {
			var pc *streaming.PendingCapturer
			if pendingStore != nil && streaming.ClientHasSessionID(w, resp) {
				pc = streaming.NewPendingCapturer(0)
			}
			outcome := streaming.StreamOpenAIToResponsesSSE(w, resp, clientModel, outboundModel, requestID, cap, pc)
			saveCapturedPending(pendingStore, pc, resp)
			return outcome
		}
		routingExec.SanitizeAnthropicTools = streaming.SanitizeAnthropicToolsInBody
		routingExec.NormalizeOpenAITools = streaming.NormalizeToolsInChatBody
		routingExec.StripMinimaxFields = streaming.StripMinimaxFieldsBody
		routingExec.StreamTimeout = time.Duration(cfg.StreamTimeout) * time.Second
		routingExec.UpstreamTimeout = time.Duration(cfg.UpstreamTimeout) * time.Second
		routingExec.StreamRetryThreshold = cfg.StreamRetryThreshold
		// 2026-06-21: 同步重试超时（全候选失败后保持客户端连接继续重试）
		routingExec.SyncRetryTimeout = 120 * time.Second
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

		// 2026-07-07 Phase 1: wire FpSlot degradation tracker.
		// Unconditional — it is in-memory only (Prometheus counters + a
		// 1-minute sliding window) and has no external dependencies, so it
		// is safe to enable in every environment. When nil the executor
		// silently skips recording, so this also documents the contract.
		routingExec.DegradationTracker = executors.NewDegradationTracker()
		slog.Info("fp_slot_degradation_tracker enabled (Phase 1 monitoring)")

		chatHandler.SetExecutor(routingExec, providerClient, stickyCache)
		chatHandler.SetSessionRouting(lastSystemSession, sessionPref)
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
	var liveStreamHub *admin.LiveStreamSSEHub
	if dbConn != nil && dbConn.Enabled() {
		liveStreamHub = admin.NewLiveStreamSSEHub(dbConn.Pool(), admin.LiveStreamConfig{
			BroadcastQueueSize: 2048,
			InitialReplayLimit: 50,
			IdleThreshold:      60 * time.Second,
			IdleTickInterval:   10 * time.Second,
			KeepaliveInterval:  25 * time.Second,
			RedisClient:        fpSlotRedis, // reuse the existing Redis connection
		})
		go liveStreamHub.Run()

		// Wire the telemetry persistence hook → SSE hub. The hook
		// Wire the live-stream SSE hub to the telemetry EmitRequestLog
		// pipeline, so dashboard updates happen immediately from the
		// in-memory request state rather than waiting for DB persistence.
		// The closure runs on the request-handling goroutine (NOT the
		// telemetry worker), so the hub's Publish() MUST be non-blocking
		// (select with default). The provider_id→catalog_code resolution
		// inside is a sync.Map lookup with a 200ms-timeout DB fallback
		// on miss. Bounded to <1ms in the common case.
		if telemetryClient.Enabled() {
			hub := liveStreamHub
			telemetryClient.SetOnRequestLogEmitted(func(entry *telemetry.RequestLogEntry) {
				hub.Publish(adminLiveRequestFromEntry(entry, hub))
			})
			slog.Info("telemetry onEmitted wired → live stream SSE hub (in-memory pipeline)")
		}
		slog.Info("live request stream hub enabled (sse /api/admin/live-stream)")
	}

	// ── Request WAL (Request Logger) ───────────────────────────────────────
	// 2026-06-22: Synchronous initial log + async batch updates for request lifecycle.
	// Uses same DB pool as telemetryClient. Disabled if env var LLM_GATEWAY_REQUEST_WAL_DISABLE=true.
	if dbConn != nil && dbConn.Enabled() && os.Getenv("LLM_GATEWAY_REQUEST_WAL_DISABLE") != "true" {
		requestLogger := telemetry.NewRequestLogger(dbConn.Pool(), &telemetry.RequestLoggerConfig{
			QueueSize:    10000,
			BatchSize:    50,
			FlushTimeout: 100 * time.Millisecond,
			Enabled:      true,
		})
		chatHandler.SetRequestLogger(requestLogger)
		slog.Info("request WAL enabled", "queue_size", 10000, "batch_size", 50)
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
	}

	// ── Attachment Extractor (2026-07-01) ───────────────────────────────
	// 从请求体中提取 base64/data-URI 附件并保存到文件系统。
	// 配置项：
	//   LLM_GATEWAY_ATTACHMENT_DIR: 存储根目录 (默认 ./data/attachments)
	//   LLM_GATEWAY_ATTACHMENT_MAX_SIZE: 单文件上限 (默认 10MB)
	attachmentDir := os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR")
	if attachmentDir == "" {
		attachmentDir = "./data/attachments"
	}
	// attachmentStorage 提升到外层作用域：admin mux 需要它构造下载/列表 handler。
	// 初始化失败时为 nil，对应的 admin 端点会返回 503（见 admin/attachments_routes.go）。
	var attachmentStorage *attachments.Storage
	if storage, err := attachments.NewStorage(attachmentDir); err != nil {
		slog.Warn("attachment storage init failed, extraction disabled", "error", err, "dir", attachmentDir)
	} else {
		attachmentStorage = storage
		// 配置单文件大小上限
		if maxSizeStr := os.Getenv("LLM_GATEWAY_ATTACHMENT_MAX_SIZE"); maxSizeStr != "" {
			if maxSize, parseErr := strconv.ParseInt(maxSizeStr, 10, 64); parseErr == nil && maxSize > 0 {
				attachmentStorage.MaxSize = maxSize
			}
		}
		attachmentExtractor := attachments.NewExtractor(attachmentStorage)
		chatHandler.SetAttachmentExtractor(attachmentExtractor)
		slog.Info("attachment extractor enabled",
			"dir", attachmentStorage.BaseDir(),
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
	var adminHandler *admin.Handler
	var approvalMgr *sessionaudit.ApprovalManager // 2026-06-27: outer-scope so the timeout worker can read it
	if dbConn != nil && dbConn.Enabled() {
		slog.Info("CHECKPOINT: before admin.NewHandler")
		adminHandler = admin.NewHandler(dbConn.Pool(), cfg.SecretKey, fernetKey)
		slog.Info("CHECKPOINT: after admin.NewHandler")
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
		chatHandler.SetFormatAnomalyRecorder(streaming.NewFormatAnomalyRecorderFromPool(dbConn.Pool()))
		slog.Info("response format anomaly recorder wired")

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
			if dbConn != nil && dbConn.Enabled() {
				if notifier, nerr := initApprovalNotifier(dbConn.Pool(), approvalMgr); nerr != nil {
					slog.Error("init approval notifier failed", "error", nerr)
				} else if notifier != nil {
					auditHook.SetNotifier(notifier)
					slog.Info("approval notifier initialized and injected to audit hook")
				}
			}

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

		slog.Info("CHECKPOINT: before modelPolicy check")
		// model-policy: share the same Checker instance with the
		// relay ChatHandler so admin writes can invalidate the
		// per-tenant cache entry immediately (Round 48).
		if modelPolicy != nil {
			adminHandler.SetModelPolicy(modelPolicy)
		}

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
	var stickyCleaner *bg.StickyCleaner
	var envelopeCleaner *bg.EnvelopeCleaner
	var settingsAuditCleaner *bg.SettingsAuditCleaner
	var taxonomySync *bg.TaxonomySync
	var partitionManager *bg.PartitionManager
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
			credProbeV2.Start(context.Background())
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
			modelProbe.Start(context.Background())
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
			passiveProbe.Start(context.Background())
			slog.Info("CHECKPOINT: after passiveProbe.Start")
		}
		slog.Info("CHECKPOINT: after probe workers block")

		// 2026-06-30: Start the credential-state manager AFTER probe
		// services have been wired. The manager watches for state changes
		// from probes, requests, and admin actions, and triggers fast
		// re-probes when consecutive failures exceed threshold.
		if stateManager != nil {
			// Wire the fast-reprobe submitters so UpdateOnFailure can
			// trigger immediate verification after threshold breaches.
			if credProbeV2 != nil {
				stateManager.SetProbeSubmitter(
					credProbeV2.SubmitFastProbe,
					func(ctx context.Context, credID int, model string) error {
						if modelProbe != nil {
							return modelProbe.TriggerManual(ctx, credID, model)
						}
						return nil
					},
				)
			}
			// 2026-07-03: Bug #8 fix - wire candidate cache invalidation
			stateManager.SetInvalidateCandidateCache(provider.InvalidateAllCandidateCache)
			stateManager.Start(context.Background())
			slog.Info("credential state manager started")
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

		// Weekly rollup + auto-tune suggester require writes to
		// credentials/audit; only run in "full" mode.
		slog.Info("CHECKPOINT: before bgDataPlaneOnly check for weekly/auto-tune", "bgDataPlaneOnly", bgDataPlaneOnly)
		if !bgDataPlaneOnly {
			slog.Info("CHECKPOINT: inside auto-tune block")
			weeklyPeakRollup = bg.NewWeeklyPeakRollup(dbConn.Pool())
			weeklyPeakRollup.Start(context.Background())

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
			// v2.1: Score() also reads profile weights from tuningStore.
			autoroute.SetTuningStore(tuningStore)
			chatHandler.SetAutoRoute(decider)

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
			retentionCfgProvider := func() bg.StorageRetentionConfig {
				var c bg.StorageRetentionConfig
				if b, _ := readBoolSettingPublic("storage.auto_cleanup_enabled"); b {
					c.AutoCleanupEnabled = true
				}
				if v, _ := readIntSettingPublic("storage.auto_cleanup_threshold"); v > 0 {
					c.AutoCleanupThreshold = float64(v)
				} else {
					c.AutoCleanupThreshold = 85
				}
				if v, _ := readIntSettingPublic("storage.disk_quota_percent"); v > 0 {
					c.DiskQuotaPercent = float64(v)
				} else {
					c.DiskQuotaPercent = 80
				}
				return c
			}
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

	slog.Info("CHECKPOINT: before router init")

	// ── Router ────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	slog.Info("CHECKPOINT: before healthz registration")

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

	// NET-008 fix: /metrics 必须 admin 鉴权（暴露所有 prometheus 注册
	// 指标含 provider / credential 等敏感标签）。使用
	// LLM_GATEWAY_ADMIN_API_KEY 静态 token（与 AdminTokenMiddleware 配合）。
	mux.Handle("/metrics", middleware.NewAdminTokenMiddleware(cfg.AdminAPIKey).Wrap(middleware.MetricsHandler()))

	slog.Info("CHECKPOINT: healthz and metrics registered")

	// v2 Pipeline feature flag (R1.12). Opt-in via LLM_GATEWAY_V2_ENABLED.
	// Default OFF → no-op; production v1 routes are untouched. When ON,
	// a parallel /v2/* route group is mounted on the same mux. See
	// cmd/gateway/main_v2_pipeline.go for the wiring.
	registerV2PipelineRoutes(mux)

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
	mux.Handle("/v1/models", modelsHandler)

	// Overlay the v2 Pipeline wrapper on top of the 4 v1 endpoints
	// when the flag is on. The wrapper is a Pipeline preflight
	// (tracing/security/audit/...) → chatHandler.ServeHTTP →
	// postflight. The 4 v1 handlers above stay registered as the
	// fallback inside v2DispatchHandler; the Pipeline re-routes
	// through them on a stage error or feature-flag off path.
	if v2DispatchEnabled {
		if _, v2Deps, ok := v2DispatchMux(chatHandler, messagesHandler, responsesHandler); ok && v2Deps != nil {
			// PR-V4-09 / PR-V4-10: 注入 DB pool + ApprovalManager + Publisher +
			// IntentStore 后再启动 Loop 和 Flusher。
			if dbConn != nil && dbConn.Pool() != nil {
				pool := dbConn.Pool()
				pub := bus.NewPGPublisher(pool, slog.Default())
				intentStore := assets.NewPGIntentAggregateStore(pool, slog.Default())
				// PR-V4-11: detector / checker / summarizer 当前传 nil；
				// 它们需要 *sql.DB（不是 pgxpool），后续 PR 再桥接。
				SetV2DispatchAnalysisResources(v2Deps, pool, approvalMgr, pub, intentStore, nil, nil, nil)
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
				Version, GitCommit, BuildNumber)))
				return
			}
			http.NotFound(w, r)
		})
	}

	// Admin API routes
	if adminHandler != nil {
		slog.Info("CHECKPOINT: before admin RegisterRoutes")
		adminHandler.RegisterRoutes(mux)
		// 2026-06-23 Phase 3: wire candidate_failure_monitor alert ring.
		if candidateFailureMonitor != nil {
			adminHandler.SetCandidateFailureHandlers(candidateFailureMonitor.RecentAlerts)
		}
		slog.Info("CHECKPOINT: after admin RegisterRoutes - admin API enabled")
	}

	slog.Info("CHECKPOINT: before middleware chain")
	// wrapAdmin wraps a handler with admin JWT/API-key authentication.
	// Used for Phase 2/3 admin endpoints registered outside RegisterRoutes.
	var wrapAdmin func(http.HandlerFunc) http.HandlerFunc
	if dbConn != nil {
		pool := dbConn.Pool()
		secret := cfg.SecretKey
		wrapAdmin = func(fn http.HandlerFunc) http.HandlerFunc {
			return admin.AdminMiddleware(fn, pool, secret)
		}
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
		compareAPI := admin.NewSessionCompareAPI(dbConn.Pool())
		mux.HandleFunc("/api/admin/session-compare", wrapAdmin(compareAPI.HandleCompare))
		handoffAPI := admin.NewHandoffAPI(dbConn.Pool())
		mux.HandleFunc("/api/admin/session-handoff", wrapAdmin(handoffAPI.HandleHandoff))
		slog.Info("Phase 3.5 session compare & handoff API enabled (/api/admin/session-compare, /session-handoff)")

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
			mux.HandleFunc("/api/admin/session-analytics", wrapAdmin(adminHandler.HandleSessionAnalyticsList))
			mux.HandleFunc("/api/admin/session-analytics/", wrapAdmin(adminHandler.RouteSessionAnalytics))
			mux.HandleFunc("/api/admin/session-clusters", wrapAdmin(adminHandler.HandleSessionClustersList))
			mux.HandleFunc("/api/admin/session-clusters/", wrapAdmin(adminHandler.RouteSessionClusters))
			// Task T1.1: 时间序列分析端点 (2026-07-06)
			mux.HandleFunc("/api/admin/session-analytics/activity", wrapAdmin(adminHandler.HandleActivityTrend))
			mux.HandleFunc("/api/admin/session-analytics/cost-trend", wrapAdmin(adminHandler.HandleCostTrend))
			mux.HandleFunc("/api/admin/session-analytics/latency-trend", wrapAdmin(adminHandler.HandleLatencyTrend))
			mux.HandleFunc("/api/admin/session-analytics/health-trend", wrapAdmin(adminHandler.HandleHealthTrend))
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
	}

		// Task T1.4: Usage Cost Enhanced API 注册已在 admin/handler.go:572 完成
		// (避免与 admin 包的双重注册 panic, 与 33d9d4fe fix 同型)

		// Phase 3.6: Credential Success Rate Management (2026-06-23)
		mux.HandleFunc("/api/admin/credential-success-rates", wrapAdmin(admin.HandleCredentialSuccessRates(dbConn.Pool())))
		mux.HandleFunc("/api/admin/credential-success-rates/reset", wrapAdmin(admin.HandleResetCredentialSuccessRate(dbConn.Pool())))
		slog.Info("Phase 3.6 credential success rate management enabled (/api/admin/credential-success-rates)")

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
				case strings.Contains(path, "/approvals/") && !strings.HasSuffix(path, "/approvals/"):
					wrapAdmin(approvalAPI.GetApproval)(w, r)
				default:
					http.NotFound(w, r)
				}
			})
			mux.HandleFunc("/api/admin/approvals", wrapAdmin(approvalAPI.ListApprovals))
			mux.HandleFunc("/api/admin/approvals/stats", wrapAdmin(approvalAPI.GetApprovalStats))
			slog.Info("Phase 3.9 approval query API enabled (/api/v1/approvals/*, /api/admin/approvals/stats)")
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
		Add(middleware.NewLoggingMiddleware()).
		Add(middleware.NewSecurityHeadersMiddleware()).
		Build().
		Then(mux)

	slog.Info("CHECKPOINT: after middleware build, before http.Server init")
	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: handler,
		// ReadHeaderTimeout: headers only. ReadTimeout covers the full request
		// body window from connection accept (see net/http readRequest). The
		// previous 10s total caused body_read_error when clients uploaded
		// large chat payloads slowly — production logs showed latency_ms ≈ 10001.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
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

	// 2. Close connection pools after all HTTP handlers have completed
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
	// Drain the Memora sink queue on shutdown so in-flight writes
	// are not lost. Bounded to 5s so shutdown is not held hostage
	// to a slow Memora.
	if memorySvc != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		memorySvc.Stop(stopCtx)
		stopCancel()
	}

	// Flush + close the rotated log file last so the final
	// "gateway stopped" record (and any deferred background-task
	// shutdown logs above) make it to disk.
	if err := logging.Shutdown(); err != nil {
		fmt.Fprintf(os.Stderr, "logging: shutdown error: %v\n", err)
	}

	slog.Info("gateway stopped")
}

// pendingStoreAdapter bridges the pending package's *Store to the
// sessions package's narrow PendingStore interface. The two-way
// import (sessions ← pending) would be a cycle; the adapter is the
// only place that can import both, so it lives here in main.go.
//
// All methods are thin shims; the heavy lifting stays in
// pending.Store where the Redis access is.
type pendingStoreAdapter struct{ s *pending.Store }

func newPendingStoreAdapter(s *pending.Store) session.PendingStore {
	return &pendingStoreAdapter{s: s}
}

func (a *pendingStoreAdapter) Get(ctx context.Context, sessionID, requestID string) (*session.PendingEntry, bool, error) {
	r, ok, err := a.s.Get(ctx, sessionID, requestID)
	if err != nil || !ok {
		return nil, false, err
	}
	return a.toEntry(r), true, nil
}

func (a *pendingStoreAdapter) GetLatest(ctx context.Context, sessionID string) (*session.PendingEntry, string, bool, error) {
	r, requestID, ok, err := a.s.GetLatest(ctx, sessionID)
	if err != nil || !ok {
		return nil, requestID, false, err
	}
	return a.toEntry(r), requestID, true, nil
}

func (a *pendingStoreAdapter) toEntry(r *pending.Response) *session.PendingEntry {
	if r == nil {
		return nil
	}
	return &session.PendingEntry{
		SessionID:    r.SessionID,
		TenantID:     r.TenantID,
		RequestID:    r.RequestID,
		Status:       string(r.Status),
		Body:         r.Body,
		ContentType:  r.ContentType,
		ProviderID:   r.ProviderID,
		CredentialID: r.CredentialID,
		IsStream:     r.IsStream,
		CompletedAt:  r.CompletedAt,
		ErrorMessage: r.ErrorMessage,
	}
}

// sessionAuthAdapter bridges the live authentication.KeyVerifier
// (which returns *authentication.KeyInfo) to the session.KeyVerifier
// interface (which returns session.KeyInfo).
type sessionAuthAdapter struct {
	kv *authentication.KeyVerifier
}

func (a sessionAuthAdapter) Enabled() bool { return a.kv != nil && a.kv.Enabled() }
func (a sessionAuthAdapter) Verify(ctx context.Context, rawKey string) (session.KeyInfo, error) {
	ki, err := a.kv.Verify(ctx, rawKey)
	if err != nil {
		return session.KeyInfo{}, err
	}
	return session.KeyInfo{ID: ki.ID, TenantID: ki.TenantID}, nil
}

// saveCapturedPending persists the capturer's buffered SSE body to the
// pending store so a client that disconnects mid-stream can pick up
// the response via GET /v1/sessions/{id}/pending-response (Track C C5,
// 2026-06-21). Shared by the OpenAI and both Anthropic (Q3 + Q4) stream
// paths so all three contribute to the same pending store namespace.
func saveCapturedPending(store *pending.Store, pc *streaming.PendingCapturer, resp *http.Response) {
	if pc == nil || store == nil {
		return
	}
	body, state, ok := pc.Snapshot()
	if !ok {
		return
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer saveCancel()
	if err := store.Save(saveCtx, &pending.Response{
		SessionID:    streaming.SessionIDFromResp(resp),
		RequestID:    streaming.RequestIDFromResp(resp),
		Status:       pending.Status(state.Status),
		Body:         string(body),
		ContentType:  "text/event-stream",
		IsStream:     true,
		CreatedAt:    time.Now().Unix(),
		CompletedAt:  state.CompletedAt,
		ErrorMessage: state.ErrMessage,
	}); err != nil {
		slog.Warn("pending_save_failed",
			"session_id", streaming.SessionIDFromResp(resp),
			"request_id", streaming.RequestIDFromResp(resp),
			"error", err,
		)
	}
}

// buildAutoLLMCaller returns the LLMCaller to use for the auto-route
// fallback classifier.
//
// Selection logic:
//  1. If LLMGatewayAutoLLMEndpoint env var is set:
//     HTTPLlmCaller (OpenAI-compatible POST /chat/completions)
//     wrapped in CircuitBreakerCaller (5-failure / 30s cooldown)
//     wrapped in InstrumentedCaller (per-call metrics)
//  2. Otherwise:
//     DisabledCaller (no LLM call; decider falls back to the
//     heuristic result at low confidence)
//
// Environment variables consumed (all optional except Endpoint):
//
//	LLMGatewayAutoLLMEndpoint  base URL (e.g. "https://llmgateway.internal.example.com/v1")
//	LLMGatewayAutoLLMApiKey   bearer token
//	LLMGatewayAutoLLMModel    model name (default "gpt-4o-mini")
//	LLMGatewayAutoLLMTimeout  seconds (default 3)
func buildAutoLLMCaller() autoroute.LLMCaller {
	caller, enabled := autoroute.BuildHTTPLlmCallerFromEnv(os.Getenv)
	if !enabled {
		return autoroute.DisabledCaller{}
	}
	// Wrap the real caller in: circuit breaker → instrumented metrics.
	// Order matters: instrumented wraps circuit breaker so metrics
	// see the outcome AFTER the breaker decides to short-circuit.
	return &autoroute.InstrumentedCaller{
		Inner:   autoroute.NewCircuitBreakerCaller(caller),
		Metrics: &autoroute.CallerMetrics{},
	}
}

// irAdapter implements routing.IRConverter by wrapping the ir package functions.
// Used when LLM_GATEWAY_IR_CONVERTER=true to enable the Phase B Parse→IR→Serialize
// pipeline, reducing protocol conversion complexity from O(N²) to O(N).
type irAdapter struct{}

func (a *irAdapter) ParseOpenAI(body []byte) (*ir.InternalRequest, error) {
	return ir.ParseOpenAI(body)
}

func (a *irAdapter) ParseAnthropic(body []byte) (*ir.InternalRequest, error) {
	return ir.ParseAnthropic(body)
}

func (a *irAdapter) SerializeOpenAI(req *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeOpenAI(req)
}

func (a *irAdapter) SerializeAnthropic(req *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeAnthropic(req)
}

// Phase D (2026-06-22): Response direction methods
func (a *irAdapter) ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error) {
	return ir.ParseAnthropicResponse(body)
}

func (a *irAdapter) ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error) {
	return ir.ParseOpenAIResponse(body)
}

func (a *irAdapter) SerializeOpenAIResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error) {
	return ir.SerializeOpenAIResponse(irResp, clientModel)
}

func (a *irAdapter) SerializeAnthropicResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error) {
	return ir.SerializeAnthropicResponse(irResp, clientModel)
}

// Phase E (2026-07-01): Responses API serializer. Implements streaming.IRConverter
// for the Responses API client target (ClientProtocol == "openai-responses").
// The stream serializer is per-chunk and emits ONE OR MORE Responses SSE events;
// the response serializer produces the complete non-stream body.
func (a *irAdapter) SerializeResponses(chunk *ir.StreamChunk, itemID string) string {
	return chunk.SerializeResponses(itemID)
}

func (a *irAdapter) SerializeResponsesResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error) {
	return ir.SerializeResponsesResponse(irResp, clientModel)
}

// applyLogSettingsToLogging 从 settings_kv 读取 log.* 配置并应用到已初始化的
// lumberjack writer（热加载）。在 settings registry 初始化后调用一次，使 DB 中
// 持久化的日志轮转参数在启动时即生效。文件路径（log.file）不在热加载范围。
//
// 静默失败：任何读取/解析错误只记 warning，保留 env/YAML 默认值，不阻塞启动。
func applyLogSettingsToLogging() {
	cur := logging.ActiveConfig()
	if cur.File == "" {
		return // 文件日志未启用，无需同步
	}
	updated := false

	if v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, "log.max_size_mb", ""); err == nil && src == "db" {
		if n := parseIntSetting(v); n > 0 {
			cur.MaxSizeMB = n
			updated = true
		}
	}
	if v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, "log.max_backups", ""); err == nil && src == "db" {
		if n := parseIntSetting(v); n >= 0 {
			cur.MaxBackups = n
			updated = true
		}
	}
	if v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, "log.max_age_days", ""); err == nil && src == "db" {
		if n := parseIntSetting(v); n >= 0 {
			cur.MaxAgeDays = n
			updated = true
		}
	}
	if v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, "log.compress", ""); err == nil && src == "db" {
		if b, ok := parseBoolSetting(v); ok {
			cur.Compress = b
			updated = true
		}
	}

	if updated {
		if err := logging.Reconfigure(cur); err != nil {
			slog.Warn("settings: apply log.* to logging failed", "error", err)
		} else {
			slog.Info("settings: log.* applied from DB (hot reload)",
				"max_size_mb", cur.MaxSizeMB,
				"max_backups", cur.MaxBackups,
				"max_age_days", cur.MaxAgeDays,
				"compress", cur.Compress)
		}
	}
}

// readIntSettingPublic 从 settings.Global 读 int 值（worker 配置用）。
func readIntSettingPublic(key string) (int, string) {
	if settings.Global == nil {
		return 0, ""
	}
	v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
	if err != nil || len(v) == 0 {
		return 0, ""
	}
	s := strings.Trim(string(v), `"`)
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, ""
	}
	return n, src
}

// readBoolSettingPublic 从 settings.Global 读 bool 值（worker 配置用）。
func readBoolSettingPublic(key string) (bool, string) {
	if settings.Global == nil {
		return false, ""
	}
	v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
	if err != nil || len(v) == 0 {
		return false, ""
	}
	s := strings.Trim(string(v), `"`)
	return s == "true" || s == "1", src
}

// parseIntSetting 解析 settings_kv 返回的 JSON 值（可能是 "100" 或 100）。
func parseIntSetting(raw json.RawMessage) int {
	s := strings.Trim(string(raw), `"`)
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return -1
}

// parseBoolSetting 解析 settings_kv 返回的 JSON bool 值。
func parseBoolSetting(raw json.RawMessage) (bool, bool) {
	s := strings.Trim(string(raw), `"`)
	switch strings.ToLower(s) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

// initApprovalNotifier 初始化审批通知器（从 DB 加载路由规则 + 创建 IM 渠道）。
// 返回 nil, nil 表示配置不全（路由表为空）；返回 nil, err 表示初始化失败。
func initApprovalNotifier(pool *pgxpool.Pool, approvalMgr *sessionaudit.ApprovalManager) (*notification.ApprovalNotifier, error) {
	if pool == nil || approvalMgr == nil {
		return nil, fmt.Errorf("init approval notifier: nil pool or approval manager")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. 加载路由规则
	routingTable := notification.NewEmptyRoutingTable()
	loader := notification.NewPgxRoutingLoader(pool)
	if err := routingTable.LoadFromDB(ctx, loader); err != nil {
		return nil, fmt.Errorf("load routing rules: %w", err)
	}

	// 2. 创建渠道实例（从环境变量配置）
	channels := make(map[notification.ChannelType]notification.NotificationChannel)

	// 飞书渠道
	if larkAppID := os.Getenv("LARK_APP_ID"); larkAppID != "" {
		larkAppSecret := os.Getenv("LARK_APP_SECRET")
		larkCfg := notification.LarkBotConfig{
			AppID:     larkAppID,
			AppSecret: larkAppSecret,
		}
		channels[notification.ChannelLark] = notification.NewLarkBotChannel(larkCfg)
		slog.Info("lark channel initialized", "app_id", larkAppID)
	}

	// 钉钉渠道
	if dingAppKey := os.Getenv("DINGTALK_APP_KEY"); dingAppKey != "" {
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
		return nil, nil
	}

	// 3. 构造 ApprovalNotifier
	notifier, err := notification.NewApprovalNotifier(notification.NotifierConfig{
		Channels:    channels,
		Routing:     routingTable,
		ApprovalMgr: approvalMgr,
		Timeout:     30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create approval notifier: %w", err)
	}

	return notifier, nil
}

// adminLiveRequestFromEntry adapts a freshly-persisted telemetry
// RequestLogEntry into the dashboard's swim-lane LiveRequest shape.
// Called on the telemetry worker goroutine, so the implementation
// MUST be cheap — the only I/O is the provider_id → catalog_code
// sync.Map lookup (with a 200ms-timeout DB fallback on miss).
// 2026-07-06: now resolves provider through credential_id when available.
func adminLiveRequestFromEntry(entry *telemetry.RequestLogEntry, hub *admin.LiveStreamSSEHub) admin.LiveRequest {
	clientModel := ""
	if entry.ClientModel != nil {
		clientModel = strings.TrimSpace(*entry.ClientModel)
	}
	outboundModel := ""
	if entry.OutboundModel != nil {
		outboundModel = strings.TrimSpace(*entry.OutboundModel)
	}
	status := ""
	if entry.RequestStatus != nil {
		status = strings.TrimSpace(*entry.RequestStatus)
	}
	var totalTokens *int
	if entry.PromptTokens != nil && entry.CompletionTokens != nil {
		t := *entry.PromptTokens + *entry.CompletionTokens
		totalTokens = &t
	}

	// Determine model display name: outbound → client (shared by both paths)
	displayModel := outboundModel
	if displayModel == "" {
		displayModel = clientModel
	}

	if hub != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		// Diagnose why provider info is sometimes missing. Both IDs being
		// empty means the request never reached credential selection
		// (e.g. auth/routing failure), which is expected for some flows;
		// one ID present but resolution returning empty points at a stale
		// cache entry or a missing providers/credentials row.
		hasCred := entry.CredentialID != nil && *entry.CredentialID > 0
		hasProv := entry.ProviderID != nil && *entry.ProviderID > 0
		if !hasCred && !hasProv {
			slog.Debug("live stream: request has no credential_id/provider_id",
				"request_id", entry.RequestID, "status", status, "tenant_id", entry.TenantID)
		}

		// Prefer credential_id resolution (accurate provider even when telemetry provider_id is stale/missing)
		providerCode := ""
		if hasCred {
			providerCode = hub.ProviderCodeForCredential(ctx, *entry.CredentialID)
		}
		// Fallback to provider_id
		if providerCode == "" && hasProv {
			providerCode = hub.ProviderCodeFor(ctx, *entry.ProviderID)
		}
		if providerCode == "" && (hasCred || hasProv) {
			slog.Debug("live stream: provider resolution returned empty",
				"request_id", entry.RequestID, "credential_id", entry.CredentialID,
				"provider_id", entry.ProviderID, "tenant_id", entry.TenantID)
		}

		// Extract canonical_id for model name resolution and aggregation
		canonicalID := 0
		if entry.CanonicalID != nil && *entry.CanonicalID > 0 {
			canonicalID = *entry.CanonicalID
		}

		return hub.LiveRequestFromTelemetry(
			ctx,
			entry.RequestID,
			time.Now().UTC(),
			entry.TenantID,
			clientModel,
			outboundModel,
			canonicalID,
			providerCode,
			status,
			entry.Success,
			entry.ErrorKind,
			entry.LatencyMs,
			entry.PromptTokens,
			entry.CompletionTokens,
			totalTokens,
			entry.CostUSD,
		)
	}
	// Fallback when hub is nil (defensive; unreachable in normal operation
	// because SetOnRequestLogEmitted is only wired when hub != nil).
	return admin.LiveRequest{
		RequestID:        entry.RequestID,
		Ts:               time.Now().UTC().Format(time.RFC3339),
		TenantID:         entry.TenantID,
		Model:            displayModel,
		CanonicalName:    displayModel, // best-effort: use display name as canonical
		ModelCategory:    "",           // cannot resolve without hub
		ProviderCode:     "",           // cannot resolve without hub
		Status:           status,
		LatencyMs:        entry.LatencyMs,
		PromptTokens:     entry.PromptTokens,
		CompletionTokens: entry.CompletionTokens,
		TotalTokens:      totalTokens,
		CostUSD:          entry.CostUSD,
		ErrorKind:        entry.ErrorKind,
	}
}
