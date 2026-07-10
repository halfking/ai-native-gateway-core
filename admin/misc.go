package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// llmGatewayVersionJSON 是版本信息的唯一来源 (SSOT - Single Source of Truth)
// 替代之前分散在 VERSION / .deploy_seq / build_seq / web/public/version.json 等多个文件中的版本信息。
//
// 部署流程：
//  1. bump-version.sh 更新 version.json（唯一写入点）
//  2. 部署时只需上传一个 version.json 文件
//  3. 所有版本/编译次数读取都从这一个文件解析
//
// 格式示例：
//
//	{
//	  "version":    "2.4.2-f56f3c5e-20260710-965",
//	  "git_tag":    "2.4.2",
//	  "git_sha":    "f56f3c5e",
//	  "build_seq":  965,
//	  "build_date": "20260710",
//	  "module":     "llm-gateway-go"
//	}
const llmGatewayVersionJSON = "version.json"

var (
	versionCache   map[string]any
	versionCacheMu sync.RWMutex
)

// loadVersionInfo 统一从 version.json 读取版本信息。
// 优先路径（与生产部署一致）：/opt/llm-gateway-go/version.json
// 回退路径（开发环境）：当前工作目录及 services/llm-gateway-go/
// 结果缓存 5 秒，避免每次请求都读文件。
//
// version 字段只显示 git tag (如 "v2.4.2")，简短易读。
// 完整的构建信息（git_sha / build_seq / build_date）在独立字段显示。
func loadVersionInfo() map[string]any {
	// 1) 优先：环境变量注入的原始 JSON（部署脚本可设置）
	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_VERSION_JSON")); raw != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			normalizeVersionField(m)
			return m
		}
	}

	// 2) 从文件读取（带缓存）
	versionCacheMu.RLock()
	cached := versionCache
	versionCacheMu.RUnlock()
	if cached != nil {
		return cached
	}

	versionCacheMu.Lock()
	defer versionCacheMu.Unlock()
	if versionCache != nil {
		return versionCache
	}

	for _, path := range versionJSONCandidates() {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			normalizeVersionField(m)
			versionCache = m
			return m
		}
	}

	// 3) 完全读取不到时使用默认值（理论上不应发生）
	versionCache = map[string]any{
		"version":    "v0.0.0", // 只显示 tag，简短
		"git_tag":    "v0.0.0",
		"git_sha":    "unknown",
		"build_seq":  0,
		"build_date": time.Now().UTC().Format("20060102"),
		"module":     "llm-gateway-go",
	}
	return versionCache
}

// normalizeVersionField 将 version 字段规范化为简短格式（只显示 git tag）。
// version.json 中 version 字段原本是 "2.4.2-f56f3c5e-20260710-965"（完整构建信息），
// 但前端只显示 tag 部分如 "v2.4.2"，更清晰易读。
// 完整的构建信息保留在 git_sha / build_seq / build_date 字段。
func normalizeVersionField(m map[string]any) {
	// 如果已经有 version 字段（短格式），优先使用 git_tag
	if gitTag, ok := m["git_tag"].(string); ok && gitTag != "" {
		// 给 tag 加 v 前缀（如果还没有）
		if !strings.HasPrefix(gitTag, "v") {
			gitTag = "v" + gitTag
		}
		m["version"] = gitTag
		m["git_tag"] = gitTag
		return
	}
	// 否则从完整 version 字符串提取 tag 部分
	if fullVersion, ok := m["version"].(string); ok && fullVersion != "" {
		// "2.4.2-f56f3c5e-20260710-965" → "v2.4.2"
		parts := strings.SplitN(fullVersion, "-", 2)
		tag := "v" + parts[0]
		if !strings.HasPrefix(parts[0], "v") {
			tag = "v" + parts[0]
		}
		m["version"] = tag
		m["git_tag"] = tag
	}
}

// versionJSONCandidates 按优先级返回可能的 version.json 路径。
func versionJSONCandidates() []string {
	candidates := []string{
		"/opt/llm-gateway-go/" + llmGatewayVersionJSON,
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidates = append(candidates,
			wd+"/"+llmGatewayVersionJSON,
			wd+"/services/llm-gateway-go/"+llmGatewayVersionJSON,
		)
	}
	return candidates
}

// parseBuildSeq 从版本字符串末尾提取 build_seq。
// 版本格式：<semver>-<git_sha>-<date>-<build_seq>
// 例如 "2.4.2-f56f3c5e-20260710-965" → 965
func parseBuildSeq(version string) int {
	parts := strings.Split(version, "-")
	if len(parts) == 0 {
		return 0
	}
	last := parts[len(parts)-1]
	if n, err := strconv.Atoi(last); err == nil {
		return n
	}
	return 0
}

// invalidateVersionCache 清除缓存（供外部在版本更新时调用，目前由环境变量方案支持）。
// 保留为公开函数以备未来需要（如热重载场景）。
func invalidateVersionCache() {
	versionCacheMu.Lock()
	versionCache = nil
	versionCacheMu.Unlock()
}

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
