// Package externalhost 是外部钩子的 Go RPC 宿主（方案 W3 路径 B）。
//
// 定位：宿主只负责"把网关的钩子调用送到进程外钩子实现方，并把回复按 W3.2/W3.3
// 语义收敛回网关"，不承载任何钩子业务逻辑。合同定义见子包 contract。
//
// 复用面（承接第 89 轮 §四.2 裁决"合同先行 + Go RPC 先行"）：
//
//   - plugin-runtime supervisor：进程生命周期（Start/Stop/Restart）+ socket 路径分配。
//   - plugin-runtime apiproxy 的 unix socket 传输范式：HTTP over unix domain socket。
//   - plugin-runtime handshake：装载前的就绪握手与版本比对。
//   - plugin-runtime health_check：socket 存活探活。
//
// 关键语义（方案 W3.2/W3.3/W3.4）：
//
//   - 超时/崩溃 → 原样直通（fail-open），并归因为 FailureTimeout/FailureCrash。
//   - 改写受保护字段（model/stream/tools）→ 整体回滚并记失败（FailureProtectedField）。
//   - onRequest 失败 → 本请求不再触发 onResponse（W3.2）。
//   - remote 来源默认不捕获正文（W3.4 隐私原则）。
//   - onResponse 捕获为 fire-and-forget，不阻塞 EOF（W3.3）。
//
// ★ 2026-11-24（L6/L7 审计轮）**实现状态说明**（原来这段只写契约、不写实现，
// 导致下游把「契约期望」当「已实现」）：
//
//   - ★ 本包在生产代码里**零接线**（`domains/hooks/externalhost` 在包外零引用）
//     ⇒ 以下「当前实现」描述的是**代码形态**，不是生产上正在发生的行为。
//   - ★ **W3.3 的「8MiB 有界副本」当前**未**在送出侧兑现**：
//     invoke 把调用方给的 Request/Response payload **原样** EncodeInvocation
//     送出（没有走 contract.BoundedCopy）⇒ 送出侧正文**无 8MiB 上限**。
//   - ★ 读入侧的 `io.LimitReader(httpResp.Body, 4*contract.MaxBodyBytes)`
//     （32MiB）限的是**钩子的回复体**，不是「捕获副本」——两者不是一回事。
//   - ⇒ 契约侧（BoundedCopy / Payload.Truncated / OriginalBytes）已备好，
//     但**没有生产调用方**；是否接线属 owner 决策（见主档 §4-L7）。
package externalhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// Config 是宿主配置。
type Config struct {
	// SocketPath 钩子进程监听的 unix domain socket 路径。
	SocketPath string
	// BaseURL 显式基址（如 http://127.0.0.1:port）。留空时使用 unix socket 传输，
	// 基址固定为 http://unix（仅作 URL host 占位，实际走 unix 拨号）。
	BaseURL string
	// Timeout 单次钩子调用超时；<=0 用合同默认 1s，越界自动收敛到 [100ms,10s]。
	Timeout time.Duration
	// AllowRemoteBodyCapture 允许 remote 来源捕获正文；默认 false（W3.4 隐私原则）。
	AllowRemoteBodyCapture bool
	// HTTPClient 自定义传输（测试注入）；nil 时使用 unix socket 传输。
	HTTPClient *http.Client
}

// Outcome是一次钩子调用的结果。
type Outcome struct {
	// Decision 钩子给出的处置结论；失败时为 DecisionContinue（原样直通）。
	Decision contract.Decision
	// Request 改写后的请求载荷（decision=rewrite 时非空）。
	Request *contract.RequestPayload
	// Response 改写后的响应载荷（decision=rewrite 时非空）。
	Response *contract.ResponsePayload
	// Reason 中止原因。
	Reason string
	// Failure 失败归因；成功为 contract.FailureNone。
	Failure contract.FailureKind
	// Err失败详情（成功为 nil）。
	Err error
	// Skipped 置位表示该钩子点被宿主策略跳过（如 remote 正文捕获）。
	Skipped bool
}

// Failed 返回本次调用是否失败。
func (o *Outcome) Failed() bool {
	return o != nil && o.Failure != contract.FailureNone
}

// Host 是外部钩子的 Go RPC 宿主。
type Host struct {
	cfg    Config
	client *http.Client
}

// unixHTTPClient 构造走 unix domain socket 的 HTTP 客户端。
//
// Proxy 显式置 nil：unix socket 传输若继承环境 HTTP(S)_PROXY，请求会被代理
// 劫持发往外部代理而不是本机 socket（生产事故面，静默但致命）。
func unixHTTPClient(socketPath string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// New构造宿主。
func New(cfg Config) *Host {
	h := &Host{cfg: cfg}
	if cfg.HTTPClient != nil {
		h.client = cfg.HTTPClient
	} else {
		h.client = unixHTTPClient(cfg.SocketPath, contract.NormalizeTimeout(cfg.Timeout))
	}
	return h
}

// baseURL 返回调用基址。
func (h *Host) baseURL() string {
	if h.cfg.BaseURL != "" {
		return strings.TrimRight(h.cfg.BaseURL, "/")
	}
	return "http://unix"
}

// Timeout 返回本次宿主生效的单次调用超时。
func (h *Host) Timeout() time.Duration {
	return contract.NormalizeTimeout(h.cfg.Timeout)
}

// OnRequest 在协议翻译前调用外部钩子（W3.1）。
//
// 返回的 Outcome 在失败（超时/崩溃/协议违规/触碰受保护字段）时一律为
// DecisionContinue 且 Request 为 nil——即"原样直通"，由调用方继续原请求。
func (h *Host) OnRequest(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
	if hookCtx == nil || req == nil {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureProtocol,
			Err: errors.New("external host: on_request requires context and payload")}
	}
	if !hookCtx.Origin.AllowsBodyCapture() && !h.cfg.AllowRemoteBodyCapture {
		// W3.4：remote 来源默认跳过正文捕获，但仍把控制面上下文交给钩子。
		stripped := *req
		stripped.Body = contract.Payload{}
		stripped.ToolDeclarations = nil
		return h.invoke(ctx, hookCtx, contract.HookPointOnRequest, &stripped, nil)
	}
	return h.invoke(ctx, hookCtx, contract.HookPointOnRequest, req, nil)
}

