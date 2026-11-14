package authplugin

import (
	"fmt"
	"sync"
)

// Registry manages plugin lifecycle and provider selection.
// It acts as a service locator for authentication-related providers.
//
// Thread-safe for concurrent access.
type Registry struct {
	mu                sync.RWMutex
	authProviders     map[string]AuthProvider
	userManagers      map[string]UserManager
	tenantManagers    map[string]TenantManager
	approvalProviders map[string]ApprovalProvider
	config            *PluginConfig
}

// PluginConfig specifies which plugins to use and their configuration.
type PluginConfig struct {
	// Provider selection (keys match registered provider names)
	AuthProvider     string // e.g., "native", "casdoor", "ldap"
	UserManager      string // e.g., "native", "casdoor"
	TenantManager    string // e.g., "native", "casdoor"
	ApprovalProvider string // e.g., "native", "hybrid"

	// Provider-specific configuration
	Native  *NativeConfig  // Native PostgreSQL provider config
	Casdoor *CasdoorConfig // Casdoor provider config
}

// NativeConfig contains native PostgreSQL provider configuration.
type NativeConfig struct {
	DatabaseDSN   string // PostgreSQL connection string
	JWTSecret     string // JWT signing secret (HS256)
	EncryptionKey []byte // Encryption key for sensitive data (32 bytes for AES-256)
}

// CasdoorConfig contains Casdoor connection parameters.
type CasdoorConfig struct {
	Endpoint      string // Casdoor server endpoint (e.g., https://casdoor.example.com)
	ClientID      string // OAuth2 client ID
	ClientSecret  string // OAuth2 client secret
	Organization  string // Casdoor organization name
	Application   string // Casdoor application name
	TLSSkipVerify bool   // Skip TLS verification (only for testing)
	Certificate   string // Optional: TLS certificate for verification
}

// NewRegistry creates a new plugin registry with the given configuration.
func NewRegistry(config *PluginConfig) *Registry {
	if config == nil {
		config = &PluginConfig{
			AuthProvider:     "native",
			UserManager:      "native",
			TenantManager:    "native",
			ApprovalProvider: "native",
		}
	}
	return &Registry{
		authProviders:     make(map[string]AuthProvider),
		userManagers:      make(map[string]UserManager),
		tenantManagers:    make(map[string]TenantManager),
		approvalProviders: make(map[string]ApprovalProvider),
		config:            config,
	}
}

// ============================================================================
// AuthProvider Registration and Retrieval
// ============================================================================

// RegisterAuthProvider registers an authentication provider.
// Returns an error if a provider with the same name is already registered.
func (r *Registry) RegisterAuthProvider(provider AuthProvider) error {
	if provider == nil {
		return fmt.Errorf("authplugin: cannot register nil auth provider")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := provider.Name()
	if name == "" {
		return fmt.Errorf("authplugin: auth provider must have a non-empty name")
	}

	if _, exists := r.authProviders[name]; exists {
		return fmt.Errorf("authplugin: auth provider %q already registered", name)
	}

	r.authProviders[name] = provider
	return nil
}

// GetAuthProvider returns the configured authentication provider.
// Returns an error if the configured provider is not registered.
func (r *Registry) GetAuthProvider() (AuthProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	name := r.config.AuthProvider
	if name == "" {
		name = "native"
	}

	provider, ok := r.authProviders[name]
	if !ok {
		return nil, fmt.Errorf("authplugin: auth provider %q not found (available: %v)",
			name, r.listAuthProviders())
	}

	return provider, nil
}

// ListAuthProviders returns the names of all registered auth providers.
func (r *Registry) ListAuthProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listAuthProviders()
}

func (r *Registry) listAuthProviders() []string {
	names := make([]string, 0, len(r.authProviders))
	for name := range r.authProviders {
		names = append(names, name)
	}
	return names
}

// ============================================================================
// UserManager Registration and Retrieval
// ============================================================================

// RegisterUserManager registers a user manager.
// Returns an error if a manager with the same name is already registered.
func (r *Registry) RegisterUserManager(manager UserManager) error {
	if manager == nil {
		return fmt.Errorf("authplugin: cannot register nil user manager")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := manager.Name()
	if name == "" {
		return fmt.Errorf("authplugin: user manager must have a non-empty name")
	}

	if _, exists := r.userManagers[name]; exists {
		return fmt.Errorf("authplugin: user manager %q already registered", name)
	}

	r.userManagers[name] = manager
	return nil
}

// GetUserManager returns the configured user manager.
// Returns an error if the configured manager is not registered.
func (r *Registry) GetUserManager() (UserManager, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	name := r.config.UserManager
	if name == "" {
		name = "native"
	}

	manager, ok := r.userManagers[name]
	if !ok {
		return nil, fmt.Errorf("authplugin: user manager %q not found (available: %v)",
			name, r.listUserManagers())
	}

	return manager, nil
}

// ListUserManagers returns the names of all registered user managers.
func (r *Registry) ListUserManagers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listUserManagers()
}

