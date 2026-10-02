package admin

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// catalogUsage 是按 gw_session_id 聚合的用量。
// 轮次只数主请求（parent 为空），与时间线主轮次一致；Token 和费用含该会话全部行。
type catalogUsage struct {
	Turns      int64
	Prompt     int64
	Completion int64
	CostUSD    float64
	Model      string
}

type catalogSearchHit struct {
	SessionID string
	Title     string
	Model     string
}

// catalogUsageSQL 从 session 族唯一事实源聚合用量：session_turns_hot（独立堆表）
// ∪ session_turns（按月分区的母表）。键 session_id 命中
// idx_session_turns_session (session_id, turn_no DESC)。
//
// 为什么不再读 request_logs（会话存储解耦 v3 S4）：
// 门控 storage.request_logs_write_enabled 关停后，request_logs_hot / request_logs
// 只剩停写之前的历史行，新会话一条都查不到，聚合恒 0。而 overlayCatalogUsage
// 只在字段为 0 时补齐 —— Redis 窗口命中的会话继续显示缓存里的旧值，未命中的
// 显示 0。同一会话显示对还是错取决于 Redis 缓存是否命中，这是最难复现的
// 不一致形态。session 族没有这个分叉：停写前后都是同一份数据。
//
// 为什么不需要保留 v1 回落腿：本函数的入参来自 Redis 会话窗口 ∪
// session_summaries，两边都是活跃/近期会话，必然落在镜像链双写窗口内。
// 镜像链启用之前的历史会话根本不会出现在这个列表里，给它加 v1 腿只会把
// 退役表重新挂回列表接口的热路径。
//
// 模型列：session_turns.model 是该轮次实际使用的模型，语义对齐原查询里
// 「优先取实际发出模型」的意图；原查询的第二兜底 client_model 在 session 族
// 属于 session_turn_details 特征层，只在需要区分「客户端请求模型 vs 实际路由
// 模型」时才读，目录列表不区分，故不引入 details 联接。
const catalogUsageSQL = `
SELECT session_id,
       COUNT(*) FILTER (WHERE COALESCE(parent_request_id, '') = '')::bigint,
       COALESCE(SUM(prompt_tokens), 0)::bigint,
       COALESCE(SUM(completion_tokens), 0)::bigint,
       COALESCE(SUM(cost_usd), 0)::float8,
       COALESCE(MAX(NULLIF(model, '')), '')
FROM (
    SELECT session_id, parent_request_id, prompt_tokens, completion_tokens,
           cost_usd, model, tenant_id
    FROM session_turns_hot
    WHERE session_id = ANY($1)
    UNION ALL
    SELECT session_id, parent_request_id, prompt_tokens, completion_tokens,
           cost_usd, model, tenant_id
    FROM session_turns
    WHERE session_id = ANY($1)
) usage_rows
WHERE ($2 = '' OR tenant_id = $2)
GROUP BY session_id`

const catalogSearchSQL = `
SELECT session_key,
       COALESCE(title, ''),
       COALESCE(primary_model, '')
FROM session_summaries
WHERE session_key IS NOT NULL
  AND btrim(session_key) <> ''
  AND (session_key ILIKE $1 ESCAPE '\'
    OR COALESCE(title, '') ILIKE $1 ESCAPE '\'
    OR COALESCE(primary_model, '') ILIKE $1 ESCAPE '\')
  AND ($2 = '' OR tenant_id = $2)
ORDER BY last_request_at DESC NULLS LAST
LIMIT $3`

// overlayCatalogUsage 只补齐仍为 0 的字段，不覆盖 Redis 里已经非 0 的计数。
func overlayCatalogUsage(item *sessionListItem, usage catalogUsage) {
	if item == nil {
		return
	}
	if item.TotalTurns == 0 && usage.Turns > 0 {
		item.TotalTurns = usage.Turns
	}
	if item.TotalPromptTokens == 0 && usage.Prompt > 0 {
		item.TotalPromptTokens = usage.Prompt
	}
	if item.TotalCompletionTokens == 0 && usage.Completion > 0 {
		item.TotalCompletionTokens = usage.Completion
	}
	if item.TotalCostUSD == 0 && usage.CostUSD > 0 {
		item.TotalCostUSD = usage.CostUSD
	}
	if strings.TrimSpace(item.CurrentModel) == "" && strings.TrimSpace(usage.Model) != "" {
		item.CurrentModel = usage.Model
	}
}

func filterCatalogItems(items []sessionListItem, q string) []sessionListItem {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return items
	}
	out := make([]sessionListItem, 0, len(items))
	for _, item := range items {
		hay := strings.ToLower(item.SessionID + " " + item.Title + " " + item.CurrentModel + " " + item.Tags)
		if strings.Contains(hay, q) {
			out = append(out, item)
		}
	}
	return out
}

