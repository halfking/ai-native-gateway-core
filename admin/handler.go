package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/distlock" // 2026-08-19 title-gen per-session distributed lock
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/domains/attachments"     //nolint:depguard // attachment download/list routes live in the admin mux
	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"   //nolint:depguard // 数据库降级模块
	"github.com/kaixuan/llm-gateway-go/domains/memory"          //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/modelquality"    // model-IQ backend interface (modelQualityBackend)
	"github.com/kaixuan/llm-gateway-go/domains/session"         //nolint:depguard // session state manager
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/stats"
	"github.com/kaixuan/llm-gateway-go/domains/stats/boardcache"
	v2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/internal/summarystore" //nolint:depguard // 2026-08-06 auto summary persistence
	"github.com/kaixuan/llm-gateway-go/internal/titlestore"   //nolint:depguard // durable title fencing/tombstone state
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/kaixuan/llm-gateway-go/security/ipblocklist"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/kaixuan/llm-gateway-go/security/sensitive"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/redis/go-redis/v9"
)

type controlledBodyDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Handler struct {
	db                   *pgxpool.Pool
	bodyDB               controlledBodyDB
	bodyServiceJWTSecret string
	secret               string
	encKey               []byte
	keyring              *secret.Keyring // AES-256-GCM keyring; nil → Fernet legacy
	discSvc              *discovery.Service
	credCycler           *bg.CredentialCycler
	credRecov            *bg.CredentialRecovery
	envCleaner           *bg.EnvelopeCleaner
	stickyClean          *bg.StickyCleaner
	taxSync              *bg.TaxonomySync
	probeV2              *bg.CredentialProbeV2  // 900-series: mini-chat probe (spec §5)
	probePicker          *bg.DefaultProbePicker // 900-series: default probe model (spec §4)
	modelProbe           *bg.ModelProbeRunner   // 2026-06-18: per-model re-probe of failing bindings (spec 2026-06-18-model-probe-rounds)
	// 2026-07-23: 系统监测模块 — 所有探测任务的唯一入口 (design §1.2 #1).
	// nil 时 /api/admin/system-monitor/* 端点 503；探测仍可能由旧 worker 跑。
	systemMonitor    SystemMonitorBackend
	systemMonitorSSE *SystemMonitorSSEHub
	// probeStreamHub (2026-08-11) fans out self-check / node-probe lifecycle
	// events to the dashboard 自检 tab over SSE, mirroring the live request
	// stream. nil when Redis is not wired — the route is then not registered.
	probeStreamHub *ProbeSSEHub
	// probeQueue (2026-08-13, 需求 6) is the unified self-check queue; exposed via
	// the public POST/DELETE /api/admin/probe/tasks API so external callers can
	// add/remove probe tasks without knowing internal details. nil in tests.
	probeQueue *bg.ProbeQueue
	// freePoolSSE (2026-08-11) fans out free-pool credential quota events
	// (rate_limited / quota_exhausted / recovered) to the 免费资源 tab over
	// SSE, so the operator sees state changes without manual refresh. nil when
	// Redis is not wired — the route is then not registered (polling still works).
	freePoolSSE *FreePoolSSEHub
	// 2026-06-23 Phase 2/3: backs /api/candidate-failures* endpoints.
	// Wired from cmd/gateway/main.go via SetCandidateFailureHandlers so
	// /alerts can read live data from the CandidateFailureMonitor.
	cfHandlers *candidateFailureHandlers
	fpSlots    *credentialfpslot.Manager
	// pendingStore (Track C C7, 2026-06-18) is the durable cache
	// for client reconnect and vendor async retry. nil disables
	// the /api/admin/pending-responses* endpoints; the GET
	// endpoint on /v1/sessions/{id}/pending-response is
	// unaffected (it lives in the sessions package, not here).
	pendingStore  *pending.Store
	settingsStore *settings.StoreDB // settings-management: DB-backed settings backend (Q1: B)
	// liveStreamHub (2026-07-03) fans out newly-persisted
	// request_logs rows to dashboard SSE clients at
	// GET /api/admin/live-stream. nil disables the endpoint.
	liveStreamHub *LiveStreamSSEHub
	// requestTraceHandler (2026-07-17) exposes the per-request trace
	// viewer + AI prompt builder. nil hides the endpoints.
	requestTraceHandler *RequestTraceHandler
	// boardCache (2026-07-14) Redis baseline+delta for dashboard board API.
	boardCache *boardcache.Service
	// boardOperationalCache caches discovery/probe/selfcheck for /dashboard/operational.
	boardOperationalCache *boardOperationalCache
	// opsOverviewCache bundles ops overview stats for /api/admin/ops/overview.
	opsOverviewCache *opsOverviewCache
	// 2026-08-17 OPTIMIZATION: in-memory LRU+TTL cache for fetchRequestBodies.
	// Reduces repeat dashboard click latency from 5s columnar scan to < 1ms.
	// Stats surfaced via admin/stats endpoint (see admin/handler.go stats handler).
	bodyFetchCache *bodyFetchCache
	// ipBlocklist backs security IP denylist admin + request gate cache.
	ipBlocklist *ipblocklist.Service
	// bodySizeTracker (2026-07-25) tracks request/response body size stats in Redis
	// for real-time dashboard display. nil disables body size metrics.
	bodySizeTracker *stats.BodySizeTracker
	// sensitiveWordEngine (2026-07-18) backs the admin sensitive-words
	// management endpoints (reload, status, match). nil disables the
	// endpoints. Wired from cmd/gateway/main.go.
	sensitiveWordEngine *sensitive.SensitiveWordEngine
	// sanitizePatternDetector reloads SmartSaniGuard's YAML rule set through
	// the same admin operation as sensitive words.
	sanitizePatternDetector *sanitize.PatternDetector
	// routeIncidentHandler (2026-07-13) backs the read-only
	// /api/admin/route-incidents* endpoints. nil disables the
	// diagnose feature on the swim lane.
	routeIncidentHandler *RouteIncidentsHandler
	// providerCostReconciliationHandler (2026-08-15, M3 CO-3) backs the
	// /api/admin/provider-cost-reconciliation* endpoints (月度账单导入 +
	// diff 查询). nil (default) keeps the endpoints unregistered —
	// flag-off behavior is identical to main.
	providerCostReconciliationHandler *ProviderCostReconciliationHandler
	peakCollector                     interface {
		Acquire(credID int64, model string)
		Release(credID int64, model string)
		Stats() map[string]interface{}
		GetLiveConcurrent(credID int64, model string) int64
	}
	// autoIndexRefresher is wired from cmd/gateway/main.go to let the
	// /api/admin/auto-route/refresh endpoint trigger immediate
	// credential_model_index refresh. nil in test mode.
	autoIndexRefresher interface {
		RefreshOnce(ctx context.Context) error
	}
	feedbackAnalyzer interface {
		AnalyzeOnce(ctx context.Context) error
	}
	// memoraClient provides connectivity status for the admin UI.
	// Structural interface avoids importing the memora package directly.
	memoraClient interface {
		Disabled() bool
		Ping(ctx context.Context) error
		BaseURL() string
		SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error)
	}
	// memoraSink provides write-path stats for the admin UI.
	memoraSink interface {
		Stats() memory.Stats
		Pause()
		Resume()
	}
	// stateManager (2026-06-30) provides live credential×model state for
	// the /api/credentials/{id}/test + /state endpoints and feeds the
	// router with real-time availability decisions. nil disables the
	// state-management feature; routing falls back to DB-based health.
	stateManager credentialstate.StateProvider
	// modelPolicy (Round 48, 2026-06-21) is the tenant-scoped model
	// denylist cache.  admin handlers call Invalidate after every
	// write so the next chat request sees the change without waiting
	// for the 60s TTL.  Interface avoids an import cycle (admin
	// → relay would be ugly; relay → admin already exists).
	modelPolicy interface {
		Invalidate(tenantID string)
	}
	// keyVerifier (2026-07-17) invalidates the KeyInfo cache when admin
	// writes (update rate limits, enable/disable, revoke, patch profile)
	// change a key, so the next chat request reloads from DB instead of
	// serving a stale entry for up to 60s. Interface avoids importing
	// domains/authentication here.
	keyVerifier interface {
		InvalidateKeyID(id int)
	}
	// modelQualityBackend (2026-08-11) backs the model-IQ admin endpoints:
	//   POST /api/admin/model-iq/trigger  — run an on-demand IQ test on one node
	// nil disables the trigger endpoint (history/node-latest/catalog still work
	// by reading the DB directly). Wired from cmd/gateway/main.go.
	modelQualityBackend interface {
		TestSingleNode(ctx context.Context, credentialID int, rawModel string) (*modelquality.QualityScore, error)
	}
	// providerSettingsResolver (2026-06-20) provides provider-level setting
	// overrides for compression, cache, etc. Wired from cmd/gateway/main.go.
	providerSettingsResolver *settings.ProviderSettingsResolver
	refreshMu                sync.Mutex            // guards lazy init of refreshState
	refreshState             *providerRefreshState // per-provider "refresh model list" tracking (see providers.go)
	// healthTracker (2026-06-22) provides access to the routing health tracker
	// for credential monitor endpoints. Wired from cmd/gateway/main.go.
	healthTracker interface {
		Enabled() bool
	}
	redisClient            interface{}                   // Redis client for sliding window access
	titleDistLock          distlock.Manager              // 2026-08-19 per-session title-gen distributed lock; defaults to in-process LocalManager
	titleStore              *titlestore.Store             // durable title fencing/tombstone state
	analysisMetadataStore   *sessionmeta.MetadataStore    // arrival/final session analysis metadata
	availabilityReader     *bg.ModelAvailabilityReader   // 2026-06-29 mirror of unified probe state to Redis
	availabilityBackfill   *bg.AvailabilityCacheBackfill // 2026-06-29 on-demand DB→Redis cache rebuild
	availabilityKeyCounter *bg.AvailabilityKeyCounter    // 2026-06-29 on-demand SCAN-based key count
	autoTitleGen           *AutoTitleGenerator           // Auto session title generator (2026-06-22)
	autoSummaryGen         *AutoSummaryGenerator         // 2026-08-06 incremental session summary via map-reduce
	sessionManager         *session.Manager              // 2026-07-06 会话状态管理 (state, cred rotation, lifecycle)
	sessionDBWriter        *session.DBWriter             // 2026-07-06 批量异步写 DB worker
	sessionCleanupWorker   *session.CleanupWorker        // 2026-07-06 清理过期 stopped session

	// 数据库降级模块 (2026-07-10)
	dbMonitor   *dbdegradation.Monitor
	fileReader  *dbdegradation.FileReader
	recovery    *dbdegradation.Recovery
	ttlManager  *dbdegradation.TTLManager
	degradation *degradationControl

	dashboardEventRecorder interface {
		RecordAccess(tenantID, userID, userRole, sessionID, apiPath, apiMethod string, statusCode int, responseTime time.Duration, cacheHit bool)
	}

	// identityPool is the legacy Layer 0 cap on total distinct end-user fingerprints.
	// nil when the global cap feature is disabled.
	// Wired from cmd/gateway/main.go.
	// Defined as an interface here to avoid a direct package dependency.
	identityPool interface {
		Stats(ctx context.Context) interface{}
		SetMaxIdentities(n int)
	}

	// approvalMgr (2026-06-27) 会话审批管理器，用于审批高风险会话
	approvalMgr *sessionaudit.ApprovalManager

	// approvalResumeHandler (2026-07-03) 审批恢复处理器，用于在审批通过后恢复 LLM 调用
	approvalResumeHandler interface {
		ResumeAfterApproval(ctx context.Context, approvalID, tenantID string) error
	}

	// configManager (2026-07-08) 审批配置管理器，用于管理租户审批规则
	configManager ConfigManagerService

	// attachmentHandler (2026-07-01, migration 325) 提供附件下载
	// (GET /api/attachments/{path...}) 与按请求列出附件元数据
	// (GET /api/logs/{request_id}/attachments) 的能力。nil 时附件下载
	// 端点返回 503，按请求列表端点返回空数组。由 cmd/gateway/main.go
	// 在 attachmentStorage 初始化成功后通过 SetAttachmentHandler 注入。
	attachmentHandler *attachments.Handler
	// attachmentStorage (2026-07-02) 持有附件文件系统存储实例，用于
	// 运行时热切换目录（迁移）与文件系统统计。nil 表示未启用文件存储。
	attachmentStorage AttachmentStorageService
	// migrationState / migrationMu 用于「存储目录迁移」的异步进度跟踪
	// （复制旧目录→切换 BaseDir→删除旧目录）。lazy 初始化，与 provider
	// refresh 的 providerRefreshState 同构。见 admin/storage_migration.go。
	migrationState *migrationState
	migrationMu    sync.Mutex
	// pendingMigrationRunID 在 PUT /api/admin/storage/config 触发迁移后短暂
	// 置位，供随后的 GET-assemble 响应带上 migration_run_id。atomic.Value
	// 避免与并发 GET 竞争。每次 PUT 末尾复位为 ""。
	pendingMigrationRunID atomic.Value // string

	// hotJobMgr 持有所有 hot 表迁移的异步任务状态；详见 data_lifecycle_hot_partition.go。
	// 在 NewHandler 中初始化，永不为 nil。
	hotJobMgr *hotJobManager

	// lifecycleJobs 是通用异步任务注册表（drop partition / vacuum / reindex）；
	// 详见 data_lifecycle_jobs.go。在 NewHandler 中初始化，永不为 nil。
	lifecycleJobs *jobRegistry

	// hotCron 是 hot 表夜间自动迁移调度器；StartHotCron() 启动 goroutine。
	// nil 表示未启用（DB 不可用 / 测试模式）。
	hotCron *HotCronScheduler

	// jobRegistry 是通用异步任务注册表（drop partition / vacuum / reindex）；
	// 详见 data_lifecycle_jobs.go。在 NewHandler 中初始化，永不为 nil。
	jobRegistry   *jobRegistry
	jobRegistryMu sync.Mutex

	// ursmV2 (2026-07-24) 是 URSM v2 Manager，用于紧急修复操作时
	// 同时清除 Redis 中的冷却/错误计数状态。通过 SetURSMv2 注入。
	ursmV2 *v2.Manager

	// circuitResetter (2026-08-15) resets the in-process credential circuit
	// breaker so emergency repair (force_enable / clear_circuit) takes effect
	// on the request hot path immediately instead of waiting for the in-memory
	// OPEN cooling window. nil → in-memory breakers are left untouched.
	circuitResetter interface {
		Reset(providerID, credentialID int)
	}

	// credStateRecoverer (2026-08-15) marks the legacy credentialstate cache
	// (mem 10s / Redis 5min) as available with a probe-success semantics so
	// emergency repair is not blocked by stale Unavailable=false entries.
	// nil → credentialstate is left to its 60s recovery ticker / Redis TTL.
	credStateRecoverer interface {
		UpdateFromProbe(ctx context.Context, state *credentialstate.State)
	}

	// probeSubmitter (2026-08-23, hzx-2 audit) lets the focused
	// /api/routing/credentials/{id}/reset-state endpoint trigger an
	// immediate self-check probe so the operator doesn't have to wait for
	// the next 5-min tick to learn whether the upstream is back.
	// nil → trigger_probe=true is silently ignored.
	//
	// Stored as a function value (not an interface) so cmd/gateway/main.go
	// can wire a closure around the late-constructed NodeProbeWorker
	// without needing a named adapter type.
	probeSubmitter func(credentialID int, rawModel, tenantID, parentReqID string)

	// autoHealOneShot (2026-08-23, hzx-2 audit) is the bg.CredentialAutoHealWorker.OneShot
	// method bound to the credentialAutoHealWorker instance. The reset-state
	// endpoint calls it synchronously after a successful reset so the
	// self-heal probes start within the same request instead of waiting
	// for the next 5-min tick. Returns the number of (cred, model) pairs
	// submitted. nil → the endpoint silently skips this step.
	autoHealOneShot func(ctx context.Context, credentialID int) int

	// rateLimiter (V3.2-LP5, 2026-08-14) 节点操作限流器：test-now 1req/s per-cred + 10req/min per-operator。
	rateLimiter *nodeOperationsRateLimiter
	// statsShadowExecutor runs bounded, read-only legacy/canonical comparisons.
	statsShadowExecutor *statsShadowExecutor
	// auditLogger (V3.2-LP5, 2026-08-14) 节点操作审计：异步写入 request_state_transitions。
	auditLogger *nodeOperationAuditLogger
}

