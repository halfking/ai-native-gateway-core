package casdoor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// CasdoorApprovalProvider implements ApprovalProvider interface using Casdoor.
// This is a hybrid implementation:
// - Configuration and approvers are stored in Casdoor (via API)
// - Execution queue remains in local PostgreSQL (domains/sessionaudit)
// - ID mapping between Casdoor UUIDs and local integer IDs
type CasdoorApprovalProvider struct {
	client   *http.Client
	endpoint string
	clientID string
	clientSecret string
	idMapper *IDMapper
	orgName  string
	appName  string
}

// NewCasdoorApprovalProvider creates a new Casdoor approval provider.
func NewCasdoorApprovalProvider(client *http.Client, endpoint, clientID, clientSecret string, idMapper *IDMapper, orgName, appName string) *CasdoorApprovalProvider {
	return &CasdoorApprovalProvider{
		client:   client,
		endpoint: endpoint,
		clientID: clientID,
		clientSecret: clientSecret,
		idMapper: idMapper,
		orgName:  orgName,
		appName:  appName,
	}
}

// Name returns the provider identifier.
func (p *CasdoorApprovalProvider) Name() string {
	return "casdoor"
}

// GetConfig retrieves approval configuration for a tenant.
// In Casdoor, this is stored as organization-level settings.
func (p *CasdoorApprovalProvider) GetConfig(ctx context.Context, tenantID string) (*authplugin.ApprovalConfig, error) {
	if p.client == nil {
		return nil, authplugin.ErrProviderUnavailable
	}

	// Map tenant ID to Casdoor organization
	orgName, err := p.mapTenantToOrg(tenantID)
	if err != nil {
		return nil, fmt.Errorf("casdoor approval: map tenant: %w", err)
	}

	// Fetch organization settings from Casdoor
	// Casdoor stores custom properties in organization.Tags or organization.Properties
	endpoint := fmt.Sprintf("/api/get-organization?id=%s/%s", p.orgName, orgName)
	
	var org casdoorOrganization
	if err := p.doGet(ctx, endpoint, &org); err != nil {
		return nil, fmt.Errorf("casdoor approval: get organization: %w", err)
	}

	// Extract approval config from organization properties
	config := p.extractApprovalConfig(&org)
	return config, nil
}

// UpdateConfig updates approval configuration.
func (p *CasdoorApprovalProvider) UpdateConfig(ctx context.Context, tenantID string, config *authplugin.ApprovalConfig) error {
	if p.client == nil {
		return authplugin.ErrProviderUnavailable
	}

	if config == nil {
		return fmt.Errorf("casdoor approval: config is required")
	}

	orgName, err := p.mapTenantToOrg(tenantID)
	if err != nil {
		return fmt.Errorf("casdoor approval: map tenant: %w", err)
	}

	// Fetch existing organization
	endpoint := fmt.Sprintf("/api/get-organization?id=%s/%s", p.orgName, orgName)
	var org casdoorOrganization
	if err := p.doGet(ctx, endpoint, &org); err != nil {
		return fmt.Errorf("casdoor approval: get organization: %w", err)
	}

	// Update approval config in organization properties
	p.injectApprovalConfig(&org, config)

	// Save back to Casdoor
	updateEndpoint := "/api/update-organization"
	if err := p.doPost(ctx, updateEndpoint, &org); err != nil {
		return fmt.Errorf("casdoor approval: update organization: %w", err)
	}

	return nil
}

// GetApprovers lists approvers for a tenant.
// Approvers are Casdoor users with specific roles in the organization.
func (p *CasdoorApprovalProvider) GetApprovers(ctx context.Context, tenantID string) ([]*authplugin.Approver, error) {
	if p.client == nil {
		return nil, authplugin.ErrProviderUnavailable
	}

	orgName, err := p.mapTenantToOrg(tenantID)
	if err != nil {
		return nil, fmt.Errorf("casdoor approval: map tenant: %w", err)
	}

	// Query users with approver role in this organization
	endpoint := fmt.Sprintf("/api/get-users?owner=%s&organization=%s", p.orgName, orgName)
	
	var users []casdoorUser
	if err := p.doGet(ctx, endpoint, &users); err != nil {
		return nil, fmt.Errorf("casdoor approval: get users: %w", err)
	}

		// Filter users with approver-related roles and convert to plugin model
		var approvers []*authplugin.Approver
		for _, user := range users {
			if p.isApproverRole(user.Type) {
				// Map Casdoor UUID to local user ID using MapOrCreate
				localUserID, err := p.idMapper.MapOrCreate(ctx, "casdoor", user.Name, orgName, user.Name, user.DisplayName, user.Email)
				if err != nil {
					// Skip users that don't have local mapping
					continue
				}

				approvers = append(approvers, &authplugin.Approver{
					UserID:      fmt.Sprintf("%d", localUserID),
					Username:    user.Name,
					DisplayName: user.DisplayName,
					Email:       user.Email,
					Priority:    p.extractPriority(user.Properties),
					Enabled:     !user.IsForbidden,
				})
			}
		}

		return approvers, nil
}

