package approvalconfig

// ApprovalConfig defines the approval system configuration.
type ApprovalConfig struct {
	Mode    string                `yaml:"mode"` // native | casdoor | hybrid
	Native  NativeApprovalConfig  `yaml:"native"`
	Casdoor CasdoorApprovalConfig `yaml:"casdoor"`
	Router  RouterConfig          `yaml:"router"`
}

// NativeApprovalConfig defines native (L1) approval configuration.
type NativeApprovalConfig struct {
	Enabled        bool `yaml:"enabled"`
	TimeoutMinutes int  `yaml:"timeout_minutes"`
}

// CasdoorApprovalConfig defines Casdoor (L2) approval configuration.
type CasdoorApprovalConfig struct {
	Enabled        bool   `yaml:"enabled"`
	Endpoint       string `yaml:"endpoint"`
	ClientID       string `yaml:"client_id"`
	ClientSecret   string `yaml:"client_secret"`
	CallbackSecret string `yaml:"callback_secret"`
	CallbackKid    string `yaml:"callback_kid"`
	CallbackURL    string `yaml:"callback_url"`
	TaskOwner      string `yaml:"task_owner"`
	DefaultFlow    string `yaml:"default_flow"`
	TTLMinutes     int    `yaml:"ttl_minutes"`
}

// RouterConfig defines approval routing configuration.
type RouterConfig struct {
	HighSensitivityThreshold       float64  `yaml:"high_sensitivity_threshold"`
	RequireCasdoorKeywords         []string `yaml:"require_casdoor_keywords"`
	CrossDepartmentRequiresCasdoor bool     `yaml:"cross_department_requires_casdoor"`
}

// DefaultApprovalConfig returns sensible defaults.
func DefaultApprovalConfig() ApprovalConfig {
	return ApprovalConfig{
		Mode: "hybrid",
		Native: NativeApprovalConfig{
			Enabled:        true,
			TimeoutMinutes: 30,
		},
		Casdoor: CasdoorApprovalConfig{
			Enabled:     false,
			TTLMinutes:  30,
			TaskOwner:   "admin",
			DefaultFlow: "@admin/sensitive-content-approval",
		},
		Router: RouterConfig{
			HighSensitivityThreshold: 0.8,
			RequireCasdoorKeywords: []string{
				"财务数据", "薪资信息", "商业机密", "客户隐私",
				"financial_data", "salary", "trade_secret", "customer_privacy",
			},
			CrossDepartmentRequiresCasdoor: true,
		},
	}
}
