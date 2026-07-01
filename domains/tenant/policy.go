package tenant

import (
	"context"
	"fmt"
	"sync"
)

// PolicyChecker checks tenant access policies.
type PolicyChecker struct {
	mu       sync.RWMutex
	policies map[string]*TenantPolicy
}

// NewPolicyChecker creates a new policy checker.
func NewPolicyChecker() *PolicyChecker {
	return &PolicyChecker{
		policies: make(map[string]*TenantPolicy),
	}
}

// SetPolicy sets policy for a tenant.
func (c *PolicyChecker) SetPolicy(policy *TenantPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policies[policy.TenantID] = policy
}

// CheckModelAccess checks if a tenant can access a model.
func (c *PolicyChecker) CheckModelAccess(ctx context.Context, tenantID, model string) error {
	c.mu.RLock()
	policy, ok := c.policies[tenantID]
	c.mu.RUnlock()

	if !ok {
		// No policy configured - allow by default
		return nil
	}

	// Check blacklist first
	for _, blocked := range policy.BlockedModels {
		if blocked == model {
			return fmt.Errorf("model %s is blocked for tenant %s", model, tenantID)
		}
	}

	// If whitelist exists, model must be in it
	if len(policy.AllowedModels) > 0 {
		found := false
		for _, allowed := range policy.AllowedModels {
			if allowed == model {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("model %s is not allowed for tenant %s", model, tenantID)
		}
	}

	return nil
}

// IsFeatureEnabled checks if a feature is enabled for a tenant.
func (c *PolicyChecker) IsFeatureEnabled(tenantID, feature string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	policy, ok := c.policies[tenantID]
	if !ok {
		return true // Default enabled
	}

	for _, enabled := range policy.EnabledFeatures {
		if enabled == feature {
			return true
		}
	}

	return false
}
