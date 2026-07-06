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
//	"initial_data" — first message after connect; carries []LiveRequest
//	"request"      — a new request completed (or transitioned to in-progress)
//	"idle_marker"  — 1 minute of silence; visual gap for the swim lane
//	"ping"         — keepalive; the EventSource ignores these
type LiveStreamEnvelope struct {
	Type      string              `json:"type"`
	Timestamp time.Time           `json:"ts"`
	Request   *LiveRequest        `json:"request,omitempty"`
	Requests  []LiveRequest       `json:"requests,omitempty"`
	Snapshot  *LiveStreamSnapshot `json:"snapshot,omitempty"`
}

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
	Model            string   `json:"model"`
	ModelCategory    string   `json:"model_category"`
	ProviderCode     string   `json:"provider_code"`
	Status           string   `json:"status"`
	LatencyMs        *int     `json:"latency_ms,omitempty"`
	PromptTokens     *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int     `json:"completion_tokens,omitempty"`
	TotalTokens      *int     `json:"total_tokens,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
	ErrorKind        *string  `json:"error_kind,omitempty"`
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

	stopCh chan struct{}

	// Metrics (added 2026-07-03 for monitoring)
	totalConnections    int64 // 累计连接数
	totalDisconnections int64 // 累计断开数
	authFailures        int64 // 认证失败次数
	broadcastCount      int64 // 广播消息数
}

// NewLiveStreamSSEHub constructs a hub. The caller MUST call Run()
// once in its own goroutine before the hub accepts traffic.
func NewLiveStreamSSEHub(db *pgxpool.Pool, cfg LiveStreamConfig) *LiveStreamSSEHub {
	cfg.defaults()
	return &LiveStreamSSEHub{
		db:           db,
		cfg:          cfg,
		store:        NewLiveStreamRedisStore(cfg.RedisClient),
		register:     make(chan *liveStreamClient, 16),
		unregister:   make(chan *liveStreamClient, 16),
		broadcast:    make(chan LiveRequest, cfg.BroadcastQueueSize),
		clients:      make(map[*liveStreamClient]struct{}),
		lastActivity: time.Now(),
		stopCh:       make(chan struct{}),
	}
}

// Run drives the hub event loop. Blocks until Stop() is called.
func (h *LiveStreamSSEHub) Run() {
	idleTicker := time.NewTicker(h.cfg.IdleTickInterval)
	keepaliveTicker := time.NewTicker(h.cfg.KeepaliveInterval)
	defer idleTicker.Stop()
	defer keepaliveTicker.Stop()

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
			var snapshot *LiveStreamSnapshot
			if h.store != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				snapshot, _ = h.store.Snapshot(ctx, req.TenantID, false, h.cfg.InitialReplayLimit)
				cancel()
			}
			h.fanOut(LiveStreamEnvelope{
				Type:      "request",
				Timestamp: time.Now().UTC(),
				Request:   &req,
				Snapshot:  snapshot,
			})
		case <-idleTicker.C:
			h.maybeEmitIdleMarker()
		case <-keepaliveTicker.C:
			h.fanOutKeepalive()
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
		return ""
	}
	if h.db == nil {
		return ""
	}
	if cached, ok := h.providerCache.Load(providerID); ok {
		return cached.(string)
	}
	var display string
	row := h.db.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(name, ''), NULLIF(catalog_code, ''), NULLIF(code, ''), '')
		FROM providers
		WHERE id = $1
	`, providerID)
	if err := row.Scan(&display); err != nil {
		h.providerCache.Store(providerID, "")
		return ""
	}
	h.providerCache.Store(providerID, display)
	return display
}

// ProviderCodeForCredential resolves the provider through the credential
// actually used by the request. This avoids displaying a stale or missing
// provider_id from the telemetry envelope.
func (h *LiveStreamSSEHub) ProviderCodeForCredential(ctx context.Context, credentialID int) string {
	if credentialID == 0 || h == nil || h.db == nil {
		return ""
	}
	cacheKey := fmt.Sprintf("cred:%d", credentialID)
	if cached, ok := h.providerCache.Load(cacheKey); ok {
		return cached.(string)
	}
	var display string
	row := h.db.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(p.name, ''), NULLIF(p.catalog_code, ''), NULLIF(p.code, ''), '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
	`, credentialID)
	if err := row.Scan(&display); err != nil {
		h.providerCache.Store(cacheKey, "")
		return ""
	}
	h.providerCache.Store(cacheKey, display)
	return display
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
		if err := h.store.RecordIdleMarker(context.Background(), now); err != nil {
			slog.Debug("live stream redis idle marker failed", "err", err.Error())
		}
	}
	h.fanOut(LiveStreamEnvelope{
		Type:      "idle_marker",
		Timestamp: now,
	})
	h.lastActivityMu.Lock()
	h.lastActivity = time.Now()
	h.lastActivityMu.Unlock()
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

// fanOut serialises a payload to every eligible client.
func (h *LiveStreamSSEHub) fanOut(env LiveStreamEnvelope) {
	h.mu.RLock()
	clients := make([]*liveStreamClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	data, err := json.Marshal(env)
	if err != nil {
		slog.Warn("live stream marshal failed", "err", err.Error())
		return
	}

	for _, c := range clients {
		if !h.shouldDeliver(c, env) {
			continue
		}
		if !h.writeEvent(c, data) {
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
			slog.Debug("live stream redis record failed", "request_id", req.RequestID, "err", err.Error())
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
			&r.ProviderCode, &r.Status, &r.LatencyMs, &r.PromptTokens,
			&r.CompletionTokens, &r.TotalTokens, &r.CostUSD, &r.ErrorKind,
		); err != nil {
			continue
		}
		r.Ts = ts.UTC().Format(time.RFC3339)
		r.ModelCategory = classifyModelCategory(r.Model)
		out = append(out, r)
	}
	return out, nil
}

// classifyModelCategory reduces a model name to one of the model family
// categories. Categories are based on the original model creators/vendors,
// dynamically grouping by most-used providers for international compatibility.
func classifyModelCategory(model string) string {
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
	case strings.Contains(m, "phi"):
		return "microsoft"
	case strings.Contains(m, "gemma"):
		return "google"
	default:
		return "other"
	}
}

// LiveRequestFromTelemetry adapts a raw RequestLogEntry into a
// LiveRequest.
func LiveRequestFromTelemetry(requestID string, ts time.Time, tenantID, clientModel, outboundModel, providerCode, status string, success bool, errorKind *string, latencyMs, promptTokens, completionTokens *int, totalTokens *int, costUSD *float64) LiveRequest {
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
	}
	if outboundModel != "" {
		out.Model = outboundModel
	} else {
		out.Model = clientModel
	}
	out.ModelCategory = classifyModelCategory(out.Model)
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
