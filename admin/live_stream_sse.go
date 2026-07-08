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
	Type       string              `json:"type"`
	Timestamp  time.Time           `json:"ts"`
	Request    *LiveRequest        `json:"request,omitempty"`
	Requests   []LiveRequest       `json:"requests,omitempty"`
	Snapshot   *LiveStreamSnapshot `json:"snapshot,omitempty"`
	Delta      *LiveStreamDelta    `json:"delta,omitempty"`
	Health     *LiveStreamHealth   `json:"health,omitempty"`
	superDelta *LiveStreamDelta    `json:"-"` // attached to Delta during fanOut for super clients; not serialised directly
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
	providerCodeForSQLBody            = providerCodeForSQL
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
	Model            string   `json:"model"`             // Display name (for backward compat, may be outbound or canonical)
	CanonicalName    string   `json:"canonical_name"`    // Standard model name for aggregation
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
}

// LiveStreamConfig controls hub behaviour. Zero values are safe and
// resolve to defaults in NewLiveStreamHub.
type LiveStreamConfig struct {
	BroadcastQueueSize int
	InitialReplayLimit int
	IdleThreshold      time.Duration
	IdleTickInterval   time.Duration
	KeepaliveInterval  time.Duration
	RedisClient        *redis.Client // optional: enables 1-hour Redis cache
}

func (c *LiveStreamConfig) defaults() {
	if c.BroadcastQueueSize <= 0 {
		c.BroadcastQueueSize = 1024
	}
	if c.InitialReplayLimit <= 0 {
		c.InitialReplayLimit = 50
	}
	if c.IdleThreshold <= 0 {
		c.IdleThreshold = 60 * time.Second
	}
	if c.IdleTickInterval <= 0 {
		c.IdleTickInterval = 10 * time.Second
	}
	if c.KeepaliveInterval <= 0 {
		c.KeepaliveInterval = 25 * time.Second
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
	cachedSnapshotMu  sync.RWMutex
	cachedSnapshot    map[string]*cachedSnapshotEntry // key = tenantID
	cachedSnapshotTTL time.Duration                   // entries older than this are evicted

	// Metrics (added 2026-07-03 for monitoring)
	totalConnections    int64 // 累计连接数
	totalDisconnections int64 // 累计断开数
	authFailures        int64 // 认证失败次数
	broadcastCount      int64 // 广播消息数

	// lastHealth tracks the previous Redis health state so we only
	// broadcast a health_update envelope when the state changes.
	lastHealthMu sync.RWMutex
	lastHealth   *LiveStreamHealth
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
		db:                db,
		cfg:               cfg,
		store:             NewLiveStreamRedisStore(cfg.RedisClient),
		register:          make(chan *liveStreamClient, 16),
		unregister:        make(chan *liveStreamClient, 16),
		broadcast:         make(chan LiveRequest, cfg.BroadcastQueueSize),
		clients:           make(map[*liveStreamClient]struct{}),
		lastActivity:      time.Now(),
		stopCh:            make(chan struct{}),
		cachedSnapshot:    make(map[string]*cachedSnapshotEntry),
		cachedSnapshotTTL: 10 * time.Minute, // evict entries idle for >10min
	}
}

