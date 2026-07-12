// Package upgrader 提供离线升级包处理功能
package upgrader

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OfflineApplier 离线升级包应用器
type OfflineApplier struct {
	dataDir       string
	backupDir     string
	composeFile   string
	containerName string
}

// NewOfflineApplier 创建离线升级应用器
func NewOfflineApplier(dataDir, backupDir, composeFile, containerName string) *OfflineApplier {
	return &OfflineApplier{
		dataDir:       dataDir,
		backupDir:     backupDir,
		composeFile:   composeFile,
		containerName: containerName,
	}
}

// ApplyOfflinePackage 应用离线升级包
//
// 流程：
// 1. 解压 tar.gz 到临时目录
// 2. 验证 manifest.json 的 SHA256
// 3. 备份当前二进制 + docker-compose.yml + .env
// 4. docker load < images.tar（加载新镜像）
// 5. 执行 SQL 迁移（migrations/*.sql）
// 6. 替换二进制 + 写 VERSION 文件
// 7. 重启 docker-compose
// 8. 5s 内探测 /healthz，失败自动 rollback
func (a *OfflineApplier) ApplyOfflinePackage(tarPath, dataDir string) error {
	ctx := context.Background()

	// 1. 解压到临时目录
	tmpDir, err := os.MkdirTemp("", "offline-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := a.extractTarGz(tarPath, tmpDir); err != nil {
		return fmt.Errorf("extract tar.gz: %w", err)
	}

	// 2. 加载并验证 manifest
	manifestPath := filepath.Join(tmpDir, "manifest.json")
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}

	if err := VerifyManifest(manifest, tmpDir); err != nil {
		return fmt.Errorf("verify manifest: %w", err)
	}

	// 3. 备份当前状态
	backupPath, err := a.backupCurrentState()
	if err != nil {
		return fmt.Errorf("backup current state: %w", err)
	}

	// 从这里开始，任何失败都需要回滚
	var rollbackNeeded bool
	defer func() {
		if rollbackNeeded {
			_ = a.rollbackFromBackup(backupPath)
		}
	}()

	// 4. 加载 Docker 镜像（可选，失败仅警告）
	imagesPath := filepath.Join(tmpDir, "images.tar")
	if _, err := os.Stat(imagesPath); err == nil {
		if err := a.loadDockerImages(imagesPath); err != nil {
			// docker load 失败不阻止升级，仅记录警告
			fmt.Printf("⚠️  Warning: docker load failed: %v\n", err)
		}
	}

	// 5. 执行 SQL 迁移
	migrationsDir := filepath.Join(tmpDir, "migrations")
	if _, err := os.Stat(migrationsDir); err == nil {
		if err := a.executeMigrations(ctx, migrationsDir); err != nil {
			rollbackNeeded = true
			return fmt.Errorf("execute migrations: %w", err)
		}
	}

	// 6. 替换二进制
	binaryPath := filepath.Join(tmpDir, "kx-gateway")
	if _, err := os.Stat(binaryPath); err == nil {
		targetBinary := filepath.Join(dataDir, "kx-gateway")
		if err := a.replaceBinary(binaryPath, targetBinary); err != nil {
			rollbackNeeded = true
			return fmt.Errorf("replace binary: %w", err)
		}
	}

	// 7. 写入 VERSION 文件
	versionPath := filepath.Join(dataDir, "VERSION")
	versionContent := fmt.Sprintf("%s\n%d", manifest.Version, manifest.BuildSeq)
	if err := os.WriteFile(versionPath, []byte(versionContent), 0644); err != nil {
		rollbackNeeded = true
		return fmt.Errorf("write VERSION: %w", err)
	}

	// 8. 重启 docker-compose
	if err := a.restartDockerCompose(); err != nil {
		rollbackNeeded = true
		return fmt.Errorf("restart docker-compose: %w", err)
	}

	// 9. 健康检查
	if err := a.healthCheck(5 * time.Second); err != nil {
		rollbackNeeded = true
		return fmt.Errorf("health check failed: %w", err)
	}

	return nil
}

// extractTarGz 解压 .tar.gz 文件
func (a *OfflineApplier) extractTarGz(tarPath, destDir string) error {
	file, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		target := filepath.Join(destDir, header.Name)

		// 安全检查：防止路径穿越
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		}
	}

	return nil
}

