package autoupdate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// InstallMode 区分两种安装取材方式。
type InstallMode string

const (
	// InstallModeArtifact 下载远端制品二进制（默认）。
	InstallModeArtifact InstallMode = "artifact"
	// InstallModeSource 在本机从源码编译出二进制再安装。
	InstallModeSource InstallMode = "source"
)

// InstallRequest 是一次「点击安装升级」的请求。
type InstallRequest struct {
	Version string      `json:"version" validate:"required"`
	Mode    InstallMode `json:"mode,omitempty"`
	// Source 仅在 mode=source 时使用。
	Source *SourceBuildRequest `json:"source,omitempty"`
	// Restart 安装完成后是否重启服务（默认 true）。
	// 置 false 时只替换二进制，由运维手工重启。
	Restart *bool `json:"restart,omitempty"`
	// BinaryURL / SHA256 可由调用方显式指定（例如前端从检索结论里拿到）。
	BinaryURL string `json:"binary_url,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	// ConfigUpdates 是本版本的主机配置变更（新增/强制更新）。前端从检索结论
	// （RemoteRelease.ConfigUpdates，maintain manifest 下发）透传；为空时
	// 安装器跳过配置步骤。
	ConfigUpdates []EnvConfigUpdate `json:"config_updates,omitempty"`
}

// InstallJob 是一次安装任务的运行时状态。
type InstallJob struct {
	ID          string      `json:"id"`
	Version     string      `json:"version"`
	Mode        InstallMode `json:"mode"`
	Status      string      `json:"status"`
	OldVersion  string      `json:"old_version,omitempty"`
	BackupPath  string      `json:"backup_path,omitempty"`
	Error       string      `json:"error,omitempty"`
	Steps       []string    `json:"steps,omitempty"`
	StartedAt   time.Time   `json:"started_at"`
	CompletedAt *time.Time  `json:"completed_at,omitempty"`
	// RestartScheduled=true 表示安装已完成并已安排服务重启。
	RestartScheduled bool `json:"restart_scheduled,omitempty"`
}

// Executor 执行安装任务。
//
// 任务在后台 goroutine 中跑，HTTP 请求立即返回 job 快照 —— 安装要下载/编译，
// 把它做成同步请求会让网关看起来「卡死」，而且重启发生在响应之后。
type Executor struct {
	store      Store
	downloader *Downloader
	installer  *Installer
	builder    *SourceBuilder
	version    VersionProvider
	instanceID string
	// artifactURLTemplate 制品下载 URL 模板，{version} 会被替换。
	artifactURLTemplate string

	mu      sync.RWMutex
	current *InstallJob
	seq     int64
}

// ExecutorOption 用于覆盖默认行为。
type ExecutorOption func(*Executor)

// WithArtifactURLTemplate 设置制品下载 URL 模板。
func WithArtifactURLTemplate(tpl string) ExecutorOption {
	return func(e *Executor) { e.artifactURLTemplate = tpl }
}

// WithExecutorInstanceID 设置实例标识（升级日志的 instance_id）。
func WithExecutorInstanceID(id string) ExecutorOption {
	return func(e *Executor) { e.instanceID = id }
}

// WithSourceBuilder 注入源码构建器（mode=source 时必需）。
func WithSourceBuilder(b *SourceBuilder) ExecutorOption {
	return func(e *Executor) { e.builder = b }
}

// NewExecutor 创建安装执行器。
func NewExecutor(store Store, downloader *Downloader, installer *Installer, version VersionProvider, opts ...ExecutorOption) *Executor {
	e := &Executor{
		store:               store,
		downloader:          downloader,
		installer:           installer,
		version:             version,
		instanceID:          "local",
		artifactURLTemplate: "https://llm.kxpms.cn/releases/{version}/llm-gateway-go",
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

// Current 返回当前/最近一次安装任务快照。
func (e *Executor) Current() *InstallJob {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.current == nil {
		return nil
	}
	clone := *e.current
	clone.Steps = append([]string(nil), e.current.Steps...)
	return &clone
}

// Start 启动一次安装任务并立即返回任务快照。
func (e *Executor) Start(ctx context.Context, req InstallRequest) (*InstallJob, error) {
	if strings.TrimSpace(req.Version) == "" {
		return nil, fmt.Errorf("version is required")
	}
	mode := req.Mode
	if mode == "" {
		mode = InstallModeArtifact
	}
	if mode == InstallModeSource && e.builder == nil {
		return nil, fmt.Errorf("source build is not configured on this node")
	}

	e.mu.Lock()
	if e.current != nil && !isTerminalStatus(e.current.Status) {
		e.mu.Unlock()
		return nil, fmt.Errorf("another install task is already running (%s)", e.current.Status)
	}
	e.seq++
	job := &InstallJob{
		ID:        fmt.Sprintf("install-%d", e.seq),
		Version:   req.Version,
		Mode:      mode,
		Status:    StatusPending,
		StartedAt: time.Now(),
	}
	e.current = job
	e.mu.Unlock()

	// 后台执行：ctx 用 context.WithoutCancel，让请求返回后任务仍能跑完。
	// 否则一次点击的 HTTP 请求结束就会把安装一起取消掉。
	go e.run(context.WithoutCancel(ctx), req, job)
	return e.Current(), nil
}

// isTerminalStatus 判断任务状态是否已终结。
func isTerminalStatus(status string) bool {
	switch status {
	case StatusSuccess, StatusFailed, StatusRollback:
		return true
	default:
		return false
	}
}

// run 执行安装流程。
func (e *Executor) run(ctx context.Context, req InstallRequest, job *InstallJob) {
	logID, _ := e.createLog(ctx, req.Version)
	defer func() {
		if err := recover(); err != nil {
			e.finish(ctx, job, logID, StatusFailed, fmt.Sprintf("panic during install: %v", err))
		}
	}()

	oldVersion := "unknown"
	if e.version != nil {
		if v, _ := e.version(); v != "" {
			oldVersion = v
		}
	}
	e.update(job, func(j *InstallJob) { j.OldVersion = oldVersion })

	var binaryPath string
	var versionJSON []byte
	var err error

	switch req.Mode {
	case InstallModeSource:
		binaryPath, versionJSON, err = e.buildFromSource(ctx, req, job)
	default:
		binaryPath, err = e.downloadArtifact(ctx, req, job)
	}
	if err != nil {
		e.finish(ctx, job, logID, StatusFailed, err.Error())
		return
	}

	release, err := e.resolveRelease(ctx, req, binaryPath)
	if err != nil {
		e.finish(ctx, job, logID, StatusFailed, err.Error())
		return
	}

	if e.installer == nil {
		e.finish(ctx, job, logID, StatusFailed, "installer is not configured")
		return
	}

	e.update(job, func(j *InstallJob) { j.Status = StatusUpgrading })
	result, err := e.installer.InstallWithOptions(ctx, binaryPath, release, InstallOptions{
		VersionJSON:   versionJSON,
		ConfigUpdates: req.ConfigUpdates,
	})
	if result != nil {
		e.update(job, func(j *InstallJob) {
			j.BackupPath = result.BackupPath
			if result.OldVersion != "" {
				j.OldVersion = result.OldVersion
			}
		})
	}
	if err != nil {
		// ★ 失败分两种，对提醒的影响不同：
		//   - RolledBack（装了但起不来 / 迁移失败 ⇒ 已还原成旧二进制与旧版本元数据）
		//     ⇒ 节点**仍在旧版本上** ⇒ 新版**仍待处理** ⇒ 提醒行必须还在
		//   - Failed（下载失败 / 校验失败 ⇒ 压根没换二进制）
		//     ⇒ 同样仍待处理
		// 两种都是「可行动」状态（见 IsActionableStatus）⇒ 提醒不会消失。
		status := StatusFailed
		if result != nil && result.RolledBack {
			status = StatusRollback
		}
		e.finish(ctx, job, logID, status, err.Error())
		return
	}

	e.update(job, func(j *InstallJob) { j.Status = StatusSuccess })
	e.updateLog(ctx, logID, StatusSuccess, "")

	// 重启放在最后：先让 HTTP 响应回去，再让服务重启，否则调用方看到的是
	// 连接被掐断而不是「升级成功」。
	if req.Restart == nil || *req.Restart {
		e.scheduleRestart(ctx, job)
	}
}

// buildFromSource 从本地源码编译出二进制。
func (e *Executor) buildFromSource(ctx context.Context, req InstallRequest, job *InstallJob) (string, []byte, error) {
	e.step(job, "从本地源码编译")
	buildReq := SourceBuildRequest{}
	if req.Source != nil {
		buildReq = *req.Source
	}
	if buildReq.Version == "" {
		buildReq.Version = req.Version
	}
	result, err := e.builder.Build(ctx, buildReq)
	if err != nil {
		return "", nil, fmt.Errorf("source build failed: %w", err)
	}
	e.step(job, fmt.Sprintf("编译完成: %s（%d ms）", result.BinaryPath, result.DurationMs))
	return result.BinaryPath, result.VersionJSON, nil
}

// downloadArtifact 下载远端制品二进制。
func (e *Executor) downloadArtifact(ctx context.Context, req InstallRequest, job *InstallJob) (string, error) {
	if e.downloader == nil {
		return "", fmt.Errorf("downloader is not configured")
	}
	url := strings.TrimSpace(req.BinaryURL)
	if url == "" {
		url = e.artifactURL(req.Version)
	}
	checksum := strings.TrimSpace(req.SHA256)
	if checksum == "" {
		release, err := e.resolveRelease(ctx, req, "")
		if err == nil && release != nil {
			checksum = strings.TrimSpace(release.ImageDigest)
		}
	}
	if checksum == "" {
		return "", fmt.Errorf("no sha256 available for %s — refusing to install unverified artifact", req.Version)
	}

	e.step(job, fmt.Sprintf("下载制品: %s", url))
	e.update(job, func(j *InstallJob) { j.Status = StatusDownload })
	res, err := e.downloader.Download(ctx, url, checksum)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	e.step(job, fmt.Sprintf("下载完成并通过 sha256 校验: %s", res.FilePath))
	e.update(job, func(j *InstallJob) { j.Status = StatusReady })
	return res.FilePath, nil
}

// artifactURL 按模板生成制品下载地址。
func (e *Executor) artifactURL(version string) string {
	tpl := e.artifactURLTemplate
	if tpl == "" {
		tpl = "https://llm.kxpms.cn/releases/{version}/llm-gateway-go"
	}
	return strings.ReplaceAll(tpl, "{version}", version)
}

// resolveRelease 取本地 releases 行；本地没有时按请求信息合成一条。
func (e *Executor) resolveRelease(ctx context.Context, req InstallRequest, binaryPath string) (*Release, error) {
	if e.store != nil {
		release, err := e.store.GetRelease(ctx, req.Version)
		if err == nil && release != nil {
			return release, nil
		}
	}
	// 本地没有该版本（例如直接从远端检索到、尚未落库）⇒ 用请求信息合成，
	// 至少让安装流程能跑完并留下可追溯的版本号。
	return &Release{
		Version:   req.Version,
		Title:     req.Version,
		ImageTag:  req.Version,
		CreatedBy: "install-executor",
	}, nil
}

// createLog 定位「本次安装要推进的那一行升级日志」，返回其 ID。
//
// ★ 关键：**复用检索器生成的那条待处理提醒行**，而不是新建一行。
//
//	提醒行与安装任务日志行**是同一行**（同一件事的两个阶段：待处理 → 执行中 → 终态）。
//	若新建一行，就会出现「提醒行永远停在 pending（升级成功了它还显示待处理）」
//	与「提醒和任务两处对不齐」两个问题。
//
// 没有可复用的行（例如运维手工安装一个尚未被检索到的版本）⇒ 新建一行。
func (e *Executor) createLog(ctx context.Context, version string) (int64, error) {
	if e.store == nil {
		return 0, nil
	}
	if id, ok := e.findActionableLog(ctx, version); ok {
		return id, nil
	}
	old := ""
	if e.version != nil {
		old, _ = e.version()
	}
	return e.store.CreateUpgradeLog(ctx, e.instanceID, old, version)
}

// findActionableLog 找出该版本上仍「待处理」的日志行（提醒行）。
//
// ★ 2026-11-17（12h 审计）：原实现只取**最近 200 行**
//
//	（GetUpgradeHistory 的 limit=200，SQL 是 ORDER BY started_at DESC LIMIT/OFFSET）
//	⇒ 当那条待处理提醒行比第 200 行更老时**找不到它** ⇒ createLog 另起新行
//	⇒ **提醒行永远停在 pending**（F13 原样复发，只是要攒够 200 行才触发）。
//	⇒ 现在按 total 分页扫到底；找不到就如实返回 false。
func (e *Executor) findActionableLog(ctx context.Context, version string) (int64, bool) {
	const page = 200
	for offset := 0; ; offset += page {
		history, total, err := e.store.GetUpgradeHistory(ctx, e.instanceID, offset, page)
		if err != nil {
			return 0, false
		}
		for _, h := range history {
			if h.Version != version || h.LogID <= 0 {
				continue
			}
			if IsActionableStatus(h.Status) {
				return h.LogID, true
			}
		}
		if len(history) == 0 || offset+len(history) >= total {
			return 0, false
		}
	}
}

// updateLog 更新升级日志行状态。
func (e *Executor) updateLog(ctx context.Context, id int64, status, errMsg string) {
	if e.store == nil || id == 0 {
		return
	}
	completed := time.Time{}
	if isTerminalStatus(status) {
		completed = time.Now()
	}
	if err := e.store.UpdateUpgradeLog(ctx, id, status, errMsg, completed); err != nil {
		slog.Warn("autoupdate: update upgrade log failed", "id", id, "error", err)
	}
}

// finish 收尾：更新任务状态与日志。
func (e *Executor) finish(ctx context.Context, job *InstallJob, logID int64, status, errMsg string) {
	e.update(job, func(j *InstallJob) {
		j.Status = status
		j.Error = errMsg
		now := time.Now()
		j.CompletedAt = &now
	})
	e.updateLog(ctx, logID, status, errMsg)
	slog.Info("autoupdate: install finished", "job", job.ID, "status", status, "error", errMsg)
}

// scheduleRestart 在短暂延迟后重启服务。
func (e *Executor) scheduleRestart(ctx context.Context, job *InstallJob) {
	if e.installer == nil || !e.installer.RestartEnabled() {
		slog.Info("autoupdate: service restart skipped (no service configured)")
		return
	}
	e.update(job, func(j *InstallJob) { j.RestartScheduled = true })
	go func() {
		// 留出时间让 HTTP 响应回到调用方。
		time.Sleep(2 * time.Second)
		if err := e.installer.RestartService(ctx); err != nil {
			slog.Error("autoupdate: service restart failed", "error", err)
			return
		}
		slog.Info("autoupdate: service restarted")
	}()
}

// step 追加一条步骤日志。
func (e *Executor) step(job *InstallJob, msg string) {
	e.update(job, func(j *InstallJob) {
		j.Steps = append(j.Steps, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg))
	})
}

// update 对任务快照做一次加锁修改。
func (e *Executor) update(job *InstallJob, fn func(*InstallJob)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current == nil {
		return
	}
	fn(e.current)
}

// fileExists 便于测试与日志判断。
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// binaryName 从二进制路径取文件名（备份命名用）。
func binaryName(path string) string {
	return filepath.Base(path)
}
