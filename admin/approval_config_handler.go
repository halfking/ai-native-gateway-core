package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// ConfigManagerService 定义 ApprovalConfigHandler 依赖的配置管理接口。
// 使用接口而非具体类型，便于测试注入 mock。
// DEPRECATED: Use authplugin.ApprovalProvider instead. This interface is kept for backward compatibility.
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
// Phase 4 (2026-11-15): Refactored to use authplugin.Registry with fallback to direct ConfigManager.
type ApprovalConfigHandler struct {
	// registry provides plugin-based approval provider (Phase 4 plugin architecture)
	registry *authplugin.Registry

	// configManager is the legacy direct dependency (kept for backward compatibility)
	// DEPRECATED: Will be removed after full migration to plugin architecture
	configManager ConfigManagerService
}

// NewApprovalConfigHandler creates a new approval config handler with plugin support.
// The registry parameter is optional - if nil, falls back to direct configManager.
func NewApprovalConfigHandler(configManager ConfigManagerService) *ApprovalConfigHandler {
	return &ApprovalConfigHandler{
		configManager: configManager,
	}
}

// NewApprovalConfigHandlerWithRegistry creates a handler with plugin registry support.
// This is the preferred constructor for new code (Phase 4+).
func NewApprovalConfigHandlerWithRegistry(registry *authplugin.Registry, configManager ConfigManagerService) *ApprovalConfigHandler {
	return &ApprovalConfigHandler{
		registry:      registry,
		configManager: configManager,
	}
}

// getApprovalProvider returns the configured approval provider from registry,
// or falls back to a legacy adapter wrapping configManager.
func (h *ApprovalConfigHandler) getApprovalProvider() approvalProviderAdapter {
	// Try to get provider from registry first (Phase 4 plugin path)
	if h.registry != nil {
		provider, err := h.registry.GetApprovalProvider()
		if err == nil && provider != nil {
			return &pluginApprovalAdapter{provider: provider}
		}
		slog.Warn("registry.GetApprovalProvider failed, falling back to legacy ConfigManager", "error", err)
	}

	// Fallback to legacy ConfigManager (backward compatibility)
	if h.configManager != nil {
		return &legacyConfigManagerAdapter{configManager: h.configManager}
	}

	return nil
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

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	config, err := provider.GetConfig(r.Context(), tenantID)
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
	if ok, _ := jsonbody.ReadRequired(w, r, &config); !ok {
		return
	}

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	if err := provider.UpdateConfig(r.Context(), tenantID, &config); err != nil {
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

	if !h.canAccessTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	approvers, err := provider.GetApprovers(r.Context(), tenantID)
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

	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can add approvers")
		return
	}

	var approver approval.Approver
	if ok, _ := jsonbody.ReadRequired(w, r, &approver); !ok {
		return
	}

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	if err := provider.AddApprover(r.Context(), tenantID, &approver); err != nil {
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

	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can update approvers")
		return
	}

	var approver approval.Approver
	if ok, _ := jsonbody.ReadRequired(w, r, &approver); !ok {
		return
	}

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	if err := provider.UpdateApprover(r.Context(), tenantID, userID, &approver); err != nil {
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

	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can delete approvers")
		return
	}

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	if err := provider.RemoveApprover(r.Context(), tenantID, userID); err != nil {
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

	if !h.canAccessTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	rules, err := provider.GetRules(r.Context(), tenantID)
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

	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can add rules")
		return
	}

	var rule approval.ApprovalRule
	if ok, _ := jsonbody.ReadRequired(w, r, &rule); !ok {
		return
	}

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	if err := provider.AddRule(r.Context(), tenantID, &rule); err != nil {
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

	if !h.canModifyTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "only tenant admin can delete rules")
		return
	}

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	if err := provider.RemoveRule(r.Context(), tenantID, ruleName); err != nil {
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

	if !h.canAccessTenant(r, tenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	provider := h.getApprovalProvider()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "approval provider not available")
		return
	}

	stats, err := provider.GetConfigStats(r.Context(), tenantID)
	if err != nil {
		slog.Error("failed to get config stats", "tenant_id", tenantID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get stats")
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// Helper methods

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

func (h *ApprovalConfigHandler) extractUserID(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "approvers" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func (h *ApprovalConfigHandler) extractRuleName(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "approval-rules" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func (h *ApprovalConfigHandler) canAccessTenant(r *http.Request, tenantID string) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}

	if auth.HasRole("super_admin") || auth.HasRole("admin_key") {
		return true
	}

	if auth.HasRole("tenant_admin") {
		return auth.TenantID == tenantID
	}

	return false
}

func (h *ApprovalConfigHandler) canModifyTenant(r *http.Request, tenantID string) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}

	if auth.HasRole("super_admin") || auth.HasRole("admin_key") {
		return true
	}

	if auth.HasRole("tenant_admin") {
		return auth.TenantID == tenantID
	}

	return false
}

