package boardcache

import "sort"

const boardPieTopN = 20

const boardPieOtherKey = "__other__"

func truncateBoardPies(board map[string]any) {
	pies, _ := board["pies"].(map[string]any)
	if pies == nil {
		return
	}
	for name, raw := range pies {
		items, ok := raw.([]any)
		if !ok || len(items) <= boardPieTopN {
			continue
		}
		pies[name] = truncatePieItems(items, boardPieTopN)
	}
}

func truncatePieItems(items []any, topN int) []any {
	if len(items) <= topN {
		return items
	}
	type row struct {
		item     map[string]any
		requests int64
	}
	rows := make([]row, 0, len(items))
	for _, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok || m == nil {
			continue
		}
		rows = append(rows, row{item: m, requests: toInt64(m["requests"])})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].requests == rows[j].requests {
			keyI, _ := rows[i].item["key"].(string)
			keyJ, _ := rows[j].item["key"].(string)
			return keyI < keyJ
		}
		return rows[i].requests > rows[j].requests
	})
	if len(rows) <= topN {
		return items
	}
	keep := rows[:topN]
	var otherReq, otherTok, otherCred int64
	var otherCost float64
	for _, r := range rows[topN:] {
		otherReq += toInt64(r.item["requests"])
		otherTok += toInt64(r.item["tokens"])
		otherCred += toInt64(r.item["credits"])
		otherCost += toFloat64(r.item["cost_usd"])
	}
	out := make([]any, 0, topN+1)
	for _, r := range keep {
		out = append(out, r.item)
	}
	if otherReq > 0 {
		out = append(out, map[string]any{
			"key":      boardPieOtherKey,
			"requests": otherReq,
			"tokens":   otherTok,
			"credits":  otherCred,
			"cost_usd": otherCost,
		})
	}
	return out
}
