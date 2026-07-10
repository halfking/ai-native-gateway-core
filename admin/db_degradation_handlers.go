package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

var backupFilenamePattern = regexp.MustCompile(`^sessions-\d{4}-\d{2}-\d{2}(-\d{2})?\.jsonl\.gz$`)

// validateBackupFilename 验证备份文件名格式，防止路径遍历攻击
func validateBackupFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename cannot be empty")
	}

	// 检查路径遍历字符
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		return fmt.Errorf("filename contains invalid characters")
	}

	// 验证格式: sessions-YYYY-MM-DD.jsonl.gz 或 sessions-YYYY-MM-DD-NN.jsonl.gz
	if !backupFilenamePattern.MatchString(filename) {
		return fmt.Errorf("filename must match format: sessions-YYYY-MM-DD.jsonl.gz")
	}

	return nil
}

// WireDBDegradation 注入数据库降级模块
func (h *Handler) WireDBDegradation(
	monitor *dbdegradation.Monitor,
	reader *dbdegradation.FileReader,
	recovery *dbdegradation.Recovery,
	ttlManager *dbdegradation.TTLManager,
) {
	if h == nil {
		return
	}
	h.dbMonitor = monitor
	h.fileReader = reader
	h.recovery = recovery
	h.ttlManager = ttlManager
}

// handleDBStatus 获取数据库状态
// GET /api/admin/db-status
func (h *Handler) handleDBStatus(w http.ResponseWriter, r *http.Request) {
	if h.dbMonitor == nil {
		writeError(w, http.StatusServiceUnavailable, "db monitor not configured")
		return
	}

	status := h.dbMonitor.GetStatus()
	lastCheck := h.dbMonitor.GetLastCheckTime()
	degradedDuration := h.dbMonitor.GetDegradedDuration()

	response := map[string]interface{}{
		"status":            status.String(),
		"last_check":        lastCheck.Format("2006-01-02T15:04:05Z07:00"),
		"degraded_duration": degradedDuration.String(),
	}

	if h.ttlManager != nil {
		response["ttl_mode"] = h.ttlManager.GetMode()
		response["ttl_stats"] = h.ttlManager.GetTTLStats(r.Context())
	}

	writeJSON(w, http.StatusOK, response)
}

// handleListBackups 列出备份文件
// GET /api/admin/backups
func (h *Handler) handleListBackups(w http.ResponseWriter, r *http.Request) {
	if h.fileReader == nil {
		writeError(w, http.StatusServiceUnavailable, "file reader not configured")
		return
	}

	summary, err := h.fileReader.GetBackupSummary(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retrieve backup summary")
		return
	}

	writeJSON(w, http.StatusOK, summary)
}

// handleGetBackupFile 获取单个备份文件详情
// GET /api/admin/backups/:filename
func (h *Handler) handleGetBackupFile(w http.ResponseWriter, r *http.Request) {
	if h.fileReader == nil {
		writeError(w, http.StatusServiceUnavailable, "file reader not configured")
		return
	}

	filename := r.PathValue("filename")
	if err := validateBackupFilename(filename); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	file, err := h.fileReader.GetFileSummary(r.Context(), filename)
	if err != nil {
		writeError(w, http.StatusNotFound, "backup file not found")
		return
	}

	writeJSON(w, http.StatusOK, file)
}

// handleRecoverBackupFile 恢复单个备份文件
// POST /api/admin/backups/:filename/recover
func (h *Handler) handleRecoverBackupFile(w http.ResponseWriter, r *http.Request) {
	if h.recovery == nil {
		writeError(w, http.StatusServiceUnavailable, "recovery not configured")
		return
	}

	filename := r.PathValue("filename")
	if err := validateBackupFilename(filename); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req struct {
		DeleteAfter bool `json:"delete_after"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	taskID, err := h.recovery.RecoverFile(r.Context(), filename, req.DeleteAfter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start recovery task")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"task_id": taskID,
		"status":  "pending",
	})
}

// handleRecoverAllBackups 恢复所有备份文件
// POST /api/admin/backups/recover-all
func (h *Handler) handleRecoverAllBackups(w http.ResponseWriter, r *http.Request) {
	if h.recovery == nil {
		writeError(w, http.StatusServiceUnavailable, "recovery not configured")
		return
	}

	var req struct {
		DeleteAfter bool `json:"delete_after"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	taskID, err := h.recovery.RecoverAll(r.Context(), req.DeleteAfter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start recovery task")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"task_id": taskID,
		"status":  "pending",
	})
}

// handleGetRecoveryTask 获取恢复任务状态
// GET /api/admin/recovery-tasks/:task_id
func (h *Handler) handleGetRecoveryTask(w http.ResponseWriter, r *http.Request) {
	if h.recovery == nil {
		writeError(w, http.StatusServiceUnavailable, "recovery not configured")
		return
	}

	taskID := r.PathValue("task_id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id required")
		return
	}

	task, ok := h.recovery.GetTaskStatus(taskID)
	if !ok {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	writeJSON(w, http.StatusOK, task)
}

// handleValidateBackupFile 验证备份文件
// POST /api/admin/backups/:filename/validate
func (h *Handler) handleValidateBackupFile(w http.ResponseWriter, r *http.Request) {
	if h.fileReader == nil {
		writeError(w, http.StatusServiceUnavailable, "file reader not configured")
		return
	}

	filename := r.PathValue("filename")
	if err := validateBackupFilename(filename); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.fileReader.ValidateFile(r.Context(), filename); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"valid": false,
			"error": "file validation failed",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"valid": true,
	})
}