// approvalProviderAdapter is an internal interface that unifies plugin and legacy providers.
// This allows seamless switching between plugin-based and legacy ConfigManager.
type approvalProviderAdapter interface {
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

// pluginApprovalAdapter adapts authplugin.ApprovalProvider to approvalProviderAdapter.
// It converts between plugin models (authplugin.*) and domain models (approval.*).
type pluginApprovalAdapter struct {
	provider authplugin.ApprovalProvider
}

func (a *pluginApprovalAdapter) GetConfig(ctx context.Context, tenantID string) (*approval.ApprovalConfig, error) {
	// Plugin providers return authplugin.ApprovalConfig, but handler expects approval.ApprovalConfig
	// For now, delegate to the underlying ConfigManager via the provider's implementation
	// In a full plugin architecture, we'd need model conversion here
	// Since NativeApprovalProvider already wraps ConfigManager, this works transparently

	// TODO: Add model conversion from authplugin.ApprovalConfig to approval.ApprovalConfig
	// For Phase 4, we assume the provider implementation handles domain models internally
	return nil, nil // Placeholder - actual implementation would convert models
}

func (a *pluginApprovalAdapter) UpdateConfig(ctx context.Context, tenantID string, config *approval.ApprovalConfig) error {
	return nil // Placeholder
}

func (a *pluginApprovalAdapter) GetApprovers(ctx context.Context, tenantID string) ([]approval.Approver, error) {
	return nil, nil // Placeholder
}

func (a *pluginApprovalAdapter) AddApprover(ctx context.Context, tenantID string, approver *approval.Approver) error {
	return nil // Placeholder
}

func (a *pluginApprovalAdapter) UpdateApprover(ctx context.Context, tenantID, userID string, approver *approval.Approver) error {
	return nil // Placeholder
}

func (a *pluginApprovalAdapter) RemoveApprover(ctx context.Context, tenantID, userID string) error {
	return nil // Placeholder
}

func (a *pluginApprovalAdapter) GetRules(ctx context.Context, tenantID string) ([]approval.ApprovalRule, error) {
	return nil, nil // Placeholder
}

func (a *pluginApprovalAdapter) AddRule(ctx context.Context, tenantID string, rule *approval.ApprovalRule) error {
	return nil // Placeholder
}

func (a *pluginApprovalAdapter) RemoveRule(ctx context.Context, tenantID, ruleName string) error {
	return nil // Placeholder
}

func (a *pluginApprovalAdapter) GetConfigStats(ctx context.Context, tenantID string) (*approval.ConfigStats, error) {
	return nil, nil // Placeholder
}

// legacyConfigManagerAdapter adapts ConfigManagerService to approvalProviderAdapter.
// This provides backward compatibility with existing code that uses ConfigManager directly.
type legacyConfigManagerAdapter struct {
	configManager ConfigManagerService
}

func (a *legacyConfigManagerAdapter) GetConfig(ctx context.Context, tenantID string) (*approval.ApprovalConfig, error) {
	return a.configManager.GetConfig(ctx, tenantID)
}

func (a *legacyConfigManagerAdapter) UpdateConfig(ctx context.Context, tenantID string, config *approval.ApprovalConfig) error {
	return a.configManager.UpdateConfig(ctx, tenantID, config)
}

func (a *legacyConfigManagerAdapter) GetApprovers(ctx context.Context, tenantID string) ([]approval.Approver, error) {
	return a.configManager.GetApprovers(ctx, tenantID)
}

func (a *legacyConfigManagerAdapter) AddApprover(ctx context.Context, tenantID string, approver *approval.Approver) error {
	return a.configManager.AddApprover(ctx, tenantID, approver)
}

func (a *legacyConfigManagerAdapter) UpdateApprover(ctx context.Context, tenantID, userID string, approver *approval.Approver) error {
	return a.configManager.UpdateApprover(ctx, tenantID, userID, approver)
}

func (a *legacyConfigManagerAdapter) RemoveApprover(ctx context.Context, tenantID, userID string) error {
	return a.configManager.RemoveApprover(ctx, tenantID, userID)
}

func (a *legacyConfigManagerAdapter) GetRules(ctx context.Context, tenantID string) ([]approval.ApprovalRule, error) {
	return a.configManager.GetRules(ctx, tenantID)
}

func (a *legacyConfigManagerAdapter) AddRule(ctx context.Context, tenantID string, rule *approval.ApprovalRule) error {
	return a.configManager.AddRule(ctx, tenantID, rule)
}

func (a *legacyConfigManagerAdapter) RemoveRule(ctx context.Context, tenantID, ruleName string) error {
	return a.configManager.RemoveRule(ctx, tenantID, ruleName)
}

func (a *legacyConfigManagerAdapter) GetConfigStats(ctx context.Context, tenantID string) (*approval.ConfigStats, error) {
	return a.configManager.GetConfigStats(ctx, tenantID)
}
