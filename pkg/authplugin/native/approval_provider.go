package native

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// NativeApprovalProvider implements ApprovalProvider interface using PostgreSQL.
// It wraps the existing domains/approval.ConfigManager to provide a plugin-compatible interface.
type NativeApprovalProvider struct {
	configManager *approval.ConfigManager
}

// NewNativeApprovalProvider creates a new native approval provider.
// It wraps the existing approval.ConfigManager to maintain backward compatibility.
func NewNativeApprovalProvider(configManager *approval.ConfigManager) *NativeApprovalProvider {
	return &NativeApprovalProvider{
		configManager: configManager,
	}
}

// Name returns the provider identifier.
func (p *NativeApprovalProvider) Name() string {
	return "native"
}

// GetConfig retrieves approval configuration for a tenant.
func (p *NativeApprovalProvider) GetConfig(ctx context.Context, tenantID string) (*authplugin.ApprovalConfig, error) {
	if p.configManager == nil {
		return nil, authplugin.ErrProviderUnavailable
	}

	domainConfig, err := p.configManager.GetConfig(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("native approval: get config: %w", err)
	}

	// Convert domain model to plugin model
	return convertDomainToPluginConfig(domainConfig), nil
}

// UpdateConfig updates approval configuration.
func (p *NativeApprovalProvider) UpdateConfig(ctx context.Context, tenantID string, config *authplugin.ApprovalConfig) error {
	if p.configManager == nil {
		return authplugin.ErrProviderUnavailable
	}

	if config == nil {
		return fmt.Errorf("native approval: config is required")
	}

	// Convert plugin model to domain model
	domainConfig := convertPluginToDomainConfig(tenantID, config)

	if err := p.configManager.UpdateConfig(ctx, tenantID, domainConfig); err != nil {
		return fmt.Errorf("native approval: update config: %w", err)
	}

	return nil
}

// GetApprovers lists approvers for a tenant.
func (p *NativeApprovalProvider) GetApprovers(ctx context.Context, tenantID string) ([]*authplugin.Approver, error) {
	if p.configManager == nil {
		return nil, authplugin.ErrProviderUnavailable
	}

	domainApprovers, err := p.configManager.GetApprovers(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("native approval: get approvers: %w", err)
	}

	// Convert domain approvers to plugin approvers
	pluginApprovers := make([]*authplugin.Approver, len(domainApprovers))
	for i, da := range domainApprovers {
		pluginApprovers[i] = &authplugin.Approver{
			UserID:      da.UserID,
			Username:    da.Name, // Domain uses Name, plugin uses Username
			DisplayName: da.Name,
			Email:       da.Email,
			Priority:    da.Priority,
			Enabled:     da.Enabled,
		}
	}

	return pluginApprovers, nil
}

// AddApprover adds an approver to a tenant.
func (p *NativeApprovalProvider) AddApprover(ctx context.Context, tenantID string, approver *authplugin.Approver) error {
	if p.configManager == nil {
		return authplugin.ErrProviderUnavailable
	}

	if approver == nil {
		return fmt.Errorf("native approval: approver is required")
	}

	// Convert plugin approver to domain approver
	domainApprover := &approval.Approver{
		UserID:   approver.UserID,
		Name:     approver.DisplayName,
		Email:    approver.Email,
		Phone:    "", // Not in plugin model
		Role:     "approver",
		Priority: approver.Priority,
		Enabled:  approver.Enabled,
	}

	if err := p.configManager.AddApprover(ctx, tenantID, domainApprover); err != nil {
		return fmt.Errorf("native approval: add approver: %w", err)
	}

	return nil
}

// UpdateApprover updates an existing approver.
func (p *NativeApprovalProvider) UpdateApprover(ctx context.Context, tenantID, userID string, approver *authplugin.Approver) error {
	if p.configManager == nil {
		return authplugin.ErrProviderUnavailable
	}

	if approver == nil {
		return fmt.Errorf("native approval: approver is required")
	}

	// Convert plugin approver to domain approver
	domainApprover := &approval.Approver{
		UserID:   userID,
		Name:     approver.DisplayName,
		Email:    approver.Email,
		Phone:    "",
		Role:     "approver",
		Priority: approver.Priority,
		Enabled:  approver.Enabled,
	}

	if err := p.configManager.UpdateApprover(ctx, tenantID, userID, domainApprover); err != nil {
		return fmt.Errorf("native approval: update approver: %w", err)
	}

	return nil
}

// RemoveApprover removes an approver from a tenant.
func (p *NativeApprovalProvider) RemoveApprover(ctx context.Context, tenantID, userID string) error {
	if p.configManager == nil {
		return authplugin.ErrProviderUnavailable
	}

	if err := p.configManager.RemoveApprover(ctx, tenantID, userID); err != nil {
		return fmt.Errorf("native approval: remove approver: %w", err)
	}

	return nil
}

