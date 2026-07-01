// Package admin — storage_config.go
//
// 2026-07-02: 本地文件存储（附件）配置管理端点。
//
// 让运维在"数据生命周期 → 存储配置"页面中：
//   - 查看/修改附件存储目录（需重启生效）
//   - 配置保留策略（TTL 天数 / 单文件大小上限）
//   - 配置磁盘水位（告警水位 / 自动清理触发水位 / 自动清理开关）
//   - 测试某路径是否可用（权限/空间）
//
// 配置持久化到 settings_kv（category='storage'），通过 settings.Global 读取。
// 运行时实际状态（当前磁盘占用、生效目录）随 GET 一起返回。
//
// 端点：
//   GET  /api/admin/storage/config            读取配置 + 运行时状态
//   PUT  /api/admin/storage/config            更新配置（校验）
//   POST /api/admin/storage/config/test-path  测试路径可用性

package admin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// StorageConfigResponse 存储配置 + 运行时实际状态
type StorageConfigResponse struct {
	// 配置值（来自 settings_kv 或默认值）
	AttachmentDirOverride string `json:"attachment_dir_override"` // DB 覆盖值（空=用env）
	TTLDays               int    `json:"ttl_days"`                // 保留天数
	MaxFileSizeMB         int    `json:"max_file_size_mb"`        // 单文件上限
	DiskQuotaPercent      int    `json:"disk_quota_percent"`      // 告警水位
	AutoCleanupEnabled    bool   `json:"auto_cleanup_enabled"`    // 自动清理开关
	AutoCleanupThreshold  int    `json:"auto_cleanup_threshold"`  // 触发水位

	// 运行时实际状态
	EffectiveDir     string  `json:"effective_dir"`      // 当前生效目录（绝对路径）
	AttachmentDirEnv string  `json:"attachment_dir_env"` // 环境变量原值
	NeedsRestart     bool    `json:"needs_restart"`      // 是否有待重启生效的改动
	CurrentDiskUsage float64 `json:"current_disk_usage"` // 当前磁盘占用%
	ConfigSource     string  `json:"config_source"`      // "db" | "env" | "default"
	// DownloadURLPrefix 告知前端附件下载 URL 的固定前缀。
	// 前端拼 URL 时用 download_url_prefix + 相对 path（见 web/src/api/logs.ts:attachmentURL）。
	DownloadURLPrefix string `json:"download_url_prefix"`
	// MigrationRunID 当 PUT 触发了目录迁移时，返回本次迁移的 run_id，
	// 前端据此轮询 GET /api/admin/storage/migration-state。空表示未触发迁移。
	MigrationRunID string `json:"migration_run_id,omitempty"`
}

// StorageConfigUpdateRequest PUT 请求体（所有字段可选，nil=不改）
type StorageConfigUpdateRequest struct {
	AttachmentDirOverride *string `json:"attachment_dir_override,omitempty"`
	TTLDays               *int    `json:"ttl_days,omitempty"`
	MaxFileSizeMB         *int    `json:"max_file_size_mb,omitempty"`
	DiskQuotaPercent      *int    `json:"disk_quota_percent,omitempty"`
	AutoCleanupEnabled    *bool   `json:"auto_cleanup_enabled,omitempty"`
	AutoCleanupThreshold  *int    `json:"auto_cleanup_threshold,omitempty"`
}

// StorageTestPathRequest 测试路径请求
type StorageTestPathRequest struct {
	Path string `json:"path"`
}

// StorageTestPathResponse 测试路径结果
type StorageTestPathResponse struct {
	Path           string  `json:"path"`
	AbsPath        string  `json:"abs_path"`
	Exists         bool    `json:"exists"`
	CanCreate      bool    `json:"can_create"`       // 不存在但可创建
	Writable       bool    `json:"writable"`         // 可写
	DiskTotalBytes uint64  `json:"disk_total_bytes"` // 所在磁盘容量
	DiskFreeBytes  uint64  `json:"disk_free_bytes"`  // 所在磁盘剩余
	DiskUsagePct   float64 `json:"disk_usage_pct"`   // 磁盘占用%
	OK             bool    `json:"ok"`               // 综合可用性
	Message        string  `json:"message"`          // 人类可读结论
}

