package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// IRTransport 使用 IR（中间表示）实现协议转换。
type IRTransport struct {
	detector  ProtocolDetector
	extractor ExtensionExtractor
	restorer  ExtensionRestorer
	cb        *StreamCircuitBreaker // 流式降级熔断器
}

// NewIRTransport 构造默认配置的 IRTransport。
func NewIRTransport() *IRTransport {
	return &IRTransport{
		detector:  &IRProtocolDetector{},
		extractor: &IRExtensionExtractor{},
		restorer:  &IRExtensionRestorer{},
		cb:        NewStreamCircuitBreaker(),
	}
}

// SetCircuitBreaker 替换流式熔断器（测试/注入用）。
func (t *IRTransport) SetCircuitBreaker(cb *StreamCircuitBreaker) {
	if cb != nil {
		t.cb = cb
	}
}

// CircuitBreaker 返回流式熔断器（监控用）。
func (t *IRTransport) CircuitBreaker() *StreamCircuitBreaker { return t.cb }

// Implementation 返回 "ir"。
func (t *IRTransport) Implementation() string { return "ir" }

// Convert 实现 4 象限协议转换（请求方向）。
func (t *IRTransport) Convert(ctx context.Context, envelope *domain.RequestEnvelope) ([]byte, error) {
	if envelope == nil || envelope.Transport == nil {
		return nil, errors.New("ir_transport: nil envelope/transport")
	}
	tc := envelope.Transport

	// 1. 检测客户端协议（如果未设置）
	if tc.ClientProtocol == "" {
		proto, _ := t.detector.Detect(tc.BodyBytes, tc.R.Header)
		tc.ClientProtocol = proto
	}

	// 2. 提取扩展属性到 TransportContext.Extensions
	if tc.Extensions.IsZero() && t.extractor != nil {
		ext, err := t.extractor.Extract(tc.BodyBytes, tc.R.Header)
		if err != nil {
			slog.Warn("ir_transport: extract extensions failed", "err", err)
		} else if ext != nil {
			tc.Extensions = *ext
		}
	}

	// 3. Parse: ClientProtocol → IR
	internalReq, err := parseRequest(tc.ClientProtocol, tc.BodyBytes)
	if err != nil {
		return nil, fmt.Errorf("ir_transport: parse %s: %w", tc.ClientProtocol, err)
	}

	// 4. Serialize: IR → UpstreamProtocol
	upstreamBody, err := serializeRequest(tc.UpstreamProtocol, internalReq)
	if err != nil {
		return nil, fmt.Errorf("ir_transport: serialize %s: %w", tc.UpstreamProtocol, err)
	}

	conversionTotal.WithLabelValues("ir", "request").Inc()
	return upstreamBody, nil
}

// ConvertResponse 实现 4 象限协议转换（响应方向）。
func (t *IRTransport) ConvertResponse(ctx context.Context, envelope *domain.RequestEnvelope, upstreamBody []byte) ([]byte, error) {
	if envelope == nil || envelope.Transport == nil {
		return nil, errors.New("ir_transport: nil envelope/transport")
	}
	tc := envelope.Transport

	// 1. Parse: UpstreamProtocol → IR
	internalResp, err := parseResponse(tc.UpstreamProtocol, upstreamBody)
	if err != nil {
		return nil, fmt.Errorf("ir_transport: parse response %s: %w", tc.UpstreamProtocol, err)
	}

	// 2. Serialize: IR → ClientProtocol
	clientBody, err := serializeResponse(tc.ClientProtocol, internalResp, tc.ClientModel)
	if err != nil {
		return nil, fmt.Errorf("ir_transport: serialize response %s: %w", tc.ClientProtocol, err)
	}

	// 3. 还原扩展属性
	if !tc.Extensions.IsZero() && t.restorer != nil {
		restored, err := t.restorer.Restore(clientBody, &tc.Extensions)
		if err != nil {
			slog.Warn("ir_transport: restore extensions failed", "err", err)
		} else if restored != nil {
			clientBody = restored
		}
	}

	conversionTotal.WithLabelValues("ir", "response").Inc()
	return clientBody, nil
}

// ErrStreamCircuitOpen 报告 IR 流式路径已被熔断，应降级到 Legacy。
var ErrStreamCircuitOpen = errors.New("ir_transport: stream circuit open, fallback to legacy")

