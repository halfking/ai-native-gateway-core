package domain

// TenantContext 封装租户管理领域的上下文。
type TenantContext struct {
	ID            string
	Name          string
	Tags          []string
	IsInternal    bool
	IsForbidden   bool
	Policy        *TenantPolicy
	AllowedModels []string
}

// TenantPolicy 租户策略。
type TenantPolicy struct {
	MaxConcurrent       int
	MaxTokensPerRequest int
	MaxRequestsPerMin   int
	AllowStreaming      bool
	AllowTools          bool
}
