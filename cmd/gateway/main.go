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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/compressor"
	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/credentialstate"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/disguise"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/modelpolicy"
	"github.com/kaixuan/llm-gateway-go/internal/observability"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/maas"
	"github.com/kaixuan/llm-gateway-go/memora"
	"github.com/kaixuan/llm-gateway-go/metatools"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/registry"
	"github.com/kaixuan/llm-gateway-go/relay"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/routing"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/kaixuan/llm-gateway-go/sessions"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/kaixuan/llm-gateway-go/telemetry"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstream "github.com/kaixuan/llm-gateway-go/upstream"
	"github.com/redis/go-redis/v9"
)

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
	// memoraSink is the async write buffer for Memora persistence.
	// Declared at the top so both the executor wiring and the
	// graceful-shutdown sequence can reference it.
	var memoraSink *memora.Sink
	var memoraClient *memora.Client

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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))

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

	cfgStore := config.NewStore(cfg)
	relay.SetConfigStore(cfgStore)
	slog.Info("gateway starting", "listen", cfg.Listen, "log_level", cfg.LogLevel)

	// ── Dependencies ──────────────────────────────────────────────────────
	dbConn, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		slog.Warn("postgres disabled", "error", err)
	}
	defer func() {
		if dbConn != nil {
			dbConn.Close()
		}
	}()

	cm := circuit.NewManager()
	lim := limiter.New()

	matrixPath := transform.DefaultMatrixPath()
	matrix := transform.New(matrixPath)

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

	chatHandler := relay.NewChatHandler(cm, lim, matrix, pools, resolver, auditSink)
	healthHandler := relay.NewHealthHandler(cm, lim, upClient.Proxy())
	modelsHandler := relay.NewModelsHandler()
	messagesHandler := relay.NewMessagesHandler(chatHandler)
	responsesHandler := relay.NewResponsesHandler(chatHandler)

	// ── Tenant model policy (Round 48, 2026-06-21) ─────────────────
	// Single Checkerr singleton shared by relay.ChatHandler (hot
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
	var sessionMgr *sessions.Manager
	var fpSlotRedis *redis.Client
	var pendingStore *pending.Store
	var redisClientForCache *sessions.RedisClient
	var routingExec *routing.Executor
	if cfg.RedisAddr != "" {
		redisClient := sessions.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
		pingErr := redisClient.Ping(pingCtx)
		pingCancel()
		if pingErr == nil {
			ttl := time.Duration(cfg.SessionTTLHours) * time.Hour
			sessionMgr = sessions.NewManager(redisClient, ttl)
			chatHandler.SetSessionGetter(sessionMgr)
			redisClientForCache = redisClient
			fpSlotRedis = redis.NewClient(&redis.Options{
				Addr:     cfg.RedisAddr,
				Password: cfg.RedisPassword,
				DB:       cfg.RedisDB,
			})
			// Track C (2026-06-18): pending response cache for client
			// reconnect and vendor async retry. Uses the same Redis
			// connection as fpSlotRedis (both are independent *redis.Client
			// sharing the same backing server). nil is a valid state
			// (Store becomes a no-op); explicit construction here so
			// the GET endpoint is available whenever Redis is up.
			pendingStore = pending.NewStore(fpSlotRedis, ttl)
			slog.Info("session manager enabled", "redis", cfg.RedisAddr, "ttl_hours", cfg.SessionTTLHours)
		} else {
			slog.Warn("session manager: redis ping failed", "error", err)
		}
	} else {
		slog.Warn("session manager disabled (no LLM_GATEWAY_REDIS_ADDR)")
	}

	fpSlots := credentialfpslot.New(credentialfpslot.Config{
		DefaultLimit: cfg.DefaultCredentialConcurrency,
		Enabled:      cfg.EnableCredentialFpSlots,
	}, fpSlotRedis)

	// 2026-06-23: enable background idle-slot reclaim (15 min idle, 30 s scan).
	// Without this, only Redis auto-expiry cleans up slots — AOF rewrite stalls
	// or RDB snapshot pauses can hold the key longer than the advertised 30 min.
	fpSlots.StartReclaim(context.Background())

	// ── Settings registry (Q1: B, Q2: A) ───────────────────────────────
	// Initialise the unified runtime-config registry. Specs registered here
	// become readable via settings.Global.EffectiveValue(scope, key, tenantID).
	// Order matters: must run BEFORE any code that calls LoadMode/LoadFraction
	// (e.g. compressor.NewCompressor).
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

		// Phase 3.2: Provider-level settings resolver
		providerSettingsResolver = settings.NewProviderSettingsResolver(dbConn.Pool(), settings.Global)
		slog.Info("settings: provider-level resolver initialised")
	} else {
		slog.Info("settings: registry disabled (no DB)")
	}

	// ── Routing executor (multi-candidate P2C) ──────────────────────────
	providerClient := provider.NewClient()
	if dbConn != nil && dbConn.Enabled() {
		providerClient.SetDB(dbConn.Pool(), cfg.SecretKey, cfg.CredentialEncryptionKey)
		resolver.SetDB(dbConn.Pool())
	}
	if providerClient.Enabled() {
		stickyCache := routing.NewStickyCache()
		if dbConn != nil && dbConn.Enabled() {
			stickyCache.SetDB(dbConn.Pool())
			if err := stickyCache.RestoreFromDB(context.Background()); err != nil {
				slog.Warn("sticky restore from DB failed", "error", err)
			}
		}
		router := routing.NewRouter(stickyCache, lim)

		// Connect FpSlots to Router for load-aware P2C selection
		router.FpSlots = fpSlots

		norm := relay.NewNormalizer()
		routingExec = routing.NewExecutor(
			router, cm, lim, pools, upClient,
			norm.NormalizeChunk,
			func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, normFunc routing.NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) routing.StreamOutcome {
				// Track C C2 (2026-06-18): wrap the streaming hot path
				// so a client that disconnects mid-stream can still
				// recover the response via
				// GET /v1/sessions/{id}/pending-response.
				//
				// Flow:
				//   1. Build a capturer (1 MiB cap, in-memory)
				//   2. Run the stream with the capturer attached
				//   3. After the stream returns, write the captured
				//      body to pending.Store so the GET endpoint can
				//      replay it on reconnect
				//
				// Why this is safe: C1 decoupled the upstream
				// context from the client context when the request
				// carries a session id, so the read loop keeps going
				// past a client disconnect. The capturer records
				// every chunk regardless of client state. The
				// write-back happens after the stream function
				// returns, so it does not block the streaming hot
				// path.
				var pc *relay.PendingCapturer
				if pendingStore != nil && relay.ClientHasSessionID(w, resp) {
					pc = relay.NewPendingCapturer(0)
				}
				outcome := relay.StreamChatWithPendingCapture(w, resp, clientModel, outboundModel, norm, capture, toolsRequested, relay.StripMinimaxFieldsBody, pc)
				saveCapturedPending(pendingStore, pc, resp)
				return outcome
			},
			auditSink,
		)
		routingExec.XMLCoerceNonStream = relay.CoerceXMLToolCallsInChatResponse
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql): wire
		// the per-provider tool_call quality processor as a hook on the
		// Executor. routing cannot import relay (relay imports routing)
		// so the hook is a closure. The executor reads
		// cand.QualityFixMode (loaded from providers.quality_fix_mode)
		// and short-circuits when the value is empty/'off'.
		//
		// The streaming variant of the processor runs inside
		// relay/stream.go directly via SetQualityFixModeOnContext +
		// qualityFixModeFromContext, so it does not need a hook on
		// the executor — the context value travels through the
		// upstream http.Request and is read on every SSE line.
		routingExec.QualityProcessNonStream = relay.WrapQualityProcessNonStream()
		routingExec.QualitySetMode = relay.WrapSetQualityFixModeOnContext()
		// Q4 streaming: Anthropic client → Anthropic upstream. Capturer-aware
		// (Track C C5, 2026-06-21): builds a pending-store capturer per request
		// when the upstream HTTP request carries a session id, so the body can
		// be replayed via GET /v1/sessions/{id}/pending-response on client
		// disconnect. Mirrors the OpenAI path wiring further below.
		routingExec.AnthropicPassthroughStream = func(
			w http.ResponseWriter,
			resp *http.Response,
			clientModel, outboundModel, requestID string,
			cap *audit.StreamCapture,
			pcAny any,
		) routing.StreamOutcome {
			var pc *relay.PendingCapturer
			if pendingStore != nil && relay.ClientHasSessionID(w, resp) {
				pc = relay.NewPendingCapturer(0)
			}
			outcome := relay.StreamAnthropicPassthrough(w, resp, clientModel, outboundModel, requestID, cap, pc)
			saveCapturedPending(pendingStore, pc, resp)
			return outcome
		}
		routingExec.ChatToAnthropic = relay.ConvertChatRequestToAnthropic
		routingExec.AnthropicToOpenAI = relay.ConvertAnthropicBodyToOpenAI

		// Phase B (2026-06-22): IR-based protocol converter.
		// When LLM_GATEWAY_IR_CONVERTER=true, use the new Parse→IR→Serialize
		// pipeline instead of the 6 scattered callbacks. Reduces conversion
		// complexity from O(N²) to O(N).
		if os.Getenv("LLM_GATEWAY_IR_CONVERTER") == "true" {
			routingExec.IR = &irAdapter{}
			slog.Info("ir_converter", "enabled", true)
		}

		// Q3 streaming: openai client -> anthropic upstream. Translates
		// Anthropic SSE chunks to OpenAI SSE chunks so the OpenAI parser
		// doesn't choke on event: ... lines. Capturer-aware (Track C C5).
		routingExec.AnthropicToOpenAIStream = func(
			w http.ResponseWriter,
			resp *http.Response,
			clientModel, outboundModel, requestID string,
			cap *audit.StreamCapture,
			pcAny any,
		) routing.StreamOutcome {
			var pc *relay.PendingCapturer
			if pendingStore != nil && relay.ClientHasSessionID(w, resp) {
				pc = relay.NewPendingCapturer(0)
			}
			outcome := relay.StreamAnthropicSSEToOpenAI(w, resp, clientModel, outboundModel, requestID, cap, pc)
			saveCapturedPending(pendingStore, pc, resp)
			return outcome
		}
		// Q3 non-stream: convert Anthropic Messages JSON to OpenAI
		// chat.completion JSON. Fixes the missing `content` field on
		// minimax-M2.7 non-stream responses.
		routingExec.AnthropicToChatResponse = relay.ConvertAnthropicResponseToChat
		routingExec.SanitizeAnthropicTools = relay.SanitizeAnthropicToolsInBody
		routingExec.NormalizeOpenAITools = relay.NormalizeToolsInChatBody
		// Strip minimax-private fields (nvext, base_resp, input_sensitive*,
		// output_sensitive*) from all non-stream chat responses before
		// returning to the client. Wired into both executeOpenAI (non-stream
		// path) and the StreamChat closure (stream path via stripFn).
		routingExec.StripMinimaxFields = relay.StripMinimaxFieldsBody
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
		routingExec.MnfStreak = routing.NewMnfStreak(mnfStreakCap)
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
		routingExec.Compressor = compressor.NewCompressor()
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
			memoraClient = memora.NewClient(memora.ClientConfig{
				BaseURL:            memoraBase,
				APIKey:             os.Getenv("LLM_GATEWAY_MEMORA_API_KEY"),
				SmartSearchBaseURL: os.Getenv("LLM_GATEWAY_MEMORA_SMART_SEARCH_BASE_URL"),
				SmartSearchAPIKey:  os.Getenv("LLM_GATEWAY_MEMORA_SMART_SEARCH_API_KEY"),
			})
			routingExec.Memora = memoraClient
			// Async sink: fire-and-forget write buffer for L1 session
			// memory persistence. 2 workers / 2048-deep queue is enough
			// for the write volume (one enqueue per successful request).
			memoraSink = memora.NewSink(memoraClient, 2, 2048)
			memoraSink.Start()
			routingExec.MemoraSink = memoraSink
			smartSearchBase := os.Getenv("LLM_GATEWAY_MEMORA_SMART_SEARCH_BASE_URL")
			slog.Info("memora context-compression oracle enabled",
				"base_url", memoraBase,
				"smart_search_url", smartSearchBase,
			)
		} else {
			slog.Info("memora context-compression oracle disabled (set LLM_GATEWAY_MEMORA_BASE_URL to enable)")
		}
		if dbConn != nil && dbConn.Enabled() {
			routingExec.State = credentialstate.NewWriter(dbConn.Pool())
			routingExec.DB = dbConn
			routingExec.HeaderProfiles = routing.NewHeaderProfileCache(dbConn.Pool())
		}
		routingExec.FpSlots = fpSlots

		// Health tracking (2026-06-22): sliding window recorder + concurrency tuner + continuous failure checker
		if fpSlotRedis != nil && dbConn != nil {
			healthTracker := routing.NewHealthTracker(
				fpSlotRedis,
				dbConn.Pool(),
				2*time.Hour, // window TTL
				100,         // max size
			)
			routingExec.HealthTracker = healthTracker
			slog.Info("health_tracker initialized", "window", "1h", "max_size", 100)
		}

		// 2026-06-23 Phase 2 (P1): per-candidate failure logger. Writes one
		// row to candidate_failure_logs per failed (request, credential,
		// model, attempt) tuple so operators can see WHICH credentials
		// failed in a sequence (request_logs only records the LAST one).
		if dbConn != nil {
			routingExec.FailureLogger = routing.NewCandidateFailureWriter(dbConn.Pool())
			slog.Info("candidate_failure_logger initialized")
		}

		routingExec.Provider = providerClient
		// Inject peak collector (after bg workers have started it).
		if peakCollector != nil {
			routingExec.PeakCollector = peakCollector
		}
		// Enable disguise mode if configured.
		if cfg.EnableDisguise {
			routingExec.DisguisePool = disguise.DefaultPool
			slog.Info("disguise mode enabled")
		}
		chatHandler.SetExecutor(routingExec, providerClient, stickyCache)
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
		chatHandler.SetIdempotentCache(relay.NewIdempotentCache(idempotentCap, time.Duration(idempotentTTL)*time.Second))
		slog.Info("idempotent_cache_enabled",
			"cap", idempotentCap,
			"ttl_seconds", idempotentTTL,
		)

		slog.Info("routing executor enabled")
	} else {
		slog.Warn("routing executor disabled (no database connection)")
	}

	// ── Auth + Rate Limiting ──────────────────────────────────────────────
	keyVerifier := auth.NewKeyVerifier()
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
	if redisClientForCache != nil && dbConn != nil && dbConn.Enabled() && telemetryClient.Enabled() && !compressorSessionDisabled() {
		scCache := compressor.NewSessionCache(redisBackendFromClient(redisClientForCache), dbBackendFromPool(dbConn))
		scDeps := compressor.SessionCompressorDeps{
			Cache:          scCache,
			CompactionDeps: NewDependenciesFromExecutor(routingExec),
		}
		chatHandler.SetSessionCompressor(compressor.NewSessionCompressor(scDeps))
		slog.Info("v3 session-level compressor wired (L1 in-mem + L2 Redis + L3 PG)")
	} else {
		slog.Info("v3 session-level compressor disabled (no Redis / no DB / env flag off)")
	}

	// ── Phase 2: Meta-tools handler ─────────────────────────────────────
	if dbConn != nil && dbConn.Enabled() {
		metaHandler := metatools.NewHandler(dbConn.Pool())
		interceptor := relay.NewMetaToolInterceptor(metaHandler)
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
		// settings-management: inject the DB-backed settings store so the
		// /api/admin/settings/* endpoints can read/write settings_kv.
		adminHandler.SetSettingsStore(settings.NewStoreDB(dbConn.Pool()))

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
	var modelProbe *bg.ModelProbeRunner
	var passiveProbe *bg.PassiveProbeListener
	var stickyCleaner *bg.StickyCleaner
	var envelopeCleaner *bg.EnvelopeCleaner
	var settingsAuditCleaner *bg.SettingsAuditCleaner
	var taxonomySync *bg.TaxonomySync
	// peakCollector / weeklyPeakRollup / slotSuggester are declared
	// at the top of main() so the executor can reference them.
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
			slog.Info("CHECKPOINT: before modelProbe.Start")
			modelProbe.Start(context.Background())
			slog.Info("CHECKPOINT: after modelProbe.Start")

			// v6 (2026-06-22): Layer 5 passive probe observer.
			// Scans request_logs every 30s for failures, promotes to
			// reviewing, and after the 5-min observation window resolves:
			// still-failing → mark unreachable; recovered → clear.
			// stateWriter lets it write availability_state='unreachable'.
			slog.Info("CHECKPOINT: before NewPassiveProbeListener")
			passiveProbe = bg.NewPassiveProbeListener(dbConn.Pool(), credentialstate.NewWriter(dbConn.Pool()))
			slog.Info("CHECKPOINT: before passiveProbe.Start")
			passiveProbe.Start(context.Background())
			slog.Info("CHECKPOINT: after passiveProbe.Start")
		}
		slog.Info("CHECKPOINT: after probe workers block")

		slog.Info("CHECKPOINT: before NewStickyCleaner")
		stickyCleaner = bg.NewStickyCleaner(dbConn.Pool())
		slog.Info("CHECKPOINT: before stickyCleaner.Start")
		stickyCleaner.Start(context.Background())
		slog.Info("CHECKPOINT: after stickyCleaner.Start")
		envelopeCleaner = bg.NewEnvelopeCleaner(dbConn.Pool())

		// settings-management: 7-day audit retention worker (Q6: C).
		slog.Info("CHECKPOINT: before NewSettingsAuditCleaner")
		settingsAuditCleaner := bg.NewSettingsAuditCleaner(dbConn.Pool())
		slog.Info("CHECKPOINT: before settingsAuditCleaner.Start")
		settingsAuditCleaner.Start(context.Background())
		slog.Info("CHECKPOINT: after settingsAuditCleaner.Start")
		envelopeCleaner.Start(context.Background())
		slog.Info("CHECKPOINT: after envelopeCleaner.Start")
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
			callHistoryAggregator = bg.NewCallHistoryAggregator(fpSlotRedis, dbConn.Pool(), 1*time.Minute)
			callHistoryAggregator.Start(context.Background())

			slog.Info("CHECKPOINT: before healthAutoRecover")
			healthAutoRecover = bg.NewHealthAutoRecover(dbConn.Pool(), 1*time.Minute)
			healthAutoRecover.Start(context.Background())
			slog.Info("CHECKPOINT: after healthAutoRecover.Start")
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
			// v2.1: Decider reads the LLM-fallback threshold from
			// tuningStore dynamically (atomic.Pointer load, no lock).
			decider.SetTuningStore(tuningStore)
			// v2.1: Score() also reads profile weights from tuningStore.
			autoroute.SetTuningStore(tuningStore)
			chatHandler.SetAutoRoute(decider)

			autoIndexRefresher = bg.NewAutoIndexRefresher(dbConn.Pool(), autoIdx)
			autoIndexRefresher.Start(context.Background())

			// v2.0.1: realtime listener for sub-second index refresh
			// (PG LISTEN/NOTIFY trigger on credential_model_bindings /
			// credentials / api_keys / model_offers).
			autoRouteListener := bg.NewAutoRouteRealtimeListener(dbConn.Pool(), autoIndexRefresher)
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
			adminHandler.SetFpSlots(fpSlots)
			slog.Info("CHECKPOINT: after SetFpSlots")
			adminHandler.SetPeakCollector(peakCollector)
			slog.Info("CHECKPOINT: after SetPeakCollector")
			// Wire redis for credential monitor endpoints (2026-06-22).
			if fpSlotRedis != nil {
				adminHandler.SetRedisClient(fpSlotRedis)
			}
			slog.Info("CHECKPOINT: after SetRedisClient")
		}
		slog.Info("CHECKPOINT: before memoraClient check")
		if memoraClient != nil {
			adminHandler.SetMemoraServices(memoraClient, memoraSink)
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
	}

	slog.Info("CHECKPOINT: before static handler init")

	// ── Static files (Vue SPA) ───────────────────────────────────────────
	staticHandler := relay.NewStaticHandler(cfg.StaticDir)

	slog.Info("CHECKPOINT: before router init")

	// ── Router ────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	slog.Info("CHECKPOINT: before healthz registration")

	mux.Handle("/healthz", healthHandler)
	mux.Handle("/metrics", middleware.MetricsHandler())

	slog.Info("CHECKPOINT: healthz and metrics registered")

	mux.Handle("/v1/chat/completions", chatHandler)
	mux.Handle("/v1/completions", chatHandler)
	mux.Handle("/v1/messages", messagesHandler)
	mux.Handle("/v1/responses", responsesHandler)
	mux.Handle("/v1/models", modelsHandler)

	if sessionMgr != nil {
		sessionHandler := sessions.NewHandler(sessionMgr)
		if keyVerifier.Enabled() {
			sessionHandler.SetAuth(keyVerifier)
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
	if configFile != "" {
		configPath := configFile
		mux.HandleFunc("/admin/config/reload", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := cfgStore.ReloadFile(configPath); err != nil {
				slog.Error("config: hot-reload failed", "error", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				//nolint:errcheck // HTTP write error non-recoverable
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
				return
			}
			slog.Info("config: hot-reload succeeded")
			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck // HTTP write error non-recoverable
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		})
		slog.Info("config: hot-reload endpoint enabled", "path", configFile)
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
				w.Write([]byte(`{"service":"llm-gateway-go","version":"0.3.0"}`))
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

		// Phase 3.5: Session List & Detail API
		sessionListAPI := admin.NewSessionListAPI(dbConn.Pool())
		mux.HandleFunc("/api/admin/sessions", wrapAdmin(sessionListAPI.HandleList))
		mux.HandleFunc("/api/admin/sessions/", wrapAdmin(sessionListAPI.HandleDetail))
		slog.Info("Phase 3.5 session list API enabled (/api/admin/sessions)")

		// Phase 3.6: Credential Success Rate Management (2026-06-23)
		mux.HandleFunc("/api/admin/credential-success-rates", wrapAdmin(admin.HandleCredentialSuccessRates(dbConn.Pool())))
		mux.HandleFunc("/api/admin/credential-success-rates/reset", wrapAdmin(admin.HandleResetCredentialSuccessRate(dbConn.Pool())))
		slog.Info("Phase 3.6 credential success rate management enabled (/api/admin/credential-success-rates)")
	}

	slog.Info("CHECKPOINT: before middleware stack build")
	// ── Middleware stack (declarative chain) ─────────────────────────────
	handler := middleware.NewBuilder().
		Add(middleware.NewRecoveryMiddleware()).
		Add(middleware.NewRequestIDMiddleware()).
		Add(middleware.NewCORSMiddleware(cfg.CORSOrigins)).
		Add(middleware.NewPrometheusMiddleware()).
		Add(middleware.NewAuthMiddleware(cfg.APIKey)).
		Add(middleware.NewLoggingMiddleware()).
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
	if passiveProbe != nil {
		passiveProbe.Stop()
	}
	if taxonomySync != nil {
		taxonomySync.Stop()
	}
	if stickyCleaner != nil {
		stickyCleaner.Stop()
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
	}
	// Drain the Memora sink queue on shutdown so in-flight writes
	// are not lost. Bounded to 5s so shutdown is not held hostage
	// to a slow Memora.
	if memoraSink != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		memoraSink.Stop(stopCtx)
		stopCancel()
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

func newPendingStoreAdapter(s *pending.Store) sessions.PendingStore {
	return &pendingStoreAdapter{s: s}
}

func (a *pendingStoreAdapter) Get(ctx context.Context, sessionID, requestID string) (*sessions.PendingEntry, bool, error) {
	r, ok, err := a.s.Get(ctx, sessionID, requestID)
	if err != nil || !ok {
		return nil, false, err
	}
	return a.toEntry(r), true, nil
}

func (a *pendingStoreAdapter) GetLatest(ctx context.Context, sessionID string) (*sessions.PendingEntry, string, bool, error) {
	r, requestID, ok, err := a.s.GetLatest(ctx, sessionID)
	if err != nil || !ok {
		return nil, requestID, false, err
	}
	return a.toEntry(r), requestID, true, nil
}

func (a *pendingStoreAdapter) toEntry(r *pending.Response) *sessions.PendingEntry {
	if r == nil {
		return nil
	}
	return &sessions.PendingEntry{
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

// saveCapturedPending persists the capturer's buffered SSE body to the
// pending store so a client that disconnects mid-stream can pick up
// the response via GET /v1/sessions/{id}/pending-response (Track C C5,
// 2026-06-21). Shared by the OpenAI and both Anthropic (Q3 + Q4) stream
// paths so all three contribute to the same pending store namespace.
//
// Best-effort: nil store or nil pc short-circuits; a save error is
// logged at WARN and the streaming hot path is not affected.
func saveCapturedPending(store *pending.Store, pc *relay.PendingCapturer, resp *http.Response) {
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
		SessionID:    relay.SessionIDFromResp(resp),
		RequestID:    relay.RequestIDFromResp(resp),
		Status:       pending.Status(state.Status),
		Body:         string(body),
		ContentType:  "text/event-stream",
		IsStream:     true,
		CreatedAt:    time.Now().Unix(),
		CompletedAt:  state.CompletedAt,
		ErrorMessage: state.ErrMessage,
	}); err != nil {
		slog.Warn("pending_save_failed",
			"session_id", relay.SessionIDFromResp(resp),
			"request_id", relay.RequestIDFromResp(resp),
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
//	LLMGatewayAutoLLMEndpoint  base URL (e.g. "https://llm.kxpms.cn/v1")
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
