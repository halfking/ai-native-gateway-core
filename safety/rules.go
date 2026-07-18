package safety

// BuiltinRules 返回内置规则集
func BuiltinRules() []Rule {
	return []Rule{
		// API Key 检测
		{
			ID:          "api_key_openai",
			Name:        "OpenAI API Key",
			Type:        RuleTypeRegex,
			Pattern:     `sk-[a-zA-Z0-9]{20}T3BlbkFJ[a-zA-Z0-9]{20}`,
			Action:      ActionBlock,
			Severity:    SeverityCritical,
			Enabled:     true,
			Description: "OpenAI API Key 泄露",
		},
		{
			ID:          "api_key_anthropic",
			Name:        "Anthropic API Key",
			Type:        RuleTypeRegex,
			Pattern:     `sk-ant-[a-zA-Z0-9\-]{95}`,
			Action:      ActionBlock,
			Severity:    SeverityCritical,
			Enabled:     true,
			Description: "Anthropic API Key 泄露",
		},

		// PII 检测
		{
			ID:          "pii_id_card",
			Name:        "身份证号",
			Type:        RuleTypeRegex,
			Pattern:     `\d{17}[\dXx]`,
			Action:      ActionSanitize,
			Severity:    SeverityHigh,
			Enabled:     true,
			Description: "身份证号检测",
		},
		{
			ID:          "pii_phone",
			Name:        "手机号",
			Type:        RuleTypeRegex,
			Pattern:     `1[3-9]\d{9}`,
			Action:      ActionSanitize,
			Severity:    SeverityMedium,
			Enabled:     true,
			Description: "手机号检测",
		},
		{
			ID:          "pii_email",
			Name:        "邮箱地址",
			Type:        RuleTypeRegex,
			Pattern:     `[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`,
			Action:      ActionWarn,
			Severity:    SeverityLow,
			Enabled:     true,
			Description: "邮箱地址检测",
		},

		// SQL 注入
		{
			ID:          "sql_injection",
			Name:        "SQL 注入",
			Type:        RuleTypeRegex,
			Pattern:     `(?i)(union|select|drop|insert|delete|update|truncate|exec|execute)\s+`,
			Action:      ActionBlock,
			Severity:    SeverityHigh,
			Enabled:     true,
			Description: "SQL 注入检测",
		},

		// 敏感词 (示例)
		{
			ID:          "sensitive_internal",
			Name:        "内部机密",
			Type:        RuleTypeKeyword,
			Pattern:     "内部机密",
			Action:      ActionBlock,
			Severity:    SeverityHigh,
			Enabled:     true,
			Description: "内部机密信息",
		},
		{
			ID:          "sensitive_confidential",
			Name:        "商业秘密",
			Type:        RuleTypeKeyword,
			Pattern:     "商业秘密",
			Action:      ActionBlock,
			Severity:    SeverityHigh,
			Enabled:     true,
			Description: "商业秘密信息",
		},

		// JWT Token
		{
			ID:          "jwt_token",
			Name:        "JWT Token",
			Type:        RuleTypeRegex,
			Pattern:     `eyJ[a-zA-Z0-9_-]+\.eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`,
			Action:      ActionBlock,
			Severity:    SeverityCritical,
			Enabled:     true,
			Description: "JWT Token 泄露",
		},

		// AWS Access Key
		{
			ID:          "aws_access_key",
			Name:        "AWS Access Key",
			Type:        RuleTypeRegex,
			Pattern:     `AKIA[0-9A-Z]{16}`,
			Action:      ActionBlock,
			Severity:    SeverityCritical,
			Enabled:     true,
			Description: "AWS Access Key 泄露",
		},

		// Private Key
		{
			ID:          "private_key",
			Name:        "私钥",
			Type:        RuleTypeRegex,
			Pattern:     `-----BEGIN (RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`,
			Action:      ActionBlock,
			Severity:    SeverityCritical,
			Enabled:     true,
			Description: "私钥泄露",
		},

		// Password Pattern
		{
			ID:          "password_pattern",
			Name:        "密码模式",
			Type:        RuleTypeRegex,
			Pattern:     `(?i)(password|passwd|pwd)\s*[=:]\s*[^\s]+`,
			Action:      ActionWarn,
			Severity:    SeverityMedium,
			Enabled:     true,
			Description: "密码模式检测",
		},
	}
}

// SensitiveWordsRules 返回敏感词规则
func SensitiveWordsRules() []Rule {
	// 这里可以从配置文件或数据库加载
	// 示例只包含几个
	return []Rule{
		{
			ID:          "sensitive_admin",
			Name:        "管理员密码",
			Type:        RuleTypeKeyword,
			Pattern:     "管理员密码",
			Action:      ActionBlock,
			Severity:    SeverityHigh,
			Enabled:     true,
			Description: "管理员密码关键词",
		},
		{
			ID:          "sensitive_root",
			Name:        "root密码",
			Type:        RuleTypeKeyword,
			Pattern:     "root密码",
			Action:      ActionBlock,
			Severity:    SeverityHigh,
			Enabled:     true,
			Description: "root密码关键词",
		},
	}
}
