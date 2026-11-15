package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// PluginConfigHandler handles plugin configuration management API requests.
// Provides endpoints to query and update provider selections (Native vs Casdoor).
type PluginConfigHandler struct {
	registry *authplugin.Registry
	db       *pgxpool.Pool
}

// NewPluginConfigHandler creates a new plugin config handler.
func NewPluginConfigHandler(registry *authplugin.Registry, db *pgxpool.Pool) *PluginConfigHandler {
	return &PluginConfigHandler{
		registry: registry,
		db:       db,
	}
}

// GetPluginConfig handles GET /api/admin/plugin-config
// Returns the current plugin provider configuration.
func (h *PluginConfigHandler) GetPluginConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.registry == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin registry not available")
		return
	}

	config := h.registry.Config()
	if config == nil {
		writeError(w, http.StatusInternalServerError, "failed to get plugin config")
		return
	}

	// Get available providers
	authProviders := h.registry.ListAuthProviders()
	userManagers := h.registry.ListUserManagers()
	tenantManagers := h.registry.ListTenantManagers()
	approvalProviders := h.registry.ListApprovalProviders()

	// Check which providers are actually functional
	status := h.checkProviderStatus()

	response := map[string]interface{}{
		"current": map[string]string{
			"auth_provider":     config.AuthProvider,
			"user_manager":      config.UserManager,
			"tenant_manager":    config.TenantManager,
			"approval_provider": config.ApprovalProvider,
		},
		"available": map[string][]string{
			"auth_providers":     authProviders,
			"user_managers":      userManagers,
			"tenant_managers":    tenantManagers,
			"approval_providers": approvalProviders,
		},
		"status": status,
		"source": h.getConfigSource(),
	}

	writeJSON(w, http.StatusOK, response)
}

