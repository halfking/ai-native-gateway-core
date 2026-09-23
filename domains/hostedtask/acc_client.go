// hostedtask/acc_client.go — ACC Runtime Control 客户端（§2.1/§6.1）。
//
// 契约（均经设计文档跨仓库源码核实）：
//   - POST /api/v2/runtime/dispatch + Idempotency-Key → 202 {command_id,…}
//   - GET  /api/v2/runtime/commands/:command_id（轮询，状态权威——R65 D7）
//   - POST /api/v2/runtime/commands/:command_id/cancel（2xx=requested）
//
// 鉴权：Bearer service token（须含 sub+tenant_id claim；sk-svc.* 不通用）。
// env：LLM_GATEWAY_ACC_BASE_URL / LLM_GATEWAY_ACC_SERVICE_TOKEN。
// http.Client 注入便于 httptest（矩阵 C/E）。ACC 响应字段做宽容解析：
// 执行真相在 ACC，但 P0 不同 ACC 版本的字段命名差异由这里统一吸收。
package hostedtask

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ACCClient 配置。
const (
	accOperation = "run"                     // dispatch operation（payload.kind=pi 驱动 companion 执行器）
	accSourceRef = "llm-gateway/hosted-task" // §3.1 ②
)

// DispatchPayload 是 dispatch payload.kind=pi 的字段集（§2.1）。
type DispatchPayload struct {
	Kind      string `json:"kind"` // "pi"
	Prompt    string `json:"prompt"`
	Cwd       string `json:"cwd,omitempty"`
	TimeoutMs int64  `json:"timeout_ms,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`
}

// DispatchRequest 是一次 Runtime Control dispatch 的完整请求体。
type DispatchRequest struct {
	RuntimeID     string          `json:"runtime_id"`
	TaskID        string          `json:"task_id"`
	Operation     string          `json:"operation"`
	Payload       DispatchPayload `json:"payload"`
	SourceRef     string          `json:"source_ref"`
	CorrelationID string          `json:"correlation_id"`
}

// DispatchResult 是 202 响应的宽容解析结果。
type DispatchResult struct {
	CommandID string
	RunID     string
}

// ACCCommand 是 GET command 的宽容解析结果。
type ACCCommand struct {
	CommandID  string
	TaskID     string
	RunID      string
	Status     string // 原样（小写化）
	ResultRaw  map[string]any
	StopReason string // result.stop_reason / stopReason，空=未提供
}

// Done 报告 ACC 侧 command 是否已到达终态。
func (c ACCCommand) Done() bool {
	switch strings.ToLower(c.Status) {
	case "completed", "complete", "succeeded", "success", "failed", "error",
		"cancelled", "canceled", "expired", "timed_out", "timeout":
		return true
	}
	return false
}

// StopIsError 报告 pi 终局 stop_reason=error（§0-F4：pi exit 0 + stopReason=
// error 会被 facade 标成功，网关终态判定必须强制校验）。
func (c ACCCommand) StopIsError() bool {
	return strings.EqualFold(strings.TrimSpace(c.StopReason), "error")
}

// ACCClient 是 Runtime Control 的 HTTP 客户端。
type ACCClient struct {
	baseURL   string
	token     string
	runtimeID string
	http      *http.Client
}

// NewACCClient 构造客户端。httpClient 为 nil 时用 30s 超时默认。
// baseURL/runtimeID 为空时返回的客户端仍可用（调用方应以 Configured() 判定）。
func NewACCClient(baseURL, token, runtimeID string, httpClient *http.Client) *ACCClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &ACCClient{baseURL: baseURL, token: token, runtimeID: runtimeID, http: httpClient}
}

// Configured 报告 ACC 是否已配置（base+token 齐备）。未配置时 dispatch 走
// dispatch_degraded 事件（§8 A 组跨仓库门禁缺失 → SKIPPED-CONFIG 语义）。
func (c *ACCClient) Configured() bool {
	return c != nil && c.baseURL != "" && c.token != ""
}

