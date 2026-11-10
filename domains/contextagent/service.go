package contextagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LoopbackSubmitter 把装配好的请求体**回灌到网关自己的** chat 端点。
//
// ★ 为什么走回灌而不是在进程内直接调 executor：
//
//	· 回灌复用的是**同一条**在役链路（鉴权 → 路由 → 凭据选择 → 上游 → 计费），
//	  不需要在本包里复刻路由与凭据治理；
//	· 与仓内既有的跟进机制同构（domains/streaming/response_interceptor_helpers.go
//	  的 defaultDispatchFollowUp 也是造一个合成请求回灌），
//	  所以「记忆加载后重新问一次」这条路的失败模式是**已知**的。
//
// ★ 安全边界：回灌必须带**调用方自己的凭据**（AuthHeader）——
//
//	否则回灌请求会以网关内部身份记账，归属错乱。
type LoopbackSubmitter struct {
	// Endpoint 是网关自己的 chat 端点（完整 URL）。
	Endpoint string
	// AuthHeader 是回灌时使用的 Authorization 头（调用方的原始凭据）。
	AuthHeader string
	// Headers 是额外要带的头（如 X-Gw-Session-Id / X-Gw-Task-Id）。
	Headers map[string]string
	// Client 是 HTTP 客户端（nil ⇒ 默认）。
	Client *http.Client
	// Timeout 是单次回灌的超时（默认 120s）。
	Timeout time.Duration
}

