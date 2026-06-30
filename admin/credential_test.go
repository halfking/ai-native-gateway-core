package admin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
)

// handleTestCredential 手动触发凭据探测
// POST /api/credentials/{id}/test
func handleTestCredential(w http.ResponseWriter, r *http.Request) {
	credIDStr := r.PathValue("id")
	credID, err := strconv.Atoi(credIDStr)
	if err != nil {
		http.Error(w, "invalid credential ID", http.StatusBadRequest)
		return
	}

	// 提交到快速探测队列（复用现有的 CredentialProbeV2.SubmitFastProbe）
	// 实际实现需要在路由注册时注入依赖
	credProbeV2 := r.Context().Value("credProbeV2")
	if credProbeV2 == nil {
		http.Error(w, "probe service not available", http.StatusServiceUnavailable)
		return
	}

	submitter, ok := credProbeV2.(interface{ SubmitFastProbe(int) })
	if !ok {
		http.Error(w, "probe service invalid", http.StatusInternalServerError)
		return
	}

	submitter.SubmitFastProbe(credID)

	slog.Info("manual probe submitted",
		"credential_id", credID,
		"source", "web_api",
		"user", r.Context().Value("user_id"))

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":       "probe submitted to fast queue",
		"credential_id": credID,
		"status":        "pending",
	})
}

// handleTestCredentialModel 手动触发凭据+模型探测
// POST /api/credentials/{id}/models/{model}/test
func handleTestCredentialModel(w http.ResponseWriter, r *http.Request) {
	credIDStr := r.PathValue("id")
	credID, err := strconv.Atoi(credIDStr)
	if err != nil {
		http.Error(w, "invalid credential ID", http.StatusBadRequest)
		return
	}

	model := r.PathValue("model")
	if model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	// 实际实现需要调用 ModelProbeRunner.TriggerManual
	// 这里先返回接受状态
	slog.Info("manual model probe submitted",
		"credential_id", credID,
		"model", model,
		"source", "web_api",
		"user", r.Context().Value("user_id"))

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":       "model probe submitted",
		"credential_id": credID,
		"model":         model,
		"status":        "pending",
	})
}

// handleBatchTestCredentials 批量触发凭据探测
// POST /api/credentials/test-batch
func handleBatchTestCredentials(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CredentialIDs []int `json:"credential_ids"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.CredentialIDs) == 0 {
		http.Error(w, "credential_ids is required", http.StatusBadRequest)
		return
	}

	if len(req.CredentialIDs) > 100 {
		http.Error(w, "too many credentials (max 100)", http.StatusBadRequest)
		return
	}

	credProbeV2 := r.Context().Value("credProbeV2")
	if credProbeV2 == nil {
		http.Error(w, "probe service not available", http.StatusServiceUnavailable)
		return
	}

	submitter, ok := credProbeV2.(interface{ SubmitFastProbe(int) })
	if !ok {
		http.Error(w, "probe service invalid", http.StatusInternalServerError)
		return
	}

	submitted := 0
	for _, credID := range req.CredentialIDs {
		submitter.SubmitFastProbe(credID)
		submitted++
	}

	slog.Info("batch probe submitted",
		"count", submitted,
		"source", "web_api",
		"user", r.Context().Value("user_id"))

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":   "batch probe submitted to fast queue",
		"submitted": submitted,
		"total":     len(req.CredentialIDs),
	})
}

// handleCredentialStateQuery 查询凭据+模型状态
// GET /api/credentials/{id}/models/{model}/state
func handleCredentialStateQuery(w http.ResponseWriter, r *http.Request) {
	credIDStr := r.PathValue("id")
	credID, err := strconv.Atoi(credIDStr)
	if err != nil {
		http.Error(w, "invalid credential ID", http.StatusBadRequest)
		return
	}

	model := r.PathValue("model")
	if model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	stateManager := r.Context().Value("stateManager")
	if stateManager == nil {
		http.Error(w, "state manager not available", http.StatusServiceUnavailable)
		return
	}

	getter, ok := stateManager.(interface {
		GetState(ctx interface{}, credID int, model string) (interface{}, error)
	})
	if !ok {
		http.Error(w, "state manager invalid", http.StatusInternalServerError)
		return
	}

	state, err := getter.GetState(r.Context(), credID, model)
	if err != nil {
		slog.Warn("failed to get credential state",
			"credential_id", credID,
			"model", model,
			"error", err)
		http.Error(w, fmt.Sprintf("failed to get state: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"credential_id": credID,
		"model":         model,
		"state":         state,
	})
}
