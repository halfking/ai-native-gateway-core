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

// 用量走热表 + 分区父表的会话索引。
// request_logs_with_current_month 会联 session_turns，本地单会话实测约 815ms。
// 同一聚合直接打底表约 1ms。检索不扫请求日志：前导 ILIKE 在约 232 万行上实测约 16s。
// 模型子串打在 models_used 的单个元素上。primary_model 约 33.2 万行里只有约 1500 行有值。
// 换行拼接是为了不让相邻模型名粘成一次命中。本地该形态约 173ms，仍在 2 秒超时内。
const catalogUsageSQL = `
SELECT gw_session_id,
       COUNT(*) FILTER (WHERE COALESCE(parent_request_id, '') = '')::bigint,
       COALESCE(SUM(prompt_tokens), 0)::bigint,
       COALESCE(SUM(completion_tokens), 0)::bigint,
       COALESCE(SUM(cost_usd), 0)::float8,
       COALESCE(MAX(NULLIF(outbound_model, '')), MAX(NULLIF(client_model, '')), '')
FROM (
    SELECT gw_session_id, parent_request_id, prompt_tokens, completion_tokens,
           cost_usd, outbound_model, client_model, tenant_id
    FROM request_logs_hot
    WHERE gw_session_id = ANY($1)
    UNION ALL
    SELECT gw_session_id, parent_request_id, prompt_tokens, completion_tokens,
           cost_usd, outbound_model, client_model, tenant_id
    FROM request_logs
    WHERE gw_session_id = ANY($1)
) usage_rows
WHERE ($2 = '' OR tenant_id = $2)
GROUP BY gw_session_id`

const catalogSearchSQL = `
SELECT session_key,
       COALESCE(title, ''),
       COALESCE(
         (SELECT m FROM unnest(models_used) AS m
           WHERE m ILIKE $1 ESCAPE '\'
           LIMIT 1),
         NULLIF(btrim(primary_model), ''),
         '')
FROM session_summaries
WHERE session_key IS NOT NULL
  AND btrim(session_key) <> ''
  AND (session_key ILIKE $1 ESCAPE '\'
    OR COALESCE(title, '') ILIKE $1 ESCAPE '\'
    OR COALESCE(primary_model, '') ILIKE $1 ESCAPE '\'
    OR array_to_string(models_used, chr(10)) ILIKE $1 ESCAPE '\')
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