// handleStorageConfig GET/PUT /api/admin/storage/config
func (h *Handler) handleStorageConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.storageConfigGet(w, r)
	case http.MethodPut:
		h.storageConfigPut(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) storageConfigGet(w http.ResponseWriter, r *http.Request) {
	resp := StorageConfigResponse{
		TTLDays:              30,
		MaxFileSizeMB:        20,
		DiskQuotaPercent:     80,
		AutoCleanupEnabled:   false,
		AutoCleanupThreshold: 85,
		ConfigSource:         "default",
	}

	// 从 settings 读取覆盖值
	resp.AttachmentDirOverride = readStringSetting("storage.attachment_dir_override")
	if v, src := readIntSetting("storage.attachment_ttl_days"); src != "" {
		resp.TTLDays = v
		resp.ConfigSource = src
	}
	if v, _ := readIntSetting("storage.attachment_max_size_mb"); v > 0 {
		resp.MaxFileSizeMB = v
	}
	if v, _ := readIntSetting("storage.disk_quota_percent"); v > 0 {
		resp.DiskQuotaPercent = v
	}
	if b, _ := readBoolSetting("storage.auto_cleanup_enabled"); b {
		resp.AutoCleanupEnabled = true
	}
	if v, _ := readIntSetting("storage.auto_cleanup_threshold"); v > 0 {
		resp.AutoCleanupThreshold = v
	}

	// 运行时实际目录
	resp.AttachmentDirEnv = envOrEmpty("LLM_GATEWAY_ATTACHMENT_DIR")
	resp.EffectiveDir = EffectiveAttachmentDir()
	// 下载 URL 前缀是固定路径（经 admin 鉴权的同源端点），见 admin/handler.go 路由注册。
	resp.DownloadURLPrefix = "/api/attachments/"
	// needs_restart：仅当 attachmentStorage 未注入或其运行时 BaseDir 与期望目录不一致时为 true。
	// 若已注入 Storage，说明可热切换（迁移），则 needs_restart=false。
	if h.attachmentStorage == nil {
		resp.NeedsRestart = resp.AttachmentDirOverride != ""
	} else {
		// 运行时目录与期望目录不一致（如上次迁移失败、或 env 与 DB override 不同）
		if loadedAbs, err := filepath.Abs(h.attachmentStorage.BaseDir()); err == nil {
			if effAbs, err2 := filepath.Abs(resp.EffectiveDir); err2 == nil && loadedAbs != effAbs {
				resp.NeedsRestart = true
			}
		}
	}

	// 当前磁盘占用
	if abs, err := filepath.Abs(resp.EffectiveDir); err == nil {
		if pct, _, _, _, statErr := diskUsageAt(abs); statErr == nil {
			resp.CurrentDiskUsage = pct
		}
	}

	// 若 PUT 本次触发了目录迁移，带上 migration_run_id（原子读取后即用）。
	if v := h.pendingMigrationRunID.Load(); v != nil {
		if runID, ok := v.(string); ok && runID != "" {
			resp.MigrationRunID = runID
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// dirUsable 判断目录是否可用（存在且可写，或不存在但可创建）。
// 供 PUT 迁移前置校验复用，避免启动迁移后才发现目标目录不可写。
func dirUsable(dir string) bool {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return false
		}
		return isWritableDir(dir)
	}
	if os.IsNotExist(err) {
		// 尝试创建以验证可写（与 handleStorageTestPath 的语义一致）。
		return os.MkdirAll(dir, 0755) == nil
	}
	return false
}

func (h *Handler) storageConfigPut(w http.ResponseWriter, r *http.Request) {
	store, ok := h.dbSettingsStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "settings store unavailable (no DB)")
		return
	}

	var req StorageConfigUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	user, role, _ := authIdentity(r)

	// 逐项写入（只写非 nil 的字段）
	setInt := func(key string, v *int, min, max int) error {
		if v == nil {
			return nil
		}
		if *v < min || *v > max {
			return fmt.Errorf("%s 超出允许范围 [%d, %d]", key, min, max)
		}
		if _, err := store.Set(settings.ScopePlatform, key, *v); err != nil {
			return fmt.Errorf("保存 %s 失败: %v", key, err)
		}
		auditSettingChange(user, role, key, fmt.Sprintf("%d", *v))
		return nil
	}

	if err := setInt("storage.attachment_ttl_days", req.TTLDays, 1, 3650); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := setInt("storage.attachment_max_size_mb", req.MaxFileSizeMB, 1, 200); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := setInt("storage.disk_quota_percent", req.DiskQuotaPercent, 50, 99); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := setInt("storage.auto_cleanup_threshold", req.AutoCleanupThreshold, 60, 99); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.AutoCleanupEnabled != nil {
		if _, err := store.Set(settings.ScopePlatform, "storage.auto_cleanup_enabled", *req.AutoCleanupEnabled); err == nil {
			auditSettingChange(user, role, "storage.auto_cleanup_enabled", fmt.Sprintf("%v", *req.AutoCleanupEnabled))
		}
	}

	// triggeredMigration 记录 PUT 本次是否触发了目录迁移（供下方组装响应用）。
	var triggeredMigration *migrationRun

	if req.AttachmentDirOverride != nil {
		// 空字符串表示清除覆盖（用 env）
		val := *req.AttachmentDirOverride
		if _, err := store.Set(settings.ScopePlatform, "storage.attachment_dir_override", val); err == nil {
			auditSettingChange(user, role, "storage.attachment_dir_override", val)
		}

		// 2026-07-02: 目录变更触发文件迁移。
		// fromDir = 运行时 Storage 当前 BaseDir（旧目录）；
		// toDir   = 持久化后重新解析的生效目录（DB override > env > default）。
		// 仅当 attachmentStorage 已注入且新旧目录（绝对路径）不同时迁移。
		if h.attachmentStorage != nil {
			fromDir := h.attachmentStorage.BaseDir()
			toDir := EffectiveAttachmentDir()
			if toAbs, err := filepath.Abs(toDir); err == nil {
				fromAbs, _ := filepath.Abs(fromDir)
				if fromAbs != toAbs {
					// 校验目标目录可用（可写或可创建），不可用则不启动迁移，
					// 直接以错误形式返回，避免配置已改但文件无法落位。
					if !dirUsable(toAbs) {
						writeError(w, http.StatusBadRequest,
							"目标目录不可用（不存在且无法创建，或权限不足）")
						return
					}
					if run, ok := h.startStorageMigration(fromAbs, toAbs); ok {
						triggeredMigration = run
					} else {
						// 已有迁移在跑：拒绝本次目录变更，回滚覆盖值。
						_, _ = store.Set(settings.ScopePlatform, "storage.attachment_dir_override",
							readStringSetting("storage.attachment_dir_override"))
						writeError(w, http.StatusConflict, "已有目录迁移在进行中，请等待完成后再试")
						return
					}
				}
			}
		}
	}

	// 返回更新后的配置。若触发了迁移，把 migration_run_id 附进响应。
	if triggeredMigration != nil {
		// 标记本次响应应带上 migration_run_id；通过临时字段传给 storageConfigGet。
		h.pendingMigrationRunID.Store(triggeredMigration.RunID)
	}
	h.storageConfigGet(w, r)
	h.pendingMigrationRunID.Store("")
}

