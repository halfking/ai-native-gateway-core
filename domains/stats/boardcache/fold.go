package boardcache

import (
	"context"
	"log/slog"
	"strconv"
	"time"
)

func (s *Service) foldScope(ctx context.Context, scope Scope) {
	buckets, err := s.listDirtyBuckets(ctx, scope)
	if err != nil || len(buckets) == 0 {
		return
	}
	for _, days := range BoardDaysPresets {
		base, since, ok := s.loadBaseline(ctx, scope, days)
		if !ok {
			continue
		}
		board := cloneMap(base)
		agg := summaryCounters{}
		dimAgg := map[string]map[string]map[string]float64{}
		for _, bucketID := range buckets {
			if bucketBeforeSince(since, bucketID) {
				continue
			}
			fields, err := s.readDeltaHash(ctx, scope, bucketID)
			if err != nil || len(fields) == 0 {
				continue
			}
			mergeDeltaFields(&agg, dimAgg, fields)
		}
		applySummaryDelta(board, agg)
		applyPieDelta(board, dimAgg)
		applyTrendDelta(board, agg)
		meta := map[string]string{
			"folded_at": time.Now().UTC().Format(time.RFC3339),
			"fold_unit": foldUnit(),
			"source":    "redis_baseline_delta",
			"scope":     string(scope),
		}
		if err := s.putBoard(ctx, scope, days, 0, board, meta); err != nil {
			slog.Warn("boardcache fold put failed", "scope", scope, "days", days, "error", err)
		}
	}
	for _, bucketID := range buckets {
		s.clearDirtyBucket(ctx, scope, bucketID)
	}
}

func bucketBeforeSince(since time.Time, bucketID string) bool {
	if since.IsZero() {
		return false
	}
	if foldUnit() == "minute" {
		t, err := time.Parse("200601021504", bucketID)
		if err != nil {
			return false
		}
		return t.Before(since.Truncate(time.Minute))
	}
	sec, err := strconv.ParseInt(bucketID, 10, 64)
	if err != nil || sec == 0 {
		return false
	}
	return time.Unix(sec, 0).Before(since)
}

func mergeDeltaFields(agg *summaryCounters, dimAgg map[string]map[string]map[string]float64, fields map[string]string) {
	for k, v := range fields {
		switch k {
		case fieldReq:
			agg.Requests += parseInt64(v)
		case fieldSuccess:
			agg.Success += parseInt64(v)
		case fieldFailure:
			agg.Failure += parseInt64(v)
		case fieldPrompt:
			agg.PromptTokens += parseInt64(v)
		case fieldCompletion:
			agg.CompletionTokens += parseInt64(v)
		case fieldTotalTok:
			agg.TotalTokens += parseInt64(v)
		case fieldCredits:
			agg.Credits += parseInt64(v)
		case fieldLatency:
			agg.LatencyMsSum += parseInt64(v)
		case fieldCostUSD:
			agg.CostUSD += parseFloat64(v)
		default:
			dimType, dimKey, metric, ok := parseDimField(k)
			if !ok {
				continue
			}
			pieKey, ok := dimPieKey[dimType]
			if !ok {
				continue
			}
			if dimAgg[pieKey] == nil {
				dimAgg[pieKey] = map[string]map[string]float64{}
			}
			if dimAgg[pieKey][dimKey] == nil {
				dimAgg[pieKey][dimKey] = map[string]float64{}
			}
			dimAgg[pieKey][dimKey][metric] += parseFloat64(v)
		}
	}
}

