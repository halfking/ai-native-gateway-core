package registry

import (
	"context"
)

// ToolRegistryAdapter 适配器，将 registry.ToolRegistry 适配为 relay 层可用的接口
type ToolRegistryAdapter struct {
	tr *ToolRegistry
}

// NewAdapter 创建适配器
func NewAdapter(tr *ToolRegistry) *ToolRegistryAdapter {
	return &ToolRegistryAdapter{tr: tr}
}

// ToolDef 用于 relay 层的工具定义
type ToolDef struct {
	ToolID     string
	Definition []byte
}

// Get 获取单个工具定义
func (a *ToolRegistryAdapter) Get(ctx context.Context, tenantID, toolID string) (*ToolDef, error) {
	tool, err := a.tr.Get(ctx, tenantID, toolID)
	if err != nil {
		return nil, err
	}
	if tool == nil {
		return nil, nil
	}

	return &ToolDef{
		ToolID:     tool.ToolID,
		Definition: []byte(tool.Definition),
	}, nil
}

// GetCategory 获取分类下所有工具定义
func (a *ToolRegistryAdapter) GetCategory(ctx context.Context, tenantID, category string) ([]*ToolDef, error) {
	tools, err := a.tr.GetCategory(ctx, tenantID, category)
	if err != nil {
		return nil, err
	}

	result := make([]*ToolDef, 0, len(tools))
	for _, tool := range tools {
		result = append(result, &ToolDef{
			ToolID:     tool.ToolID,
			Definition: []byte(tool.Definition),
		})
	}

	return result, nil
}

// ExpandToolIDs 展开 tool_ids 通配符
func (a *ToolRegistryAdapter) ExpandToolIDs(ctx context.Context, tenantID string, toolIDs []string) []string {
	return a.tr.ExpandToolIDs(ctx, tenantID, toolIDs)
}

// IsAllowed 检查租户是否有权使用工具
func (a *ToolRegistryAdapter) IsAllowed(tenantID, toolID string) (bool, string) {
	return a.tr.IsAllowed(tenantID, toolID)
}

// IsDeprecated 检查工具是否已废弃
func (a *ToolRegistryAdapter) IsDeprecated(toolID string) bool {
	return a.tr.IsDeprecated(toolID)
}

// GetSupersededBy 获取替代工具ID
func (a *ToolRegistryAdapter) GetSupersededBy(toolID string) string {
	return a.tr.GetSupersededBy(toolID)
}
