package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/registry"
)

// PolicyAPI handles tenant tool policy admin endpoints (Phase 3.4)
type PolicyAPI struct {
	db           *pgxpool.Pool
	toolRegistry *registry.ToolRegistry
}

// NewPolicyAPI creates a new PolicyAPI instance
func NewPolicyAPI(db *pgxpool.Pool, tr *registry.ToolRegistry) *PolicyAPI {
	return &PolicyAPI{
		db:           db,
		toolRegistry: tr,
	}
}

// PolicyRequest represents a policy creation/update request
type PolicyRequest struct {
	TenantID    string `json:"tenant_id"`
	ToolPattern string `json:"tool_pattern"`
	PolicyType  string `json:"policy_type"` // "allow" or "deny"
	Reason      string `json:"reason"`
	CreatedBy   string `json:"created_by"`
}

// HandleCreate handles POST /api/admin/policies
// Creates a new tenant tool policy
func (api *PolicyAPI) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if api.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "Invalid request body",
			"error":   err.Error(),
		})
		return
	}

	// Validate
	if req.TenantID == "" || req.ToolPattern == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "tenant_id and tool_pattern are required",
		})
		return
	}
	if req.PolicyType != "allow" && req.PolicyType != "deny" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "policy_type must be 'allow' or 'deny'",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	query := `
		INSERT INTO tenant_tool_policies 
			(tenant_id, tool_pattern, policy_type, reason, created_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (tenant_id, tool_pattern)
		DO UPDATE SET 
			policy_type = EXCLUDED.policy_type,
			reason = EXCLUDED.reason,
			enabled = TRUE,
			updated_at = NOW()
		RETURNING id, created_at, updated_at
	`

	var id int64
	var createdAt, updatedAt time.Time
	err := api.db.QueryRow(ctx, query,
		req.TenantID, req.ToolPattern, req.PolicyType, req.Reason, req.CreatedBy,
	).Scan(&id, &createdAt, &updatedAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Failed to create policy",
			"error":   err.Error(),
		})
		return
	}

	// Reload cache
	if api.toolRegistry != nil {
		_ = api.toolRegistry.Reload(ctx)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":     "ok",
		"id":         id,
		"tenant_id":  req.TenantID,
		"created_at": createdAt,
		"updated_at": updatedAt,
	})
}

// HandleList handles GET /api/admin/policies?tenant_id=
// Lists policies for a tenant
func (api *PolicyAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	if api.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tenantID := r.URL.Query().Get("tenant_id")
	if tenantID == "" {
		tenantID = "default"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	query := `
		SELECT id, tenant_id, tool_pattern, policy_type, reason, enabled, created_at, updated_at, created_by
		FROM tenant_tool_policies
		WHERE tenant_id = $1
		ORDER BY created_at DESC
	`

	rows, err := api.db.Query(ctx, query, tenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Failed to list policies",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()

	policies := []map[string]any{}
	for rows.Next() {
		var (
			id                              int64
			tid, pattern, ptype, createdBy  string
			reason                          *string
			enabled                         bool
			createdAt, updatedAt            time.Time
		)
		err := rows.Scan(&id, &tid, &pattern, &ptype, &reason, &enabled, &createdAt, &updatedAt, &createdBy)
		if err != nil {
			continue
		}
		policies = append(policies, map[string]any{
			"id":           id,
			"tenant_id":    tid,
			"tool_pattern": pattern,
			"policy_type":  ptype,
			"reason":       reason,
			"enabled":      enabled,
			"created_at":   createdAt,
			"updated_at":   updatedAt,
			"created_by":   createdBy,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"policies":  policies,
		"count":     len(policies),
		"tenant_id": tenantID,
	})
}

// HandleDelete handles DELETE /api/admin/policies?id=
// Deletes a policy by ID
func (api *PolicyAPI) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if api.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "id parameter is required",
		})
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "id must be a number",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := api.db.Exec(ctx, "DELETE FROM tenant_tool_policies WHERE id = $1", id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Failed to delete policy",
			"error":   err.Error(),
		})
		return
	}

	// Reload cache
	if api.toolRegistry != nil {
		_ = api.toolRegistry.Reload(ctx)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"deleted_id":  id,
		"rows_affected": tag.RowsAffected(),
	})
}

// HandleCheck handles GET /api/admin/policies/check?tenant_id=&tool_id=
// Checks if a tenant is allowed to use a tool (for testing)
func (api *PolicyAPI) HandleCheck(w http.ResponseWriter, r *http.Request) {
	if api.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tenantID := r.URL.Query().Get("tenant_id")
	toolID := r.URL.Query().Get("tool_id")

	if toolID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "tool_id parameter is required",
		})
		return
	}

	if api.toolRegistry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "error",
			"message": "Tool registry not available",
		})
		return
	}

	allowed, reason := api.toolRegistry.IsAllowed(tenantID, toolID)

	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": tenantID,
		"tool_id":   toolID,
		"allowed":   allowed,
		"reason":    reason,
	})
}

