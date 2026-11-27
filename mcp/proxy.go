package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ProtocolVersion 是网关对外声明的 MCP 协议版本。
const ProtocolVersion = "2026-07-28"

// jsonrpcRequest 是 JSON-RPC 2.0 请求体。
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse 是 JSON-RPC 2.0 响应体。
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// UpstreamClient 负责把 JSON-RPC 请求转发到上游 MCP 适配器。
type UpstreamClient struct {
	http *http.Client
	// jwtIssuer 为 auth_mode=llm_gateway 的适配器签发出站 service JWT
	// （ADR 2026-11-27 §4 D2a）。nil ⇒ 该模式不可用（装配时必须显式给）。
	jwtIssuer *ServiceJWTIssuer
}

// NewUpstreamClient 构造上游客户端。timeout 为单次请求超时上限。
func NewUpstreamClient(timeout time.Duration) *UpstreamClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &UpstreamClient{
		http: &http.Client{Timeout: timeout},
	}
}

// SetServiceJWTIssuer 注入 llm_gateway 模式的出站签发器（装配面调用）。
func (c *UpstreamClient) SetServiceJWTIssuer(i *ServiceJWTIssuer) {
	c.jwtIssuer = i
}

// CallContext 是一次上游调用的上下文参数。
type CallContext struct {
	// Adapter 目标适配器。
	Adapter Adapter
	// Method JSON-RPC 方法名。
	Method string
	// Params 调用参数（JSON 编码后透传）。
	Params json.RawMessage
	// ToolName 目标工具名（用于 Mcp-Name 头与能力路由）。
	ToolName string
	// Capability 能力域（能力路由头）。
	Capability string
	// ProtocolVersion 协议版本头。
	ProtocolVersion string
	// ExtraHeaders 附加头。
	ExtraHeaders map[string]string
	// Timeout 单次调用超时（覆盖客户端默认）。
	Timeout time.Duration
	// Retries 重试次数（仅对幂等方法有意义）。
	Retries int
	// WriteGranted 调用方 key 具备显式 write 授权（仅 llm_gateway 模式消费：
	// true 才在出站 service JWT scope 里扩入 memory.write；ADR §4 默认不可达）。
	WriteGranted bool
	// KeyID 调用方 mcp key 的 ID（llm_gateway 模式透传 X-MCP-Gateway-Key-ID 归因）。
	KeyID string
	// CallID 网关侧调用关联键 ULID（llm_gateway 模式透传 X-MCP-Call-ID，
	// ADR §6 三本日志对账键）。
	CallID string
	// RequestID 是要透传给上游的 JSON-RPC id（E2E 实证：go-sdk 上游对
	// 带 method 无 id 的请求按 notification 语义拒绝——missing id）。
	// 空 ⇒ 序列化省略（保持既有对 stdio/notification 类上游的宽容）。
	RequestID json.RawMessage
}

// injectProtocolMeta 把 MCP 协议版本注入 params._meta（go-sdk 上游按
// 2026-06 规范要求请求级 `_meta["io.modelcontextprotocol/protocolVersion"]`；
// E2E 实证缺失即 -32602）。params 为空/非对象时从零构造。
func injectProtocolMeta(params json.RawMessage, protocol string) json.RawMessage {
	if protocol == "" {
		protocol = ProtocolVersion
	}
	var m map[string]any
	if len(params) > 0 {
		if err := json.Unmarshal(params, &m); err != nil || m == nil {
			m = map[string]any{}
		}
	} else {
		m = map[string]any{}
	}
	meta, _ := m["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	if _, ok := meta["io.modelcontextprotocol/protocolVersion"]; !ok {
		meta["io.modelcontextprotocol/protocolVersion"] = protocol
	}
	// go-sdk 上游同时要求 clientCapabilities 声明（空对象即合法：
	// 网关是聚合代理，不代持客户端能力协商结果）。
	if _, ok := meta["io.modelcontextprotocol/clientCapabilities"]; !ok {
		meta["io.modelcontextprotocol/clientCapabilities"] = map[string]any{}
	}
	m["_meta"] = meta
	out, err := json.Marshal(m)
	if err != nil {
		return params
	}
	return out
}

// upstreamURLOf 由适配器基址推导上游 MCP 端点。
//
// 兼容两种注册形态（E2E 实证：memora registry 条目的 endpoint 常已带
// `/mcp` 路径，如 http://memora-server:9002/mcp——无条件追加会拼出
// `/mcp/mcp`）：末段已是 `mcp` 视为完整端点原样用；否则按基址约定补
// `/mcp`（既有行为，存量适配器不受影响）。
func upstreamURLOf(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return "/mcp"
	}
	if idx := strings.LastIndex(b, "/"); idx >= 0 && b[idx+1:] == "mcp" {
		return b
	}
	return b + "/mcp"
}

// reqIDSeq 是网关对上游请求的 JSON-RPC id 序列（进程内自增即可：
// 上游按 id 回配响应，跨进程唯一性无意义）。
var reqIDSeq atomic.Int64

// reqID 取下一个上游请求 id。
func reqID() int64 { return reqIDSeq.Add(1) }

// id2Raw 把 int64 id 编成 JSON number 形态的 RawMessage。
func id2Raw(v int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(v, 10))
}

// UpstreamResult 是上游返回结果。
type UpstreamResult struct {
	// Result 成功结果体。
	Result json.RawMessage
	// Status HTTP 状态码。
	Status int
	// Latency 耗时。
	Latency time.Duration
}

