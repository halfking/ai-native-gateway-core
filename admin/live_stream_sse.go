// Package admin — live request stream SSE hub.
//
//	GET /api/admin/live-stream  →  text/event-stream
//
// The hub fans out newly-persisted request_logs rows to every
// connected dashboard client. It also sends an initial replay of the
// most recent N requests when a client connects, so a freshly opened
// dashboard fills from left-to-right immediately rather than waiting
// for the next request to land.
//
// Why SSE and not WebSocket (a deliberate v2→current migration
// simplification):
//
//  1. SSE rides on standard HTTP — the cookie-based JWT auth path
//     (rule 20 §6.1, HttpOnly "llmgw_session" cookie) just works.
//     WebSocket upgrade cannot set Authorization and the cookie is
//     HttpOnly so the browser JS cannot read it; v2 had to smuggle
//     the token via ?token=… which is fragile (bearer leaks into
//     proxy access logs, server access logs, browser history).
//  2. SSE is browser-native (EventSource) with built-in auto-reconnect
//     + Last-Event-ID resume; we get resilience for free.
//  3. The dashboard is read-only — no need for the bidirectional
//     channel that WebSocket provides.
//
// Backpressure: the broadcast channel is bounded; if the hub can not
// enqueue, the message is dropped (the dashboard will catch up on
// reconnect via initial replay). Per-client writes are guarded by a
// mutex so a slow client can not stall the broadcast loop.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/redis/go-redis/v9"
)

// LiveStreamEnvelope wraps every SSE payload. The Type field
// lets the frontend switch on the variant without sniffing the shape.
//
//	"initial_data" — first message after connect; carries []LiveRequest + full snapshot
//	"request"      — a new request completed (or transitioned to in-progress); carries delta
//	"idle_marker"  — 1 minute of silence; carries delta
//	"ping"         — keepalive; the EventSource ignores these
//
// Delta is the tenant-scoped delta (for tenant-admin clients). superDelta
// is the global/super-admin-scoped delta (for super-admin clients). The
// hub's shouldDeliver picks the right one per client so a super-admin's
// lane view is never overwritten by an individual tenant's snapshot.
type LiveStreamEnvelope struct {
	Type      string              `json:"type"`
	Timestamp time.Time           `json:"ts"`
	Request   *LiveRequest        `json:"request,omitempty"`
	Requests  []LiveRequest       `json:"requests,omitempty"`
	Snapshot  *LiveStreamSnapshot `json:"snapshot,omitempty"`
	Delta     *LiveStreamDelta    `json:"delta,omitempty"`
	Health    *LiveStreamHealth   `json:"health,omitempty"`
	// Incident (2026-07-13) carries a route incident update for the
	// dashboard's diagnose entry. Type="incident_update" identifies
	// this variant on the wire. The body is sanitized — no tenant
	// id, no credential value, no full request body.
	Incident   *LiveIncidentUpdate `json:"incident,omitempty"`
	superDelta *LiveStreamDelta    `json:"-"` // attached to Delta during fanOut for super clients; not serialised directly

	// LaneIDs carries the list of known lane IDs for idle_marker envelopes.
	// The frontend uses this to decide which lanes need idle tiles.
	LaneIDs []string `json:"lane_ids,omitempty"`
}

