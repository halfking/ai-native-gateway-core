package admin

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/stats/boardcache"
)

// SetBoardCache wires the Redis board stats cache (baseline + delta).
func (h *Handler) SetBoardCache(svc *boardcache.Service) {
	h.boardCache = svc
}

func (h *Handler) buildBoardPayload(ctx context.Context, tenantFilter string, days int, providerID int64) (map[string]any, error) {
	tr := daysToBoardTimeRange(days)
	summary, fromMinute := h.queryBoardSummary(ctx, tenantFilter, tr)
	if !fromMinute {
		summary = h.fallbackBoardSummary(ctx, tenantFilter, tr)
	}
	pies, err := h.queryBoardPies(ctx, tenantFilter, tr)
	if err != nil {
		return nil, err
	}
	trends, err := h.resolveBoardTrends(ctx, tenantFilter, tr, providerID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"summary": summary,
		"pies":    pies,
		"trends":  trends,
		"days":    days,
	}, nil
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

func attachBoardOperational(ctx context.Context, h *Handler, payload map[string]any) {
	if payload == nil {
		return
	}
	if bgTasks := h.queryBoardBackgroundTasks(ctx); bgTasks != nil {
		payload["background_tasks"] = bgTasks
	}
	if selfcheck := h.queryBoardSelfCheck(ctx); selfcheck != nil {
		payload["selfcheck"] = selfcheck
	}
}
