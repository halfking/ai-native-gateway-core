package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
)

const (
	streamBufSize       = 64 * 1024
	sseKeepaliveComment = ": keep-alive\n\n"
)

func StreamChat(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm *Normalizer) {
	StreamChatWithCapture(w, resp, clientModel, outboundModel, norm, nil)
}

func StreamChatWithCapture(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm *Normalizer, capture *audit.StreamCapture) {
	StreamChatWithCaptureAndToolFallback(w, resp, clientModel, outboundModel, norm, capture, false)
}

func StreamChatWithCaptureAndToolFallback(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm *Normalizer, capture *audit.StreamCapture, toolsRequested bool) {
	defer resp.Body.Close()
	runtimeCfg := currentStreamRuntimeConfig()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var ctx context.Context
	if resp.Request != nil {
		ctx = resp.Request.Context()
	} else {
		ctx = context.Background()
	}

	reader := bufio.NewReaderSize(resp.Body, streamBufSize)
	discoveredUpstream := ""
	lastSend := time.Now()

	if clientModel != "" && outboundModel != "" && clientModel != outboundModel {
		slog.Debug("upstream model diff",
			"client_model", clientModel,
			"outbound_model", outboundModel,
		)
	}

	// ── First-byte timeout ──────────────────────────────────────────
	firstLine, err := readLineWithTimeout(ctx, reader, runtimeCfg.firstByteTimeout)
	if err != nil {
		if capture != nil {
			capture.MarkInterruptedWithReason("first_byte_timeout")
		}
		slog.Warn("stream first-byte timeout", "error", err)
		safeWriteSSE(w, "data: {\"error\":{\"message\":\"upstream first-byte timeout\",\"type\":\"timeout\",\"code\":\"first_byte_timeout\"}}\n\n")
		safeFlush(flusher)
		return
	}

	if firstLine != "" {
		firstLine = coerceXMLToolCallsInStreamLine(firstLine, toolsRequested)
		if clientModel != "" && discoveredUpstream == "" {
			discoveredUpstream = extractModelFromChunk(firstLine)
		}
		if clientModel != "" {
			firstLine = replaceModelInChunk(firstLine, clientModel, discoveredUpstream)
		}

		payload := extractPayload(firstLine)
		if payload != "" && capture != nil {
			finishReason := ExtractFinishReason(payload)
			usage := ExtractUsageFromChunk(payload)
			capture.ObservePayload(payload, finishReason, false)
			if usage.PromptTokens != nil || usage.CompletionTokens != nil {
				capture.ObserveUsage(usage.PromptTokens, usage.CompletionTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
			}
		}

		if norm != nil {
			firstLine = string(norm.NormalizeChunk([]byte(firstLine), true))
		}
		safeWriteSSE(w, firstLine)
		safeFlush(flusher)
		lastSend = time.Now()
	}

	// ── Main streaming loop with keep-alive ─────────────────────────
	for {
		select {
		case <-ctx.Done():
			if capture != nil {
				capture.MarkInterruptedWithReason("client_cancel")
			}
			return
		default:
		}

		readResult := readNextStreamLine(ctx, reader, w, &lastSend, runtimeCfg)
		if readResult.err != nil {
			switch readResult.state {
			case streamReadEOF:
				safeWriteSSE(w, "data: [DONE]\n\n")
				safeFlush(flusher)
				if capture != nil {
					capture.ObservePayload("[DONE]", "", true)
				}
			case streamReadCanceled:
				slog.Debug("stream cancelled by client")
				if capture != nil {
					capture.MarkInterruptedWithReason("client_cancel")
				}
			case streamReadTimeout:
				slog.Warn("stream read timeout, sending error chunk")
				safeWriteSSE(w, "data: {\"error\":{\"message\":\"upstream read timeout\",\"type\":\"timeout\",\"code\":\"stream_timeout\"}}\n\n")
				safeFlush(flusher)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_timeout")
				}
			default:
				slog.Warn("stream read error", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("read_error")
				}
			}
			return
		}

		line := readResult.line

		line = coerceXMLToolCallsInStreamLine(line, toolsRequested)

		if clientModel != "" && discoveredUpstream == "" {
			discoveredUpstream = extractModelFromChunk(line)
		}
		if clientModel != "" {
			line = replaceModelInChunk(line, clientModel, discoveredUpstream)
		}

		payload := extractPayload(line)
		if payload != "" && capture != nil {
			finishReason := ExtractFinishReason(payload)
			usage := ExtractUsageFromChunk(payload)
			isDone := payload == "[DONE]"
			capture.ObservePayload(payload, finishReason, isDone)
			if usage.PromptTokens != nil || usage.CompletionTokens != nil {
				capture.ObserveUsage(usage.PromptTokens, usage.CompletionTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
			}
		}

		if norm != nil {
			line = string(norm.NormalizeChunk([]byte(line), true))
		}

		safeWriteSSE(w, line)
		safeFlush(flusher)
		lastSend = time.Now()
	}
}

func extractPayload(line string) string {
	if !strings.HasPrefix(line, "data: ") {
		return ""
	}
	payload := strings.TrimPrefix(line, "data: ")
	return strings.TrimSpace(payload)
}

func readLineWithTimeout(ctx context.Context, reader *bufio.Reader, timeout time.Duration) (string, error) {
	return newTimedLineReader(reader).ReadLine(ctx, timeout)
}

type timedLineReader struct {
	reader *bufio.Reader
}

func newTimedLineReader(reader *bufio.Reader) *timedLineReader {
	return &timedLineReader{reader: reader}
}

func (r *timedLineReader) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{"", fmt.Errorf("read panic: %v", r)}
			}
		}()
		line, err := r.reader.ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case r := <-ch:
		return r.line, r.err
	case <-readCtx.Done():
		if readCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("stream read timeout")
		}
		return "", readCtx.Err()
	}
}

func extractModelFromChunk(line string) string {
	if !strings.HasPrefix(line, "data: ") || strings.HasPrefix(line, "data: [DONE") {
		return ""
	}
	jsonStr := strings.TrimPrefix(line, "data: ")
	jsonStr = strings.TrimSpace(jsonStr)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
		return ""
	}
	if modelRaw, ok := obj["model"]; ok {
		var modelStr string
		if err := json.Unmarshal(modelRaw, &modelStr); err == nil {
			return modelStr
		}
	}
	return ""
}

func safeFlush(flusher http.Flusher) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("flush after close", "recover", r)
		}
	}()
	flusher.Flush()
}

func safeWriteSSE(w io.Writer, line string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("write after close", "recover", r)
		}
	}()
	io.WriteString(w, line)
}

func replaceModelInChunk(line, clientModel, discoveredUpstream string) string {
	if !strings.HasPrefix(line, "data: ") || clientModel == "" {
		return line
	}
	if strings.HasPrefix(line, "data: [DONE") {
		return line
	}
	jsonStr := strings.TrimPrefix(line, "data: ")
	jsonStr = strings.TrimSpace(jsonStr)

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
		return line
	}
	modelRaw, ok := obj["model"]
	if !ok {
		return line
	}
	var modelStr string
	if err := json.Unmarshal(modelRaw, &modelStr); err != nil {
		return line
	}
	if modelStr == clientModel {
		return line
	}
	if discoveredUpstream != "" && modelStr != discoveredUpstream {
		return line
	}
	obj["model"], _ = json.Marshal(clientModel)
	newJSON, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	return "data: " + string(newJSON) + "\n"
}

func BuildSSEChunk(data string) string {
	var b strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		fmt.Fprintf(&b, "data: %s\n", scanner.Text())
	}
	b.WriteString("\n")
	return b.String()
}