// Submit 实现 Submitter。
func (s *LoopbackSubmitter) Submit(ctx context.Context, body []byte) ([]byte, error) {
	if s == nil || strings.TrimSpace(s.Endpoint) == "" {
		return nil, fmt.Errorf("contextagent: loopback endpoint is empty")
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("contextagent: build loopback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.AuthHeader != "" {
		req.Header.Set("Authorization", s.AuthHeader)
	}
	for k, v := range s.Headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contextagent: loopback submit: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("contextagent: read loopback response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("contextagent: loopback returned %d: %s", resp.StatusCode, clipBytes(string(respBody), 512))
	}
	return respBody, nil
}

// ForRequest 派生一个带调用方凭据与身份头的回灌实例。
//
// ★ 凭据必须逐请求带：回灌请求要以**调用方**的身份过鉴权与计费，
//
//	否则归属会挂到网关内部身份上（记账、审计、凭据治理全部错位）。
//
// ★ 身份头（会话 / 任务 / 项目）也一并带过去：回灌必须落在**同一个**
//
//	会话族里，否则网关侧会把它当成一条新会话 ⇒ 记忆层取不到东西。
func (s *LoopbackSubmitter) ForRequest(identity Identity, authHeader string) Submitter {
	if s == nil {
		return nil
	}
	c := *s
	c.AuthHeader = authHeader
	headers := map[string]string{}
	for k, v := range s.Headers {
		headers[k] = v
	}
	if identity.Session != "" {
		headers["X-Gw-Session-Id"] = identity.Session
	}
	if identity.Task != "" {
		headers["X-Gw-Task-Id"] = identity.Task
	}
	if identity.Project != "" {
		headers[HeaderProjectID] = identity.Project
	}
	c.Headers = headers
	return &c
}

// Service 是上下文智能体的 HTTP 门面。
//
// ★ 三个端点，正好对应用户描述的三件事：
//
//	POST /resolve —— 识别 + 计划 + 分层加载 + 装配
//	                （返回装配后的出站体，客户端拿它去提交）
//	POST /tool    —— 执行一次记忆工具调用
//	                （MCP 的本地记忆工具也走这条）
//	GET  /record/{request_id} —— 取回可还原记录
//	                （原样还原客户端请求 / 上游响应 / 最终响应）
type Service struct {
	Agent *Agent
	// Submitter 是「装配后提交上游」的能力（可为 nil ⇒ /resolve 只装配不提交）。
	Submitter Submitter
}

// MaxRequestBytes 是服务接受的请求体上限。
const MaxRequestBytes = 8 << 20

// ResolveRequest 是 /resolve 的入参。
type ResolveRequest struct {
	// Body 是客户端原始请求体（JSON 字符串；原样字节会被记录）。
	Body string `json:"body"`
	// Identity 允许调用方显式给出身份（缺省时从请求头推断）。
	Identity *Identity `json:"identity,omitempty"`
	// Protocol 覆盖客户端协议。
	Protocol string `json:"protocol,omitempty"`
	// ContextWindow 覆盖上下文窗口。
	ContextWindow int `json:"context_window,omitempty"`
	// Submit 为 true 时，装配后**继续提交上游**并跑完记忆工具回环。
	Submit bool `json:"submit,omitempty"`
}

// ResolveResponse 是 /resolve 的出参。
type ResolveResponse struct {
	RequestID string         `json:"request_id"`
	Identity  Identity       `json:"identity"`
	Plan      Plan           `json:"plan"`
	Layers    []LayerContent `json:"layers"`
	// Body 是装配后的出站体（Submit=false 时返回；Submit=true 时返回最终响应体）。
	Body []byte `json:"body,omitempty"`
	// ResponseBody 是最终响应体（仅 Submit=true 时有值）。
	ResponseBody []byte `json:"response_body,omitempty"`
	// Rounds 是上游交互轮数。
	Rounds int `json:"rounds"`
	// ServedToolCalls 是网关自己执行过的记忆工具调用。
	ServedToolCalls []ServedCall `json:"served_tool_calls,omitempty"`
	// RecordID 是可还原记录的 id（用 GET /record/{id} 取回）。
	RecordID string `json:"record_id,omitempty"`
	// Error 是非致命错误说明（记忆加载失败等）。
	Error string `json:"error,omitempty"`
}

// ToolRequest 是 /tool 的入参。
type ToolRequest struct {
	// Name 是记忆工具名（load_step_detail / search_memory）。
	Name string `json:"name"`
	// Arguments 是工具参数（JSON 对象）。
	Arguments map[string]any `json:"arguments,omitempty"`
	// Identity 是调用身份。
	Identity *Identity `json:"identity,omitempty"`
}

// ToolResponse 是 /tool 的出参。
type ToolResponse struct {
	ToolCallID string   `json:"tool_call_id,omitempty"`
	Name       string   `json:"name"`
	Content    string   `json:"content"`
	Loaded     []string `json:"loaded,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// RecordResponse 是 /record/{id} 的出参。
//
// ★ 这是「原样还原」的出口：三份字节都是 base64（JSON 无字节类型），
//
//	还原后与原始字节逐字节相等。
type RecordResponse struct {
	RequestID string         `json:"request_id"`
	CreatedAt string         `json:"created_at"`
	Identity  Identity       `json:"identity"`
	Plan      Plan           `json:"plan"`
	Layers    []LayerContent `json:"layers"`
	// OriginalRequestBodyBase64 是客户端原始请求体。
	OriginalRequestBodyBase64 string `json:"original_request_body_b64"`
	// OriginalResponseBodyBase64 是第一次上游响应体。
	OriginalResponseBodyBase64 string `json:"original_response_body_b64"`
	// FinalResponseBodyBase64 是最终返回客户端的响应体。
	FinalResponseBodyBase64 string `json:"final_response_body_b64"`
	// Rounds 是逐轮中间态。
	Rounds []Round `json:"rounds"`
	// ServedToolCalls 是被网关执行过的记忆工具调用。
	ServedToolCalls []ServedCall `json:"served_tool_calls,omitempty"`
}

// HandleResolve 处理 /resolve。
func (s *Service) HandleResolve(ctx context.Context, r *http.Request) (*ResolveResponse, int, error) {
	if s == nil || s.Agent == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("contextagent: agent is not configured")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("contextagent: read body: %w", err)
	}
	var req ResolveRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("contextagent: decode request: %w", err)
	}
	if strings.TrimSpace(req.Body) == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("contextagent: body is required")
	}
	clientBody := []byte(req.Body)

	id := Identity{}
	if req.Identity != nil {
		id = *req.Identity
	} else {
		id = Recognize(r, clientBody, IdentityFacts{
			TenantID:       r.Header.Get("X-Gw-Tenant-Id"),
			RequestID:      r.Header.Get("X-Request-Id"),
			ClientProtocol: req.Protocol,
		})
	}
	if req.Protocol != "" {
		id.Protocol = req.Protocol
	}
	if id.RequestID == "" {
		id.RequestID = "ctx-" + time.Now().UTC().Format("20060102T150405.000000000")
	}

	// ① 只装配（不提交）。
	agent := *s.Agent // 拷贝，避免污染共享实例的开关
	agent.InjectTools = false
	out, err := agent.Process(ctx, Request{
		RequestID:       id.RequestID,
		Identity:        id,
		Body:            clientBody,
		Protocol:        id.Protocol,
		ContextWindow:   req.ContextWindow,
		DisableToolLoop: true,
	})
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	resp := &ResolveResponse{
		RequestID: id.RequestID,
		Identity:  id,
		Plan:      out.Record.Plan,
		Layers:    out.Layers,
		Rounds:    out.Rounds,
		RecordID:  id.RequestID,
	}

	if !req.Submit {
		// 只装配：把装配后的出站体交给调用方（客户端驱动形态）。
		assembled := out.Record.Rounds[0].RequestBody
		resp.Body = assembled
		return resp, http.StatusOK, nil
	}

	// ② 装配 + 提交 + 记忆工具回环（网关驱动形态）。
	submitter := s.Submitter
	if submitter == nil {
		submitter = s.Agent.Submitter
	}
	if submitter == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("contextagent: no submitter configured")
	}
	// ★ 回灌必须带**调用方自己的凭据**：否则回灌请求以网关内部身份过
	//   鉴权与计费，归属错乱（凭据归属 / 计费 / 审计全部挂错主体）。
	if d, ok := submitter.(RequestSubmitterDeriver); ok {
		submitter = d.ForRequest(id, r.Header.Get("Authorization"))
	}
	full := *s.Agent
	full.Submitter = submitter
	full.InjectTools = true
	fullOut, err := full.Process(ctx, Request{
		RequestID:     id.RequestID,
		Identity:      id,
		Body:          clientBody,
		Protocol:      id.Protocol,
		ContextWindow: req.ContextWindow,
	})
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	resp.Body = nil
	resp.ResponseBody = fullOut.ResponseBody
	resp.Rounds = fullOut.Rounds
	resp.ServedToolCalls = fullOut.ServedToolCalls
	resp.Layers = fullOut.Layers
	resp.Plan = fullOut.Record.Plan
	return resp, http.StatusOK, nil
}

// HandleTool 处理 /tool。
func (s *Service) HandleTool(ctx context.Context, r *http.Request) (*ToolResponse, int, error) {
	if s == nil || s.Agent == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("contextagent: agent is not configured")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("contextagent: read body: %w", err)
	}
	var req ToolRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("contextagent: decode request: %w", err)
	}
	if !isMemoryTool(req.Name) {
		return nil, http.StatusBadRequest, fmt.Errorf("contextagent: unknown memory tool %q", req.Name)
	}

	id := Identity{}
	if req.Identity != nil {
		id = *req.Identity
	} else {
		id = Recognize(r, nil, IdentityFacts{
			TenantID:  r.Header.Get("X-Gw-Tenant-Id"),
			RequestID: r.Header.Get("X-Request-Id"),
		})
	}

	args := req.Arguments
	if args == nil {
		args = map[string]any{}
	}
	callArgs, _ := json.Marshal(args)
	res := ServeMemoryTool(ctx, ToolDeps{
		Identity:   id,
		StepLoader: s.Agent.StepLoader,
		LongTerm:   s.Agent.LongTerm,
		MaxBytes:   s.Agent.MaxToolResultBytes,
	}, ToolCall{ID: "ctx-tool", Name: req.Name, Arguments: callArgs})

	return &ToolResponse{
		ToolCallID: res.ToolCallID,
		Name:       res.Name,
		Content:    res.Content,
		Loaded:     res.Loaded,
		Error:      res.Error,
	}, http.StatusOK, nil
}

// HandleRecord 处理 /record/{request_id}。
func (s *Service) HandleRecord(ctx context.Context, r *http.Request, requestID string) (*RecordResponse, int, error) {
	if s == nil || s.Agent == nil || s.Agent.Recorder == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("contextagent: recorder is not configured")
	}
	rec, err := s.Agent.Recorder.Load(ctx, requestID)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if rec == nil {
		return nil, http.StatusNotFound, fmt.Errorf("contextagent: record %q not found", requestID)
	}
	return &RecordResponse{
		RequestID:                  rec.RequestID,
		CreatedAt:                  rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		Identity:                   rec.Identity,
		Plan:                       rec.Plan,
		Layers:                     rec.Layers,
		OriginalRequestBodyBase64:  encodeBase64(rec.OriginalRequestBody),
		OriginalResponseBodyBase64: encodeBase64(rec.OriginalResponseBody),
		FinalResponseBodyBase64:    encodeBase64(rec.FinalResponseBody),
		Rounds:                     rec.Rounds,
		ServedToolCalls:            rec.ServedToolCalls,
	}, http.StatusOK, nil
}