// Run drives the hub event loop. Blocks until Stop() is called.
func (h *LiveStreamSSEHub) Run() {
	idleTicker := time.NewTicker(h.cfg.IdleTickInterval)
	keepaliveTicker := time.NewTicker(h.cfg.KeepaliveInterval)
	cacheCleanupTicker := time.NewTicker(h.cachedSnapshotTTL)
	healthTicker := time.NewTicker(30 * time.Second) // Redis health check interval
	defer idleTicker.Stop()
	defer keepaliveTicker.Stop()
	defer cacheCleanupTicker.Stop()
	defer healthTicker.Stop()

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
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				tenantDelta = h.computeScopeDelta(ctx, tenantID, false)
				if hasSuperClient {
					superDelta = h.computeScopeDelta(ctx, "", true)
				}
				cancel()
			}
			h.fanOut(LiveStreamEnvelope{
				Type:      "request",
				Timestamp: time.Now().UTC(),
				Request:   &req,
				Delta:     tenantDelta,
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
		}
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
	snapshot, err := h.store.Snapshot(ctx, tenantID, isSuper, h.cfg.InitialReplayLimit)
	if err != nil {
		slog.Debug("live stream scope snapshot failed",
			"scope_tenant", tenantID, "is_super", isSuper, "err", err.Error())
		return nil
	}
	// Scope cache key: super scope uses a dedicated sentinel so it never
	// collides with a tenant literally named "default"/etc.
	cacheKey := tenantID
	if isSuper {
		cacheKey = "__super__"
	}
	h.cachedSnapshotMu.Lock()
	defer h.cachedSnapshotMu.Unlock()
	var cached *LiveStreamSnapshot
	if entry := h.cachedSnapshot[cacheKey]; entry != nil {
		cached = entry.snapshot
	}
	// Guard: do not let an empty snapshot overwrite a populated cache.
	// An empty read from Redis is treated as "no change" (return the
	// last delta is pointless; better to simply skip). This is what
	// stops a 200ms Redis stall from clearing every dashboard.
	if snapshot == nil || snapshot.Summary.Total == 0 {
		return nil
	}
	delta := ComputeDelta(cached, snapshot)
	h.cachedSnapshot[cacheKey] = &cachedSnapshotEntry{
		snapshot:     snapshot,
		lastAccessed: time.Now(),
	}
	return delta
}

