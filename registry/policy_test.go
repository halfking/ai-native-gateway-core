package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestIsDeprecated 测试废弃检查
func TestIsDeprecated(t *testing.T) {
	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	tomorrow := now.Add(24 * time.Hour)

	tests := []struct {
		name string
		tool *Tool
		want bool
	}{
		{"not deprecated (nil date)", &Tool{ToolID: "test.tool"}, false},
		{"deprecated (date passed)", &Tool{ToolID: "test.tool", DeprecationDate: &yesterday}, true},
		{"not yet deprecated", &Tool{ToolID: "test.tool", DeprecationDate: &tomorrow}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.tool.IsDeprecated())
		})
	}
}

// TestIsSuperseded 测试被替代检查
func TestIsSuperseded(t *testing.T) {
	tests := []struct {
		name string
		tool *Tool
		want bool
	}{
		{"not superseded", &Tool{ToolID: "test.tool"}, false},
		{"superseded", &Tool{ToolID: "test.tool", SupersededBy: "test.tool.v2"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.tool.IsSuperseded())
		})
	}
}

// TestIsAllowed_NoPolicies 测试无策略情况
func TestIsAllowed_NoPolicies(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools:    make(map[string]*Tool),
			policies: make(map[string][]*TenantPolicy),
		},
	}

	allowed, reason := tr.IsAllowed("tenant1", "filesystem.read_file")
	assert.True(t, allowed)
	assert.Empty(t, reason)
}

// TestIsAllowed_DenyPolicy 测试 deny 策略
func TestIsAllowed_DenyPolicy(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: make(map[string]*Tool),
			policies: map[string][]*TenantPolicy{
				"tenant1": {
					{TenantID: "tenant1", ToolPattern: "filesystem.*", PolicyType: "deny", Enabled: true, Reason: "security restriction"},
				},
			},
		},
	}

	allowed, reason := tr.IsAllowed("tenant1", "filesystem.read_file")
	assert.False(t, allowed)
	assert.Equal(t, "security restriction", reason)
}

// TestIsAllowed_AllowPolicy 测试 allow 策略
func TestIsAllowed_AllowPolicy(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: make(map[string]*Tool),
			policies: map[string][]*TenantPolicy{
				"tenant1": {
					{TenantID: "tenant1", ToolPattern: "filesystem.*", PolicyType: "allow", Enabled: true},
				},
			},
		},
	}

	allowed, _ := tr.IsAllowed("tenant1", "filesystem.read_file")
	assert.True(t, allowed)
}

// TestIsAllowed_AllowWithNonMatching 测试 allow 策略不匹配
func TestIsAllowed_AllowWithNonMatching(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: make(map[string]*Tool),
			policies: map[string][]*TenantPolicy{
				"tenant1": {
					{TenantID: "tenant1", ToolPattern: "filesystem.*", PolicyType: "allow", Enabled: true},
				},
			},
		},
	}

	// network.* 不在白名单中
	allowed, _ := tr.IsAllowed("tenant1", "network.http_get")
	assert.False(t, allowed)
}

// TestIsAllowed_DisabledPolicy 测试禁用的策略
func TestIsAllowed_DisabledPolicy(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: make(map[string]*Tool),
			policies: map[string][]*TenantPolicy{
				"tenant1": {
					{TenantID: "tenant1", ToolPattern: "filesystem.*", PolicyType: "deny", Enabled: false},
				},
			},
		},
	}

	// 禁用的 deny 策略不应生效
	allowed, _ := tr.IsAllowed("tenant1", "filesystem.read_file")
	assert.True(t, allowed)
}

// TestMatchPattern 测试通配符匹配
func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		toolID  string
		want    bool
	}{
		{"filesystem.read_file", "filesystem.read_file", true},
		{"filesystem.read_file", "filesystem.write_file", false},
		{"filesystem.*", "filesystem.read_file", true},
		{"filesystem.*", "filesystem.write_file", true},
		{"filesystem.*", "network.http_get", false},
		{"*.read_file", "filesystem.read_file", true},
		{"*.read_file", "database.read_file", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_vs_"+tt.toolID, func(t *testing.T) {
			assert.Equal(t, tt.want, matchPattern(tt.pattern, tt.toolID))
		})
	}
}

// TestGetPolicies 测试获取策略列表
func TestGetPolicies(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: make(map[string]*Tool),
			policies: map[string][]*TenantPolicy{
				"tenant1": {
					{TenantID: "tenant1", ToolPattern: "filesystem.*", PolicyType: "deny"},
					{TenantID: "tenant1", ToolPattern: "network.*", PolicyType: "allow"},
				},
			},
		},
	}

	policies := tr.GetPolicies("tenant1")
	assert.Len(t, policies, 2)
}

func TestGetPolicies_EmptyTenant(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools:    make(map[string]*Tool),
			policies: make(map[string][]*TenantPolicy),
		},
	}

	policies := tr.GetPolicies("nonexistent")
	assert.Empty(t, policies)
}