func applySummaryDelta(board map[string]any, d summaryCounters) {
	if d.Requests == 0 {
		return
	}
	summary, _ := board["summary"].(map[string]any)
	if summary == nil {
		summary = map[string]any{}
		board["summary"] = summary
	}
	prevTotal := toInt64(summary["total_requests"])
	addInt(summary, "total_requests", d.Requests)
	addInt(summary, "total_prompt_tokens", d.PromptTokens)
	addInt(summary, "total_completion_tokens", d.CompletionTokens)
	addInt(summary, "total_tokens", d.TotalTokens)
	addInt(summary, "total_credits_charged", d.Credits)
	addFloat(summary, "total_cost_usd", d.CostUSD)
	total := toInt64(summary["total_requests"])
	prevSuccess := int64(toFloat64(summary["success_rate"]) * float64(prevTotal))
	if total > 0 {
		summary["success_rate"] = float64(prevSuccess+d.Success) / float64(total)
	}
	prevLat := int64(toFloat64(summary["avg_latency_ms"]) * float64(prevTotal))
	if total > 0 {
		summary["avg_latency_ms"] = float64(prevLat+d.LatencyMsSum) / float64(total)
	}
}

func applyPieDelta(board map[string]any, dimAgg map[string]map[string]map[string]float64) {
	pies, _ := board["pies"].(map[string]any)
	if pies == nil {
		pies = map[string]any{}
		board["pies"] = pies
	}
	for pieName, keys := range dimAgg {
		items, _ := pies[pieName].([]any)
		index := map[string]int{}
		for i, raw := range items {
			m, _ := raw.(map[string]any)
			if m == nil {
				continue
			}
			if k, ok := m["key"].(string); ok {
				index[k] = i
			}
		}
		for dimKey, metrics := range keys {
			req := int64(metrics["requests"])
			if req == 0 {
				continue
			}
			if idx, ok := index[dimKey]; ok {
				m, _ := items[idx].(map[string]any)
				addInt(m, "requests", req)
				addInt(m, "tokens", int64(metrics["tokens"]))
				addInt(m, "credits", int64(metrics["credits"]))
				addFloat(m, "cost_usd", metrics["cost_usd"])
				continue
			}
			items = append(items, map[string]any{
				"key":      dimKey,
				"requests": req,
				"tokens":   int64(metrics["tokens"]),
				"credits":  int64(metrics["credits"]),
				"cost_usd": metrics["cost_usd"],
			})
		}
		pies[pieName] = items
	}
}

func applyTrendDelta(board map[string]any, d summaryCounters) {
	if d.Requests == 0 {
		return
	}
	raw, ok := board["trends"].([]any)
	if !ok || len(raw) == 0 {
		board["trends"] = []any{newTrendPoint(time.Now().UTC(), d)}
		return
	}
	last, ok := raw[len(raw)-1].(map[string]any)
	if !ok {
		return
	}
	bucketStr, _ := last["bucket"].(string)
	lastBucket, err := time.Parse(time.RFC3339, bucketStr)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	same := now.Sub(lastBucket) < time.Hour
	if foldUnit() == "minute" {
		same = lastBucket.Truncate(time.Minute).Equal(now.Truncate(time.Minute))
	}
	if same {
		addInt(last, "requests", d.Requests)
		addInt(last, "tokens", d.TotalTokens)
		addInt(last, "credits", d.Credits)
		addFloat(last, "cost_usd", d.CostUSD)
		return
	}
	board["trends"] = append(raw, newTrendPoint(now, d))
}

func newTrendPoint(ts time.Time, d summaryCounters) map[string]any {
	return map[string]any{
		"bucket":   ts.UTC().Format(time.RFC3339),
		"requests": d.Requests,
		"tokens":   d.TotalTokens,
		"credits":  d.Credits,
		"cost_usd": d.CostUSD,
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCloneJSON(v)
	}
	return out
}

func deepCloneJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		c := make(map[string]any, len(t))
		for k, val := range t {
			c[k] = deepCloneJSON(val)
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i, val := range t {
			c[i] = deepCloneJSON(val)
		}
		return c
	default:
		return v
	}
}

func addInt(m map[string]any, key string, delta int64) {
	m[key] = toInt64(m[key]) + delta
}

func addFloat(m map[string]any, key string, delta float64) {
	m[key] = toFloat64(m[key]) + delta
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}