func (r *Registry) listUserManagers() []string {
	names := make([]string, 0, len(r.userManagers))
	for name := range r.userManagers {
		names = append(names, name)
	}
	return names
}

// ============================================================================
// TenantManager Registration and Retrieval
// ============================================================================

// RegisterTenantManager registers a tenant manager.
// Returns an error if a manager with the same name is already registered.
func (r *Registry) RegisterTenantManager(manager TenantManager) error {
	if manager == nil {
		return fmt.Errorf("authplugin: cannot register nil tenant manager")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := manager.Name()
	if name == "" {
		return fmt.Errorf("authplugin: tenant manager must have a non-empty name")
	}

	if _, exists := r.tenantManagers[name]; exists {
		return fmt.Errorf("authplugin: tenant manager %q already registered", name)
	}

	r.tenantManagers[name] = manager
	return nil
}

// GetTenantManager returns the configured tenant manager.
// Returns an error if the configured manager is not registered.
func (r *Registry) GetTenantManager() (TenantManager, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	name := r.config.TenantManager
	if name == "" {
		name = "native"
	}

	manager, ok := r.tenantManagers[name]
	if !ok {
		return nil, fmt.Errorf("authplugin: tenant manager %q not found (available: %v)",
			name, r.listTenantManagers())
	}

	return manager, nil
}

// ListTenantManagers returns the names of all registered tenant managers.
func (r *Registry) ListTenantManagers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listTenantManagers()
}

func (r *Registry) listTenantManagers() []string {
	names := make([]string, 0, len(r.tenantManagers))
	for name := range r.tenantManagers {
		names = append(names, name)
	}
	return names
}

// ============================================================================
// ApprovalProvider Registration and Retrieval
// ============================================================================

// RegisterApprovalProvider registers an approval provider.
// Returns an error if a provider with the same name is already registered.
func (r *Registry) RegisterApprovalProvider(provider ApprovalProvider) error {
	if provider == nil {
		return fmt.Errorf("authplugin: cannot register nil approval provider")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := provider.Name()
	if name == "" {
		return fmt.Errorf("authplugin: approval provider must have a non-empty name")
	}

	if _, exists := r.approvalProviders[name]; exists {
		return fmt.Errorf("authplugin: approval provider %q already registered", name)
	}

	r.approvalProviders[name] = provider
	return nil
}

// GetApprovalProvider returns the configured approval provider.
// Returns an error if the configured provider is not registered.
func (r *Registry) GetApprovalProvider() (ApprovalProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	name := r.config.ApprovalProvider
	if name == "" {
		name = "native"
	}

	provider, ok := r.approvalProviders[name]
	if !ok {
		return nil, fmt.Errorf("authplugin: approval provider %q not found (available: %v)",
			name, r.listApprovalProviders())
	}

	return provider, nil
}

// ListApprovalProviders returns the names of all registered approval providers.
func (r *Registry) ListApprovalProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listApprovalProviders()
}

func (r *Registry) listApprovalProviders() []string {
	names := make([]string, 0, len(r.approvalProviders))
	for name := range r.approvalProviders {
		names = append(names, name)
	}
	return names
}

// ============================================================================
// Utility Methods
// ============================================================================

// Config returns a copy of the registry configuration.
func (r *Registry) Config() *PluginConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Return a shallow copy to prevent external mutation
	configCopy := *r.config
	return &configCopy
}

// IsConfigured checks if all required providers are registered and configured.
func (r *Registry) IsConfigured() error {
	if _, err := r.GetAuthProvider(); err != nil {
		return fmt.Errorf("auth provider not configured: %w", err)
	}
	if _, err := r.GetUserManager(); err != nil {
		return fmt.Errorf("user manager not configured: %w", err)
	}
	if _, err := r.GetTenantManager(); err != nil {
		return fmt.Errorf("tenant manager not configured: %w", err)
	}
	if _, err := r.GetApprovalProvider(); err != nil {
		return fmt.Errorf("approval provider not configured: %w", err)
	}
	return nil
}

// Summary returns a human-readable summary of the registry configuration.
func (r *Registry) Summary() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return fmt.Sprintf("AuthPlugin Registry:\n"+
		"  Auth Provider: %s (available: %v)\n"+
		"  User Manager: %s (available: %v)\n"+
		"  Tenant Manager: %s (available: %v)\n"+
		"  Approval Provider: %s (available: %v)",
		r.config.AuthProvider, r.listAuthProviders(),
		r.config.UserManager, r.listUserManagers(),
		r.config.TenantManager, r.listTenantManagers(),
		r.config.ApprovalProvider, r.listApprovalProviders(),
	)
}
