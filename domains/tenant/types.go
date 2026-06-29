// Package tenant provides tenant-level configuration, quota, and policy management.
//
// This package extracts tenant-related logic from the settings/ package to form
// an independent domain with clear boundaries and responsibilities.
package tenant

// TenantQuota represents resource quotas for a tenant.
type TenantQuota struct {
	TenantID          string
	MaxRequestsPerDay int
	MaxTokensPerDay   int64
	MaxConcurrent     int
	MaxModels         int
}

// TenantPolicy represents access control policies for a tenant.
type TenantPolicy struct {
	TenantID        string
	AllowedModels   []string // Model whitelist
	BlockedModels   []string // Model blacklist
	EnabledFeatures []string // Feature flags
}

// RLSConfig represents Row-Level Security configuration for a tenant.
type RLSConfig struct {
	TenantID string
	Enabled  bool
	Policy   string // RLS policy name
}