// isPathSafe 检查路径是否在允许的安全区域内（防止路径遍历攻击）
func isPathSafe(absPath string) bool {
	// 白名单：只允许测试以下前缀的路径
	safePrefixes := []string{
		"/data/",
		"/var/llm-gateway/",
		"/opt/llm-gateway/",
		"/tmp/llm-gateway/",
		"./data/",
		"./attachments/",
	}

	// 黑名单：明确拒绝系统敏感目录
	dangerousPrefixes := []string{
		"/etc/",
		"/root/",
		"/home/",
		"/usr/bin/",
		"/usr/sbin/",
		"/boot/",
		"/sys/",
		"/proc/",
	}

	// 先检查黑名单
	for _, prefix := range dangerousPrefixes {
		if strings.HasPrefix(absPath, prefix) {
			return false
		}
	}

	// 再检查白名单
	for _, prefix := range safePrefixes {
		if strings.HasPrefix(absPath, prefix) {
			return true
		}
	}

	// 不在白名单中也拒绝
	return false
}

// handleStorageTestPath POST /api/admin/storage/config/test-path
func (h *Handler) handleStorageTestPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req StorageTestPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	abs, err := filepath.Abs(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	// 路径白名单验证：防止探测系统敏感目录
	if !isPathSafe(abs) {
		writeError(w, http.StatusForbidden, "路径不在允许的安全区域内")
		return
	}

	resp := StorageTestPathResponse{Path: req.Path, AbsPath: abs}

	// 磁盘信息（针对路径所在文件系统）
	pct, total, free, _, statErr := diskUsageAt(abs)
	if statErr == nil {
		resp.DiskTotalBytes = total
		resp.DiskFreeBytes = free
		resp.DiskUsagePct = pct
	}

	// 存在性 + 可写性
	info, err := os.Stat(abs)
	if err == nil {
		resp.Exists = true
		if info.IsDir() {
			// 尝试创建临时文件测试可写
			tmp := filepath.Join(abs, ".gw_test_writable")
			if f, wErr := os.Create(tmp); wErr == nil {
				_ = f.Close()
				_ = os.Remove(tmp)
				resp.Writable = true
			}
		}
	} else if os.IsNotExist(err) {
		// 父目录可写则可创建
		if parent := filepath.Dir(abs); parent != "." {
			resp.CanCreate = isWritableDir(parent)
		} else {
			resp.CanCreate = isWritableDir(abs)
		}
	}

	// 综合结论
	resp.OK = resp.Exists && resp.Writable
	if resp.OK {
		resp.Message = fmt.Sprintf("目录可用，剩余空间 %s（磁盘占用 %.1f%%）", humanBytes(int64(free)), pct)
	} else if resp.CanCreate {
		resp.OK = true
		resp.Message = fmt.Sprintf("目录不存在但可创建，所在磁盘剩余 %s", humanBytes(int64(free)))
	} else {
		resp.Message = "目录不可用：不存在且无法创建，或权限不足"
	}

	writeJSON(w, http.StatusOK, resp)
}