// AddApprover adds an approver to a tenant.
func (p *CasdoorApprovalProvider) AddApprover(ctx context.Context, tenantID string, approver *authplugin.Approver) error {
	if p.client == nil {
		return authplugin.ErrProviderUnavailable
	}

	if approver == nil {
		return fmt.Errorf("casdoor approval: approver is required")
	}

	// In Casdoor, adding an approver means:
	// 1. Ensure user exists
	// 2. Add user to organization with approver role
	// 3. Set priority in user properties

	_, err := p.mapTenantToOrg(tenantID)
	if err != nil {
		return fmt.Errorf("casdoor approval: map tenant: %w", err)
	}

	// Get or create user
	userEndpoint := fmt.Sprintf("/api/get-user?id=%s/%s", p.orgName, approver.Username)
	var user casdoorUser
	err = p.doGet(ctx, userEndpoint, &user)
	if err != nil {
		// User doesn't exist, would need to create
		return fmt.Errorf("casdoor approval: user not found: %s", approver.Username)
	}

	// Update user properties to mark as approver
	if user.Properties == nil {
		user.Properties = make(map[string]string)
	}
	user.Properties["approver_priority"] = fmt.Sprintf("%d", approver.Priority)
	user.Properties["approver_enabled"] = fmt.Sprintf("%t", approver.Enabled)
	user.Type = "approver" // Set user type to approver

	// Update user
	updateEndpoint := "/api/update-user"
	if err := p.doPost(ctx, updateEndpoint, &user); err != nil {
		return fmt.Errorf("casdoor approval: update user: %w", err)
	}

	return nil
}

// UpdateApprover updates an existing approver.
func (p *CasdoorApprovalProvider) UpdateApprover(ctx context.Context, tenantID, userID string, approver *authplugin.Approver) error {
	// Same as AddApprover - Casdoor uses upsert semantics
	return p.AddApprover(ctx, tenantID, approver)
}

// RemoveApprover removes an approver from a tenant.
func (p *CasdoorApprovalProvider) RemoveApprover(ctx context.Context, tenantID, userID string) error {
	if p.client == nil {
		return authplugin.ErrProviderUnavailable
	}

	// Map local user ID to Casdoor username
	localID := 0
	fmt.Sscanf(userID, "%d", &localID)
	
	// Use GetByInternalID to lookup Casdoor external ID
	_, casdoorName, _, err := p.idMapper.GetByInternalID(ctx, localID)
	if err != nil {
		return fmt.Errorf("casdoor approval: map user ID: %w", err)
	}

	// Get user
	userEndpoint := fmt.Sprintf("/api/get-user?id=%s/%s", p.orgName, casdoorName)
	var user casdoorUser
	if err := p.doGet(ctx, userEndpoint, &user); err != nil {
		return fmt.Errorf("casdoor approval: get user: %w", err)
	}

	// Remove approver properties
	if user.Properties != nil {
		delete(user.Properties, "approver_priority")
		delete(user.Properties, "approver_enabled")
	}
	user.Type = "normal-user" // Reset to normal user

	// Update user
	updateEndpoint := "/api/update-user"
	if err := p.doPost(ctx, updateEndpoint, &user); err != nil {
		return fmt.Errorf("casdoor approval: update user: %w", err)
	}

	return nil
}

// GetRules lists approval rules for a tenant.
func (p *CasdoorApprovalProvider) GetRules(ctx context.Context, tenantID string) ([]*authplugin.ApprovalRule, error) {
	if p.client == nil {
		return nil, authplugin.ErrProviderUnavailable
	}

	// Rules are stored in organization properties
	// Extract rules from config (stored as JSON in org properties)
	orgName, _ := p.mapTenantToOrg(tenantID)
	endpoint := fmt.Sprintf("/api/get-organization?id=%s/%s", p.orgName, orgName)
	
	var org casdoorOrganization
	if err := p.doGet(ctx, endpoint, &org); err != nil {
		return nil, fmt.Errorf("casdoor approval: get organization: %w", err)
	}

	rules := p.extractRules(&org)
	return rules, nil
}