func NewHandler(db *pgxpool.Pool, secretKey string, encKey []byte) *Handler {
	h := &Handler{
		db:                   db,
		bodyDB:               db,
		bodyServiceJWTSecret: strings.TrimSpace(os.Getenv("LLM_GATEWAY_SESSION_SERVICE_JWT_SECRET")),
		secret:               secretKey,
		encKey:               encKey,
		rateLimiter:          newNodeOperationsRateLimiter(),
		auditLogger:          newNodeOperationAuditLogger(db),
		// 2026-08-17 OPTIMIZATION: LRU 1024 entries × 5min TTL.
		// 1024 entries × ~20KB/entry ≈ 20MB max footprint (rule 23 §3).
		// 5min TTL ≈ body 数据写入后罕见被修改（rule 36 §1 持久化）。
		bodyFetchCache:      newBodyFetchCache(1024, 5*time.Minute),
		statsShadowExecutor: newStatsShadowExecutor(db),
		// 2026-08-19: title generation lock defaults to the in-process
		// LocalManager so no-Redis deployments still benefit from
		// single-flight semantics within a single replica. The wiring
		// code in cmd/gateway/main.go upgrades this to a Redis-backed
		// manager once the cluster Redis client is healthy.
		titleDistLock: distlock.NewLocalManager(),
		titleStore:            titlestore.New(db),
		analysisMetadataStore: sessionmeta.NewMetadataStore(db),
	}

	// Initialize auto title generator
	h.autoTitleGen = NewAutoTitleGenerator(h)
	// 2026-08-06: initialize incremental-rolling session summary generator.
	// Persists via internal/summarystore so the v2 dispatch worker and the
	// on-request path share one row shape.
	h.autoSummaryGen = NewAutoSummaryGenerator(h, summarystore.NewStore(db))
	// 2026-07-13: hot 表异步迁移任务注册表（内存）；异步任务状态由前端轮询查询
	h.hotJobMgr = newHotJobManager()
	// 2026-07-13: 通用异步任务注册表，管理 drop partition / vacuum / reindex 等任务
	// lazy init via getJobRegistry()
	return h
}

// SetAttachmentHandler wires the attachment download/list handler. Called
// from cmd/gateway/main.go after attachmentStorage is successfully built.
func (h *Handler) SetAttachmentHandler(ah *attachments.Handler) {
	h.attachmentHandler = ah
}

// SetAttachmentStorage wires the attachment filesystem storage instance.
// Called from cmd/gateway/main.go；用于运行时目录迁移与文件系统统计。
func (h *Handler) SetAttachmentStorage(s AttachmentStorageService) {
	h.attachmentStorage = s
}

// SetModelPolicy (Round 48, 2026-06-21) wires the tenant-scoped model
// denylist cache so admin write endpoints can invalidate per-tenant
// entries.  Called from cmd/gateway/main.go after constructing both
// the admin Handler and the modelpolicy.Checker.
func (h *Handler) SetModelPolicy(mp interface{ Invalidate(string) }) {
	h.modelPolicy = mp
}

// SetKeyVerifier (2026-07-17) wires the KeyInfo cache so admin write endpoints
// (updateKeyLimits, setKeyEnabled, deleteKey, patchKey) can invalidate a key's
// cached entry after mutating it, making rate-limit / status changes effective
// immediately instead of after the 60s TTL. Called from cmd/gateway/main.go.
func (h *Handler) SetKeyVerifier(kv interface{ InvalidateKeyID(id int) }) {
	h.keyVerifier = kv
}

// invalidateKeyCache drops the cached KeyInfo for an api_key after a write.
// Safe to call when no verifier is wired (no-op). Called by admin write
// endpoints in admin/keys.go so rate-limit / status changes apply on the next
// request instead of after the 60s TTL.
func (h *Handler) invalidateKeyCache(id int) {
	if h.keyVerifier != nil {
		h.keyVerifier.InvalidateKeyID(id)
	}
}

// SetProviderSettingsResolver (2026-06-20) wires the provider-level settings
// resolver so admin endpoints can clear cache after updates.
func (h *Handler) SetProviderSettingsResolver(psr *settings.ProviderSettingsResolver) {
	h.providerSettingsResolver = psr
}

