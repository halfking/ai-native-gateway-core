package streaming

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // same handler authentication surface
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

// ModelsHandler serves the /v1/models endpoint.
// It returns only models that have valid, active credentials.
type ModelsHandler struct {
	dbPool *pgxpool.Pool

	// keyVerifier 校验调用方。2026-10-11 起不再是可选的「装饰」——
	// AuthMiddleware 对 sk-* 故意放行（见 middleware/auth_mw.go 的 Rule 20 §2），
	// 校验责任落在各 handler 自己身上，漏掉就是无鉴权端点。
	keyVerifier requestKeyVerifier

	staleMu      sync.Mutex
	staleEntries []modelEntry
	staleAt      time.Time
}

func NewModelsHandler() *ModelsHandler {
	return &ModelsHandler{}
}

func (h *ModelsHandler) SetDB(pool *pgxpool.Pool) {
	h.dbPool = pool
}

// SetAuth wires the data-plane key verifier.
//
// 2026-10-11 生产实测的鉴权缺口（154 与 245 均复现）：
//
//	不带 Authorization                        → 401
//	Bearer sk-totally-bogus（凭空伪造）        → 200，返回全量 615 个模型
//	已吊销的 key                               → 200
//	同一 token 打 /v1/chat/completions          → 401
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
	if h.dbPool == nil {
		if entries, ok := h.lastGoodEntries(); ok {
			slog.Warn("models: no database connection, serving last-good list")
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
	h.serveFromDB(w, r)
}

// lastGoodEntries returns the cached successful result while it is inside
// the stale grace window.
func (h *ModelsHandler) lastGoodEntries() ([]modelEntry, bool) {
	h.staleMu.Lock()
	defer h.staleMu.Unlock()
	if len(h.staleEntries) == 0 || h.staleAt.IsZero() {
		return nil, false
	}
	if time.Since(h.staleAt) > modelsStaleGrace {
		return nil, false
	}
	return h.staleEntries, true
}

func (h *ModelsHandler) rememberGoodEntries(entries []modelEntry) {
	h.staleMu.Lock()
	defer h.staleMu.Unlock()
	h.staleEntries = entries
	h.staleAt = time.Now()
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

func (h *ModelsHandler) serveFromDB(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rows, err := h.dbPool.Query(ctx, `
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
		  AND p.enabled = TRUE
		ORDER BY family, mc.canonical_name
	`)
	if err != nil {
		slog.Error("models: db query failed", "error", err)
		if entries, ok := h.lastGoodEntries(); ok {
			slog.Warn("models: serving last-good list during db outage",
				"models", len(entries), "stale_for", time.Since(h.staleSnapshotAt()).Round(time.Second).String())
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
	defer rows.Close()

	models := make([]modelEntry, 0)
	for rows.Next() {
		var name, family, modality string
		var contextWindow *int
		if err := rows.Scan(&name, &family, &modality, &contextWindow); err != nil {
			h.writeDBFailure(w, fmt.Errorf("scan models: %w", err))
			return
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
		h.writeDBFailure(w, fmt.Errorf("iterate models: %w", err))
		return
	}
	h.rememberGoodEntries(models)
	h.writeEntries(w, models)
}

func (h *ModelsHandler) writeDBFailure(w http.ResponseWriter, err error) {
	slog.Error("models: db rows failed", "error", err)
	if entries, ok := h.lastGoodEntries(); ok {
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
}

func (h *ModelsHandler) staleSnapshotAt() time.Time {
	h.staleMu.Lock()
	defer h.staleMu.Unlock()
	return h.staleAt
}
