package streaming

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // same handler authentication surface
	"github.com/kaixuan/llm-gateway-go/settings"
)

// modelsStaleGrace bounds how long the last successful /v1/models result
// may be re-served after the DB query starts failing (2026-09-04
// availability work). One hour aligns with the provider package's
// candidate/reveal outage windows: clients keep discovering models while
// PostgreSQL is down instead of receiving 500s from a pure read endpoint.
const modelsStaleGrace = time.Hour

type modelEntry struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Family        string `json:"family,omitempty"`
	Modality      string `json:"modality,omitempty"`
	ContextWindow *int   `json:"context_window,omitempty"`
}

// modelsListOnlyRoutableKey is the settings_kv kill-switch (hot-reloaded)
// for the r1027 routable-models filter. Default true: /v1/models derives
// liveness from the same surface the router routes on
// (credential_model_bindings + node_probe_state), so models whose only
// credential is balance-dead / protocol-dead stop being advertised to
// clients that pick discovery models straight off this list (154
// 2026-10-14: opencode kept calling minimax-m2.7-quickspeed's fixed
// auxiliary request against a model with zero catalog rows → 400 every
// time). Flipping it to false restores the legacy model_offers semantics
// without a deploy.
const modelsListOnlyRoutableKey = "models.list_only_routable"

// modelsRoutableSQL lists models that are routable RIGHT NOW: at least one
// binding is available on an active credential of an enabled provider, and
// the pair's probe state is not failing. This mirrors the router's DB-level
// gates and UrsmlHealthyKeyReconciler's healthyCandidateSQL predicates —
// NOT model_offers.available, which is a different table the failure ladders
// never touch (the 2026-10-13 grok-4.7 incident: its only credential sat at
// 88 consecutive INSUFFICIENT_BALANCE failures while model_offers still said
// available). The node_probe_state gate keeps a balance-dead credential's
// flapping binding (probe marks it down → auto-recover re-enables it → probe
// fails again) from flickering the model back into the list: a credential
// with consecutive_failures > 0 stays excluded until a probe actually
// succeeds and zeroes the counter.
//
// 2026-10-14 第二分片（生产 403→127 复核抓到的缝隙）：bindings 只覆盖绑定级
// 死活；credentials.availability_state / quota_state 是凭据级死活
// （auth_failed / permanently_exhausted 的凭据，其 binding 仍可挂着
// available=TRUE——grok-4.7 的五个死凭据 67/68/69/70/72 全是这个形态）。
// availability_state='ready' 门与 UrsmlHealthyKeyReconciler 的
// healthyCandidateSQL 同款。
//
// ★ 2026-10-29：查询前面接 modelsFeaturedCTE（精选清单），WHERE 后面接
// modelsFeaturedPredicate（$1=true ⇒ 只出精选模型）。见 models_scope.go。
const modelsRoutableSQL = modelsFeaturedCTE + `
		SELECT DISTINCT
			mc.canonical_name,
			COALESCE(mc.family, 'unknown') AS family,
			COALESCE(mc.modality, 'text') AS modality,
			mc.context_window
		FROM models_canonical mc
		JOIN provider_models pm ON pm.canonical_id = mc.id
		JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN node_probe_state nps
		  ON nps.credential_id = cmb.credential_id
		  AND nps.raw_model_name = pm.raw_model_name
		WHERE cmb.available = TRUE
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  -- 模型级死活（2026-10-14 审计补齐）：provider_models 自身的
		  -- available / unavailable_reason 是模型级下线开关，路由侧
		  -- recommend_v2.go 与 v_routable_credential_models 视图都有，
		  -- 漏掉会让运维的下线对发现列表完全无效。
		  AND pm.available = TRUE
		  AND COALESCE(pm.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.availability_state, 'ready') = 'ready'
		  -- quota_state 有三个耗尽态（凭据级），availability_state='ready'
		  -- 并不蕴含 quota_state 非耗尽——两列各有独立 CHECK 约束，路由
		  -- 视图正是因此同时检查两者。原实现只排除了其中 1/3。
		  AND COALESCE(c.quota_state, 'ok') <> ALL (ARRAY['permanently_exhausted','balance_exhausted','periodic_exhausted'])
		  AND COALESCE(c.health_status, 'unknown') IN ('healthy', 'unknown')
		  AND COALESCE(c.trust_level, '') NOT IN ('quarantine')
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, TRUE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND (nps.credential_id IS NULL
		       OR COALESCE(nps.consecutive_failures, 0) = 0)` + modelsFeaturedPredicate + `
		ORDER BY family, mc.canonical_name
	`

