// Package admin — log_management.go
//
// 2026-07-02: 日志文件统一管理端点。
//
// 让运维在"数据生命周期 → 日志管理"页面中：
//   - 查看/修改日志轮转配置（max_size_mb / max_backups / max_age_days / compress）
//     修改后通过 logging.Reconfigure() 热加载，即时生效（路径除外）
//   - 查看日志目录占用（文件数、总大小、磁盘占比）
//   - 列出所有日志文件（当前 + 轮转备份 + 归档）
//   - 分层保留：归档（超 N 天打包移到 archive/）→ 删除（超 M 天的归档）
//
// 端点：
//   GET  /api/admin/logs/config          读取轮转配置 + 生效状态
//   PUT  /api/admin/logs/config          更新轮转配置（热加载）
//   GET  /api/admin/logs/files           列出日志文件
//   GET  /api/admin/logs/stats           日志目录占用统计
//   POST /api/admin/logs/archive         归档超期日志（preview/execute）
//   POST /api/admin/logs/cleanup         删除超期日志（preview/execute）
//   GET  /api/admin/logs/archive/list    列出已归档文件

package admin

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/logging"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// LogConfigResponse 日志轮转配置 + 生效状态
type LogConfigResponse struct {
	// 轮转参数
	MaxSizeMB   int  `json:"max_size_mb"`
	MaxBackups  int  `json:"max_backups"`
	MaxAgeDays  int  `json:"max_age_days"`
	Compress    bool `json:"compress"`
	ArchiveDays int  `json:"archive_days"` // 归档天数
	DeleteDays  int  `json:"delete_days"`  // 删除天数

	// 运行时状态
	LogFile       string `json:"log_file"`       // 当前日志文件路径
	LogDir        string `json:"log_dir"`        // 日志目录
	Enabled       bool   `json:"enabled"`        // 文件日志是否启用
	HotReloadable bool   `json:"hot_reloadable"` // 配置是否热加载生效
	ConfigSource  string `json:"config_source"`  // "db" | "env" | "default"
}

// LogStatsResponse 日志目录占用统计
type LogStatsResponse struct {
	LogDir         string     `json:"log_dir"`
	Exists         bool       `json:"exists"`
	TotalFiles     int        `json:"total_files"`
	TotalSizeBytes int64      `json:"total_size_bytes"`
	TotalSizeHuman string     `json:"total_size_human"`
	ArchiveFiles   int        `json:"archive_files"` // archive/ 子目录文件数
	ArchiveSize    int64      `json:"archive_size"`  // archive/ 大小
	OldestMtime    *time.Time `json:"oldest_mtime"`
	NewestMtime    *time.Time `json:"newest_mtime"`
	DiskUsagePct   float64    `json:"disk_usage_pct"` // 日志占所在磁盘的%
}

// LogFilesListResponse 日志文件列表
type LogFilesListResponse struct {
	Files []LogFileInfoExt `json:"files"`
	Total int              `json:"total"`
	Dir   string           `json:"dir"`
}

// LogFileInfoExt 扩展 logging.LogFileInfo，增加人类可读字段
type LogFileInfoExt struct {
	logging.LogFileInfo
	SizeHuman string `json:"size_human"`
}

// LogArchiveRequest 归档请求
type LogArchiveRequest struct {
	OlderThanDays int  `json:"older_than_days"` // 归档 N 天前的文件
	DryRun        bool `json:"dry_run"`         // 预览模式
}

// LogCleanupRequest 删除请求
type LogCleanupRequest struct {
	OlderThanDays int    `json:"older_than_days"` // 删除 N 天前的文件
	DryRun        bool   `json:"dry_run"`
	Scope         string `json:"scope"` // "all" | "archives" | "backups"
}

// LogOpResponse 归档/删除操作结果
type LogOpResponse struct {
	DryRun          bool     `json:"dry_run"`
	FilesAffected   int      `json:"files_affected"`
	BytesFreed      int64    `json:"bytes_freed"`
	BytesFreedHuman string   `json:"bytes_freed_human"`
	AffectedPaths   []string `json:"affected_paths,omitempty"`
	ArchiveFile     string   `json:"archive_file,omitempty"` // 归档产生的 tar.gz
	Error           string   `json:"error,omitempty"`
}

