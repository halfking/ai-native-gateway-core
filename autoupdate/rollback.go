package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Rollback 回滚器（恢复备份 + 清理）
type Rollback struct {
	binPath      string
	backupDir    string
	dataDir      string
	envFile      string // 主机配置文件（回滚时还原其备份；默认 <dataDir>/.env）
	serviceName  string // 可选：回滚后重启的 systemd 服务名
	healthURL    string // 可选：默认 http://localhost:8781/healthz
	startWait    time.Duration
	healthClient *http.Client
}

// NewRollback 创建回滚器
func NewRollback(binPath, backupDir, dataDir string) *Rollback {
	return &Rollback{
		binPath:      binPath,
		backupDir:    backupDir,
		dataDir:      dataDir,
		envFile:      filepath.Join(dataDir, ".env"),
		healthURL:    "http://localhost:8781/healthz",
		startWait:    5 * time.Second,
		healthClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetServiceName enables a systemctl restart of the named service after
// the rollback. Pass an empty string to skip the restart step.
func (r *Rollback) SetServiceName(name string) {
	r.serviceName = strings.TrimSpace(name)
}

// SetEnvFile 覆盖主机配置文件路径（与 Installer.SetEnvFile 对应）。
func (r *Rollback) SetEnvFile(path string) {
	if p := strings.TrimSpace(path); p != "" {
		r.envFile = p
	}
}

// SetHealthURL overrides the post-rollback health-check endpoint. The
// default targets /healthz on port 8781 to match the gateway's default
// listen address; pass the listener address your deployment exposes.
func (r *Rollback) SetHealthURL(url string) {
	if url = strings.TrimSpace(url); url != "" {
		r.healthURL = url
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

	// 3.5 还原主机配置备份（升级配置合同的回退侧：回滚到旧版本 ⇒ 配置也回到
	// 备份时的原值）。找不到配置备份时跳过——旧行为（配置未被动过）不受影响。
	if err := r.restoreEnvBackup(); err != nil {
		result.Error = fmt.Sprintf("restore env backup: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result, fmt.Errorf("restore env backup after rollback: %w", err)
	}

	// 4. 更新版本元数据（version.json 真源 + VERSION 兼容文件）
	if err := r.writeRollbackVersionFiles(targetVersion); err != nil {
		result.Error = fmt.Sprintf("write version files: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result, fmt.Errorf("write version files after rollback: %w", err)
	}

	// 5. 重启服务（可选 — 与 install 对称）
	if r.serviceName != "" {
		if err := r.restartService(ctx); err != nil {
			result.Error = fmt.Sprintf("rollback succeeded but service restart failed: %v", err)
			result.Success = false
			result.DurationMs = time.Since(start).Milliseconds()
			return result, fmt.Errorf("service restart failed after rollback: %w", err)
		}
	}

	// 6. 健康检查：验证回滚后的服务可用
	if err := r.healthCheck(ctx); err != nil {
		result.Error = fmt.Sprintf("rollback succeeded but health check failed: %v", err)
		result.Success = false
		result.DurationMs = time.Since(start).Milliseconds()
		return result, fmt.Errorf("health check failed after rollback: %w", err)
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
		if !contains(name, version) {
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

// restoreEnvBackup 把 backupDir 里最新的 env 备份（<env名>_<时间戳> 形态，
// 与二进制备份同目录同命名风格）还原到配置文件路径。二进制已回到旧版本，
// 配置若被强制更新过也必须一起回去，否则旧二进制读新配置可能起不来。
func (r *Rollback) restoreEnvBackup() error {
	envFile := r.envFile
	if strings.TrimSpace(envFile) == "" {
		return nil
	}
	entries, err := os.ReadDir(r.backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read backup dir: %w", err)
	}
	base := filepath.Base(envFile) + "_"
	var latest string
	var latestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), base) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latest = filepath.Join(r.backupDir, entry.Name())
		}
	}
	if latest == "" {
		return nil
	}
	tmp := envFile + ".rollback"
	if err := r.copyFile(latest, tmp); err != nil {
		return fmt.Errorf("copy env backup: %w", err)
	}
	if err := os.Chmod(tmp, 0o640); err != nil {
		return fmt.Errorf("chmod env backup: %w", err)
	}
	if err := os.Rename(tmp, envFile); err != nil {
		return fmt.Errorf("replace env file: %w", err)
	}
	return nil
}

// writeRollbackVersionFiles 把版本元数据重写成 targetVersion。
//
// ★ 必须写 version.json（网关运行时的版本真源，见 cmd/gateway/version.go 与
// installer.go 步骤 5 的版本身份漂移注释）。此前手动回滚只还原 VERSION：
// 二进制回到旧版、version.json 仍报新版号 ⇒ Checker 拿错误基线去问远端 ⇒
// 「已是最新」⇒ 节点永远停在旧二进制上且再也不会被提醒。
//
// 手动回滚没有 installer 那样的旧元数据快照（备份目录只存二进制），所以按
// 现有 version.json 合成：version 与被恢复的二进制对齐，其余字段尽量保留。
func (r *Rollback) writeRollbackVersionFiles(targetVersion string) error {
	jsonPath := filepath.Join(r.dataDir, "version.json")
	info := map[string]any{
		"version":    targetVersion,
		"git_tag":    "v" + strings.TrimPrefix(targetVersion, "v"),
		"git_sha":    "unknown",
		"build_seq":  0,
		"build_date": time.Now().UTC().Format("20060102"),
		"module":     "llm-gateway-go",
	}
	if raw, err := os.ReadFile(jsonPath); err == nil {
		var existing map[string]any
		if err := json.Unmarshal(raw, &existing); err == nil {
			for key := range info {
				if key == "version" {
					continue
				}
				if v, ok := existing[key]; ok {
					info[key] = v
				}
			}
		}
	}
	encoded, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		encoded = []byte(`{"version": "` + targetVersion + `"}`)
	}
	if err := os.WriteFile(jsonPath, append(encoded, '\n'), 0644); err != nil {
		return err
	}
	versionFile := filepath.Join(r.dataDir, "VERSION")
	if err := os.WriteFile(versionFile, []byte(targetVersion+"\n"), 0644); err != nil {
		return err
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

// contains 检查字符串是否包含子串（P0 修复：使用标准库）
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// healthCheck 健康检查（P1 修复：回滚后验证服务可用）。
//
// The endpoint defaults to http://<gateway>/healthz — the gateway's
// configured health probe. Override via SetHealthURL when the deployment
// uses a reverse proxy or a different listen address.
func (r *Rollback) healthCheck(ctx context.Context) error {
	healthURL := r.healthURL
	if healthURL == "" {
		healthURL = "http://localhost:8781/healthz"
	}

	// 给服务启动时间（默认 5 秒，可被 startWait 覆盖）
	if r.startWait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.startWait):
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
	if err != nil {
		return fmt.Errorf("create health check request: %w", err)
	}

	client := r.healthClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}

	return nil
}

// restartService restarts the configured systemd unit (if any).
func (r *Rollback) restartService(ctx context.Context) error {
	if r.serviceName == "" {
		return errors.New("service name not configured")
	}
	cmd := exec.CommandContext(ctx, "systemctl", "restart", r.serviceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart %s failed: %w, output: %s", r.serviceName, err, string(output))
	}
	time.Sleep(3 * time.Second)
	statusCmd := exec.CommandContext(ctx, "systemctl", "is-active", r.serviceName)
	statusOutput, err := statusCmd.CombinedOutput()
	if err != nil || string(statusOutput) != "active\n" {
		return fmt.Errorf("service %s not active after restart: %s", r.serviceName, string(statusOutput))
	}
	return nil
}