// SetHealthTracker (2026-06-22) wires the routing health tracker for credential monitor endpoints.
func (h *Handler) SetHealthTracker(ht interface{ Enabled() bool }) {
	h.healthTracker = ht
}

// SetRedisClient (2026-06-22) wires the Redis client for sliding window access.
func (h *Handler) SetRedisClient(rc interface{}) {
	h.redisClient = rc
}

// SetTitleDistLock (2026-08-19) upgrades the title-generation distributed
// lock from the in-process LocalManager (set in NewHandler) to a
// Redis-backed manager. Safe to leave unset when Redis is unavailable —
// the title pipeline will still acquire the lock, just without
// cross-replica coordination. The title DistLock guards the leader /
// follower pattern for both the streaming auto-title pipeline and the
// admin summarize-title / PUT / DELETE handlers.
func (h *Handler) SetTitleDistLock(m distlock.Manager) {
	if m == nil {
		return
	}
	h.titleDistLock = m
}

// SetAvailabilityReader (2026-06-29) wires the Redis availability reader
// so admin endpoints can query the unified probe-state cache directly.
func (h *Handler) SetAvailabilityReader(r *bg.ModelAvailabilityReader) {
	h.availabilityReader = r
}

// SetAvailabilityBackfill (2026-06-29) wires the periodic DB→Redis cache
// rebuild worker. The admin /api/admin/probe/cache-rebuild endpoint
// invokes it on demand; the worker also runs every interval if started.
func (h *Handler) SetAvailabilityBackfill(b *bg.AvailabilityCacheBackfill) {
	h.availabilityBackfill = b
}

// SetAvailabilityKeyCounter (2026-06-29) wires the SCAN-based
// keyspace cardinality worker. The admin /api/admin/probe/cache-keys
// endpoint invokes CountOnce on demand; the worker also runs every
// interval if started.
func (h *Handler) SetAvailabilityKeyCounter(k *bg.AvailabilityKeyCounter) {
	h.availabilityKeyCounter = k
}

// GetAutoTitleGenerator (2026-06-22) returns the auto title generator for use by routing package.
func (h *Handler) GetAutoTitleGenerator() *AutoTitleGenerator {
	return h.autoTitleGen
}

// GetAutoSummaryGenerator (2026-08-06) returns the auto summary generator
// for use by the streaming chat handler. Symmetric with
// GetAutoTitleGenerator.
func (h *Handler) GetAutoSummaryGenerator() *AutoSummaryGenerator {
	return h.autoSummaryGen
}

// SetKeyring configures AES-256-GCM key rotation.  Call this at startup after
// loading the KEYRING_JSON environment variable.
func (h *Handler) SetKeyring(kr *secret.Keyring) {
	h.keyring = kr
}

// SetLiveStreamSSE wires the dashboard swim-lane SSE hub. Once set,
// GET /api/admin/live-stream starts streaming real-time request
// telemetry to connected dashboards. Pass nil to disable.
func (h *Handler) SetLiveStreamSSE(hub *LiveStreamSSEHub) {
	h.liveStreamHub = hub
}

// SetRequestTraceHandler (2026-07-17) wires the trace viewer endpoints.
// nil disables the page.
func (h *Handler) SetRequestTraceHandler(rth *RequestTraceHandler) {
	h.requestTraceHandler = rth
}

// SetRouteIncidentsHandler wires the read-only route-incident API
// (Phase 1, 2026-07-13). Once set, /api/admin/route-incidents* and
// the /incident_update SSE envelope become available. Pass nil to
// disable (the swim lane's diagnose button is hidden in that case).
func (h *Handler) SetRouteIncidentsHandler(rih *RouteIncidentsHandler) {
	h.routeIncidentHandler = rih
}

// SetProviderCostReconciliationHandler wires the provider cost
// reconciliation API (M3 CO-3, 2026-08-15). Once set,
// POST /api/admin/provider-cost-reconciliation/bill and
// GET  /api/admin/provider-cost-reconciliation become available.
// Pass nil (or leave unset) to keep the endpoints unregistered.
func (h *Handler) SetProviderCostReconciliationHandler(hh *ProviderCostReconciliationHandler) {
	h.providerCostReconciliationHandler = hh
}

// SetSessionManager (2026-07-06) wires the session.Manager for the
// /api/admin/sessions/* endpoints. Pass nil to disable.
func (h *Handler) SetSessionManager(m *session.Manager) {
	h.sessionManager = m
}

// SetSessionDBWriter (2026-07-06) wires the batch DB writer.
func (h *Handler) SetSessionDBWriter(w *session.DBWriter) {
	h.sessionDBWriter = w
}

// SetSessionCleanupWorker (2026-07-06) wires the cleanup worker.
func (h *Handler) SetSessionCleanupWorker(c *session.CleanupWorker) {
	h.sessionCleanupWorker = c
}

// encryptCred encrypts a plaintext credential using AES-256-GCM (if keyring is
// configured) or Fernet-CBC legacy (backward-compat fallback).
// Returns the envelope string ready to store in key_ciphertext.
//
// 2026-08-18 (incident 154): a round-trip self-check is now performed before
// returning. The 154 production failure was caused by raw Fernet bytes being
// stored without base64-encode, leaving the column unreadable by DecryptAny.
// The self-check catches any future drift in the Encrypt path — if a caller
// returns an envelope that cannot be re-decrypted by the active keyring/fernet
// key, we refuse to hand it back, so the bad value never reaches the DB.
func (h *Handler) encryptCred(plaintext []byte) (string, error) {
	var envelope string
	if h.keyring != nil {
		out, err := secret.EncryptAESGCM(plaintext, h.keyring)
		if err != nil {
			return "", err
		}
		envelope = out
	} else {
		enc, err := encryptFernet(plaintext, h.encKey)
		if err != nil {
			return "", err
		}
		envelope = string(enc)
	}
	// round-trip: ensure decryptCred can read back exactly the plaintext we
	// just encrypted. If it fails, fail closed so callers never persist a
	// ciphertext the gateway can't later decrypt.
	if _, _, derr := h.decryptCred(envelope); derr != nil {
		return "", fmt.Errorf("encryptCred round-trip failed: %w", derr)
	}
	return envelope, nil
}

// decryptCred decrypts a credential stored as either a v1 AES-GCM envelope or
// a legacy Fernet token.  Returns (plaintext, isLegacy, error).
// When isLegacy=true the caller MAY re-encrypt and update the DB row.
func (h *Handler) decryptCred(ciphertext string) (string, bool, error) {
	if secret.IsV1Envelope(ciphertext) {
		if h.keyring == nil {
			return "", false, errorf("AES-GCM keyring not configured")
		}
		pt, err := secret.DecryptAESGCM([]byte(ciphertext), h.keyring)
		if err != nil {
			return "", false, err
		}
		return string(pt), false, nil
	}
	// Legacy Fernet path
	if len(h.encKey) != 32 {
		return "", false, errorf("legacy encryption key not configured")
	}
	pt, err := decryptFernet([]byte(ciphertext), h.encKey)
	return pt, err == nil, err
}

// decryptCredStr is a convenience wrapper over decryptCred that returns only
// the plaintext string and an error, discarding the isLegacy flag.
// Use this where lazy re-encryption is not needed.
func (h *Handler) decryptCredStr(ciphertext string) (string, error) {
	pt, _, err := h.decryptCred(ciphertext)
	return pt, err
}

func (h *Handler) SetDashboardEventRecorder(recorder interface {
	RecordAccess(tenantID, userID, userRole, sessionID, apiPath, apiMethod string, statusCode int, responseTime time.Duration, cacheHit bool)
}) {
	h.dashboardEventRecorder = recorder
}

func (h *Handler) SetDiscoveryService(svc *discovery.Service) {
	h.discSvc = svc
}

// SetApprovalManager (2026-06-27) 注入审批管理器
func (h *Handler) SetApprovalManager(mgr *sessionaudit.ApprovalManager) {
	h.approvalMgr = mgr
}

// SetApprovalResumeHandler (2026-07-03) 注入审批恢复处理器
func (h *Handler) SetApprovalResumeHandler(handler interface {
	ResumeAfterApproval(ctx context.Context, approvalID, tenantID string) error
}) {
	h.approvalResumeHandler = handler
}

// SetConfigManager (2026-07-08) wires the approval config manager for tenant approval rules.
// Called from cmd/gateway/main.go after constructing the config manager.
func (h *Handler) SetConfigManager(cm ConfigManagerService) {
	h.configManager = cm
}

func (h *Handler) SetBackgroundServices(credCycler *bg.CredentialCycler, credRecov *bg.CredentialRecovery, envCleaner *bg.EnvelopeCleaner, stickyClean *bg.StickyCleaner, taxSync *bg.TaxonomySync) {
	h.credCycler = credCycler
	h.credRecov = credRecov
	h.envCleaner = envCleaner
	h.stickyClean = stickyClean
	h.taxSync = taxSync
}

// SetProbeServices injects the 900-series background services (spec §4-5).
func (h *Handler) SetProbeServices(probeV2 *bg.CredentialProbeV2, picker *bg.DefaultProbePicker) {
	h.probeV2 = probeV2
	h.probePicker = picker
}

// SetModelProbeRunner wires the per-model re-probe worker (spec
// 2026-06-18-model-probe-rounds).  nil-safe — admin keeps working
// without the manual-trigger endpoint if the worker isn't running.
func (h *Handler) SetModelProbeRunner(r *bg.ModelProbeRunner) { h.modelProbe = r }

// SetModelQualityBackend wires the model-quality worker for the on-demand
// node IQ test endpoint (POST /api/admin/model-iq/trigger). Pass nil to
// disable only that endpoint; history/node-latest/catalog read the DB directly.
func (h *Handler) SetModelQualityBackend(b interface {
	TestSingleNode(ctx context.Context, credentialID int, rawModel string) (*modelquality.QualityScore, error)
}) {
	h.modelQualityBackend = b
}

// SetStateManager wires the credential-state manager for /api/credentials/*/state
// and /api/credentials/*/test endpoints. Pass nil to disable.
func (h *Handler) SetStateManager(sm credentialstate.StateProvider) { h.stateManager = sm }

func (h *Handler) SetFpSlots(m *credentialfpslot.Manager) {
	h.fpSlots = m
}

