package upgrader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ApplyUpdate 应用更新（下载 → 备份 → 替换 → 验证 → 上报）
func ApplyUpdate(release *Release, dataDir, masterURL string) error {
	start := time.Now()

	// 1. 确保备份目录存在
	backupDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}

	// 2. 下载新版本
	fmt.Printf("▶ 下载 %s ...\n", release.Version)
	downloadPath := filepath.Join(os.TempDir(), fmt.Sprintf("kx-gateway-%s", release.Version))
	if err := downloadFile(release.DownloadURL, downloadPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer os.Remove(downloadPath)

	// 3. SHA256 验证
	fmt.Println("▶ 验证 SHA256 ...")
	if err := verifyChecksum(downloadPath, release.SHA256); err != nil {
		return fmt.Errorf("checksum verification failed: %w", err)
	}

	// 4. 备份当前二进制
	fmt.Println("▶ 备份当前版本 ...")
	binPath := "/usr/local/bin/kx-gateway"
	currentVersion := getCurrentVersion(dataDir)
	backupPath := filepath.Join(backupDir, fmt.Sprintf("%s.backup", currentVersion))
	if err := copyFile(binPath, backupPath); err != nil {
		return fmt.Errorf("backup: %w", err)
	}

	// 5. 替换二进制
	fmt.Println("▶ 替换二进制文件 ...")
	if err := replaceFile(downloadPath, binPath); err != nil {
		// 回滚
		_ = copyFile(backupPath, binPath)
		return fmt.Errorf("replace binary: %w", err)
	}

	// 6. 重启服务
	fmt.Println("▶ 重启服务 ...")
	if err := restartService(); err != nil {
		// 回滚
		_ = copyFile(backupPath, binPath)
		_ = restartService()
		return fmt.Errorf("restart service: %w", err)
	}

	// 7. 健康检查（3 次重试，间隔 1s）
	fmt.Println("▶ 健康检查 ...")
	if err := healthCheck(3, 1*time.Second); err != nil {
		// 回滚
		fmt.Println("⚠️  健康检查失败，自动回滚 ...")
		_ = copyFile(backupPath, binPath)
		_ = restartService()
		return fmt.Errorf("health check failed, rolled back: %w", err)
	}

	// 8. 写入新版本信息
	versionFile := filepath.Join(dataDir, "app", "VERSION")
	if err := os.WriteFile(versionFile, []byte(release.Version+"\n"), 0644); err != nil {
		fmt.Printf("⚠️  写入 VERSION 失败: %v\n", err)
	}

	// 9. 清理旧备份（保留最近 3 个）
	if err := cleanupOldBackups(backupDir, 3); err != nil {
		fmt.Printf("⚠️  清理旧备份失败: %v\n", err)
	}

	// 10. 上报结果（可选）
	client := NewClient(masterURL)
	_ = client.ReportUpdate(context.Background(), &ReportUpdateRequest{
		InstanceID: "local", // 实际应从配置读取
		Version:    release.Version,
		Status:     "success",
		DurationMs: time.Since(start).Milliseconds(),
	})

	fmt.Printf("✅ 升级完成: %s (耗时 %v)\n", release.Version, time.Since(start))
	return nil
}

// downloadFile 下载文件
func downloadFile(url, destPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}

	return nil
}

// verifyChecksum 验证 SHA256
func verifyChecksum(filePath, expectedChecksum string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actualChecksum := hex.EncodeToString(h.Sum(nil))
	if actualChecksum != expectedChecksum {
		return fmt.Errorf("expected %s, got %s", expectedChecksum, actualChecksum)
	}

	return nil
}

// copyFile 复制文件
func copyFile(src, dst string) error {
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

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	// 复制权限
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, srcInfo.Mode())
}

// replaceFile 替换文件
func replaceFile(src, dst string) error {
	// 设置可执行权限
	if err := os.Chmod(src, 0755); err != nil {
		return err
	}

	// 原子替换
	tmpDst := dst + ".new"
	if err := copyFile(src, tmpDst); err != nil {
		return err
	}

	if err := os.Rename(tmpDst, dst); err != nil {
		os.Remove(tmpDst)
		return err
	}

	return nil
}

// restartService 重启服务
func restartService() error {
	cmd := exec.Command("systemctl", "restart", "kx-gateway")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl restart: %w", err)
	}
	return nil
}

// healthCheck 健康检查
func healthCheck(retries int, interval time.Duration) error {
	client := &http.Client{Timeout: 5 * time.Second}

	for i := 0; i < retries; i++ {
		if i > 0 {
			time.Sleep(interval)
		}

		resp, err := client.Get("http://localhost:8781/healthz")
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}
	}

	return fmt.Errorf("health check failed after %d retries", retries)
}

// getCurrentVersion 获取当前版本
func getCurrentVersion(dataDir string) string {
	versionFile := filepath.Join(dataDir, "app", "VERSION")
	data, err := os.ReadFile(versionFile)
	if err != nil {
		return "unknown"
	}
	return string(data)
}

// cleanupOldBackups 清理旧备份（保留最近 N 个）
func cleanupOldBackups(backupDir string, keepCount int) error {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}

	if len(entries) <= keepCount {
		return nil
	}

	// 按修改时间排序，删除最旧的
	type fileInfo struct {
		name    string
		modTime time.Time
	}

	var files []fileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{name: entry.Name(), modTime: info.ModTime()})
	}

	// 简单排序（冒泡）
	for i := 0; i < len(files)-1; i++ {
		for j := i + 1; j < len(files); j++ {
			if files[i].modTime.After(files[j].modTime) {
				files[i], files[j] = files[j], files[i]
			}
		}
	}

	// 删除旧文件
	deleteCount := len(files) - keepCount
	for i := 0; i < deleteCount; i++ {
		path := filepath.Join(backupDir, files[i].name)
		_ = os.Remove(path)
	}

	return nil
}
