package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  cc.Method,
		Params:  cc.Params,
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
	endpoint := strings.TrimRight(cc.Adapter.Endpoint, "/") + "/mcp"
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
			req.Header.Set("X-MCP-Capability-Tools", strings.Join(tools, ","))
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
