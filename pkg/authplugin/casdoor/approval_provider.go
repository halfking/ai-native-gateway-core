package casdoor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// CasdoorApprovalProvider implements ApprovalProvider interface using Casdoor.
// This is a hybrid implementation:
// - Configuration and approvers are stored in Casdoor (via API)
// - Execution queue remains in local PostgreSQL (domains/sessionaudit)
// - ID mapping between Casdoor UUIDs and local integer IDs
// approvalConfigTagPrefix 是审批配置在 Casdoor 组织 Tags 里的键前缀。
const approvalConfigTagPrefix = "approval_config="

// ApprovalIDMapper 是审批提供者所需的 ID 映射能力（依赖倒置）。
//
// ★ 抽接口的原因与 native 包同理：直接持有具体类型 *IDMapper 时，
//
//	单元测试无法注入替身 ⇒ 这条路径没有测试。
//	*IDMapper 的方法集天然满足该接口，生产行为不变。
type ApprovalIDMapper interface {
	MapOrCreate(ctx context.Context, provider, externalID, tenantID, username, displayName, email string) (int, error)
	GetByInternalID(ctx context.Context, internalID int) (provider, externalID, tenantID string, err error)
}

type CasdoorApprovalProvider struct {
	client       *http.Client
	endpoint     string
	clientID     string
	clientSecret string
	idMapper     ApprovalIDMapper
	orgName      string
	appName      string
}

