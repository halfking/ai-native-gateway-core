package admin

// provider_endpoints.go — provider_endpoint_protocols 的 admin 管理面
// （协议自适应方案 §7 A4：该表此前无任何 CRUD，运营只能靠 SQL 建行）。
//
// Endpoints（挂在 /api/providers/{id}/ 前缀分发下，不新增 mux 注册）：
//
//	GET    /api/providers/{id}/endpoints              列出该供应商全部协议端点（含停用行）
//	PUT    /api/providers/{id}/endpoints/{protocol}   按 (provider_id, protocol) upsert
//	DELETE /api/providers/{id}/endpoints/{protocol}   删除一行（主端点不可删）
//
// 写入语义（为什么这样收敛）：
//
//   - protocol 必须落在表 CHECK 的五值之内（先过别名归一）。自适应选择器
//     'adaptive' 在这里**不接受**：端点行描述的是「上游真实提供的具体线格式」，
//     选择器不是线格式。
//   - 每个供应商至多一个主端点（部分唯一索引 provider_endpoint_protocols_one_primary）。
//     置 is_primary=true 在同一事务里先摘掉旧主端点再落新行，避免 23505。
//     **不允许把当前主端点直接降级为非主**、也不允许删除主端点：二者都会让供应商
//     落到「零主端点」状态（选择器 Stage 4 回落语义依赖有主端点），必须先提升另一行。
//   - base_url 必须是带主机的 http(s) URL；本地供应商（kind=local）复用
//     validateLocalBaseURL 的回环/私网约束，与 providers.base_url 同口径。
//   - 写后 provider.InvalidateAllCandidateCache()：候选载入把该表聚合进每个候选，
//     不失效就要等缓存 TTL 才生效。
//
// 影响面（如实）：写这张表**今天就会影响探针**——bg/node_probe.go 的
// resolveDirectTarget 无视 FF_ENDPOINT_SELECTOR 直接按协议匹配行取 base_url；
// 路由侧则要 FF_ENDPOINT_SELECTOR=true 才会消费（默认 false）。
import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
	"github.com/kaixuan/llm-gateway-go/provider"
	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

// providerEndpointProtocols 是表 CHECK（provider_endpoint_protocols_protocol_check）
// 的五个值。与 sql/objects/tables/provider_endpoint_protocols.sql 逐字一致；
// 单测 TestProviderEndpointProtocolsMatchTableCheck 钉住这一点。
var providerEndpointProtocols = []string{
	providercatalog.ProtocolOpenAICompletions,
	providercatalog.ProtocolOpenAIResponses,
	providercatalog.ProtocolAnthropicMessages,
	providercatalog.ProtocolGeminiGenerate,
	providercatalog.ProtocolOllamaNative,
}

func isProviderEndpointProtocol(p string) bool {
	for _, v := range providerEndpointProtocols {
		if v == p {
			return true
		}
	}
	return false
}

const (
	providerEndpointMinWeight = 1
	providerEndpointMaxWeight = 999
	providerEndpointMaxNotes  = 500
)

