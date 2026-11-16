package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Installer 安装器（备份 + 校验 + 替换 + 版本元数据 + 迁移 + 重启）
type Installer struct {
	binPath       string
	backupDir     string
	dataDir       string
	migrationCmd  string // 数据库迁移命令（可选）
	migrationArgs []string
	serviceName   string // 安装完成后重启的 systemd 服务名
	// minBinarySize 二进制最小字节数。Go 编译出来的网关二进制远大于 1KB；
	// 用一个明显偏小的阈值挡掉「下到一个 HTML 错误页就当二进制装」的情况。
	minBinarySize int64
}

// NewInstaller 创建安装器
func NewInstaller(binPath, backupDir, dataDir string) *Installer {
	return &Installer{
		binPath:       binPath,
		backupDir:     backupDir,
		dataDir:       dataDir,
		minBinarySize: 1 << 20, // 1 MiB
	}
}

// SetServiceName enables an automatic systemctl restart of the named service
// after a successful install. Pass an empty string to skip the restart step.
func (i *Installer) SetServiceName(name string) {
	i.serviceName = strings.TrimSpace(name)
}

// SetBinaryPath 覆盖被替换的二进制路径。
func (i *Installer) SetBinaryPath(path string) {
	i.binPath = strings.TrimSpace(path)
}

// SetBackupDir 覆盖备份目录。
func (i *Installer) SetBackupDir(dir string) {
	i.backupDir = strings.TrimSpace(dir)
}

// SetDataDir 覆盖数据目录（VERSION / version.json 所在目录）。
func (i *Installer) SetDataDir(dir string) {
	i.dataDir = strings.TrimSpace(dir)
}

// SetMinBinarySize 覆盖二进制最小字节数（测试用）。
func (i *Installer) SetMinBinarySize(size int64) {
	i.minBinarySize = size
}

// RestartEnabled 报告是否配置了安装后重启。
func (i *Installer) RestartEnabled() bool {
	return strings.TrimSpace(i.serviceName) != ""
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

// InstallOptions 是一次安装的可选参数。
type InstallOptions struct {
	// VersionJSON 是新版本的版本元数据（version.json 内容）。
	// 传 nil 时由安装器按 release 合成。
	VersionJSON []byte
	// SkipMigration 跳过数据库迁移（由外部编排迁移时机时使用）。
	SkipMigration bool
}

// Install 安装新版本（备份 + 替换）
//
// 保留旧签名以兼容既有调用方；新代码用 InstallWithOptions。
func (i *Installer) Install(ctx context.Context, downloadPath string, release *Release) (*InstallResult, error) {
	return i.InstallWithOptions(ctx, downloadPath, release, InstallOptions{})
}

// InstallWithOptions 安装新版本（备份 + 校验 + 替换 + 版本元数据 + 迁移 + 重启）。
func (i *Installer) InstallWithOptions(ctx context.Context, downloadPath string, release *Release, opts InstallOptions) (*InstallResult, error) {
	start := time.Now()
	result := &InstallResult{
		OldVersion: i.getCurrentVersion(),
	}
	if release != nil {
		result.NewVersion = release.Version
	}

	// 1. 创建备份目录
	if err := os.MkdirAll(i.backupDir, 0755); err != nil {
		result.Error = fmt.Sprintf("create backup dir: %v", err)
		return result, err
	}

	// 2. 备份当前二进制
	timestamp := time.Now().Format("20060102_150405")
	backupPath := filepath.Join(i.backupDir, fmt.Sprintf("llm-gateway-go_%s_%s", sanitizeVersionForFilename(result.OldVersion), timestamp))

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
		_ = i.copyFile(backupPath, i.binPath)
		result.Error = fmt.Sprintf("replace binary: %v", err)
		return result, err
	}

	// 5. 写入版本信息（only on success — a partial install must not
	//    leave a future restart pointing at an incompatible binary).
	//
	// ★ 版本真源：网关运行时读的是 dataDir/version.json（见 cmd/gateway/version.go），
	//   VERSION 文件只是历史兼容物。所以两者都写，且 version.json 优先。
	if err := i.writeVersionFiles(release, opts.VersionJSON); err != nil {
		result.Error = fmt.Sprintf("write version file: %v", err)
		if rbErr := i.copyFile(backupPath, i.binPath); rbErr != nil {
			result.Error += fmt.Sprintf("; rollback also failed: %v", rbErr)
		}
		result.DurationMs = time.Since(start).Milliseconds()
		return result, fmt.Errorf("write version file: %w", err)
	}

	// 6. 数据库迁移（P1 修复：协调二进制升级与数据库迁移）
	if i.migrationCmd != "" && !opts.SkipMigration {
		if err := i.runMigration(ctx); err != nil {
			result.Error = fmt.Sprintf("install succeeded but migration failed: %v", err)
			if rbErr := i.copyFile(backupPath, i.binPath); rbErr != nil {
				result.Error += fmt.Sprintf("; rollback also failed: %v", rbErr)
			}
			_ = i.restoreVersionFiles(result.OldVersion)
			result.Success = false
			result.DurationMs = time.Since(start).Milliseconds()
			return result, fmt.Errorf("migration failed: %w", err)
		}
	}

	// 7. 重启服务（可选 — 通过 SetServiceName 配置）
	if i.serviceName != "" {
		if err := i.restartService(ctx); err != nil {
			result.Error = fmt.Sprintf("install succeeded but service restart failed: %v", err)
			if rbErr := i.copyFile(backupPath, i.binPath); rbErr != nil {
				result.Error += fmt.Sprintf("; rollback also failed: %v", rbErr)
			}
			_ = i.restoreVersionFiles(result.OldVersion)
			result.Success = false
			result.DurationMs = time.Since(start).Milliseconds()
			return result, fmt.Errorf("service restart failed: %w", err)
		}
	} else {
		slog.Info("installer: service restart skipped (no service_name configured)")
	}

	result.Success = true
	result.DurationMs = time.Since(start).Milliseconds()
	return result, nil
}