// NewCasdoorApprovalProvider creates a new Casdoor approval provider.
//
// idMapper 可以为 nil —— 此时所有「需要把 Casdoor 主体映射到本地用户」的
// 操作都会**返回明确错误**而不是 panic（见 GetApprovers / AddApprover）。
func NewCasdoorApprovalProvider(client *http.Client, endpoint, clientID, clientSecret string, idMapper ApprovalIDMapper, orgName, appName string) *CasdoorApprovalProvider {
	return &CasdoorApprovalProvider{
		client:       client,
		endpoint:     endpoint,
		clientID:     clientID,
		clientSecret: clientSecret,
		idMapper:     idMapper,
		orgName:      orgName,
		appName:      appName,
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
	config, err := p.extractApprovalConfig(&org)
	if err != nil {
		return nil, err
	}
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
			//
			// ★ idMapper 为 nil 时不能直接解引用（会 panic）——
			//   无映射能力时**跳过该用户**，而不是让整个列表接口崩掉。
			if p.idMapper == nil {
				continue
			}
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
	//
	// ★ idMapper 为 nil 时必须**返回错误**而不是解引用 nil（panic）。
	if p.idMapper == nil {
		return fmt.Errorf("casdoor approval: id mapper is not configured")
	}
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

// defaultApprovalConfig 返回审批配置的默认值。
func defaultApprovalConfig() *authplugin.ApprovalConfig {
	return &authplugin.ApprovalConfig{
		Enabled:          false,
		RequireApproval:  false,
		TimeoutMinutes:   15,
		TimeoutAction:    "deny",
		NotifyOnPending:  true,
		NotifyOnApproved: true,
		NotifyOnRejected: true,
	}
}

// approvalConfigJSONAliases 给出审批配置 JSON 的**键名别名表**。
//
// ★ 为什么需要别名表：authplugin.ApprovalConfig 没有 json tag，
//
//	Go 侧写出的键是字段名（Enabled / RequireApproval / TimeoutMinutes…），
//	而同一份内容也可能由别的生产者或人工写成驼峰（requireApproval）
//	或下划线（require_approval）。
//	encoding/json 的匹配**只忽略大小写、不忽略下划线**
//	⇒ "require_approval" 匹配不上 "RequireApproval"
//	⇒ 该字段**保持默认值**（RequireApproval=false、TimeoutMinutes=15）。
//	★ 更糟的是：整段 JSON 若解析失败，旧实现**静默吞掉错误**并返回默认值，
//	  而默认值里 Enabled=false ⇒ **审批被悄悄关掉**（fail-open）。
//	  这正是本函数改为「解析失败即报错」的原因。
var approvalConfigJSONAliases = map[string]string{
	"enabled":            "Enabled",
	"requireapproval":    "RequireApproval",
	"require_approval":   "RequireApproval",
	"timeoutminutes":     "TimeoutMinutes",
	"timeout_minutes":    "TimeoutMinutes",
	"timeoutaction":      "TimeoutAction",
	"timeout_action":     "TimeoutAction",
	"notifyonpending":    "NotifyOnPending",
	"notify_on_pending":  "NotifyOnPending",
	"notifyonapproved":   "NotifyOnApproved",
	"notify_on_approved": "NotifyOnApproved",
	"notifyonrejected":   "NotifyOnRejected",
	"notify_on_rejected": "NotifyOnRejected",
}

// normalizeApprovalConfigKeys 把 JSON 对象的键改写成 Go 字段名，
// 使「字段名 / 驼峰 / 下划线」三种写法都能被 json.Unmarshal 正确识别。
func normalizeApprovalConfigKeys(raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		nk := strings.ToLower(k)
		if mapped, ok := approvalConfigJSONAliases[nk]; ok {
			nk = mapped
		}
		out[nk] = v
	}
	return json.Marshal(out)
}

// decodeApprovalConfigJSON 解析审批配置 JSON。
//
// 解析失败**返回错误**（不再静默回落默认值）——因为默认值 Enabled=false
// 意味着「审批不生效」，静默回落等于把安全闸悄悄打开。
func decodeApprovalConfigJSON(raw []byte) (*authplugin.ApprovalConfig, error) {
	normalized, err := normalizeApprovalConfigKeys(raw)
	if err != nil {
		return nil, fmt.Errorf("decode approval config: %w", err)
	}
	config := defaultApprovalConfig()
	if err := json.Unmarshal(normalized, config); err != nil {
		return nil, fmt.Errorf("decode approval config: %w", err)
	}
	return config, nil
}

func (p *CasdoorApprovalProvider) extractApprovalConfig(org *casdoorOrganization) (*authplugin.ApprovalConfig, error) {
	// Extract approval config from organization tags/properties
	config := defaultApprovalConfig()

	if org.Tags == nil {
		return config, nil
	}

	// Parse approval config from tags (stored as JSON)
	for _, tag := range org.Tags {
		if !strings.HasPrefix(tag, approvalConfigTagPrefix) {
			continue
		}
		// 去掉 "approval_config=" 前缀后解析
		parsed, err := decodeApprovalConfigJSON([]byte(strings.TrimPrefix(tag, approvalConfigTagPrefix)))
		if err != nil {
			return nil, fmt.Errorf("casdoor approval: parse %s tag: %w", approvalConfigTagPrefix, err)
		}
		return parsed, nil
	}

	return config, nil
}

func (p *CasdoorApprovalProvider) injectApprovalConfig(org *casdoorOrganization, config *authplugin.ApprovalConfig) {
	if org.Tags == nil {
		org.Tags = make([]string, 0)
	}

	// Serialize config to JSON and store in tags
	configJSON, _ := json.Marshal(config)

	// Casdoor uses Tags as string array, so we need to store as key=value
	configStr := approvalConfigTagPrefix + string(configJSON)

	// Remove old config if exists
	newTags := []string{}
	for _, tag := range org.Tags {
		if !strings.HasPrefix(tag, approvalConfigTagPrefix) {
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
	Owner        string            `json:"owner"`
	Name         string            `json:"name"`
	CreatedTime  string            `json:"createdTime"`
	DisplayName  string            `json:"displayName"`
	WebsiteUrl   string            `json:"websiteUrl"`
	Favicon      string            `json:"favicon"`
	PasswordType string            `json:"passwordType"`
	Tags         []string          `json:"tags"`
	Properties   map[string]string `json:"properties,omitempty"`
}

// casdoorUser represents Casdoor user structure
type casdoorUser struct {
	Owner       string            `json:"owner"`
	Name        string            `json:"name"`
	CreatedTime string            `json:"createdTime"`
	UpdatedTime string            `json:"updatedTime"`
	Id          string            `json:"id"`
	Type        string            `json:"type"`
	Password    string            `json:"password,omitempty"`
	DisplayName string            `json:"displayName"`
	Avatar      string            `json:"avatar"`
	Email       string            `json:"email"`
	Phone       string            `json:"phone"`
	Address     []string          `json:"address"`
	Affiliation string            `json:"affiliation"`
	Tag         string            `json:"tag"`
	IsAdmin     bool              `json:"isAdmin"`
	IsForbidden bool              `json:"isForbidden"`
	IsDeleted   bool              `json:"isDeleted"`
	Properties  map[string]string `json:"properties,omitempty"`
}
