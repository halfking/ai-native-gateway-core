package autoupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Rollback 回滚器（恢复备份 + 清理）
type Rollback struct {
	binPath   string
	backupDir string
	dataDir   string
}

// NewRollback 创建回滚器
func NewRollback(binPath, backupDir, dataDir string) *Rollback {
	return &Rollback{
		binPath:   binPath,
		backupDir: backupDir,
		dataDir:   dataDir,
	}
}

// RollbackResult 回滚结果
type RollbackResult struct {
	Success      bool
	RestoredFrom string
	Version      string
	Error        string
	DurationMs   int64
}

// Rollback 回滚到上一个版本
func (r *Rollback) Rollback(ctx context.Context, targetVersion string) (*RollbackResult, error) {
	start := time.Now()
	result := &RollbackResult{
		Version: targetVersion,
	}

	// 1. 查找备份文件
	backupPath, err := r.findBackup(targetVersion)
	if err != nil {
		result.Error = fmt.Sprintf("find backup: %v", err)
		return result, err
	}
	result.RestoredFrom = backupPath

	// 2. 验证备份文件
	if err := r.verifyBackup(backupPath); err != nil {
		result.Error = fmt.Sprintf("verify backup: %v", err)
		return result, err
	}

	// 3. 恢复二进制文件
	if err := r.restoreBinary(backupPath); err != nil {
		result.Error = fmt.Sprintf("restore binary: %v", err)
		return result, err
	}

	// 4. 更新版本文件
	versionFile := filepath.Join(r.dataDir, "VERSION")
	if err := os.WriteFile(versionFile, []byte(targetVersion+"\n"), 0644); err != nil {
		result.Error = fmt.Sprintf("write version file: %v", err)
	}

	result.Success = true
	result.DurationMs = time.Since(start).Milliseconds()
	return result, nil
}

// findBackup 查找备份文件（最新的匹配版本）
func (r *Rollback) findBackup(version string) (string, error) {
	entries, err := os.ReadDir(r.backupDir)
	if err != nil {
		return "", fmt.Errorf("read backup dir: %w", err)
	}

	// 查找匹配版本的最新备份
	var latestBackup string
	var latestTime time.Time

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// 检查文件名是否包含目标版本
		name := entry.Name()
		if !strings.Contains(name, version) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latestBackup = filepath.Join(r.backupDir, name)
		}
	}

	if latestBackup == "" {
		return "", fmt.Errorf("no backup found for version %s", version)
	}

	return latestBackup, nil
}

// verifyBackup 验证备份文件
func (r *Rollback) verifyBackup(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	if info.Size() < 1024 {
		return fmt.Errorf("backup file too small: %d bytes", info.Size())
	}

	return nil
}

// restoreBinary 恢复二进制文件
func (r *Rollback) restoreBinary(backupPath string) error {
	// 复制备份到临时文件
	tmpPath := r.binPath + ".rollback"
	if err := r.copyFile(backupPath, tmpPath); err != nil {
		return fmt.Errorf("copy backup: %w", err)
	}

	// 设置可执行权限
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// 原子替换
	if err := os.Rename(tmpPath, r.binPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// copyFile 复制文件
func (r *Rollback) copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcFile.Close() }()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = dstFile.Close() }()

	if _, err := srcFile.WriteTo(dstFile); err != nil {
		return err
	}

	// 复制权限
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, srcInfo.Mode())
}

// ListBackups 列出所有备份
func (r *Rollback) ListBackups() ([]BackupInfo, error) {
	entries, err := os.ReadDir(r.backupDir)
	if err != nil {
		return nil, err
	}

	var backups []BackupInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		backups = append(backups, BackupInfo{
			Path:    filepath.Join(r.backupDir, entry.Name()),
			Name:    entry.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	return backups, nil
}

// BackupInfo 备份信息
type BackupInfo struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
}