// LiveIncidentUpdate is the wire shape of a route incident update
// (see domains/routeincident.IncidentUpdate). It is duplicated here
// to keep admin's import surface clean and to keep the SSE contract
// decoupled from the domain types. The body is identical by
// convention; tests assert the field set.
type LiveIncidentUpdate struct {
	Type           string              `json:"type"`
	TenantID       string              `json:"-"`
	IncidentID     string              `json:"incident_id"`
	State          string              `json:"state"`
	FailureStreak  int                 `json:"failure_streak"`
	RecoveryStreak int                 `json:"recovery_streak"`
	Visible        bool                `json:"visible"`
	AffectedLanes  []AffectedLane      `json:"affected_lanes,omitempty"`
	LastError      *LiveSanitizedError `json:"last_error,omitempty"`
	RouteKey       LiveRouteKey        `json:"route_key"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

// AffectedLane matches domains/routeincident.AffectedLane.
type AffectedLane struct {
	Dimension string `json:"dimension"`
	Value     string `json:"value"`
}

// LiveSanitizedError matches domains/routeincident.SanitizedError.
type LiveSanitizedError struct {
	Kind  string `json:"kind"`
	Stage string `json:"stage,omitempty"`
}

// LiveRouteKey is the sanitized projection of a route key. The
// tenant id is intentionally NOT serialised so the dashboard
// cannot accidentally surface a cross-tenant incident on the wrong
// user's screen.
type LiveRouteKey struct {
	Protocol     string `json:"endpoint_protocol"`
	Model        string `json:"model"`
	ProviderID   *int64 `json:"provider_id,omitempty"`
	CredentialID *int64 `json:"credential_id,omitempty"`
}

// LiveStreamHealth reports backend resource health for the dashboard.
// Sent as part of periodic keepalive / health_update envelopes so the
// frontend can warn operators when Redis (or other critical infra) is
// degraded.
type LiveStreamHealth struct {
	RedisConnected bool   `json:"redis_connected"`
	RedisError     string `json:"redis_error,omitempty"`
}

// providerCodeForSQL resolves providers.id → display name with a sensible fallback chain.
//
// IMPORTANT (2026-07-07): must reference providers.display_name (not providers.name).
// providers.name does not exist in sql/schema/01-schema.sql — pgx returns
// ERROR: column "name" does not exist (SQLSTATE 42703), which the caller
// silently treats as an empty result and the live stream dashboard then
// renders as "未知" / "未知供应商". The static regression test in
// live_stream_sse_test.go prevents accidental reverts of this column.
const providerCodeForSQL = `
		SELECT COALESCE(NULLIF(display_name, ''), NULLIF(catalog_code, ''), NULLIF(code, ''), '')
		FROM providers
		WHERE id = $1
	`

// providerCodeForCredentialSQL resolves credentials.id → provider display
// name via credentials → providers JOIN.
//
// IMPORTANT (2026-07-07): same constraint as providerCodeForSQL — must
// reference p.display_name, not p.name. See providerCodeForSQL doc.
const providerCodeForCredentialSQL = `
		SELECT COALESCE(NULLIF(p.display_name, ''), NULLIF(p.catalog_code, ''), NULLIF(p.code, ''), '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
	`

// providerCodeForSQLBody / providerCodeForCredentialSQLBody are plain
// copies used only by regression tests; the live code path uses the
// unindented constants above. The test asserts the SQL still references
// display_name so a future careless rename can't silently regress.
var (
	providerCodeForSQLBody           = providerCodeForSQL
	providerCodeForCredentialSQLBody = providerCodeForCredentialSQL
)

// LiveRequest is the minimal projection the dashboard swim lane needs.
// Fields are nullable to match the database shape — clients render "—"
// when a value is missing.
type LiveRequest struct {
	Type      string `json:"type,omitempty"` // "request" | "idle_marker"
	RequestID string `json:"request_id"`
	Ts        string `json:"ts"`
	TenantID  string `json:"tenant_id"`
	// GwSessionID is the LLM-Gateway session id (request_logs.gw_session_id)
	// if the request was sent through a session. Empty when the request
	// is one-off (e.g. a /v1/chat/completions call without a session).
	GwSessionID      string   `json:"gw_session_id,omitempty"`
	Model            string   `json:"model"`          // Standard model name (canonical preferred; 2026-07-16: outbound-only as last-resort fallback so tile matches the dimension key)
	CanonicalName    string   `json:"canonical_name"` // Standard model name for aggregation (canonical_name in DB)
	ModelCategory    string   `json:"model_category"`
	ProviderCode     string   `json:"provider_code"`
	Status           string   `json:"status"`
	LatencyMs        *int     `json:"latency_ms,omitempty"`
	PromptTokens     *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int     `json:"completion_tokens,omitempty"`
	TotalTokens      *int     `json:"total_tokens,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
	ErrorKind        *string  `json:"error_kind,omitempty"`
	FailureStage     *string  `json:"failure_stage,omitempty"` // "gateway" | "upstream" — failure origin

	// 2026-07-13: 主动探测标记字段
	// IsProbe 由 entry.TaskType=='probe_triggered' 推断。
	// ProbeOrigin = "direct" / "gateway" / "scheduled"，目前仅 "direct"。
	// ProbeAttempt 是当前轮次 (1-5)。
	IsProbe        bool   `json:"is_probe,omitempty"`
	ProbeOrigin    string `json:"probe_origin,omitempty"`
	ProbeAttempt   int    `json:"probe_attempt,omitempty"`
	ClientProfile  string `json:"client_profile,omitempty"`
	IdentityHash   string `json:"identity_hash,omitempty"`
	CreditsCharged *int   `json:"credits_charged,omitempty"`
}

// LiveStreamConfig controls hub behaviour. Zero values are safe and
// resolve to defaults in NewLiveStreamHub.
type LiveStreamConfig struct {
	BroadcastQueueSize            int
	InitialReplayLimit            int
	IdleThreshold                 time.Duration
	IdleTickInterval              time.Duration
	KeepaliveInterval             time.Duration
	RedisClient                   *redis.Client // optional: enables 1-hour Redis cache
	CachedSnapshotTTL             time.Duration // 淘汰阈值；零值 → 4h。可通过 LLM_GATEWAY_LIVE_STREAM_CACHED_TTL 覆盖
	CachedSnapshotCleanupInterval time.Duration // evict ticker 周期；零值 → 与 TTL 一致。可通过 LLM_GATEWAY_LIVE_STREAM_CACHED_CLEANUP_INTERVAL 覆盖
	SnapshotRefreshInterval       time.Duration // 全量快照推送间隔；零值 → 30min。可通过 LLM_GATEWAY_LIVE_STREAM_SNAPSHOT_REFRESH_INTERVAL 覆盖
}

func (c *LiveStreamConfig) defaults() {
	if c.BroadcastQueueSize <= 0 {
		c.BroadcastQueueSize = 1024
	}
	if c.InitialReplayLimit <= 0 {
		c.InitialReplayLimit = liveStreamReplayLimit
	}
	if c.IdleThreshold <= 0 {
		c.IdleThreshold = LiveStreamIdleThreshold
	}
	if c.IdleTickInterval <= 0 {
		c.IdleTickInterval = 5 * time.Minute
	}
	if c.KeepaliveInterval <= 0 {
		c.KeepaliveInterval = 25 * time.Second
	}
	if c.CachedSnapshotTTL <= 0 {
		c.CachedSnapshotTTL = LiveStreamLaneRetention
	}
	if c.CachedSnapshotCleanupInterval <= 0 {
		c.CachedSnapshotCleanupInterval = c.CachedSnapshotTTL
	}
	if c.SnapshotRefreshInterval <= 0 {
		// 2026-07-27: Reconciled with the field-doc default and the production
		// caller. cmd/gateway/main.go passes 30m (or LLM_GATEWAY_LIVE_STREAM_
		// SNAPSHOT_REFRESH_INTERVAL) so this branch is a last-resort fallback
		// for direct construction without main.go. Previously this returned 2h,
		// contradicting both the field comment and the effective production
		// value — a stale value left over from an interim 2026-07-19 change.
		// 30m matches what actually runs and is what the frontend's snapshot
		// guard (now '<' instead of '<=') expects for periodic reconciliation.
		c.SnapshotRefreshInterval = 30 * time.Minute
	}
}

// liveStreamClient is the per-connection state held by the hub. The
// write mutex serialises writes because http.Flusher is not
// concurrent-safe on the same ResponseWriter under the net/http stdlib.
type liveStreamClient struct {
	fl       http.Flusher
	w        http.ResponseWriter
	tenantID string
	isSuper  bool
	writeMu  sync.Mutex
	closed   bool
}

// LiveStreamSSEHub is the singleton fan-out point. Create one at
// startup, call Run() in a goroutine, and call HandleLiveStream on
// the admin mux. Producers call Publish() to fan a new request out.
type LiveStreamSSEHub struct {
	db    *pgxpool.Pool
	cfg   LiveStreamConfig
	store *LiveStreamRedisStore

	register   chan *liveStreamClient
	unregister chan *liveStreamClient
	broadcast  chan LiveRequest

	mu      sync.RWMutex
	clients map[*liveStreamClient]struct{}

	lastActivityMu sync.RWMutex
	lastActivity   time.Time

	// providerCache is a sync.Map of provider_id → catalog_code.
	// Populated lazily on first miss by ProviderCodeFor() so the
	// telemetry hot path can resolve a provider_id cheaply.
	providerCache sync.Map

	// modelFamilyCache is a sync.Map of model → vendor.
	// Populated lazily by ModelVendorFor() to resolve model vendor from
	// models_canonical.family → model_families.vendor.
	modelFamilyCache sync.Map

	// canonicalCache is a sync.Map of canonical_id → canonical_name.
	// Populated lazily by CanonicalNameFor() so live stream can display
	// model names by their standard/canonical identity rather than the
	// credential-level outbound name, enabling proper aggregation in the
	// model dimension view (same canonical model from different credentials
	// should appear in one lane, not scattered across multiple lanes).
	canonicalCache sync.Map

	stopCh chan struct{}

	// cachedSnapshot holds the last-known snapshot per tenant so broadcast
	// can compute a delta instead of sending the full snapshot every time.
	// Each entry carries a lastAccessed timestamp for periodic cleanup of
	// stale tenants (prevents unbounded memory growth).
	cachedSnapshotMu sync.RWMutex
	cachedSnapshot   map[string]*cachedSnapshotEntry // key = tenantID
	// 淘汰阈值统一使用 cfg.CachedSnapshotTTL，不再单独维护字段。

	// Metrics (added 2026-07-03 for monitoring)
	totalConnections              int64 // 累计连接数
	totalDisconnections           int64 // 累计断开数
	authFailures                  int64 // 认证失败次数
	broadcastCount                int64 // 广播消息数
	broadcastDrops                int64 // 广播队列满丢弃数
	cachedSnapshotBaselinePresent int64 // computeScopeDelta 时已有 delta baseline
	cachedSnapshotBaselineAbsent  int64 // computeScopeDelta 时尚无 delta baseline
	cachedSnapshotEmptySkips      int64 // 读出空 snapshot 触发早返的次数
	cachedSnapshotEvictions       int64 // evictStaleCachedSnapshots 累计清掉的 entry 数

	// lastHealth tracks the previous Redis health state so we only
	// broadcast a health_update envelope when the state changes.
	lastHealthMu sync.RWMutex
	lastHealth   *LiveStreamHealth

	// 2026-07-13: route-incident updates ride their own channel
	// so a burst of incidents cannot starve the per-request feed.
	// Initialised lazily on the first PublishIncidentUpdate call so
	// deployments that never wire the diagnose feature pay nothing.
	incidentMu       sync.Mutex
	incidentUpdateCh chan LiveStreamEnvelope
	incidentDrops    atomic.Int64
}

type cachedSnapshotEntry struct {
	snapshot     *LiveStreamSnapshot
	lastAccessed time.Time
}

// NewLiveStreamSSEHub constructs a hub. The caller MUST call Run()
// once in its own goroutine before the hub accepts traffic.
func NewLiveStreamSSEHub(db *pgxpool.Pool, cfg LiveStreamConfig) *LiveStreamSSEHub {
	cfg.defaults()
	return &LiveStreamSSEHub{
		db:             db,
		cfg:            cfg,
		store:          NewLiveStreamRedisStore(cfg.RedisClient),
		register:       make(chan *liveStreamClient, 16),
		unregister:     make(chan *liveStreamClient, 16),
		broadcast:      make(chan LiveRequest, cfg.BroadcastQueueSize),
		clients:        make(map[*liveStreamClient]struct{}),
		lastActivity:   time.Now(),
		stopCh:         make(chan struct{}),
		cachedSnapshot: make(map[string]*cachedSnapshotEntry),
	}
}

// Run drives the hub event loop. Blocks until Stop() is called.
func (h *LiveStreamSSEHub) Run() {
	if h.store != nil && h.cfg.RedisClient != nil {
		go h.runRedisSubscriber()
	}

	idleTicker := time.NewTicker(h.cfg.IdleTickInterval)
	keepaliveTicker := time.NewTicker(h.cfg.KeepaliveInterval)
	// evict ticker 周期默认跟随 CachedSnapshotTTL，但可被独立放宽——
	// 拆开避免 TTL > TTL 时 ticker 也被放大导致过期 entry 滞留。
	cacheCleanupTicker := time.NewTicker(h.cfg.CachedSnapshotCleanupInterval)
	healthTicker := time.NewTicker(30 * time.Second) // Redis health check interval
	snapshotRefreshTicker := time.NewTicker(h.cfg.SnapshotRefreshInterval)
	defer idleTicker.Stop()
	defer keepaliveTicker.Stop()
	defer cacheCleanupTicker.Stop()
	defer healthTicker.Stop()
	defer snapshotRefreshTicker.Stop()

	// Emit initial health status immediately so freshly-connected
	// clients do not have to wait 30s to learn about Redis state.
	h.checkAndBroadcastHealth()

	for {
		select {
		case <-h.stopCh:
			h.closeAll()
			return
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
			atomic.AddInt64(&h.totalConnections, 1)
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
			}
			h.mu.Unlock()
			atomic.AddInt64(&h.totalDisconnections, 1)
		case req := <-h.broadcast:
			h.lastActivityMu.Lock()
			h.lastActivity = time.Now()
			h.lastActivityMu.Unlock()
			atomic.AddInt64(&h.broadcastCount, 1)
			// Compute BOTH scopes so super-admin and tenant-admin clients each
			// receive a delta consistent with their own view. Previously a
			// single tenant-scoped snapshot was fanned out to everyone, which
			// caused super-admin lanes to flicker/disappear as different
			// tenants' requests alternately overwrote the shared cache.
			// The super-scope delta is only computed when at least one
			// super-admin client is connected (avoids 2x Redis reads when no
			// super admin is watching).
			tenantID := normalizeLiveStreamTenant(req.TenantID)
			hasSuperClient := h.hasSuperClient()
			var tenantDelta, superDelta *LiveStreamDelta
			if h.store != nil {
				// 2026-07-23: 修复实时流只显示 1 个泳道的 bug。
				// 原来用 200ms 超时，但 SnapshotFromDimensionQueues 内部需要
				// SCAN 多次迭代 + 27+ 个 pipeline 读，200ms 不够，导致
				// SCAN 第一次返回后 ctx 已超时，所有后续 SCAN/Pipeline 失败。
				// 改为 2 秒，覆盖 SCAN 全量（27+ 维度队列）+ 详情批量读取。
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				tenantDelta = h.computeScopeDelta(ctx, tenantID, false)
				if hasSuperClient {
					superDelta = h.computeScopeDelta(ctx, "", true)
				}
				cancel()

				// 2026-07-21: 详细日志记录每次推送的delta内容
				if tenantDelta != nil {
					h.logDeltaDetails("tenant", tenantID, req.RequestID, tenantDelta)
				}
				if superDelta != nil {
					h.logDeltaDetails("super", "", req.RequestID, superDelta)
				}
				// 2026-07-21: 当 delta 为 nil 但 store 可用时记录 warning，
				// 帮助诊断"流不更新"类问题（空 snapshot / Redis 队列过期等）。
				if tenantDelta == nil || (hasSuperClient && superDelta == nil) {
					slog.Warn("live stream: scope delta nil, request may not appear in swim lanes",
						"request_id", req.RequestID,
						"tenant_id", tenantID,
						"has_super_client", hasSuperClient,
						"empty_skips", atomic.LoadInt64(&h.cachedSnapshotEmptySkips))
				}
			}
			h.fanOut(LiveStreamEnvelope{
				Type:       "request",
				Timestamp:  time.Now().UTC(),
				Request:    &req,
				Delta:      tenantDelta,
				superDelta: superDelta,
			})
		case <-idleTicker.C:
			h.maybeEmitIdleMarker()
		case <-keepaliveTicker.C:
			h.fanOutKeepalive()
		case <-cacheCleanupTicker.C:
			h.evictStaleCachedSnapshots()
		case <-healthTicker.C:
			h.checkAndBroadcastHealth()
		case <-snapshotRefreshTicker.C:
			h.PushFullSnapshots()
		}
	}
}

