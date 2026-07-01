// Package admin — storage_migration.go
//
// 2026-07-02: 附件存储目录迁移。
//
// 当运维在「存储配置」页修改附件目录后，PUT /api/admin/storage/config
// 会触发 startStorageMigration。流程采用「先复制 → 校验 → 切换 → 删旧」
// 的安全顺序：
//
//  1. 收集旧目录所有文件，逐个复制到新目录（保留相对路径结构）
//  2. 校验新目录文件数/字节数 ≥ 预期
//  3. 调用 storage.SetBaseDir 原子切换运行时 BaseDir（写锁互斥）
//  4. 删除旧目录
//
// 复制期间并发的下载/写入仍走旧 BaseDir（SetBaseDir 尚未调用）；
// 切换后的新写入自动落在新目录。迁移在独立 goroutine 中异步执行，
// 前端通过 GET /api/admin/storage/migration-state 轮询进度。
//
// 设计参考 provider_refresh.go 的 providerRefreshState（lazy 初始化 +
// mutex 守护的 map + 值拷贝读取 + detached context）。

package admin

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// migrationStatus 迁移状态枚举（与 providerRefreshStatus 同构）。
type migrationStatus string

const (
	migrationIdle      migrationStatus = "idle"
	migrationRunning   migrationStatus = "running"
	migrationSucceeded migrationStatus = "succeeded"
	migrationFailed    migrationStatus = "failed"
)

