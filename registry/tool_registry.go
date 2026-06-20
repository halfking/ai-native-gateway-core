package registry

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// toolCache 进程内缓存
type toolCache struct {
	tools       map[string]*Tool         // key: "tenant_id:tool_id"
	byCategory  map[string][]*Tool       // key: "tenant_id:category"
	policies    map[string][]*TenantPolicy // key: tenant_id
	lastRefresh time.Time
}

// ToolRegistry 工具注册表服务
type ToolRegistry struct {
	db     *pgxpool.Pool
	cache  *toolCache
	mu     sync.RWMutex
	logger *slog.Logger
}

// NewToolRegistry 创建新的工具注册表实例
func NewToolRegistry(db *pgxpool.Pool, logger *slog.Logger) *ToolRegistry {
	if logger == nil {
		logger = slog.Default()
	}

	tr := &ToolRegistry{
		db: db,
		cache: &toolCache{
			tools:      make(map[string]*Tool),
			byCategory: make(map[string][]*Tool),
			policies:   make(map[string][]*TenantPolicy),
		},
		logger: logger,
	}

	// 启动时加载
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := tr.refresh(ctx); err != nil {
		logger.Warn("initial tool registry load failed", "error", err)
	} else {
		logger.Info("tool registry loaded",
			"tools_count", len(tr.cache.tools),
			"categories_count", len(tr.cache.byCategory))
	}

	// 后台刷新 goroutine（60 秒）
	go tr.backgroundRefresh()

	return tr
}

// Get 获取单个工具（支持租户覆盖）
// 查询优先级：tenant_id -> default
func (tr *ToolRegistry) Get(ctx context.Context, tenantID, toolID string) (*Tool, error) {
	tr.mu.RLock()
	defer tr.mu.RUnlock()

	// 优先查租户级别
	key := tenantID + ":" + toolID
	if tool, ok := tr.cache.tools[key]; ok {
		return tool, nil
	}

	// 回退到 default
	defaultKey := "default:" + toolID
	if tool, ok := tr.cache.tools[defaultKey]; ok {
		return tool, nil
	}

	return nil, nil // 未找到
}

// GetCategory 获取分类下所有工具（支持租户覆盖 + 去重）
func (tr *ToolRegistry) GetCategory(ctx context.Context, tenantID, category string) ([]*Tool, error) {
	tr.mu.RLock()
	defer tr.mu.RUnlock()

	seen := make(map[string]bool)
	var result []*Tool

	// 优先租户工具
	tenantKey := tenantID + ":" + category
	for _, tool := range tr.cache.byCategory[tenantKey] {
		if tool.Enabled {
			result = append(result, tool)
			seen[tool.ToolID] = true
		}
	}

	// 补充默认工具（去重）
	defaultKey := "default:" + category
	for _, tool := range tr.cache.byCategory[defaultKey] {
		if tool.Enabled && !seen[tool.ToolID] {
			result = append(result, tool)
		}
	}

	return result, nil
}

