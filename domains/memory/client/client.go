// Package memora integrates the Memora / MemOS memory service as a
// context-compression oracle for llm-gateway-go.
//
// All operations are best-effort. A Memora outage MUST NOT fail the
// main request path. Errors are logged and counted, never propagated.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/memory" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/pkg/httputil"
)

// Client is a thin HTTP client for the Memora / MemOS product API.
type Client struct {
	baseURL            string
	apiKey             string
	http               *http.Client
	addTimeout         time.Duration
	searchTimeout      time.Duration
	smartSearchBaseURL string
	smartSearchAPIKey  string
	addPath            string
	searchPath         string
	healthPath         string
	projectID          string
}

// Route defaults (2026-10-20, audit D1 — the gateway→memora push chain was
// pointed at routes memora never registered).
//
// memora's real route table, verified against the memora checkout
// (memora/internal/memory/handler.go:73-108, `group := r.Group("/api")`):
//
//	POST /api/add           → h.Add     (handler.go:86)
//	POST /api/memory/add    → h.Add     (handler.go:87)
//	POST /api/search        → h.Search  (handler.go:83)
//	POST /api/memory/search → h.Search  (handler.go:84)
//	GET  /api/health        → h.Health  (handler.go:88)
//
// `/product/add` 与 `/product/search` 是 **MemOS** 的路径（见本文件
// SmartSearch 的注释与 2026-06-20 的双主机设计），**memora 全仓没有任何
// /product/* 路由**。旧客户端把 add/search 都打在这两个路径上 ⇒ 人工
// 抽取端点（admin/session_extract.go）推 memora 必然 404，整条
// 「会话数据 → 记忆」链从未走通。
//
// 因此：默认走 memora 真实路径，MemOS 旧路径保留为**显式 opt-in**
// （ClientConfig.LegacyMemOSPaths 或环境变量 LLM_GATEWAY_MEMORA_LEGACY_MEMOS_PATHS）。
const (
	// DefaultAddPath is memora's add route (memora/internal/memory/handler.go:87).
	DefaultAddPath = "/api/memory/add"
	// DefaultSearchPath is memora's search route (memora/internal/memory/handler.go:83).
	DefaultSearchPath = "/api/search"
	// DefaultHealthPath is memora's health route (memora/internal/memory/handler.go:88).
	DefaultHealthPath = "/api/health"
	// LegacyMemOSAddPath is the MemOS-era add route. Not registered by memora;
	// reachable only through the explicit legacy opt-in.
	LegacyMemOSAddPath = "/product/add"
	// LegacyMemOSSearchPath is the MemOS-era search route. Not registered by
	// memora; reachable only through the explicit legacy opt-in.
	LegacyMemOSSearchPath = "/product/search"
	// LegacyMemOSPathsEnv opts the whole client back into the MemOS route
	// set. Values accepted: 1 / true / yes / on (case-insensitive).
	LegacyMemOSPathsEnv = "LLM_GATEWAY_MEMORA_LEGACY_MEMOS_PATHS"
)

// ClientConfig configures a new Client. All fields are optional.
type ClientConfig struct {
	BaseURL       string
	APIKey        string
	AddTimeout    time.Duration
	SearchTimeout time.Duration
	// SmartSearchBaseURL (2026-06-20) overrides BaseURL for the M1
	// /api/smart_search endpoint. Empty = use BaseURL (legacy single-host
	// mode). When set, Search() and Add() still use BaseURL but
	// SmartSearch() hits this URL instead. This lets the gateway point
	// legacy MemOS calls at the MemOS service and the new 5-step pipeline
	// at the kxmemory Dashboard (which proxies to MemOS + Qdrant).
	SmartSearchBaseURL string
	// SmartSearchAPIKey (2026-06-20) is the API key for the Dashboard
	// smart_search route. Empty = reuse APIKey (when SmartSearchBaseURL
	// == BaseURL). Required when SmartSearchBaseURL is set and differs.
	SmartSearchAPIKey string
	// AddPath overrides the add route. Empty = DefaultAddPath, or
	// LegacyMemOSAddPath when the legacy opt-in is on.
	AddPath string
	// SearchPath overrides the search route. Empty = DefaultSearchPath, or
	// LegacyMemOSSearchPath when the legacy opt-in is on.
	SearchPath string
	// HealthPath overrides the route used by Ping. Empty = DefaultHealthPath,
	// or LegacyMemOSSearchPath (the pre-2026-10-20 behaviour) when the
	// legacy opt-in is on.
	HealthPath string
	// LegacyMemOSPaths selects the MemOS-era /product/* routes instead of
	// memora's real ones. Also enabled by LLM_GATEWAY_MEMORA_LEGACY_MEMOS_PATHS.
	LegacyMemOSPaths bool
	// ProjectID is sent as the X-Project-ID header. memora's project scope
	// is fail-closed: /api/add and /api/search answer
	// 400 "project_id is required (000035 contract)" without it
	// (memora/internal/memory/handler.go:208-211, 335-338; the header is
	// read by memora's ProjectScope middleware).
	ProjectID string
}

