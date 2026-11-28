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
	envFile       string // 主机配置文件（升级配置合同的对象；默认 <dataDir>/.env）
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
		envFile:       filepath.Join(dataDir, ".env"),
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

// SetEnvFile 覆盖主机配置文件路径（升级配置合同的对象）。
// 空串回退默认 <dataDir>/.env。传入 systemd EnvironmentFile 的实际路径即可。
func (i *Installer) SetEnvFile(path string) {
	if p := strings.TrimSpace(path); p != "" {
		i.envFile = p
	}
}

// EnvFile 返回生效的主机配置文件路径（日志/排障用）。
func (i *Installer) EnvFile() string { return i.envFile }

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
	// RolledBack=true 表示安装失败后**已经把旧二进制与旧版本元数据都还原了**。
	//
	// ★ 它决定提醒行最终落成什么状态：
	//   - RolledBack=true  ⇒ 节点仍在旧版本上 ⇒ 新版**仍待处理** ⇒ 提醒必须还在
	//   - RolledBack=false ⇒ 装成功 ⇒ 提醒可以撤下
	RolledBack bool
	// ConfigBackupPath 是主机配置文件在应用配置变更前的备份路径（空=未改动配置）。
	ConfigBackupPath string
	// ConfigApplied 是实际写入的配置键（新增 + 强制覆盖）；ConfigKeptExisting 是
	// 主机已有值被保留的键（非强制更新）——升级配置合同要求原配置保值。
	ConfigApplied      []string
	ConfigKeptExisting []string
}