// OnResponse 在上游响应返回后调用外部钩子（W3.1/W3.3）。
//
// fire-and-forget 语义由调用方决定：本方法自身是同步的，但宿主不持有响应正文。
//
// ★ 2026-11-24（L7）：原注释写「捕获副本有界（8MiB），因此不会阻塞 EOF 路径」，
// ★★ 这是一句**失实断言**——当时的实现把调用方的 payload **原样**送出，
//   **没有**走 contract.BoundedCopy ⇒ 送出侧正文并没有 8MiB 上限。
//   ⇒ 这里改为陈述**实际语义**，并把「契约期望」与「当前实现」分开标注，
//   免得下游继续把契约当现状。
//   ★ 另：读入侧的 32MiB 上限（4*MaxBodyBytes）限的是**钩子的回复体**，
//   与「捕获副本」不是同一个东西。
func (h *Host) OnResponse(ctx context.Context, hookCtx *contract.Context, resp *contract.ResponsePayload) *Outcome {
	if hookCtx == nil || resp == nil {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureProtocol,
			Err: errors.New("external host: on_response requires context and payload")}
	}
	if !hookCtx.Origin.AllowsBodyCapture() && !h.cfg.AllowRemoteBodyCapture {
		stripped := *resp
		stripped.Body = contract.Payload{}
		return h.invoke(ctx, hookCtx, contract.HookPointOnResponse, nil, &stripped)
	}
	return h.invoke(ctx, hookCtx, contract.HookPointOnResponse, nil, resp)
}

// invoke 完成一次线协议往返，并按 W3.2 语义收敛结果。
func (h *Host) invoke(ctx context.Context, hookCtx *contract.Context, point contract.HookPoint,
	req *contract.RequestPayload, resp *contract.ResponsePayload) *Outcome {

	inv := &contract.Invocation{
		ContractVersion: contract.ContractVersion,
		HookPoint:       point,
		Context:         hookCtx,
		Request:         req,
		Response:        resp,
	}
	if err := contract.ValidateInvocation(inv); err != nil {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureProtocol, Err: err}
	}
	payload, err := contract.EncodeInvocation(inv)
	if err != nil {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureProtocol, Err: err}
	}

	callCtx, cancel := context.WithTimeout(ctx, h.Timeout())
	defer cancel()

	url := h.baseURL() + point.Path()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureProtocol, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-External-Hook-Contract", contract.ContractVersion)

	httpResp, err := h.client.Do(httpReq)
	if err != nil {
		return h.classifyTransportError(ctx, err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 4*contract.MaxBodyBytes))
	if err != nil {
		return h.classifyTransportError(ctx, err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureProtocol,
			Err: fmt.Errorf("external hook status %d", httpResp.StatusCode)}
	}

	reply, err := contract.DecodeReply(body)
	if err != nil {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureProtocol, Err: err}
	}
	if err := contract.ValidateReply(reply); err != nil {
		kind := contract.FailureProtocol
		if errors.Is(err, contract.ErrInvalidDecision) {
			kind = contract.FailureHook
		}
		return &Outcome{Decision: contract.DecisionContinue, Failure: kind, Err: err}
	}

	out := &Outcome{Decision: reply.Decision, Reason: reply.Reason}
	switch reply.Decision {
	case contract.DecisionBlock:
		// 中止是钩子的显式意图，不算失败；但中止原因必须已通过校验。
		return out
	case contract.DecisionRewrite:
		if reply.Request != nil {
			// 补丁语义：缺省字段不变；生效载荷 = 原载荷 + 补丁。
			effective := contract.MergeRequest(req, reply.Request)
			if touched := contract.CheckProtectedFields(req, effective); len(touched) > 0 {
				return &Outcome{
					Decision: contract.DecisionContinue,
					Failure:  contract.FailureProtectedField,
					Err:      fmt.Errorf("%w: %v", contract.ErrProtectedField, touched),
				}
			}
			out.Request = effective
		}
		if reply.Response != nil {
			out.Response = contract.MergeResponse(resp, reply.Response)
		}
		return out
	default:
		if reply.Error != "" {
			return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureHook,
				Err: fmt.Errorf("external hook error: %s", reply.Error)}
		}
		return out
	}
}

// classifyTransportError 把传输层错误归因为超时或崩溃（W3.2：两者都原样直通）。
func (h *Host) classifyTransportError(parent context.Context, err error) *Outcome {
	if errors.Is(err, context.DeadlineExceeded) || (parent != nil && parent.Err() != nil && errors.Is(parent.Err(), context.DeadlineExceeded)) {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureTimeout, Err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureCrash, Err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureTimeout, Err: err}
	}
	return &Outcome{Decision: contract.DecisionContinue, Failure: contract.FailureCrash, Err: err}
}

// EncodeReply 是给钩子实现方用的回复编码助手（便于测试与参考实现）。
func EncodeReply(reply *contract.Reply) ([]byte, error) {
	return json.Marshal(reply)
}
