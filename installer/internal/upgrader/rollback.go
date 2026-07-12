package upgrader

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Rollback 回滚到上一个版本
func Rollback(dataDir, masterURL string) error {
	start := time.Now()

	// 1. 查找最新备份
	fmt.Println("▶ 查找最新备份 ...")
	backupDir := filepath.Join(dataDir, "backups")
	backupPath, version, err := findLatestBackup(backupDir)
	if err != nil {
		return fmt.Errorf("find backup: %w", err)
	}
	fmt.Printf("  找到备份: %s (%s)\n", version, backupPath)

	// 2. 验证备份文件
	fmt.Println("▶ 验证备份文件 ...")
	if err := verifyBackup(backupPath); err != nil {
		return fmt.Errorf("verify backup: %w", err)
	}

	// 3. 恢复二进制文件
	fmt.Println("▶ 恢复二进制文件 ...")
	binPath := "/usr/local/bin/kx-gateway"
	if err := replaceFile(backupPath, binPath); err != nil {
		return fmt.Errorf("restore binary: %w", err)
	}

	// 4. 重启服务
	fmt.Println("▶ 重启服务 ...")
	if err := restartService(); err != nil {
		return fmt.Errorf("restart service: %w (需要人工介入)", err)
	}

	// 5. 健康检查
	fmt.Println("▶ 健康检查 ...")
	if err := healthCheck(3, 1*time.Second); err != nil {
		fmt.Println("❌ 健康检查失败，需要人工介入")
		fmt.Println("\n恢复步骤：")
		fmt.Printf("  1. 检查服务状态: systemctl status kx-gateway\n")
		fmt.Printf("  2. 查看日志: journalctl -u kx-gateway -n 100\n")
		fmt.Printf("  3. 手动恢复备份: cp %s %s\n", backupPath, binPath)
		fmt.Printf("  4. 重启服务: systemctl restart kx-gateway\n")
		return fmt.Errorf("health check failed: %w", err)
	}

	// 6. 更新版本文件
	versionFile := filepath.Join(dataDir, "app", "VERSION")
	if err := os.WriteFile(versionFile, []byte(version+"\n"), 0644); err != nil {
		fmt.Printf("⚠️  写入 VERSION 失败: %v\n", err)
	}

	// 7. 上报回滚（可选）
	client := NewClient(masterURL)
	_ = client.ReportRollback(context.Background(), &ReportRollbackRequest{
		InstanceID: "local",
		ToVersion:  version,
		Reason:     "manual rollback",
	})

	fmt.Printf("✅ 回滚完成: %s (耗时 %v)\n", version, time.Since(start))
	return nil
}

// findLatestBackup 查找最新备份
func findLatestBackup(backupDir string) (path string, version string, err error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return "", "", fmt.Errorf("read backup dir: %w", err)
	}

	type backupFile struct {
		path    string
		modTime time.Time
		version string
	}

	var backups []backupFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if filepath.Ext(name) != ".backup" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// 版本从文件名提取: v1.13.0.backup -> v1.13.0
		version := name[:len(name)-7] // 去掉 .backup

		backups = append(backups, backupFile{
			path:    filepath.Join(backupDir, name),
			modTime: info.ModTime(),
			version: version,
		})
	}

	if len(backups) == 0 {
		return "", "", fmt.Errorf("no backup found")
	}

	// 按修改时间排序，取最新的
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].modTime.After(backups[j].modTime)
	})

	latest := backups[0]
	return latest.path, latest.version, nil
}

// verifyBackup 验证备份文件
func verifyBackup(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	if info.Size() < 1024 {
		return fmt.Errorf("file too small: %d bytes", info.Size())
	}

	return nil
}

// replaceFile 替换文件（在 apply.go 中已定义，这里复用逻辑）
func replaceFileForRollback(src, dst string) error {
	// 设置可执行权限
	if err := os.Chmod(src, 0755); err != nil {
		return err
	}

	// 原子替换
	tmpDst := dst + ".rollback"
	if err := copyFileForRollback(src, tmpDst); err != nil {
		return err
	}

	if err := os.Rename(tmpDst, dst); err != nil {
		os.Remove(tmpDst)
		return err
	}

	return nil
}

// copyFileForRollback 复制文件（在 apply.go 中已定义，这里复用逻辑）
func copyFileForRollback(src, dst string) error {
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
