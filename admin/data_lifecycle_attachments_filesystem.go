package admin

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ── Attachment Filesystem Management API ────────────────────────────────────
// 管理附件文件系统：目录大小、磁盘空间、文件数统计、按时间清理文件。
// 与 data_lifecycle_attachments.go 的区别：
//   - data_lifecycle_attachments.go: 管理 DB 中的 request_logs.attachments JSONB 元数据
//   - 本文件: 管理文件系统中的实体文件（按 hash 命名的 .png/.jpg 等）

// AttachmentFilesystemStatsResponse 附件文件系统统计信息
type AttachmentFilesystemStatsResponse struct {
	AttachmentDir    string  `json:"attachment_dir"`     // 附件存储根目录
	TotalFiles       int     `json:"total_files"`        // 文件总数
	TotalSizeBytes   int64   `json:"total_size_bytes"`   // 总大小（字节）
	TotalSizeHuman   string  `json:"total_size_human"`   // 人类可读大小
	OldestFileTime   *string `json:"oldest_file_time"`   // 最早文件时间
	DiskTotalBytes   uint64  `json:"disk_total_bytes"`   // 磁盘总容量
	DiskUsedBytes    uint64  `json:"disk_used_bytes"`    // 磁盘已用
	DiskAvailBytes   uint64  `json:"disk_avail_bytes"`   // 磁盘可用
	DiskUsagePercent float64 `json:"disk_usage_percent"` // 磁盘使用率
	DiskWarningLevel string  `json:"disk_warning_level"` // safe | warning | danger
}

// AttachmentFilesystemCleanupRequest 文件清理请求
type AttachmentFilesystemCleanupRequest struct {
	OlderThanDays int    `json:"older_than_days"` // 清理 N 天前的文件
	DryRun        bool   `json:"dry_run"`         // 预览模式（不实际删除）
	Reason        string `json:"reason"`          // 清理原因（审计用）
}

// AttachmentFilesystemCleanupResponse 文件清理响应
type AttachmentFilesystemCleanupResponse struct {
	DryRun          bool     `json:"dry_run"`
	FilesDeleted    int      `json:"files_deleted"`
	BytesFreed      int64    `json:"bytes_freed"`
	BytesFreedHuman string   `json:"bytes_freed_human"`
	DeletedPaths    []string `json:"deleted_paths,omitempty"` // 预览模式返回
	Error           string   `json:"error,omitempty"`
}

// handleAttachmentFilesystemStats 返回附件文件系统统计信息
// GET /api/admin/attachments/filesystem/stats
func (h *Handler) handleAttachmentFilesystemStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	attachmentDir := os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR")
	if attachmentDir == "" {
		attachmentDir = "./data/attachments"
	}

	// 转为绝对路径
	absDir, err := filepath.Abs(attachmentDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolve attachment dir: "+err.Error())
		return
	}

	// 统计文件数和总大小
	var totalFiles int
	var totalSize int64
	var oldestTime *time.Time

	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 忽略无法访问的目录
		}
		if d.IsDir() {
			return nil
		}
		totalFiles++
		info, err := d.Info()
		if err == nil {
			totalSize += info.Size()
			if oldestTime == nil || info.ModTime().Before(*oldestTime) {
				t := info.ModTime()
				oldestTime = &t
			}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, "walk attachment dir: "+err.Error())
		return
	}

	// 查询磁盘空间 (syscall.Statfs)
	var stat syscall.Statfs_t
	if err := syscall.Statfs(absDir, &stat); err != nil {
		writeError(w, http.StatusInternalServerError, "statfs: "+err.Error())
		return
	}

	diskTotal := stat.Blocks * uint64(stat.Bsize)
	diskAvail := stat.Bavail * uint64(stat.Bsize)
	diskUsed := diskTotal - diskAvail
	diskUsagePercent := float64(diskUsed) / float64(diskTotal) * 100

	warningLevel := "safe"
	if diskUsagePercent >= 90 {
		warningLevel = "danger"
	} else if diskUsagePercent >= 75 {
		warningLevel = "warning"
	}

	var oldestStr *string
	if oldestTime != nil {
		s := oldestTime.Format(time.RFC3339)
		oldestStr = &s
	}

	resp := AttachmentFilesystemStatsResponse{
		AttachmentDir:    absDir,
		TotalFiles:       totalFiles,
		TotalSizeBytes:   totalSize,
		TotalSizeHuman:   humanBytes(totalSize),
		OldestFileTime:   oldestStr,
		DiskTotalBytes:   diskTotal,
		DiskUsedBytes:    diskUsed,
		DiskAvailBytes:   diskAvail,
		DiskUsagePercent: diskUsagePercent,
		DiskWarningLevel: warningLevel,
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleAttachmentFilesystemCleanup 按时间清理附件文件
// POST /api/admin/attachments/filesystem/cleanup
func (h *Handler) handleAttachmentFilesystemCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req AttachmentFilesystemCleanupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.OlderThanDays <= 0 {
		writeError(w, http.StatusBadRequest, "older_than_days must be positive")
		return
	}

	attachmentDir := os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR")
	if attachmentDir == "" {
		attachmentDir = "./data/attachments"
	}

	absDir, err := filepath.Abs(attachmentDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolve attachment dir: "+err.Error())
		return
	}

	cutoffTime := time.Now().AddDate(0, 0, -req.OlderThanDays)
	var filesDeleted int
	var bytesFreed int64
	var deletedPaths []string

	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 忽略错误
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// 按修改时间判断
		if info.ModTime().Before(cutoffTime) {
			if req.DryRun {
				deletedPaths = append(deletedPaths, path)
				bytesFreed += info.Size()
				filesDeleted++
			} else {
				if rmErr := os.Remove(path); rmErr == nil {
					bytesFreed += info.Size()
					filesDeleted++
				}
			}
		}
		return nil
	})

	resp := AttachmentFilesystemCleanupResponse{
		DryRun:          req.DryRun,
		FilesDeleted:    filesDeleted,
		BytesFreed:      bytesFreed,
		BytesFreedHuman: humanBytes(bytesFreed),
	}
	if req.DryRun {
		resp.DeletedPaths = deletedPaths
	}
	if err != nil {
		resp.Error = err.Error()
	}

	writeJSON(w, http.StatusOK, resp)
}