// NewClient builds a Client from the given config.
func NewClient(cfg ClientConfig) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:8000"
	}
	if cfg.AddTimeout == 0 {
		cfg.AddTimeout = 5 * time.Second
	}
	if cfg.SearchTimeout == 0 {
		cfg.SearchTimeout = 200 * time.Millisecond
	}
	smartBase := cfg.SmartSearchBaseURL
	if smartBase == "" {
		smartBase = cfg.BaseURL
	}
	smartKey := cfg.SmartSearchAPIKey
	if smartKey == "" {
		smartKey = cfg.APIKey
	}
	addPath, searchPath, healthPath := DefaultAddPath, DefaultSearchPath, DefaultHealthPath
	if cfg.LegacyMemOSPaths || legacyMemOSPathsFromEnv() {
		// MemOS 形态：add 打 /product/add，search 打 /product/search，
		// Ping 仍打 GET /product/search（2026-10-20 之前的实现）。
		addPath, searchPath, healthPath = LegacyMemOSAddPath, LegacyMemOSSearchPath, LegacyMemOSSearchPath
	}
	if cfg.AddPath != "" {
		addPath = cfg.AddPath
	}
	if cfg.SearchPath != "" {
		searchPath = cfg.SearchPath
	}
	if cfg.HealthPath != "" {
		healthPath = cfg.HealthPath
	}
	return &Client{
		baseURL:            cfg.BaseURL,
		apiKey:             cfg.APIKey,
		addTimeout:         cfg.AddTimeout,
		searchTimeout:      cfg.SearchTimeout,
		http:               &http.Client{Timeout: cfg.AddTimeout},
		smartSearchBaseURL: smartBase,
		smartSearchAPIKey:  smartKey,
		addPath:            addPath,
		searchPath:         searchPath,
		healthPath:         healthPath,
		projectID:          cfg.ProjectID,
	}
}

// legacyMemOSPathsFromEnv reports whether the MemOS route set was explicitly
// requested through the environment. Anything other than a recognised truthy
// value (including an empty variable) keeps the memora-real defaults.
func legacyMemOSPathsFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(LegacyMemOSPathsEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// AddPath returns the resolved add route (for status display / diagnostics).
func (c *Client) AddPath() string {
	if c == nil {
		return ""
	}
	return c.addPath
}

// SearchPath returns the resolved search route (for status display / diagnostics).
func (c *Client) SearchPath() string {
	if c == nil {
		return ""
	}
	return c.searchPath
}

// Disabled reports whether the client is configured to no-op.
func (c *Client) Disabled() bool {
	return c == nil || c.baseURL == ""
}

// BaseURL returns the configured Memora base URL (for status display).
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// Ping tests connectivity to the Memora service. Returns nil if the
// service responds (even with a non-2xx status, since that proves the
// server is alive). Returns an error only on transport failure.
// Disabled clients silently return nil.
func (c *Client) Ping(ctx context.Context) error {
	if c.Disabled() {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet,
		c.baseURL+c.healthPath, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	c.setProjectHeader(req, c.projectID)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	if _, err := httputil.ReadPrefixAndDrain(resp.Body, 256); err != nil {
		return err
	}
	return nil
}

// AddMessage persists a message pair to Memora. Best-effort.
// Retries up to 2 times on transient errors (network / 5xx).
func (c *Client) AddMessage(ctx context.Context, userID string, messages []memory.Message, info map[string]any) error {
	if c.Disabled() || len(messages) == 0 || userID == "" {
		return nil
	}
	var lastErr error
	backoff := 100 * time.Millisecond
	for attempt := 0; attempt <= 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 3 // 100ms → 300ms
		}
		lastErr = c.addMessageOnce(ctx, userID, messages, info)
		if lastErr == nil || !isRetryable(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

func (c *Client) addMessageOnce(ctx context.Context, userID string, messages []memory.Message, info map[string]any) error {
	if info == nil {
		info = map[string]any{}
	}
	payload := map[string]any{
		"user_id":  userID,
		"messages": messages,
		"info":     info,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, c.addTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		c.baseURL+c.addPath, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	// memora 的 project scope 是 fail-closed 的：缺 project_id 直接 400
	// （memora/internal/memory/handler.go:335-338）。人工抽取路径把
	// project_id 放在 info 里（admin/session_extract.go:196-202），所以这里
	// 优先用 info 里的 project_id，其次用 client 级默认值。
	c.setProjectHeader(req, projectIDFromInfo(info, c.projectID))
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	body, bodyErr := httputil.ReadPrefixAndDrain(resp.Body, 512)
	if bodyErr != nil {
		return bodyErr
	}
	if resp.StatusCode >= 400 {
		return &httpError{status: resp.StatusCode, body: string(body)}
	}
	return nil
}

// httpError captures a Memora HTTP error for retry classification.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("memos status=%d body=%s", e.status, e.body)
}

// isRetryable returns true for errors worth retrying: network/transport
// errors and server-side 5xx responses. Client 4xx errors are not retried.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if he, ok := err.(*httpError); ok {
		return he.status >= 500
	}
	// Transport errors (connection refused, reset, timeout) are retryable.
	return true
}

// Search queries Memora for the top-K facts most relevant to query.
// Returns an empty slice (not an error) on 0 results or non-2xx.
func (c *Client) Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	return c.searchWithTimeout(ctx, userID, query, topK, c.searchTimeout)
}