// backupCurrentState 备份当前状态（二进制 + docker-compose.yml + .env）
func (a *OfflineApplier) backupCurrentState() (string, error) {
	timestamp := time.Now().Format("20060102_150405")
	backupPath := filepath.Join(a.backupDir, fmt.Sprintf("backup_%s", timestamp))

	if err := os.MkdirAll(backupPath, 0755); err != nil {
		return "", err
	}

	// 备份二进制
	binaryPath := filepath.Join(a.dataDir, "kx-gateway")
	if _, err := os.Stat(binaryPath); err == nil {
		if err := a.copyFile(binaryPath, filepath.Join(backupPath, "kx-gateway")); err != nil {
			return "", fmt.Errorf("backup binary: %w", err)
		}
	}

	// 备份 docker-compose.yml
	if _, err := os.Stat(a.composeFile); err == nil {
		if err := a.copyFile(a.composeFile, filepath.Join(backupPath, "docker-compose.yml")); err != nil {
			return "", fmt.Errorf("backup docker-compose.yml: %w", err)
		}
	}

	// 备份 .env
	envPath := filepath.Join(filepath.Dir(a.composeFile), ".env")
	if _, err := os.Stat(envPath); err == nil {
		if err := a.copyFile(envPath, filepath.Join(backupPath, ".env")); err != nil {
			return "", fmt.Errorf("backup .env: %w", err)
		}
	}

	return backupPath, nil
}

// rollbackFromBackup 从备份恢复
func (a *OfflineApplier) rollbackFromBackup(backupPath string) error {
	// 恢复二进制
	backupBinary := filepath.Join(backupPath, "kx-gateway")
	if _, err := os.Stat(backupBinary); err == nil {
		targetBinary := filepath.Join(a.dataDir, "kx-gateway")
		if err := a.copyFile(backupBinary, targetBinary); err != nil {
			return fmt.Errorf("restore binary: %w", err)
		}
	}

	// 恢复 docker-compose.yml
	backupCompose := filepath.Join(backupPath, "docker-compose.yml")
	if _, err := os.Stat(backupCompose); err == nil {
		if err := a.copyFile(backupCompose, a.composeFile); err != nil {
			return fmt.Errorf("restore docker-compose.yml: %w", err)
		}
	}

	// 恢复 .env
	backupEnv := filepath.Join(backupPath, ".env")
	envPath := filepath.Join(filepath.Dir(a.composeFile), ".env")
	if _, err := os.Stat(backupEnv); err == nil {
		if err := a.copyFile(backupEnv, envPath); err != nil {
			return fmt.Errorf("restore .env: %w", err)
		}
	}

	// 重启服务
	if err := a.restartDockerCompose(); err != nil {
		return fmt.Errorf("restart after rollback: %w", err)
	}

	return nil
}

// loadDockerImages 加载 Docker 镜像
func (a *OfflineApplier) loadDockerImages(imagesPath string) error {
	cmd := exec.Command("docker", "load", "-i", imagesPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker load: %w, output: %s", err, string(output))
	}
	return nil
}

// executeMigrations 执行 SQL 迁移（按文件名顺序）
func (a *OfflineApplier) executeMigrations(ctx context.Context, migrationsDir string) error {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return err
	}

	// 按文件名排序（376_*.sql → 377_*.sql）
	var sqlFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			sqlFiles = append(sqlFiles, entry.Name())
		}
	}
	sort.Strings(sqlFiles)

	// 逐个执行
	for _, sqlFile := range sqlFiles {
		sqlPath := filepath.Join(migrationsDir, sqlFile)
		if err := a.executeSQLFile(ctx, sqlPath); err != nil {
			return fmt.Errorf("execute %s: %w", sqlFile, err)
		}
	}

	return nil
}

// executeSQLFile 执行单个 SQL 文件
func (a *OfflineApplier) executeSQLFile(ctx context.Context, sqlPath string) error {
	// 使用 docker exec 在容器内执行
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", a.containerName,
		"psql", "-U", "kxuser", "-d", "llm_gateway", "-f", "-")

	sqlContent, err := os.ReadFile(sqlPath)
	if err != nil {
		return err
	}

	cmd.Stdin = strings.NewReader(string(sqlContent))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql error: %w, output: %s", err, string(output))
	}

	return nil
}

// replaceBinary 替换二进制文件
func (a *OfflineApplier) replaceBinary(srcPath, dstPath string) error {
	// 设置可执行权限
	if err := os.Chmod(srcPath, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// 原子替换
	tmpPath := dstPath + ".new"
	if err := a.copyFile(srcPath, tmpPath); err != nil {
		return fmt.Errorf("copy to tmp: %w", err)
	}

	if err := os.Rename(tmpPath, dstPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// restartDockerCompose 重启 docker-compose
func (a *OfflineApplier) restartDockerCompose() error {
	cmd := exec.Command("docker-compose", "-f", a.composeFile, "restart")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker-compose restart: %w, output: %s", err, string(output))
	}
	return nil
}

// healthCheck 健康检查（探测 /healthz）
func (a *OfflineApplier) healthCheck(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// 尝试访问 /healthz
		cmd := exec.Command("curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "http://localhost:8781/healthz")
		output, err := cmd.Output()
		if err == nil && string(output) == "200" {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("health check timeout after %v", timeout)
}

// copyFile 复制文件
func (a *OfflineApplier) copyFile(src, dst string) error {
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
