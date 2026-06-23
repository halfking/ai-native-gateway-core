package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/relay"
)

// LegacyTransport 使用直接转换实现协议转换（复用 relay 包）。
//
// 与 IRTransport 的关键差异：不做 IR 抽象，直接调用 6 个回调函数完成转换。
// 性能更好但扩展性差，仅作为 IR 的备选实现长期共存。
type LegacyTransport struct {
	captureBuilder func() *audit.StreamCapture
}

// NewLegacyTransport 构造默认配置的 LegacyTransport。
func NewLegacyTransport() *LegacyTransport {
	return &LegacyTransport{
		captureBuilder: func() *audit.StreamCapture { return &audit.StreamCapture{} },
	}
}

// Implementation 返回 "legacy"。
func (t *LegacyTransport) Implementation() string { return "legacy" }

// Convert 实现 4 象限协议转换（请求方向）。
func (t *LegacyTransport) Convert(ctx context.Context, envelope *domain.RequestEnvelope) ([]byte, error) {
	if envelope == nil || envelope.Transport == nil {
		return nil, errors.New("legacy_transport: nil envelope/transport")
	}
	tc := envelope.Transport

	// Q1: OpenAI → OpenAI（直通）
	if isOpenAI(tc.ClientProtocol) && isOpenAI(tc.UpstreamProtocol) {
		conversionTotal.WithLabelValues("legacy", "request").Inc()
		return tc.BodyBytes, nil
	}

	// Q2: Anthropic → OpenAI
	if isAnthropic(tc.ClientProtocol) && isOpenAI(tc.UpstreamProtocol) {
		out, err := relay.ConvertAnthropicRequestToChat(tc.BodyBytes)
		if err != nil {
			conversionErrors.WithLabelValues("legacy", "request").Inc()
			return nil, fmt.Errorf("legacy_transport: anthropic→openai: %w", err)
		}
		conversionTotal.WithLabelValues("legacy", "request").Inc()
		return out, nil
	}

	// Q3: OpenAI → Anthropic
	if isOpenAI(tc.ClientProtocol) && isAnthropic(tc.UpstreamProtocol) {
		converted, err := relay.ConvertChatRequestToAnthropic(tc.BodyBytes)
		if err != nil {
			conversionErrors.WithLabelValues("legacy", "request").Inc()
			return nil, fmt.Errorf("legacy_transport: openai→anthropic: %w", err)
		}
		conversionTotal.WithLabelValues("legacy", "request").Inc()
		return converted, nil
	}

	// Q4: Anthropic → Anthropic（直通）
	if isAnthropic(tc.ClientProtocol) && isAnthropic(tc.UpstreamProtocol) {
		conversionTotal.WithLabelValues("legacy", "request").Inc()
		return tc.BodyBytes, nil
	}

	return nil, fmt.Errorf("legacy_transport: unsupported conversion %s → %s", tc.ClientProtocol, tc.UpstreamProtocol)
}

// ConvertResponse 实现 4 象限协议转换（响应方向）。
func (t *LegacyTransport) ConvertResponse(ctx context.Context, envelope *domain.RequestEnvelope, upstreamBody []byte) ([]byte, error) {
	if envelope == nil || envelope.Transport == nil {
		return nil, errors.New("legacy_transport: nil envelope/transport")
	}
	tc := envelope.Transport

	// Q1/Q4: 协议一致（直通）
	if tc.ClientProtocol == tc.UpstreamProtocol {
		conversionTotal.WithLabelValues("legacy", "response").Inc()
		return upstreamBody, nil
	}

	// Q3: OpenAI client ← Anthropic upstream
	if isOpenAI(tc.ClientProtocol) && isAnthropic(tc.UpstreamProtocol) {
		out, err := relay.ConvertAnthropicResponseToChat(upstreamBody, tc.ClientModel)
		if err != nil {
			conversionErrors.WithLabelValues("legacy", "response").Inc()
			return nil, fmt.Errorf("legacy_transport: openai←anthropic: %w", err)
		}
		conversionTotal.WithLabelValues("legacy", "response").Inc()
		return out, nil
	}

	// Q2: Anthropic client ← OpenAI upstream（Q2 直通）
	if isAnthropic(tc.ClientProtocol) && isOpenAI(tc.UpstreamProtocol) {
		conversionTotal.WithLabelValues("legacy", "response").Inc()
		return upstreamBody, nil
	}

	return nil, fmt.Errorf("legacy_transport: unsupported response conversion %s ← %s", tc.ClientProtocol, tc.UpstreamProtocol)
}

// ConvertStream 实现流式 SSE 转换。
func (t *LegacyTransport) ConvertStream(ctx context.Context, envelope *domain.RequestEnvelope, upstreamResp *http.Response) error {
	if envelope == nil || envelope.Transport == nil || upstreamResp == nil {
		return errors.New("legacy_transport: nil envelope/transport/upstream")
	}
	tc := envelope.Transport

	capture := t.captureBuilder()
	pc := relay.NewPendingCapturer(0)

	switch {
	case tc.ClientProtocol == tc.UpstreamProtocol:
		// 直通：Anthropic→Anthropic 或 OpenAI→OpenAI
		if isAnthropic(tc.ClientProtocol) {
			relay.StreamAnthropicPassthrough(
				tc.W, upstreamResp,
				tc.ClientModel, tc.OutboundModel, envelope.RequestID,
				capture, pc,
			)
		} else {
			// OpenAI 直通：复制字节流
			tc.W.Header().Set("Content-Type", "text/event-stream")
			_, err := io.Copy(tc.W, upstreamResp.Body)
			upstreamResp.Body.Close()
			if err != nil {
				conversionErrors.WithLabelValues("legacy", "stream").Inc()
				return err
			}
		}
	case isOpenAI(tc.ClientProtocol) && isAnthropic(tc.UpstreamProtocol):
		// Q3: OpenAI ← Anthropic
		relay.StreamAnthropicSSEToOpenAI(
			tc.W, upstreamResp,
			tc.ClientModel, tc.OutboundModel, envelope.RequestID,
			capture, pc,
		)
	default:
		return fmt.Errorf("legacy_transport: unsupported stream conversion %s ← %s", tc.ClientProtocol, tc.UpstreamProtocol)
	}

	conversionTotal.WithLabelValues("legacy", "stream").Inc()
	if err := ctx.Err(); err != nil {
		slog.Warn("legacy_transport: context cancelled", "err", err)
	}
	return nil
}

func isOpenAI(protocol string) bool {
	return protocol == "openai-chat" || protocol == "openai"
}

func isAnthropic(protocol string) bool {
	return protocol == "anthropic-messages" || protocol == "anthropic"
}