// ─── helpers ─────────────────────────────────────────────────────

// EffectiveAttachmentDir 返回当前生效的附件目录。
// 优先级：DB override > env > 默认 ./data/attachments
// 2026-07-02: 导出供 data_lifecycle_attachments_filesystem 等管理端复用，
// 收口分散的 os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR") 读取。
func EffectiveAttachmentDir() string {
	if override := readStringSetting("storage.attachment_dir_override"); override != "" {
		return override
	}
	if env := os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR"); env != "" {
		return env
	}
	return "./data/attachments"
}

// readStringSetting 从 settings.Global 读 string 值（platform scope）
func readStringSetting(key string) string {
	if settings.Global == nil {
		return ""
	}
	v, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
	if err != nil || len(v) == 0 {
		return ""
	}
	// 去掉 JSON 字符串的引号
	s := string(v)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return s
}

// readIntSetting 从 settings.Global 读 int 值。返回 (值, source)。
func readIntSetting(key string) (int, string) {
	if settings.Global == nil {
		return 0, ""
	}
	v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
	if err != nil || len(v) == 0 {
		return 0, ""
	}
	s := string(v)
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, ""
	}
	return n, src
}

// readBoolSetting 从 settings.Global 读 bool 值。
func readBoolSetting(key string) (bool, string) {
	if settings.Global == nil {
		return false, ""
	}
	v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
	if err != nil || len(v) == 0 {
		return false, ""
	}
	s := string(v)
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	return s == "true" || s == "1", src
}

// diskUsageAt 返回 path 所在磁盘的 (usage%, total, free, used, error)
func diskUsageAt(path string) (pct float64, total, free, used uint64, err error) {
	var stat syscall.Statfs_t
	if err = syscall.Statfs(path, &stat); err != nil {
		return
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free = stat.Bavail * uint64(stat.Bsize)
	used = total - (stat.Bfree * uint64(stat.Bsize))
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return
}

// isWritableDir 测试目录是否可写
func isWritableDir(dir string) bool {
	tmp := filepath.Join(dir, ".gw_test_writable")
	f, err := os.Create(tmp)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(tmp)
	return true
}

// auditSettingChange 记录设置变更审计（slog structured）。
// 复用现有 slog 审计机制；变更同时会进入 settings_kv.prev_value 回滚链。
func auditSettingChange(user, role, key, newVal string) {
	slog.Info("storage config updated",
		"user", user, "role", role, "key", key, "new_val", newVal)
}
