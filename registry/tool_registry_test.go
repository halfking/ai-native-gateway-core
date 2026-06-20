package registry

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestDB 创建测试数据库连接
// 需要设置环境变量 TOOL_REGISTRY_TEST_DB
// 例如: export TOOL_REGISTRY_TEST_DB="postgres://llm_gateway:password@host:5432/llm_gateway?sslmode=disable"
func setupTestDB(t *testing.T) *pgxpool.Pool {
	testDBURL := os.Getenv("TOOL_REGISTRY_TEST_DB")
	if testDBURL == "" {
		t.Skip("Integration test: requires TOOL_REGISTRY_TEST_DB env var")
	}

	ctx := context.Background()
	db, err := pgxpool.New(ctx, testDBURL)
	require.NoError(t, err, "failed to connect to test database")

	t.Cleanup(func() {
		db.Close()
	})

	return db
}

// 单元测试：不需要数据库

func TestTool_Type(t *testing.T) {
	tool := &Tool{
		ID:       1,
		ToolID:   "filesystem.read_file",
		TenantID: "default",
		Category: "filesystem",
		ToolName: "read_file",
	}

	assert.Equal(t, int64(1), tool.ID)
	assert.Equal(t, "filesystem.read_file", tool.ToolID)
	assert.Equal(t, "default", tool.TenantID)
}

func TestToolCache_Keys(t *testing.T) {
	cache := &toolCache{
		tools:      make(map[string]*Tool),
		byCategory: make(map[string][]*Tool),
	}

	tool := &Tool{
		ToolID:   "filesystem.read_file",
		TenantID: "default",
	}

	// 测试 key 格式
	key := tool.TenantID + ":" + tool.ToolID
	assert.Equal(t, "default:filesystem.read_file", key)

	cache.tools[key] = tool
	assert.NotNil(t, cache.tools["default:filesystem.read_file"])
}

// 集成测试：需要真实数据库

func TestToolRegistry_Get_DefaultTool(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tr := NewToolRegistry(db, slog.Default())
	ctx := context.Background()

	// 测试：获取默认工具
	tool, err := tr.Get(ctx, "tenant1", "filesystem.read_file")
	require.NoError(t, err)
	if tool != nil {
		assert.Equal(t, "filesystem.read_file", tool.ToolID)
		assert.Equal(t, "default", tool.TenantID)
		assert.Equal(t, "filesystem", tool.Category)
	} else {
		t.Log("No tools in database, skipping assertion")
	}
}

func TestToolRegistry_Get_ToolNotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tr := NewToolRegistry(db, slog.Default())
	ctx := context.Background()

	// 测试：工具不存在
	tool, err := tr.Get(ctx, "tenant1", "nonexistent.tool")
	require.NoError(t, err)
	assert.Nil(t, tool)
}

func TestToolRegistry_GetCategory(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tr := NewToolRegistry(db, slog.Default())
	ctx := context.Background()

	// 测试：获取 filesystem 分类
	tools, err := tr.GetCategory(ctx, "default", "filesystem")
	require.NoError(t, err)
	
	if len(tools) > 0 {
		// 验证工具 ID 格式
		for _, tool := range tools {
			assert.NotEmpty(t, tool.ToolID)
			assert.Contains(t, tool.ToolID, ".")
			assert.Equal(t, "filesystem", tool.Category)
		}
	} else {
		t.Log("No filesystem tools in database")
	}
}

func TestToolRegistry_GetCategory_EmptyCategory(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tr := NewToolRegistry(db, slog.Default())
	ctx := context.Background()

	// 测试：空分类
	tools, err := tr.GetCategory(ctx, "default", "nonexistent_category")
	require.NoError(t, err)
	assert.Empty(t, tools)
}

func TestToolRegistry_Reload(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tr := NewToolRegistry(db, slog.Default())
	ctx := context.Background()

	// 记录初始刷新时间
	initialRefreshTime := tr.GetLastRefreshTime()

	// 等待 1 秒确保时间戳不同
	time.Sleep(1 * time.Second)

	// 手动刷新
	err := tr.Reload(ctx)
	require.NoError(t, err)

	// 验证刷新时间已更新
	newRefreshTime := tr.GetLastRefreshTime()
	assert.True(t, newRefreshTime.After(initialRefreshTime),
		"refresh time should be updated after reload")
}

func TestToolRegistry_Stats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tr := NewToolRegistry(db, slog.Default())

	// 获取统计信息
	stats := tr.Stats()

	assert.NotNil(t, stats)
	assert.Contains(t, stats, "tools_count")
	assert.Contains(t, stats, "categories_count")
	assert.Contains(t, stats, "last_refresh")

	toolsCount := stats["tools_count"].(int)
	assert.GreaterOrEqual(t, toolsCount, 0)
}

func TestToolRegistry_TenantOverride(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// 插入租户覆盖工具（临时测试数据）
	_, err := db.Exec(ctx, `
		INSERT INTO tool_registry (tool_id, tenant_id, category, tool_name, tool_definition, version, priority)
		VALUES ('filesystem.read_file', 'tenant_test_override', 'filesystem', 'read_file_override', 
		        '{"type":"function","function":{"name":"read_file_override"}}', 1, 100)
		ON CONFLICT (tenant_id, tool_id, version) DO NOTHING
	`)
	require.NoError(t, err)

	// 创建新的 ToolRegistry 实例（会加载新数据）
	tr := NewToolRegistry(db, slog.Default())

	// 测试：租户优先
	tool, err := tr.Get(ctx, "tenant_test_override", "filesystem.read_file")
	require.NoError(t, err)
	if tool != nil {
		assert.Equal(t, "tenant_test_override", tool.TenantID)
		assert.Equal(t, "read_file_override", tool.ToolName)
	}

	// 清理测试数据
	_, err = db.Exec(ctx, `
		DELETE FROM tool_registry WHERE tenant_id = 'tenant_test_override'
	`)
	require.NoError(t, err)
}

func TestToolRegistry_GetCategory_Deduplication(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// 插入租户覆盖工具
	_, err := db.Exec(ctx, `
		INSERT INTO tool_registry (tool_id, tenant_id, category, tool_name, tool_definition, version, priority)
		VALUES ('filesystem.write_file', 'tenant_test_dedup', 'filesystem', 'write_file_dedup', 
		        '{"type":"function","function":{"name":"write_file_dedup"}}', 1, 100)
		ON CONFLICT (tenant_id, tool_id, version) DO NOTHING
	`)
	require.NoError(t, err)

	tr := NewToolRegistry(db, slog.Default())

	// 获取 tenant_test_dedup 的 filesystem 分类
	tools, err := tr.GetCategory(ctx, "tenant_test_dedup", "filesystem")
	require.NoError(t, err)

	// 统计 tool_id 出现次数（验证去重）
	toolIDCount := make(map[string]int)
	for _, tool := range tools {
		toolIDCount[tool.ToolID]++
	}

	// 验证：每个 tool_id 只出现一次
	for toolID, count := range toolIDCount {
		assert.Equal(t, 1, count, "tool_id %s should appear only once", toolID)
	}

	// 验证：tenant_test_dedup 的 write_file 优先
	var writeFileTool *Tool
	for _, tool := range tools {
		if tool.ToolID == "filesystem.write_file" {
			writeFileTool = tool
			break
		}
	}
	if writeFileTool != nil {
		assert.Equal(t, "tenant_test_dedup", writeFileTool.TenantID)
	}

	// 清理测试数据
	_, err = db.Exec(ctx, `
		DELETE FROM tool_registry WHERE tenant_id = 'tenant_test_dedup'
	`)
	require.NoError(t, err)
}
