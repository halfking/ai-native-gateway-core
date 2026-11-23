package executors

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
)

const maxOpenRouterSSELine = 64 << 10

// openRouterCostBody observes bounded SSE data lines while returning every
// byte unchanged to the consumer. It retains at most one line and never reads
// ahead, buffers a response, or changes Close/error behavior.
type openRouterCostBody struct {
	io.ReadCloser
	line    []byte
	discard bool
	record  func(string)
}

func (b *openRouterCostBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.observe(p[:n])
	}
	if err == io.EOF && len(b.line) > 0 && !b.discard {
		b.consumeLine(b.line)
		b.line = b.line[:0]
	}
	return n, err
}

func (b *openRouterCostBody) observe(chunk []byte) {
	for _, c := range chunk {
		if c == '\n' {
			if !b.discard {
				b.consumeLine(b.line)
			}
			b.line = b.line[:0]
			b.discard = false
			continue
		}
		if !b.discard {
			if len(b.line) >= maxOpenRouterSSELine {
				b.line = b.line[:0]
				b.discard = true
			} else {
				b.line = append(b.line, c)
			}
		}
	}
}

func (b *openRouterCostBody) consumeLine(line []byte) {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(line[len("data:"):])
	amount, _, _, ok := reconciliation.ParseReportedCost(data, "https://openrouter.ai")
	if ok && b.record != nil {
		b.record(amount)
	}
}

func observeOpenRouterSSE(respContentType, endpoint string, body io.ReadCloser, record func(string)) io.ReadCloser {
	if body == nil || !strings.Contains(strings.ToLower(respContentType), "text/event-stream") || !strings.EqualFold(endpointHost(endpoint), "openrouter.ai") {
		return body
	}
	return &openRouterCostBody{ReadCloser: body, record: record}
}

func endpointHost(endpoint string) string {
	if i := strings.Index(endpoint, "://"); i >= 0 {
		endpoint = endpoint[i+3:]
	}
	if i := strings.IndexAny(endpoint, "/?#"); i >= 0 {
		endpoint = endpoint[:i]
	}
	if i := strings.LastIndex(endpoint, ":"); i >= 0 {
		endpoint = endpoint[:i]
	}
	return endpoint
}

func (e *Executor) observeOpenRouterCost(attempt reconciliation.Attempt, resp *http.Response) {
	if resp == nil || resp.Body == nil || e == nil || e.AttemptRecorder == nil {
		return
	}
	resp.Body = observeOpenRouterSSE(resp.Header.Get("Content-Type"), reconciliation.ResponseEndpoint(resp), resp.Body, func(amount string) {
		u := reconciliation.UsageEvidence{ReportedAmount: &amount, ReportedCurrency: "USD", ReportedSemantics: "openrouter_usage_cost_usd", Source: "provider_sse:openrouter"}
		e.recordAttemptUsage(attempt, u, nil, "")
	})
}
