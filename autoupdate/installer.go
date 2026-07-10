package autoupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Installer 安装器（备份 + 替换 + 验证）
type Installer struct {
	binPath   string
	backupDir string
	dataDir   string
}

// NewInstaller 创建安装器
func NewInstaller(binPath, backupDir, dataDir string) *Installer {
	return &Installer{
		binPath:   binPath,
		backupDir: backupDir,
		dataDir:   dataDir,
	}
}

// InstallResult 安装结果
type InstallResult struct {
	Success    bool
	BackupPath string
	OldVersion string
	NewVersion string
	Error      string
	DurationMs int64
}

// Install 安装新版本（备份 + 替换）
func (i *Installer) Install(ctx context.Context, downloadPath string, release *Release) (*InstallResult, error) {
	start := time.Now()
	result := &InstallResult{
		OldVersion: i.getCurrentVersion(),
		NewVersion: release.Version,
	}

	// 1. 创建备份目录
	if err := os.MkdirAll(i.backupDir, 0755); err != nil {
		result.Error = fmt.Sprintf("create backup dir: %v", err)
		return result, err
	}

	// 2. 备份当前二进制
	timestamp := time.Now().Format("20060102_150405")
	backupPath := filepath.Join(i.backupDir, fmt.Sprintf("llm-gateway-go_%s_%s", result.OldVersion, timestamp))

	if err := i.copyFile(i.binPath, backupPath); err != nil {
		result.Error = fmt.Sprintf("backup failed: %v", err)
		return result, err
	}
	result.BackupPath = backupPath

	// 3. 验证新文件可执行
	if err := i.verifyBinary(downloadPath); err != nil {
		result.Error = fmt.Sprintf("verify new binary: %v", err)
		return result, err
	}

	// 4. 替换二进制文件
	if err := i.replaceBinary(downloadPath); err != nil {
		// 回滚
		i.copyFile(backupPath, i.binPath)
		result.Error = fmt.Sprintf("replace binary: %v", err)
		return result, err
	}

	// 5. 写入版本信息
	versionFile := filepath.Join(i.dataDir, "VERSION")
	if err := os.WriteFile(versionFile, []byte(release.Version+"\n"), 0644); err != nil {
		result.Error = fmt.Sprintf("write version file: %v", err)
	}

	result.Success = true
	result.DurationMs = time.Since(start).Milliseconds()
	return result, nil
}

// verifyBinary 验证二进制文件
func (i *Installer) verifyBinary(path string) error {
	// 检查文件存在
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	// 检查文件大小
	if info.Size() < 1024 {
		return fmt.Errorf("file too small: %d bytes", info.Size())
	}

	// 尝试执行 --version
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("execute --version: %w", err)
	}

	return nil
}

// replaceBinary 替换二进制文件
func (i *Installer) replaceBinary(newPath string) error {
	// 设置可执行权限
	if err := os.Chmod(newPath, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// 移动到目标位置
	tmpPath := i.binPath + ".new"
	if err := i.copyFile(newPath, tmpPath); err != nil {
		return fmt.Errorf("copy to tmp: %w", err)
	}

	// 原子替换
	if err := os.Rename(tmpPath, i.binPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// copyFile 复制文件
func (i *Installer) copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

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

// getCurrentVersion 获取当前版本
func (i *Installer) getCurrentVersion() string {
	versionFile := filepath.Join(i.dataDir, "VERSION")
	data, err := os.ReadFile(versionFile)
	if err != nil {
		return "unknown"
	}
	return string(data)
}

// CleanupOldBackups 清理旧备份（保留最近N个）
func (i *Installer) CleanupOldBackups(keepCount int) error {
	entries, err := os.ReadDir(i.backupDir)
	if err != nil {
		return err
	}

	if len(entries) <= keepCount {
		return nil
	}

	// 删除最旧的文件
	for idx, entry := range entries {
		if idx >= len(entries)-keepCount {
			break
		}
		path := filepath.Join(i.backupDir, entry.Name())
		os.Remove(path)
	}

	return nil
}