func likeContains(q string) string {
	q = strings.TrimSpace(q)
	runes := []rune(q)
	if len(runes) > 80 {
		q = string(runes[:80])
	}
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + replacer.Replace(q) + "%"
}

// mergeCatalogSearchHits 把摘要命中写回行。已在列表里的会话只补空白标题和模型，
// 避免随后的字段过滤把「标题命中、行上标题仍为空」的会话丢掉。
func mergeCatalogSearchHits(items []sessionListItem, hits []catalogSearchHit, tenantID string) []sessionListItem {
	index := make(map[string]int, len(items)+len(hits))
	for i := range items {
		index[items[i].SessionID] = i
	}
	for _, hit := range hits {
		id := strings.TrimSpace(hit.SessionID)
		if id == "" {
			continue
		}
		if i, ok := index[id]; ok {
			fillBlankCatalogText(&items[i], hit)
			continue
		}
		item := sessionListItem{SessionID: id, TenantID: tenantID}
		fillBlankCatalogText(&item, hit)
		items = append(items, item)
		index[id] = len(items) - 1
	}
	return items
}

func fillBlankCatalogText(item *sessionListItem, hit catalogSearchHit) {
	if item == nil {
		return
	}
	if strings.TrimSpace(item.Title) == "" && strings.TrimSpace(hit.Title) != "" {
		item.Title = strings.TrimSpace(hit.Title)
	}
	if strings.TrimSpace(item.CurrentModel) == "" && strings.TrimSpace(hit.Model) != "" {
		item.CurrentModel = strings.TrimSpace(hit.Model)
	}
}

func (h *Handler) enrichCatalogUsage(ctx context.Context, items []sessionListItem, tenantID string) {
	if h.db == nil || len(items) == 0 {
		return
	}
	ids := make([]string, 0, len(items))
	for i := range items {
		if id := strings.TrimSpace(items[i].SessionID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	usage, err := h.queryCatalogUsage(ctx, ids, tenantID)
	if err != nil {
		slog.Warn("catalog usage query failed", "err", err, "sessions", len(ids))
		return
	}
	for i := range items {
		if u, ok := usage[items[i].SessionID]; ok {
			overlayCatalogUsage(&items[i], u)
		}
	}
}

func (h *Handler) queryCatalogUsage(ctx context.Context, ids []string, tenantID string) (map[string]catalogUsage, error) {
	rows, err := h.db.Query(ctx, catalogUsageSQL, ids, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]catalogUsage, len(ids))
	for rows.Next() {
		var id string
		var u catalogUsage
		if err := rows.Scan(&id, &u.Turns, &u.Prompt, &u.Completion, &u.CostUSD, &u.Model); err != nil {
			slog.Warn("catalog usage scan failed", "err", err)
			continue
		}
		out[id] = u
	}
	return out, rows.Err()
}

func (h *Handler) mergeCatalogSearch(ctx context.Context, items []sessionListItem, tenantID, q string, limit int) []sessionListItem {
	if h.db == nil || strings.TrimSpace(q) == "" {
		return items
	}
	if limit < 1 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	hits, err := h.queryCatalogSearchHits(ctx, tenantID, q, limit)
	if err != nil {
		slog.Warn("catalog search failed", "err", err)
		return items
	}
	before := len(items)
	items = mergeCatalogSearchHits(items, hits, tenantID)
	if h.sessionManager == nil {
		return items
	}
	for i := before; i < len(items); i++ {
		sess, err := h.sessionManager.Get(ctx, items[i].SessionID)
		if err != nil || sess == nil {
			continue
		}
		items[i].TenantID = sess.TenantID
		items[i].APIKeyID = sess.APIKeyID
		items[i].Status = defaultString(sess.Status, "")
		if strings.TrimSpace(sess.CurrentModel) != "" {
			items[i].CurrentModel = sess.CurrentModel
		}
		if strings.TrimSpace(sess.Title) != "" {
			items[i].Title = sess.Title
		}
		items[i].Tags = sess.Tags
		items[i].CreatedAt = sess.CreatedAt
		items[i].LastActive = sess.LastActive
		items[i].LastRequestAt = sess.LastRequestAt
	}
	return items
}

func (h *Handler) queryCatalogSearchHits(ctx context.Context, tenantID, q string, limit int) ([]catalogSearchHit, error) {
	rows, err := h.db.Query(ctx, catalogSearchSQL, likeContains(q), tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := make([]catalogSearchHit, 0, limit)
	for rows.Next() {
		var hit catalogSearchHit
		if err := rows.Scan(&hit.SessionID, &hit.Title, &hit.Model); err != nil {
			slog.Warn("catalog search scan failed", "err", err)
			continue
		}
		if strings.TrimSpace(hit.SessionID) == "" {
			continue
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}
