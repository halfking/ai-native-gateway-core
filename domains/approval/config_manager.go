package approval

import (
	"context"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"time"

	"github.com/redis/go-redis/v9"
)

// ConfigManager manages approval configuration with caching.
type ConfigManager struct {
	store ApprovalStore
	cache *redis.Client
}

// NewConfigManager creates a new configuration manager.
func NewConfigManager(store ApprovalStore, cache *redis.Client) *ConfigManager {
	return &ConfigManager{
		store: store,
		cache: cache,
	}
}

// GetConfig retrieves approval configuration for a tenant.
// Uses cache if available, falls back to database.
func (m *ConfigManager) GetConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}

	// Try to get from store (which has its own cache layer)
	config, err := m.store.GetConfig(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}

	return config, nil
}

// UpdateConfig updates approval configuration for a tenant.
// Validates the configuration before saving.
func (m *ConfigManager) UpdateConfig(ctx context.Context, tenantID string, config *ApprovalConfig) error {
	if tenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}

	if config == nil {
		return fmt.Errorf("config is required")
	}

	// Ensure tenant ID matches
	config.TenantID = tenantID

	// Validate configuration
	if err := m.validateConfig(config); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Save to store
	if err := m.store.SaveConfig(ctx, config); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	return nil
}

// GetApprovers retrieves all approvers for a tenant.
func (m *ConfigManager) GetApprovers(ctx context.Context, tenantID string) ([]Approver, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}

	approvers, err := m.store.GetApprovers(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get approvers: %w", err)
	}

	return approvers, nil
}

// AddApprover adds or updates an approver for a tenant.
func (m *ConfigManager) AddApprover(ctx context.Context, tenantID string, approver *Approver) error {
	if tenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}

	if approver == nil {
		return fmt.Errorf("approver is required")
	}

	// Validate approver
	if err := m.validateApprover(approver); err != nil {
		return fmt.Errorf("invalid approver: %w", err)
	}

	// Save to store
	if err := m.store.SaveApprover(ctx, tenantID, approver); err != nil {
		return fmt.Errorf("save approver: %w", err)
	}

	return nil
}

// UpdateApprover updates an existing approver for a tenant.
func (m *ConfigManager) UpdateApprover(ctx context.Context, tenantID string, userID string, approver *Approver) error {
	if tenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}

	if userID == "" {
		return fmt.Errorf("user_id is required")
	}

	if approver == nil {
		return fmt.Errorf("approver is required")
	}

	// Ensure user ID matches
	approver.UserID = userID

	// Validate approver
	if err := m.validateApprover(approver); err != nil {
		return fmt.Errorf("invalid approver: %w", err)
	}

	// Save to store
	if err := m.store.SaveApprover(ctx, tenantID, approver); err != nil {
		return fmt.Errorf("update approver: %w", err)
	}

	return nil
}

// RemoveApprover removes an approver from a tenant.
func (m *ConfigManager) RemoveApprover(ctx context.Context, tenantID, userID string) error {
	if tenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}

	if userID == "" {
		return fmt.Errorf("user_id is required")
	}

	if err := m.store.DeleteApprover(ctx, tenantID, userID); err != nil {
		return fmt.Errorf("delete approver: %w", err)
	}

	return nil
}

// GetRules retrieves all approval rules for a tenant.
func (m *ConfigManager) GetRules(ctx context.Context, tenantID string) ([]ApprovalRule, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}

	rules, err := m.store.GetRules(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get rules: %w", err)
	}

	return rules, nil
}

// AddRule adds or updates a rule for a tenant.
func (m *ConfigManager) AddRule(ctx context.Context, tenantID string, rule *ApprovalRule) error {
	if tenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}

	if rule == nil {
		return fmt.Errorf("rule is required")
	}

	// Validate rule
	if err := m.validateRule(rule); err != nil {
		return fmt.Errorf("invalid rule: %w", err)
	}

	// Save to store
	if err := m.store.SaveRule(ctx, tenantID, rule); err != nil {
		return fmt.Errorf("save rule: %w", err)
	}

	return nil
}

