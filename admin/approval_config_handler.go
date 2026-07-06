package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
)

// ConfigManagerService 定义 ApprovalConfigHandler 依赖的配置管理接口。
// 使用接口而非具体类型，便于测试注入 mock。
type ConfigManagerService interface {
	GetConfig(ctx context.Context, tenantID string) (*approval.ApprovalConfig, error)
	UpdateConfig(ctx context.Context, tenantID string, config *approval.ApprovalConfig) error
	GetApprovers(ctx context.Context, tenantID string) ([]approval.Approver, error)
	AddApprover(ctx context.Context, tenantID string, approver *approval.Approver) error
	UpdateApprover(ctx context.Context, tenantID, userID string, approver *approval.Approver) error
	RemoveApprover(ctx context.Context, tenantID, userID string) error
	GetRules(ctx context.Context, tenantID string) ([]approval.ApprovalRule, error)
	AddRule(ctx context.Context, tenantID string, rule *approval.ApprovalRule) error
	RemoveRule(ctx context.Context, tenantID, ruleName string) error
	GetConfigStats(ctx context.Context, tenantID string) (*approval.ConfigStats, error)
}

// ApprovalConfigHandler handles approval configuration API requests.
type ApprovalConfigHandler struct {
	configManager ConfigManagerService
}

// NewApprovalConfigHandler creates a new approval config handler.
// 接受 ConfigManagerService 接口，*approval.ConfigManager 默认满足该接口。
func NewApprovalConfigHandler(configManager ConfigManagerService) *ApprovalConfigHandler {
	return &ApprovalConfigHandler{
		configManager: configManager,
	}
}

// GetConfig handles GET /api/admin/tenants/{tenant_id}/approval-config
// Returns the approval configuration for a tenant.
func (h *ApprovalConfigHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	// Check authorization - only tenant admin or super admin can access
	if !h.canAccessTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	config, err := h.configManager.GetConfig(r.Context(), tenantID)
	if err != nil {
		slog.Error("failed to get approval config", "tenant_id", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get config")
		return
	}

	writeJSON(w, http.StatusOK, config)
}

// UpdateConfig handles PUT /api/admin/tenants/{tenant_id}/approval-config
// Updates the approval configuration for a tenant.
func (h *ApprovalConfigHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	// Check authorization - only tenant admin can modify
	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can modify configuration")
		return
	}

	var config approval.ApprovalConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := h.configManager.UpdateConfig(r.Context(), tenantID, &config); err != nil {
		slog.Error("failed to update approval config", "tenant_id", tenantID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "configuration updated successfully",
		"config":  config,
	})
}

// GetApprovers handles GET /api/admin/tenants/{tenant_id}/approvers
// Returns the list of approvers for a tenant.
func (h *ApprovalConfigHandler) GetApprovers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	// Check authorization
	if !h.canAccessTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	approvers, err := h.configManager.GetApprovers(r.Context(), tenantID)
	if err != nil {
		slog.Error("failed to get approvers", "tenant_id", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get approvers")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"approvers": approvers,
		"count":     len(approvers),
	})
}

// AddApprover handles POST /api/admin/tenants/{tenant_id}/approvers
// Adds a new approver to a tenant.
func (h *ApprovalConfigHandler) AddApprover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	// Check authorization
	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can add approvers")
		return
	}

	var approver approval.Approver
	if err := json.NewDecoder(r.Body).Decode(&approver); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := h.configManager.AddApprover(r.Context(), tenantID, &approver); err != nil {
		slog.Error("failed to add approver", "tenant_id", tenantID, "user_id", approver.UserID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"success":  true,
		"message":  "approver added successfully",
		"approver": approver,
	})
}

// UpdateApprover handles PUT /api/admin/tenants/{tenant_id}/approvers/{user_id}
// Updates an existing approver.
func (h *ApprovalConfigHandler) UpdateApprover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	userID := h.extractUserID(r.URL.Path)
	if userID == "" {
		writeError(w, http.StatusBadRequest, "missing user_id")
		return
	}

	// Check authorization
	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can update approvers")
		return
	}

	var approver approval.Approver
	if err := json.NewDecoder(r.Body).Decode(&approver); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := h.configManager.UpdateApprover(r.Context(), tenantID, userID, &approver); err != nil {
		slog.Error("failed to update approver", "tenant_id", tenantID, "user_id", userID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"message":  "approver updated successfully",
		"approver": approver,
	})
}

