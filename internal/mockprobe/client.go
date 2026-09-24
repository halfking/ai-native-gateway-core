// Package mockprobe —— Mock 探测客户端与调度器（2026-09-24，
// docs/design/2026-09-23-mock-probe-channel §3.5）。
//
// client.go：探测 HTTP 客户端。走网关自身入口（主 mux → 鉴权旁路 →
// /mock/v1/chat/completions/{fast,slow}），凭证为系统白名单
// Bearer mock-probe-client（internal/auth）。协议范围锁定 OpenAI Chat
// Completions（设计 v2 头注：其它协议走 follow-up）。
package mockprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/auth"
	"github.com/kaixuan/llm-gateway-go/internal/providers/mock"
	"github.com/kaixuan/llm-gateway-go/internal/sse"
)

// ProtocolOpenAI 标记探测协议（mock_probe_history.protocol 列）。
const ProtocolOpenAI = "openai"

// DefaultProbeTimeout 单次探测的兜底超时（runner 亦按此包裹 ctx）：slow
// 通道延迟上限 1s，10s 足以覆盖启动/调度抖动而不阻塞下一轮。
const DefaultProbeTimeout = 10 * time.Second

// Client 是 mock 探测 HTTP 客户端（并发安全）。
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewClient 构造探测客户端。baseURL 形如 http://127.0.0.1:8782。
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   auth.MockProbeClientToken,
		hc: &http.Client{
			Timeout: 30 * time.Second, // 兜底；单次探测另受 ctx 约束
			Transport: &http.Transport{
				// 探测是到自身 loopback 的短连接高频请求，禁用 keepalive
				// 池复用，避免与真实流量共享连接状态。
				DisableKeepAlives: true,
				DialContext: (&net.Dialer{
					Timeout: 3 * time.Second,
				}).DialContext,
			},
		},
	}
}

// BaseURL 返回客户端指向的网关入口（测试断言用）。
func (c *Client) BaseURL() string { return c.baseURL }

// ProbeResult 是一次探测的完整结果（指标 + 历史落库的数据源）。
type ProbeResult struct {
	Supplier   string
	Stream     bool
	Protocol   string
	Channel    string
	Latency    time.Duration
	StatusCode int
	RequestID  string // mock 响应携带的 id（chatcmpl-mock-*）
	ErrorCode  string // "" 表示成功；"timeout"/"transport"/"http_500"/"bad_body"/...
	OK         bool
}

// Channel 生成通道标识（supplier:stream|nonstream），与
// mock_probe_history.channel 列一致。
func Channel(supplier string, stream bool) string {
	if stream {
		return supplier + ":stream"
	}
	return supplier + ":nonstream"
}

// Probe 对指定供应商 × 流式模式发起一次 OpenAI Chat Completions 探测。
// 调用方通过 ctx 控制超时；结果经全量校验（状态码 + 响应形状 + 流式
// [DONE] 收尾）后才置 OK。
func (c *Client) Probe(ctx context.Context, supplier string, stream bool) ProbeResult {
	res := ProbeResult{
		Supplier: supplier,
		Stream:   stream,
		Protocol: ProtocolOpenAI,
		Channel:  Channel(supplier, stream),
	}
	segment, ok := mock.Segment(supplier)
	if !ok {
		res.ErrorCode = "bad_supplier"
		return res
	}
	body := fmt.Sprintf(`{"model":"mock-probe","messages":[{"role":"user","content":"ping"}],"stream":%t}`, stream)
	url := c.baseURL + "/mock/v1/chat/completions/" + segment

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		res.ErrorCode = "build_request"
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.hc.Do(req)
	if err != nil {
		res.Latency = time.Since(start)
		res.ErrorCode = classifyTransportError(ctx, err)
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	res.StatusCode = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		// 非 200 优先按状态码分类（http_404 等）；错误响应体不是
		// chat.completion 形状，形状校验只对成功响应有意义。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		res.Latency = time.Since(start)
		res.ErrorCode = fmt.Sprintf("http_%d", resp.StatusCode)
		return res
	}
	if stream {
		res.RequestID, res.ErrorCode = readStreamProbe(resp.Body)
	} else {
		res.RequestID, res.ErrorCode = readBodyProbe(resp.Body)
	}
	res.Latency = time.Since(start)
	res.OK = res.ErrorCode == ""
	return res
}

// readBodyProbe 校验非流式响应：JSON 形状 + chat.completion 对象 +
// mock id。
func readBodyProbe(r io.Reader) (requestID, errCode string) {
	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return "", "read_body"
	}
	var body struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", "bad_body"
	}
	if body.Object != "chat.completion" || len(body.Choices) == 0 {
		return "", "bad_body"
	}
	if !strings.Contains(body.ID, "mock") || body.Choices[0].Message.Content == "" {
		return "", "bad_body"
	}
	return body.ID, ""
}

// readStreamProbe 校验流式响应：至少一个携带内容的 delta + [DONE] 收尾；
// requestID 取首个 chunk 的 id。复用 internal/sse.LineReader 按行读。
func readStreamProbe(r io.Reader) (requestID, errCode string) {
	lr := sse.NewLineReader(r, 1<<20)
	sawContent := false
	for {
		line, err := lr.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return requestID, "stream_read"
		}
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			if !sawContent {
				return requestID, "stream_empty"
			}
			return requestID, ""
		}
		var chunk struct {
			ID      string `json:"id"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // 非 JSON 数据帧（注释/心跳）容忍
		}
		if requestID == "" && strings.Contains(chunk.ID, "mock") {
			requestID = chunk.ID
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			sawContent = true
		}
	}
	return requestID, "stream_interrupted" // 无 [DONE] 收尾
}

// classifyTransportError 区分超时与一般传输错误（历史表 error_code 用）。
func classifyTransportError(ctx context.Context, err error) string {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "transport"
}

// BaseURLFromListen 把网关监听地址（":8782" / "0.0.0.0:8782" /
// "10.0.0.5:8782"）归一为探测客户端的 loopback 基地址。host 恒钳为
// 127.0.0.1（只保留端口）：探测是网关进程的自检流量，只允许走 loopback
// 网络栈，不得经由物理网卡/外部网络绕行——即便监听绑定在 0.0.0.0 或
// 某个内网 IP 上。
func BaseURLFromListen(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://127.0.0.1"
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port)
}
