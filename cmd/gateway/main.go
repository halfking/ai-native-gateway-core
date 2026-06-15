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
	"strings"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/telemetry"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/credentialstate"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/disguise"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/memora"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/relay"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/routing"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/kaixuan/llm-gateway-go/sessions"
	"github.com/redis/go-redis/v9"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstream "github.com/kaixuan/llm-gateway-go/upstream"
)

func main() {
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

	// ── Redis (sessions + credential fp slots) ─────────────────────────
	var sessionMgr *sessions.Manager
	var fpSlotRedis *redis.Client
	if cfg.RedisAddr != "" {
		redisClient := sessions.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
		pingErr := redisClient.Ping(pingCtx)
		pingCancel()
		if pingErr == nil {
			ttl := time.Duration(cfg.SessionTTLHours) * time.Hour
			sessionMgr = sessions.NewManager(redisClient, ttl)
			chatHandler.SetSessionGetter(sessionMgr)
			fpSlotRedis = redis.NewClient(&redis.Options{
				Addr:     cfg.RedisAddr,
				Password: cfg.RedisPassword,
				DB:       cfg.RedisDB,
			})
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
		norm := relay.NewNormalizer()
		exec := routing.NewExecutor(
			router, cm, lim, pools, upClient,
			norm.NormalizeChunk,
			func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, normFunc routing.NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) routing.StreamOutcome {
				// Strip minimax-private fields from each SSE chunk's
				// payload envelope (nvext, base_resp, input_sensitive*,
				// output_sensitive*). Closes the 'base_resp' leak that
				// was visible in stream chunks from minimax-OpenAI /
				// minimax-Anthropic upstreams in Phase 12.
				return relay.StreamChatWithCaptureAndToolFallback(w, resp, clientModel, outboundModel, norm, capture, toolsRequested, relay.StripMinimaxFieldsBody)
			},
			auditSink,
		)
		exec.XMLCoerceNonStream = relay.CoerceXMLToolCallsInChatResponse
		exec.AnthropicPassthroughStream = relay.StreamAnthropicPassthrough
		exec.ChatToAnthropic = relay.ConvertChatRequestToAnthropic
		exec.AnthropicToOpenAI = relay.ConvertAnthropicBodyToOpenAI
		// Q3 streaming: openai client -> anthropic upstream. Translates
		// Anthropic SSE chunks to OpenAI SSE chunks so the OpenAI parser
		// doesn't choke on event: ... lines. Fixes the "供应商错误"
		// symptom on minimax-M2.7 / minimax-M3 etc. (Q3 model routes).
		exec.AnthropicToOpenAIStream = func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, cap *audit.StreamCapture) routing.StreamOutcome {
			// relay.StreamOutcome and routing.StreamOutcome are
			// structurally identical; explicit conversion to bridge the
			// import boundary (routing can't import relay).
			return routing.StreamOutcome(relay.StreamAnthropicSSEToOpenAI(w, resp, clientModel, outboundModel, requestID, cap))
		}
		// Q3 non-stream: convert Anthropic Messages JSON to OpenAI
		// chat.completion JSON. Fixes the missing `content` field on
		// minimax-M2.7 non-stream responses.
		exec.AnthropicToChatResponse = relay.ConvertAnthropicResponseToChat
		exec.SanitizeAnthropicTools = relay.SanitizeAnthropicToolsInBody
		exec.NormalizeOpenAITools = relay.NormalizeToolsInChatBody
		// Strip minimax-private fields (nvext, base_resp, input_sensitive*,
		// output_sensitive*) from all non-stream chat responses before
		// returning to the client. Wired into both executeOpenAI (non-stream
		// path) and the StreamChat closure (stream path via stripFn).
		exec.StripMinimaxFields = relay.StripMinimaxFieldsBody
		exec.StreamTimeout = time.Duration(cfg.StreamTimeout) * time.Second
		exec.UpstreamTimeout = time.Duration(cfg.UpstreamTimeout) * time.Second
		exec.StreamRetryThreshold = cfg.StreamRetryThreshold
		// Memora: optional context-compression oracle. When the
		// LLM_GATEWAY_MEMORA_BASE_URL env is set, the executor can ask
		// Memora for L1 session facts on context overflow and rebuild
		// the body around them. Disabled by default (no env var).
		if memoraBase := os.Getenv("LLM_GATEWAY_MEMORA_BASE_URL"); memoraBase != "" {
			memoraClient = memora.NewClient(memora.ClientConfig{
				BaseURL: memoraBase,
				APIKey:  os.Getenv("LLM_GATEWAY_MEMORA_API_KEY"),
			})
			exec.Memora = memoraClient
			// Async sink: fire-and-forget write buffer for L1 session
			// memory persistence. 2 workers / 2048-deep queue is enough
			// for the write volume (one enqueue per successful request).
			memoraSink = memora.NewSink(memoraClient, 2, 2048)
			memoraSink.Start()
			exec.MemoraSink = memoraSink
			slog.Info("memora context-compression oracle enabled", "base_url", memoraBase)
		} else {
			slog.Info("memora context-compression oracle disabled (set LLM_GATEWAY_MEMORA_BASE_URL to enable)")
		}
		if dbConn != nil && dbConn.Enabled() {
			exec.State = credentialstate.NewWriter(dbConn.Pool())
			exec.DB = dbConn
			exec.HeaderProfiles = routing.NewHeaderProfileCache(dbConn.Pool())
		}
		exec.FpSlots = fpSlots
		exec.Provider = providerClient
		// Inject peak collector (after bg workers have started it).
		if peakCollector != nil {
			exec.PeakCollector = peakCollector
		}
		// Enable disguise mode if configured.
		if cfg.EnableDisguise {
			exec.DisguisePool = disguise.DefaultPool
			slog.Info("disguise mode enabled")
		}
		chatHandler.SetExecutor(exec, providerClient, stickyCache)
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
		slog.Info("telemetry emission enabled")
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
		} else {
			slog.Info("model discovery skipped (bg_mode=data-plane)")
		}
	}

	// ── Admin API ───────────────────────────────────────────────────────
	var adminHandler *admin.Handler
	if dbConn != nil && dbConn.Enabled() {
		adminHandler = admin.NewHandler(dbConn.Pool(), cfg.SecretKey, fernetKey)
		if keyring != nil {
			adminHandler.SetKeyring(keyring)
		}
		if discoverySvc != nil {
			adminHandler.SetDiscoveryService(discoverySvc)
		}

		// Ensure users table exists for multi-tenant admin auth
		migCtx, migCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := dbConn.EnsureUsersTable(migCtx); err != nil {
			slog.Error("failed to ensure users table", "error", err)
		}
		migCancel()

		// Seed initial admin user if table is empty
		admin.EnsureSeedAdmin(dbConn.Pool())

		seedCtx, seedCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if created, err := admin.SeedProvidersFromCatalog(seedCtx, dbConn.Pool()); err != nil {
			slog.Warn("provider catalog seed failed", "error", err)
		} else if created > 0 {
			slog.Info("seeded providers from catalog", "created", created)
		}
		seedCancel()
	}

	// ── Background Services ─────────────────────────────────────────────
	var credRecovery *bg.CredentialRecovery
	var credCycler *bg.CredentialCycler
	var credProbeV2 *bg.CredentialProbeV2
	var defaultProbePicker *bg.DefaultProbePicker
	var stickyCleaner *bg.StickyCleaner
	var envelopeCleaner *bg.EnvelopeCleaner
	var taxonomySync *bg.TaxonomySync
	// peakCollector / weeklyPeakRollup / slotSuggester are declared
	// at the top of main() so the executor can reference them.
	if dbConn != nil && dbConn.Enabled() {
		credRecovery = bg.NewCredentialRecovery(dbConn.Pool())
		credRecovery.Start(context.Background())
		if !bgDataPlaneOnly && fernetKey != nil {
			credCycler = bg.NewCredentialCycler(dbConn.Pool(), fernetKey)
			if keyring != nil {
				credCycler.SetKeyring(keyring)
			}
			credCycler.Start(context.Background())
		} else if bgDataPlaneOnly {
			slog.Info("credential cycler skipped (bg_mode=data-plane)")
		}

		// 900-series: v2 mini-chat probe (spec §5) — independent of v1 cycler
		if !bgDataPlaneOnly {
			credProbeV2 = bg.NewCredentialProbeV2(dbConn.Pool(), fernetKey)
			if keyring != nil {
				credProbeV2.SetKeyring(keyring)
			}
			credProbeV2.Start(context.Background())

			// 900-series: default probe model picker (spec §4.2.1) — daily 0:00
			defaultProbePicker = bg.NewDefaultProbePicker(dbConn.Pool())
			defaultProbePicker.Start(context.Background())
		}

		stickyCleaner = bg.NewStickyCleaner(dbConn.Pool())
		stickyCleaner.Start(context.Background())
		envelopeCleaner = bg.NewEnvelopeCleaner(dbConn.Pool())
		envelopeCleaner.Start(context.Background())
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

		// Weekly rollup + auto-tune suggester require writes to
		// credentials/audit; only run in "full" mode.
		if !bgDataPlaneOnly {
			weeklyPeakRollup = bg.NewWeeklyPeakRollup(dbConn.Pool())
			weeklyPeakRollup.Start(context.Background())

			slotSuggester = bg.NewSlotSuggester(dbConn.Pool())
			slotSuggester.Start(context.Background())

			autoIdx := autoroute.NewIndex()
			autoIdx.SetPool(dbConn.Pool())

			// v2.1: TuningStore provides dynamic keyword/threshold/weight
			// overrides from the tuning_params table. Reloaded on a 5-min
			// ticker aligned with auto_index_refresher. Falls back to
			// compiled defaults when the DB is empty (already seeded in
			// db.ensureTuningParamsSchema).
			tuningStore := autoroute.NewTuningStore(dbConn.Pool())
			if err := tuningStore.Reload(context.Background()); err != nil {
				slog.Warn("tuning_store initial reload failed, using defaults", "error", err)
			}
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
			adminHandler.SetBackgroundServices(credCycler, credRecovery, envelopeCleaner, stickyCleaner, taxonomySync)
			adminHandler.SetProbeServices(credProbeV2, defaultProbePicker)
			adminHandler.SetFpSlots(fpSlots)
			adminHandler.SetPeakCollector(peakCollector)
		}
		if memoraClient != nil {
			adminHandler.SetMemoraServices(memoraClient, memoraSink)
		}

		// v2.0.2 audit fix #6: admin auto-route refresh endpoint needs
		// the live AutoIndexRefresher wired in. Without this, /refresh
		// returns 503 "index refresher not wired".
		if autoIndexRefresher != nil && adminHandler != nil {
			adminHandler.SetAutoIndexRefresher(autoIndexRefresher)
		}

		bg.StartWorkTypeACCSync(context.Background(), dbConn.Pool(), func(ctx context.Context) error {
			return admin.SyncWorkTypesFromACCForBG(ctx, dbConn.Pool())
		})
	}

	// ── Static files (Vue SPA) ───────────────────────────────────────────
	staticHandler := relay.NewStaticHandler(cfg.StaticDir)

	// ── Router ────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	mux.Handle("/healthz", healthHandler)
	mux.Handle("/metrics", middleware.MetricsHandler())

	mux.Handle("/v1/chat/completions", chatHandler)
	mux.Handle("/v1/completions", chatHandler)
	mux.Handle("/v1/messages", messagesHandler)
	mux.Handle("/v1/responses", responsesHandler)
	mux.Handle("/v1/models", modelsHandler)

	if sessionMgr != nil {
		sessionHandler := sessions.NewHandler(sessionMgr)
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
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
				return
			}
			slog.Info("config: hot-reload succeeded")
			w.Header().Set("Content-Type", "application/json")
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
				w.Write([]byte(`{"service":"llm-gateway-go","version":"0.3.0"}`))
				return
			}
			http.NotFound(w, r)
		})
	}

	// Admin API routes
	if adminHandler != nil {
		adminHandler.RegisterRoutes(mux)
		slog.Info("admin API enabled")
	}

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

	srv := &http.Server{
		Addr:           cfg.Listen,
		Handler:        handler,
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
	if credCycler != nil {
		credCycler.Stop()
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


// buildAutoLLMCaller returns the LLMCaller to use for the auto-route
// fallback classifier.
//
// Selection logic:
//   1. If LLMGatewayAutoLLMEndpoint env var is set:
//        HTTPLlmCaller (OpenAI-compatible POST /chat/completions)
//        wrapped in CircuitBreakerCaller (5-failure / 30s cooldown)
//        wrapped in InstrumentedCaller (per-call metrics)
//   2. Otherwise:
//        DisabledCaller (no LLM call; decider falls back to the
//        heuristic result at low confidence)
//
// Environment variables consumed (all optional except Endpoint):
//   LLMGatewayAutoLLMEndpoint  base URL (e.g. "https://llm.kxpms.cn/v1")
//   LLMGatewayAutoLLMApiKey   bearer token
//   LLMGatewayAutoLLMModel    model name (default "gpt-4o-mini")
//   LLMGatewayAutoLLMTimeout  seconds (default 3)
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
