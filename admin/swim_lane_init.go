package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SwimLaneInitRequest 泳道初始化请求
type SwimLaneInitRequest struct {
	Hours int `json:"hours"` // 查询最近N小时的数据，默认1小时
}

// SwimLaneInitResponse 泳道初始化响应
type SwimLaneInitResponse struct {
	Requests []SwimLaneRequest `json:"requests"`
	Stats    SwimLaneStats     `json:"stats"`
}

// SwimLaneRequest 单个请求数据
type SwimLaneRequest struct {
	RequestID        string   `json:"request_id"`
	Timestamp        string   `json:"timestamp"`
	Model            string   `json:"model"`
	Vendor           string   `json:"vendor"`
	Provider         string   `json:"provider"`
	Status           string   `json:"status"`
	ErrorKind        string   `json:"error_kind,omitempty"`
	LatencyMS        *int     `json:"latency_ms,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
	PromptTokens     *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int     `json:"completion_tokens,omitempty"`
}

// SwimLaneStats 统计数据
type SwimLaneStats struct {
	TotalCount    int            `json:"total_count"`
	SuccessCount  int            `json:"success_count"`
	FailureCount  int            `json:"failure_count"`
	VendorStats   map[string]int `json:"vendor_stats"`
	ProviderStats map[string]int `json:"provider_stats"`
	ModelStats    map[string]int `json:"model_stats"`
}

// HandleSwimLaneInit 处理泳道初始化请求
// GET /api/admin/dashboard/swim-lane-init?hours=1
func (h *Handler) HandleSwimLaneInit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 解析参数
	hoursStr := r.URL.Query().Get("hours")
	hours := 1 // 默认1小时
	if hoursStr != "" {
		if h, err := parseInt(hoursStr); err == nil && h > 0 && h <= 24 {
			hours = h
		}
	}

	// 查询数据
	requests, stats, err := h.fetchSwimLaneData(ctx, hours)
	if err != nil {
		http.Error(w, "Failed to fetch swim lane data: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 返回响应
	resp := SwimLaneInitResponse{
		Requests: requests,
		Stats:    stats,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// fetchSwimLaneData 从数据库查询泳道初始化数据
func (h *Handler) fetchSwimLaneData(ctx context.Context, hours int) ([]SwimLaneRequest, SwimLaneStats, error) {
	// 查询最近N小时的请求数据
	since := time.Now().Add(-time.Duration(hours) * time.Hour)

	query := `
		SELECT 
			request_id,
			request_at,
			model,
			COALESCE(original_model_family, 'unknown') as vendor,
			COALESCE(credential_name, 'unknown') as provider,
			status,
			error_kind,
			latency_ms,
			cost_usd,
			prompt_tokens,
			completion_tokens
		FROM request_logs_default
		WHERE request_at >= $1
		ORDER BY request_at DESC
		LIMIT 500
	`

	rows, err := h.db.Query(ctx, query, since)
	if err != nil {
		return nil, SwimLaneStats{}, err
	}
	defer rows.Close()

	var requests []SwimLaneRequest
	stats := SwimLaneStats{
		VendorStats:   make(map[string]int),
		ProviderStats: make(map[string]int),
		ModelStats:    make(map[string]int),
	}

	for rows.Next() {
		var req SwimLaneRequest
		var requestAt time.Time

		err := rows.Scan(
			&req.RequestID,
			&requestAt,
			&req.Model,
			&req.Vendor,
			&req.Provider,
			&req.Status,
			&req.ErrorKind,
			&req.LatencyMS,
			&req.CostUSD,
			&req.PromptTokens,
			&req.CompletionTokens,
		)
		if err != nil {
			continue
		}

		req.Timestamp = requestAt.Format(time.RFC3339)
		requests = append(requests, req)

		// 统计
		stats.TotalCount++
		if req.Status == "success" {
			stats.SuccessCount++
		} else if req.Status == "failure" {
			stats.FailureCount++
		}

		stats.VendorStats[req.Vendor]++
		stats.ProviderStats[req.Provider]++
		stats.ModelStats[req.Model]++
	}

	return requests, stats, rows.Err()
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