// SetPeakCollector injects the concurrency peak collector so the admin
// API can report live stats. Accepts a structural interface to avoid
// an import cycle with the bg package's optional methods.
func (h *Handler) SetPeakCollector(pc interface {
	Acquire(credID int64, model string)
	Release(credID int64, model string)
	Stats() map[string]interface{}
	GetLiveConcurrent(credID int64, model string) int64
}) {
	h.peakCollector = pc
}

// SetAutoIndexRefresher injects the bg.AutoIndexRefresher so the
// /api/admin/auto-route/refresh endpoint can trigger an immediate
// credential_model_index refresh. Accepts a structural interface to
// avoid an import cycle with the bg package.
func (h *Handler) SetAutoIndexRefresher(r interface {
	RefreshOnce(ctx context.Context) error
}) {
	h.autoIndexRefresher = r
}

// SetPendingStore (Track C C7, 2026-06-18) injects the durable
// pending response cache. nil disables the /api/admin/pending-
// responses* endpoints; the operator can still hit the public
// /v1/sessions/{id}/pending-response endpoint (lives in
// sessions/handler.go, not here).
func (h *Handler) SetPendingStore(s *pending.Store) {
	h.pendingStore = s
}

// SetURSMv2 (2026-07-24) injects the URSM v2 Manager so that emergency
// repair operations can also clear Redis state (cooling/disabled/fail_streak).
func (h *Handler) SetURSMv2(m *v2.Manager) {
	h.ursmV2 = m
}

// SetCircuitResetter (2026-08-15) injects the in-process circuit breaker
// manager (domains/credential.Manager) so emergency repair can reset a
// single credential's breaker immediately.
func (h *Handler) SetCircuitResetter(r interface {
	Reset(providerID, credentialID int)
}) {
	h.circuitResetter = r
}

// SetCredStateRecoverer (2026-08-15) injects the credentialstate manager so
// emergency repair can force-recover its cached state via UpdateFromProbe.
func (h *Handler) SetCredStateRecoverer(r interface {
	UpdateFromProbe(ctx context.Context, state *credentialstate.State)
}) {
	h.credStateRecoverer = r
}

// SetProbeSubmitter (2026-08-23, hzx-2 audit) wires the focused
// /api/routing/credentials/{id}/reset-state endpoint to a probe-submitting
// function. Optional — when nil the reset endpoint still works but
// trigger_probe=true is silently ignored.
func (h *Handler) SetProbeSubmitter(s func(credentialID int, rawModel, tenantID, parentReqID string)) {
	h.probeSubmitter = s
}

// SetAutoHealOneShot (2026-08-23, hzx-2 audit) wires the bg.CredentialAutoHealWorker.OneShot
// method into the focused reset-state endpoint so a successful reset kicks
// off self-heal probes immediately. Optional — when nil the endpoint skips
// the immediate self-heal submission.
func (h *Handler) SetAutoHealOneShot(f func(ctx context.Context, credentialID int) int) {
	h.autoHealOneShot = f
}

// SetSettingsStore (settings-management, 2026-06-20) injects the
// DB-backed settings backend so /api/admin/settings/* endpoints
// can read/write settings_kv / tenant_settings_kv.
func (h *Handler) SetSettingsStore(s *settings.StoreDB) {
	h.settingsStore = s
}

// SetFeedbackAnalyzer wires the daily feedback analyzer for tuning endpoints.
func (h *Handler) SetFeedbackAnalyzer(a interface {
	AnalyzeOnce(ctx context.Context) error
}) {
	h.feedbackAnalyzer = a
}

// SetMemoraServices wires the Memora client and sink so the admin UI
// can display connectivity status and sink statistics.
func (h *Handler) SetMemoraServices(client interface {
	Disabled() bool
	Ping(ctx context.Context) error
	BaseURL() string
	AddMessage(ctx context.Context, userID string, messages []memory.Message, info map[string]any) error
	Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error)
	SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error)
}, sink interface {
	Stats() memory.Stats
	Pause()
	Resume()
}) {
	h.memoraClient = client
	h.memoraSink = sink
}

func (h *Handler) fpSlotsDefaultLimit() int {
	if h.fpSlots == nil {
		return 5
	}
	return h.fpSlots.DefaultLimit()
}

func (h *Handler) SetIPBlocklist(svc *ipblocklist.Service) {
	h.ipBlocklist = svc
}

// SetSensitiveWordEngine (2026-07-18) wires the sensitive word AC engine
// for the /api/admin/sensitive-words/* endpoints. Pass nil to disable.
func (h *Handler) SetSensitiveWordEngine(engine *sensitive.SensitiveWordEngine) {
	h.sensitiveWordEngine = engine
}

// SetSanitizePatternDetector wires the SmartSaniGuard YAML detector for admin reload.
func (h *Handler) SetSanitizePatternDetector(detector *sanitize.PatternDetector) {
	h.sanitizePatternDetector = detector
}

func (h *Handler) admin(fn http.HandlerFunc) http.HandlerFunc {
	return AdminMiddleware(fn, h.db, h.secret)
}