// RemoveRule removes a rule from a tenant.
func (m *ConfigManager) RemoveRule(ctx context.Context, tenantID, ruleName string) error {
	if tenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}

	if ruleName == "" {
		return fmt.Errorf("rule_name is required")
	}

	if err := m.store.DeleteRule(ctx, tenantID, ruleName); err != nil {
		return fmt.Errorf("delete rule: %w", err)
	}

	return nil
}

// Validation methods

func (m *ConfigManager) validateConfig(config *ApprovalConfig) error {
	// Validate mode
	switch config.Mode {
	case ModeDisabled, ModeAutomatic, ModeManual:
		// Valid
	default:
		return fmt.Errorf("invalid mode: %s (must be disabled, automatic, or manual)", config.Mode)
	}

	// Validate timeout
	if config.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds must be >= 0")
	}
	if config.TimeoutSeconds > 86400 { // Max 24 hours
		return fmt.Errorf("timeout_seconds must be <= 86400 (24 hours)")
	}

	// Validate approvers
	for i, approver := range config.Approvers {
		if err := m.validateApprover(&approver); err != nil {
			return fmt.Errorf("approver[%d]: %w", i, err)
		}
	}

	// Validate notification channels
	for i, channel := range config.Channels {
		if err := m.validateChannel(&channel); err != nil {
			return fmt.Errorf("channel[%d]: %w", i, err)
		}
	}

	// Validate rules
	for i, rule := range config.Rules {
		if err := m.validateRule(&rule); err != nil {
			return fmt.Errorf("rule[%d]: %w", i, err)
		}
	}

	return nil
}

func (m *ConfigManager) validateApprover(approver *Approver) error {
	if approver.UserID == "" {
		return fmt.Errorf("user_id is required")
	}

	if approver.Name == "" {
		return fmt.Errorf("name is required")
	}

	// Validate email format if provided
	if approver.Email != "" {
		if _, err := mail.ParseAddress(approver.Email); err != nil {
			return fmt.Errorf("invalid email format: %w", err)
		}
	}

	// Validate role
	switch approver.Role {
	case "admin", "auditor", "manager", "approver":
		// Valid roles
	default:
		return fmt.Errorf("invalid role: %s (must be admin, auditor, manager, or approver)", approver.Role)
	}

	// Validate priority
	if approver.Priority < 0 {
		return fmt.Errorf("priority must be >= 0")
	}

	return nil
}

func (m *ConfigManager) validateChannel(channel *NotificationChannel) error {
	// Validate channel type
	switch channel.Type {
	case ChannelFeishu, ChannelWeChat, ChannelDingTalk, ChannelEmail, ChannelWebhook:
		// Valid
	default:
		return fmt.Errorf("invalid channel type: %s", channel.Type)
	}

	// Validate webhook URL if present
	if channel.Type == ChannelWebhook {
		if webhookURL, ok := channel.Config["webhook_url"]; ok && webhookURL != "" {
			if _, err := url.ParseRequestURI(webhookURL); err != nil {
				return fmt.Errorf("invalid webhook_url: %w", err)
			}
		}
	}

	// Validate Feishu webhook
	if channel.Type == ChannelFeishu {
		if webhookURL, ok := channel.Config["webhook_url"]; ok && webhookURL != "" {
			if _, err := url.ParseRequestURI(webhookURL); err != nil {
				return fmt.Errorf("invalid feishu webhook_url: %w", err)
			}
		}
	}

	return nil
}

func (m *ConfigManager) validateRule(rule *ApprovalRule) error {
	if rule.Name == "" {
		return fmt.Errorf("name is required")
	}

	if rule.Priority < 0 {
		return fmt.Errorf("priority must be >= 0")
	}

	// Validate conditions
	if len(rule.Conditions) == 0 {
		return fmt.Errorf("at least one condition is required")
	}

	for i, condition := range rule.Conditions {
		if err := m.validateCondition(&condition); err != nil {
			return fmt.Errorf("condition[%d]: %w", i, err)
		}
	}

	// Validate action
	if err := m.validateAction(&rule.Action); err != nil {
		return fmt.Errorf("action: %w", err)
	}

	return nil
}

