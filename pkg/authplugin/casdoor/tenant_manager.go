package casdoor

import (
	"context"
	"fmt"

	casdoorsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// CasdoorTenantManager implements authplugin.TenantManager using Casdoor Organizations.
// It maps Casdoor organizations to internal tenants.
type CasdoorTenantManager struct {
	client *casdoorsdk.Client
	config *authplugin.CasdoorConfig
}

// NewCasdoorTenantManager creates a Casdoor tenant manager.
func NewCasdoorTenantManager(config *authplugin.CasdoorConfig) (*CasdoorTenantManager, error) {
	if config == nil {
		return nil, fmt.Errorf("casdoor config is required")
	}

	// Validate required fields
	if config.Endpoint == "" {
		return nil, fmt.Errorf("casdoor endpoint is required")
	}
	if config.ClientID == "" {
		return nil, fmt.Errorf("casdoor client_id is required")
	}
	if config.ClientSecret == "" {
		return nil, fmt.Errorf("casdoor client_secret is required")
	}
	if config.Organization == "" {
		return nil, fmt.Errorf("casdoor organization is required")
	}
	if config.Application == "" {
		return nil, fmt.Errorf("casdoor application is required")
	}

	// Initialize Casdoor SDK client
	authConfig := &casdoorsdk.AuthConfig{
		Endpoint:         config.Endpoint,
		ClientId:         config.ClientID,
		ClientSecret:     config.ClientSecret,
		Certificate:      config.Certificate,
		OrganizationName: config.Organization,
		ApplicationName:  config.Application,
	}

	client := casdoorsdk.NewClientWithConf(authConfig)

	return &CasdoorTenantManager{
		client: client,
		config: config,
	}, nil
}

// Name returns the manager identifier.
func (m *CasdoorTenantManager) Name() string {
	return "casdoor"
}

// GetTenant retrieves a tenant by ID (organization name in Casdoor).
func (m *CasdoorTenantManager) GetTenant(ctx context.Context, tenantID string) (*authplugin.Tenant, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	// Get organization from Casdoor
	org, err := m.client.GetOrganization(tenantID)
	if err != nil {
		return nil, authplugin.ErrNotFound
	}

	if org == nil {
		return nil, authplugin.ErrNotFound
	}

	return m.convertTenant(org), nil
}

// ListTenants lists all accessible tenants (organizations).
func (m *CasdoorTenantManager) ListTenants(ctx context.Context, filter *authplugin.TenantFilter) ([]*authplugin.Tenant, error) {
	// Get all organizations from Casdoor
	orgs, err := m.client.GetOrganizations()
	if err != nil {
		return nil, fmt.Errorf("failed to list organizations from casdoor: %w", err)
	}

	var result []*authplugin.Tenant
	for _, org := range orgs {
		tenant := m.convertTenant(org)

		// Apply filters
		if filter != nil {
			if filter.Status != "" && tenant.Status != filter.Status {
				continue
			}
			// Additional filters can be added here
		}

		result = append(result, tenant)

		// Apply limit
		if filter != nil && filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}

	return result, nil
}

// CreateTenant creates a new tenant (organization) in Casdoor.
func (m *CasdoorTenantManager) CreateTenant(ctx context.Context, req *authplugin.CreateTenantRequest) (*authplugin.Tenant, error) {
	if req == nil {
		return nil, fmt.Errorf("create tenant request is required")
	}

	if req.ID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	if req.Name == "" {
		return nil, fmt.Errorf("tenant name is required")
	}

	// Create Casdoor organization object
	org := &casdoorsdk.Organization{
		Owner:       "admin", // Default owner for organizations
		Name:        req.ID,
		CreatedTime: "",      // Will be set by Casdoor
		DisplayName: req.Name,
		WebsiteUrl:  "",
		Favicon:     "",
		// Additional fields can be set from metadata if needed
	}

	// Create in Casdoor
	affected, err := m.client.AddOrganization(org)
	if err != nil {
		return nil, fmt.Errorf("failed to create organization in casdoor: %w", err)
	}
	if !affected {
		return nil, fmt.Errorf("failed to create organization in casdoor: no rows affected")
	}

	// Get created organization to obtain full details
	createdOrg, err := m.client.GetOrganization(req.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch created organization: %w", err)
	}

	if createdOrg == nil {
		return nil, fmt.Errorf("organization not found after creation")
	}

	return m.convertTenant(createdOrg), nil
}

// UpdateTenant updates an existing tenant (organization) in Casdoor.
func (m *CasdoorTenantManager) UpdateTenant(ctx context.Context, tenantID string, req *authplugin.UpdateTenantRequest) (*authplugin.Tenant, error) {
	if req == nil {
		return nil, fmt.Errorf("update tenant request is required")
	}

	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	// Get existing organization from Casdoor
	org, err := m.client.GetOrganization(tenantID)
	if err != nil {
		return nil, authplugin.ErrNotFound
	}

	if org == nil {
		return nil, authplugin.ErrNotFound
	}

	// Update fields
	if req.Name != nil {
		org.DisplayName = *req.Name
	}
	// Note: Status updates would be handled here if supported by Casdoor

	// Update in Casdoor
	affected, err := m.client.UpdateOrganization(org)
	if err != nil {
		return nil, fmt.Errorf("failed to update organization in casdoor: %w", err)
	}
	if !affected {
		return nil, fmt.Errorf("failed to update organization in casdoor: no rows affected")
	}

	return m.convertTenant(org), nil
}

// DeleteTenant deletes a tenant (organization) from Casdoor.
func (m *CasdoorTenantManager) DeleteTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return fmt.Errorf("tenant ID is required")
	}

	// Get organization from Casdoor
	org, err := m.client.GetOrganization(tenantID)
	if err != nil {
		return authplugin.ErrNotFound
	}

	if org == nil {
		return authplugin.ErrNotFound
	}

	// Delete from Casdoor
	affected, err := m.client.DeleteOrganization(org)
	if err != nil {
		return fmt.Errorf("failed to delete organization from casdoor: %w", err)
	}
	if !affected {
		return fmt.Errorf("failed to delete organization from casdoor: no rows affected")
	}

	return nil
}

// SupportsFeature reports which optional features this manager supports.
func (m *CasdoorTenantManager) SupportsFeature(feature authplugin.TenantFeature) bool {
	switch feature {
	case authplugin.FeatureTenantHierarchy:
		return false // Casdoor organizations are flat
	case authplugin.FeatureTenantIsolation:
		return true // Casdoor enforces tenant isolation
	case authplugin.FeatureTenantQuotas:
		return false // Not implemented in this version
	default:
		return false
	}
}

// convertTenant converts a Casdoor organization to authplugin.Tenant.
func (m *CasdoorTenantManager) convertTenant(org *casdoorsdk.Organization) *authplugin.Tenant {
	if org == nil {
		return nil
	}

	// Casdoor organizations don't have a direct status field
	// We assume all organizations are active by default
	status := "active"

	return &authplugin.Tenant{
		ID:          org.Name,
		Name:        org.Name,
		DisplayName: org.DisplayName,
		Status:      status,
		Tags:        []string{},
		IsInternal:  false,
		IsForbidden: false,
		Policy:      nil,
		Metadata: map[string]interface{}{
			"owner":       org.Owner,
			"website_url": org.WebsiteUrl,
			"favicon":     org.Favicon,
			"provider":    "casdoor",
		},
	}
}