// superAdmin wraps the handler with auth + super_admin role enforcement.
// tenant_admin gets 403 Forbidden.
func (h *Handler) superAdmin(fn http.HandlerFunc) http.HandlerFunc {
	return SuperAdminMiddleware(fn, h.db, h.secret)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Public routes (no Bearer admin key)
	mux.HandleFunc("/api/auth/token", h.handleLogin)
	mux.HandleFunc("/api/auth/logout", h.handleLogout)
	mux.HandleFunc("/api/system/version", h.handleSystemVersion)    // Public endpoint for frontend version display
	mux.HandleFunc("/api/system/client-error", h.handleClientError) // Frontend error reporting (public, rate-limited)
	// JWT-authenticated routes (JWT or admin key)
	mux.Handle("/v1/keys/apply", h.admin(h.handleV1KeysApply))

	admin := h.admin
	// JWT-authenticated routes (JWT or admin key)
	mux.HandleFunc("/api/auth/me", admin(h.handleAuthMe))
	mux.HandleFunc("/api/auth/change-password", admin(h.handleChangePassword))
	mux.HandleFunc("/api/users", admin(h.handleUsers))
	mux.HandleFunc("/api/admin/audit-logs", h.superAdmin(h.handleListAuditLogs))
	mux.HandleFunc("/api/admin/format-anomaly-summary", h.superAdmin(h.handleFormatAnomalySummary))
	mux.HandleFunc("/api/admin/format-anomalies", h.superAdmin(h.handleFormatAnomalies))
	mux.HandleFunc("/api/admin/format-anomalies/", h.superAdmin(h.handleFormatAnomalySubrouter))
	// 2026-07-28: model integrity detection (model_mismatch,
	// finish_refusal, finish_truncation, empty_response, repeated_content,
	// fingerprint_drift). See domains/streaming/integrity/ and
	// admin/model_integrity.go for the read/write surface.
	mux.HandleFunc("/api/admin/model-integrity", h.superAdmin(h.handleModelIntegrity))
	mux.HandleFunc("/api/admin/model-integrity/", h.superAdmin(h.handleModelIntegrity))
	mux.HandleFunc("/api/admin/tenants", h.superAdmin(h.handleTenants))
	mux.HandleFunc("/api/admin/tenants/", h.superAdmin(h.handleTenants))
	mux.HandleFunc("/api/users/", admin(h.handleUsers))
	mux.HandleFunc("/api/routing/resolve", admin(h.handleRoutingResolve))
	mux.HandleFunc("/api/routing/overview", admin(h.handleRoutingOverview))
	mux.HandleFunc("/api/routing/recent-model-failures", admin(h.handleRoutingRecentModelFailures))
	// 2026-07-24: routing-v2 resolve 页"候选设置"写入端点（仅 super_admin）。
	// 仅允许改 credential_model_bindings 的 manual_priority / routing_tier /
	// weight / priority 四个排序相关字段；状态/熔断/可用性等硬规则必须走原有监控路径。
	mux.HandleFunc("/api/routing/candidate-binding/", h.superAdmin(h.handleRoutingCandidateBindingUpdate))
	mux.HandleFunc("/api/routing/candidate-bindings/reorder", h.superAdmin(h.handleRoutingCandidateBindingReorder))
	// 2026-07-24: emergency repair endpoint for routing-v2 resolve page.
	// Supports: force_enable, force_disable, clear_circuit, reset_errors.
	// Only super_admin can access. All actions are audited.
	mux.HandleFunc("/api/routing/emergency-repair", h.superAdmin(h.handleEmergencyRepair))

	// 2026-08-23 (hzx-2 audit): focused credential state reset. Body
	// {reason, raw_model?, trigger_probe?}; runs the same DB + in-memory
	// + URSM v2 chain as emergency-repair force_enable, with optional
	// immediate self-check probe. super_admin only.
	mux.HandleFunc("POST /api/routing/credentials/{id}/reset-state", h.superAdmin(h.handleResetCredentialState))

	// NOTE: /api/credentials/monitor-summary is registered later in
	// RegisterMonitorRoutes (line ~460) via NewCredentialMonitorHandlers.
	// Do NOT register it here to avoid mux.HandleFunc panic.
	// Controlled body resolver: service JWT only; intentionally bypasses the
	// human admin middleware and never exposes the /api/logs surface.
	mux.HandleFunc("/api/admin/bodies/{body_ref...}", h.handleControlledBody)
	mux.HandleFunc("/api/admin/compression/stats", admin(h.handleCompressionStats))
	mux.HandleFunc("/api/admin/compression/sessions", admin(h.handleCompressionSessions))

	// 2026-08-17 OPTIMIZATION: bodyFetchCache 命中率/淘汰数可观测端点。
	// 运维用来判断 cold path 是否被 cache 缓解（命中率应 > 50%）。
	mux.HandleFunc("/api/admin/logs/body-cache-stats", admin(h.handleBodyFetchCacheStats))
	mux.HandleFunc("/api/admin/data-lifecycle/stats", admin(h.handleDataLifecycleStats))
	mux.HandleFunc("/api/admin/data-lifecycle/cleanup/preview", admin(h.handleDataLifecycleCleanupPreview))
	mux.HandleFunc("/api/admin/data-lifecycle/metrics", admin(h.handleDataLifecycleMetrics))
	// Partition management endpoints (2026-06-28)
	mux.HandleFunc("/api/admin/data-lifecycle/partitions", admin(h.handleDataLifecyclePartitions))
	mux.HandleFunc("/api/admin/data-lifecycle/partitions/archive", h.superAdmin(h.handleDataLifecycleArchivePartition))
	mux.HandleFunc("/api/admin/data-lifecycle/partitions/archive-batch", h.superAdmin(h.handleDataLifecycleArchiveBatch))
	// Hot table to partition migration endpoints (2026-07-10)
	// 2026-07-13: /promote 统一走异步分发，保留 /promote-async 以便兼容
	mux.HandleFunc("/api/admin/data-lifecycle/hot/promote", h.superAdmin(h.handleDataLifecyclePromoteHotAsync))
	mux.HandleFunc("/api/admin/data-lifecycle/hot/promote-async", h.superAdmin(h.handleDataLifecyclePromoteHotAsync))
	mux.HandleFunc("/api/admin/data-lifecycle/hot/jobs", h.superAdmin(h.handleDataLifecycleHotJobs))
	mux.HandleFunc("/api/admin/data-lifecycle/hot/job/", h.superAdmin(h.handleDataLifecycleHotJob)) // /job/{id}
	mux.HandleFunc("/api/admin/data-lifecycle/hot/job", h.superAdmin(h.handleDataLifecycleHotJob))  // /job/{id}/cancel 等
	mux.HandleFunc("/api/admin/data-lifecycle/hot/cron/stats", h.superAdmin(h.handleDataLifecycleHotCronStats))
	// Drop partition — async dispatch (2026-07-13: both /drop and /drop-async dispatch to job registry)
	mux.HandleFunc("/api/admin/data-lifecycle/partitions/drop", h.superAdmin(h.handleDataLifecycleDropPartitionAsync))
	mux.HandleFunc("/api/admin/data-lifecycle/partitions/drop-async", h.superAdmin(h.handleDataLifecycleDropPartitionAsync))
	// Generic async job query endpoints (2026-07-13)
	mux.HandleFunc("/api/admin/data-lifecycle/jobs", admin(h.handleLifecycleJobs))
	mux.HandleFunc("/api/admin/data-lifecycle/jobs/", admin(h.handleLifecycleJobByID))
	// Storage overview endpoints (2026-07-01)
	mux.HandleFunc("/api/admin/data-lifecycle/storage", admin(h.handleDataLifecycleStorage))
	mux.HandleFunc("/api/admin/data-lifecycle/storage/tables", admin(h.handleDataLifecycleTableSizes))
	// Per-table maintenance (2026-07-08): VACUUM / VACUUM FULL / REINDEX
	// superAdmin 限定：操作有锁表风险，仅平台运维可执行
	mux.HandleFunc("/api/admin/data-lifecycle/storage/tables/vacuum", h.superAdmin(h.handleDataLifecycleTableVacuum))
	mux.HandleFunc("/api/admin/data-lifecycle/storage/tables/vacuum-full", h.superAdmin(h.handleDataLifecycleTableVacuumFull))
	mux.HandleFunc("/api/admin/data-lifecycle/storage/tables/reindex", h.superAdmin(h.handleDataLifecycleTableReindex))
	// Async per-table maintenance (2026-07-13): VACUUM / VACUUM FULL / REINDEX
	mux.HandleFunc("/api/admin/data-lifecycle/storage/tables/vacuum-async", h.superAdmin(h.handleDataLifecycleTableVacuum))
	mux.HandleFunc("/api/admin/data-lifecycle/storage/tables/vacuum-full-async", h.superAdmin(h.handleDataLifecycleTableVacuumFull))
	mux.HandleFunc("/api/admin/data-lifecycle/storage/tables/reindex-async", h.superAdmin(h.handleDataLifecycleTableReindex))
	// Blob management endpoints (2026-07-01)
	mux.HandleFunc("/api/admin/data-lifecycle/blobs/top", admin(h.handleDataLifecycleBlobTop))
	mux.HandleFunc("/api/admin/data-lifecycle/blobs/cleanup/preview", admin(h.handleDataLifecycleBlobCleanupPreview))
	mux.HandleFunc("/api/admin/data-lifecycle/blobs/cleanup/execute", h.superAdmin(h.handleDataLifecycleBlobCleanupExecute))
	// Attachment filesystem management endpoints (2026-07-01)
	mux.HandleFunc("/api/admin/attachments/filesystem/stats", admin(h.handleAttachmentFilesystemStats))
	mux.HandleFunc("/api/admin/attachments/filesystem/cleanup", h.superAdmin(h.handleAttachmentFilesystemCleanup))

	// 2026-07-02: 补齐附件 DB 元数据管理路由（方法已实现，路由此前缺失）
	mux.HandleFunc("/api/admin/attachments", admin(h.handleDataLifecycleAttachments))
	mux.HandleFunc("/api/admin/attachments/stats", admin(h.handleDataLifecycleAttachmentStats))
	mux.HandleFunc("/api/admin/attachments/policy", admin(h.handleDataLifecycleAttachmentPolicy))
	mux.HandleFunc("/api/admin/attachments/cleanup/preview", admin(h.handleDataLifecycleAttachmentCleanupPreview))
	mux.HandleFunc("/api/admin/attachments/cleanup/execute", h.superAdmin(h.handleDataLifecycleAttachmentCleanupExecute))
	mux.HandleFunc("/api/admin/attachments/", admin(h.handleDataLifecycleAttachmentItem))

	// 2026-07-03: live request stream SSE feed. Rides the cookie
	// JWT auth path (admin middleware) — no custom token shim, no
	// query-string bearer leakage. nil hub means the endpoint is
	// not registered.
	if h.liveStreamHub != nil {
		mux.HandleFunc("/api/admin/live-stream", admin(h.liveStreamHub.HandleLiveStream))
		mux.HandleFunc("/api/admin/live-stream/stats", admin(h.handleLiveStreamStats))
		// TEMPORARY DEBUG: snapshot_refresh validation (added 2026-07-26, TODO: remove when no longer needed)
		mux.HandleFunc("/api/admin/live-stream/trigger-snapshot", admin(h.liveStreamHub.HandleTriggerSnapshot))
	}

	// 2026-08-18: 会话优化 v4（T4）连接注册表只读投影 + 请求 action
	// 时间线 REST 回退（liveactions Redis LIST 有界扫描，(request_id,seq)
	// 去重）。仅元数据/计数，不暴露请求正文或凭据（§13.5 安全约束）。
	// Registry 未装配时 list/get 返回 503（SetConnectionRegistry 门控）。
	mux.HandleFunc("/api/admin/connection-registry", admin(h.handleConnectionRegistryList))
	mux.HandleFunc("/api/admin/connection-registry/{request_id}", admin(h.handleConnectionRegistryGet))
	mux.HandleFunc("/api/admin/requests/{id}/actions", admin(h.handleRequestActions))
	// 2026-08-23: 节点恢复时间线（node_probe_runs → SPA NodeRecoveryEvent）。
	mux.HandleFunc("/api/admin/node-health/{credential_id}/timeline", admin(h.handleNodeHealthTimeline))

	// 2026-07-13: read-only route-incident diagnosis API (Phase 1).
	// Super-admin only. nil handler means the diagnose entry is
	// hidden on the swim lane and the SSE incident_update envelope
	// is suppressed.
	if h.routeIncidentHandler != nil {
		h.routeIncidentHandler.RegisterRoutes(mux, h.superAdmin)
	}

	// 2026-08-15: 供应商成本对账端点（super_admin only，账单导入 + diff
	// 查询）。nil（对账开关关闭）时不注册。
	if h.providerCostReconciliationHandler != nil {
		h.providerCostReconciliationHandler.RegisterRoutes(mux, h.superAdmin)
	}

	// 2026-07-17: 请求链路追踪查看端点 (super_admin only, 仅子路径 /trace 与 /ai-prompt).
	if h.requestTraceHandler != nil {
		h.requestTraceHandler.RegisterRoutes(mux, h.superAdmin)
	}

	// 2026-07-05: 泳道初始化数据接口（从request_logs查询最近N小时的请求）
	mux.HandleFunc("/api/admin/dashboard/swim-lane-init", admin(h.HandleSwimLaneInit))

	// 2026-07-07: 首页会话统计概览接口
	mux.HandleFunc("/api/admin/dashboard/session-overview", admin(h.handleDashboardSessionOverviewAudited))

	// 2026-07-10: Dashboard API v2 - 6个新接口
	mux.HandleFunc("/api/admin/dashboard/session-trend", admin(h.handleDashboardSessionTrend))
	mux.HandleFunc("/api/admin/dashboard/session-health", admin(h.handleDashboardSessionHealth))
	mux.HandleFunc("/api/admin/dashboard/session-active", admin(h.handleDashboardSessionActive))
	mux.HandleFunc("/api/admin/dashboard/module-stats", admin(h.handleDashboardModuleStats))
	mux.HandleFunc("/api/admin/dashboard/errors", admin(h.handleDashboardErrors))
	mux.HandleFunc("/api/admin/dashboard/performance", admin(h.handleDashboardPerformance))
	mux.HandleFunc("/api/admin/dashboard/board", admin(h.handleDashboardBoard))
	mux.HandleFunc("/api/admin/stats", admin(h.handleStats))
	mux.HandleFunc("/api/admin/stats/", admin(h.handleStats))
	mux.HandleFunc("/api/admin/dashboard/operational", admin(h.handleDashboardOperational))
	mux.HandleFunc("/api/admin/dashboard/board/error-drill", admin(h.handleDashboardBoardErrorDrill))
	mux.HandleFunc("/api/admin/ops/overview", admin(h.handleOpsOverview))
	mux.HandleFunc("/api/admin/security/ip-blocklist", h.superAdmin(h.handleIPBlocklistCollection))
	mux.HandleFunc("/api/admin/security/ip-blocklist/reload", h.superAdmin(h.handleIPBlocklistReload))
	mux.HandleFunc("/api/admin/security/ip-blocklist/", h.superAdmin(h.handleIPBlocklistItem))

	// 2026-07-18: 敏感词管理端点 (reload / status / match)
	// 引擎未注入时隐藏这些端点（nil-safe）。
	if h.sensitiveWordEngine != nil {
		swH := NewSensitiveWordsHandler(h.sensitiveWordEngine)
		swH.SetPatternDetector(h.sanitizePatternDetector)
		swH.RegisterRoutes(mux, h.admin)
	}

	// 2026-07-07: P2会话分析 - 客户端/任务维度分析
	mux.HandleFunc("/api/admin/session-analytics/clients", admin(h.handleClientAnalyticsList))
	mux.HandleFunc("/api/admin/session-analytics/clients/", admin(h.handleClientAnalyticsDetail))
	mux.HandleFunc("/api/admin/session-analytics/tasks", admin(h.handleTaskAnalyticsList))
	mux.HandleFunc("/api/admin/session-analytics/tasks/", admin(h.handleTaskAnalyticsDetail))

	// 2026-07-07: P2会话分析 - 用户画像（三层隔离：super_admin/tenant_admin/user）
	mux.HandleFunc("/api/admin/session-analytics/users", admin(h.handleUserAnalyticsList))
	mux.HandleFunc("/api/admin/session-analytics/users/", admin(h.handleUserAnalyticsDetail))

	// 2026-07-02: 存储配置管理（附件目录/保留策略/水位/自动清理）
	mux.HandleFunc("/api/admin/storage/config", h.superAdmin(h.handleStorageConfig))
	mux.HandleFunc("/api/admin/storage/config/test-path", h.superAdmin(h.handleStorageTestPath))
	// 2026-07-02: 存储目录迁移进度查询（GET，供前端轮询迁移进度）
	mux.HandleFunc("/api/admin/storage/migration-state", admin(h.handleMigrationState))

	// 2026-07-02: 日志文件统一管理（轮转配置热加载/文件列表/归档/删除）
	mux.HandleFunc("/api/admin/logs/config", h.superAdmin(h.handleLogConfig))
	mux.HandleFunc("/api/admin/logs/files", admin(h.handleLogFiles))
	mux.HandleFunc("/api/admin/logs/stats", admin(h.handleLogStats))
	mux.HandleFunc("/api/admin/logs/archive", h.superAdmin(h.handleLogArchive))
	mux.HandleFunc("/api/admin/logs/cleanup", h.superAdmin(h.handleLogCleanup))
	mux.HandleFunc("/api/admin/logs/archive/list", admin(h.handleLogArchiveList))

	// settings-management (Q1: B, Q2: A, Q3: B): 4 platform + 4 tenant endpoints.
	// Tenant endpoints require super_admin (enforced inside the handler).
	h.registerSettingsRoutes(mux)

	// 2026-07-06: session state management endpoints.
	mux.HandleFunc("/api/admin/sessions", admin(h.handleListSessions))
	mux.HandleFunc("/api/admin/sessions/", admin(h.handleSessionSubrouter))

	// 2026-08-06: session management endpoints (project/task/tag dimensions)
	// These new endpoints complement the existing session list/detail APIs
	// with enhanced filtering by project_id, task_id, user_tags, and full-text search.
	mux.HandleFunc("/api/sessions/list", admin(h.HandleSessionManagementList))
	mux.HandleFunc("/api/sessions/detail/", admin(h.HandleSessionManagementDetail))
	mux.HandleFunc("/api/sessions/update/", admin(h.HandleSessionManagementUpdate))
	mux.HandleFunc("/api/sessions/task-flow/", admin(h.HandleTaskFlow))
	mux.HandleFunc("/api/sessions/project-costs/", admin(h.HandleProjectCosts))

	// 2026-07-10: 数据库降级和备份恢复端点
	if h.dbMonitor != nil {
		mux.HandleFunc("/api/admin/db-status", admin(h.handleDBStatus))
	}
	if h.fileReader != nil {
		mux.HandleFunc("/api/admin/backups", admin(h.handleListBackups))
		mux.HandleFunc("/api/admin/backups/{filename}", admin(h.handleGetBackupFile))
		mux.HandleFunc("/api/admin/backups/{filename}/validate", admin(h.handleValidateBackupFile))
	}
	if h.recovery != nil {
		mux.HandleFunc("/api/admin/backups/{filename}/recover", h.superAdmin(h.handleRecoverBackupFile))
		mux.HandleFunc("/api/admin/backups/recover-all", h.superAdmin(h.handleRecoverAllBackups))
		mux.HandleFunc("/api/admin/recovery-tasks/{task_id}", admin(h.handleGetRecoveryTask))
	}
	if h.degradation != nil {
		mux.HandleFunc("/api/admin/data-lifecycle/degradation/control", h.superAdmin(h.handleDegradationControl))
		mux.HandleFunc("/api/admin/data-lifecycle/degradation/status", admin(h.handleDegradationStatus))
		mux.HandleFunc("/api/admin/data-lifecycle/degradation/recover", h.superAdmin(h.handleDegradationRecovery))
		mux.HandleFunc("/api/admin/data-lifecycle/degradation/tasks/{task_id}", admin(h.handleDegradationRecoveryTask))
	}

	// Module management — enterprise feature module listing, toggling, and
	// integration configuration (Feishu bot, webhook, etc.).
	h.registerModuleRoutes(mux)

	// 2026-06-27 Session audit & approval queue endpoints (sessionaudit domain).
	// All require JWT/admin-key auth (h.admin wrap). Approve/Reject endpoints
	// additionally enforce tenant_id comparison in handler (cross-tenant 403).
	mux.HandleFunc("/api/admin/session-audit", admin(h.handleSessionAuditList))
	mux.HandleFunc("/api/admin/session-audit/", admin(h.handleSessionAuditGet))
	mux.HandleFunc("/api/admin/session-audit/stats", admin(h.handleSessionAuditStats))
	mux.HandleFunc("/api/admin/session-audit/export", admin(h.handleSessionAuditExport))
	mux.HandleFunc("/api/admin/session-approvals", admin(h.handleApprovalList))
	mux.HandleFunc("/api/admin/session-approvals/", admin(h.handleApprovalSubrouter))

	// 2026-07-09 Session Inspector stats endpoint (session_inspector module).
	// Platform-level aggregate stats: active/idle/closed counts, average health score, recycled today.
	mux.HandleFunc("/api/admin/sessions/inspector-stats", admin(h.HandleSessionInspectorStats))

	// B3 PR2 (2026-08-17): admin 节点操作审计独立查询面，替换
	// /api/admin/requests/{id}/transitions 中 audit 行查询部分
	// （transition_type='state' 的伪 request_id 行）。请求生命周期事件
	// 已迁入 requestjourney，由 /api/admin/request-journeys/ Detail 覆盖。
	mux.HandleFunc("/api/admin/audit/node-operations", admin(h.handleAuditNodeOperations))
	mux.HandleFunc("/api/admin/sessions/online", admin(h.handleSessionsOnline))
	mux.HandleFunc("/api/admin/sessions/{id}/timeline", admin(h.handleSessionTimeline))
	// 2026-08-15 V3.3-OBS (OBS-BE6): 会话轮次-子请求树（仅元数据，分页）。
	// 主请求按 ts 排序派生 turn_number，子请求经 parent_request_id 关联，
	// request_type 优先 510 迁移列、缺失回退 origin_actor（X-Gw-Source-Actor 落库）。
	mux.HandleFunc("/api/admin/sessions/{id}/turns", admin(h.handleSessionTurnsTree))

	// Public polling endpoint (no auth) — clients poll this to learn whether
	// their pending approval was approved/rejected/timeout. Cross-tenant
	// protection via optional X-Tenant-ID header (see handler docstring).
	mux.HandleFunc("/v1/approvals/", h.handleApprovalStatus)

	// Identity pool (Layer 0 cap) — admin can inspect live stats and
	// update the global max-identities setting. superAdmin-only because
	// raising the cap affects upstream rate-limit exposure.
	mux.HandleFunc("/api/admin/identity-pool/stats", h.superAdmin(h.getIdentityPoolStats))
	mux.HandleFunc("/api/admin/identity-pool/max", h.superAdmin(h.setIdentityPoolMax))
	mux.HandleFunc("/api/routing/model-tree", admin(h.handleRoutingModelTree))
	mux.HandleFunc("/api/routing/policy", h.superAdmin(h.handleRoutingPolicy))
	mux.HandleFunc("/api/routing/featured", h.superAdmin(h.handleRoutingFeatured))
	mux.HandleFunc("/api/routing/available-models", admin(h.handleRoutingAvailableModels))
	mux.HandleFunc("/api/routing/available-models/raw", admin(h.handleRoutingAvailableModelsRaw))
	mux.HandleFunc("/api/routing/decisions", admin(h.handleRoutingDecisions))
	mux.HandleFunc("/api/routing/health", admin(h.handleRoutingHealth))
	mux.HandleFunc("/api/routing/audit", h.superAdmin(h.handleRoutingAudit))
	mux.HandleFunc("/api/routing/probe", h.superAdmin(h.handleRoutingProbe))
	mux.HandleFunc("/api/routing/manual-priority", h.superAdmin(h.handleRoutingManualPriority))
	mux.HandleFunc("/api/routing/score-details", admin(h.handleRoutingScoreDetails))
	mux.HandleFunc("/api/routing/scoring-weights", h.superAdmin(h.handleRoutingScoringWeights))
	mux.HandleFunc("/api/routing/featured-models", admin(h.handleRoutingFeaturedModelsDynamic))
	mux.HandleFunc("/api/telemetry/decision-log", admin(h.handleTelemetryDecisionLog))
	mux.HandleFunc("/api/telemetry/request-log", admin(h.handleTelemetryRequestLog))
	mux.HandleFunc("/api/telemetry/batch", admin(h.handleTelemetryBatch))
	mux.HandleFunc("/api/providers", h.superAdmin(h.handleProvidersRoot))
	mux.HandleFunc("/api/providers/", h.superAdmin(h.handleProviders))
	mux.HandleFunc("/api/providers/seed-from-catalog", h.superAdmin(h.handleSeedFromCatalog))
	mux.HandleFunc("/api/providers/credentials/", h.superAdmin(h.handleForceRecover))

	// 2026-08-14 V3.2 (BE-A2): 节点操作 API — 同步测试 + 启用/禁用切换。
	// handleNodeTestNow / handleNodeToggle 内部已调 RequireSuperAdminForWrite，
	// 此处直接注册、不外加 super_admin wrapper（避免双层鉴权）。
	// 路径中的 {id} 由 Go 1.22+ ServeMux 解析，handler 用 r.PathValue("id")。
	mux.HandleFunc("/api/admin/providers/{id}/test-now", h.handleNodeTestNow)
	mux.HandleFunc("/api/admin/providers/{id}/enable", h.handleNodeToggle)
	mux.HandleFunc("/api/admin/credentials/{id}/session-ping", h.superAdmin(h.handleCredentialSessionPing))

	// 2026-06-23 Phase 2 (P1) + Phase 3 (P2): candidate_failure_logs query
	// endpoints + alert ring view from the CandidateFailureMonitor.
	// JWT-auth required (same as /api/routing/*).
	if h.cfHandlers == nil {
		h.cfHandlers = &candidateFailureHandlers{db: h.db}
	}
	mux.HandleFunc("/api/candidate-failures", admin(h.cfHandlers.listCandidateFailures))
	mux.HandleFunc("/api/candidate-failures/stats", admin(h.cfHandlers.getCandidateFailureStats))
	mux.HandleFunc("/api/candidate-failures/credential/{id}", admin(h.cfHandlers.getCandidateFailuresByCredential))
	mux.HandleFunc("/api/candidate-failures/alerts", admin(h.cfHandlers.listRecentAlerts))
	mux.HandleFunc("/api/keys", admin(h.handleKeysRoot))
	mux.HandleFunc("/api/keys/", admin(h.handleKeys))
	mux.HandleFunc("/api/key-applications", admin(h.handleKeyApplicationsList))
	mux.HandleFunc("/api/key-applications/", admin(h.handleKeyApplications))
	mux.HandleFunc("/api/models", admin(h.handleModelsRoot))
	mux.HandleFunc("/api/models/", admin(h.handleModels))
	mux.HandleFunc("/api/models/name-mapping", admin(h.handleModelNameMapping))
	mux.HandleFunc("/api/models/name-mapping/", admin(h.handleModelNameMapping))
	mux.HandleFunc("/api/client-configs/audit", admin(h.handleClientConfigAudit))
	mux.HandleFunc("/api/usage", admin(h.handleUsageSummary))
	mux.HandleFunc("/api/usage/", admin(h.handleUsage))
	// T1.4 用量成本增强端点 (2026-07-06)
	mux.HandleFunc("/api/admin/usage/", h.admin(h.HandleUsageAdmin))
	mux.HandleFunc("/api/logs", admin(h.handleLogsRoot))
	mux.HandleFunc("/api/logs/", admin(h.handleLogs))
	// 2026-08-09: 跨会话轮次列表端点（复用 session_turns 表）
	mux.HandleFunc("/api/admin/turns", admin(h.handleTurnsList))
	// 2026-08-10: 会话分组轮次列表端点（最外层会话 + 内层轮次，分层展示）
	mux.HandleFunc("/api/admin/turns/sessions", admin(h.handleTurnsSessions))
	// 2026-08-10: 轮次列表页各筛选维度的热门可选值（供前端 filterable 下拉）
	mux.HandleFunc("/api/admin/turns/sessions/filter-options", admin(h.handleTurnsFilterOptions))
	// 2026-07-01 (migration 325): attachment file download.
	// GET /api/attachments/{path...} streams an attachment file from the
	// configured storage dir. Admin-authenticated so attachments are not
	// publicly accessible. The handler is nil-safe (returns 503 when
	// attachment storage was not configured at startup).
	mux.HandleFunc("/api/attachments/", admin(h.handleAttachmentsDownload))
	mux.HandleFunc("/api/catalog", h.superAdmin(h.handleCatalogRoot))
	mux.HandleFunc("/api/catalog/", h.superAdmin(h.handleCatalog))
	mux.HandleFunc("/api/tags", h.superAdmin(h.handleTags))
	mux.HandleFunc("/api/system/background-tasks", h.admin(h.handleSystemTasks))
	// /api/system/version is registered as public route above
	mux.HandleFunc("/api/system/memora-status", h.admin(h.handleMemoraStatus))
	mux.HandleFunc("/api/system/memora-ping", h.admin(h.handleMemoraPing))
	mux.HandleFunc("/api/system/memora-sink", h.admin(h.handleMemoraSinkControl))
	mux.HandleFunc("/api/system/memora-query/", h.admin(h.handleMemoraQuery))
	mux.HandleFunc("/api/system/memora-sessions", h.admin(h.handleMemoraSessions))
	mux.HandleFunc("/api/system/memora-context/", h.admin(h.handleMemoraContext))
	mux.HandleFunc("/api/system/session-messages/", h.admin(h.handleSessionMessages))
	mux.HandleFunc("/api/system/session-context/", h.admin(h.handleSessionContextRoutes))
	mux.HandleFunc("/api/system/no-topic-session/", h.admin(h.handleNoTopicSessionRoutes))
	mux.HandleFunc("/api/admin/session-crosstalk", h.admin(h.handleSessionCrosstalkCheck))
	mux.HandleFunc("/api/tasks/", admin(h.handleTasks))
	mux.HandleFunc("/api/free-pool/status", h.admin(h.handleFreePoolStatus))
	mux.HandleFunc("/api/free-pool/register", h.superAdmin(h.handleFreePoolRegister))
	mux.HandleFunc("/api/free-pool/models", h.admin(h.handleFreePoolModels))
	mux.HandleFunc("/api/free-pool/catalog", h.admin(h.handleFreePoolCatalog))
	mux.HandleFunc("/api/free-pool/available", h.admin(h.handleFreePoolAvailable))
	mux.HandleFunc("/api/free-pool/register-all", h.superAdmin(h.handleFreePoolRegisterAll))
	mux.HandleFunc("/api/free-pool/import-env", h.superAdmin(h.handleFreePoolImportEnv))
	mux.HandleFunc("/api/free-pool/bootstrap", h.superAdmin(h.handleFreePoolBootstrap))
	mux.HandleFunc("/api/free-pool/bridge-oauth", h.superAdmin(h.handleFreePoolBridgeOAuth))
	mux.HandleFunc("/api/free-pool/discover", h.superAdmin(h.handleFreePoolDiscover))
	mux.HandleFunc("/api/free-pool/bulk-register", h.superAdmin(h.handleFreePoolBulkRegister))
	mux.HandleFunc("/api/free-pool/methods", h.admin(h.handleFreePoolMethods))
	mux.HandleFunc("/api/free-pool/discovery-status", h.admin(h.handleFreePoolDiscoveryStatus))
	mux.HandleFunc("/api/free-pool/signup-hub", h.admin(h.handleFreePoolSignupHub))
	mux.HandleFunc("/api/free-pool/temp-email", h.superAdmin(h.handleFreePoolTempEmail))
	mux.HandleFunc("/api/free-pool/temp-email/poll", h.superAdmin(h.handleFreePoolTempEmailPoll))
	mux.HandleFunc("/api/free-pool/probe", h.superAdmin(h.handleFreePoolProbe))
	mux.HandleFunc("/api/free-pool/quick-entry", h.superAdmin(h.handleFreePoolQuickEntry))
	mux.HandleFunc("/api/free-pool/keys", h.superAdmin(h.handleFreePoolKeysRouter))
	mux.HandleFunc("/api/free-pool/keys/", h.superAdmin(h.handleFreePoolKeysSubRouter))
	if h.freePoolSSE != nil {
		mux.HandleFunc("/api/free-pool/stream", h.admin(h.freePoolSSE.HandleStream))
	}
	mux.HandleFunc("/api/pricing/", admin(h.handlePricing))
	mux.HandleFunc("/api/config/default-limits", admin(h.handleDefaultLimits))

	// Track C C7 (2026-06-18): pending response admin API.
	// Three endpoints, all under the standard admin auth wrap.
	// The /stats path is a peer of the list endpoint, registered
	// BEFORE the catch-all /api/admin/pending-responses/ to
	// avoid the slash-suffix path swallowing the literal "stats".
	mux.HandleFunc("/api/admin/pending-responses/stats", admin(h.handlePendingStats))
	mux.HandleFunc("/api/admin/pending-responses", admin(h.handlePendingList))
	mux.HandleFunc("/api/admin/pending-responses/", admin(h.handlePendingSubrouter))

	// Peak concurrency + auto-tune endpoints (Phase 2).
	if h.db != nil {
		peakH := NewPeakHandlers(h.db)
		peakH.RegisterPeakRoutes(mux, h.superAdmin)

		// v2.0 auto-route admin endpoints (Phase 3).
		autoH := NewAutoRouteHandlers(h.db)
		if h.autoIndexRefresher != nil {
			autoH.SetIndexRefresher(h.autoIndexRefresher)
		}
		if h.feedbackAnalyzer != nil {
			autoH.SetFeedbackAnalyzer(h.feedbackAnalyzer)
		}
		autoH.RegisterAutoRouteRoutes(mux, h.superAdmin)

		// Phase 2a analytics (matrix / flow / model-task-index / decision-replay).
		// superAdmin only: these expose cross-tenant credential/model routing
		// internals and auto-route tuner metrics; tenant_admin must not see them.
		analyticsH := NewAnalyticsHandlers(h.db)
		analyticsH.RegisterAnalyticsRoutes(mux, h.superAdmin)

		// Phase 1 work type config CRUD.
		wtH := NewWorkTypeHandlers(h.db)
		wtH.RegisterWorkTypeRoutes(mux, h.superAdmin)

		// 2026-08-20: 项目归属 admin 端点（POST sync-from-acc / GET list）。
		// 与 work_type 一样的 superAdmin 保护：同步动作会写 project_dim，
		// 列表会泄漏租户项目清单。
		pH := NewProjectHandlers(h.db)
		pH.RegisterProjectAdminRoutes(mux, h.superAdmin)

		// Credential monitor (2026-06-22): sliding window + manual promote/demote.
		// Redis is optional: monitor-summary works from DB alone; sliding window
		// degrades to request_logs fallback when Redis is unavailable.
		// 2026-07-04: 改用 h.admin 中间件，允许 tenant_admin 访问凭据监控页面
		var rc *redis.Client
		if h.redisClient != nil {
			if r, ok := h.redisClient.(*redis.Client); ok {
				rc = r
			}
		}
		monitorH := NewCredentialMonitorHandlers(h, nil, rc)
		monitorH.RegisterMonitorRoutes(mux, h.admin)

		// Credential state management (2026-06-30): manual probe + live state query.
		// Routes are guarded by superAdmin (same as monitor routes).
		h.registerStateRoutes(mux)

		// 2026-07-12: 应急诊断接口 — 连续异常时管理员手动恢复
		mux.HandleFunc("/api/admin/diagnostics/credential", h.superAdmin(h.handleCredentialDiagnostic))
		mux.HandleFunc("/api/admin/diagnostics/credential/force-recover", h.superAdmin(h.handleForceRecoverSingle))

		// 2026-07-24: 供应商级路由阻塞诊断 — "凭据正常但路由不到"场景
		mux.HandleFunc("/api/admin/diagnostics/routing-blocked", h.superAdmin(h.handleRoutingBlockedDiagnostic))
		mux.HandleFunc("/api/admin/diagnostics/routing-blocked/fix", h.superAdmin(h.handleRoutingBlockedFix))
		mux.HandleFunc("/api/admin/diagnostics/model-routing", h.superAdmin(h.handleModelRoutingDiagnostic))

		// 2026-08-11: 模型智商（Model IQ）— 标准智商 / 节点智商历史 / 立即测试。
		// catalog/node-latest/history 只读 DB；trigger 调用 modelQualityBackend。
		mux.HandleFunc("/api/admin/model-iq/catalog", h.superAdmin(h.handleModelIQCatalog))
		mux.HandleFunc("/api/admin/model-iq/node-latest", h.superAdmin(h.handleModelIQNodeLatest))
		mux.HandleFunc("/api/admin/model-iq/history", h.superAdmin(h.handleModelIQHistory))
		mux.HandleFunc("/api/admin/model-iq/trigger", h.superAdmin(h.handleModelIQTrigger))

		h.registerMaasRoutes(mux)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("json marshal failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		//nolint:errcheck // HTTP write error non-recoverable
		w.Write([]byte(`{"error":{"detail":"json marshal failed"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//nolint:errcheck // HTTP write error non-recoverable
	w.Write(data)
	//nolint:errcheck // HTTP write error non-recoverable
	w.Write([]byte("\n"))
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"detail": msg},
	})
}

// writeErrorWithCode 返回带 error_code 的错误响应（V3.2 契约）。
func writeErrorWithCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":   code,
			"detail": msg,
		},
	})
}

func readJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	//nolint:errcheck // best-effort close
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func queryInt(r *http.Request, key string, def int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func queryBool(r *http.Request, key string) bool {
	s := r.URL.Query().Get(key)
	return strings.EqualFold(s, "true") || s == "1"
}

func queryString(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}

func queryOptionalBool(r *http.Request, key string) *bool {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil
	}
	v := strings.EqualFold(raw, "true") || raw == "1"
	return &v
}

// handleDefaultLimits handles GET and PUT /api/config/default-limits
// It stores the default limits in the database (app_settings table).
func (h *Handler) handleDefaultLimits(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getDefaultLimits(w, r)
	case http.MethodPut:
		if !IsSuperAdminOrLegacy(r) {
			writeError(w, http.StatusForbidden, "super_admin role required to modify default limits")
			return
		}
		h.setDefaultLimits(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) getDefaultLimits(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var rateLimitRPM, rateLimitConcurrent *int
	var rateLimitTPM *int

	err := h.db.QueryRow(ctx, `
		SELECT rate_limit_rpm, rate_limit_concurrent, rate_limit_tpm
		FROM app_settings
		WHERE tenant_id = 'default' AND app_id = 'gateway'
	`).Scan(&rateLimitRPM, &rateLimitConcurrent, &rateLimitTPM)

	if err != nil {
		// Return hardcoded defaults if not found
		writeJSON(w, http.StatusOK, map[string]any{
			"rate_limit_rpm":        60,
			"rate_limit_concurrent": 20,
			"rate_limit_tpm":        nil,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"rate_limit_rpm":        rateLimitRPM,
		"rate_limit_concurrent": rateLimitConcurrent,
		"rate_limit_tpm":        rateLimitTPM,
	})
}

func (h *Handler) setDefaultLimits(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RateLimitRPM        *int `json:"rate_limit_rpm"`
		RateLimitConcurrent *int `json:"rate_limit_concurrent"`
		RateLimitTPM        *int `json:"rate_limit_tpm"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err := h.db.Exec(ctx, `
		INSERT INTO app_settings (tenant_id, app_id, rate_limit_rpm, rate_limit_concurrent, rate_limit_tpm, updated_at)
		VALUES ('default', 'gateway', $1, $2, $3, now())
		ON CONFLICT (tenant_id, app_id) DO UPDATE SET
			rate_limit_rpm = EXCLUDED.rate_limit_rpm,
			rate_limit_concurrent = EXCLUDED.rate_limit_concurrent,
			rate_limit_tpm = EXCLUDED.rate_limit_tpm,
			updated_at = now()
	`, req.RateLimitRPM, req.RateLimitConcurrent, req.RateLimitTPM)

	if err != nil {
		slog.Error("setDefaultLimits failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save default limits")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":                "ok",
		"rate_limit_rpm":        req.RateLimitRPM,
		"rate_limit_concurrent": req.RateLimitConcurrent,
		"rate_limit_tpm":        req.RateLimitTPM,
	})
}

// SetCandidateFailureHandlers (2026-06-23 Phase 3) wires the alert ring
// getter onto the handler. Called by main.go right after starting the
// CandidateFailureMonitor so the /api/candidate-failures/alerts endpoint
// returns live data.
func (h *Handler) SetCandidateFailureHandlers(getter func() []bg.CandidateFailureAlert) {
	if h.cfHandlers == nil {
		h.cfHandlers = &candidateFailureHandlers{db: h.db}
	}
	h.cfHandlers.SetRecentAlerts(getter)
}

// SystemMonitorBackend 是 admin 与 bg/systemmonitor 的对接类型。
//
// 设计依据 (docs/会话优化v2/32 §6.2): handler.go 不直接 import
// bg/systemmonitor，用结构投影 + 适配器（systemMonitorAdapter 在
// cmd/gateway/system_monitor_adapter.go 中实现）隔离依赖。
//
// main.go 在 wire 时构造 *systemMonitorAdapter 传入 SetSystemMonitor。
type SystemMonitorBackend interface {
	Submit(ctx context.Context, task *SystemMonitorTask) (int64, error)
	QueueStats(ctx context.Context) (SystemMonitorQueueStats, error)
	IsFallback() bool
	GetMetricsCollector() interface{} // Returns *systemmonitor.MetricsCollector, but we use interface{} to avoid import cycle
	// RecoveryStats returns the URSM v2 recovery gate snapshot:
	// last error (with timestamp) and last successful reopen (with
	// timestamp + observed key count). Safe on a nil receiver; returns
	// the zero value when no operation has happened yet. Added in
	// audit follow-up #4 to expose the recovery gate observability
	// surface to admin endpoints and Prometheus exporters.
	RecoveryStats() SystemMonitorRecoveryStats
}

// SystemMonitorTask 是 systemmonitor.Task 的最小投影（字段全名相同），
// 让 admin 包不 import systemmonitor。
//
// KEEP: 系统监测 backend 接口 —— 直到 bg/systemmonitor/types.go 拆为独立
// 子包 sysevent 后再考虑去掉 [@monitoring] [review 2026-Q4]
type SystemMonitorTask struct {
	ID           int64
	TaskType     string
	Automaticity string
	Source       string
	CredentialID int64
	ProviderID   int64
	RawModel     string
	ScheduledAt  time.Time
	NextRunAt    time.Time
	MaxAttempts  int
}

// SystemMonitorQueueStats 是 admin 自己定义的快照类型；字段与
// bg/systemmonitor.QueueStats 一致。
type SystemMonitorQueueStats struct {
	QueueSize   int64
	RunningSize int64
	InFallback  bool
}

// SystemMonitorRecoveryStats is the admin-side projection of
// bg/systemmonitor.RecoveryStats. Mirrors recovery.Stats with JSON
// tags so it can be returned directly from the admin endpoint.
type SystemMonitorRecoveryStats struct {
	// LastError is the most recent operation failure (empty on success).
	LastError string `json:"last_error,omitempty"`
	// LastErrorAt is the timestamp of LastError (zero on success).
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
	// LastRecoveryAt is the timestamp of the most recent successful
	// gate reopen (zero when no recovery has happened yet).
	LastRecoveryAt time.Time `json:"last_recovery_at,omitempty"`
	// LastRecoveryKeyCount is the key count observed on the most recent
	// successful reopen.
	LastRecoveryKeyCount int `json:"last_recovery_key_count,omitempty"`
	// ConsecutiveFailures is the monitor's own counter — included
	// here so admin dashboards can render "consecutive failures" +
	// "last error" + "last recovery" in a single round-trip.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// FailThreshold is the configured consecutive-failures threshold
	// before auto-close fires. Useful for "X / Y failures" displays.
	FailThreshold int `json:"fail_threshold"`
}

// ErrSystemMonitorDisabled 由 SetSystemMonitor(nil) 后调用 Submit 触发。
var ErrSystemMonitorDisabled = errors.New("system monitor disabled")

// SetSystemMonitor wires the system-monitoring module so the
// /api/admin/system-monitor/* endpoints can submit tasks and read
// queue state. Safe to call with nil to disable.
func (h *Handler) SetSystemMonitor(sm SystemMonitorBackend) {
	h.systemMonitor = sm
}

// SetSystemMonitorSSE wires the dashboard SSE hub for the system-monitor
// stream. Pass nil to disable.
func (h *Handler) SetSystemMonitorSSE(hub *SystemMonitorSSEHub) {
	h.systemMonitorSSE = hub
}

// SetProbeStreamSSE wires the dashboard SSE hub for the self-check / node-probe
// queue stream (自检 tab). Pass nil to disable.
func (h *Handler) SetProbeStreamSSE(hub *ProbeSSEHub) {
	h.probeStreamHub = hub
}

// SetProbeQueue wires the unified self-check queue (需求 6). Enables the public
// POST/DELETE /api/admin/probe/tasks API so external callers can add/remove
// probe tasks without knowing internal details. Pass nil to disable.
func (h *Handler) SetProbeQueue(q *bg.ProbeQueue) {
	h.probeQueue = q
}

// SetFreePoolSSE wires the dashboard SSE hub for free-pool credential quota
// events (免费资源 tab). Pass nil to disable (the page falls back to polling).
func (h *Handler) SetFreePoolSSE(hub *FreePoolSSEHub) {
	h.freePoolSSE = hub
}
