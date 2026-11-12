// Package memora 是提示词优化模块的**真实 Memora** 接入层。
//
// 与仓内既有的 domains/memory/client 的分工：
//
//	domains/memory/client —— 网关热路径用的 MemOS 兼容客户端，
//	                          走 /api/add、/api/search（legacy 路由，V1Auth）。
//	memora（本包）        —— 提示词优化模块用的 memora **v2 fail-closed** 客户端，
//	                          走 /api/v2/memories/{ingest,search} 与
//	                          /api/v2/session-analysis（service JWT + project scope）。
//
// 为什么不用 legacy 路由：memora 的 legacy 路由挂在 V1Auth 下，
// 明确拒绝 service token（错误码 service_token_wrong_route）。
// 提示词优化服务是一个**后台服务**，没有用户会话，只能用 service JWT，
// 因此必须走 v2 fail-closed 组。
//
// 认证与授权模型（实测自 ai-platform/memora v2 fail-closed 组）：
//
//	service JWT = HS256(secret, {iss, aud, sub, tenant_id, actor_id, actor_type, scope, exp})
//	  - iss/aud 必须与 memora 的 MEMORA_V2_JWT_ISSUER / MEMORA_V2_JWT_AUDIENCE 一致；
//	  - actor_id 是**授权主体**：memora 的 project ACL 判的是 actor_id，不是 sub；
//	  - scope 必须覆盖 memory.read / memory.write。
//	project scope = X-Project-ID 请求头 + scope_chain.project_id 请求体字段。
//	  - project 必须存在且 actor_id 必须是该 project 的 owner 或在 project_acl 中有授权。
//
// 三个写/读端点各自还要求幂等与关联头，缺一个就 422：
//
//	POST /api/v2/memories/ingest       → Idempotency-Key + X-Correlation-ID
//	POST /api/v2/memories/search       → （无额外头）
//	POST /api/v2/session-analysis      → Idempotency-Key(<dispatch_id>-<analysis_kind>) + X-Correlation-ID
//
// 本包不引入任何第三方依赖：HTTP 用 net/http，JWT 用 crypto/hmac + crypto/sha256。
package memora

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
)

// 默认值，与 memora 容器的 MEMORA_V2_JWT_ISSUER / MEMORA_V2_JWT_AUDIENCE 对齐。
const (
	DefaultIssuer   = "memora"
	DefaultAudience = "memora-api"
	DefaultTenantID = "default"
)

// defaultScope 是本客户端申请的 scope。
//
// memory.read 用于检索历史，memory.write 用于写入优化产出与会话分析。
const defaultScope = "memory.read memory.write knowledge.read knowledge.write"

// Config 客户端配置。
type Config struct {
	// BaseURL memora 服务根地址，例如 http://127.0.0.1:12113。
	BaseURL string

	// JWTSecret HS256 共享密钥。**必填**：没有它无法签发 service JWT。
	JWTSecret string

	// Issuer 签发方，默认 memora。
	Issuer string

	// Audience 受众，默认 memora-api。
	Audience string

	// TenantID 租户 ID，默认 default。
	TenantID string

	// ActorID 授权主体（ACL 判定对象）。默认 local-ops —— 与 memora
	// 本地栈 localauth.MintServiceJWT 的 actor 一致。
	ActorID string

	// ProjectID 项目作用域。**必填**：memora 的 project scope 是 fail-closed 的。
	ProjectID string

	// Timeout 单次请求超时。
	Timeout time.Duration

	// HTTPClient 可选，自定义 transport。
	HTTPClient *http.Client
}

// Client memora v2 客户端。
//
// 零值不可用，必须经 New 构造；New 失败时调用方应退化为"无记忆"模式。
type Client struct {
	baseURL    string
	secret     string
	issuer     string
	audience   string
	tenantID   string
	actorID    string
	projectID  string
	httpClient *http.Client

	tokenMu  sync.Mutex
	token    string
	tokenExp time.Time
}