// ConvertStream 实现流式 SSE 转换。
//
// 行为：
//   - 入口检查 CircuitBreaker；若已 Open 返回 ErrStreamCircuitOpen（Factory 应降级 Legacy）
//   - 维护 pendingEvent 状态以正确配对 Anthropic SSE 的 `event:` + `data:` 双行
//   - 单个 chunk 解析错误：记录到熔断器 + slog.Warn，继续处理后续事件
//   - 流结束：记录成功到熔断器
func (t *IRTransport) ConvertStream(ctx context.Context, envelope *domain.RequestEnvelope, upstreamResp *http.Response) error {
	if envelope == nil || envelope.Transport == nil || upstreamResp == nil {
		return errors.New("ir_transport: nil envelope/transport/upstream")
	}
	tc := envelope.Transport

	// 入口熔断检查
	if t.cb != nil && t.cb.ShouldFallback() {
		return ErrStreamCircuitOpen
	}

	// 设置 SSE headers
	if tc.W != nil {
		tc.W.Header().Set("Content-Type", "text/event-stream")
		tc.W.Header().Set("Cache-Control", "no-cache")
		tc.W.Header().Set("Connection", "keep-alive")
	}

	flusher, ok := tc.W.(http.Flusher)
	if !ok {
		return errors.New("ir_transport: response writer does not support flushing")
	}

	br := bufioReader(upstreamResp.Body)
	defer upstreamResp.Body.Close()

	// pendingEvent 跟踪 Anthropic SSE 的 event: 行类型，等待对应的 data: 行
	pendingEvent := ""

	for {
		line, err := br.ReadBytes('\n')

		if len(line) > 0 {
			if writeErr := t.processStreamLine(tc, envelope, line, &pendingEvent); writeErr != nil {
				// 关键写入错误 → 立即终止
				if t.cb != nil {
					t.cb.RecordError()
				}
				return writeErr
			}
		}

		if err != nil {
			break
		}
	}

	// 发送 [DONE]（仅 OpenAI 客户端需要）
	if tc.ClientProtocol == "openai-chat" {
		fmt.Fprintf(tc.W, "data: [DONE]\n\n")
		flusher.Flush()
	}

	// 流成功完成
	if t.cb != nil {
		t.cb.RecordSuccess()
	}
	conversionTotal.WithLabelValues("ir", "stream").Inc()
	return nil
}

// processStreamLine 处理单行 SSE 输入并写入客户端。
//
// 维护 pendingEvent 状态以正确配对 Anthropic SSE：
//   - `event: <type>` 行：写入 pendingEvent
//   - `data: <json>` 行：用 pendingEvent 解析（若上游是 Anthropic）
//   - 空行（事件分隔）：清空 pendingEvent
//
// 解析错误时：slog.Warn + 记录到熔断器，但继续处理（不让单错杀全流）。
func (t *IRTransport) processStreamLine(tc *domain.TransportContext, env *domain.RequestEnvelope, line []byte, pendingEvent *string) error {
	trimmed := bytes.TrimRight(line, "\r\n")
	trimmedSpace := bytes.TrimSpace(trimmed)

	// 空行（事件分隔）→ 清空 pendingEvent
	if len(trimmedSpace) == 0 {
		*pendingEvent = ""
		return nil
	}

	// event: 行（Anthropic 协议）
	if bytes.HasPrefix(trimmedSpace, []byte("event:")) {
		*pendingEvent = string(bytes.TrimSpace(trimmedSpace[6:]))
		return nil
	}

	// data: 行（事件负载）
	if !bytes.HasPrefix(trimmedSpace, []byte("data:")) {
		// 其他行（如 OpenAI 的注释行 `:xxx`）跳过
		return nil
	}

	payload := bytes.TrimSpace(trimmedSpace[5:])
	if len(payload) == 0 {
		return nil
	}

	// 解析上游 chunk
	chunk, parseErr := t.parseUpstreamChunk(tc.UpstreamProtocol, trimmedSpace, *pendingEvent)
	if parseErr != nil {
		slog.Warn("ir_transport: parse stream chunk failed",
			"upstream", tc.UpstreamProtocol,
			"pending_event", *pendingEvent,
			"err", parseErr)
		// 解析错误：记录到熔断器但继续
		if t.cb != nil {
			t.cb.RecordError()
		}
		*pendingEvent = ""
		return nil
	}
	if chunk == nil {
		// sentinel 帧（如 [DONE]）或空事件
		*pendingEvent = ""
		return nil
	}

	// 序列化为客户端协议
	clientData := t.serializeClientChunk(tc, env, chunk)
	*pendingEvent = ""

	if clientData == "" {
		return nil
	}

	// 写入客户端 + flush
	if _, err := fmt.Fprintf(tc.W, "data: %s\n\n", clientData); err != nil {
		return err
	}
	if f, ok := tc.W.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// parseUpstreamChunk 解析上游 SSE 数据行为 IR StreamChunk。
//
// line 应是已剥除 \r\n 但保留 `data: ` 前缀的完整行（让 IR 函数自己剥前缀）。
// 对于 Anthropic：使用 pendingEvent 作为 eventType（来自上一行 `event:`）。
// 对于 OpenAI：pendingEvent 被忽略（OpenAI 没有 event 行）。
func (t *IRTransport) parseUpstreamChunk(protocol string, line []byte, eventType string) (*ir.StreamChunk, error) {
	switch protocol {
	case "openai-chat", "openai":
		// IR 函数期望带 "data: " 前缀的输入
		return ir.ParseOpenAIStreamChunk(string(line))
	case "anthropic-messages", "anthropic":
		// Anthropic: data 行 payload 是纯 JSON（不需 "data: " 前缀）
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimSpace(line[5:])
			if bytes.Equal(payload, []byte("[DONE]")) {
				return nil, nil
			}
			return ir.ParseAnthropicStreamEvent(eventType, payload)
		}
		// 非 data 行（已被外层处理，不应到这里）
		return nil, nil
	default:
		return nil, fmt.Errorf("ir_transport: unsupported upstream protocol %s", protocol)
	}
}