// ── 配置 ──────────────────────────────────────────────────────────

// handleLogConfig GET/PUT /api/admin/logs/config
func (h *Handler) handleLogConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.logConfigGet(w, r)
	case http.MethodPut:
		h.logConfigPut(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) logConfigGet(w http.ResponseWriter, r *http.Request) {
	cur := logging.ActiveConfig()
	resp := LogConfigResponse{
		MaxSizeMB:     cur.MaxSizeMB,
		MaxBackups:    cur.MaxBackups,
		MaxAgeDays:    cur.MaxAgeDays,
		Compress:      cur.Compress,
		LogFile:       cur.File,
		Enabled:       cur.File != "",
		HotReloadable: cur.File != "",
		ConfigSource:  "default",
		ArchiveDays:   7,
		DeleteDays:    30,
	}
	if cur.File != "" {
		resp.LogDir = filepath.Dir(cur.File)
	}

	// 读 settings 覆盖值
	if v, src := readIntSetting("log.max_size_mb"); src != "" && v > 0 {
		resp.MaxSizeMB = v
		resp.ConfigSource = src
	}
	if v, _ := readIntSetting("log.max_backups"); v >= 0 {
		resp.MaxBackups = v
	}
	if v, _ := readIntSetting("log.max_age_days"); v >= 0 {
		resp.MaxAgeDays = v
	}
	if b, _ := readBoolSetting("log.compress"); b {
		resp.Compress = true
	} else if _, src := readBoolSetting("log.compress"); src == "db" {
		resp.Compress = false
	}
	if v, _ := readIntSetting("log.archive_days"); v > 0 {
		resp.ArchiveDays = v
	}
	if v, _ := readIntSetting("log.delete_days"); v > 0 {
		resp.DeleteDays = v
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) logConfigPut(w http.ResponseWriter, r *http.Request) {
	store, ok := h.dbSettingsStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "settings store unavailable")
		return
	}

	var req struct {
		MaxSizeMB   *int  `json:"max_size_mb,omitempty"`
		MaxBackups  *int  `json:"max_backups,omitempty"`
		MaxAgeDays  *int  `json:"max_age_days,omitempty"`
		Compress    *bool `json:"compress,omitempty"`
		ArchiveDays *int  `json:"archive_days,omitempty"`
		DeleteDays  *int  `json:"archive_delete_days,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	user, role, _ := authIdentity(r)
	setInt := func(key string, v *int, min, max int) {
		if v == nil {
			return
		}
		if *v < min || *v > max {
			return
		}
		if _, err := store.Set(settings.ScopePlatform, key, *v); err == nil {
			auditSettingChange(user, role, key, fmt.Sprintf("%d", *v))
		}
	}
	setInt("log.max_size_mb", req.MaxSizeMB, 1, 1000)
	setInt("log.maxBackups", req.MaxBackups, 0, 100)
	setInt("log.max_age_days", req.MaxAgeDays, 0, 365)
	setInt("log.archive_days", req.ArchiveDays, 1, 365)
	setInt("log.delete_days", req.DeleteDays, 7, 3650)

	if req.Compress != nil {
		if _, err := store.Set(settings.ScopePlatform, "log.compress", *req.Compress); err == nil {
			auditSettingChange(user, role, "log.compress", fmt.Sprintf("%v", *req.Compress))
		}
	}

	// 热加载：把最新 settings 应用到 lumberjack
	newCfg := logging.ActiveConfig()
	if v, _ := readIntSetting("log.max_size_mb"); v > 0 {
		newCfg.MaxSizeMB = v
	}
	if v, _ := readIntSetting("log.max_backups"); v >= 0 {
		newCfg.MaxBackups = v
	}
	if v, _ := readIntSetting("log.max_age_days"); v >= 0 {
		newCfg.MaxAgeDays = v
	}
	if _, src := readBoolSetting("log.compress"); src == "db" {
		b, _ := readBoolSetting("log.compress")
		newCfg.Compress = b
	}
	if err := logging.Reconfigure(newCfg); err != nil {
		slog.Warn("logs: reconfigure failed", "error", err)
	}

	// 返回更新后配置
	h.logConfigGet(w, r)
}

// ── 文件列表与统计 ────────────────────────────────────────────────

// handleLogFiles GET /api/admin/logs/files
func (h *Handler) handleLogFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	files, err := logging.ListFiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list log files: "+err.Error())
		return
	}

	out := make([]LogFileInfoExt, 0, len(files))
	for _, f := range files {
		out = append(out, LogFileInfoExt{LogFileInfo: f, SizeHuman: humanBytes(f.SizeBytes)})
	}

	dir := ""
	if cur := logging.ActiveConfig(); cur.File != "" {
		dir = filepath.Dir(cur.File)
	}

	writeJSON(w, http.StatusOK, LogFilesListResponse{Files: out, Total: len(out), Dir: dir})
}

// handleLogStats GET /api/admin/logs/stats
func (h *Handler) handleLogStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cur := logging.ActiveConfig()
	resp := LogStatsResponse{}
	if cur.File == "" {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	dir := filepath.Dir(cur.File)
	resp.LogDir = dir
	resp.Exists = dirExists(dir)
	if !resp.Exists {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 主目录统计（排除 archive/）
	var oldest, newest *time.Time
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		// 跳过 archive 子目录
		if strings.Contains(p, string(filepath.Separator)+"archive"+string(filepath.Separator)) ||
			filepath.Base(filepath.Dir(p)) == "archive" {
			resp.ArchiveFiles++
			resp.ArchiveSize += fi.Size()
			return nil
		}
		resp.TotalFiles++
		resp.TotalSizeBytes += fi.Size()
		mt := fi.ModTime()
		if oldest == nil || mt.Before(*oldest) {
			oldest = &mt
		}
		if newest == nil || mt.After(*newest) {
			newest = &mt
		}
		return nil
	})
	resp.TotalSizeHuman = humanBytes(resp.TotalSizeBytes)
	resp.OldestMtime = oldest
	resp.NewestMtime = newest

	// 磁盘占比
	if pct, _, _, _, err := diskUsageAt(dir); err == nil {
		resp.DiskUsagePct = pct
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── 归档与删除 ────────────────────────────────────────────────────

// handleLogArchive POST /api/admin/logs/archive
// 分层保留 - 温层：把超 N 天的日志打包移到 archive/ 子目录
func (h *Handler) handleLogArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req LogArchiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.OlderThanDays <= 0 {
		req.OlderThanDays = 7
	}

	cur := logging.ActiveConfig()
	if cur.File == "" {
		writeError(w, http.StatusBadRequest, "file logging is not enabled")
		return
	}
	dir := filepath.Dir(cur.File)
	currentName := filepath.Base(cur.File)
	cutoff := time.Now().AddDate(0, 0, -req.OlderThanDays)

	// 收集待归档文件（排除当前文件和已有归档）
	type candidate struct {
		path string
		info os.FileInfo
	}
	var candidates []candidate
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == currentName {
			continue // 不归档当前活动日志
		}
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".log.gz") && !strings.HasSuffix(name, ".gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			candidates = append(candidates, candidate{path: filepath.Join(dir, name), info: info})
		}
	}

	resp := LogOpResponse{DryRun: req.DryRun}
	for _, c := range candidates {
		resp.FilesAffected++
		resp.BytesFreed += c.info.Size()
		if req.DryRun {
			resp.AffectedPaths = append(resp.AffectedPaths, c.path)
		}
	}
	resp.BytesFreedHuman = humanBytes(resp.BytesFreed)

	if !req.DryRun && len(candidates) > 0 {
		// 创建 archive 子目录
		archiveDir := filepath.Join(dir, "archive")
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			resp.Error = "create archive dir: " + err.Error()
			writeJSON(w, http.StatusOK, resp)
			return
		}
		// 打包为 tar.gz
		archiveName := fmt.Sprintf("logs-%s.tar.gz", time.Now().Format("20060102-150405"))
		archivePath := filepath.Join(archiveDir, archiveName)
		paths := make([]string, len(candidates))
		for i, c := range candidates {
			paths[i] = c.path
		}
		if err := createTarGzFromPaths(archivePath, paths); err != nil {
			resp.Error = "create archive: " + err.Error()
			writeJSON(w, http.StatusOK, resp)
			return
		}
		// 删除原文件
		for _, c := range candidates {
			_ = os.Remove(c.path)
		}
		resp.ArchiveFile = archivePath
		slog.Info("logs: archived",
			"files", len(candidates), "archive", archivePath,
			"bytes", resp.BytesFreed)
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleLogCleanup POST /api/admin/logs/cleanup
// 分层保留 - 冷层：删除超 N 天的文件
func (h *Handler) handleLogCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req LogCleanupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.OlderThanDays <= 0 {
		req.OlderThanDays = 30
	}
	if req.Scope == "" {
		req.Scope = "all"
	}

	cur := logging.ActiveConfig()
	if cur.File == "" {
		writeError(w, http.StatusBadRequest, "file logging is not enabled")
		return
	}
	dir := filepath.Dir(cur.File)
	currentName := filepath.Base(cur.File)
	cutoff := time.Now().AddDate(0, 0, -req.OlderThanDays)

	resp := LogOpResponse{DryRun: req.DryRun}

	// 收集待删除文件
	type candidate struct {
		path string
		info os.FileInfo
	}
	var candidates []candidate

	if req.Scope == "all" || req.Scope == "backups" {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if name == currentName {
				continue // 永不删除当前活动日志
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				candidates = append(candidates, candidate{path: filepath.Join(dir, name), info: info})
			}
		}
	}

	if req.Scope == "all" || req.Scope == "archives" {
		archiveDir := filepath.Join(dir, "archive")
		if entries, err := os.ReadDir(archiveDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				if info.ModTime().Before(cutoff) {
					candidates = append(candidates, candidate{path: filepath.Join(archiveDir, e.Name()), info: info})
				}
			}
		}
	}

	for _, c := range candidates {
		resp.FilesAffected++
		resp.BytesFreed += c.info.Size()
		if req.DryRun {
			resp.AffectedPaths = append(resp.AffectedPaths, c.path)
		}
	}
	resp.BytesFreedHuman = humanBytes(resp.BytesFreed)

	if !req.DryRun {
		for _, c := range candidates {
			_ = os.Remove(c.path)
		}
		slog.Info("logs: cleanup executed",
			"files", len(candidates), "bytes", resp.BytesFreed, "scope", req.Scope)
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleLogArchiveList GET /api/admin/logs/archive/list
func (h *Handler) handleLogArchiveList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cur := logging.ActiveConfig()
	if cur.File == "" {
		writeJSON(w, http.StatusOK, map[string]any{"archives": []any{}, "total": 0})
		return
	}
	archiveDir := filepath.Join(filepath.Dir(cur.File), "archive")
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"archives": []any{}, "total": 0, "dir": archiveDir, "exists": false})
		return
	}

	type archiveItem struct {
		Name      string    `json:"name"`
		SizeBytes int64     `json:"size_bytes"`
		SizeHuman string    `json:"size_human"`
		ModTime   time.Time `json:"mod_time"`
	}
	items := make([]archiveItem, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, archiveItem{
			Name:      e.Name(),
			SizeBytes: info.Size(),
			SizeHuman: humanBytes(info.Size()),
			ModTime:   info.ModTime(),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ModTime.After(items[j].ModTime)
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"archives": items,
		"total":    len(items),
		"dir":      archiveDir,
		"exists":   true,
	})
}

// ── helpers ───────────────────────────────────────────────────────

// dirExists 检查目录是否存在
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// createTarGzFromPaths 把多个文件路径打包成一个 tar.gz
func createTarGzFromPaths(dest string, paths []string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gw := gzip.NewWriter(f)
	defer func() { _ = gw.Close() }()
	tw := tar.NewWriter(gw)
	defer func() { _ = tw.Close() }()

	for _, path := range paths {
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			continue
		}
		hdr.Name = filepath.Base(path)
		if err := tw.WriteHeader(hdr); err != nil {
			continue
		}
		src, err := os.Open(path)
		if err != nil {
			continue
		}
		_, _ = io.Copy(tw, src)
		_ = src.Close()
	}
	return nil
}