// Call 转发一次 JSON-RPC 调用到上游。
//
// 无状态路由约定：协议元数据随请求携带（`MCP-Protocol-Version` / `Mcp-Method` /
// `Mcp-Name` 头），网关不依赖 transport session 亲和。
func (c *UpstreamClient) Call(ctx context.Context, cc CallContext) (*UpstreamResult, error) {
	params := injectProtocolMeta(cc.Params, cc.ProtocolVersion)
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      cc.RequestID,
		Method:  cc.Method,
		Params:  params,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal upstream request: %w", err)
	}

	protocol := cc.ProtocolVersion
	if protocol == "" {
		protocol = ProtocolVersion
	}

	retries := cc.Retries
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		res, err := c.do(ctx, cc, body, protocol)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func (c *UpstreamClient) do(ctx context.Context, cc CallContext, body []byte, protocol string) (*UpstreamResult, error) {
	endpoint := upstreamURLOf(cc.Adapter.Endpoint)
	reqCtx := ctx
	if cc.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, cc.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp: build upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocol)
	if cc.Method != "" {
		req.Header.Set("Mcp-Method", cc.Method)
	}
	if cc.ToolName != "" {
		req.Header.Set("Mcp-Name", cc.ToolName)
	}
	if cc.Capability != "" {
		req.Header.Set("X-MCP-Capability", cc.Capability)
		if tools := CapabilityTools(cc.Capability); len(tools) > 0 {
			// ★ 值必须是 **JSON 数组字符串**（既有实现是 JSON.stringify([...])）：
			//   上游按 JSON.parse 解析这个头，逗号分隔会让解析失败。
			//   头名大小写由 Go 规范化（X-Mcp-Capability），HTTP 头名本身大小写不敏感
			//   （RFC 9110 §5.1），语义与既有实现一致。
			raw, err := json.Marshal(tools)
			if err != nil {
				raw = []byte("[]")
			}
			req.Header.Set("X-MCP-Capability-Tools", string(raw))
		}
	}
	for k, v := range cc.Adapter.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range cc.ExtraHeaders {
		req.Header.Set(k, v)
	}
	switch cc.Adapter.AuthMode {
	case "bearer":
		if cc.Adapter.AuthSecret != "" {
			req.Header.Set("Authorization", "Bearer "+cc.Adapter.AuthSecret)
		}
	case "apikey":
		if cc.Adapter.AuthSecret != "" {
			req.Header.Set("X-Api-Key", cc.Adapter.AuthSecret)
		}
	case AuthModeLLMGateway:
		// ADR 2026-11-27 §4 D2a：网关动态签短 service JWT（memora 入站唯一
		// 凭据形态），scope 恒只读最小集；write 扩入由 writeGranted 决定
		// （缺签发器 ⇒ 调用失败：该模式配置了就必须能签，静默降级=凭据黑洞）。
		if c.jwtIssuer == nil {
			return nil, fmt.Errorf("mcp: adapter %q auth_mode=%s but no service jwt issuer configured", cc.Adapter.Name, AuthModeLLMGateway)
		}
		token, err := c.jwtIssuer.Issue(cc.Adapter.TenantID, cc.WriteGranted)
		if err != nil {
			return nil, fmt.Errorf("mcp: issue service jwt for adapter %q: %w", cc.Adapter.Name, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		// 归因透传（ADR §4/§6）：tenant/账户维度只作归因记录，memora 鉴权
		// 与 RLS 钉的是 JWT claim 本身。
		if cc.Adapter.TenantID != "" {
			req.Header.Set("X-MCP-On-Behalf-Of", cc.Adapter.TenantID)
		}
		if cc.KeyID != "" {
			req.Header.Set("X-MCP-Gateway-Key-ID", cc.KeyID)
		}
		if cc.CallID != "" {
			req.Header.Set("X-MCP-Call-ID", cc.CallID)
		}
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: upstream call failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("mcp: read upstream body: %w", err)
	}
	latency := time.Since(start)

	if resp.StatusCode >= 500 {
		return nil, &UpstreamHTTPError{Status: resp.StatusCode, Body: truncate(string(raw), 512)}
	}
	if resp.StatusCode >= 400 {
		return nil, &UpstreamHTTPError{Status: resp.StatusCode, Body: truncate(string(raw), 512), Fatal: true}
	}

	payload := raw
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload = extractSSEData(raw)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, &UpstreamHTTPError{Status: resp.StatusCode, Body: "empty upstream response", Fatal: true}
	}

	var out jsonrpcResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("mcp: decode upstream response: %w", err)
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return &UpstreamResult{Result: out.Result, Status: resp.StatusCode, Latency: latency}, nil
}

// UpstreamHTTPError 表示上游返回了非 2xx 或非法响应。
type UpstreamHTTPError struct {
	Status int
	Body   string
	// Fatal 表示不可重试（4xx / 非法响应）。
	Fatal bool
}

// Error 实现 error 接口。
func (e *UpstreamHTTPError) Error() string {
	return fmt.Sprintf("mcp: upstream http %d: %s", e.Status, e.Body)
}

// extractSSEData 从 SSE 流中取出最后一个 data: 段（JSON-RPC 响应体）。
func extractSSEData(raw []byte) []byte {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var last []byte
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		last = []byte(data)
	}
	return last
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