// liveStreamScope identifies one cache/subscription scope. The tenant is
// normalized once so Redis reads and in-memory baselines cannot diverge for
// "" and "default".
type liveStreamScope struct {
	cacheKey string
	tenantID string
	isSuper  bool
}

func newLiveStreamScope(tenantID string, isSuper bool) liveStreamScope {
	if isSuper {
		return liveStreamScope{
			cacheKey: "scope:super",
			isSuper:  true,
		}
	}

	tenantID = normalizeLiveStreamTenant(tenantID)
	return liveStreamScope{
		cacheKey: "scope:tenant:" + tenantID,
		tenantID: tenantID,
	}
}

// computeScopeDelta reads a fresh snapshot for the given scope
// (tenantID="" + isSuper=true for the global view, or tenantID+false
// for a tenant view) and returns the delta against the cached snapshot
// for that scope. It NEVER overwrites the cache with an empty snapshot:
// when Redis is momentarily empty we keep the last good snapshot so a
// transient Redis hiccup cannot blank out every client's lanes (the
// root cause of the "queues disappear, come back on refresh" bug).
func (h *LiveStreamSSEHub) computeScopeDelta(ctx context.Context, tenantID string, isSuper bool) *LiveStreamDelta {
	if h.store == nil {
		return nil
	}
	scope := newLiveStreamScope(tenantID, isSuper)

	// Entering the scope refreshes an existing baseline before any Redis I/O.
	// This preserves the last known snapshot across a transient empty/error
	// read without creating entries for scopes that never produced a snapshot.
	h.cachedSnapshotMu.Lock()
	if entry := h.cachedSnapshot[scope.cacheKey]; entry != nil {
		entry.lastAccessed = time.Now()
		atomic.AddInt64(&h.cachedSnapshotBaselinePresent, 1)
	} else {
		atomic.AddInt64(&h.cachedSnapshotBaselineAbsent, 1)
	}
	h.cachedSnapshotMu.Unlock()

	// 2026-07-19: Use dimension-queue-based snapshot to fix swim lane flickering.
	// Read from dimension queues ensures each vendor/provider/model gets its
	// full 20-tile allocation regardless of traffic distribution in main queue.
	snapshot, err := h.store.SnapshotFromDimensionQueues(ctx, scope.tenantID, scope.isSuper)
	if err != nil {
		slog.Debug("live stream scope snapshot failed",
			"scope_tenant", scope.tenantID, "is_super", scope.isSuper, "err", err.Error())
		return nil
	}
	if snapshot == nil || snapshot.Summary.Total == 0 {
		if items, replayErr := h.replay(ctx, scope.tenantID, scope.isSuper, h.cfg.InitialReplayLimit); replayErr == nil && len(items) > 0 {
			snapshot = BuildLiveStreamSnapshot(items)
		} else if replayErr != nil {
			slog.Debug("live stream scope replay fallback failed",
				"scope_tenant", scope.tenantID, "is_super", scope.isSuper, "err", replayErr.Error())
		}
	}
	// Guard: do not let an empty snapshot overwrite a populated cache.
	// An empty read from Redis is treated as "no change" (return the
	// last delta is pointless; better to simply skip). This is what
	// stops a 200ms Redis stall from clearing every dashboard.
	if snapshot == nil || snapshot.Summary.Total == 0 {
		atomic.AddInt64(&h.cachedSnapshotEmptySkips, 1)
		return nil
	}
	h.cachedSnapshotMu.Lock()
	defer h.cachedSnapshotMu.Unlock()
	var cached *LiveStreamSnapshot
	if entry := h.cachedSnapshot[scope.cacheKey]; entry != nil {
		cached = entry.snapshot
	}
	delta := ComputeDelta(cached, snapshot)
	h.cachedSnapshot[scope.cacheKey] = &cachedSnapshotEntry{
		snapshot:     snapshot,
		lastAccessed: time.Now(),
	}
	return delta
}

// evictStaleCachedSnapshots removes cached snapshots for scopes that are both
// inactive and older than CachedSnapshotTTL. Connected SSE clients retain
// their scope baseline so their next request continues as a delta rather than
// restarting from a full snapshot.
func (h *LiveStreamSSEHub) evictStaleCachedSnapshots() {
	now := time.Now()
	activeScopes := h.activeScopes()
	var evicted int64
	h.cachedSnapshotMu.Lock()
	for key, entry := range h.cachedSnapshot {
		if _, active := activeScopes[key]; active {
			continue
		}
		if now.Sub(entry.lastAccessed) > h.cfg.CachedSnapshotTTL {
			delete(h.cachedSnapshot, key)
			evicted++
		}
	}
	remain := int64(len(h.cachedSnapshot))
	h.cachedSnapshotMu.Unlock()

	atomic.AddInt64(&h.cachedSnapshotEvictions, evicted)

	if evicted > 0 {
		slog.Info("live stream cached snapshot evicted",
			"evicted", evicted,
			"remain", remain,
			"active_scopes", len(activeScopes),
			"ttl", h.cfg.CachedSnapshotTTL.String(),
			"cleanup_interval", h.cfg.CachedSnapshotCleanupInterval.String())
	}
}

