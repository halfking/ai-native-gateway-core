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