// modelsLegacySQL is the pre-r1027 query (model_offers liveness), kept as
// the models.list_only_routable=false escape hatch.
//
// ★ It carries the SAME featured filter as modelsRoutableSQL: the scope is
// an orthogonal concern from the liveness source, so flipping the
// liveness switch must not silently change which models are advertised.
const modelsLegacySQL = modelsFeaturedCTE + `
		SELECT DISTINCT
			mc.canonical_name,
			COALESCE(mc.family, 'unknown') AS family,
			COALESCE(mc.modality, 'text') AS modality,
			mc.context_window
		FROM models_canonical mc
		JOIN model_offers mo ON mo.canonical_id = mc.id
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE mo.available = TRUE
		  AND c.status = 'active'
		  AND c.trust_level NOT IN ('quarantine')
		  AND p.enabled = TRUE` + modelsFeaturedPredicate + `
		ORDER BY family, mc.canonical_name
	`

// modelsQueryFor resolves which liveness source serves the discovery list:
// the router-aligned binding/probe-state query when the gate is on (default),
// the legacy model_offers query as the escape hatch.
func modelsQueryFor(listOnlyRoutable bool) string {
	if listOnlyRoutable {
		return modelsRoutableSQL
	}
	return modelsLegacySQL
}

// ModelsDB is the read surface the models endpoint needs. *pgxpool.Pool
// satisfies it; tests inject pgxmock.
type ModelsDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ModelsHandler serves the /v1/models endpoint.
// It returns only models that have valid, active credentials.
type ModelsHandler struct {
	db ModelsDB

	// keyVerifier 校验调用方。2026-10-11 起不再是可选的「装饰」——
	// AuthMiddleware 对 sk-* 故意放行（见 middleware/auth_mw.go 的 Rule 20 §2），
	// 校验责任落在各 handler 自己身上，漏掉就是无鉴权端点。
	keyVerifier requestKeyVerifier

	// staleMu guards stale.
	//
	// ★ The cache is keyed BY SCOPE (2026-10-29): the query result itself
	// depends on the scope (the featured filter lives in SQL), so a single
	// slot would let a "scope=all" request serve the "scope=featured"
	// snapshot during a DB outage — i.e. answer "here is everything" with
	// a list that is silently missing models. Each scope keeps its own
	// last-good snapshot; a scope with no snapshot gets the same explicit
	// 500 a DB-less boot gets.
	staleMu sync.Mutex
	stale   map[string]modelsStaleEntry
}

// modelsStaleEntry is one scope's last-good snapshot.
type modelsStaleEntry struct {
	entries []modelEntry
	at      time.Time
}

func NewModelsHandler() *ModelsHandler {
	return &ModelsHandler{}
}

func (h *ModelsHandler) SetDB(pool *pgxpool.Pool) {
	h.db = pool
}

// SetModelsDB injects the read surface directly; tests use this to supply a
// pgxmock pool without constructing a real *pgxpool.Pool.
func (h *ModelsHandler) SetModelsDB(q ModelsDB) {
	h.db = q
}

// SetAuth wires the data-plane key verifier.
//
// 2026-10-11 生产实测的鉴权缺口（154 与 245 均复现）：
// 不带 Authorization                        → 401
// Bearer sk-totally-bogus（凭空伪造）        → 200，返回全量 615 个模型
// 已吊销的 key                               → 200
// 同一 token 打 /v1/chat/completions          → 401
//
// 成因不是「鉴权没开」，是**委托链断了一节**：middleware/auth_mw.go 明确规定
// sk-* 由各 handler 自行调用 KeyVerifier（Rule 20 §2），本 handler 从未接上，
// 于是中间件那扇为内部调用者开的门，对外成了任意字符串即可通行。
func (h *ModelsHandler) SetAuth(kv *authentication.KeyVerifier) {
	if h == nil || kv == nil {
		return
	}
	h.keyVerifier = kv
}