// evictStaleCachedSnapshots removes tenant cached snapshots that have
// not been touched within cachedSnapshotTTL. Prevents unbounded memory
// growth from tenants that send a single request then go silent.
func (h *LiveStreamSSEHub) evictStaleCachedSnapshots() {
	now := time.Now()
	h.cachedSnapshotMu.Lock()
	defer h.cachedSnapshotMu.Unlock()
	for tenantID, entry := range h.cachedSnapshot {
		if now.Sub(entry.lastAccessed) > h.cachedSnapshotTTL {
			delete(h.cachedSnapshot, tenantID)
		}
	}
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
func (h *LiveStreamSSEHub) CanonicalNameFor(ctx context.Context, canonicalID int) string {
	if canonicalID == 0 {
		return ""
	}
	if h == nil || h.db == nil {
		return ""
	}

	// Check cache first
	if cached, ok := h.canonicalCache.Load(canonicalID); ok {
		return cached.(string)
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
		"openai":     "openai",
		"anthropic":  "anthropic",
		"google":     "google",
		"alibaba":    "alibaba",
		"qwen":       "alibaba",
		"zhipu":      "zhipu",
		"deepseek":   "deepseek",
		"bytedance":  "bytedance",
		"doubao":     "bytedance",
		"baidu":      "baidu",
		"moonshot":   "moonshot",
		"01ai":       "01ai",
		"baichuan":   "baichuan",
		"meta":       "meta",
		"mistral":    "mistral",
		"xiaomi":     "xiaomi",
		"microsoft":  "microsoft",
		"xai":        "xai",
		"stepfun":    "stepfun",
		"minimax":    "minimax",
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
	h.lastActivityMu.RLock()
	last := h.lastActivity
	h.lastActivityMu.RUnlock()
	if time.Since(last) < h.cfg.IdleThreshold {
		return
	}
	now := time.Now().UTC()
	if h.store != nil {
		if err := h.store.ScanAndRecordIdleMarkers(context.Background(), now); err != nil {
			slog.Debug("live stream scan and record idle markers failed", "err", err.Error(), "timestamp", now.Format(time.RFC3339))
		}
	}
	// Idle markers were just persisted to the main queues. Push the global
	// (super) delta so super-admin dashboards refresh immediately. Tenant
	// clients do not receive a delta here: idle markers are per-tenant and
	// recomputing every tenant would be expensive; each tenant's lanes will
	// refresh naturally the next time one of its requests arrives (or on
	// reconnect via initial_data). Attaching only superDelta is safe because
	// computeScopeDelta never overwrites the cache with an empty snapshot.
	var superDelta *LiveStreamDelta
	if h.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		superDelta = h.computeScopeDelta(ctx, "", true)
		cancel()
	}
	h.fanOut(LiveStreamEnvelope{
		Type:       "idle_marker",
		Timestamp:  now,
		superDelta: superDelta,
	})
	h.lastActivityMu.Lock()
	h.lastActivity = time.Now()
	h.lastActivityMu.Unlock()
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

func (h *LiveStreamSSEHub) shouldDeliver(c *liveStreamClient, env LiveStreamEnvelope) bool {
	if c.isSuper {
		return true
	}
	if env.Request != nil {
		return env.Request.TenantID == c.tenantID
	}
	return true
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

// Publish enqueues a new request for fan-out. Drops if the broadcast
// queue is full so a stuck consumer can never block the producer.
func (h *LiveStreamSSEHub) Publish(req LiveRequest) {
	if h.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if err := h.store.Record(ctx, req); err != nil {
			slog.Debug("live stream redis record failed", "request_id", req.RequestID, "tenant_id", req.TenantID, "model", req.Model, "provider", req.ProviderCode, "err", err.Error())
		}
		cancel()
	}
	select {
	case h.broadcast <- req:
	default:
		slog.Debug("live stream broadcast queue full, dropping request", "request_id", req.RequestID)
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
// Browser EventSource cannot set Authorization headers, so when
// the request comes in without a cookie (i.e. the dashboard is
// authenticating via a legacy admin api key, not the JWT login
// path) the browser's only option is to fail. To support that
// case we ALSO accept the api_key via the `?token=` query string
// — same shape as the v2 WebSocket path. The token is consumed
// only when the JWT-cookie path failed; the legacy Bearer header
// path stays untouched.
func (h *LiveStreamSSEHub) HandleLiveStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Promote ?token=… into Authorization when no Bearer header is
	// set. This is the only place we accept the query-string token;
	// every other admin route still requires a proper Authorization
	// header or a signed cookie. We do NOT log the token.
	if r.Header.Get("Authorization") == "" {
		if t := strings.TrimSpace(r.URL.Query().Get("token")); t != "" {
			slog.Debug("live stream: promoting ?token= to Authorization header")
			r.Header.Set("Authorization", "Bearer "+t)
		}
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
			if ss, ssErr := h.store.Snapshot(r.Context(), tenantID, isSuper, h.cfg.InitialReplayLimit); ssErr == nil && ss != nil && ss.Summary.Total > 0 {
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
		       COALESCE(NULLIF(rl.outbound_model, ''), rl.client_model, '') AS model,
		       COALESCE(mc.canonical_name, '') AS canonical_name,
		       COALESCE(NULLIF(p.name, ''), NULLIF(p.catalog_code, ''), NULLIF(p.code, ''), '') AS provider_code,
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
//   - Model: outboundModel → clientModel → canonical_name (from canonicalID)
//   - ModelCategory: from Model → from Provider (when model is empty)
//   - ProviderCode: already resolved by caller (credential → provider)
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
	
	// Model fallback chain: outbound → client → canonical_name
	if outboundModel != "" {
		out.Model = outboundModel
	} else if clientModel != "" {
		out.Model = clientModel
	} else if canonicalID > 0 {
		canonicalName := h.CanonicalNameFor(ctx, canonicalID)
		if canonicalName != "" {
			out.Model = canonicalName
			slog.Debug("live stream: using canonical_name as model fallback",
				"request_id", requestID, "canonical_id", canonicalID, "canonical_name", canonicalName)
		}
	}
	
	// Set CanonicalName for model dimension aggregation (always use canonical if available)
	if canonicalID > 0 {
		out.CanonicalName = h.CanonicalNameFor(ctx, canonicalID)
	}
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
	return out
}

// Stats 返回 SSE Hub 的监控指标
func (h *LiveStreamSSEHub) Stats() map[string]interface{} {
	h.mu.RLock()
	activeClients := len(h.clients)
	h.mu.RUnlock()

	h.lastActivityMu.RLock()
	lastActivity := h.lastActivity
	h.lastActivityMu.RUnlock()

	return map[string]interface{}{
		"active_clients":         activeClients,
		"total_connections":      atomic.LoadInt64(&h.totalConnections),
		"total_disconnections":   atomic.LoadInt64(&h.totalDisconnections),
		"auth_failures":          atomic.LoadInt64(&h.authFailures),
		"broadcast_count":        atomic.LoadInt64(&h.broadcastCount),
		"last_activity":          lastActivity.UTC().Format(time.RFC3339),
		"seconds_since_activity": time.Since(lastActivity).Seconds(),
	}
}
