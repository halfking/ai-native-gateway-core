package bg

import "context"

// AttachmentStorageService 定义附件存储服务接口,避免 bg 包直接依赖 domains/attachments
type AttachmentStorageService interface {
	// BaseDir 返回当前存储根目录
	BaseDir() string
}

// AttachmentRef 是一个待清理附件候选的引用身份。RelPath 为存储相对路径
// （与 request_attachments.storage_path 同形，如 2026/07/a1/b2/<sha256>.png，
// 历史 req_<requestID>/ 布局也原样）；Hash 为内容 sha256（legacy 布局解析
// 不出时为空，此时仅按路径反查）。
type AttachmentRef struct {
	RelPath string
	Hash    string
}

// AttachmentReferenceChecker 在删除文件前反查 request_attachments 的存活
// 引用（§三#2 悬挂删除守卫）。返回候选中仍被引用的 RelPath 集合——路径
// 等值或哈希等值任一命中即算存活（哈希命中是对跨月去重复本与 legacy 行
// 的保守过保护，宁可少删不可误删）。
type AttachmentReferenceChecker interface {
	ReferencedPaths(ctx context.Context, candidates []AttachmentRef) (map[string]bool, error)
}