// authenticate 校验 /v1/models 的调用方。
//
// 只做认证，不做限流、预算与状态门：这是纯读端点，套上数据面的 RPM/预算判定会
// 改变现有客户端的可见行为（大量客户端用 /v1/models 做模型发现）；对
// `throttled` 状态同样放行——限流管的是生成容量，在这里 429 会让客户端在限流
// 尖峰期连「有哪些模型可用」都探不到。两者都超出本修复范围，且要变更需单独
// 评估客户端兼容性。判据见 TestModelsThrottledKeyStillGetsCatalog。
func (h *ModelsHandler) authenticate(w http.ResponseWriter, r *http.Request) bool {
	if h.keyVerifier == nil || !h.keyVerifier.Enabled() {
		// lite / 本地模式没有校验器，沿用旧行为。
		return true
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		writeModelsAuthError(w, "Missing API key", "missing_key")
		return false
	}
	if _, err := h.keyVerifier.Verify(r.Context(), rawKey); err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeModelsAuthError(w, "Invalid or expired API key", "invalid_key")
		return false
	}
	return true
}

// writeModelsAuthError 用与本文件其它错误一致的 OpenAI 形状，避免客户端因为
// 换了错误结构而走不同分支。
func writeModelsAuthError(w http.ResponseWriter, message, code string) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": map[string]string{
			"message": message,
			"type":    "authentication_error",
			"code":    code,
		},
	})
}

func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authenticate(w, r) {
		return
	}
	scope, err := resolveModelsScope(r)
	if err != nil {
		writeModelsScopeError(w, err)
		return
	}
	if h.db == nil {
		if entries, ok := h.lastGoodEntries(scope); ok {
			slog.Warn("models: no database connection, serving last-good list", "scope", scope)
			h.writeEntries(w, entries)
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{
				"message": "Models service unavailable: no database connection",
				"type":    "server_error",
				"code":    "database_unavailable",
			},
		})
		return
	}
	h.serveFromDB(w, r, scope)
}

// writeModelsScopeError reports a malformed scope parameter in the same
// OpenAI-shaped error every other failure on this endpoint uses, so a client
// does not need a second error parser.
func writeModelsScopeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]string{
			"message": err.Error(),
			"type":    "invalid_request_error",
			"code":    "invalid_scope",
		},
	})
}

// geminiModelEntry is one entry of the Gemini native model list
// (docs/vendor-formats/gemini.md §模型列表; the same shape the repo's
// own Google scanner reads: domains/freediscovery/google_scanner.go
// googleModelEntry).
type geminiModelEntry struct {
	// Name carries the "models/" prefix (e.g. "models/gemini-2.5-flash").
	Name string `json:"name"`
	// DisplayName is the human-facing name.
	DisplayName string `json:"displayName"`
	// SupportedGenerationMethods lists the actions this gateway can
	// actually serve for the model.
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	// InputTokenLimit is the model's context window, when known.
	InputTokenLimit *int `json:"inputTokenLimit,omitempty"`
}