// RuntimeID 返回目标 runtime（companion 注册的 agent_inventory 条目）。
func (c *ACCClient) RuntimeID() string {
	if c == nil {
		return ""
	}
	return c.runtimeID
}

func (c *ACCClient) do(ctx context.Context, method, path string, body any, idempotencyKey string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func decodeErr(resp *http.Response, action string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("acc %s: status %d: %s", action, resp.StatusCode, strings.TrimSpace(string(body)))
}

// Dispatch 提交一次执行派发（§3.1 ②）。幂等键由调用方派生
// （gw-hosted-<id>-a1）：网络失败重放同键，ACC 保证不双发。
func (c *ACCClient) Dispatch(ctx context.Context, req DispatchRequest, idempotencyKey string) (DispatchResult, error) {
	var out DispatchResult
	if !c.Configured() {
		return out, fmt.Errorf("acc dispatch: client not configured")
	}
	if req.Operation == "" {
		req.Operation = accOperation
	}
	if req.SourceRef == "" {
		req.SourceRef = accSourceRef
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/v2/runtime/dispatch", req, idempotencyKey)
	if err != nil {
		return out, fmt.Errorf("acc dispatch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, decodeErr(resp, "dispatch")
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return out, fmt.Errorf("acc dispatch: decode: %w", err)
	}
	out.CommandID = accString(body, "command_id", "commandId", "id")
	out.RunID = accString(body, "run_id", "runId")
	// 某些版本把 command 包在 data 里。
	if data, ok := body["data"].(map[string]any); ok {
		if out.CommandID == "" {
			out.CommandID = accString(data, "command_id", "commandId", "id")
		}
		if out.RunID == "" {
			out.RunID = accString(data, "run_id", "runId")
		}
	}
	if out.CommandID == "" {
		return out, fmt.Errorf("acc dispatch: 2xx without command_id")
	}
	return out, nil
}

// GetCommand 轮询兜底：GET /api/v2/runtime/commands/:id。
func (c *ACCClient) GetCommand(ctx context.Context, commandID string) (ACCCommand, error) {
	var out ACCCommand
	if !c.Configured() {
		return out, fmt.Errorf("acc getCommand: client not configured")
	}
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/runtime/commands/"+url.PathEscape(commandID), nil, "")
	if err != nil {
		return out, fmt.Errorf("acc getCommand: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, decodeErr(resp, "getCommand")
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return out, fmt.Errorf("acc getCommand: decode: %w", err)
	}
	if data, ok := body["data"].(map[string]any); ok {
		body = data
	}
	out.CommandID = accString(body, "command_id", "commandId", "id")
	out.TaskID = accString(body, "task_id", "taskId")
	out.RunID = accString(body, "run_id", "runId")
	out.Status = strings.ToLower(accString(body, "status", "state"))
	if res, ok := body["result"].(map[string]any); ok {
		out.ResultRaw = res
		out.StopReason = accString(res, "stop_reason", "stopReason")
	}
	if out.StopReason == "" {
		out.StopReason = accString(body, "stop_reason", "stopReason")
	}
	return out, nil
}

// Cancel 请求取消（§3.2/§0-F4：2xx 仅=requested，回执链 delivered/effective
// 由 reconciler 跟踪；P0 语义明确“取消=请求受理”）。
func (c *ACCClient) Cancel(ctx context.Context, commandID string) error {
	if !c.Configured() {
		return fmt.Errorf("acc cancel: client not configured")
	}
	resp, err := c.do(ctx, http.MethodPost,
		"/api/v2/runtime/commands/"+url.PathEscape(commandID)+"/cancel", map[string]any{}, "")
	if err != nil {
		return fmt.Errorf("acc cancel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeErr(resp, "cancel")
	}
	return nil
}

// accString 从宽容解析的 map 里按优先级取第一个非空字符串。
func accString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
		if v, ok := m[k].(float64); ok {
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}
