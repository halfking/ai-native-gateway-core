package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

type degradationControl struct {
	monitor *dbdegradation.Monitor
	ttl     *dbdegradation.TTLManager
	writer  *dbdegradation.FileWriter
	reader  *dbdegradation.FileReader
	generic *dbdegradation.GenericRecovery
	mu      sync.Mutex
	manual  bool
	enter   func(context.Context) error
	exit    func(context.Context) error
}

func (h *Handler) WireDegradationControl(monitor *dbdegradation.Monitor, ttl *dbdegradation.TTLManager, writer *dbdegradation.FileWriter, reader *dbdegradation.FileReader, generic *dbdegradation.GenericRecovery, enter, exit func(context.Context) error) {
	h.degradation = &degradationControl{monitor: monitor, ttl: ttl, writer: writer, reader: reader, generic: generic, enter: enter, exit: exit}
}

func (h *Handler) handleDegradationStatus(w http.ResponseWriter, r *http.Request) {
	if h.degradation == nil {
		writeError(w, http.StatusServiceUnavailable, "degradation control not configured")
		return
	}
	status := "unknown"
	if h.degradation.monitor != nil {
		status = h.degradation.monitor.GetStatus().String()
	}
	manual := false
	h.degradation.mu.Lock()
	manual = h.degradation.manual
	h.degradation.mu.Unlock()
	response := map[string]any{"status": status, "manual": manual}
	if h.degradation.ttl != nil {
		response["ttl_mode"] = h.degradation.ttl.GetMode()
		response["ttl_stats"] = h.degradation.ttl.GetTTLStats(r.Context())
	}
	if h.degradation.reader != nil {
		if summary, err := h.degradation.reader.GetBackupSummary(r.Context()); err == nil {
			response["backups"] = summary
		}
	}
	writeJSON(w, http.StatusOK, response)
}
func (h *Handler) handleDegradationControl(w http.ResponseWriter, r *http.Request) {
	if h.degradation == nil {
		writeError(w, http.StatusServiceUnavailable, "degradation control not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	h.degradation.mu.Lock()
	defer h.degradation.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	switch req.Action {
	case "enter":
		if h.degradation.enter != nil {
			if err := h.degradation.enter(ctx); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if h.degradation.ttl != nil {
			if err := h.degradation.ttl.EnterDegradedMode(ctx); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		h.degradation.manual = true
	case "exit":
		if h.degradation.exit != nil {
			if err := h.degradation.exit(ctx); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if h.degradation.ttl != nil {
			if err := h.degradation.ttl.ExitDegradedMode(ctx); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		h.degradation.manual = false
	default:
		writeError(w, http.StatusBadRequest, "action must be enter or exit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "manual": h.degradation.manual})
}

func (h *Handler) handleDegradationRecovery(w http.ResponseWriter, r *http.Request) {
	if h.degradation == nil || h.degradation.reader == nil || h.degradation.generic == nil {
		writeError(w, http.StatusServiceUnavailable, "degradation recovery not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Filename string `json:"filename"`
		Archive  bool   `json:"archive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Filename == "" {
		writeError(w, http.StatusBadRequest, "filename required")
		return
	}
	taskID, err := h.degradation.generic.Recover(r.Context(), req.Filename, req.Archive)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task_id": taskID, "status": "pending"})
}

func (h *Handler) handleDegradationRecoveryTask(w http.ResponseWriter, r *http.Request) {
	if h.degradation == nil || h.degradation.generic == nil {
		writeError(w, http.StatusServiceUnavailable, "degradation recovery not configured")
		return
	}
	task, ok := h.degradation.generic.Status(r.PathValue("task_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}