// verifyBinary 验证新二进制。
//
// ★ 顺序很重要：**先做结构校验，再做语义探测**。
//
// 旧实现在这里直接跑 `<binary> --version`。但网关二进制只认 `migrate` 子命令，
// 传 `--version` 会让它**整体启动**（连数据库、抢监听端口），5 秒后被 context
// 杀掉 ⇒ verify 恒失败 ⇒ 安装 100% 回滚。
//
// 现在：
//  1. 结构校验（可执行位 + 魔数 + 最小体积）—— 确定性、不执行被装文件。
//  2. 语义探测（`<binary> --version`）—— **尽力而为**：超时/非零退出只记 WARN，
//     因为不同版本的二进制未必都实现了 --version。
func (i *Installer) verifyBinary(path string) error {
	if err := i.verifyBinaryStructure(path); err != nil {
		return err
	}
	if err := i.probeBinaryVersion(path); err != nil {
		slog.Warn("installer: binary version probe failed (continuing — structure check passed)",
			"path", path, "error", err)
	}
	return nil
}

// verifyBinaryStructure 校验二进制的结构特征（不执行被装文件）。
func (i *Installer) verifyBinaryStructure(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("path is a directory")
	}
	minSize := i.minBinarySize
	if minSize <= 0 {
		minSize = 1 << 20
	}
	if info.Size() < minSize {
		return fmt.Errorf("file too small: %d bytes (expected at least %d)", info.Size(), minSize)
	}
	if info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("file is not executable: mode %s", info.Mode().Perm())
	}
	magic, err := readMagic(path, 4)
	if err != nil {
		return fmt.Errorf("read magic: %w", err)
	}
	if !isExecutableMagic(magic) {
		return fmt.Errorf("not an executable binary: magic %x", magic)
	}
	return nil
}

// isExecutableMagic 判断文件头是否为常见可执行格式。
func isExecutableMagic(magic []byte) bool {
	if len(magic) < 2 {
		return false
	}
	switch {
	case magic[0] == 0x7f && magic[1] == 'E': // ELF
		return true
	case magic[0] == 0xCF && magic[1] == 0xFA: // Mach-O 64
		return true
	case magic[0] == 0xCE && magic[1] == 0xFA: // Mach-O 32
		return true
	case magic[0] == 0xCA && magic[1] == 0xFE: // Mach-O universal
		return true
	case magic[0] == 'M' && magic[1] == 'Z': // PE
		return true
	default:
		return false
	}
}

// readMagic 读取文件头若干字节。
func readMagic(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	read, err := f.Read(buf)
	if err != nil && read == 0 {
		return nil, err
	}
	return buf[:read], nil
}

// probeBinaryVersion 尽力而为地跑一次 `--version`。
func (i *Installer) probeBinaryVersion(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("execute --version: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("--version produced no output")
	}
	return nil
}

