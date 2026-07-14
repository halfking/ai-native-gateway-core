package admin

import (
	"context"
	"strconv"
	"strings"
)

// resolveProviderPieLabels replaces numeric provider_id keys with human-readable
// provider names for dashboard board pies and error-drill slices.
func (h *Handler) resolveProviderPieLabels(ctx context.Context, items []boardPieItem) []boardPieItem {
	if len(items) == 0 {
		return items
	}

	ids := make([]int64, 0, len(items))
	seen := make(map[int64]struct{}, len(items))
	for _, item := range items {
		id, err := strconv.ParseInt(strings.TrimSpace(item.Key), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	names := map[int64]string{}
	if h != nil && h.db != nil && len(ids) > 0 {
		if lookedUp, err := h.lookupProviderDisplayNames(ctx, ids); err == nil {
			names = lookedUp
		}
	}

	out := make([]boardPieItem, len(items))
	for i, item := range items {
		out[i] = item
		id, err := strconv.ParseInt(strings.TrimSpace(item.Key), 10, 64)
		if err != nil || id <= 0 {
			if item.Key == "0" || item.Key == "__unknown__" {
				out[i].Key = "Unknown"
			}
			continue
		}
		if label, ok := names[id]; ok && label != "" {
			out[i].Key = label
		}
	}
	return out
}

func (h *Handler) lookupProviderDisplayNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	rows, err := h.db.Query(ctx, `
		SELECT id,
			COALESCE(
				NULLIF(display_name, ''),
				NULLIF(catalog_code, ''),
				NULLIF(code, ''),
				id::text
			)
		FROM providers
		WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var label string
		if err := rows.Scan(&id, &label); err != nil {
			continue
		}
		out[id] = label
	}
	return out, rows.Err()
}
