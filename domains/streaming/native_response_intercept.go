package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// nativeResponseInterception carries request-scoped, authenticated identity
// into the last write boundary. The body is always the client protocol body
// after upstream conversion; an upstream Chat body must never be checked in
// place of the bytes the native client receives.
type nativeResponseInterception struct {
	ctx         context.Context
	request     response.InterceptRequest
	failureCode *string
}

func setNativeResponseFailure(options []nativeResponseInterception, code string) {
	if len(options) > 0 && options[0].failureCode != nil {
		*options[0].failureCode = code
	}
}

func interceptNativeResponseBody(chain ResponseInterceptor, options *nativeResponseInterception, body []byte) ([]byte, bool, error) {
	if chain == nil || options == nil {
		return body, false, nil
	}
	request := options.request
	request.ResponseBody = body
	ctx := options.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := chain.InterceptNonStream(ctx, &request)
	if err != nil {
		return nil, false, fmt.Errorf("intercept native response: %w", err)
	}
	if result == nil {
		return body, false, nil
	}
	if result.ShouldBlock {
		return nil, true, nil
	}
	if len(result.ModifiedBody) == 0 {
		return body, false, nil
	}
	if !json.Valid(result.ModifiedBody) {
		return nil, false, fmt.Errorf("interceptor returned invalid JSON")
	}
	format, empty := classifyNonStreamUpstreamResponse(result.ModifiedBody)
	if empty || options.request.ClientProtocol == "anthropic-messages" && format != nonStreamResponseAnthropic ||
		options.request.ClientProtocol == "openai-responses" && format != nonStreamResponseResponses {
		return nil, false, fmt.Errorf("interceptor returned invalid client protocol body")
	}
	return result.ModifiedBody, false, nil
}

func commitNativeJSON(w http.ResponseWriter, requestID string, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func validDeferredClientBody(protocol string, body []byte) bool {
	if !json.Valid(body) {
		return false
	}
	format, empty := classifyNonStreamUpstreamResponse(body)
	if empty {
		return false
	}
	switch protocol {
	case "anthropic-messages":
		return format == nonStreamResponseAnthropic
	case "openai-responses":
		return format == nonStreamResponseResponses
	default:
		if format != nonStreamResponseChat {
			return false
		}
		var envelope struct {
			Choices json.RawMessage `json:"choices"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			return false
		}
		var choices []json.RawMessage
		return json.Unmarshal(envelope.Choices, &choices) == nil && len(choices) > 0
	}
}

// commitDeferredChatResponse preserves safe upstream response headers while
// recomputing the length for the fully transformed, policy-approved body.
func commitDeferredChatResponse(w http.ResponseWriter, upstream *http.Response, requestID string, body []byte) error {
	status := http.StatusOK
	if upstream != nil {
		if upstream.StatusCode >= 200 && upstream.StatusCode < 300 {
			status = upstream.StatusCode
		}
		for key, values := range upstream.Header {
			if ratelimit.IsGatewayRateLimitScopeHeader(key) ||
				strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Content-Encoding") ||
				strings.EqualFold(key, "Connection") || strings.EqualFold(key, "Transfer-Encoding") {
				continue
			}
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	n, err := w.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	return err
}

func nativeResponseTokensUsed(body []byte) int {
	var value struct {
		Usage struct {
			TotalTokens  int `json:"total_tokens"`
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &value) != nil {
		return 0
	}
	if value.Usage.TotalTokens > 0 {
		return value.Usage.TotalTokens
	}
	return value.Usage.InputTokens + value.Usage.OutputTokens
}

func nativeJSONArrayCount(raw []byte) int {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return 0
	}
	return len(values)
}