// PushFullSnapshots reads a fresh snapshot from Redis for every active
// scope and pushes it to connected clients as a "snapshot_refresh" envelope.
// This ensures the dashboard's base data stays current even when the delta
// stream has gaps (e.g. after a period of no traffic or a Redis partition).
// The frontend replaces its local snapshot with the fresh one.
//
// 2026-07-19: Temporarily disabled because periodic snapshot_refresh overwrote
// newer delta updates (race condition: Redis snapshot data could be older than
// what the frontend already received via real-time deltas).
//
// 2026-07-25 RE-ENABLED: Each snapshot now carries LatestRequestTs — the max
// request timestamp across all tiles in the snapshot. The frontend tracks the
// max timestamp it has ever seen (from snapshots, deltas, and initial_data)
// and rejects any snapshot_refresh whose LatestRequestTs ≤ local max timestamp.
// This prevents stale Redis snapshots from overwriting newer frontend state.
func (h *LiveStreamSSEHub) PushFullSnapshots() {
	if h.store == nil {
		return
	}
	h.mu.RLock()
	type scopeEntry struct {
		tenantID string
		isSuper  bool
	}
	seen := make(map[string]scopeEntry)
	for c := range h.clients {
		scope := newLiveStreamScope(c.tenantID, c.isSuper)
		if _, ok := seen[scope.cacheKey]; !ok {
			seen[scope.cacheKey] = scopeEntry{tenantID: scope.tenantID, isSuper: scope.isSuper}
		}
	}
	h.mu.RUnlock()

	if len(seen) == 0 {
		return
	}

	for _, entry := range seen {
		h.pushScopeSnapshot(entry.tenantID, entry.isSuper)
	}
}

// pushScopeSnapshot reads a fresh snapshot for one scope and broadcasts it.
func (h *LiveStreamSSEHub) pushScopeSnapshot(tenantID string, isSuper bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	snapshot, err := h.store.SnapshotFromDimensionQueues(ctx, tenantID, isSuper)
	if err != nil {
		slog.Debug("live stream snapshot refresh failed",
			"tenant_id", tenantID, "is_super", isSuper, "err", err.Error())
		return
	}
	if snapshot == nil || snapshot.Summary.Total == 0 {
		return
	}

	scope := newLiveStreamScope(tenantID, isSuper)
	env := LiveStreamEnvelope{
		Type:      "snapshot_refresh",
		Timestamp: time.Now().UTC(),
		Snapshot:  snapshot,
	}
	h.fanOutScope(scope, env)
}

// Stop tears down the hub. Safe to call once.
func (h *LiveStreamSSEHub) Stop() {
	select {
	case <-h.stopCh:
	default:
		close(h.stopCh)
	}
}

// ProviderCodeFor resolves a providers.id to its display name, falling
// back to catalog_code/code when the provider has no name.
func (h *LiveStreamSSEHub) ProviderCodeFor(ctx context.Context, providerID int) string {
	if providerID == 0 {
		slog.Debug("live stream provider lookup: zero provider_id")
		return ""
	}
	if h.db == nil {
		slog.Debug("live stream provider lookup: no database connection")
		return ""
	}
	if cached, ok := h.providerCache.Load(providerID); ok {
		return cached.(string)
	}
	var display string
	row := h.db.QueryRow(ctx, providerCodeForSQL, providerID)
	if err := row.Scan(&display); err != nil {
		slog.Debug("live stream provider lookup failed", "provider_id", providerID, "err", err.Error())
		h.providerCache.Store(providerID, "")
		return ""
	}
	if display == "" {
		slog.Debug("live stream provider lookup: empty result", "provider_id", providerID)
	}
	h.providerCache.Store(providerID, display)
	return display
}

// ProviderCodeForCredential resolves the provider through the credential
// actually used by the request. This avoids displaying a stale or missing
// provider_id from the telemetry envelope.
func (h *LiveStreamSSEHub) ProviderCodeForCredential(ctx context.Context, credentialID int) string {
	if credentialID == 0 || h == nil || h.db == nil {
		if credentialID == 0 {
			slog.Debug("live stream credential provider lookup: zero credential_id")
		} else if h.db == nil {
			slog.Debug("live stream credential provider lookup: no database connection")
		}
		return ""
	}
	cacheKey := fmt.Sprintf("cred:%d", credentialID)
	if cached, ok := h.providerCache.Load(cacheKey); ok {
		return cached.(string)
	}
	var display string
	row := h.db.QueryRow(ctx, providerCodeForCredentialSQL, credentialID)
	if err := row.Scan(&display); err != nil {
		slog.Debug("live stream credential provider lookup failed", "credential_id", credentialID, "err", err.Error())
		h.providerCache.Store(cacheKey, "")
		return ""
	}
	if display == "" {
		slog.Debug("live stream credential provider lookup: empty result", "credential_id", credentialID)
	}
	h.providerCache.Store(cacheKey, display)
	return display
}

// ModelVendorFor resolves a model name to its vendor via models_canonical.family → model_families.vendor.
// Falls back to pattern matching when DB lookup fails.
func (h *LiveStreamSSEHub) ModelVendorFor(ctx context.Context, model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "" // Don't cache empty strings, return empty to skip this dimension
	}
	if h == nil || h.db == nil {
		vendor := classifyModelCategoryFallback(model)
		if vendor == "" {
			slog.Debug("model vendor classification failed (no db, no pattern match)", "model", model)
		}
		return vendor
	}

	// Check cache first
	if cached, ok := h.modelFamilyCache.Load(model); ok {
		return cached.(string)
	}

	// Query database: model → family → vendor
	var vendor string
	row := h.db.QueryRow(ctx, `
		SELECT COALESCE(mf.vendor, '')
		FROM models_canonical mc
		LEFT JOIN model_families mf ON mf.id = mc.family
		WHERE mc.canonical_name = $1
		LIMIT 1
	`, model)

	if err := row.Scan(&vendor); err != nil || vendor == "" {
		// Fallback to pattern matching
		vendor = classifyModelCategoryFallback(model)
		if vendor == "" {
			slog.Debug("model vendor not found in db or pattern", "model", model, "db_error", err)
		}
		h.modelFamilyCache.Store(model, vendor)
		return vendor
	}

	// Normalize vendor name to lowercase for consistency
	vendor = strings.ToLower(strings.TrimSpace(vendor))
	h.modelFamilyCache.Store(model, vendor)
	return vendor
}

// CanonicalNameFor resolves a canonical_id to its canonical_name from
// models_canonical. This enables the live stream to display models by their
// standard identity rather than credential-level outbound names, so the same
// model from different credentials aggregates into one lane instead of being
// scattered. Returns empty string when the ID is zero, invalid, or lookup fails.
//
// 2026-07-16: cache check moved BEFORE the db nil guard so unit tests can
// pre-populate canonicalCache to drive the new canonical-first fallback in
// LiveRequestFromTelemetry without standing up a real database.
func (h *LiveStreamSSEHub) CanonicalNameFor(ctx context.Context, canonicalID int) string {
	if canonicalID == 0 {
		return ""
	}
	// h == nil guard MUST come before any h.<field> access to avoid panic.
	if h == nil {
		return ""
	}

	// Check cache first (works without DB; tests pre-populate here).
	if cached, ok := h.canonicalCache.Load(canonicalID); ok {
		return cached.(string)
	}

	if h.db == nil {
		return ""
	}

	// Query database: canonical_id → canonical_name
	var canonicalName string
	row := h.db.QueryRow(ctx, `
		SELECT canonical_name
		FROM models_canonical
		WHERE id = $1
		  AND COALESCE(status, 'active') = 'active'
		LIMIT 1
	`, canonicalID)

	if err := row.Scan(&canonicalName); err != nil {
		slog.Debug("canonical name lookup failed", "canonical_id", canonicalID, "err", err.Error())
		h.canonicalCache.Store(canonicalID, "")
		return ""
	}

	canonicalName = strings.TrimSpace(canonicalName)
	h.canonicalCache.Store(canonicalID, canonicalName)
	return canonicalName
}

// VendorFromProvider attempts to infer the model vendor (category) from the
// provider code when the model name itself is unknown/empty. Many providers
// have a 1:1 mapping with a vendor (e.g., "openai" provider → "openai" vendor).
// Returns empty string if no mapping exists or provider is unrecognized.
func VendorFromProvider(providerCode string) string {
	// Normalize to lowercase for comparison
	p := strings.ToLower(strings.TrimSpace(providerCode))

	// Direct provider → vendor mappings (providers that exclusively serve one vendor)
	knownMappings := map[string]string{
		"openai":    "openai",
		"anthropic": "anthropic",
		"google":    "google",
		"alibaba":   "alibaba",
		"qwen":      "alibaba",
		"zhipu":     "zhipu",
		"deepseek":  "deepseek",
		"bytedance": "bytedance",
		"doubao":    "bytedance",
		"baidu":     "baidu",
		"moonshot":  "moonshot",
		"01ai":      "01ai",
		"baichuan":  "baichuan",
		"meta":      "meta",
		"mistral":   "mistral",
		"xiaomi":    "xiaomi",
		"microsoft": "microsoft",
		"xai":       "xai",
		"stepfun":   "stepfun",
		"minimax":   "minimax",
	}

	if vendor, ok := knownMappings[p]; ok {
		return vendor
	}

	// Partial match for composite provider codes (e.g., "openai-azure" → "openai")
	for providerKey, vendor := range knownMappings {
		if strings.Contains(p, providerKey) {
			return vendor
		}
	}

	return ""
}