// UpdatePluginConfig handles PUT /api/admin/plugin-config
// Updates the plugin provider configuration.
func (h *PluginConfigHandler) UpdatePluginConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.registry == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin registry not available")
		return
	}

	var req struct {
		AuthProvider     string `json:"auth_provider"`
		UserManager      string `json:"user_manager"`
		TenantManager    string `json:"tenant_manager"`
		ApprovalProvider string `json:"approval_provider"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate that requested providers are available
	if err := h.validateProviderSelection(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Update environment variables (for runtime changes)
	// Note: This only affects the current process; for persistent changes,
	// update the config file or database
	if req.AuthProvider != "" {
		os.Setenv("AUTH_PROVIDER", req.AuthProvider)
	}
	if req.UserManager != "" {
		os.Setenv("USER_MANAGER", req.UserManager)
	}
	if req.TenantManager != "" {
		os.Setenv("TENANT_MANAGER", req.TenantManager)
	}
	if req.ApprovalProvider != "" {
		os.Setenv("APPROVAL_PROVIDER", req.ApprovalProvider)
	}

	// Persist to database (plugin_config table)
	if err := h.savePluginConfig(r.Context(), &req); err != nil {
		slog.Error("failed to save plugin config", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save configuration")
		return
	}

	slog.Info("plugin configuration updated",
		"auth_provider", req.AuthProvider,
		"user_manager", req.UserManager,
		"tenant_manager", req.TenantManager,
		"approval_provider", req.ApprovalProvider,
	)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "plugin configuration updated successfully - restart required to take full effect",
		"config": map[string]string{
			"auth_provider":     req.AuthProvider,
			"user_manager":      req.UserManager,
			"tenant_manager":    req.TenantManager,
			"approval_provider": req.ApprovalProvider,
		},
	})
}

// GetProviderStatus handles GET /api/admin/plugin-providers
// Returns status information for all registered providers.
func (h *PluginConfigHandler) GetProviderStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.registry == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin registry not available")
		return
	}

	status := h.checkProviderStatus()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"providers": status,
		"summary":   h.registry.Summary(),
	})
}

// Helper methods

func (h *PluginConfigHandler) validateProviderSelection(req *struct {
	AuthProvider     string `json:"auth_provider"`
	UserManager      string `json:"user_manager"`
	TenantManager    string `json:"tenant_manager"`
	ApprovalProvider string `json:"approval_provider"`
}) error {
	// Check auth provider
	if req.AuthProvider != "" {
		available := h.registry.ListAuthProviders()
		if !containsStringItem(available, req.AuthProvider) {
			return fmt.Errorf("auth provider %q not available (available: %v)", req.AuthProvider, available)
		}
	}

	// Check user manager
	if req.UserManager != "" {
		available := h.registry.ListUserManagers()
		if !containsStringItem(available, req.UserManager) {
			return fmt.Errorf("user manager %q not available (available: %v)", req.UserManager, available)
		}
	}

	// Check tenant manager
	if req.TenantManager != "" {
		available := h.registry.ListTenantManagers()
		if !containsStringItem(available, req.TenantManager) {
			return fmt.Errorf("tenant manager %q not available (available: %v)", req.TenantManager, available)
		}
	}

	// Check approval provider
	if req.ApprovalProvider != "" {
		available := h.registry.ListApprovalProviders()
		if !containsStringItem(available, req.ApprovalProvider) {
			return fmt.Errorf("approval provider %q not available (available: %v)", req.ApprovalProvider, available)
		}
	}

	return nil
}

func (h *PluginConfigHandler) checkProviderStatus() map[string]interface{} {
	status := make(map[string]interface{})

	// Check auth provider
	authProvider, err := h.registry.GetAuthProvider()
	status["auth_provider"] = map[string]interface{}{
		"name":      getProviderName(authProvider),
		"available": err == nil,
		"error":     getErrorString(err),
	}

	// Check user manager
	userManager, err := h.registry.GetUserManager()
	status["user_manager"] = map[string]interface{}{
		"name":      getProviderName(userManager),
		"available": err == nil,
		"error":     getErrorString(err),
	}

	// Check tenant manager
	tenantManager, err := h.registry.GetTenantManager()
	status["tenant_manager"] = map[string]interface{}{
		"name":      getProviderName(tenantManager),
		"available": err == nil,
		"error":     getErrorString(err),
	}

	// Check approval provider
	approvalProvider, err := h.registry.GetApprovalProvider()
	status["approval_provider"] = map[string]interface{}{
		"name":      getProviderName(approvalProvider),
		"available": err == nil,
		"error":     getErrorString(err),
	}

	return status
}

func (h *PluginConfigHandler) getConfigSource() string {
	// Determine where the current config comes from
	if os.Getenv("AUTH_PROVIDER") != "" {
		return "environment"
	}

	// Check if config exists in database
	ctx := context.Background()
	var count int
	err := h.db.QueryRow(ctx, "SELECT COUNT(*) FROM plugin_config WHERE id = 1").Scan(&count)
	if err == nil && count > 0 {
		return "database"
	}

	return "default"
}

func (h *PluginConfigHandler) savePluginConfig(ctx context.Context, req *struct {
	AuthProvider     string `json:"auth_provider"`
	UserManager      string `json:"user_manager"`
	TenantManager    string `json:"tenant_manager"`
	ApprovalProvider string `json:"approval_provider"`
}) error {
	if h.db == nil {
		return fmt.Errorf("database not available")
	}

	// Upsert into plugin_config table
	query := `
		INSERT INTO plugin_config (id, auth_provider, user_manager, tenant_manager, approval_provider, updated_at)
		VALUES (1, $1, $2, $3, $4, NOW())
		ON CONFLICT (id) DO UPDATE SET
			auth_provider = EXCLUDED.auth_provider,
			user_manager = EXCLUDED.user_manager,
			tenant_manager = EXCLUDED.tenant_manager,
			approval_provider = EXCLUDED.approval_provider,
			updated_at = NOW()
	`

	_, err := h.db.Exec(ctx, query,
		req.AuthProvider,
		req.UserManager,
		req.TenantManager,
		req.ApprovalProvider,
	)

	return err
}

// Utility functions

func containsStringItem(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

type namedProvider interface {
	Name() string
}

func getProviderName(provider interface{}) string {
	if provider == nil {
		return ""
	}
	if np, ok := provider.(namedProvider); ok {
		return np.Name()
	}
	return "unknown"
}

func getErrorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
