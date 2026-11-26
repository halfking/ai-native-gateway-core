package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// pluginConfigDB 是 plugin-config GET/PUT 的窄注入面（★ 2026-11-27 L135 轮 ★）。
// 刻意保持窄接口（QueryRow + Exec）：db 字段是具体 *pgxpool.Pool，无法在单测里
// 注入 pgxmock，而「PUT 落库语句形状（整行覆盖、含四列）」与「GET source 判定
// 查询」这两条判据必须能被单测钉住 —— 按 controlledBodyDB / loginDB 的同款
// 范式开窄口。nil ⇒ 用 h.db（生产路径零变化）。
type pluginConfigDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// PluginConfigHandler handles plugin configuration management API requests.
// Provides endpoints to query and update provider selections (Native vs Casdoor).
type PluginConfigHandler struct {
	registry *authplugin.Registry
	db       *pgxpool.Pool
	// configDB 是窄注入面：nil ⇒ h.db。作用见 pluginConfigDB 注释。
	configDB pluginConfigDB
}

// NewPluginConfigHandler creates a new plugin config handler.
func NewPluginConfigHandler(registry *authplugin.Registry, db *pgxpool.Pool) *PluginConfigHandler {
	return &PluginConfigHandler{
		registry: registry,
		db:       db,
	}
}

// setPluginConfigDB 注入窄查询面（仅测试使用；nil ⇒ h.db 生产路径）。
func (h *PluginConfigHandler) setPluginConfigDB(db pluginConfigDB) {
	h.configDB = db
}

// configQuerier 返回生效的查询面：显式注入优先，否则回落具体连接池。
// typed-nil 判定在具体类型上做（Go 经典坑：typed-nil 装进接口后 != nil）。
func (h *PluginConfigHandler) configQuerier() pluginConfigDB {
	if h.configDB != nil {
		return h.configDB
	}
	if h.db == nil {
		return nil
	}
	return h.db
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

	// ★ 2026-11-27 L135 轮（P0-2b 热切换收口）：
	//   ① 删除 os.Setenv 四行副作用 —— env 是进程级全局，PUT 之后（a）同进程
	//      的 loadPluginConfig 在重启前不会再读它（热切换根本不靠 env），
	//      （b）重启后 env 覆盖 plugin_config 表 ⇒ 数据库里「已切换」的口径
	//      被启动环境静默打回 ⇒ 配置源分叉。表是唯一事实源。
	//   ② 空字段语义改为「保持现状」：旧实现把空串直接写进 plugin_config 表，
	//      撞 CHECK 约束（valid_*_provider IN ('native','casdoor')）⇒
	//      局部更新请求（只切 approval_provider）今天就是 500；同时空串覆盖
	//      其它列的既有值。现在局部字段合并进当前 registry 配置，整行落库。
	//   ③ savePluginConfig 成功后 registry.SwitchConfig 原子换选择 ⇒ 登录面
	//      （handleLogin → registry.GetAuthProvider）与审批面
	//      （approval_config_handler → registry.GetApprovalProvider）立即用新
	//      provider —— 不重启、不写 env。casdoor 需在网关启动时配置了
	//      CASDOOR_* env（装配点据此注册 casdoor provider），否则 PUT 会在
	//      validateProviderSelection 处被拒（"not available"）。

	// Merge partial request onto the current selection (empty = keep current)
	current := h.registry.Config()
	merged := &pluginConfigUpdate{
		AuthProvider:     firstNonEmpty(req.AuthProvider, current.AuthProvider),
		UserManager:      firstNonEmpty(req.UserManager, current.UserManager),
		TenantManager:    firstNonEmpty(req.TenantManager, current.TenantManager),
		ApprovalProvider: firstNonEmpty(req.ApprovalProvider, current.ApprovalProvider),
	}

	// Persist to database (plugin_config table) — the single source of truth
	if err := h.savePluginConfig(r.Context(), merged); err != nil {
		slog.Error("failed to save plugin config", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save configuration")
		return
	}

	// Hot-switch the shared registry: subsequent Get*Provider calls resolve
	// against the new selection immediately (no restart).
	h.registry.SwitchConfig(merged.AuthProvider, merged.UserManager, merged.TenantManager, merged.ApprovalProvider)

	slog.Info("plugin configuration updated (hot-switched)",
		"auth_provider", merged.AuthProvider,
		"user_manager", merged.UserManager,
		"tenant_manager", merged.TenantManager,
		"approval_provider", merged.ApprovalProvider,
	)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "plugin configuration updated and hot-switched (no restart required)",
		"config": map[string]string{
			"auth_provider":     merged.AuthProvider,
			"user_manager":      merged.UserManager,
			"tenant_manager":    merged.TenantManager,
			"approval_provider": merged.ApprovalProvider,
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
	db := h.configQuerier()
	if db == nil {
		return "default"
	}
	ctx := context.Background()
	var count int
	err := db.QueryRow(ctx, "SELECT COUNT(*) FROM plugin_config WHERE id = 1").Scan(&count)
	if err == nil && count > 0 {
		return "database"
	}

	return "default"
}

// pluginConfigUpdate is the merged (request ∪ current) provider selection
// that gets persisted and hot-switched. Separate type from the wire request
// struct so savePluginConfig's signature documents what it stores.
type pluginConfigUpdate struct {
	AuthProvider     string
	UserManager      string
	TenantManager    string
	ApprovalProvider string
}

// firstNonEmpty returns a if non-empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (h *PluginConfigHandler) savePluginConfig(ctx context.Context, req *pluginConfigUpdate) error {
	db := h.configQuerier()
	if db == nil {
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

	_, err := db.Exec(ctx, query,
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
