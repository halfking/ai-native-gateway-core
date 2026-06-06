package admin

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

func (h *Handler) handleTags(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if r.Method == http.MethodGet {
		h.listTags(ctx, w)
		return
	}
	if r.Method == http.MethodPost {
		h.createTag(ctx, w, r)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) listTags(ctx context.Context, w http.ResponseWriter) {
	rows, err := h.db.Query(ctx, `
		WITH unnested AS (
			SELECT mc.id, mc.canonical_name, UNNEST(COALESCE(mc.tags,'[]'::jsonb)) AS tag
			FROM models_canonical mc
			WHERE mc.tags IS NOT NULL AND mc.tags != '[]'::jsonb
			AND COALESCE(mc.status,'active') = 'active'
		)
		SELECT tag,
		       COUNT(*) AS canonical_count,
		       ARRAY_AGG(canonical_name ORDER BY canonical_name)[1:5] AS samples
		FROM unnested
		GROUP BY tag
		ORDER BY tag
	`)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"namespaces": []any{}})
		return
	}
	defer rows.Close()

	type tagInfo struct {
		Tag     string   `json:"tag"`
		Count   int      `json:"count"`
		Samples []string `json:"samples"`
	}
	type namespaceInfo struct {
		Namespace string    `json:"namespace"`
		Tags     []tagInfo `json:"tags"`
	}

	grouped := map[string][]tagInfo{}
	for rows.Next() {
		var tag string
		var count int
		var samples []string
		if err := rows.Scan(&tag, &count, &samples); err != nil {
			continue
		}
		ns := "other"
		if idx := strings.Index(tag, ":"); idx > 0 {
			ns = tag[:idx]
		}
		grouped[ns] = append(grouped[ns], tagInfo{Tag: tag, Count: count, Samples: samples})
	}

	namespaces := make([]namespaceInfo, 0)
	for ns, tags := range grouped {
		namespaces = append(namespaces, namespaceInfo{Namespace: ns, Tags: tags})
	}
	if namespaces == nil {
		namespaces = []namespaceInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": namespaces})
}

func (h *Handler) createTag(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tag    string `json:"tag"`
		Models []int  `json:"models,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Tag == "" {
		writeError(w, http.StatusBadRequest, "tag required")
		return
	}

	if len(req.Models) > 0 {
		h.db.Exec(ctx, `
			UPDATE models_canonical
			SET tags = COALESCE(tags, '[]'::jsonb) || $1::jsonb
			WHERE id = ANY($2)
		`, fmt.Sprintf(`["%s"]`, req.Tag), req.Models)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"tag":      req.Tag,
		"models":   len(req.Models),
		"message":  "tag created",
	})
}

func (h *Handler) handleSystemTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	type taskStatus struct {
		Name    string `json:"name"`
		Alive   bool   `json:"alive"`
		Status  string `json:"status,omitempty"`
		Started string `json:"started_at,omitempty"`
	}
	tasks := []taskStatus{
		{Name: "discovery", Alive: h.discSvc != nil},
		{Name: "cred_cycler", Alive: h.credCycler != nil},
		{Name: "cred_recovery", Alive: h.credRecov != nil},
		{Name: "env_cleaner", Alive: h.envCleaner != nil},
		{Name: "sticky_clean", Alive: h.stickyClean != nil},
		{Name: "tax_sync", Alive: h.taxSync != nil},
	}
	aliveCount := 0
	for i := range tasks {
		if tasks[i].Alive {
			tasks[i].Status = "running"
			aliveCount++
		} else {
			tasks[i].Status = "not_configured"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":  tasks,
		"status": "running",
		"alive_count": aliveCount,
	})
}

func (h *Handler) handleSystemVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	version := os.Getenv("LLM_GATEWAY_VERSION")
	if version == "" {
		version = "0.2.0"
	}

	gitSHA := os.Getenv("GIT_SHA")
	if gitSHA == "" {
		gitSHA = "unknown"
	}

	buildTime := os.Getenv("BUILD_TIME")
	if buildTime == "" {
		buildTime = time.Now().Format("20060102")
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":    version,
		"git_sha":    gitSHA,
		"build_time": buildTime,
		"go_version": runtime.Version(),
		"os":         runtime.GOOS + "/" + runtime.GOARCH,
	})
}