// migrationRun 描述一次目录迁移的完整快照（JSON 序列化给前端轮询）。
type migrationRun struct {
	RunID        string          `json:"run_id"`
	Status       migrationStatus `json:"status"`
	FromDir      string          `json:"from_dir"`
	ToDir        string          `json:"to_dir"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
	HeartbeatAt  *time.Time      `json:"heartbeat_at,omitempty"`
	FilesTotal   int             `json:"files_total"`
	FilesCopied  int             `json:"files_copied"`
	BytesTotal   int64           `json:"bytes_total"`
	BytesCopied  int64           `json:"bytes_copied"`
	FilesDeleted int             `json:"files_deleted"`
	OldDirPurged bool            `json:"old_dir_purged"`
	Errors       []string        `json:"errors,omitempty"`
	Message      string          `json:"message,omitempty"`
}

// migrationState 持有迁移运行记录（单例：同一时刻只允许一个迁移）。
// 通过 Handler.migrationMu 实现 lazy 初始化，与 providerRefreshState 同构。
type migrationState struct {
	mu      sync.Mutex
	running *migrationRun // 进行中的迁移（nil 表示无）
	latest  *migrationRun // 最近一次完成的迁移（succeeded/failed）
}

// getMigrationState lazy 初始化迁移状态跟踪器。
func (h *Handler) getMigrationState() *migrationState {
	h.migrationMu.Lock()
	defer h.migrationMu.Unlock()
	if h.migrationState == nil {
		h.migrationState = &migrationState{}
	}
	return h.migrationState
}

// recordMigration 写入当前快照（running 或 latest）。
func (h *Handler) recordMigration(run *migrationRun) {
	st := h.getMigrationState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if run.Status == migrationRunning {
		st.running = run
	} else {
		st.running = nil
		latest := *run // 值拷贝
		st.latest = &latest
	}
}

// getMigration 返回当前快照的值拷贝（调用方可安全序列化）。
func (h *Handler) getMigration() *migrationRun {
	st := h.getMigrationState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.running != nil {
		copy := *st.running
		return &copy
	}
	if st.latest != nil {
		copy := *st.latest
		return &copy
	}
	return nil
}

// startStorageMigration 启动一次目录迁移。若已有迁移在进行则返回 (nil, false)。
// fromDir/toDir 必须是绝对路径。返回创建的 run 快照 + 是否成功启动。
func (h *Handler) startStorageMigration(fromDir, toDir string) (*migrationRun, bool) {
	if h.attachmentStorage == nil {
		return nil, false
	}

	st := h.getMigrationState()
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.running != nil {
		return nil, false // 已有迁移在跑，拒绝
	}

	now := time.Now()
	hb := now
	run := &migrationRun{
		RunID:       fmt.Sprintf("migration-%d", now.UnixNano()),
		Status:      migrationRunning,
		FromDir:     fromDir,
		ToDir:       toDir,
		StartedAt:   now,
		HeartbeatAt: &hb,
		Message:     "正在收集待迁移文件…",
	}
	// 在锁内直接设置 running，避免并发时间窗口
	st.running = run

	// detached context：客户端断开不中止迁移（与 providerRefresh 一致）
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	go func() {
		defer cancel()
		h.runMigration(ctx, run)
	}()

	copy := *run
	return &copy, true
}

// runMigration 迁移主循环（在 goroutine 中执行）。
func (h *Handler) runMigration(ctx context.Context, run *migrationRun) {
	defer func() {
		// 确保最终状态一定被记录（即使 panic）
		if r := recover(); r != nil {
			run.Status = migrationFailed
			run.Errors = append(run.Errors, fmt.Sprintf("panic: %v", r))
			fin := time.Now()
			run.FinishedAt = &fin
			h.recordMigration(run)
		}
	}()

	storage := h.attachmentStorage
	if storage == nil {
		h.finishMigration(run, migrationFailed, "storage 未注入")
		return
	}

	// 阶段 1：收集旧目录文件清单
	run.Message = "正在收集待迁移文件…"
	h.recordMigration(run)

	files, totalBytes, err := collectMigrationFiles(run.FromDir)
	if err != nil {
		h.finishMigration(run, migrationFailed, "收集文件失败: "+err.Error())
		return
	}
	if len(files) == 0 {
		// 空目录：直接切换，删除前再次校验是否仍为空（防迁移期间有新写入）
		run.FilesTotal = 0
		run.BytesTotal = 0
		run.Message = "旧目录为空，直接切换"
		h.recordMigration(run)
		if err := storage.SetBaseDir(run.ToDir); err != nil {
			h.finishMigration(run, migrationFailed, "切换 BaseDir 失败: "+err.Error())
			return
		}
		// 删除前再次检查是否为空
		if entries, err := os.ReadDir(run.FromDir); err == nil && len(entries) > 0 {
			slog.Warn("storage migration: fromDir 非空（迁移期间有新写入），跳过删除",
				"dir", run.FromDir, "files", len(entries))
		} else {
			_ = os.RemoveAll(run.FromDir)
			run.OldDirPurged = true
		}
		h.finishMigration(run, migrationSucceeded, "迁移完成（空目录）")
		return
	}
	run.FilesTotal = len(files)
	run.BytesTotal = totalBytes
	h.recordMigration(run)

	// 阶段 2：逐个复制（保留相对路径）
	run.Message = fmt.Sprintf("正在复制 %d 个文件 (%s)…", len(files), humanBytes(totalBytes))
	h.recordMigration(run)

	for i, absPath := range files {
		if ctx.Err() != nil {
			h.finishMigration(run, migrationFailed, "迁移超时或取消")
			return
		}

		rel, err := filepath.Rel(run.FromDir, absPath)
		if err != nil {
			run.Errors = append(run.Errors, fmt.Sprintf("rel path %s: %v", absPath, err))
			h.recordMigration(run)
			continue
		}
		dst := filepath.Join(run.ToDir, rel)
		if err := copyFile(absPath, dst); err != nil {
			errMsg := fmt.Sprintf("copy %s: %v", rel, err)
			run.Errors = append(run.Errors, errMsg)
			// 检测磁盘空间不足错误，提前终止避免继续占用空间
			if strings.Contains(err.Error(), "no space left") || strings.Contains(err.Error(), "disk full") {
				h.finishMigration(run, migrationFailed, "目标磁盘空间不足")
				return
			}
			h.recordMigration(run)
			continue
		}
		// 按字节数累计（文件计数用已处理的 errors 容差）
		if fi, serr := os.Stat(dst); serr == nil {
			run.BytesCopied += fi.Size()
		}
		run.FilesCopied = i + 1
		now := time.Now()
		run.HeartbeatAt = &now
		h.recordMigration(run)
	}

	// 复制失败率检查：超过 20% 或绝对失败数超过 100 个时拒绝迁移，避免数据丢失
	failureRate := float64(len(run.Errors)) / float64(len(files))
	if failureRate > 0.2 || len(run.Errors) > 100 {
		h.finishMigration(run, migrationFailed,
			fmt.Sprintf("复制失败率过高 (%.1f%%, %d/%d)", failureRate*100, len(run.Errors), len(files)))
		return
	}

	// 阶段 3：校验新目录
	run.Message = "正在校验迁移结果…"
	h.recordMigration(run)
	if err := verifyMigration(run.ToDir, run.FilesTotal-len(run.Errors), run.BytesCopied); err != nil {
		h.finishMigration(run, migrationFailed, "校验失败: "+err.Error())
		return
	}

	// 阶段 4：原子切换 BaseDir（写锁，与并发的 Save/Load 互斥）
	run.Message = "正在切换到新目录…"
	h.recordMigration(run)
	if err := storage.SetBaseDir(run.ToDir); err != nil {
		h.finishMigration(run, migrationFailed, "切换 BaseDir 失败: "+err.Error())
		return
	}

	// 阶段 5：删除旧目录
	run.Message = "正在删除旧目录…"
	h.recordMigration(run)
	purged, derr := purgeOldDir(run.FromDir, run.FilesTotal)
	if derr != nil {
		run.Errors = append(run.Errors, "删除旧目录失败: "+derr.Error())
	} else if purged {
		run.OldDirPurged = true
		run.FilesDeleted = run.FilesTotal
	}

	msg := fmt.Sprintf("迁移完成：%d 文件 (%s)", run.FilesCopied, humanBytes(run.BytesCopied))
	if len(run.Errors) > 0 {
		msg += fmt.Sprintf("，%d 个警告", len(run.Errors))
	}
	h.finishMigration(run, migrationSucceeded, msg)
}

// finishMigration 设置终态并记录。
func (h *Handler) finishMigration(run *migrationRun, status migrationStatus, msg string) {
	run.Status = status
	run.Message = msg
	fin := time.Now()
	run.FinishedAt = &fin
	h.recordMigration(run)
	slog.Info("storage migration finished",
		"run_id", run.RunID, "status", status,
		"from", run.FromDir, "to", run.ToDir,
		"files_copied", run.FilesCopied, "files_total", run.FilesTotal,
		"errors", len(run.Errors))
}

// ─── 文件操作辅助 ─────────────────────────────────────────────────

// fileEntry 收集到的待迁移文件（绝对路径 + 大小）。
type fileEntry struct {
	abs  string
	size int64
}

// collectMigrationFiles 遍历 dir，返回所有普通文件的绝对路径（升序）与总字节数。
func collectMigrationFiles(dir string) ([]string, int64, error) {
	var entries []fileEntry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 忽略无法访问的子目录
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		entries = append(entries, fileEntry{abs: path, size: info.Size()})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, 0, err
	}
	// 排序保证迁移顺序确定（按路径升序）
	var total int64
	files := make([]string, len(entries))
	for i, e := range entries {
		files[i] = e.abs
		total += e.size
	}
	return files, total, nil
}

// copyFile 复制单个文件，自动创建目标父目录。保留 0644 权限。
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// verifyMigration 校验 toDir 的文件数与总字节数 ≥ 期望值。
// 用 ≥ 而非 == 是因为新目录可能预先存在少量文件（如测试 .gw_test_writable）。
func verifyMigration(toDir string, expectFiles int, expectBytes int64) error {
	gotFiles, gotBytes, err := collectMigrationFiles(toDir)
	if err != nil {
		return err
	}
	if len(gotFiles) < expectFiles {
		return fmt.Errorf("文件数不足: got %d, want %d", len(gotFiles), expectFiles)
	}
	if gotBytes < expectBytes {
		return fmt.Errorf("字节数不足: got %d, want %d", gotBytes, expectBytes)
	}
	return nil
}

// purgeOldDir 删除旧目录及其全部内容。返回是否成功删除。
// 删除前再校验一次文件数，防止误删非预期目录。
func purgeOldDir(oldDir string, expectFiles int) (bool, error) {
	got, _, err := collectMigrationFiles(oldDir)
	if err != nil {
		return false, err
	}
	if len(got) < expectFiles {
		return false, fmt.Errorf("旧目录文件数不足: got %d, want %d（可能已被手动删除，拒绝删除）", len(got), expectFiles)
	}
	if len(got) > expectFiles {
		// 迁移期间有新写入（理论上不应发生，因切换前仍走旧目录），警告但继续删除
		slog.Warn("purgeOldDir: 旧目录文件数超预期", "got", len(got), "expected", expectFiles, "dir", oldDir)
	}
	return true, os.RemoveAll(oldDir)
}

// dirUsable 定义在 storage_config.go（与存储配置 PUT 共用）。

// getStorageMigrationState GET /api/admin/storage/migration-state
// 返回 {running, latest}，与 providerRefresh 状态端点形状一致。
func (h *Handler) getStorageMigrationState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	run := h.getMigration()
	resp := map[string]any{
		"running": nil,
		"latest":  nil,
	}
	if run != nil {
		if run.Status == migrationRunning {
			resp["running"] = run
		} else {
			resp["latest"] = run
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleMigrationState 是 mux 的分发入口（go http.ServeMux 约定的 Handler
// 签名），转给 getStorageMigrationState 保持内部方法命名一致。
func (h *Handler) handleMigrationState(w http.ResponseWriter, r *http.Request) {
	h.getStorageMigrationState(w, r)
}