// writeVersionFiles 写入版本元数据：version.json（真源）+ VERSION（兼容）。
func (i *Installer) writeVersionFiles(release *Release, versionJSON []byte) error {
	newVersion := ""
	if release != nil {
		newVersion = release.Version
	}
	if newVersion == "" {
		newVersion = "unknown"
	}

	payload := versionJSON
	if len(payload) == 0 {
		payload = i.synthesizeVersionJSON(newVersion)
	}
	if err := os.WriteFile(filepath.Join(i.dataDir, "version.json"), payload, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(i.dataDir, "VERSION"), []byte(newVersion+"\n"), 0644); err != nil {
		return err
	}
	return nil
}

// restoreVersionFiles 回滚版本元数据（仅回写 VERSION，version.json 保持原值）。
func (i *Installer) restoreVersionFiles(oldVersion string) error {
	if oldVersion == "" {
		return nil
	}
	return os.WriteFile(filepath.Join(i.dataDir, "VERSION"), []byte(oldVersion+"\n"), 0644)
}

// synthesizeVersionJSON 在没有显式元数据时按 release 合成一份 version.json。
func (i *Installer) synthesizeVersionJSON(version string) []byte {
	info := map[string]any{
		"version":    version,
		"git_tag":    "v0.0.0",
		"git_sha":    "unknown",
		"build_seq":  0,
		"build_date": time.Now().UTC().Format("20060102"),
		"module":     "llm-gateway-go",
	}
	// 保留已有的 git_sha / build_seq，避免把已知信息抹掉。
	if raw, err := os.ReadFile(filepath.Join(i.dataDir, "version.json")); err == nil {
		var existing map[string]any
		if err := json.Unmarshal(raw, &existing); err == nil {
			for _, key := range []string{"git_tag", "git_sha", "build_seq", "build_date", "module"} {
				if v, ok := existing[key]; ok {
					info[key] = v
				}
			}
		}
	}
	encoded, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return []byte("{\n  \"version\": \"" + version + "\"\n}\n")
	}
	return append(encoded, '\n')
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

// getCurrentVersion 获取当前版本。
//
// 优先读 version.json（网关运行时的真源），其次读 VERSION 文件（历史兼容）。
func (i *Installer) getCurrentVersion() string {
	if raw, err := os.ReadFile(filepath.Join(i.dataDir, "version.json")); err == nil {
		var info struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &info); err == nil {
			if v := strings.TrimSpace(info.Version); v != "" {
				return v
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(i.dataDir, "VERSION")); err == nil {
		if v := strings.TrimSpace(string(raw)); v != "" {
			return v
		}
	}
	return "unknown"
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
		_ = os.Remove(path)
	}

	return nil
}

// RestartService 显式重启服务（供执行器在响应返回后调用）。
func (i *Installer) RestartService(ctx context.Context) error {
	return i.restartService(ctx)
}

// restartService 重启服务（由 SetServiceName 显式启用）
func (i *Installer) restartService(ctx context.Context) error {
	serviceName := i.serviceName
	if serviceName == "" {
		return errors.New("service name not configured")
	}

	cmd := exec.CommandContext(ctx, "systemctl", "restart", serviceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart %s failed: %w, output: %s", serviceName, err, string(output))
	}

	// 等待服务启动
	time.Sleep(3 * time.Second)

	// 验证服务状态
	statusCmd := exec.CommandContext(ctx, "systemctl", "is-active", serviceName)
	statusOutput, err := statusCmd.CombinedOutput()
	if err != nil || string(statusOutput) != "active\n" {
		return fmt.Errorf("service %s not active after restart: %s", serviceName, string(statusOutput))
	}

	return nil
}

// runMigration 运行数据库迁移（P1 修复：协调二进制升级与数据库迁移）
func (i *Installer) runMigration(ctx context.Context) error {
	if i.migrationCmd == "" {
		return nil
	}

	cmd := exec.CommandContext(ctx, i.migrationCmd, i.migrationArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("migration command %s failed: %w, output: %s", i.migrationCmd, err, string(output))
	}

	return nil
}

// SetMigration 配置数据库迁移命令（P1：可选配置）
func (i *Installer) SetMigration(cmd string, args ...string) {
	i.migrationCmd = cmd
	i.migrationArgs = args
}

// sanitizeVersionForFilename 把版本号里的路径分隔符换掉，避免备份文件名越界。
func sanitizeVersionForFilename(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_", ":", "_")
	return replacer.Replace(v)
}