func (h *LiveStreamSSEHub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		h.safeClose(c)
		delete(h.clients, c)
	}
}

func (h *LiveStreamSSEHub) safeClose(c *liveStreamClient) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.closed = true
}

func (h *LiveStreamSSEHub) maybeEmitIdleMarker() {
	now := time.Now().UTC()

	// 1) Write idle markers to Redis so they survive delta updates.
	//    Previously the frontend managed idle tiles locally, but backend
	//    delta updates (mergeLanesById → target.requests = lane.requests)
	//    would wipe them, causing the "idle tile flicker" bug.
	if h.store != nil {
		// 60s budget: SCAN must traverse all 396K+ keys in the shared
		// Redis DB to find ~50 activity keys. With 0.2ms RTT to Redis
		// the actual scan takes <1s, but we keep 60s headroom so the
		// hub never logs a spurious "context deadline exceeded" if the
		// network blips.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := h.store.ScanAndRecordIdleMarkers(ctx, now, 0); err != nil {
			slog.Warn("live stream idle marker scan failed", "err", err.Error())
		}
		cancel()
	}

	// 2) Collect all known lane IDs from cached snapshots across all
	//    scopes. This is still sent so the frontend can synchronise its
	//    lane roster without waiting for the next request delta.
	h.cachedSnapshotMu.RLock()
	seenIDs := make(map[string]bool)
	var laneIDs []string

	for _, entry := range h.cachedSnapshot {
		if entry == nil || entry.snapshot == nil {
			continue
		}
		snap := entry.snapshot
		for _, dim := range []string{"vendor", "provider", "model"} {
			for _, lane := range snap.Dimensions[dim] {
				lid := dim + ":" + lane.ID
				if !seenIDs[lid] {
					seenIDs[lid] = true
					laneIDs = append(laneIDs, lid)
				}
			}
		}
	}
	h.cachedSnapshotMu.RUnlock()

	if len(laneIDs) == 0 {
		return
	}

	// 3) Compute a delta for each active scope so the idle markers
	//    written in step 1 are pushed to every connected client as a
	//    real delta (no flicker — the idle tile arrives via the same
	//    mergeDelta path that regular request updates use).
	h.mu.RLock()
	type clientScope struct {
		c        *liveStreamClient
		tenantID string
		isSuper  bool
	}
	all := make([]clientScope, 0, len(h.clients))
	for c := range h.clients {
		all = append(all, clientScope{
			c:        c,
			tenantID: normalizeLiveStreamTenant(c.tenantID),
			isSuper:  c.isSuper,
		})
	}
	h.mu.RUnlock()

	// Deduplicate scopes so we call computeScopeDelta once per unique
	// (tenantID, isSuper) pair.
	deltaByCacheKey := make(map[string]*LiveStreamDelta, len(all))
	for _, cs := range all {
		sk := newLiveStreamScope(cs.tenantID, cs.isSuper).cacheKey
		if _, ok := deltaByCacheKey[sk]; ok {
			continue
		}
		if h.store == nil {
			deltaByCacheKey[sk] = nil
			continue
		}
		// 2026-07-23: 修复实时流只显示 1 个泳道的 bug（同上：从 200ms 改为 2s）
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		deltaByCacheKey[sk] = h.computeScopeDelta(ctx, cs.tenantID, cs.isSuper)
		cancel()
	}

	// 4) Fan out — each client receives the envelope whose delta matches
	//    its own scope.
	base := LiveStreamEnvelope{
		Type:      "idle_marker",
		Timestamp: now,
		LaneIDs:   laneIDs,
	}
	for _, cs := range all {
		sk := newLiveStreamScope(cs.tenantID, cs.isSuper).cacheKey
		env := base
		env.Delta = deltaByCacheKey[sk]
		data, err := json.Marshal(env)
		if err != nil {
			slog.Warn("live stream idle marker marshal failed", "err", err.Error())
			continue
		}
		if !h.writeEvent(cs.c, data) {
			h.evict(cs.c)
		}
	}

	slog.Debug("live stream idle marker injected",
		"lanes", len(laneIDs))
}

// checkAndBroadcastHealth pings Redis and broadcasts a health_update
// envelope when the health state changes. This lets the frontend show
// a warning banner when Redis is unavailable.
func (h *LiveStreamSSEHub) checkAndBroadcastHealth() {
	health := h.checkRedisHealth()

	h.lastHealthMu.RLock()
	prev := h.lastHealth
	h.lastHealthMu.RUnlock()

	// Only broadcast when state changes (or on first check).
	if prev != nil && prev.RedisConnected == health.RedisConnected &&
		prev.RedisError == health.RedisError {
		return
	}

	h.lastHealthMu.Lock()
	h.lastHealth = health
	h.lastHealthMu.Unlock()

	h.fanOut(LiveStreamEnvelope{
		Type:      "health_update",
		Timestamp: time.Now().UTC(),
		Health:    health,
	})
}