// =============================================================================
// Usage Statistics API (Phase 3.3)
// =============================================================================

// UsageStatsAPI handles tool usage statistics endpoints (Phase 3.3)
type UsageStatsAPI struct {
	db *pgxpool.Pool
}

// NewUsageStatsAPI creates a new UsageStatsAPI instance
func NewUsageStatsAPI(db *pgxpool.Pool) *UsageStatsAPI {
	return &UsageStatsAPI{db: db}
}

// HandleStats handles GET /api/admin/tools/stats?tool_id=&tenant_id=&days=7
// Returns tool usage statistics
func (api *UsageStatsAPI) HandleStats(w http.ResponseWriter, r *http.Request) {
	if api.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	toolID := r.URL.Query().Get("tool_id")
	tenantID := r.URL.Query().Get("tenant_id")
	daysStr := r.URL.Query().Get("days")

	days := 7
	if daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 && d <= 365 {
			days = d
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	query := `
		SELECT tool_id, tenant_id, usage_date, call_count, success_count, error_count, avg_latency_ms, last_called_at
		FROM tool_usage_stats
		WHERE usage_date >= CURRENT_DATE - $1::int
	`
	args := []interface{}{days}

	if toolID != "" {
		query += fmt.Sprintf(" AND tool_id = $%d", len(args)+1)
		args = append(args, toolID)
	}
	if tenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", len(args)+1)
		args = append(args, tenantID)
	}

	query += " ORDER BY usage_date DESC, call_count DESC LIMIT 1000"

	rows, err := api.db.Query(ctx, query, args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Failed to query stats",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()

	stats := []map[string]any{}
	for rows.Next() {
		var (
			tid, ttenantID                  string
			usageDate                       time.Time
			callCount, successCount, errCount int64
			avgLatency                      int
			lastCalled                      *time.Time
		)
		err := rows.Scan(&tid, &ttenantID, &usageDate, &callCount, &successCount, &errCount, &avgLatency, &lastCalled)
		if err != nil {
			continue
		}
		entry := map[string]any{
			"tool_id":       tid,
			"tenant_id":     ttenantID,
			"usage_date":    usageDate,
			"call_count":    callCount,
			"success_count": successCount,
			"error_count":   errCount,
			"avg_latency_ms": avgLatency,
		}
		if lastCalled != nil {
			entry["last_called_at"] = *lastCalled
		}
		stats = append(stats, entry)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stats":     stats,
		"count":     len(stats),
		"days":      days,
		"tool_id":   toolID,
		"tenant_id": tenantID,
	})
}

// HandleTopTools handles GET /api/admin/tools/top?tenant_id=&limit=10&days=7
// Returns most used tools
func (api *UsageStatsAPI) HandleTopTools(w http.ResponseWriter, r *http.Request) {
	if api.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tenantID := r.URL.Query().Get("tenant_id")
	limitStr := r.URL.Query().Get("limit")
	daysStr := r.URL.Query().Get("days")

	limit := 10
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}
	days := 7
	if daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 && d <= 365 {
			days = d
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	query := `
		SELECT tool_id, tenant_id, 
		       SUM(call_count) as total_calls,
		       SUM(success_count) as total_success,
		       SUM(error_count) as total_error,
		       AVG(avg_latency_ms)::int as avg_latency
		FROM tool_usage_stats
		WHERE usage_date >= CURRENT_DATE - $1::int
	`
	args := []interface{}{days}

	if tenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", len(args)+1)
		args = append(args, tenantID)
	}

	query += fmt.Sprintf(" GROUP BY tool_id, tenant_id ORDER BY total_calls DESC LIMIT $%d", len(args)+1)
	args = append(args, limit)

	rows, err := api.db.Query(ctx, query, args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Failed to query top tools",
			"error":   err.Error(),
		})
		return
	}
	defer rows.Close()

	tools := []map[string]any{}
	for rows.Next() {
		var (
			tid, ttenantID            string
			calls, success, errCount  int64
			avgLatency                int
		)
		err := rows.Scan(&tid, &ttenantID, &calls, &success, &errCount, &avgLatency)
		if err != nil {
			continue
		}
		tools = append(tools, map[string]any{
			"tool_id":        tid,
			"tenant_id":      ttenantID,
			"call_count":     calls,
			"success_count":  success,
			"error_count":    errCount,
			"avg_latency_ms": avgLatency,
			"success_rate":   fmt.Sprintf("%.2f%%", float64(success)/float64(calls)*100),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tools":     tools,
		"count":     len(tools),
		"days":      days,
		"limit":     limit,
		"tenant_id": tenantID,
	})
}

