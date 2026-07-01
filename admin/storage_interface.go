package admin

// AttachmentStorageService 定义附件存储服务接口,避免 admin 包直接依赖 domains/attachments
type AttachmentStorageService interface {
	// BaseDir 返回当前存储根目录
	BaseDir() string
	// SetBaseDir 原子切换存储根目录
	SetBaseDir(dir string) error
}