// GetRules lists approval rules for a tenant.
func (p *NativeApprovalProvider) GetRules(ctx context.Context, tenantID string) ([]*authplugin.ApprovalRule, error) {
	if p.configManager == nil {
		return nil, authplugin.ErrProviderUnavailable
	}

	domainRules, err := p.configManager.GetRules(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("native approval: get rules: %w", err)
	}

	// Convert domain rules to plugin rules
	pluginRules := make([]*authplugin.ApprovalRule, len(domainRules))
	for i, dr := range domainRules {
		pluginRules[i] = &authplugin.ApprovalRule{
			Name:        dr.Name,
			Description: dr.Action.Reason,
			Conditions:  convertConditions(dr.Conditions),
			Priority:    dr.Priority,
			Enabled:     dr.Enabled,
		}
	}

	return pluginRules, nil
}

// AddRule adds an approval rule.
func (p *NativeApprovalProvider) AddRule(ctx context.Context, tenantID string, rule *authplugin.ApprovalRule) error {
	if p.configManager == nil {
		return authplugin.ErrProviderUnavailable
	}

	if rule == nil {
		return fmt.Errorf("native approval: rule is required")
	}

	// Convert plugin rule to domain rule
	domainRule := &approval.ApprovalRule{
		Name:       rule.Name,
		Enabled:    rule.Enabled,
		Priority:   rule.Priority,
		Conditions: convertPluginConditionsToDomain(rule.Conditions),
		Action: approval.RuleAction{
			Type:      "require_approval",
			RiskLevel: approval.RiskMedium,
			Reason:    rule.Description,
		},
	}

	if err := p.configManager.AddRule(ctx, tenantID, domainRule); err != nil {
		return fmt.Errorf("native approval: add rule: %w", err)
	}

	return nil
}

// RemoveRule removes an approval rule by name.
func (p *NativeApprovalProvider) RemoveRule(ctx context.Context, tenantID, ruleName string) error {
	if p.configManager == nil {
		return authplugin.ErrProviderUnavailable
	}

	if err := p.configManager.RemoveRule(ctx, tenantID, ruleName); err != nil {
		return fmt.Errorf("native approval: remove rule: %w", err)
	}

	return nil
}

// SupportsFeature checks if provider supports an optional feature.
func (p *NativeApprovalProvider) SupportsFeature(feature authplugin.ApprovalFeature) bool {
	switch feature {
	case authplugin.FeatureMultiLevelApproval:
		return true // Native supports priority-based approvers
	case authplugin.FeatureApprovalDelegation:
		return false // Not implemented yet
	case authplugin.FeatureApprovalEscalation:
		return true // Native supports timeout actions
	default:
		return false
	}
}

// Helper functions for model conversion

func convertDomainToPluginConfig(dc *approval.ApprovalConfig) *authplugin.ApprovalConfig {
	if dc == nil {
		return nil
	}

	// Map domain ApprovalMode to plugin config fields
	enabled := dc.Enabled && dc.Mode != approval.ModeDisabled
	requireApproval := dc.Mode == approval.ModeManual

	timeoutAction := "deny"
	if !dc.AutoRejectOnTimeout {
		timeoutAction = "auto_approve"
	}

	return &authplugin.ApprovalConfig{
		Enabled:          enabled,
		RequireApproval:  requireApproval,
		TimeoutMinutes:   dc.TimeoutSeconds / 60,
		TimeoutAction:    timeoutAction,
		NotifyOnPending:  len(dc.Channels) > 0,
		NotifyOnApproved: len(dc.Channels) > 0,
		NotifyOnRejected: len(dc.Channels) > 0,
	}
}

func convertPluginToDomainConfig(tenantID string, pc *authplugin.ApprovalConfig) *approval.ApprovalConfig {
	if pc == nil {
		return nil
	}

	// Map plugin config to domain ApprovalMode
	mode := approval.ModeDisabled
	if pc.Enabled {
		if pc.RequireApproval {
			mode = approval.ModeManual
		} else {
			mode = approval.ModeAutomatic
		}
	}

	autoRejectOnTimeout := pc.TimeoutAction == "deny"

	return &approval.ApprovalConfig{
		TenantID:            tenantID,
		Enabled:             pc.Enabled,
		Mode:                mode,
		TimeoutSeconds:      pc.TimeoutMinutes * 60,
		AutoRejectOnTimeout: autoRejectOnTimeout,
		Approvers:           []approval.Approver{},
		Channels:            []approval.NotificationChannel{},
		Rules:               []approval.ApprovalRule{},
	}
}

func convertConditions(domainConditions []approval.RuleCondition) map[string]interface{} {
	result := make(map[string]interface{})
	for i, dc := range domainConditions {
		key := fmt.Sprintf("condition_%d", i)
		result[key] = map[string]string{
			"field":    dc.Field,
			"operator": dc.Operator,
			"value":    dc.Value,
		}
	}
	return result
}

func convertPluginConditionsToDomain(pluginConditions map[string]interface{}) []approval.RuleCondition {
	var result []approval.RuleCondition
	
	// Convert map to slice - conditions are stored as a map in plugin model
	// but as a slice in domain model
	for _, v := range pluginConditions {
		if condMap, ok := v.(map[string]interface{}); ok {
			field, _ := condMap["field"].(string)
			operator, _ := condMap["operator"].(string)
			value, _ := condMap["value"].(string)
			
			result = append(result, approval.RuleCondition{
				Field:    field,
				Operator: operator,
				Value:    value,
			})
		}
	}
	
	return result
}
