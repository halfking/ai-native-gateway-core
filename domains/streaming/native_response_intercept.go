package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// nativeResponseInterception carries request-scoped, authenticated identity
// into the last write boundary. The body is always the client protocol body
// after upstream conversion; an upstream Chat body must never be checked in
// place of the bytes the native client receives.
type nativeResponseInterception struct {
	ctx     context.Context
	request response.InterceptRequest
	// failureEmit (optional) is invoked when the interceptor blocks the body,
	// so the caller can emit an output_policy_blocked failure audit row with
	// the chosen provider/credential attribution (R25-V). The provider body
	// itself must never persist in that row.
	failureEmit func()
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

// candidateAttribution returns non-nil pointers for the attempt's
// provider/credential ids so governance failure audit rows keep their
// attribution (zero values stay nil — no fabricated ids on rows).
func candidateAttribution(result *executors.ExecuteResult) (providerID, credentialID *int) {
	if result == nil {
		return nil, nil
	}
	if v := result.Candidate.ProviderID; v > 0 {
		providerID = &v
	}
	if v := result.Candidate.CredentialID; v > 0 {
		credentialID = &v
	}
	return providerID, credentialID
}

// validateChatInterceptedBody guards the chat lane's ModifiedBody commit
// (R25-V). classifyNonStreamUpstreamResponse only checks envelope-key
// presence, so `{"choices":"invalid"}` (choices is a string) classifies as
// FormatChat — writing it verbatim would corrupt the client stream. The
// chat lane additionally requires choices to unmarshal as an array.
func validateChatInterceptedBody(body []byte, clientProtocol string) bool {
	if len(body) == 0 || !json.Valid(body) {
		return false
	}
	format, empty := classifyNonStreamUpstreamResponse(body)
	if empty {
		return false
	}
	switch clientProtocol {
	case "anthropic-messages":
		return format == nonStreamResponseAnthropic
	case "openai-responses":
		return format == nonStreamResponseResponses
	default:
		// openai-chat: envelope key + well-formed choices array.
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
		return json.Unmarshal(envelope.Choices, &choices) == nil
	}
}

// outputGovernanceMandatory reports whether the configured interceptor
// declares its non-stream errors MANDATORY (FailClosed). Chains expose it
// via FailClosedOnNonStreamError; bare interceptors via FailClosed itself.
func outputGovernanceMandatory(interceptor ResponseInterceptor) bool {
	if fc, ok := interceptor.(interface{ FailClosedOnNonStreamError() bool }); ok {
		return fc.FailClosedOnNonStreamError()
	}
	if fc, ok := interceptor.(interface{ FailClosed() bool }); ok {
		return fc.FailClosed()
	}
	return false
}

// copySafeUpstreamHeaders copies response headers to the client-facing
// writer, skipping hop-by-hop headers and length/encoding fields that the
// deferred commit recomputes for the (possibly rewritten) body.
func copySafeUpstreamHeaders(dst, src http.Header) {
	for key, values := range src {
		if ratelimit.IsGatewayRateLimitScopeHeader(key) ||
			strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Content-Encoding") ||
			strings.EqualFold(key, "Connection") || strings.EqualFold(key, "Transfer-Encoding") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// deferredNonStreamWriter captures the executor's client-facing success
// write (status, headers, body) so output compliance can block or rewrite
// the body before the first byte reaches the client. The chat non-stream
// path previously ran InterceptNonStream only after the executor had
// already written the response, which left ShouldBlock with no effect on
// the wire (audit round 25, R25-A).
type deferredNonStreamWriter struct {
	header      http.Header
	status      int
	body        []byte
	wroteStatus bool
}

func newDeferredNonStreamWriter() *deferredNonStreamWriter {
	return &deferredNonStreamWriter{header: make(http.Header)}
}

func (d *deferredNonStreamWriter) Header() http.Header { return d.header }

func (d *deferredNonStreamWriter) WriteHeader(status int) {
	if d.wroteStatus {
		return
	}
	d.wroteStatus = true
	d.status = status
}

func (d *deferredNonStreamWriter) Write(p []byte) (int, error) {
	if !d.wroteStatus {
		d.wroteStatus = true
		d.status = http.StatusOK
	}
	d.body = append(d.body, p...)
	return len(p), nil
}

// Flush is a no-op: nothing reaches the client until commit.
func (d *deferredNonStreamWriter) Flush() {}

// commit replays the captured response on w, substituting the
// policy-approved body when non-empty, else the captured body.
func (d *deferredNonStreamWriter) commit(w http.ResponseWriter, body []byte) {
	if len(body) == 0 {
		body = d.body
	}
	copySafeUpstreamHeaders(w.Header(), d.header)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	status := http.StatusOK
	if d.wroteStatus {
		status = d.status
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