func (m *ConfigManager) validateCondition(condition *RuleCondition) error {
	if condition.Field == "" {
		return fmt.Errorf("field is required")
	}

	// Validate field names
	validFields := map[string]bool{
		"message_content": true,
		"token_count":     true,
		"cost":            true,
		"tool_name":       true,
		"risk_level":      true,
		"model":           true,
		"user_message":    true,
	}
	if !validFields[condition.Field] {
		return fmt.Errorf("invalid field: %s", condition.Field)
	}

	// Validate operator
	validOperators := map[string]bool{
		"contains": true,
		"gt":       true,
		"gte":      true,
		"lt":       true,
		"lte":      true,
		"eq":       true,
		"ne":       true,
		"regex":    true,
	}
	if !validOperators[condition.Operator] {
		return fmt.Errorf("invalid operator: %s", condition.Operator)
	}

	// Validate regex if operator is regex
	if condition.Operator == "regex" {
		if _, err := regexp.Compile(condition.Value); err != nil {
			return fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	return nil
}

func (m *ConfigManager) validateAction(action *RuleAction) error {
	// Validate action type
	switch action.Type {
	case "require_approval", "auto_approve", "auto_reject":
		// Valid
	default:
		return fmt.Errorf("invalid action type: %s (must be require_approval, auto_approve, or auto_reject)", action.Type)
	}

	// Validate risk level
	switch action.RiskLevel {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		// Valid
	case "": // Optional
		// OK
	default:
		return fmt.Errorf("invalid risk_level: %s", action.RiskLevel)
	}

	return nil
}

// ClearCache clears the cached configuration for a tenant.
func (m *ConfigManager) ClearCache(ctx context.Context, tenantID string) error {
	if m.cache == nil {
		return nil
	}

	key := fmt.Sprintf("approval:config:%s", tenantID)
	if err := m.cache.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("clear cache: %w", err)
	}

	return nil
}

// RefreshConfig forces a reload of configuration from the database.
func (m *ConfigManager) RefreshConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error) {
	// Clear cache first
	if err := m.ClearCache(ctx, tenantID); err != nil {
		// Log but don't fail
		_ = err
	}

	// Force reload from database
	return m.GetConfig(ctx, tenantID)
}

// GetConfigStats returns statistics about approval configuration.
func (m *ConfigManager) GetConfigStats(ctx context.Context, tenantID string) (*ConfigStats, error) {
	config, err := m.GetConfig(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	stats := &ConfigStats{
		TenantID:          tenantID,
		Enabled:           config.Enabled,
		Mode:              string(config.Mode),
		ApproverCount:     len(config.Approvers),
		RuleCount:         len(config.Rules),
		ChannelCount:      len(config.Channels),
		TimeoutSeconds:    config.TimeoutSeconds,
		LastUpdated:       config.UpdatedAt,
	}

	// Count enabled items
	for _, approver := range config.Approvers {
		if approver.Enabled {
			stats.EnabledApprovers++
		}
	}

	for _, rule := range config.Rules {
		if rule.Enabled {
			stats.EnabledRules++
		}
	}

	for _, channel := range config.Channels {
		if channel.Enabled {
			stats.EnabledChannels++
		}
	}

	return stats, nil
}

// ConfigStats represents statistics about approval configuration.
type ConfigStats struct {
	TenantID         string    `json:"tenant_id"`
	Enabled          bool      `json:"enabled"`
	Mode             string    `json:"mode"`
	ApproverCount    int       `json:"approver_count"`
	EnabledApprovers int       `json:"enabled_approvers"`
	RuleCount        int       `json:"rule_count"`
	EnabledRules     int       `json:"enabled_rules"`
	ChannelCount     int       `json:"channel_count"`
	EnabledChannels  int       `json:"enabled_channels"`
	TimeoutSeconds   int       `json:"timeout_seconds"`
	LastUpdated      time.Time `json:"last_updated"`
}