// refresh 从数据库重新加载缓存
func (tr *ToolRegistry) refresh(ctx context.Context) error {
	rows, err := tr.db.Query(ctx, `
		SELECT id, tool_id, tenant_id, category, tool_name, tool_definition, 
		       enabled, priority, version, created_at, updated_at
		FROM tool_registry
		ORDER BY tenant_id, category, priority DESC, tool_id
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	newCache := &toolCache{
		tools:       make(map[string]*Tool),
		byCategory:  make(map[string][]*Tool),
		lastRefresh: time.Now(),
	}

	for rows.Next() {
		var tool Tool
		if err := rows.Scan(
			&tool.ID, &tool.ToolID, &tool.TenantID, &tool.Category,
			&tool.ToolName, &tool.Definition, &tool.Enabled, &tool.Priority,
			&tool.Version, &tool.CreatedAt, &tool.UpdatedAt,
		); err != nil {
			tr.logger.Warn("failed to scan tool row", "error", err)
			continue
		}

		// 填充 tools map
		key := tool.TenantID + ":" + tool.ToolID
		newCache.tools[key] = &tool

		// 填充 byCategory map
		catKey := tool.TenantID + ":" + tool.Category
		newCache.byCategory[catKey] = append(newCache.byCategory[catKey], &tool)
	}

	if err := rows.Err(); err != nil {
		return err
	}

	tr.mu.Lock()
	tr.cache = newCache
	tr.mu.Unlock()

	tr.logger.Debug("tool registry refreshed",
		"tools_count", len(newCache.tools),
		"refresh_time", newCache.lastRefresh)

	return nil
}

// backgroundRefresh 后台定时刷新（60 秒）
func (tr *ToolRegistry) backgroundRefresh() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := tr.refresh(ctx); err != nil {
			tr.logger.Error("background tool registry refresh failed", "error", err)
		}
		cancel()
	}
}

// Reload 手动触发刷新（Admin API 用）
func (tr *ToolRegistry) Reload(ctx context.Context) error {
	return tr.refresh(ctx)
}

// GetLastRefreshTime 获取最后刷新时间（用于健康检查）
func (tr *ToolRegistry) GetLastRefreshTime() time.Time {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	return tr.cache.lastRefresh
}

// Stats 获取统计信息（用于监控）
func (tr *ToolRegistry) Stats() map[string]interface{} {
	tr.mu.RLock()
	defer tr.mu.RUnlock()

	return map[string]interface{}{
		"tools_count":      len(tr.cache.tools),
		"categories_count": len(tr.cache.byCategory),
		"last_refresh":     tr.cache.lastRefresh,
	}
}

// ExpandToolIDs 展开 tool_ids 通配符
// 支持三种模式：
// 1. 精确匹配: "filesystem.read_file" -> ["filesystem.read_file"]
// 2. 分类通配: "filesystem.*" -> ["filesystem.read_file", "filesystem.write_file", ...]
// 3. 全局通配: "*" -> 所有工具
//
// 返回: 展开后的 tool_id 列表（去重）
func (tr *ToolRegistry) ExpandToolIDs(ctx context.Context, tenantID string, toolIDs []string) []string {
	if len(toolIDs) == 0 {
		return nil
	}

	tr.mu.RLock()
	defer tr.mu.RUnlock()

	seen := make(map[string]bool)
	var result []string

	for _, pattern := range toolIDs {
		if pattern == "" {
			continue
		}

		// Case 1: 全局通配 "*"
		if pattern == "*" {
			// 先尝试租户工具
			for _, tool := range tr.cache.tools {
				if tool.TenantID == tenantID && !seen[tool.ToolID] {
					seen[tool.ToolID] = true
					result = append(result, tool.ToolID)
				}
			}
			// 回退到 default
			for _, tool := range tr.cache.tools {
				if tool.TenantID == "default" && !seen[tool.ToolID] {
					seen[tool.ToolID] = true
					result = append(result, tool.ToolID)
				}
			}
			continue
		}

		// Case 2: 分类通配 "category.*"
		if len(pattern) > 2 && pattern[len(pattern)-2:] == ".*" {
			category := pattern[:len(pattern)-2]
			
			// 先尝试租户工具
			catKey := tenantID + ":" + category
			if tools, ok := tr.cache.byCategory[catKey]; ok {
				for _, tool := range tools {
					if !seen[tool.ToolID] {
						seen[tool.ToolID] = true
						result = append(result, tool.ToolID)
					}
				}
			}
			
			// 回退到 default
			defaultCatKey := "default:" + category
			if tools, ok := tr.cache.byCategory[defaultCatKey]; ok {
				for _, tool := range tools {
					if !seen[tool.ToolID] {
						seen[tool.ToolID] = true
						result = append(result, tool.ToolID)
					}
				}
			}
			continue
		}

		// Case 3: 精确匹配 "category.tool_name"
		if !seen[pattern] {
			// 验证工具是否存在（使用 Get 逻辑）
			key := tenantID + ":" + pattern
			if _, ok := tr.cache.tools[key]; ok {
				seen[pattern] = true
				result = append(result, pattern)
				continue
			}
			
			// 回退到 default
			defaultKey := "default:" + pattern
			if _, ok := tr.cache.tools[defaultKey]; ok {
				seen[pattern] = true
				result = append(result, pattern)
			}
		}
	}

	return result
}