// New 构造客户端。
//
// 校验失败的返回错误（而不是"构造一个坏客户端"）：
// 缺 BaseURL / JWTSecret / ProjectID 三者任一都无法完成真实调用，
// 静默降级只会让调用方在运行期才发现"记忆没接上"。
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return nil, errors.New("memora: base url must not be empty")
	}
	if strings.TrimSpace(cfg.JWTSecret) == "" {
		return nil, errors.New("memora: jwt secret must not be empty")
	}
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return nil, errors.New("memora: project id must not be empty")
	}

	issuer := strings.TrimSpace(cfg.Issuer)
	if issuer == "" {
		issuer = DefaultIssuer
	}
	audience := strings.TrimSpace(cfg.Audience)
	if audience == "" {
		audience = DefaultAudience
	}
	tenant := strings.TrimSpace(cfg.TenantID)
	if tenant == "" {
		tenant = DefaultTenantID
	}
	actor := strings.TrimSpace(cfg.ActorID)
	if actor == "" {
		actor = "local-ops"
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	return &Client{
		baseURL:    base,
		secret:     cfg.JWTSecret,
		issuer:     issuer,
		audience:   audience,
		tenantID:   tenant,
		actorID:    actor,
		projectID:  strings.TrimSpace(cfg.ProjectID),
		httpClient: httpClient,
	}, nil
}

// Disabled 实现 memory.Reader。
//
// 本客户端一旦构造成功就是可用的；该方法存在的意义是满足 memory.Reader 接口，
// 让它能直接喂给 context.Manager。
func (c *Client) Disabled() bool { return c == nil }

// ProjectID 返回项目作用域。
func (c *Client) ProjectID() string { return c.projectID }

// ActorID 返回授权主体。
func (c *Client) ActorID() string { return c.actorID }

// Search 实现 memory.Reader：检索历史记忆。
func (c *Client) Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	if c == nil {
		return nil, errors.New("memora: client is nil")
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}

	body := map[string]any{
		"query": query,
		"top_k": topK,
		"scope_chain": map[string]any{
			"tenant_id":  c.tenantID,
			"project_id": c.projectID,
		},
	}

	var resp searchResponse
	if err := c.do(ctx, http.MethodPost, "/api/v2/memories/search", body, nil, &resp); err != nil {
		return nil, fmt.Errorf("memora: search failed: %w", err)
	}

	items := resp.Data.Items
	out := make([]memory.Memory, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Snippet)
		if text == "" {
			continue
		}
		out = append(out, memory.Memory{
			ID:     item.MemoryID,
			Text:   text,
			Tags:   item.Metadata.Tags,
			Score:  item.Score,
			CubeID: item.Scope,
		})
	}
	return out, nil
}

// SmartSearch 实现 memory.Reader。
//
// memora 的 v2 检索已经是统一混合检索面，没有单独的 smart 通道，
// 因此与 Search 同实现；保留两个方法是为了满足 memory.Reader 接口。
func (c *Client) SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	return c.Search(ctx, userID, query, topK)
}

// searchResponse 是 POST /api/v2/memories/search 的响应。
type searchResponse struct {
	Data struct {
		Degraded bool         `json:"degraded"`
		Items    []searchItem `json:"items"`
	} `json:"data"`
	RequestID string `json:"request_id"`
}

// searchItem 是检索命中的一条记忆。
type searchItem struct {
	Source         string  `json:"source"`
	MemoryID       string  `json:"memory_id"`
	Scope          string  `json:"scope"`
	Score          float64 `json:"score"`
	TokenCount     int     `json:"token_count"`
	Snippet        string  `json:"snippet"`
	PolicyDecision string  `json:"policy_decision"`
	Metadata       struct {
		SourceSystem string   `json:"source_system"`
		SourceType   string   `json:"source_type"`
		ContentRef   string   `json:"content_ref"`
		Tags         []string `json:"tags"`
	} `json:"metadata"`
}

// IngestItem 一条待写入的记忆。
type IngestItem struct {
	// IdempotencyKey 幂等键。空则由 ContentRef 推导。
	IdempotencyKey string
	// SourceType 取值受 memora 约束：email|note|meeting|session_summary|artifact。
	SourceType string
	// SourceID 来源资源 ID，必填。
	SourceID string
	// ContentRef 内容引用（路径/URL），可选。
	ContentRef string
	// ContentPreview 内容预览，**检索命中返回的就是它**，所以要写足信息。
	ContentPreview string
	// Tags 标签。
	Tags []string
	// Metadata 附加元数据。
	Metadata map[string]any
	// RunID / TaskID / AgentSessionID 写入 scope_chain 的谱系字段。
	RunID          string
	TaskID         string
	AgentSessionID string
}

// IngestResult 是 ingest 的结果。
type IngestResult struct {
	Accepted      int      `json:"accepted"`
	Degraded      int      `json:"degraded"`
	Duplicates    int      `json:"duplicates"`
	Failed        []string `json:"failed"`
	RequestID     string   `json:"request_id"`
	CorrelationID string   `json:"correlation_id"`
}

