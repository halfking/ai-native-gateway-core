package admin

import "github.com/kaixuan/llm-gateway-go/domains/attachments"

// AttachmentStorageService 定义附件存储服务接口,避免 admin 包直接依赖 domains/attachments
type AttachmentStorageService interface {
	// BaseDir 返回当前存储根目录
	BaseDir() string
	// SetBaseDir 原子切换存储根目录
	SetBaseDir(dir string) error
	// HealthCheck 执行存储后端健康检查
	HealthCheck() error
	// BackendInfo 返回后端信息（类型、位置等）
	BackendInfo() attachments.BackendInfo
}
