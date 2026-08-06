package admin

import (
	"net/http"
)

// 2026-08-06: Session management API handlers (public wrappers)
// These methods expose the internal session management handlers with proper authentication.
// The actual implementation is in session_management_api.go and session_task_project_api.go.

// HandleSessionManagementList handles the session list request with enhanced filtering
// GET /api/sessions/list
func (h *Handler) HandleSessionManagementList(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	h.handleSessionsList(w, r)
}

// HandleSessionManagementDetail handles session detail requests
// GET /api/sessions/detail/{session_key}
func (h *Handler) HandleSessionManagementDetail(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	h.handleSessionDetail(w, r)
}

// HandleSessionManagementUpdate handles session metadata update requests
// PATCH /api/sessions/update/{session_key}
func (h *Handler) HandleSessionManagementUpdate(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	h.handleSessionUpdate(w, r)
}

// HandleTaskFlow handles task flow requests
// GET /api/sessions/task-flow/{task_id}
func (h *Handler) HandleTaskFlow(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	h.handleTaskFlow(w, r)
}

// HandleProjectCosts handles project cost summary requests
// GET /api/sessions/project-costs/{project_id}
func (h *Handler) HandleProjectCosts(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	h.handleProjectCosts(w, r)
}
