package registry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExpandToolIDs_ExactMatch 测试精确匹配
func TestExpandToolIDs_ExactMatch(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: map[string]*Tool{
				"default:filesystem.read_file":  {ToolID: "filesystem.read_file", TenantID: "default"},
				"default:filesystem.write_file": {ToolID: "filesystem.write_file", TenantID: "default"},
			},
			byCategory: make(map[string][]*Tool),
		},
	}

	ctx := context.Background()
	result := tr.ExpandToolIDs(ctx, "default", []string{"filesystem.read_file"})

	assert.Equal(t, []string{"filesystem.read_file"}, result)
}

// TestExpandToolIDs_CategoryWildcard 测试分类通配符
func TestExpandToolIDs_CategoryWildcard(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: map[string]*Tool{
				"default:filesystem.read_file":       {ToolID: "filesystem.read_file", TenantID: "default"},
				"default:filesystem.write_file":      {ToolID: "filesystem.write_file", TenantID: "default"},
				"default:filesystem.list_directory":  {ToolID: "filesystem.list_directory", TenantID: "default"},
				"default:network.http_get":           {ToolID: "network.http_get", TenantID: "default"},
			},
			byCategory: map[string][]*Tool{
				"default:filesystem": {
					{ToolID: "filesystem.read_file", TenantID: "default"},
					{ToolID: "filesystem.write_file", TenantID: "default"},
					{ToolID: "filesystem.list_directory", TenantID: "default"},
				},
			},
		},
	}

	ctx := context.Background()
	result := tr.ExpandToolIDs(ctx, "default", []string{"filesystem.*"})

	assert.ElementsMatch(t, []string{
		"filesystem.read_file",
		"filesystem.write_file",
		"filesystem.list_directory",
	}, result)
}

// TestExpandToolIDs_GlobalWildcard 测试全局通配符
func TestExpandToolIDs_GlobalWildcard(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: map[string]*Tool{
				"default:filesystem.read_file":  {ToolID: "filesystem.read_file", TenantID: "default"},
				"default:filesystem.write_file": {ToolID: "filesystem.write_file", TenantID: "default"},
				"default:network.http_get":      {ToolID: "network.http_get", TenantID: "default"},
			},
			byCategory: make(map[string][]*Tool),
		},
	}

	ctx := context.Background()
	result := tr.ExpandToolIDs(ctx, "default", []string{"*"})

	assert.ElementsMatch(t, []string{
		"filesystem.read_file",
		"filesystem.write_file",
		"network.http_get",
	}, result)
}

// TestExpandToolIDs_Mixed 测试混合模式
func TestExpandToolIDs_Mixed(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: map[string]*Tool{
				"default:filesystem.read_file":  {ToolID: "filesystem.read_file", TenantID: "default"},
				"default:filesystem.write_file": {ToolID: "filesystem.write_file", TenantID: "default"},
				"default:network.http_get":      {ToolID: "network.http_get", TenantID: "default"},
				"default:network.http_post":     {ToolID: "network.http_post", TenantID: "default"},
			},
			byCategory: map[string][]*Tool{
				"default:filesystem": {
					{ToolID: "filesystem.read_file", TenantID: "default"},
					{ToolID: "filesystem.write_file", TenantID: "default"},
				},
			},
		},
	}

	ctx := context.Background()
	result := tr.ExpandToolIDs(ctx, "default", []string{"filesystem.*", "network.http_get"})

	assert.ElementsMatch(t, []string{
		"filesystem.read_file",
		"filesystem.write_file",
		"network.http_get",
	}, result)
}

// TestExpandToolIDs_TenantOverride 测试租户覆盖
func TestExpandToolIDs_TenantOverride(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: map[string]*Tool{
				"default:filesystem.read_file":     {ToolID: "filesystem.read_file", TenantID: "default"},
				"tenant1:filesystem.read_file":     {ToolID: "filesystem.read_file", TenantID: "tenant1"},
				"tenant1:filesystem.custom_tool":   {ToolID: "filesystem.custom_tool", TenantID: "tenant1"},
			},
			byCategory: map[string][]*Tool{
				"tenant1:filesystem": {
					{ToolID: "filesystem.read_file", TenantID: "tenant1"},
					{ToolID: "filesystem.custom_tool", TenantID: "tenant1"},
				},
			},
		},
	}

	ctx := context.Background()
	result := tr.ExpandToolIDs(ctx, "tenant1", []string{"filesystem.*"})

	// tenant1 的工具应该被优先返回
	assert.ElementsMatch(t, []string{
		"filesystem.read_file",
		"filesystem.custom_tool",
	}, result)
}

// TestExpandToolIDs_Deduplication 测试去重
func TestExpandToolIDs_Deduplication(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: map[string]*Tool{
				"default:filesystem.read_file":  {ToolID: "filesystem.read_file", TenantID: "default"},
				"default:filesystem.write_file": {ToolID: "filesystem.write_file", TenantID: "default"},
			},
			byCategory: map[string][]*Tool{
				"default:filesystem": {
					{ToolID: "filesystem.read_file", TenantID: "default"},
					{ToolID: "filesystem.write_file", TenantID: "default"},
				},
			},
		},
	}

	ctx := context.Background()
	// 重复的 tool_id 应该被去重
	result := tr.ExpandToolIDs(ctx, "default", []string{
		"filesystem.*",
		"filesystem.read_file",
		"filesystem.read_file",
	})

	assert.ElementsMatch(t, []string{
		"filesystem.read_file",
		"filesystem.write_file",
	}, result)
}

// TestExpandToolIDs_NonExistent 测试不存在的工具
func TestExpandToolIDs_NonExistent(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: map[string]*Tool{
				"default:filesystem.read_file": {ToolID: "filesystem.read_file", TenantID: "default"},
			},
			byCategory: make(map[string][]*Tool),
		},
	}

	ctx := context.Background()
	result := tr.ExpandToolIDs(ctx, "default", []string{
		"filesystem.read_file",
		"nonexistent.tool",
	})

	// 不存在的工具被忽略
	assert.Equal(t, []string{"filesystem.read_file"}, result)
}

// TestExpandToolIDs_EmptyInput 测试空输入
func TestExpandToolIDs_EmptyInput(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools:      make(map[string]*Tool),
			byCategory: make(map[string][]*Tool),
		},
	}

	ctx := context.Background()
	result := tr.ExpandToolIDs(ctx, "default", []string{})

	assert.Nil(t, result)
}

// TestExpandToolIDs_EmptyStrings 测试空字符串
func TestExpandToolIDs_EmptyStrings(t *testing.T) {
	tr := &ToolRegistry{
		cache: &toolCache{
			tools: map[string]*Tool{
				"default:filesystem.read_file": {ToolID: "filesystem.read_file", TenantID: "default"},
			},
			byCategory: make(map[string][]*Tool),
		},
	}

	ctx := context.Background()
	result := tr.ExpandToolIDs(ctx, "default", []string{"", "filesystem.read_file", ""})

	assert.Equal(t, []string{"filesystem.read_file"}, result)
}