// Ingest 批量写入记忆。
//
// sourceSystem 必须是 memora 允许的值：pocket|redclaw|acc|gateway。
func (c *Client) Ingest(ctx context.Context, sourceSystem string, items []IngestItem) (IngestResult, error) {
	if c == nil {
		return IngestResult{}, errors.New("memora: client is nil")
	}
	if len(items) == 0 {
		return IngestResult{}, errors.New("memora: ingest items must not be empty")
	}
	if strings.TrimSpace(sourceSystem) == "" {
		return IngestResult{}, errors.New("memora: source system must not be empty")
	}

	now := time.Now().UTC()
	batchID := fmt.Sprintf("po-%d", now.UnixNano())
	corrID := fmt.Sprintf("po-corr-%d", now.UnixNano())

	payloadItems := make([]map[string]any, 0, len(items))
	for i, item := range items {
		key := strings.TrimSpace(item.IdempotencyKey)
		if key == "" {
			key = fmt.Sprintf("%s-%d", batchID, i)
		}
		sourceType := strings.TrimSpace(item.SourceType)
		if sourceType == "" {
			sourceType = "note"
		}
		payloadItems = append(payloadItems, map[string]any{
			"idempotency_key": key,
			"source_type":     sourceType,
			"source_id":       item.SourceID,
			"content_ref":     item.ContentRef,
			"content_preview": item.ContentPreview,
			"scope_chain": map[string]any{
				"project_id":       c.projectID,
				"run_id":           item.RunID,
				"task_id":          item.TaskID,
				"agent_session_id": item.AgentSessionID,
			},
			"tags":     item.Tags,
			"metadata": item.Metadata,
		})
	}

	body := map[string]any{
		"source_system": sourceSystem,
		"batch_id":      batchID,
		"items":         payloadItems,
	}

	headers := map[string]string{
		"Idempotency-Key":  batchID,
		"X-Correlation-ID": corrID,
	}

	var resp struct {
		Data          IngestResult `json:"data"`
		RequestID     string       `json:"request_id"`
		CorrelationID string       `json:"correlation_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v2/memories/ingest", body, headers, &resp); err != nil {
		return IngestResult{}, fmt.Errorf("memora: ingest failed: %w", err)
	}
	resp.Data.RequestID = resp.RequestID
	resp.Data.CorrelationID = resp.CorrelationID
	return resp.Data, nil
}

// SessionAnalysisPayload 是会话分析载荷。
//
// 字段与 memora 的 sessionanalysis.AnalysisPayload 同构（只取优化模块用得到的子集）。
type SessionAnalysisPayload struct {
	// AnalysisKind 分析种类，必填。幂等键 = <dispatch_id>-<analysis_kind>。
	AnalysisKind string `json:"analysis_kind"`
	// DispatchID 网关侧 dispatch 标识，幂等键的 `<dispatch_id>` 部分。
	DispatchID string `json:"dispatch_id,omitempty"`
	// SourceSessionID 来源会话 ID。
	SourceSessionID string `json:"source_session_id,omitempty"`
	// Title 会话标题。
	Title string `json:"title,omitempty"`
	// Summary 结论性摘要（不是 transcript）。
	Summary string `json:"summary,omitempty"`
	// Intent 用户意图分类。
	Intent string `json:"intent,omitempty"`
	// Outcome 终态结论。
	Outcome string `json:"outcome,omitempty"`
	// KeyPoints 关键要点。
	KeyPoints []string `json:"key_points,omitempty"`
	// OpenQuestions 尚未有答案的问题。
	OpenQuestions []string `json:"open_questions,omitempty"`
	//
	// 注意：memora 的 sessionanalysis.AnalysisPayload.tags 是结构体数组
	// （{key,value,source,confidence}），不是字符串数组 ——
	// 传字符串数组会被判 invalid_json（422）。因此本包**不暴露**该字段。
}

// SessionAnalysisResult 是会话分析的结果。
type SessionAnalysisResult struct {
	ExperienceMemoryID string `json:"experience_memory_id"`
	FactMemoryID       string `json:"fact_memory_id"`
	ManifestID         string `json:"manifest_id"`
	ManifestVersion    int    `json:"manifest_version"`
	IdempotencyKey     string `json:"idempotency_key"`
	VectorState        string `json:"vector_state"`
	RequestID          string `json:"request_id"`
	CorrelationID      string `json:"correlation_id"`
}

// PostSessionAnalysis 投递会话分析。
//
// 这是"历史会话信息"进入记忆库的入口：投递后 memora 会产出
// fact_memory / experience_memory / context manifest，随后可被 Search 检索到。
//
// 幂等键形如 <dispatch_id>-<analysis_kind>，缺后缀 memora 会拒（422）。
func (c *Client) PostSessionAnalysis(ctx context.Context, payload SessionAnalysisPayload) (SessionAnalysisResult, error) {
	if c == nil {
		return SessionAnalysisResult{}, errors.New("memora: client is nil")
	}
	kind := strings.TrimSpace(payload.AnalysisKind)
	if kind == "" {
		return SessionAnalysisResult{}, errors.New("memora: analysis kind must not be empty")
	}
	dispatch := strings.TrimSpace(payload.DispatchID)
	if dispatch == "" {
		dispatch = fmt.Sprintf("po-dispatch-%d", time.Now().UTC().UnixNano())
	}
	corrID := fmt.Sprintf("po-corr-%d", time.Now().UTC().UnixNano())

	body := map[string]any{
		"project_id":    c.projectID,
		"analysis_kind": kind,
		"dispatch_id":   dispatch,
	}
	if payload.SourceSessionID != "" {
		body["source_session_id"] = payload.SourceSessionID
	}
	if payload.Title != "" {
		body["title"] = payload.Title
	}
	if payload.Summary != "" {
		body["summary"] = payload.Summary
	}
	if payload.Intent != "" {
		body["intent"] = payload.Intent
	}
	if payload.Outcome != "" {
		body["outcome"] = payload.Outcome
	}
	if len(payload.KeyPoints) > 0 {
		body["key_points"] = payload.KeyPoints
	}
	if len(payload.OpenQuestions) > 0 {
		body["open_questions"] = payload.OpenQuestions
	}

	headers := map[string]string{
		"Idempotency-Key":  dispatch + "-" + kind,
		"X-Correlation-ID": corrID,
	}

	var resp struct {
		Data          SessionAnalysisResult `json:"data"`
		RequestID     string                `json:"request_id"`
		CorrelationID string                `json:"correlation_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v2/session-analysis", body, headers, &resp); err != nil {
		return SessionAnalysisResult{}, fmt.Errorf("memora: session analysis failed: %w", err)
	}
	resp.Data.RequestID = resp.RequestID
	resp.Data.CorrelationID = resp.CorrelationID
	return resp.Data, nil
}