// checkRedisHealth pings Redis with a short timeout. Returns a
// healthy status when the store or client is nil (graceful degradation).
func (h *LiveStreamSSEHub) checkRedisHealth() *LiveStreamHealth {
	if h.store == nil || h.store.rdb == nil {
		return &LiveStreamHealth{RedisConnected: false, RedisError: "Redis not configured"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := h.store.rdb.Ping(ctx).Err(); err != nil {
		return &LiveStreamHealth{RedisConnected: false, RedisError: err.Error()}
	}
	return &LiveStreamHealth{RedisConnected: true}
}

// fanOutKeepalive sends an SSE comment frame to every connected
// client. Comments (lines starting with ":") are ignored by
// EventSource but DO reset the read deadline on most reverse
// proxies (nginx, cloudflare).
func (h *LiveStreamSSEHub) fanOutKeepalive() {
	h.mu.RLock()
	clients := make([]*liveStreamClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		h.writeComment(c, ":keepalive\n\n")
	}
}

// fanOut serialises a payload to every eligible client. When the
// envelope carries a super-scope delta (superDelta) in addition to the
// tenant-scope delta, super-admin clients are serialised with the
// super delta so their lane view reflects the global snapshot rather
// than whichever tenant happened to produce the latest request.
func (h *LiveStreamSSEHub) fanOut(env LiveStreamEnvelope) {
	h.mu.RLock()
	clients := make([]*liveStreamClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	// Pre-serialise the two variants at most once.
	var tenantData, superData []byte
	var marshalErr error
	tenantEnv := env
	tenantEnv.Delta = env.Delta
	if tenantData, marshalErr = json.Marshal(tenantEnv); marshalErr != nil {
		slog.Warn("live stream marshal failed (tenant)", "err", marshalErr.Error())
		return
	}
	if env.superDelta != nil {
		superEnv := env
		superEnv.Delta = env.superDelta
		if superData, marshalErr = json.Marshal(superEnv); marshalErr != nil {
			slog.Warn("live stream marshal failed (super)", "err", marshalErr.Error())
			// Fall back to tenant payload for everyone rather than dropping.
			superData = nil
		}
	}

	for _, c := range clients {
		if !h.shouldDeliver(c, env) {
			continue
		}
		// Super-admin clients get the global-scoped delta when available.
		payload := tenantData
		if c.isSuper && superData != nil {
			payload = superData
		}
		if !h.writeEvent(c, payload) {
			h.evict(c)
		}
	}
}

// fanOutScope delivers a scope-specific refresh only to clients subscribed to
// that scope. A snapshot for one tenant must never replace another tenant's
// lane queue in an open dashboard.
func (h *LiveStreamSSEHub) fanOutScope(scope liveStreamScope, env LiveStreamEnvelope) {
	data, err := json.Marshal(env)
	if err != nil {
		slog.Warn("live stream scoped marshal failed", "err", err.Error())
		return
	}

	h.mu.RLock()
	clients := make([]*liveStreamClient, 0, len(h.clients))
	for c := range h.clients {
		if newLiveStreamScope(c.tenantID, c.isSuper).cacheKey == scope.cacheKey {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range clients {
		if !h.writeEvent(c, data) {
			h.evict(c)
		}
	}
}

func (h *LiveStreamSSEHub) shouldDeliver(c *liveStreamClient, env LiveStreamEnvelope) bool {
	if c.isSuper {
		return true
	}
	if env.Incident != nil {
		return normalizeLiveStreamTenant(env.Incident.TenantID) == normalizeLiveStreamTenant(c.tenantID)
	}
	if env.Request != nil {
		return env.Request.TenantID == c.tenantID
	}
	return true
}

// activeScopes returns the currently subscribed cache scopes. It derives the
// set from the authoritative client registry, so register/unregister and
// shutdown need no parallel subscription bookkeeping.
func (h *LiveStreamSSEHub) activeScopes() map[string]struct{} {
	h.mu.RLock()
	defer h.mu.RUnlock()

	scopes := make(map[string]struct{}, len(h.clients))
	for client := range h.clients {
		scope := newLiveStreamScope(client.tenantID, client.isSuper)
		scopes[scope.cacheKey] = struct{}{}
	}
	return scopes
}

// hasSuperClient reports whether any connected client is a super admin.
// Used to skip the (relatively expensive) global-scope snapshot read on
// the hot broadcast path when nobody is watching the super-admin view.
func (h *LiveStreamSSEHub) hasSuperClient() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.isSuper {
			return true
		}
	}
	return false
}

// logDeltaDetails logs detailed delta information for debugging swim-lane rolling issues.
// 2026-07-21: Track every delta push to identify when/why lane windows shift.
func (h *LiveStreamSSEHub) logDeltaDetails(scope string, tenantID string, requestID string, delta *LiveStreamDelta) {
	if delta == nil {
		return
	}

	// Only log when there are actual lane changes
	if len(delta.ChangedLanes) == 0 {
		return
	}

	// Build a compact summary of what changed
	var changedSummary []string
	for dim, lanes := range delta.ChangedLanes {
		for _, lane := range lanes {
			// Extract request IDs for detailed tracking
			requestIDs := make([]string, 0, len(lane.Requests))
			for _, tile := range lane.Requests {
				requestIDs = append(requestIDs, tile.RequestID)
			}

			summary := fmt.Sprintf("%s/%s: total=%d tiles=%d ids=[%s...%s]",
				dim,
				lane.Name,
				lane.Stats.Total,
				len(lane.Requests),
				firstN(requestIDs, 2),
				lastN(requestIDs, 2),
			)
			changedSummary = append(changedSummary, summary)
		}
	}

	slog.Info("live stream delta push",
		"scope", scope,
		"tenant_id", tenantID,
		"trigger_request", requestID,
		"summary_total", delta.Summary.Total,
		"summary_success", delta.Summary.Success,
		"summary_failure", delta.Summary.Failure,
		"changed_lanes_count", len(delta.ChangedLanes),
		"changed_details", strings.Join(changedSummary, " | "),
	)
}

// firstN returns the first N elements of a slice as a comma-separated string
func firstN(items []string, n int) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) <= n {
		return strings.Join(items, ",")
	}
	return strings.Join(items[:n], ",")
}

// lastN returns the last N elements of a slice as a comma-separated string
func lastN(items []string, n int) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) <= n {
		return strings.Join(items, ",")
	}
	return strings.Join(items[len(items)-n:], ",")
}

// writeEvent serialises one envelope to one client.
//
// Wire format:
//
//	event: message\n
//	data: <json>\n
//	\n
func (h *LiveStreamSSEHub) writeEvent(c *liveStreamClient, data []byte) bool {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("live stream write panic", "err", fmt.Sprint(r))
			c.closed = true
		}
	}()
	if _, err := fmt.Fprintf(c.w, "event: message\ndata: %s\n\n", data); err != nil {
		return false
	}
	if c.fl != nil {
		c.fl.Flush()
	}
	return true
}

func (h *LiveStreamSSEHub) writeComment(c *liveStreamClient, commentFrame string) bool {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("live stream keepalive panic", "err", fmt.Sprint(r))
			c.closed = true
		}
	}()
	if _, err := c.w.Write([]byte(commentFrame)); err != nil {
		return false
	}
	if c.fl != nil {
		c.fl.Flush()
	}
	return true
}

func (h *LiveStreamSSEHub) evict(c *liveStreamClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	h.safeClose(c)
}

// runRedisSubscriber listens for Redis pub/sub notifications and
// enqueues reconstructed requests for SSE fan-out. This decouples
// live updates from the request handler: any gateway instance that
// writes to Redis can drive dashboards on every connected hub.
func (h *LiveStreamSSEHub) runRedisSubscriber() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-h.stopCh
		cancel()
	}()

	pubsub := h.cfg.RedisClient.Subscribe(ctx, liveStreamNotifyChannel)
	defer pubsub.Close()

	slog.Info("live stream redis subscriber started", "channel", liveStreamNotifyChannel)

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-pubsub.Channel():
			if !ok {
				return
			}
			h.handleRedisNotify(msg.Payload)
		}
	}
}

func (h *LiveStreamSSEHub) handleRedisNotify(payload string) {
	if h.store == nil {
		return
	}
	var notify liveStreamNotifyPayload
	if err := json.Unmarshal([]byte(payload), &notify); err != nil {
		slog.Debug("live stream redis notify: invalid payload", "err", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	req, err := h.store.LoadRequest(ctx, notify.TenantID, notify.RequestID)
	cancel()
	if err != nil {
		slog.Debug("live stream redis notify: load request failed", "request_id", notify.RequestID, "tenant_id", notify.TenantID, "err", err.Error())
		return
	}
	h.enqueueBroadcast(req)
}

func (h *LiveStreamSSEHub) enqueueBroadcast(req LiveRequest) {
	select {
	case h.broadcast <- req:
	default:
		atomic.AddInt64(&h.broadcastDrops, 1)
		slog.Warn("live stream broadcast queue full, dropping request",
			"request_id", req.RequestID,
			"drops_total", atomic.LoadInt64(&h.broadcastDrops))
	}
}

// Publish persists a request to Redis and notifies every subscriber
// to fan out SSE updates. Always broadcasts regardless of Redis state
// so connected SSE clients receive the request in real-time.
func (h *LiveStreamSSEHub) Publish(req LiveRequest) {
	if h.store != nil && h.cfg.RedisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if err := h.store.Record(ctx, req); err != nil {
			slog.Debug("live stream redis record failed", "request_id", req.RequestID, "tenant_id", req.TenantID, "model", req.Model, "provider", req.ProviderCode, "err", err.Error())
		}
		cancel()
		h.enqueueBroadcast(req)
		return
	}
	if h.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if err := h.store.Record(ctx, req); err != nil {
			slog.Debug("live stream redis record failed", "request_id", req.RequestID, "tenant_id", req.TenantID, "model", req.Model, "provider", req.ProviderCode, "err", err.Error())
		}
		cancel()
	}
	h.enqueueBroadcast(req)
}

// incidentUpdateCh is a separate, lower-priority channel for
// route-incident updates. We keep the two channels distinct so
// bursts of incident updates (e.g. a flapping route) cannot
// starve the per-request feed.
const (
	incidentUpdateChCapacity = 256
	incidentUpdateDropsLog   = 50 // log every Nth drop to bound the log volume
)

// PublishIncidentUpdate fans out a route incident transition to all
// connected SSE clients. It is non-blocking: when the channel is
// full the update is dropped and a counter is incremented (logged
// at most every incidentUpdateDropsLog drops). Returns immediately
// if the hub is nil or the update is nil.
//
// The function is intentionally separate from Publish so the
// per-request feed (which has its own back-pressure policy) is not
// affected by incident bursts. The observer calls this from its
// own goroutine so the call is also non-blocking with respect to
// the telemetry worker.
func (h *LiveStreamSSEHub) PublishIncidentUpdate(upd *LiveIncidentUpdate) {
	if h == nil || upd == nil {
		return
	}
	if h.incidentUpdateCh == nil {
		h.initIncidentUpdateCh()
	}
	upd.Type = "incident_update"
	env := LiveStreamEnvelope{
		Type:      "incident_update",
		Timestamp: time.Now().UTC(),
		Incident:  upd,
	}
	select {
	case h.incidentUpdateCh <- env:
	default:
		h.incidentDrops.Add(1)
		if n := h.incidentDrops.Load(); n%incidentUpdateDropsLog == 1 {
			slog.Warn("live stream incident update queue full, dropping",
				"dropped", n,
				"incident_id", upd.IncidentID,
				"state", upd.State,
			)
		}
	}
}