// ServeGeminiModels serves the model list in Google's NATIVE Gemini
// shape, for native Gemini clients.
//
// ★ Why a second shape rather than "the OpenAI one": a native Gemini
// client (google-genai SDK) parses `{"models":[{"name":"models/…"}]}`
// (docs/vendor-formats/gemini.md §模型列表). Handing it the OpenAI
// `{"object":"list","data":[…]}` shape would fail at the client's
// unmarshal step, far away from the cause.
//
// ★ Same DATA SOURCE as the OpenAI list (the routable-model query) —
// the gateway advertises exactly the models it can route; only the wire
// shape differs. Two data sources would be two truths about "what can
// this gateway serve".
//
// ★ The supported generation methods are the ones this gateway ACTUALLY
// serves for a Gemini upstream: generateContent, streamGenerateContent and
// countTokens.
//
// ★★ 2026-10-29（Gemini countTokens 轮）：countTokens **从「不列」变成「列」**
// —— 上一轮刻意不列它，理由是「网关没实现 ⇒ 列一个自己做不到的方法 =
// 给客户端一个它会当真、然后失败的承诺」。⇒ 本轮**把它实现了**
// （domains/streaming/handler_gemini.go: serveCountTokens）⇒ ⇒ 承诺兑现了
// ⇒ ⇒ **现在必须列**：仍然不列 = 网关明明做得到却对外说做不到 ⇒ 客户端
// 会去问厂商要（而厂商那边要另一次凭证/网络往返）⇒ ⇒ **能力清单必须与
// 实现逐项对账，两个方向都对**。
func (h *ModelsHandler) ServeGeminiModels(w http.ResponseWriter, r *http.Request) {
	if !h.authenticate(w, r) {
		return
	}
	scope, err := resolveModelsScope(r)
	if err != nil {
		writeModelsScopeError(w, err)
		return
	}
	if h.db == nil {
		if entries, ok := h.lastGoodEntries(scope); ok {
			slog.Warn("models: no database connection, serving last-good list (gemini shape)", "scope", scope)
			h.writeGeminiEntries(w, entries)
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{
				"message": "Models service unavailable: no database connection",
				"type":    "server_error",
				"code":    "database_unavailable",
			},
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	entries, err := h.loadScopedEntries(ctx, scope)
	if err != nil {
		slog.Error("models: db query failed (gemini shape)", "error", err, "scope", scope)
		if lastGood, ok := h.lastGoodEntries(scope); ok {
			slog.Warn("models: serving last-good list during db outage (gemini shape)",
				"scope", scope, "models", len(lastGood),
				"stale_for", time.Since(h.staleSnapshotAt(scope)).Round(time.Second).String())
			h.writeGeminiEntries(w, lastGood)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"message": "Failed to query models from database",
				"type":    "server_error",
				"code":    "database_query_error",
			},
		})
		return
	}
	h.rememberGoodEntries(scope, entries)
	h.writeGeminiEntries(w, entries)
}

// writeGeminiEntries renders the model list in the Gemini native shape.
func (h *ModelsHandler) writeGeminiEntries(w http.ResponseWriter, entries []modelEntry) {
	// Serve a defensive copy so a concurrent refresh cannot race the
	// serializer.
	out := make([]geminiModelEntry, 0, len(entries))
	for _, e := range entries {
		name := e.ID
		if name == "" {
			continue
		}
		// The native shape carries a "models/" prefixed resource name.
		if !strings.HasPrefix(name, "models/") {
			name = "models/" + name
		}
		out = append(out, geminiModelEntry{
			Name:                       name,
			DisplayName:                e.ID,
			SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent", "countTokens"},
			InputTokenLimit:            e.ContextWindow,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

// lastGoodEntries returns the cached successful result for the given scope
// while it is inside the stale grace window.
func (h *ModelsHandler) lastGoodEntries(scope string) ([]modelEntry, bool) {
	h.staleMu.Lock()
	defer h.staleMu.Unlock()
	e, ok := h.stale[scope]
	if !ok || len(e.entries) == 0 || e.at.IsZero() {
		return nil, false
	}
	if time.Since(e.at) > modelsStaleGrace {
		return nil, false
	}
	return e.entries, true
}

func (h *ModelsHandler) rememberGoodEntries(scope string, entries []modelEntry) {
	h.staleMu.Lock()
	defer h.staleMu.Unlock()
	if h.stale == nil {
		h.stale = make(map[string]modelsStaleEntry, 2)
	}
	h.stale[scope] = modelsStaleEntry{entries: entries, at: time.Now()}
}

func (h *ModelsHandler) writeEntries(w http.ResponseWriter, entries []modelEntry) {
	// Serve a defensive copy so a concurrent refresh cannot race the
	// serializer.
	out := make([]modelEntry, len(entries))
	copy(out, entries)
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   out,
	})
}

func (h *ModelsHandler) serveFromDB(w http.ResponseWriter, r *http.Request, scope string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	models, err := h.loadScopedEntries(ctx, scope)
	if err != nil {
		slog.Error("models: db query failed", "error", err, "scope", scope)
		if entries, ok := h.lastGoodEntries(scope); ok {
			slog.Warn("models: serving last-good list during db outage",
				"scope", scope, "models", len(entries),
				"stale_for", time.Since(h.staleSnapshotAt(scope)).Round(time.Second).String())
			h.writeEntries(w, entries)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"message": "Failed to query models from database",
				"type":    "server_error",
				"code":    "database_query_error",
			},
		})
		return
	}
	h.rememberGoodEntries(scope, models)
	h.writeEntries(w, models)
}

