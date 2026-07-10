package vibecoding

import (
	"context"
	"log/slog"
)

// ProjectManager 项目管理器
type ProjectManager struct {
	store Store
}

// NewProjectManager 创建项目管理器
func NewProjectManager(store Store) *ProjectManager {
	return &ProjectManager{
		store: store,
	}
}

// CreateProject 创建项目
func (m *ProjectManager) CreateProject(ctx context.Context, tenantID, name, description, language, framework, createdBy string) (*Project, error) {
	project := &Project{
		TenantID:    tenantID,
		Name:        name,
		Description: description,
		Language:    language,
		Framework:   framework,
		Status:      ProjectStatusActive,
		Settings:    make(map[string]interface{}),
		CreatedBy:   createdBy,
	}

	if err := m.store.CreateProject(ctx, project); err != nil {
		slog.Error("create project failed", "error", err)
		return nil, err
	}

	slog.Info("project created", "project_id", project.ID, "name", name)
	return project, nil
}

// GetProject 获取项目
func (m *ProjectManager) GetProject(ctx context.Context, id int64) (*Project, error) {
	return m.store.GetProject(ctx, id)
}

// ListProjects 列出项目
func (m *ProjectManager) ListProjects(ctx context.Context, tenantID string, status ProjectStatus, offset, limit int) ([]Project, int, error) {
	return m.store.ListProjects(ctx, tenantID, status, offset, limit)
}

// UpdateProject 更新项目
func (m *ProjectManager) UpdateProject(ctx context.Context, project *Project) error {
	if err := m.store.UpdateProject(ctx, project); err != nil {
		slog.Error("update project failed", "error", err)
		return err
	}

	slog.Info("project updated", "project_id", project.ID)
	return nil
}

// ArchiveProject 归档项目
func (m *ProjectManager) ArchiveProject(ctx context.Context, id int64) error {
	project, err := m.store.GetProject(ctx, id)
	if err != nil {
		return err
	}

	project.Status = ProjectStatusArchived
	if err := m.store.UpdateProject(ctx, project); err != nil {
		slog.Error("archive project failed", "error", err)
		return err
	}

	slog.Info("project archived", "project_id", id)
	return nil
}

// DeleteProject 删除项目
func (m *ProjectManager) DeleteProject(ctx context.Context, id int64) error {
	if err := m.store.DeleteProject(ctx, id); err != nil {
		slog.Error("delete project failed", "error", err)
		return err
	}

	slog.Info("project deleted", "project_id", id)
	return nil
}

// GetProjectStats 获取项目统计
func (m *ProjectManager) GetProjectStats(ctx context.Context, tenantID string) (*ProjectStats, error) {
	// 获取所有状态的项目数量
	active, _, _ := m.store.ListProjects(ctx, tenantID, ProjectStatusActive, 0, 1)
	archived, _, _ := m.store.ListProjects(ctx, tenantID, ProjectStatusArchived, 0, 1)

	stats := &ProjectStats{
		TotalProjects:    len(active) + len(archived),
		ActiveProjects:   len(active),
		ArchivedProjects: len(archived),
	}

	return stats, nil
}

// ProjectStats 项目统计
type ProjectStats struct {
	TotalProjects    int `json:"total_projects"`
	ActiveProjects   int `json:"active_projects"`
	ArchivedProjects int `json:"archived_projects"`
}
