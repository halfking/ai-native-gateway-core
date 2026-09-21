package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// pluginInstaller is the minimal surface the HTTP handler needs.
// *pluginruntime.Installer satisfies it; tests pass a fake.
type pluginInstaller interface {
	Install(ctx context.Context, pluginID, version string) error
}

type pluginInstallRequest struct {
	PluginID string `json:"plugin_id"`
	Version  string `json:"version"`
}

// makePluginInstallHandler returns an http.HandlerFunc that triggers a plugin
// install. Admin-gating is applied by the caller via admin.AdminMiddleware.
// If inst is nil (installer disabled), returns 503.
func makePluginInstallHandler(inst pluginInstaller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if inst == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "plugin installer disabled (LLM_GATEWAY_MAINTAIN_URL not set)",
				"code":  "plugin.installer_disabled",
			})
			return
		}
		var req pluginInstallRequest
		if err := jsonbody.DecodeRequest(r, &req, jsonbody.MaxRequiredBody, true); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "request body is not valid JSON",
				"code":  "plugin.invalid_body",
			})
			return
		}
		if req.PluginID == "" || req.Version == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "plugin_id and version are required",
				"code":  "plugin.missing_fields",
			})
			return
		}
		if err := inst.Install(r.Context(), req.PluginID, req.Version); err != nil {
			slog.Error("plugin install failed", "plugin", req.PluginID, "version", req.Version, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": err.Error(),
				"code":  "plugin.install_failed",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":    "installed",
			"plugin_id": req.PluginID,
			"version":   req.Version,
		})
	}
}

// Compile-time check that *pluginruntime.Installer satisfies pluginInstaller.
var _ pluginInstaller = (*pluginruntime.Installer)(nil)