type providerEndpointWire struct {
	ID                  int64      `json:"id"`
	Protocol            string     `json:"protocol"`
	BaseURL             string     `json:"base_url"`
	IsPrimary           bool       `json:"is_primary"`
	VendorNative        *string    `json:"vendor_native"`
	Enabled             bool       `json:"enabled"`
	Weight              int        `json:"weight"`
	Notes               *string    `json:"notes"`
	HealthStatus        string     `json:"health_status"`
	HealthCheckedAt     *time.Time `json:"health_checked_at"`
	HealthLatencyMs     *int       `json:"health_latency_ms"`
	HealthError         *string    `json:"health_error"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

const providerEndpointSelectCols = `id, protocol, base_url, is_primary, vendor_native, enabled, weight, notes,
	health_status, health_checked_at, health_latency_ms, health_error, consecutive_failures,
	created_at, updated_at`

func scanProviderEndpoint(row pgx.Row) (providerEndpointWire, error) {
	var e providerEndpointWire
	var weight int16
	err := row.Scan(&e.ID, &e.Protocol, &e.BaseURL, &e.IsPrimary, &e.VendorNative, &e.Enabled, &weight, &e.Notes,
		&e.HealthStatus, &e.HealthCheckedAt, &e.HealthLatencyMs, &e.HealthError, &e.ConsecutiveFailures,
		&e.CreatedAt, &e.UpdatedAt)
	e.Weight = int(weight)
	return e, err
}

// handleProviderEndpoints 分发 /api/providers/{id}/endpoints[/{protocol}]。
// sub 是 "endpoints" 之后的剩余片段（可为空）。
func (h *Handler) handleProviderEndpoints(w http.ResponseWriter, r *http.Request, providerID int, sub string) {
	if auth := GetAuthContext(r); auth != nil && auth.Role != "super_admin" && auth.Role != "admin_key" {
		writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
		return
	}
	if h.policyDB() == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	sub = strings.Trim(sub, "/")
	if sub == "" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.listProviderEndpoints(w, r, providerID)
		return
	}
	protocol, err := providercatalog.NormalizeProviderProtocol(sub)
	if err != nil || !isProviderEndpointProtocol(protocol) {
		writeError(w, http.StatusBadRequest, "invalid endpoint protocol: "+sub)
		return
	}
	switch r.Method {
	case http.MethodPut:
		h.putProviderEndpoint(w, r, providerID, protocol)
	case http.MethodDelete:
		h.deleteProviderEndpoint(w, r, providerID, protocol)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) listProviderEndpoints(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.policyDB().Query(ctx, `
		SELECT `+providerEndpointSelectCols+`
		FROM provider_endpoint_protocols
		WHERE provider_id = $1
		ORDER BY is_primary DESC, weight DESC, id ASC
	`, providerID)
	if err != nil {
		writeInternalErr(w, "providerEndpoints.list", err)
		return
	}
	defer rows.Close()

	out := make([]providerEndpointWire, 0, 4)
	for rows.Next() {
		e, err := scanProviderEndpoint(rows)
		if err != nil {
			writeInternalErr(w, "providerEndpoints.scan", err)
			return
		}
		out = append(out, e)
	}
	// 截断的端点列表会让前端误以为「没配」，比显式失败危险。
	if err := rows.Err(); err != nil {
		writeInternalErr(w, "providerEndpoints.list.rows", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id": providerID,
		"endpoints":   out,
	})
}

type providerEndpointPutReq struct {
	BaseURL      *string `json:"base_url"`
	IsPrimary    *bool   `json:"is_primary"`
	Enabled      *bool   `json:"enabled"`
	Weight       *int    `json:"weight"`
	VendorNative *string `json:"vendor_native"`
	Notes        *string `json:"notes"`
}

// validateProviderEndpointBaseURL 返回空串表示通过。
func validateProviderEndpointBaseURL(raw string, local bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "base_url is required"
	}
	if local {
		return validateLocalBaseURL(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "base_url is not a valid URL: " + raw
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "base_url must use http(s): " + raw
	}
	return ""
}

func (h *Handler) putProviderEndpoint(w http.ResponseWriter, r *http.Request, providerID int, protocol string) {
	var req providerEndpointPutReq
	if err := jsonbody.DecodeRequest(r, &req, jsonbody.MaxRequiredBody, true); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Weight != nil && (*req.Weight < providerEndpointMinWeight || *req.Weight > providerEndpointMaxWeight) {
		writeError(w, http.StatusBadRequest, "weight must be between 1 and 999")
		return
	}
	if req.Notes != nil && len(*req.Notes) > providerEndpointMaxNotes {
		writeError(w, http.StatusBadRequest, "notes too long")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	db := h.policyDB()
	var kind string
	if err := db.QueryRow(ctx, `SELECT COALESCE(kind,'') FROM providers WHERE id = $1`, providerID).Scan(&kind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "provider not found")
			return
		}
		writeInternalErr(w, "providerEndpoints.provider", err)
		return
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "providerEndpoints.begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 现状：行是否存在 / 是否已是主端点 / 当前 base_url。
	var curID int64
	var curPrimary bool
	var curBaseURL string
	exists := true
	err = tx.QueryRow(ctx, `
		SELECT id, is_primary, base_url
		FROM provider_endpoint_protocols
		WHERE provider_id = $1 AND protocol = $2
		FOR UPDATE
	`, providerID, protocol).Scan(&curID, &curPrimary, &curBaseURL)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeInternalErr(w, "providerEndpoints.lookup", err)
			return
		}
		exists = false
	}

	baseURL := curBaseURL
	if req.BaseURL != nil {
		baseURL = strings.TrimSpace(*req.BaseURL)
	}
	if msg := validateProviderEndpointBaseURL(baseURL, isLocalKind(kind)); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	if exists && curPrimary && req.IsPrimary != nil && !*req.IsPrimary {
		writeError(w, http.StatusConflict, "cannot demote the primary endpoint; promote another endpoint first")
		return
	}
	if exists && curPrimary && req.Enabled != nil && !*req.Enabled {
		writeError(w, http.StatusConflict, "cannot disable the primary endpoint; promote another endpoint first")
		return
	}
	wantPrimary := req.IsPrimary != nil && *req.IsPrimary
	if wantPrimary && req.Enabled != nil && !*req.Enabled {
		writeError(w, http.StatusBadRequest, "a primary endpoint cannot be disabled")
		return
	}

	if wantPrimary {
		if _, err := tx.Exec(ctx, `
			UPDATE provider_endpoint_protocols
			   SET is_primary = false, updated_at = now()
			 WHERE provider_id = $1 AND is_primary = true AND protocol <> $2
		`, providerID, protocol); err != nil {
			writeInternalErr(w, "providerEndpoints.demote", err)
			return
		}
	}

	var out providerEndpointWire
	if exists {
		row := tx.QueryRow(ctx, `
			UPDATE provider_endpoint_protocols SET
				base_url      = $3,
				is_primary    = COALESCE($4, is_primary),
				enabled       = COALESCE($5, enabled),
				weight        = COALESCE($6, weight),
				vendor_native = CASE WHEN $7::boolean THEN $8 ELSE vendor_native END,
				notes         = CASE WHEN $9::boolean THEN $10 ELSE notes END,
				updated_at    = now()
			WHERE provider_id = $1 AND protocol = $2
			RETURNING `+providerEndpointSelectCols,
			providerID, protocol, baseURL, req.IsPrimary, req.Enabled, weightArg(req.Weight),
			req.VendorNative != nil, trimmedOrNil(req.VendorNative),
			req.Notes != nil, trimmedOrNil(req.Notes))
		out, err = scanProviderEndpoint(row)
	} else {
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		weight := 100
		if req.Weight != nil {
			weight = *req.Weight
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO provider_endpoint_protocols
				(provider_id, protocol, base_url, is_primary, vendor_native, enabled, weight, notes)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING `+providerEndpointSelectCols,
			providerID, protocol, baseURL, wantPrimary, trimmedOrNil(req.VendorNative), enabled, weight, trimmedOrNil(req.Notes))
		out, err = scanProviderEndpoint(row)
	}
	if err != nil {
		writeInternalErr(w, "providerEndpoints.write", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "providerEndpoints.commit", err)
		return
	}

	provider.InvalidateAllCandidateCache()
	status := http.StatusOK
	if !exists {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

func (h *Handler) deleteProviderEndpoint(w http.ResponseWriter, r *http.Request, providerID int, protocol string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var isPrimary bool
	err := h.policyDB().QueryRow(ctx, `
		SELECT is_primary FROM provider_endpoint_protocols
		WHERE provider_id = $1 AND protocol = $2
	`, providerID, protocol).Scan(&isPrimary)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		writeInternalErr(w, "providerEndpoints.delete.lookup", err)
		return
	}
	if isPrimary {
		writeError(w, http.StatusConflict, "cannot delete the primary endpoint; promote another endpoint first")
		return
	}
	tag, err := h.policyDB().Exec(ctx, `
		DELETE FROM provider_endpoint_protocols
		WHERE provider_id = $1 AND protocol = $2 AND is_primary = false
	`, providerID, protocol)
	if err != nil {
		writeInternalErr(w, "providerEndpoints.delete", err)
		return
	}
	// 上面读到的 is_primary=false 与这条 DELETE 之间没有事务：并发 PUT 可能已把这一行
	// 提升为主端点，`AND is_primary = false` 于是命中 0 行。报 200 等于告诉运营「删了」
	// 而实际没删，比显式冲突更危险。
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "cannot delete the primary endpoint; promote another endpoint first")
		return
	}
	provider.InvalidateAllCandidateCache()
	writeJSON(w, http.StatusOK, map[string]any{
		"message":     "endpoint deleted",
		"provider_id": providerID,
		"protocol":    protocol,
	})
}

// trimmedOrNil：空白串落 NULL（vendor_native / notes 可空列，空串无语义）。
func trimmedOrNil(s *string) any {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil
	}
	return v
}

// weightArg 把 *int 转成 smallint 参数（nil ⇒ SQL NULL ⇒ COALESCE 保持原值）。
func weightArg(w *int) any {
	if w == nil {
		return nil
	}
	return int16(*w)
}