// InstallOptions 是一次安装的可选参数。
type InstallOptions struct {
	// VersionJSON 是新版本的版本元数据（version.json 内容）。
	// 传 nil 时由安装器按 release 合成。
	VersionJSON []byte
	// SkipMigration 跳过数据库迁移（由外部编排迁移时机时使用）。
	SkipMigration bool
	// ConfigUpdates 是本版本要求应用的主机配置变更（新增/强制更新）。
	// 语义见 EnvConfigUpdate；应用前自动备份原配置，任何后续步骤失败都会还原。
	ConfigUpdates []EnvConfigUpdate
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

	// 4.5 应用主机配置变更（升级配置合同：原配置保值，只处理新增/强制更新）。
	//     时机在二进制替换之后、迁移/重启之前：新二进制重启时配置必须已就位；
	//     快照先于改动落盘，之后任何一步失败都随二进制一起还原。
	envSnapshot := i.snapshotEnvFile()
	if len(opts.ConfigUpdates) > 0 {
		cfgBackup, applied, kept, cfgErr := i.applyConfigUpdates(opts.ConfigUpdates)
		if cfgErr != nil {
			result.Error = fmt.Sprintf("apply config updates: %v", cfgErr)
			if rbErr := i.copyFile(backupPath, i.binPath); rbErr != nil {
				result.Error += fmt.Sprintf("; rollback also failed: %v", rbErr)
			}
			_ = i.restoreEnvFile(envSnapshot)
			result.Success = false
			result.RolledBack = true
			result.DurationMs = time.Since(start).Milliseconds()
			return result, fmt.Errorf("apply config updates: %w", cfgErr)
		}
		result.ConfigBackupPath = cfgBackup
		result.ConfigApplied = applied
		result.ConfigKeptExisting = kept
	}

	// 5. 写入版本信息（only on success — a partial install must not
	//    leave a future restart pointing at an incompatible binary).
	//
	// ★ 版本真源：网关运行时读的是 dataDir/version.json（见 cmd/gateway/version.go），
	//   VERSION 文件只是历史兼容物。所以两者都写，且 version.json 优先。
	//
	// ★ 写之前先**快照**旧的版本元数据：后续任何一步失败都要把它原样还原。
	//   否则会出现「二进制已还原成旧版、网关却报新版号」的**版本身份漂移**：
	//   网关的版本真源就是 version.json ⇒ 漂移后 Checker 会拿错误的基线去问远端
	//   ⇒ 远端答「已是最新」⇒ **该节点永远停在旧二进制上，且再也不会被提醒**。
	versionSnapshot := i.snapshotVersionFiles()
	if err := i.writeVersionFiles(release, opts.VersionJSON); err != nil {
		result.Error = fmt.Sprintf("write version file: %v", err)
		if rbErr := i.copyFile(backupPath, i.binPath); rbErr != nil {
			result.Error += fmt.Sprintf("; rollback also failed: %v", rbErr)
		}
		_ = i.restoreEnvFile(envSnapshot)
		_ = i.restoreVersionFiles(versionSnapshot)
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
			_ = i.restoreEnvFile(envSnapshot)
			_ = i.restoreVersionFiles(versionSnapshot)
			result.Success = false
			result.RolledBack = true
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
			// ★ 二进制换回去了，版本元数据**必须**一起还原 —— 否则节点会顶着
			//   新版号跑旧二进制（见步骤 5 的注释）。配置变更同理：原 env 随
			//   快照还原，保证「升级失败 ⇒ 主机回到升级前完整状态」。
			_ = i.restoreEnvFile(envSnapshot)
			_ = i.restoreVersionFiles(versionSnapshot)
			result.Success = false
			result.RolledBack = true
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

// versionSnapshot 是「旧版本元数据」的快照，用于失败时原样还原。
type versionSnapshot struct {
	versionJSON []byte
	versionFile []byte
	hadJSON     bool
	hadFile     bool
}

// snapshotVersionFiles 快照当前版本元数据（version.json + VERSION）。
func (i *Installer) snapshotVersionFiles() versionSnapshot {
	snap := versionSnapshot{}
	if raw, err := os.ReadFile(filepath.Join(i.dataDir, "version.json")); err == nil {
		snap.versionJSON = raw
		snap.hadJSON = true
	}
	if raw, err := os.ReadFile(filepath.Join(i.dataDir, "VERSION")); err == nil {
		snap.versionFile = raw
		snap.hadFile = true
	}
	return snap
}

// restoreVersionFiles 按快照还原版本元数据。
//
// ★ 关键：**version.json 才是真源**（网关运行时读它，见 cmd/gateway/version.go）。
//
//	只还原 VERSION 而留着新版 version.json ⇒ 二进制是旧的、报出来的版本号是新的
//	⇒ **版本身份漂移** ⇒ Checker 拿错基线去问远端 ⇒ 「已是最新」
//	⇒ 节点永远停在旧二进制上，且再也不会被提醒。
//
// 快照里本来就没有该文件时（首次安装）⇒ 删掉新建的那个，
// 免得留下一个「假装这个版本装过」的元数据。
func (i *Installer) restoreVersionFiles(snap versionSnapshot) error {
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	jsonPath := filepath.Join(i.dataDir, "version.json")
	if snap.hadJSON {
		record(os.WriteFile(jsonPath, snap.versionJSON, 0644))
	} else {
		record(os.Remove(jsonPath))
	}

	versionPath := filepath.Join(i.dataDir, "VERSION")
	if snap.hadFile {
		record(os.WriteFile(versionPath, snap.versionFile, 0644))
	} else {
		record(os.Remove(versionPath))
	}

	return firstErr
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

// envSnapshot 是主机配置文件的快照，用于失败时原样还原（升级配置合同：
// 改动前必备份，失败必还原——主机的原配置不可丢）。
type envSnapshot struct {
	content []byte
	hadFile bool
}

func (i *Installer) snapshotEnvFile() envSnapshot {
	snap := envSnapshot{}
	if raw, err := os.ReadFile(i.envFile); err == nil {
		snap.content = raw
		snap.hadFile = true
	}
	return snap
}

// restoreEnvFile 按快照还原配置文件；快照里本来没有该文件时（首次安装）
// 删掉新建的那个，免得留下「假装配置早就在」的痕迹。
func (i *Installer) restoreEnvFile(snap envSnapshot) error {
	if snap.hadFile {
		return os.WriteFile(i.envFile, snap.content, 0640)
	}
	return os.Remove(i.envFile)
}

// applyConfigUpdates 按「新增/强制更新」语义改写主机配置文件，返回
// （备份路径, 实际写入的键, 保留主机原值的键）。
//
// 规则：
//   - 键已存在（KEY= 前缀行）⇒ 原值保留；仅 Forced=true 覆盖该行值部分，
//     行内其余内容（行尾注释等）不动；
//   - 键不存在 ⇒ 追加 KEY=value；
//   - 改动任何一行之前先把原文件整体备份进 backupDir。
//
// 写入用临时文件+rename 原子替换；env 文件不存在时按空文件处理并新建。
func (i *Installer) applyConfigUpdates(updates []EnvConfigUpdate) (string, []string, []string, error) {
	if strings.TrimSpace(i.envFile) == "" {
		return "", nil, nil, fmt.Errorf("env file path is empty")
	}
	var applied, kept []string
	// 非强制且主机已有值 ⇒ 保留，不算改动；先探一遍，全部保留则不落备份不写盘。
	raw, _ := os.ReadFile(i.envFile)
	existing := map[string]int{} // key -> line index
	for idx, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		trimmed = strings.TrimPrefix(trimmed, "export ")
		if eq := strings.Index(trimmed, "="); eq > 0 {
			key := strings.TrimSpace(trimmed[:eq])
			existing[key] = idx
		}
	}
	for _, u := range updates {
		key := strings.TrimSpace(u.Key)
		if key == "" {
			return "", nil, nil, fmt.Errorf("config update has empty key")
		}
		if _, ok := existing[key]; ok && !u.Forced {
			kept = append(kept, key)
		}
	}
	if len(applied) == 0 && allKept(updates, existing) {
		return "", applied, kept, nil
	}

	// 备份原配置（升级配置合同：改动前必备份）。
	if err := os.MkdirAll(i.backupDir, 0o755); err != nil {
		return "", nil, nil, fmt.Errorf("create backup dir: %w", err)
	}
	timestamp := time.Now().Format("20060102_150405")
	envBase := sanitizeVersionForFilename(filepath.Base(i.envFile))
	backupPath := filepath.Join(i.backupDir, fmt.Sprintf("%s_%s", envBase, timestamp))
	if err := i.copyFile(i.envFile, backupPath); err != nil {
		if !os.IsNotExist(err) {
			return "", nil, nil, fmt.Errorf("backup env file: %w", err)
		}
		// 原文件不存在 ⇒ 无可备份，记录即可（首次安装形态）。
		backupPath = ""
	}

	// 逐条应用。
	lines := strings.Split(string(raw), "\n")
	for _, u := range updates {
		key := strings.TrimSpace(u.Key)
		idx, ok := existing[key]
		switch {
		case ok && u.Forced:
			lines[idx] = replaceEnvValue(lines[idx], u.Value)
			applied = append(applied, key)
		case ok:
			// 原值保留（上面已计入 kept）
		default:
			lines = append(lines, key+"="+u.Value)
			existing[key] = len(lines) - 1
			applied = append(applied, key)
		}
	}
	out := strings.Join(lines, "\n")
	// 原文件以换行结尾 ⇒ 输出也必须以换行结尾（追加的最后一行不能裸奔）。
	if strings.HasSuffix(string(raw), "\n") && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	tmp := i.envFile + ".update"
	if err := os.WriteFile(tmp, []byte(out), 0o640); err != nil {
		return backupPath, nil, nil, fmt.Errorf("write env file: %w", err)
	}
	if err := os.Rename(tmp, i.envFile); err != nil {
		return backupPath, nil, nil, fmt.Errorf("replace env file: %w", err)
	}
	return backupPath, applied, kept, nil
}

// allKept 报告是否每一条 update 都命中已有键且非强制（⇒ 整个配置步骤是 no-op）。
func allKept(updates []EnvConfigUpdate, existing map[string]int) bool {
	for _, u := range updates {
		key := strings.TrimSpace(u.Key)
		if _, ok := existing[key]; !ok || u.Forced {
			return false
		}
	}
	return true
}

// replaceEnvValue 只替换 KEY= 行中第一个 '=' 之后的值部分，保留键名、
// 行首空白 / export 前缀与行尾注释（原值后第一个 " #" 起视为注释段原样保留；
// 带引号值内含 " #" 的场景按无注释处理——env 文件值约定不含裸 #）。
func replaceEnvValue(line, newValue string) string {
	trimmed := strings.TrimLeft(line, " \t")
	lead := line[:len(line)-len(trimmed)]
	body := strings.TrimPrefix(trimmed, "export ")
	prefix := lead + trimmed[:len(trimmed)-len(body)]
	eq := strings.Index(body, "=")
	if eq < 0 {
		return line
	}
	trailing := ""
	if ci := strings.Index(body[eq+1:], " #"); ci >= 0 {
		trailing = body[eq+1+ci:]
	}
	return prefix + body[:eq+1] + newValue + trailing
}
