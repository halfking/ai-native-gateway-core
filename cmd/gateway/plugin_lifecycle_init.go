package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// pluginLifecycle is the surface the lifecycle admin API needs. The real
// *pluginruntime.Supervisor satisfies it; tests pass a fake. The DB-truth
// (plugin_catalog rows, install manifests) lives outside the supervisor's
// in-memory state, so a noop stub is permitted by the contract — the route
// stays stable while persistence is wired separately.
//
// All methods must be safe to call when the plugin is unknown (return an
// error). Activate on an already-running plugin returns nil (idempotent).
type pluginLifecycle interface {
	Activate(ctx context.Context, pluginID string) error
	Deactivate(ctx context.Context, pluginID string) error
	Uninstall(ctx context.Context, pluginID string) error
}

// noopPluginLifecycle is the default when no supervisor is wired (plugins
// disabled). Every method returns ErrPluginLifecycleUnavailable so the
// admin API can surface 503 without crashing the gateway.
type noopPluginLifecycle struct{}

func (noopPluginLifecycle) Activate(context.Context, string) error   { return errPluginLifecycleUnavailable }
func (noopPluginLifecycle) Deactivate(context.Context, string) error { return errPluginLifecycleUnavailable }
func (noopPluginLifecycle) Uninstall(context.Context, string) error  { return errPluginLifecycleUnavailable }

var errPluginLifecycleUnavailable = &pluginLifecycleError{code: "plugin.lifecycle_unavailable", msg: "plugin lifecycle API disabled (LLM_GATEWAY_PLUGINS_DIR not set)"}

type pluginLifecycleError struct {
	code string
	msg  string
}

func (e *pluginLifecycleError) Error() string { return e.msg }

// resolveLifecycle returns the real supervisor wrapper when a supervisor is
// wired (pluginsDir != "" && sup != nil), otherwise a noop that 503s. Kept
// as a function so future code paths can swap in DB-backed implementations
// without touching the handler factories.
func resolveLifecycle(sup *pluginruntime.Supervisor, pluginsDir string) pluginLifecycle {
	if sup == nil {
		return noopPluginLifecycle{}
	}
	return &supervisorLifecycle{sup: sup, pluginsDir: pluginsDir}
}

// supervisorLifecycle adapts *pluginruntime.Supervisor to the lifecycle
// surface. Activate is idempotent (Restart = Stop+Start via Start),
// Deactivate is Stop (no-op if not running), Uninstall stops the process,
// clears supervisor in-memory state, and deletes the on-disk bundle
// directory under pluginsDir with strict path-containment (02 §3.4 / V5.1
// P0-2: uninstall must leave no residual bundle, dir/symlink, or nav).
type supervisorLifecycle struct {
	sup        *pluginruntime.Supervisor
	pluginsDir string
}

func (s *supervisorLifecycle) Activate(ctx context.Context, pluginID string) error {
	if !isValidPluginID(pluginID) {
		return errPluginInvalidID
	}
	m := s.sup.ManifestOf(pluginID)
	if m == nil {
		return &pluginLifecycleError{code: "plugin.not_found", msg: "plugin " + pluginID + " is not installed"}
	}
	if err := s.sup.Restart(pluginID); err != nil {
		return &pluginLifecycleError{code: "plugin.activate_failed", msg: err.Error()}
	}
	return nil
}

func (s *supervisorLifecycle) Deactivate(ctx context.Context, pluginID string) error {
	if !isValidPluginID(pluginID) {
		return errPluginInvalidID
	}
	if err := s.sup.Stop(pluginID); err != nil {
		return &pluginLifecycleError{code: "plugin.deactivate_failed", msg: err.Error()}
	}
	return nil
}

func (s *supervisorLifecycle) Uninstall(ctx context.Context, pluginID string) error {
	if !isValidPluginID(pluginID) {
		return errPluginInvalidID
	}
	// Stop first so the unix socket and process release before disk removal.
	// Stop returns an error if the plugin wasn't running; we treat that as
	// non-fatal so uninstall of an inactive plugin still cleans up the
	// bundle (omniroute PR #3473: deactivate order is mandatory before
	// unregister, which mirrors our Stop-before-rmdir).
	if err := s.sup.Stop(pluginID); err != nil {
		slog.Warn("plugin stop during uninstall reported error (continuing)", "plugin", pluginID, "error", err)
	}
	if err := s.sup.Uninstall(pluginID); err != nil {
		return &pluginLifecycleError{code: "plugin.uninstall_failed", msg: err.Error()}
	}
	if s.pluginsDir == "" {
		// No on-disk bundle to clean; supervisor state already cleared.
		return nil
	}
	bundle, err := resolvePluginBundle(s.pluginsDir, pluginID)
	if err != nil {
		return &pluginLifecycleError{code: "plugin.invalid_id", msg: err.Error()}
	}
	if bundle != "" {
		if err := os.RemoveAll(bundle); err != nil {
			return &pluginLifecycleError{code: "plugin.uninstall_failed", msg: "remove bundle: " + err.Error()}
		}
		slog.Info("plugin bundle removed", "plugin", pluginID, "path", bundle)
	}
	return nil
}

