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
}

// NewIRTransport 构造默认配置的 IRTransport。
func NewIRTransport() *IRTransport {
	return &IRTransport{
		detector:  &IRProtocolDetector{},
		extractor: &IRExtensionExtractor{},
		restorer:  &IRExtensionRestorer{},
	}
}

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

// ConvertStream 实现流式 SSE 转换。
func (t *IRTransport) ConvertStream(ctx context.Context, envelope *domain.RequestEnvelope, upstreamResp *http.Response) error {
	if envelope == nil || envelope.Transport == nil || upstreamResp == nil {
		return errors.New("ir_transport: nil envelope/transport/upstream")
	}
	tc := envelope.Transport

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

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if err := t.convertStreamLine(tc, envelope, line); err != nil {
				slog.Warn("ir_transport: convert stream line failed", "err", err)
			}
		}
		if err != nil {
			break
		}
	}

	// 发送 [DONE]
	if tc.UpstreamProtocol == "openai-chat" {
		fmt.Fprintf(tc.W, "data: [DONE]\n\n")
		flusher.Flush()
	}

	conversionTotal.WithLabelValues("ir", "stream").Inc()
	return nil
}

func (t *IRTransport) convertStreamLine(tc *domain.TransportContext, env *domain.RequestEnvelope, line []byte) error {
	// OpenAI SSE: 直接是 `data: {...}\n`
	// Anthropic SSE: `event: <type>\ndata: {...}\n\n`
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}

	var chunk *ir.StreamChunk
	var err error

	switch tc.UpstreamProtocol {
	case "openai-chat":
		// 提取 data: 后面的 JSON
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			return nil
		}
		payload := bytes.TrimSpace(trimmed[5:])
		if bytes.Equal(payload, []byte("[DONE]")) {
			return nil
		}
		chunk, err = ir.ParseOpenAIStreamChunk(string(payload))
	case "anthropic-messages":
		// 简化处理：忽略 event 行，只处理 data 行
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			return nil
		}
		payload := bytes.TrimSpace(trimmed[5:])
		// Anthropic event type 默认是 message_start
		chunk, err = ir.ParseAnthropicStreamEvent("", payload)
	default:
		return fmt.Errorf("ir_transport: unsupported upstream protocol %s", tc.UpstreamProtocol)
	}

	if err != nil || chunk == nil {
		return nil
	}

	var clientData string
	switch tc.ClientProtocol {
	case "openai-chat":
		clientData = chunk.SerializeOpenAI(env.RequestID, tc.ClientModel, env.CreatedAt.Unix())
	case "anthropic-messages":
		clientData = chunk.SerializeAnthropic(env.RequestID, tc.ClientModel)
	default:
		return fmt.Errorf("ir_transport: unsupported client protocol %s", tc.ClientProtocol)
	}

	if clientData == "" {
		return nil
	}

	fmt.Fprintf(tc.W, "data: %s\n\n", clientData)
	if f, ok := tc.W.(http.Flusher); ok {
		f.Flush()
	}
	return nil
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
