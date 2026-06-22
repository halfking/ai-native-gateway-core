package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const llmGatewayVersionFile = "VERSION"
const llmGatewayDeploySeqFile = ".deploy_seq"

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
		Tags      []tagInfo `json:"tags"`
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
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `
			UPDATE models_canonical
			SET tags = COALESCE(tags, '[]'::jsonb) || $1::jsonb
			WHERE id = ANY($2)
		`, fmt.Sprintf(`["%s"]`, req.Tag), req.Models)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"tag":     req.Tag,
		"models":  len(req.Models),
		"message": "tag created",
	})
}

func (h *Handler) handleSystemTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var discStatus *string
	var discStarted, discFinished, discHeartbeat *time.Time
	var discTrigger, discError *string
	var discSummary []byte
	_ = h.db.QueryRow(ctx, `
		SELECT status, started_at, finished_at, heartbeat_at, trigger, summary_json, error
		FROM model_discovery_runs
		WHERE tenant_id = 'default'
		ORDER BY started_at DESC LIMIT 1
	`).Scan(&discStatus, &discStarted, &discFinished, &discHeartbeat, &discTrigger, &discSummary, &discError)

	isDiscoveryRunning := discStatus != nil && *discStatus == "running"
	now := time.Now().UTC()

	var discStatusVal any
	if discStatus != nil {
		discStatusVal = *discStatus
	}
	var discTriggerVal any
	if discTrigger != nil {
		discTriggerVal = *discTrigger
	}
	var discStartedVal any
	if discStarted != nil {
		discStartedVal = discStarted.UTC().Format(time.RFC3339)
	}
	var discFinishedVal any
	if discFinished != nil {
		discFinishedVal = discFinished.UTC().Format(time.RFC3339)
	}
	var discHeartbeatVal any
	if discHeartbeat != nil {
		discHeartbeatVal = discHeartbeat.UTC().Format(time.RFC3339)
	}
	var discErrorVal any
	if discError != nil {
		discErrorVal = *discError
	}
	var discSummaryVal any
	if len(discSummary) > 0 {
		var summary any
		if json.Unmarshal(discSummary, &summary) == nil {
			discSummaryVal = summary
		}
	}

	var elapsedSeconds any
	if discStarted != nil && isDiscoveryRunning {
		elapsedSeconds = int(now.Sub(discStarted.UTC()).Seconds())
	}
	var sinceLastSeconds any
	if discFinished != nil {
		sinceLastSeconds = int(now.Sub(discFinished.UTC()).Seconds())
	}

	discovery := map[string]any{
		"alive":              h.discSvc != nil,
		"running":            isDiscoveryRunning,
		"status":             discStatusVal,
		"trigger":            discTriggerVal,
		"started_at":         discStartedVal,
		"finished_at":        discFinishedVal,
		"heartbeat_at":       discHeartbeatVal,
		"error":              discErrorVal,
		"summary":            discSummaryVal,
		"elapsed_seconds":    elapsedSeconds,
		"since_last_seconds": sinceLastSeconds,
	}

	var lastProbeAt *time.Time
	var checksLast10m int
	_ = h.db.QueryRow(ctx, `
		SELECT MAX(created_at), COUNT(*) FILTER (WHERE created_at > now() - interval '10 minutes')
		FROM credential_health_checks
	`).Scan(&lastProbeAt, &checksLast10m)
	var lastProbeVal any
	if lastProbeAt != nil {
		lastProbeVal = lastProbeAt.UTC().Format(time.RFC3339)
	}
	probeLoop := map[string]any{
		"alive":           h.credCycler != nil,
		"last_check_at":   lastProbeVal,
		"checks_last_10m": checksLast10m,
	}

	var lastCyclerAt *time.Time
	_ = h.db.QueryRow(ctx, `
		SELECT chc.created_at
		FROM credential_health_checks chc
		JOIN model_discovery_runs mdr ON mdr.id = chc.run_id
		WHERE mdr.trigger = 'scheduled'
		ORDER BY chc.created_at DESC LIMIT 1
	`).Scan(&lastCyclerAt)
	var lastCyclerVal any
	if lastCyclerAt != nil {
		lastCyclerVal = lastCyclerAt.UTC().Format(time.RFC3339)
	}
	cycler := map[string]any{
		"alive":         h.credCycler != nil,
		"last_check_at": lastCyclerVal,
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"discovery":     discovery,
		"probe_loop":    probeLoop,
		"cycler":        cycler,
		"recovery":      map[string]any{"alive": h.credRecov != nil},
		"telemetry":     map[string]any{"alive": true},
		"load_balancer": map[string]any{},
	})
}

func (h *Handler) handleSystemVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	info := loadVersionInfo()
	writeJSON(w, http.StatusOK, info)
}

func loadVersionInfo() map[string]any {
	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_VERSION")); raw != "" {
		return parseVersionString(raw)
	}
	for _, path := range versionFileCandidates() {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if content := strings.TrimSpace(string(raw)); content != "" {
			return parseVersionString(content)
		}
	}

	sha := strings.TrimSpace(os.Getenv("GIT_SHA"))
	if sha == "" {
		sha = "unknown"
	}
	now := time.Now().UTC()
	return map[string]any{
		"version":    "0.1.0",
		"git_sha":    sha,
		"build_time": now.Format("20060102"),
		"build_date": now.Format("2006-01-02"),
		"build_seq":  loadDeploySeq(),
	}
}

func versionFileCandidates() []string {
	candidates := []string{
		"/opt/llm-gateway-go/" + llmGatewayVersionFile,
		llmGatewayVersionFile,
		"services/llm-gateway-go/" + llmGatewayVersionFile,
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidates = append(candidates,
			wd+"/"+llmGatewayVersionFile,
			wd+"/services/llm-gateway-go/"+llmGatewayVersionFile,
		)
	}
	return candidates
}

func loadDeploySeq() int {
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_BUILD_SEQ")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	for _, path := range deploySeqFileCandidates() {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			return n
		}
	}
	return 0
}

func deploySeqFileCandidates() []string {
	candidates := []string{
		"/opt/llm-gateway-go/" + llmGatewayDeploySeqFile,
		llmGatewayDeploySeqFile,
		"services/llm-gateway-go/" + llmGatewayDeploySeqFile,
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidates = append(candidates,
			wd+"/"+llmGatewayDeploySeqFile,
			wd+"/services/llm-gateway-go/"+llmGatewayDeploySeqFile,
		)
	}
	return candidates
}

func parseVersionString(raw string) map[string]any {
	parts := strings.SplitN(raw, "-", 3)
	version := parts[0]
	gitSHA := ""
	buildDate := ""
	if len(parts) > 1 {
		gitSHA = parts[1]
	}
	if len(parts) > 2 {
		buildDate = parts[2]
	}
	if envSHA := strings.TrimSpace(os.Getenv("GIT_SHA")); envSHA != "" {
		gitSHA = envSHA
	}
	if buildDate == "" {
		if bt := strings.TrimSpace(os.Getenv("BUILD_TIME")); bt != "" {
			buildDate = bt
		} else {
			buildDate = time.Now().UTC().Format("20060102")
		}
	}
	displayDate := buildDate
	if len(buildDate) == 8 && buildDate[0] >= '0' && buildDate[0] <= '9' {
		displayDate = buildDate[0:4] + "-" + buildDate[4:6] + "-" + buildDate[6:8]
	}
	return map[string]any{
		"version":    version,
		"git_sha":    gitSHA,
		"build_time": buildDate,
		"build_date": displayDate,
		"build_seq":  loadDeploySeq(),
	}
}