// AddRule adds an approval rule.
func (p *CasdoorApprovalProvider) AddRule(ctx context.Context, tenantID string, rule *authplugin.ApprovalRule) error {
	if p.client == nil {
		return authplugin.ErrProviderUnavailable
	}

	if rule == nil {
		return fmt.Errorf("casdoor approval: rule is required")
	}

	orgName, err := p.mapTenantToOrg(tenantID)
	if err != nil {
		return fmt.Errorf("casdoor approval: map tenant: %w", err)
	}

	// Get existing organization
	endpoint := fmt.Sprintf("/api/get-organization?id=%s/%s", p.orgName, orgName)
	var org casdoorOrganization
	if err := p.doGet(ctx, endpoint, &org); err != nil {
		return fmt.Errorf("casdoor approval: get organization: %w", err)
	}

	// Add rule to organization properties
	p.injectRule(&org, rule)

	// Save back to Casdoor
	updateEndpoint := "/api/update-organization"
	if err := p.doPost(ctx, updateEndpoint, &org); err != nil {
		return fmt.Errorf("casdoor approval: update organization: %w", err)
	}

	return nil
}

// RemoveRule removes an approval rule by name.
func (p *CasdoorApprovalProvider) RemoveRule(ctx context.Context, tenantID, ruleName string) error {
	if p.client == nil {
		return authplugin.ErrProviderUnavailable
	}

	orgName, err := p.mapTenantToOrg(tenantID)
	if err != nil {
		return fmt.Errorf("casdoor approval: map tenant: %w", err)
	}

	// Get existing organization
	endpoint := fmt.Sprintf("/api/get-organization?id=%s/%s", p.orgName, orgName)
	var org casdoorOrganization
	if err := p.doGet(ctx, endpoint, &org); err != nil {
		return fmt.Errorf("casdoor approval: get organization: %w", err)
	}

	// Remove rule from organization properties
	p.removeRule(&org, ruleName)

	// Save back to Casdoor
	updateEndpoint := "/api/update-organization"
	if err := p.doPost(ctx, updateEndpoint, &org); err != nil {
		return fmt.Errorf("casdoor approval: update organization: %w", err)
	}

	return nil
}

// SupportsFeature checks if provider supports an optional feature.
func (p *CasdoorApprovalProvider) SupportsFeature(feature authplugin.ApprovalFeature) bool {
	switch feature {
	case authplugin.FeatureMultiLevelApproval:
		return true
	case authplugin.FeatureApprovalDelegation:
		return false // Not implemented
	case authplugin.FeatureApprovalEscalation:
		return true
	default:
		return false
	}
}

// Helper methods

func (p *CasdoorApprovalProvider) mapTenantToOrg(tenantID string) (string, error) {
	// For now, use direct mapping (tenant ID = org name)
	// In production, this might need a lookup table
	if tenantID == "" || tenantID == "default" {
		return p.orgName, nil
	}
	return tenantID, nil
}

func (p *CasdoorApprovalProvider) isApproverRole(userType string) bool {
	// Check if user type indicates approver role
	return userType == "approver" || userType == "admin" || userType == "auditor"
}

func (p *CasdoorApprovalProvider) extractPriority(properties map[string]string) int {
	if properties == nil {
		return 100 // Default priority
	}
	priorityStr, ok := properties["approver_priority"]
	if !ok {
		return 100
	}
	var priority int
	fmt.Sscanf(priorityStr, "%d", &priority)
	return priority
}

func (p *CasdoorApprovalProvider) extractApprovalConfig(org *casdoorOrganization) *authplugin.ApprovalConfig {
	// Extract approval config from organization tags/properties
	config := &authplugin.ApprovalConfig{
		Enabled:          false,
		RequireApproval:  false,
		TimeoutMinutes:   15,
		TimeoutAction:    "deny",
		NotifyOnPending:  true,
		NotifyOnApproved: true,
		NotifyOnRejected: true,
	}

	if org.Tags == nil {
		return config
	}

	// Parse approval config from tags (stored as JSON)
	for _, tag := range org.Tags {
		if len(tag) > 15 && tag[:15] == "approval_config" {
			// Extract JSON after the "=" sign
			parts := []byte(tag[16:]) // Skip "approval_config="
			json.Unmarshal(parts, config)
			break
		}
	}

	return config
}