// loadScopedEntries loads the model list for the requested scope.
//
// ★ The featured scope falls back to the FULL list when the curated set
// yields nothing. Two very different situations produce that emptiness:
// the operator's featured list is empty (or the routing_policy row is
// missing), which is a configuration gap; or the featured models are all
// currently unroutable.
//
// In both cases the answer "here is everything we can route" is the right
// one, NOT an empty catalog: a discovery endpoint that answers "no models"
// breaks every client that reads it (model pickers, SDK health checks,
// tooling that picks a default model). The fallback is logged at WARN so the
// configuration gap stays VISIBLE instead of silent — the same discipline
// the repo applies to a silent scope downgrade anywhere else.
//
// ★ The fallback is not a silent scope change: it is a WARN + a second
// query, and the snapshot is cached under the REQUESTED scope so an outage
// later serves the same answer the live path would have served.
func (h *ModelsHandler) loadScopedEntries(ctx context.Context, scope string) ([]modelEntry, error) {
	featuredOnly := modelsScopeFeaturedOnly(scope)
	entries, err := h.loadEntries(ctx, featuredOnly)
	if err != nil {
		return nil, err
	}
	if featuredOnly && len(entries) == 0 {
		slog.Warn("models: featured scope produced no models; serving the full routable list",
			"scope", scope,
			"reason", "featured set is empty, or none of the featured models are currently routable")
		full, err := h.loadEntries(ctx, false)
		if err != nil {
			return nil, err
		}
		return full, nil
	}
	return entries, nil
}

// loadEntries runs the routable-model query and materializes the rows.
//
// ★ featuredOnly is passed as the query's $1 parameter (the featured filter
// lives in SQL, see models_scope.go) rather than being applied here: the
// canonical name alone cannot express the match, because the curated list may
// name a model by its PROVIDER-side name (raw_model_name /
// standardized_name) — and the router resolves featured models exactly that
// way (bg/shared_pick.go). Filtering on the canonical name only would hide a
// model the router treats as featured.
//
// ★ Extracted so the OpenAI-shaped list and the Gemini-native list share
// ONE data source: "which models can this gateway serve" must have a
// single answer, and only the wire shape may differ.
func (h *ModelsHandler) loadEntries(ctx context.Context, featuredOnly bool) ([]modelEntry, error) {
	// r1027: liveness source follows the router (bindings + probe state),
	// not model_offers. models.list_only_routable=false restores the legacy
	// query without a deploy.
	rows, err := h.db.Query(ctx, modelsQueryFor(settings.GetPlatformBool(modelsListOnlyRoutableKey, true)), featuredOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	models := make([]modelEntry, 0)
	for rows.Next() {
		var name, family, modality string
		var contextWindow *int
		if err := rows.Scan(&name, &family, &modality, &contextWindow); err != nil {
			return nil, fmt.Errorf("scan models: %w", err)
		}
		models = append(models, modelEntry{
			ID:            name,
			Object:        "model",
			Family:        family,
			Modality:      modality,
			ContextWindow: contextWindow,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate models: %w", err)
	}
	return models, nil
}

func (h *ModelsHandler) staleSnapshotAt(scope string) time.Time {
	h.staleMu.Lock()
	defer h.staleMu.Unlock()
	return h.stale[scope].at
}