// SmartSearch is the M1 (2026-06-19) high-recall retrieval entry point.
//
// As of 2026-06-20, Memora's Dashboard /api/smart_search route (see
// services/kxmemory/dashboard/backend/routes/smart_search.py) accepts
// an optional `user_id` query parameter and threads it through to a
// Qdrant Filter(must=[FieldCondition(key=user_id, match=MatchValue(...))]).
// This makes the 5-step RRF+MMR pipeline safe to use for per-tenant v3
// injection without the cross-tenant leak risk.
//
// Behavior:
//   - Tries POST {baseURL}/api/smart_search with body {query, user_id,
//     top_k}. This routes through the Dashboard pipeline with the
//     user_id Qdrant filter applied.
//   - On ANY non-2xx or transport error, falls back to the legacy
//     single-vector searchWithTimeout (MemOS /product/search) which is
//     also user-scoped (MemOS user_id filter is applied server-side).
//   - This keeps the multi-tenant safety guarantee: no smart_search call
//     ever returns cross-tenant memories, regardless of whether the
//     user_id filter is applied.
//
// Backward compatible: callers (compressor.tryMemoraCompression) that
// pass a real user_id now get the higher-recall 5-step pipeline; the
// fallback path remains the safe user-scoped single-vector search.
func (c *Client) SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	if c.Disabled() || userID == "" {
		return c.searchWithTimeout(ctx, userID, query, topK, c.searchTimeout)
	}
	if topK <= 0 {
		topK = 8
	}
	payload := map[string]any{
		"user_id": userID,
		"query":   query,
		"top_k":   topK,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return c.searchWithTimeout(ctx, userID, query, topK, c.searchTimeout)
	}
	cctx, cancel := context.WithTimeout(ctx, c.searchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		c.smartSearchBaseURL+"/api/smart_search", &buf)
	if err != nil {
		return c.searchWithTimeout(ctx, userID, query, topK, c.searchTimeout)
	}
	req.Header.Set("Content-Type", "application/json")
	// Dashboard's /api/smart_search uses X-API-Key (NOT Authorization:
	// Bearer). Sending Bearer here returns 401 "Missing session or API Key".
	if c.smartSearchAPIKey != "" {
		req.Header.Set("X-API-Key", c.smartSearchAPIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Transport error → safe fallback.
		return c.searchWithTimeout(ctx, userID, query, topK, c.searchTimeout)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// Upstream rejected → safe fallback.
		return c.searchWithTimeout(ctx, userID, query, topK, c.searchTimeout)
	}
	var raw struct {
		Code     int    `json:"code"`
		Message  string `json:"message"`
		Reranked []struct {
			ID     string  `json:"id"`
			Memory string  `json:"memory"`
			Score  float64 `json:"score"`
		} `json:"reranked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return c.searchWithTimeout(ctx, userID, query, topK, c.searchTimeout)
	}
	if len(raw.Reranked) == 0 {
		// No hits from smart search; try the legacy path so the caller
		// still gets SOMETHING (better than giving up entirely).
		return c.searchWithTimeout(ctx, userID, query, topK, c.searchTimeout)
	}
	out := make([]memory.Memory, 0, len(raw.Reranked))
	for _, h := range raw.Reranked {
		if h.Memory == "" {
			continue
		}
		out = append(out, memory.Memory{
			ID:    h.ID,
			Text:  h.Memory,
			Score: h.Score,
		})
	}
	if len(out) == 0 {
		return c.searchWithTimeout(ctx, userID, query, topK, c.searchTimeout)
	}
	return out, nil
}

// SearchAdmin is like Search but uses a longer timeout for admin UI reads.
func (c *Client) SearchAdmin(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	timeout := 8 * time.Second
	if c != nil && c.searchTimeout > timeout {
		timeout = c.searchTimeout
	}
	return c.searchWithTimeout(ctx, userID, query, topK, timeout)
}

func (c *Client) searchWithTimeout(ctx context.Context, userID, query string, topK int, timeout time.Duration) ([]memory.Memory, error) {
	if c.Disabled() || userID == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 8
	}
	payload := map[string]any{
		"user_id":             userID,
		"query":               query,
		"top_k":               topK,
		"memory_limit_number": topK,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		c.baseURL+c.searchPath, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	c.setProjectHeader(req, c.projectID)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	responseBody, bodyErr := httputil.ReadPrefixAndDrain(resp.Body, 4<<20)
	if bodyErr != nil {
		return nil, bodyErr
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("memos search status=%d", resp.StatusCode)
	}
	// Two response shapes must be understood (2026-10-20, audit D1):
	//
	//   - memora: c.JSON(200, gin.H{"query":…, "results":[…], "count": n})
	//     (memora/internal/memory/handler.go:231; item shape is
	//     memora/internal/memory/service.go:420-437 → id/content/tags/score).
	//   - MemOS:  {"code":0,"data":{"text_mem":[{"cube_id":…,"memories":[…]}]}}
	//     (the historical /product/search shape).
	//
	// Decoding only the MemOS shape would make the fixed route return
	// **zero results against memora** — the path fix would look like it
	// worked while silently returning nothing.
	var raw struct {
		Query string `json:"query"`
		// memora shape
		Results []struct {
			ID        string   `json:"id"`
			Content   string   `json:"content"`
			Tags      []string `json:"tags"`
			MemCubeID *string  `json:"mem_cube_id"`
			Score     *float64 `json:"score"`
		} `json:"results"`
		Count int `json:"count"`
		// MemOS shape
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TextMem []struct {
				CubeID   string `json:"cube_id"`
				Memories []struct {
					ID         string   `json:"id"`
					Memory     string   `json:"memory"`
					Background string   `json:"background,omitempty"`
					Key        string   `json:"key,omitempty"`
					Tags       []string `json:"tags,omitempty"`
					Score      float64  `json:"score,omitempty"`
					Relativity float64  `json:"relativity,omitempty"`
				} `json:"memories"`
			} `json:"text_mem"`
		} `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &raw); err != nil {
		return nil, err
	}
	out := make([]memory.Memory, 0, topK)
	for _, r := range raw.Results {
		txt := r.Content
		if txt == "" {
			continue
		}
		score := 0.0
		if r.Score != nil {
			score = *r.Score
		}
		cubeID := ""
		if r.MemCubeID != nil {
			cubeID = *r.MemCubeID
		}
		out = append(out, memory.Memory{
			ID:     r.ID,
			Text:   txt,
			Tags:   r.Tags,
			Score:  score,
			CubeID: cubeID,
		})
	}
	if len(out) > 0 {
		return out, nil
	}
	// MemOS shape fallback.
	for _, cube := range raw.Data.TextMem {
		for _, m := range cube.Memories {
			txt := m.Memory
			if txt == "" {
				txt = m.Key
			}
			if txt == "" {
				continue
			}
			if m.Background != "" && m.Background != txt {
				txt = m.Background + "\n→ " + txt
			}
			score := m.Relativity
			if score == 0 {
				score = m.Score
			}
			out = append(out, memory.Memory{
				ID:     m.ID,
				Text:   txt,
				Tags:   m.Tags,
				Score:  score,
				CubeID: cube.CubeID,
			})
		}
	}
	return out, nil
}

// setProjectHeader sets memora's X-Project-ID header when a project is known.
// Empty = header omitted (memora then answers 400 "project_id is required").
func (c *Client) setProjectHeader(req *http.Request, projectID string) {
	if req == nil || projectID == "" {
		return
	}
	req.Header.Set("X-Project-ID", projectID)
}

// projectIDFromInfo extracts the per-call project id carried in the info map
// (the manual extraction path puts it there) and falls back to the client
// level default.
func projectIDFromInfo(info map[string]any, fallback string) string {
	if info != nil {
		if raw, ok := info["project_id"]; ok {
			if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return fallback
}
