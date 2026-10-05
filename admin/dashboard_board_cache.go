package admin

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/stats"
	"github.com/kaixuan/llm-gateway-go/domains/stats/boardcache"
)

// SetBoardCache wires the Redis board stats cache (baseline + delta).
func (h *Handler) SetBoardCache(svc *boardcache.Service) {
	h.boardCache = svc
}

// SetBodySizeTracker wires the Redis body size tracker for real-time statistics.
// 2026-07-25: Added to support request/response body size monitoring on dashboard.
func (h *Handler) SetBodySizeTracker(tracker *stats.BodySizeTracker) {
	h.bodySizeTracker = tracker
}

func (h *Handler) buildBoardPayload(ctx context.Context, tenantFilter string, days int, providerID int64) (map[string]any, error) {
	tr := daysToBoardTimeRange(days)
	summary, fromMinute := h.queryBoardSummary(ctx, tenantFilter, tr)
	if !fromMinute {
		// 2026-10-03：回退失败不再被吞掉。非 42P01 上抛（超时就是超时，
		// 把它说成「0 请求」是更坏的谎）；42P01 已在载荷里带好 degraded。
		fb, fbErr := h.fallbackBoardSummary(ctx, tenantFilter, tr)
		if fbErr != nil {
			return nil, fbErr
		}
		summary = fb
	}
	pies, piesDegraded, err := h.resolveBoardPies(ctx, tenantFilter, tr)
	if err != nil {
		pies = emptyBoardPies()
		piesDegraded = boardPieDegradation{
			Keys:        []string{"clients", "client_ips", "identity_hashes", "models", "errors", "tenants", "providers"},
			MissingView: boardMissingViewName(err),
		}
	}
	trends, err := h.resolveBoardTrends(ctx, tenantFilter, tr, providerID)
	if err != nil {
		trends = []boardTrendPoint{}
	}
	payload := map[string]any{
		"summary": summary,
		"pies":    pies,
		"trends":  trends,
		"days":    days,
	}
	// 缓存路径与直查路径必须写同一套降级字段：这份 payload 会被塞进 Redis
	// 再读回来（handleDashboardBoard 的 boardCache 分支直接原样返回），
	// 只在一侧打标记 = 换个入口标记就消失。
	applyBoardDegradation(payload, piesDegraded, err)
	return payload, nil
}

// BuildBoardBaseline is the PostgreSQL baseline builder for boardcache.Service.
func (h *Handler) BuildBoardBaseline(ctx context.Context, tenantFilter string, days int, providerID int64) (map[string]any, error) {
	return h.buildBoardPayload(ctx, tenantFilter, days, providerID)
}

func (h *Handler) buildErrorDrillMaps(ctx context.Context, tenantFilter string, days int, errorKind, dimension string) ([]map[string]any, error) {
	items, err := h.queryErrorDrill(ctx, tenantFilter, days, errorKind, dimension)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"key":      it.Key,
			"requests": it.Requests,
			"tokens":   it.Tokens,
			"credits":  it.Credits,
			"cost_usd": it.CostUSD,
		})
	}
	return out, nil
}

func boardScopeForTenant(tenantID string) boardcache.Scope {
	if tenantID == "" {
		return boardcache.ScopeGlobal
	}
	return boardcache.ScopeTenant(tenantID)
}