func (p *CasdoorApprovalProvider) injectApprovalConfig(org *casdoorOrganization, config *authplugin.ApprovalConfig) {
	if org.Tags == nil {
		org.Tags = make([]string, 0)
	}

	// Serialize config to JSON and store in tags
	configJSON, _ := json.Marshal(config)
	
	// Casdoor uses Tags as string array, so we need to store as key=value
	configStr := fmt.Sprintf("approval_config=%s", string(configJSON))
	
	// Remove old config if exists
	newTags := []string{}
	for _, tag := range org.Tags {
		if len(tag) < 15 || tag[:15] != "approval_config" {
			newTags = append(newTags, tag)
		}
	}
	newTags = append(newTags, configStr)
	org.Tags = newTags
}

func (p *CasdoorApprovalProvider) extractRules(org *casdoorOrganization) []*authplugin.ApprovalRule {
	var rules []*authplugin.ApprovalRule

	if org.Tags == nil {
		return rules
	}

	// Parse rules from tags
	for _, tag := range org.Tags {
		if len(tag) > 14 && tag[:14] == "approval_rule=" {
			var rule authplugin.ApprovalRule
			json.Unmarshal([]byte(tag[14:]), &rule)
			rules = append(rules, &rule)
		}
	}

	return rules
}

func (p *CasdoorApprovalProvider) injectRule(org *casdoorOrganization, rule *authplugin.ApprovalRule) {
	if org.Tags == nil {
		org.Tags = make([]string, 0)
	}

	// Serialize rule to JSON
	ruleJSON, _ := json.Marshal(rule)
	ruleStr := fmt.Sprintf("approval_rule=%s", string(ruleJSON))
	
	org.Tags = append(org.Tags, ruleStr)
}

func (p *CasdoorApprovalProvider) removeRule(org *casdoorOrganization, ruleName string) {
	if org.Tags == nil {
		return
	}

	// Remove rule with matching name
	newTags := []string{}
	for _, tag := range org.Tags {
		if len(tag) > 14 && tag[:14] == "approval_rule=" {
			var rule authplugin.ApprovalRule
			json.Unmarshal([]byte(tag[14:]), &rule)
			if rule.Name != ruleName {
				newTags = append(newTags, tag)
			}
		} else {
			newTags = append(newTags, tag)
		}
	}
	org.Tags = newTags
}

// HTTP helper methods

// doGet performs a GET request to Casdoor API
func (p *CasdoorApprovalProvider) doGet(ctx context.Context, path string, result interface{}) error {
	url := p.endpoint + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	
	// Add authentication headers if needed
	if p.clientID != "" && p.clientSecret != "" {
		req.SetBasicAuth(p.clientID, p.clientSecret)
	}
	
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("casdoor API error: status %d", resp.StatusCode)
	}
	
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	
	return nil
}

// doPost performs a POST request to Casdoor API
func (p *CasdoorApprovalProvider) doPost(ctx context.Context, path string, body interface{}) error {
	url := p.endpoint + path
	
	jsonData, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	
	req.Header.Set("Content-Type", "application/json")
	if p.clientID != "" && p.clientSecret != "" {
		req.SetBasicAuth(p.clientID, p.clientSecret)
	}
	
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("casdoor API error: status %d", resp.StatusCode)
	}
	
	return nil
}

// casdoorOrganization represents Casdoor organization structure
type casdoorOrganization struct {
	Owner       string            `json:"owner"`
	Name        string            `json:"name"`
	CreatedTime string            `json:"createdTime"`
	DisplayName string            `json:"displayName"`
	WebsiteUrl  string            `json:"websiteUrl"`
	Favicon     string            `json:"favicon"`
	PasswordType string           `json:"passwordType"`
	Tags        []string          `json:"tags"`
	Properties  map[string]string `json:"properties,omitempty"`
}

// casdoorUser represents Casdoor user structure
type casdoorUser struct {
	Owner         string            `json:"owner"`
	Name          string            `json:"name"`
	CreatedTime   string            `json:"createdTime"`
	UpdatedTime   string            `json:"updatedTime"`
	Id            string            `json:"id"`
	Type          string            `json:"type"`
	Password      string            `json:"password,omitempty"`
	DisplayName   string            `json:"displayName"`
	Avatar        string            `json:"avatar"`
	Email         string            `json:"email"`
	Phone         string            `json:"phone"`
	Address       []string          `json:"address"`
	Affiliation   string            `json:"affiliation"`
	Tag           string            `json:"tag"`
	IsAdmin       bool              `json:"isAdmin"`
	IsForbidden   bool              `json:"isForbidden"`
	IsDeleted     bool              `json:"isDeleted"`
	Properties    map[string]string `json:"properties,omitempty"`
}
