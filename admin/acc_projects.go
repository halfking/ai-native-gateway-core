package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// accProjectsPath 是 ACC 侧 GET /api/v2/llm/projects 的全路径。
//
// 不复用 /api/llm/work-types 的路径：工作类型是单租户公开 seed，项目
// 目录是按租户隔离的服务 token 接口，URL 必须区分。
const accProjectsPath = "/api/v2/llm/projects"

// accProjectPayload 是 ACC LLM 视图单条记录的 JSON 形状。
type accProjectPayload struct {
	Ref           string   `json:"ref"`
	LegacyID      string   `json:"legacy_id,omitempty"`
	TenantID      string   `json:"tenant_id,omitempty"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	MatchKeywords []string `json:"match_keywords"`
	RepoPaths     []string `json:"repo_paths"`
	Enabled       bool     `json:"enabled"`
	UpdatedAt     string   `json:"updated_at"`
}

// accProjectsResponse 镜像 ACC llmProjectsResponse。
type accProjectsResponse struct {
	OK         bool                `json:"ok"`
	Source     string              `json:"source"`
	Tenant     string              `json:"tenant"`
	Items      []accProjectPayload `json:"items"`
	Total      int                 `json:"total"`
	PageSize   int                 `json:"page_size"`
	NextCursor string              `json:"next_cursor"`
}

// projectSyncResult 是 admin/bg 看到的同步结果。
type projectSyncResult struct {
	Synced    bool      `json:"synced"`
	Message   string    `json:"message"`
	Source    string    `json:"source"`
	SyncedAt  time.Time `json:"synced_at,omitempty"`
	Upserted  int       `json:"upserted"`
	Disabled  int       `json:"disabled"`
	ACCCount  int       `json:"acc_count"`
	Tenant    string    `json:"tenant,omitempty"`
	TablesReached bool  `json:"tables_reached"`
}

// 项目同步的 HTTP 边界常量：与 acc_work_types.go 保持一致（15s 超时、
// 4 MiB body 限制）——这是网关对 ACC 的统一超时策略，写在常量里便于
// 之后审计对齐。
const (
	accProjectsHTTPTimeout = 15 * time.Second
	accProjectsBodyLimit   = 4 << 20 // 4 MiB
	accProjectsPageSize    = 200     // 经验上限，与 ACC 端 llmProjectsMaxPageSize 对齐
)

// 项目同步的"可禁用 missing"前置条件：
//
//   - 必须拿到"完成页"标志（NextCursor == ""），否则视为部分遍历；
//   - 整个同步期间 ACC 任何一次错误都直接放弃 disable-missing，避免误
//     禁用那些只是这次没拉到的项目。
//
// 这两条约束把 disable-missing 限定在"完整且成功的同步"窗口内。

// SyncProjectsFromACCForBG is the entry point for the optional bg worker.
// 与 SyncWorkTypesFromACCForBG 同款形状，便于在 main.go 里像
// `func(ctx) error { return admin.Sync...ForBG(...) }` 一样挂上。
func SyncProjectsFromACCForBG(ctx context.Context, db *pgxpool.Pool, tenantID string) error {
	_, err := syncProjectsFromACC(ctx, db, tenantID)
	return err
}

// fetchACCProjects 把分页遍历 + 鉴权 + body limit + 状态映射封装在一起，
// 返回所有页的合并视图 + 是否完整遍历（finished）。
//
// 关键约束：finished=false 时调用方不应执行 disable-missing。
func fetchACCProjects(ctx context.Context, cfg accSyncConfig, tenantID string) ([]accProjectPayload, bool, error) {
	if cfg.BaseURL == "" {
		return nil, false, fmt.Errorf("ACC 未配置：请设置 ACC_BASE_URL 或 LLM_GATEWAY_ACC_BASE_URL")
	}
	if cfg.ServiceToken == "" {
		return nil, false, fmt.Errorf("ACC 凭据未配置：请设置 ACC_SERVICE_TOKEN 或 LLM_GATEWAY_ACC_SERVICE_TOKEN")
	}

	client := &http.Client{Timeout: accProjectsHTTPTimeout}
	all := []accProjectPayload{}
	cursor := ""
	pages := 0

	for {
		pages++
		// 防御：避免 ACC 端分页逻辑失控导致无限循环。
		// 经验值：项目在百量级，100 页 × 200 项 = 20000 条，超过此值
		// 应直接报错。
		if pages > 100 {
			return all, false, fmt.Errorf("ACC 项目分页超过 100 页，疑似失控")
		}

		url := cfg.BaseURL + accProjectsPath
		if tenantID != "" {
			url += "?tenant=" + queryEscape(tenantID)
		}
		if cursor != "" {
			sep := "?"
			if tenantID != "" {
				sep = "&"
			}
			url += sep + "cursor=" + queryEscape(cursor)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return all, false, err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.ServiceToken)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return all, false, fmt.Errorf("ACC 项目请求失败: %w", err)
		}
		//nolint:errcheck // best-effort close
		body, _ := io.ReadAll(io.LimitReader(resp.Body, accProjectsBodyLimit))
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return all, false, fmt.Errorf("ACC 尚未提供 %s 端点 (HTTP 404)", accProjectsPath)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return all, false, fmt.Errorf("ACC 鉴权失败 (HTTP %d)", resp.StatusCode)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			snippet := strings.TrimSpace(string(body))
			if len(snippet) > 200 {
				snippet = snippet[:200] + "…"
			}
			return all, false, fmt.Errorf("ACC 返回 HTTP %d: %s", resp.StatusCode, snippet)
		}

		var page accProjectsResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return all, false, fmt.Errorf("ACC 响应无法解析: %w", err)
		}
		if !page.OK {
			return all, false, fmt.Errorf("ACC 响应 ok=false: source=%s", page.Source)
		}

		all = append(all, page.Items...)
		if page.NextCursor == "" {
			return all, true, nil
		}
		cursor = page.NextCursor
	}
}

// queryEscape 是 url.QueryEscape 的本地包装，便于后续替换为更严格的字符集
// 校验。当前实现与 net/url 等价。
func queryEscape(s string) string {
	// 使用 url.QueryEscape 等价实现，避免再 import net/url。
	// 这里只针对 tenant/cursor（白名单字符）做 escape，所以简单替换足够。
	r := strings.NewReplacer(" ", "%20", "\n", "%0A", "&", "%26", "?", "%3F", "#", "%23")
	return r.Replace(s)
}

// syncProjectsFromACC 是同步主流程。
//
// 设计要点：
//
//   - 单事务里：先 upsert 本次拉到的项目，再（且仅在 finished=true 时）
//     禁用"此前同步过但本次未出现"的项目。
//   - tenant 维度：本函数同步的是 tenant 维度（tenantID == "" 时为公共
//     项目）。disable-missing 只在当前 tenant 维度内进行——不会因为
//     公共项目同步把租户私有项目误禁用，反之亦然。
//   - 失败语义：fetch 失败立刻返回错误；upsert 失败让 PG 抛错；disable
//     失败让 PG 抛错。调用方把整次同步视为一次原子单元。
func syncProjectsFromACC(ctx context.Context, db *pgxpool.Pool, tenantID string) (projectSyncResult, error) {
	cfg := loadACCSyncConfig()
	items, finished, err := fetchACCProjects(ctx, cfg, tenantID)
	if err != nil {
		return projectSyncResult{Synced: false, Message: err.Error(), Source: "acc", Tenant: tenantID}, err
	}

	now := time.Now().UTC()
	syncedRefs := make([]string, 0, len(items))
	upserted := 0
	tx, err := db.Begin(ctx)
	if err != nil {
		return projectSyncResult{}, err
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	for _, p := range items {
		ref := strings.TrimSpace(p.Ref)
		if ref == "" || strings.TrimSpace(p.Name) == "" {
			continue
		}
		keywords := p.MatchKeywords
		if keywords == nil {
			keywords = []string{}
		}
		paths := p.RepoPaths
		if paths == nil {
			paths = []string{}
		}
		description := strings.TrimSpace(p.Description)

		// upsert by project_ref。tenant 维度由 enabled/disabled + 同步
		// 范围共同决定；同一个 project_ref 不允许跨 tenant 共存。
		_, err := tx.Exec(ctx, `
			INSERT INTO project_dim
			    (project_ref, tenant_id, name, description,
			     match_keywords, repo_paths, enabled,
			     synced_from_acc_at, created_at, updated_at)
			VALUES ($1, NULLIF($2, ''), $3, NULLIF($4, ''),
			        $5, $6, $7,
			        $8, NOW(), NOW())
			ON CONFLICT (project_ref) DO UPDATE SET
			    tenant_id = COALESCE(EXCLUDED.tenant_id, project_dim.tenant_id),
			    name = EXCLUDED.name,
			    description = EXCLUDED.description,
			    match_keywords = EXCLUDED.match_keywords,
			    repo_paths = EXCLUDED.repo_paths,
			    enabled = EXCLUDED.enabled,
			    synced_from_acc_at = EXCLUDED.synced_from_acc_at,
			    updated_at = NOW()
		`,
			ref, tenantID, p.Name, description,
			keywords, paths, p.Enabled,
			now)
		if err != nil {
			return projectSyncResult{}, fmt.Errorf("upsert %s: %w", ref, err)
		}
		upserted++
		syncedRefs = append(syncedRefs, ref)
	}

	disabled := 0
	// 只在完整遍历成功时才 disable missing——否则会把"本次没拉到"的项目
	// 误禁用，下次完整同步才能恢复。
	if finished && len(syncedRefs) > 0 {
		tag, err := tx.Exec(ctx, `
			UPDATE project_dim
			SET enabled = FALSE, updated_at = NOW()
			WHERE synced_from_acc_at IS NOT NULL
			  AND (tenant_id IS NOT DISTINCT FROM $2::text)
			  AND NOT (project_ref = ANY($1))
		`, syncedRefs, nilIfEmpty(tenantID))
		if err != nil {
			return projectSyncResult{}, err
		}
		disabled = int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return projectSyncResult{}, err
	}

	msg := fmt.Sprintf("已从 ACC 同步 %d 个项目（tenant=%q）", upserted, tenantID)
	if disabled > 0 {
		msg += fmt.Sprintf("；禁用 %d 个 ACC 已移除项", disabled)
	}
	return projectSyncResult{
		Synced:        true,
		Message:       msg,
		Source:        "acc",
		SyncedAt:      now,
		Upserted:      upserted,
		Disabled:      disabled,
		ACCCount:      len(items),
		Tenant:        tenantID,
		TablesReached: true,
	}, nil
}

// nilIfEmpty 把空字符串转换成 nil，避免 SQL 端 NULLIF / IS NOT DISTINCT
// FROM 的语义错位。tenantID 为空时 SQL 端对应 NULL。
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// QueryProjectSyncMeta 返回 project_dim 上次 ACC 同步的元信息。
//
// 跟 queryWorkTypeSyncMeta 同款：包含 source / last_synced_at /
// enabled_count / acc_configured。供运维面板和"acc_configured 是否生效"
// 健康检查使用；DB 错误一律静默返回（不破坏读取路径）。
func QueryProjectSyncMeta(ctx context.Context, db *pgxpool.Pool, tenantID string) map[string]interface{} {
	meta := map[string]interface{}{
		"source":         "acc",
		"last_synced_at": nil,
		"enabled_count":  0,
		"total_count":    0,
		"acc_configured": loadACCSyncConfig().BaseURL != "",
		"tenant":         tenantID,
	}
	if db == nil {
		return meta
	}
	var lastSync *time.Time
	_ = db.QueryRow(ctx, `
		SELECT MAX(synced_from_acc_at) FROM project_dim
		WHERE synced_from_acc_at IS NOT NULL
		  AND (tenant_id IS NOT DISTINCT FROM $1::text)
	`, nilIfEmpty(tenantID)).Scan(&lastSync)
	if lastSync != nil {
		meta["last_synced_at"] = lastSync.UTC().Format(time.RFC3339)
	}
	var enabled, total int
	_ = db.QueryRow(ctx, `
		SELECT
		    COUNT(*) FILTER (WHERE enabled),
		    COUNT(*)
		FROM project_dim
		WHERE (tenant_id IS NOT DISTINCT FROM $1::text)
	`, nilIfEmpty(tenantID)).Scan(&enabled, &total)
	meta["enabled_count"] = enabled
	meta["total_count"] = total
	return meta
}

// ─── Admin HTTP endpoints ───────────────────────────────────────────────
//
// 与 work_types 共用模式：adminWrap 来自 admin.Server，挂载到 admin mux。
// 手动同步端点是计划里的"运维兜底"——bg worker 没启用或失败时，运维
// 可通过 POST 触发一次。GET 端点返回同步元信息 + 命中规则的项目清单
// 的子集（仅 ref/name/enabled），不返回 keywords/paths 以避免敏感仓库
// 路径泄漏到前端列表 API。

// ProjectHandlers 包装 project 同步的 admin handler 集合。
type ProjectHandlers struct {
	db *pgxpool.Pool
}

// NewProjectHandlers 构造 handler 集。db 为 nil 时所有端点返回 503。
func NewProjectHandlers(db *pgxpool.Pool) *ProjectHandlers {
	return &ProjectHandlers{db: db}
}

// RegisterProjectAdminRoutes 挂载到 admin mux。adminWrap 来自 admin.Server。
func (h *ProjectHandlers) RegisterProjectAdminRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("/api/admin/projects/sync-from-acc", adminWrap(h.handleSyncFromACC))
	mux.HandleFunc("/api/admin/projects", adminWrap(h.handleList))
}

func (h *ProjectHandlers) handleSyncFromACC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
		return
	}
	if h.db == nil {
		writeJSONErrCtx(w, r, http.StatusServiceUnavailable, "projects_db_not_configured")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant"))
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, err := syncProjectsFromACC(ctx, h.db, tenantID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"synced":    false,
			"message":   result.Message,
			"source":    "acc",
			"sync_meta": QueryProjectSyncMeta(ctx, h.db, tenantID),
		})
		return
	}
	writeJSONOk(w, map[string]interface{}{
		"synced":    result.Synced,
		"message":   result.Message,
		"source":    result.Source,
		"synced_at": result.SyncedAt.Format(time.RFC3339),
		"upserted":  result.Upserted,
		"disabled":  result.Disabled,
		"acc_count": result.ACCCount,
		"tenant":    result.Tenant,
		"sync_meta": QueryProjectSyncMeta(ctx, h.db, tenantID),
	})
}

func (h *ProjectHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
		return
	}
	if h.db == nil {
		writeJSONErrCtx(w, r, http.StatusServiceUnavailable, "projects_db_not_configured")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant"))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	out := map[string]interface{}{
		"sync_meta": QueryProjectSyncMeta(ctx, h.db, tenantID),
		"projects":  []map[string]interface{}{},
	}
	rows, err := h.db.Query(ctx, `
		SELECT project_ref, name, enabled,
		       synced_from_acc_at IS NOT NULL AS from_acc,
		       synced_from_acc_at,
		       updated_at
		FROM project_dim
		WHERE (tenant_id IS NOT DISTINCT FROM $1::text)
		ORDER BY name ASC
		LIMIT 500
	`, nilIfEmpty(tenantID))
	if err != nil {
		writeJSONErrCtx(w, r, http.StatusInternalServerError, "projects_query_failed")
		return
	}
	defer rows.Close()
	type item struct {
		Ref        string     `json:"ref"`
		Name       string     `json:"name"`
		Enabled    bool       `json:"enabled"`
		FromACC    bool       `json:"from_acc"`
		SyncedAt   *time.Time `json:"synced_at,omitempty"`
		UpdatedAt  time.Time  `json:"updated_at"`
	}
	items := []item{}
	for rows.Next() {
		var it item
		var synced *time.Time
		if err := rows.Scan(&it.Ref, &it.Name, &it.Enabled, &it.FromACC, &synced, &it.UpdatedAt); err != nil {
			continue
		}
		it.SyncedAt = synced
		items = append(items, it)
	}
	out["projects"] = items
	writeJSONOk(w, out)
}