// DeleteApprover handles DELETE /api/admin/tenants/{tenant_id}/approvers/{user_id}
// Removes an approver from a tenant.
func (h *ApprovalConfigHandler) DeleteApprover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	userID := h.extractUserID(r.URL.Path)
	if userID == "" {
		writeError(w, http.StatusBadRequest, "missing user_id")
		return
	}

	// Check authorization
	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can delete approvers")
		return
	}

	if err := h.configManager.RemoveApprover(r.Context(), tenantID, userID); err != nil {
		slog.Error("failed to delete approver", "tenant_id", tenantID, "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete approver")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "approver deleted successfully",
	})
}

// GetRules handles GET /api/admin/tenants/{tenant_id}/approval-rules
// Returns the list of approval rules for a tenant.
func (h *ApprovalConfigHandler) GetRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	// Check authorization
	if !h.canAccessTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	rules, err := h.configManager.GetRules(r.Context(), tenantID)
	if err != nil {
		slog.Error("failed to get rules", "tenant_id", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get rules")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"rules": rules,
		"count": len(rules),
	})
}

// AddRule handles POST /api/admin/tenants/{tenant_id}/approval-rules
// Adds a new approval rule to a tenant.
func (h *ApprovalConfigHandler) AddRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	// Check authorization
	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can add rules")
		return
	}

	var rule approval.ApprovalRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := h.configManager.AddRule(r.Context(), tenantID, &rule); err != nil {
		slog.Error("failed to add rule", "tenant_id", tenantID, "rule_name", rule.Name, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"success": true,
		"message": "rule added successfully",
		"rule":    rule,
	})
}

// DeleteRule handles DELETE /api/admin/tenants/{tenant_id}/approval-rules/{rule_name}
// Removes an approval rule from a tenant.
func (h *ApprovalConfigHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	ruleName := h.extractRuleName(r.URL.Path)
	if ruleName == "" {
		writeError(w, http.StatusBadRequest, "missing rule_name")
		return
	}

	// Check authorization
	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can delete rules")
		return
	}

	if err := h.configManager.RemoveRule(r.Context(), tenantID, ruleName); err != nil {
		slog.Error("failed to delete rule", "tenant_id", tenantID, "rule_name", ruleName, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete rule")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "rule deleted successfully",
	})
}

// GetConfigStats handles GET /api/admin/tenants/{tenant_id}/approval-config/stats
// Returns statistics about the approval configuration.
func (h *ApprovalConfigHandler) GetConfigStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := h.extractTenantID(r.URL.Path)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant_id")
		return
	}

	// Check authorization
	if !h.canAccessTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	stats, err := h.configManager.GetConfigStats(r.Context(), tenantID)
	if err != nil {
		slog.Error("failed to get config stats", "tenant_id", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get stats")
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// Helper methods

// extractTenantID extracts tenant_id from URL path.
// Supports two path layouts (legacy + post-2026-07-03):
//   - /api/admin/tenants/{tenant_id}/...               (legacy prefix)
//   - /api/admin/tenant-approval-config/{tenant_id}/... (new prefix,
//     avoids ServeMux conflict with /api/admin/tenants/ handle in handler.go)
func (h *ApprovalConfigHandler) extractTenantID(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "tenant-approval-config" && i+1 < len(parts) {
			return parts[i+1]
		}
		if part == "tenants" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// extractUserID extracts user_id from URL path
// Supports paths like: /api/admin/tenants/{tenant_id}/approvers/{user_id}
func (h *ApprovalConfigHandler) extractUserID(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "approvers" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// extractRuleName extracts rule_name from URL path
// Supports paths like: /api/admin/tenants/{tenant_id}/approval-rules/{rule_name}
func (h *ApprovalConfigHandler) extractRuleName(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "approval-rules" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// canAccessTenant checks if the current user can access the tenant's data.
// Super admin can access any tenant, tenant admin can only access their own.
func (h *ApprovalConfigHandler) canAccessTenant(r *http.Request, tenantID string) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}

	// Super admin or legacy admin key can access any tenant
	if auth.Role == "super_admin" || auth.Role == "admin_key" {
		return true
	}

	// Tenant admin can only access their own tenant
	if auth.Role == "tenant_admin" {
		return auth.TenantID == tenantID
	}

	return false
}

// canModifyTenant checks if the current user can modify the tenant's configuration.
// Only tenant admin (for their own tenant) or super admin can modify.
func (h *ApprovalConfigHandler) canModifyTenant(r *http.Request, tenantID string) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}

	// Super admin or legacy admin key can modify any tenant
	if auth.Role == "super_admin" || auth.Role == "admin_key" {
		return true
	}

	// Tenant admin can only modify their own tenant
	if auth.Role == "tenant_admin" {
		return auth.TenantID == tenantID
	}

	return false
}

// Note: writeJSON and writeError helper functions are defined in handler.go
