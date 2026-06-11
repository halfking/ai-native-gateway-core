package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
)

func StreamResponsesSSE(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture) (outcome StreamOutcome) {
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("responses stream panic recovered", "panic", r, "stack", string(debug.Stack()), "request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
		}
	}()
	runtimeCfg := currentStreamRuntimeConfig()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return StreamOutcome{}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	respID := "resp_"
	msgID := "msg_"
	if len(requestID) > 24 {
		respID += requestID[:24]
		msgID += requestID[8:24]
	} else {
		respID += requestID
		msgID += requestID
	}
	createdAt := int(time.Now().Unix())

	initialResp := map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      clientModel,
			"status":     "in_progress",
			"output":     []any{},
		},
	}
	writeSSE(w, "response.created", initialResp)
	flusher.Flush()

	outputItem := map[string]any{
		"type": "response.output_item.added",
		"output_index": 0,
		"item": map[string]any{
			"type":   "message",
			"id":     msgID,
			"status": "in_progress",
			"role":   "assistant",
			"content": []any{},
		},
	}
	writeSSE(w, "response.output_item.added", outputItem)
	flusher.Flush()

	contentPart := map[string]any{
		"type":          "response.content_part.added",
		"item_id":       msgID,
		"output_index":  0,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		},
	}
	writeSSE(w, "response.content_part.added", contentPart)
	flusher.Flush()

	var ctx context.Context
	if resp.Request != nil {
		ctx = resp.Request.Context()
	} else {
		ctx = context.Background()
	}

	reader := bufio.NewReaderSize(resp.Body, streamBufSize)
	lastSend := time.Now()

	fullText := ""
	finalFinishReason := ""
	promptTokens := 0
	completionTokens := 0

	firstLine, err := readLineWithTimeout(ctx, reader, runtimeCfg.firstByteTimeout)
	if err != nil {
		if capture != nil {
			capture.MarkInterruptedWithReason("first_byte_timeout")
		}
		slog.Warn("responses stream first-byte timeout", "error", err)
		writeResponsesIncomplete(w, flusher, respID, msgID, createdAt, clientModel, fullText, "first_byte_timeout")
		outcome.Interrupted = true
		outcome.Reason = "first_byte_timeout"
		return outcome
	}

	processLine := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			return
		}
		data := line[6:]
		if data == "[DONE]" {
			return
		}

		var chunk map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return
		}

		if raw, ok := chunk["usage"]; ok {
			var usage map[string]any
			if json.Unmarshal(raw, &usage) == nil {
				if v, ok := usage["prompt_tokens"].(float64); ok {
					promptTokens = int(v)
				}
				if v, ok := usage["completion_tokens"].(float64); ok {
					completionTokens = int(v)
				}
			}
			if capture != nil {
				pt := promptTokens
				ct := completionTokens
				capture.ObserveUsage(&pt, &ct, nil, nil)
			}
		}

		var choices []map[string]any
		if raw, ok := chunk["choices"]; ok {
			json.Unmarshal(raw, &choices)
		}
		if len(choices) == 0 {
			return
		}

		choice := choices[0]
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			finalFinishReason = fr
		}

		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			return
		}

		textDelta, _ := delta["content"].(string)
		if textDelta != "" {
			fullText += textDelta
			deltaEvent := map[string]any{
				"type":          "response.output_text.delta",
				"item_id":       msgID,
				"output_index":  0,
				"content_index": 0,
				"delta":         textDelta,
			}
			writeSSE(w, "response.output_text.delta", deltaEvent)
			lastSend = time.Now()
			if capture != nil {
				capture.ObservePayload(fmt.Sprintf(`{"delta":%q,"item_id":%q}`, textDelta, msgID), "", false)
			}
		}
	}

	if firstLine != "" {
		processLine(firstLine)
	}

	for {
		stop := false
		readResult := readNextStreamLine(ctx, reader, w, &lastSend, runtimeCfg)
		if readResult.err != nil {
			switch readResult.state {
			case streamReadCanceled:
				slog.Debug("responses stream client disconnected")
				if capture != nil {
					capture.MarkInterruptedWithReason("client_disconnected")
				}
				writeResponsesIncomplete(w, flusher, respID, msgID, createdAt, clientModel, fullText, "client_disconnected")
				outcome.Interrupted = true
				outcome.Reason = "client_cancel"
				return outcome
			case streamReadEOF:
				stop = true
			case streamReadTimeout:
				slog.Warn("responses stream read timeout", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_timeout")
				}
				writeResponsesIncomplete(w, flusher, respID, msgID, createdAt, clientModel, fullText, "stream_timeout")
				outcome.Interrupted = true
				outcome.Reason = "stream_timeout"
				return outcome
			default:
				slog.Warn("responses stream read error", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_error")
				}
				writeResponsesIncomplete(w, flusher, respID, msgID, createdAt, clientModel, fullText, "upstream_error")
				outcome.Interrupted = true
				outcome.Reason = "read_error"
				return outcome
			}
		}

		if stop {
			break
		}

		line := readResult.line

		if line == "" {
			continue
		}
		processLine(line)
	}

	textDone := map[string]any{
		"type":          "response.output_text.done",
		"item_id":       msgID,
		"output_index":  0,
		"content_index": 0,
		"text":          fullText,
	}
	writeSSE(w, "response.output_text.done", textDone)

	itemStatus := "completed"
	if finalFinishReason == "length" {
		itemStatus = "incomplete"
	}

	doneItem := map[string]any{
		"type": "response.output_item.done",
		"output_index": 0,
		"item": map[string]any{
			"type":   "message",
			"id":     msgID,
			"status": itemStatus,
			"role":   "assistant",
			"content": []map[string]any{
				{"type": "output_text", "text": fullText, "annotations": []any{}},
			},
		},
	}
	writeSSE(w, "response.output_item.done", doneItem)

	completedResp := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      clientModel,
			"status":     itemStatus,
			"output": []map[string]any{
				{
					"type":   "message",
					"id":     msgID,
					"status": itemStatus,
					"role":   "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": fullText, "annotations": []any{}},
					},
				},
			},
		},
	}
	writeSSE(w, "response.completed", completedResp)
	flusher.Flush()

	if capture != nil {
		capture.ObservePayload(`{"type":"response.completed"}`, finalFinishReason, true)
	}
	return outcome
}

func writeResponsesIncomplete(w http.ResponseWriter, flusher http.Flusher, respID, msgID string, createdAt int, clientModel, fullText, reason string) {
	var output []map[string]any
	if fullText != "" {
		output = []map[string]any{
			{
				"type":   "message",
				"id":     msgID,
				"status": "incomplete",
				"role":   "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": fullText, "annotations": []any{}},
				},
			},
		}
	}

	incompleteResp := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      clientModel,
			"status":     "incomplete",
			"incomplete_details": map[string]any{"reason": reason},
			"output":     output,
		},
	}
	writeSSE(w, "response.completed", incompleteResp)
	flusher.Flush()
}
