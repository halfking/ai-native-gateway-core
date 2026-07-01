package tenant

import (
	"context"
	"fmt"
	"sync"
)

// QuotaChecker checks tenant resource quotas.
type QuotaChecker struct {
	mu     sync.RWMutex
	quotas map[string]*TenantQuota
}

// NewQuotaChecker creates a new quota checker.
func NewQuotaChecker() *QuotaChecker {
	return &QuotaChecker{
		quotas: make(map[string]*TenantQuota),
	}
}

// SetQuota sets quota for a tenant.
func (c *QuotaChecker) SetQuota(quota *TenantQuota) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quotas[quota.TenantID] = quota
}

// CheckQuota checks if a tenant has quota available.
func (c *QuotaChecker) CheckQuota(ctx context.Context, tenantID string, tokensRequested int64) error {
	c.mu.RLock()
	_, ok := c.quotas[tenantID]
	c.mu.RUnlock()

	if !ok {
		// No quota configured - allow by default
		return nil
	}

	// TODO: Implement actual quota tracking with Redis/DB
	// This is a placeholder implementation
	_ = tokensRequested

	return nil
}

// GetQuota retrieves quota for a tenant.
func (c *QuotaChecker) GetQuota(tenantID string) (*TenantQuota, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	quota, ok := c.quotas[tenantID]
	if !ok {
		return nil, fmt.Errorf("no quota configured for tenant %s", tenantID)
	}

	return quota, nil
}