// initIncidentUpdateCh lazily creates the channel + drops counter.
// The hub constructor doesn't pre-allocate them so the cost is
// only paid when the diagnose feature is actually wired in.
func (h *LiveStreamSSEHub) initIncidentUpdateCh() {
	h.incidentMu.Lock()
	defer h.incidentMu.Unlock()
	if h.incidentUpdateCh == nil {
		h.incidentUpdateCh = make(chan LiveStreamEnvelope, incidentUpdateChCapacity)
		// Drive a tiny fan-out goroutine so the observer never
		// touches the broadcast channel directly. The hub's main
		// loop is too expensive to poll many channels; this is
		// the cheapest correct path.
		go h.fanOutIncidentUpdates()
	}
}

func (h *LiveStreamSSEHub) fanOutIncidentUpdates() {
	for {
		select {
		case <-h.stopCh:
			return
		case env, ok := <-h.incidentUpdateCh:
			if !ok {
				return
			}
			h.fanOut(env)
		}
	}
}

// HandleLiveStream is the SSE entry point.
//
// Route: GET /api/admin/live-stream
//
// IMPORTANT: this handler is mounted inside the admin Handler (which
// runs it inside h.admin() / h.superAdmin()). The auth check has
// already passed by the time we get here.
//
// Browser EventSource uses the same-origin HttpOnly session cookie. Clients
// without that cookie must authenticate through the standard Authorization
// header; credentials are never accepted in a query string.
func (h *LiveStreamSSEHub) HandleLiveStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := ""
	isSuper := IsSuperAdminOrLegacy(r)
	if IsTenantAdmin(r) {
		tenantID = GetTenantID(r)
		isSuper = false
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Origin check.
	origin := r.Header.Get("Origin")
	if origin != "" {
		o := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
		if o != r.Host && !strings.HasPrefix(o, r.Host+":") && !strings.HasPrefix(r.Host, o+":") {
			writeError(w, http.StatusForbidden, "origin not allowed")
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	client := &liveStreamClient{
		fl:       flusher,
		w:        w,
		tenantID: tenantID,
		isSuper:  isSuper,
	}
	h.register <- client
	defer func() { h.unregister <- client }()

	// Initial replay (oldest → newest so client renders left-to-right).
	// Include current Redis health so the client can show warnings immediately.
	initialHealth := h.checkRedisHealth()
	if items, err := h.replay(r.Context(), tenantID, isSuper, h.cfg.InitialReplayLimit); err != nil {
		slog.Warn("live stream initial replay failed", "err", err.Error())
	} else if len(items) > 0 {
		snapshot := BuildLiveStreamSnapshot(items)
		if h.store != nil {
			// 2026-07-19: Use dimension-queue-based snapshot for initial data
			if ss, ssErr := h.store.SnapshotFromDimensionQueues(r.Context(), tenantID, isSuper); ssErr == nil && ss != nil && ss.Summary.Total > 0 {
				snapshot = ss
			}
		}
		data, mErr := json.Marshal(LiveStreamEnvelope{
			Type:      "initial_data",
			Timestamp: time.Now().UTC(),
			Requests:  items,
			Snapshot:  snapshot,
			Health:    initialHealth,
		})
		if mErr == nil {
			h.writeEvent(client, data)
		}
	}

	// Block until the client disconnects.
	<-r.Context().Done()
}

// HandleTriggerSnapshot is a POST-only TEMPORARY DEBUG endpoint that triggers an immediate
// full-snapshot push. Added 2026-07-26 for snapshot_refresh guard validation.
// TODO: Remove this endpoint when no longer needed for debugging.
//
// Route: POST /api/admin/live-stream/trigger-snapshot
func (h *LiveStreamSSEHub) HandleTriggerSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.PushFullSnapshots()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// replay loads the most recent N requests. ASC order so the client
// renders them left-to-right.
func (h *LiveStreamSSEHub) replay(ctx context.Context, tenantID string, isSuper bool, limit int) ([]LiveRequest, error) {
	if h.store != nil {
		items, err := h.store.Replay(ctx, tenantID, isSuper, limit)
		if err == nil && len(items) > 0 {
			return items, nil
		}
		if err != nil {
			slog.Debug("live stream redis replay failed", "err", err.Error())
		}
	}
	if h.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tenantClause := ""
	args := []any{limit}
	if !isSuper || tenantID != "" {
		tenantClause = "AND rl.tenant_id = $2"
		args = append(args, tenantID)
	}

	query := `
		SELECT rl.request_id,
		       rl.ts,
		       COALESCE(NULLIF(rl.tenant_id, ''), 'default') AS tenant_id,
		       COALESCE(NULLIF(rl.gw_session_id, ''), '') AS gw_session_id,
		       COALESCE(NULLIF(mc.canonical_name, ''), NULLIF(rl.client_model, ''), rl.outbound_model, '') AS model,
		       COALESCE(mc.canonical_name, '') AS canonical_name,
		       COALESCE(NULLIF(p.display_name, ''), NULLIF(p.catalog_code, ''), NULLIF(p.code, ''), '') AS provider_code,
		       COALESCE(NULLIF(rl.request_status, ''), CASE WHEN rl.success THEN 'success' WHEN rl.success = FALSE THEN 'failure' ELSE 'in_progress' END) AS status,
		       rl.latency_ms,
		       rl.prompt_tokens,
		       rl.completion_tokens,
		       rl.total_tokens,
		       rl.cost_usd::float8,
		       rl.error_kind
		FROM request_logs_with_current_month rl
		LEFT JOIN credentials c ON c.id = rl.credential_id
		LEFT JOIN providers p ON p.id = COALESCE(c.provider_id, rl.provider_id)
		LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
		WHERE rl.ts >= NOW() - INTERVAL '1 hour'
		  ` + tenantClause + `
		ORDER BY rl.ts ASC
		LIMIT $1
	`

	rows, err := h.db.Query(cctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]LiveRequest, 0, limit)
	for rows.Next() {
		var r LiveRequest
		var ts time.Time
		if err := rows.Scan(
			&r.RequestID, &ts, &r.TenantID, &r.GwSessionID, &r.Model,
			&r.CanonicalName, &r.ProviderCode, &r.Status, &r.LatencyMs, &r.PromptTokens,
			&r.CompletionTokens, &r.TotalTokens, &r.CostUSD, &r.ErrorKind,
		); err != nil {
			continue
		}
		r.Ts = ts.UTC().Format(time.RFC3339)

		// Apply ModelCategory fallback: DB family.vendor → provider-code 映射 → 固化 model pattern → model 名本身
		// 老板要求："没有就用固化的标准，再没有就直接用模型名称，不要用未知或其它来标记。"
		if r.Model != "" {
			r.ModelCategory = h.ModelVendorFor(cctx, r.Model)
		}
		if r.ModelCategory == "" && r.ProviderCode != "" {
			r.ModelCategory = VendorFromProvider(r.ProviderCode)
		}
		if r.ModelCategory == "" && r.Model != "" {
			r.ModelCategory = InferVendorFromModel(r.Model)
		}
		// 最终兜底：把 model 名字本身当作"原厂"显示，前端泳道名称会显示出来，
		// 不会出现"未知"字样。
		if r.ModelCategory == "" {
			if r.Model != "" {
				r.ModelCategory = r.Model
			} else if r.CanonicalName != "" {
				r.ModelCategory = r.CanonicalName
			} else {
				r.ModelCategory = "其他"
			}
		}

		out = append(out, r)
	}
	return out, nil
}

// classifyModelCategoryFallback provides pattern-based vendor classification
// when database lookup is unavailable or fails.
// This is a fallback mechanism; prefer using ModelVendorFor with database lookup.
func classifyModelCategoryFallback(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gpt"), strings.Contains(m, "o1"), strings.Contains(m, "o3"), strings.Contains(m, "o4"):
		return "openai"
	case strings.Contains(m, "claude"):
		return "anthropic"
	case strings.Contains(m, "gemini"), strings.Contains(m, "palm"):
		return "google"
	case strings.Contains(m, "qwen"):
		return "alibaba"
	case strings.Contains(m, "glm"):
		return "zhipu"
	case strings.Contains(m, "deepseek"):
		return "deepseek"
	case strings.Contains(m, "doubao"):
		return "bytedance"
	case strings.Contains(m, "ernie"):
		return "baidu"
	case strings.Contains(m, "moonshot"):
		return "moonshot"
	case strings.Contains(m, "yi-"):
		return "01ai"
	case strings.Contains(m, "baichuan"):
		return "baichuan"
	case strings.Contains(m, "llama"):
		return "meta"
	case strings.Contains(m, "mistral"), strings.Contains(m, "mixtral"):
		return "mistral"
	case strings.Contains(m, "minimax"):
		return "minimax"
	case strings.Contains(m, "mimo"):
		return "xiaomi"
	case strings.Contains(m, "phi"):
		return "microsoft"
	case strings.Contains(m, "gemma"):
		return "google"
	default:
		// Return empty string instead of "other" to avoid creating an "other" queue
		// The caller should handle empty vendor appropriately
		return ""
	}
}