// serializeClientChunk 将 IR StreamChunk 序列化为客户端协议 SSE 数据。
func (t *IRTransport) serializeClientChunk(tc *domain.TransportContext, env *domain.RequestEnvelope, chunk *ir.StreamChunk) string {
	switch tc.ClientProtocol {
	case "openai-chat", "openai":
		return chunk.SerializeOpenAI(env.RequestID, tc.ClientModel, env.CreatedAt.Unix())
	case "anthropic-messages", "anthropic":
		return chunk.SerializeAnthropic(env.RequestID, tc.ClientModel)
	default:
		slog.Warn("ir_transport: unsupported client protocol", "protocol", tc.ClientProtocol)
		return ""
	}
}

// parseRequest 解析客户端请求到 IR。
func parseRequest(protocol string, body []byte) (*ir.InternalRequest, error) {
	switch protocol {
	case "openai-chat", "openai":
		return ir.ParseOpenAI(body)
	case "anthropic-messages", "anthropic":
		return ir.ParseAnthropic(body)
	default:
		return nil, fmt.Errorf("unsupported client protocol: %s", protocol)
	}
}

// serializeRequest 从 IR 序列化到上游协议。
func serializeRequest(protocol string, req *ir.InternalRequest) ([]byte, error) {
	switch protocol {
	case "openai-chat", "openai":
		return ir.SerializeOpenAI(req)
	case "anthropic-messages", "anthropic":
		return ir.SerializeAnthropic(req)
	default:
		return nil, fmt.Errorf("unsupported upstream protocol: %s", protocol)
	}
}

// parseResponse 解析上游响应到 IR。
func parseResponse(protocol string, body []byte) (*ir.InternalResponse, error) {
	switch protocol {
	case "openai-chat", "openai":
		return ir.ParseOpenAIResponse(body)
	case "anthropic-messages", "anthropic":
		return ir.ParseAnthropicResponse(body)
	default:
		return nil, fmt.Errorf("unsupported upstream protocol: %s", protocol)
	}
}

// serializeResponse 从 IR 序列化到客户端协议。
func serializeResponse(protocol string, resp *ir.InternalResponse, clientModel string) ([]byte, error) {
	switch protocol {
	case "openai-chat", "openai":
		return ir.SerializeOpenAIResponse(resp, clientModel)
	case "anthropic-messages", "anthropic":
		return ir.SerializeAnthropicResponse(resp, clientModel)
	default:
		return nil, fmt.Errorf("unsupported client protocol: %s", protocol)
	}
}