// resolvePluginBundle returns the absolute <pluginsDir>/<pluginID> path,
// verifying that the resolved path is contained inside pluginsDir. Returns
// ("", nil) when the directory does not exist (idempotent uninstall on an
// already-cleaned install). Returns an error if the resolved path escapes
// the pluginsDir root — defense against plugin_id values that trick
// filepath.Clean into an outer path (e.g. "a/../b" cleans to "b" which is
// still inside root but no longer reflects the plugin's identity).
func resolvePluginBundle(pluginsDir, pluginID string) (string, error) {
	root, err := filepath.Abs(pluginsDir)
	if err != nil {
		return "", fmt.Errorf("resolve pluginsDir: %w", err)
	}
	// Reject anything that isn't a single path segment: "../etc", "a/b",
	// "a/../b" all collapse to non-segment values. isValidPluginID already
	// filters the upstream cases, but resolvePluginBundle is the single
	// source of truth for path safety — re-check before any FS call.
	cleaned := filepath.Clean(pluginID)
	if cleaned != pluginID || strings.Contains(cleaned, string(filepath.Separator)) || cleaned == "." || cleaned == ".." {
		return "", fmt.Errorf("plugin id escapes pluginsDir")
	}
	target := filepath.Join(root, pluginID)
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve bundle: %w", err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin id escapes pluginsDir")
	}
	if _, err := os.Stat(abs); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat bundle: %w", err)
	}
	return abs, nil
}

var errPluginInvalidID = &pluginLifecycleError{code: "plugin.invalid_id", msg: "invalid plugin id"}

// pluginIDPattern mirrors the manifest validator's plugin_id rule so we
// reject the same shape before crossing any boundary. Defense-in-depth:
// the path value comes from the URL, but admin auth already gates the route.
var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func isValidPluginID(id string) bool { return id != "" && len(id) <= 64 && pluginIDPattern.MatchString(id) }

// makePluginActivateHandler: POST /api/v1/plugins/{id}/activate.
// Admin-gating is applied by the caller via admin.AdminMiddleware.
func makePluginActivateHandler(lc pluginLifecycle) http.HandlerFunc {
	return makeLifecycleHandler(lc, "activate", func(ctx context.Context, lc pluginLifecycle, id string) error {
		return lc.Activate(ctx, id)
	})
}

// makePluginDeactivateHandler: POST /api/v1/plugins/{id}/deactivate.
func makePluginDeactivateHandler(lc pluginLifecycle) http.HandlerFunc {
	return makeLifecycleHandler(lc, "deactivate", func(ctx context.Context, lc pluginLifecycle, id string) error {
		return lc.Deactivate(ctx, id)
	})
}

// makePluginUninstallHandler: POST /api/v1/plugins/{id}/uninstall.
func makePluginUninstallHandler(lc pluginLifecycle) http.HandlerFunc {
	return makeLifecycleHandler(lc, "uninstall", func(ctx context.Context, lc pluginLifecycle, id string) error {
		return lc.Uninstall(ctx, id)
	})
}

// makeLifecycleHandler wraps the per-action handler with the common contract:
// read {id} path value, dispatch, map errors to status codes.
//
// Status mapping:
//   - 503 if lifecycle is unavailable (LLM_GATEWAY_PLUGINS_DIR unset / sup nil)
//   - 400 if id fails pluginIDPattern
//   - 404 if plugin not installed (catalog-side miss)
//   - 500 on generic action failure
//
// The route table in main.go mounts these as admin-gated; auth middleware
// runs before this handler so anonymous callers never reach here.
func makeLifecycleHandler(lc pluginLifecycle, action string, do func(context.Context, pluginLifecycle, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if lc == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "plugin lifecycle API disabled",
				"code":  "plugin.lifecycle_unavailable",
			})
			return
		}
		id := r.PathValue("id")
		if !isValidPluginID(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid plugin id",
				"code":  "plugin.invalid_id",
			})
			return
		}
		if err := do(r.Context(), lc, id); err != nil {
			var ple *pluginLifecycleError
			status := http.StatusInternalServerError
			if e, ok := err.(*pluginLifecycleError); ok {
				ple = e
				switch ple.code {
				case "plugin.not_found":
					status = http.StatusNotFound
				case "plugin.invalid_id":
					status = http.StatusBadRequest
				case "plugin.lifecycle_unavailable":
					status = http.StatusServiceUnavailable
				}
			}
			slog.Error("plugin "+action+" failed", "plugin", id, "error", err)
			writeJSON(w, status, map[string]string{
				"error":     err.Error(),
				"code":      errorCode(err),
				"plugin_id": id,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":    pastTense(action),
			"plugin_id": id,
		})
	}
}

// errorCode returns the structured code on a *pluginLifecycleError or
// "plugin.internal_error" otherwise. Keeps the response shape stable.
func errorCode(err error) string {
	if ple, ok := err.(*pluginLifecycleError); ok && ple.code != "" {
		return ple.code
	}
	return "plugin.internal_error"
}

// pastTense maps the action verb to a stable status string. The values are
// fixed (not action+"d") so "uninstall" doesn't become "uninstalld" and we
// can match on them in tests/clients without guessing the suffix rule.
func pastTense(action string) string {
	switch action {
	case "activate":
		return "activated"
	case "deactivate":
		return "deactivated"
	case "uninstall":
		return "uninstalled"
	default:
		return action
	}
}