// LiveRequestFromTelemetry adapts a raw RequestLogEntry into a
// LiveRequest. This is a method on Hub to enable database-backed model vendor lookup.
// Implements fallback chains for all three key dimensions:
//   - Model: canonical_name (from canonicalID) → clientModel → outboundModel
//     (2026-07-16: standard-name-first; vendor raw name only as last-resort fallback
//     so the dashboard tile matches the dimension key built from CanonicalName.)
//   - ModelCategory: from Model → from Provider (when model is empty)
//   - ProviderCode: already resolved by caller (credential → provider)
//
// The trailing *telemetry.RequestLogEntry arg is used to populate the
// 2026-07-13 probe-pill fields (IsProbe / ProbeOrigin / ProbeAttempt):
//
//	task_type        = "probe_triggered" → IsProbe = true
//	task_type_chosen = "probe_direct" | "probe_gateway" | "probe_scheduled"
//	auto_decision    = JSON {"probe_attempt": N} → ProbeAttempt = N
func (h *LiveStreamSSEHub) LiveRequestFromTelemetry(
	ctx context.Context,
	requestID string,
	ts time.Time,
	tenantID string,
	clientModel string,
	outboundModel string,
	canonicalID int,
	providerCode string,
	status string,
	success bool,
	errorKind *string,
	latencyMs *int,
	promptTokens *int,
	completionTokens *int,
	totalTokens *int,
	costUSD *float64,
	failureStage *string,
	entry *telemetry.RequestLogEntry,
) LiveRequest {
	out := LiveRequest{
		RequestID:        requestID,
		Ts:               ts.UTC().Format(time.RFC3339),
		TenantID:         tenantID,
		ProviderCode:     providerCode,
		LatencyMs:        latencyMs,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		CostUSD:          costUSD,
		ErrorKind:        errorKind,
		FailureStage:     failureStage,
	}

	// Model fallback chain: canonical_name → client → outbound.
	//
	// 老板要求（2026-07-16）"实时请求流后台分维要根据模型进行分维时，
	// 需要将调用的模型名称全部转成标准名称再建立维度，然后请求过来后
	// 要使用请求中的标准模型名称，不是供应商的原始模型名称"：
	//
	//   1. 分维度（liveStreamDimensionKey 的 model 分支）已经用
	//      CanonicalName 优先 + normalizeModelKey 做 case-insensitive 聚合；
	//   2. 但流式推给前端的单条 tile 仍然把供应商原始名（outbound_model）
	//      当成"model"显示，会让同一标准模型跨凭证散成多个泳道名称。
	//
	// 这里把 Model 字段的选择顺序倒过来，标准名 (CanonicalName) 永远优先；
	// 解析不出标准名时回退到 client，再没才用 outbound（兜底保留向后兼容）。
	canonicalName := ""
	if canonicalID > 0 {
		canonicalName = h.CanonicalNameFor(ctx, canonicalID)
	}
	if canonicalName != "" {
		out.Model = canonicalName
	} else if clientModel != "" {
		out.Model = clientModel
	} else {
		out.Model = outboundModel
	}

	// Set CanonicalName for model dimension aggregation (always use canonical if available)
	out.CanonicalName = canonicalName
	// Fallback: if no canonicalID but we have a model, use that as canonical (best effort)
	if out.CanonicalName == "" && out.Model != "" {
		out.CanonicalName = out.Model
	}

	// Log when provider is missing to help diagnose the issue
	if providerCode == "" {
		slog.Debug("live request from telemetry: missing provider_code",
			"request_id", requestID, "model", out.Model, "tenant_id", tenantID)
	}

	// ModelCategory fallback chain: from Model → from Provider → from Model pattern
	if out.Model != "" {
		out.ModelCategory = h.ModelVendorFor(ctx, out.Model)
	}
	if out.ModelCategory == "" && providerCode != "" {
		// Try inferring vendor from provider (e.g., "openai" provider → "openai" vendor)
		out.ModelCategory = VendorFromProvider(providerCode)
		if out.ModelCategory != "" {
			slog.Debug("live stream: inferred model_category from provider",
				"request_id", requestID, "provider", providerCode, "category", out.ModelCategory)
		}
	}
	if out.ModelCategory == "" && out.Model != "" {
		// Last resort: infer from model name patterns
		out.ModelCategory = InferVendorFromModel(out.Model)
		if out.ModelCategory != "" {
			slog.Debug("live stream: inferred model_category from model name pattern",
				"request_id", requestID, "model", out.Model, "category", out.ModelCategory)
		}
	}
	// 最终兜底：把 model 名字本身当作"原厂"显示。老板原话：
	//   "原厂"泳道没有正确从模型数据获取到原厂信息时，应该用固化的标准，
	//   再没有就直接用模型名称，不要用未知或其它来标记。
	if out.ModelCategory == "" {
		if out.Model != "" {
			out.ModelCategory = out.Model
		} else if out.CanonicalName != "" {
			out.ModelCategory = out.CanonicalName
		} else {
			out.ModelCategory = "其他"
		}
	}

	if status == "" {
		switch {
		case success:
			out.Status = "success"
		case errorKind != nil && strings.TrimSpace(*errorKind) != "":
			out.Status = "failure"
		default:
			out.Status = "in_progress"
		}
	} else {
		out.Status = status
	}

	// 2026-07-13: 主动探测 pill — 见 admin/probe_request_info.go 的 extractProbeInfo
	// （admin 包内直接调用，无需 import；同一文件内定义保持内聚）。
	probe := extractProbeInfo(entry)
	out.IsProbe = probe.IsProbe
	out.ProbeOrigin = probe.ProbeOrigin
	out.ProbeAttempt = probe.ProbeAttempt
	if entry != nil {
		if entry.ClientProfile != nil {
			out.ClientProfile = strings.TrimSpace(*entry.ClientProfile)
		}
		if entry.IdentityHash != nil {
			out.IdentityHash = strings.TrimSpace(*entry.IdentityHash)
		}
		if entry.CreditsCharged != nil {
			v := int(*entry.CreditsCharged)
			out.CreditsCharged = &v
		}
		if entry.GwSessionID != nil && out.GwSessionID == "" {
			out.GwSessionID = strings.TrimSpace(*entry.GwSessionID)
		}
	}

	return out
}

// Stats 返回 SSE Hub 的监控指标
func (h *LiveStreamSSEHub) Stats() map[string]interface{} {
	h.mu.RLock()
	activeClients := len(h.clients)
	activeScopes := make(map[string]struct{}, activeClients)
	for client := range h.clients {
		scope := newLiveStreamScope(client.tenantID, client.isSuper)
		activeScopes[scope.cacheKey] = struct{}{}
	}
	h.mu.RUnlock()

	h.cachedSnapshotMu.RLock()
	cachedSnapshotEntries := len(h.cachedSnapshot)
	h.cachedSnapshotMu.RUnlock()

	h.lastActivityMu.RLock()
	lastActivity := h.lastActivity
	h.lastActivityMu.RUnlock()

	return map[string]interface{}{
		"active_clients":                   activeClients,
		"active_scope_subscriptions":       len(activeScopes),
		"total_connections":                atomic.LoadInt64(&h.totalConnections),
		"total_disconnections":             atomic.LoadInt64(&h.totalDisconnections),
		"auth_failures":                    atomic.LoadInt64(&h.authFailures),
		"broadcast_count":                  atomic.LoadInt64(&h.broadcastCount),
		"broadcast_drops":                  atomic.LoadInt64(&h.broadcastDrops),
		"cached_snapshot_baseline_present": atomic.LoadInt64(&h.cachedSnapshotBaselinePresent),
		"cached_snapshot_baseline_absent":  atomic.LoadInt64(&h.cachedSnapshotBaselineAbsent),
		"cached_snapshot_empty_skips":      atomic.LoadInt64(&h.cachedSnapshotEmptySkips),
		"cached_snapshot_evictions":        atomic.LoadInt64(&h.cachedSnapshotEvictions),
		"cached_snapshot_entries":          cachedSnapshotEntries,
		"last_activity":                    lastActivity.UTC().Format(time.RFC3339),
		"seconds_since_activity":           time.Since(lastActivity).Seconds(),
	}
}