// do 发一次请求并解析响应。
//
// headers 为 nil 时不带额外头；out 非 nil 时把响应体解码进 out。
func (c *Client) do(ctx context.Context, method, path string, body any, headers map[string]string, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-ID", c.projectID)

	token, err := c.serviceToken(time.Now().UTC())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			StatusCode: resp.StatusCode,
			Path:       path,
			Body:       truncate(string(raw), 500),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w (body: %s)", err, truncate(string(raw), 300))
	}
	return nil
}

// APIError 是 memora 返回的非 2xx 响应。
type APIError struct {
	StatusCode int
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("memora: %s returned %d: %s", e.Path, e.StatusCode, e.Body)
}

// serviceToken 签发 service JWT，并在有效期内缓存。
func (c *Client) serviceToken(now time.Time) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	// 提前 60s 过期，避免请求途中失效。
	if c.token != "" && now.Add(60*time.Second).Before(c.tokenExp) {
		return c.token, nil
	}

	exp := now.Add(2 * time.Hour)
	payload := map[string]any{
		"iss":        c.issuer,
		"aud":        c.audience,
		"sub":        "service:local-auth:" + c.actorID,
		"tenant_id":  c.tenantID,
		"actor_id":   c.actorID,
		"actor_type": "service",
		"scope":      defaultScope,
		"iat":        now.Unix(),
		"exp":        exp.Unix(),
	}

	token, err := signHS256(c.secret, payload)
	if err != nil {
		return "", fmt.Errorf("memora: sign service token: %w", err)
	}
	c.token = token
	c.tokenExp = exp
	return token, nil
}

// signHS256 用 HS256 签一个 JWT。
//
// 与 memora 的 third_party/identity-go/token/sign.go 同构：
// header={"alg":"HS256","typ":"JWT"}，payload 为 JSON，签名覆盖 "header.payload"。
func signHS256(secret string, payload map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(body)

	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(signingInput)); err != nil {
		return "", err
	}
	return signingInput + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

// truncate 截断长字符串，便于放进错误信息。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
