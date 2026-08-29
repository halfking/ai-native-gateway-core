package streaming

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/sse"
)

// NativeResponsesEvent is one complete upstream Responses SSE event. Raw is
// retained as the client-facing wire representation; the decoded payload is
// only used for classification and side-channel capture.
type NativeResponsesEvent struct {
	Name    string
	Data    []byte
	Raw     []byte
	Class   FrameClass
	Payload map[string]json.RawMessage
}

// NativeResponsesEventReader reads bounded Responses SSE events without
// normalizing their bytes. It supports multiline data, comments, CRLF/LF and
// an unterminated final event.
type NativeResponsesEventReader struct {
	reader *sse.LineReader
}

func NewNativeResponsesEventReader(r io.Reader, maxLineBytes int) *NativeResponsesEventReader {
	return &NativeResponsesEventReader{reader: sse.NewLineReader(r, maxLineBytes)}
}

func (r *NativeResponsesEventReader) ReadEvent(ctx context.Context, timeout time.Duration, closer io.Closer) (NativeResponsesEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type result struct {
		event NativeResponsesEvent
		err   error
	}
	readCtx := ctx

	cancel := func() {}
	if timeout > 0 {
		readCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	ch := make(chan result, 1)
	go func() {
		// Audit-2026-08-29 (hardening §4 #1): if readEvent panics, the channel
		// send is skipped and the reader goroutine dies. The caller in
		// ReadEvent blocks on the select until readCtx times out, leaving
		// StreamNativeResponsesSSE hung for the entire stream chunk timeout.
		// LineReader is known safe today, but contract surface includes
		// finishNativeResponsesEvent / json.Unmarshal on attacker-controlled
		// data — guard the goroutine so a panic becomes an error, never a
		// silent reader death.
		event, err := func() (ev NativeResponsesEvent, retErr error) {
			defer func() {
				if r := recover(); r != nil {
					retErr = fmt.Errorf("native Responses SSE read panic: %v", r)
				}
			}()
			return r.readEvent(readCtx)
		}()
		ch <- result{event: event, err: err}
	}()
	select {
	case got := <-ch:
		return got.event, got.err
	case <-readCtx.Done():
		if closer != nil {
			_ = closer.Close()
		}
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return NativeResponsesEvent{}, fmt.Errorf("native Responses SSE read timeout: %w", readCtx.Err())
		}
		return NativeResponsesEvent{}, readCtx.Err()
	}
}

func (r *NativeResponsesEventReader) readEvent(ctx context.Context) (NativeResponsesEvent, error) {
	if r == nil || r.reader == nil {
		return NativeResponsesEvent{}, io.EOF
	}
	var event NativeResponsesEvent
	var dataLines []string
	var raw strings.Builder
	for {
		select {
		case <-ctx.Done():
			return event, ctx.Err()
		default:
		}
		line, err := r.reader.ReadLine()
		raw.WriteString(line)
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if len(dataLines) == 0 {
				if raw.Len() > len(line) {
					event.Raw = []byte(raw.String())
					event.Class = FrameClassKeepalive
					return event, nil
				}
				if err != nil {
					return event, err
				}
				continue
			}
			return finishNativeResponsesEvent(event, dataLines, []byte(raw.String()))
		}
		switch {
		case strings.HasPrefix(trimmed, "event:"):
			event.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		case strings.HasPrefix(trimmed, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		case strings.HasPrefix(trimmed, ":"):
			// Comment/heartbeat is preserved in Raw but has no payload.
		}
		if err != nil {
			if len(dataLines) > 0 {
				return finishNativeResponsesEvent(event, dataLines, []byte(raw.String()))
			}
			return event, io.EOF
		}
	}
}

func finishNativeResponsesEvent(event NativeResponsesEvent, dataLines []string, raw []byte) (NativeResponsesEvent, error) {
	event.Raw = append([]byte(nil), raw...)
	event.Data = []byte(strings.Join(dataLines, "\n"))
	if len(event.Data) == 0 {
		event.Class = FrameClassAttemptMetadata
		return event, nil
	}
	if string(event.Data) != "[DONE]" {
		if err := json.Unmarshal(event.Data, &event.Payload); err != nil {
			return event, fmt.Errorf("native Responses SSE event %q: invalid JSON: %w", event.Name, err)
		}
	}
	event.Class = classifyNativeResponsesEvent(event.Name, event.Data)
	return event, nil
}

func classifyNativeResponsesEvent(name string, data []byte) FrameClass {
	frame := "event: " + name + "\ndata: " + string(data) + "\n\n"
	return ClassifyClientFrame(ProtocolOpenAIResponses, frame)
}

// StreamNativeResponsesSSE forwards a verified native Responses stream without
// generating bridge scaffold or terminal events. Raw event bytes are written
// unchanged; parsing only drives capture and retry semantics.
func StreamNativeResponsesSSE(ctx context.Context, w http.ResponseWriter, resp *http.Response, requestID string, capture *audit.StreamCapture) (outcome StreamOutcome) {
	if resp == nil || resp.Body == nil {
		return StreamOutcome{Interrupted: true, Reason: "empty_response", Kind: errorsx.KindUpstreamDown, Resumable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StreamOutcome{Interrupted: true, Reason: "upstream_status", Kind: errorsx.KindTransient, Resumable: true}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w, gate := wrapAttemptWriter(ctx, w, ProtocolOpenAIResponses)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return StreamOutcome{Interrupted: true, Reason: "no_flusher", Kind: errorsx.KindUpstreamDown, Resumable: true}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusOK)
	if !safeFlush(flusher) {
		return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false}
	}

	reader := NewNativeResponsesEventReader(bufio.NewReaderSize(resp.Body, streamBufSize), currentStreamRuntimeConfig().sseMaxLineBytes)
	chunkCount := 0
	terminal := false
	for {
		event, err := reader.ReadEvent(ctx, currentStreamRuntimeConfig().streamChunkTimeout, resp.Body)
		if err != nil {
			if errors.Is(err, io.EOF) && terminal {
				if capture != nil {
					capture.MarkDone()
				}
				return StreamOutcome{ChunkCount: chunkCount}
			}
			resumable := !attemptHasClientSemanticOutput(gate, chunkCount)
			reason := "native_responses_read_error"
			kind := errorsx.KindUpstreamDown
			if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
				reason, kind = "stream_timeout", errorsx.KindStreamTimeout
			}
			if errors.Is(err, sse.ErrLineTooLong) {
				reason = "stream_line_too_large"
			}
			if capture != nil {
				capture.MarkInterruptedWithReason(reason)
			}
			return StreamOutcome{Interrupted: true, Reason: reason, Kind: kind, Resumable: resumable, ChunkCount: chunkCount}
		}
		if event.Name == "" && len(event.Data) == 0 {
			continue
		}
		if _, err := w.Write(event.Raw); err != nil {
			if capture != nil {
				capture.MarkInterruptedWithReason("client_write_failed")
			}
			return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false, ChunkCount: chunkCount}
		}
		if !safeFlush(flusher) {
			if capture != nil {
				capture.MarkInterruptedWithReason("client_write_failed")
			}
			return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false, ChunkCount: chunkCount}
		}
		if event.Class == FrameClassContent || event.Class == FrameClassToolCall || event.Class == FrameClassUnknown {
			chunkCount++
			if capture != nil {
				capture.RecordChunkSent()
				observeNativeResponsesEvent(capture, event)
			}
		} else if capture != nil {
			// Lifecycle/terminal frames can still carry native usage and model
			// metadata; observe them without affecting semantic chunk counts.
			observeNativeResponsesEvent(capture, event)
		}
		if capture != nil {
			switch event.Name {
			case "response.completed", "response.incomplete":
				// MarkDone only on the first canonical terminal event: an
				// earlier response.failed already finalized the capture as
				// interrupted, and MarkDone must not overwrite that outcome.
				// (MarkDone currently does not clear the interrupted flag,
				// but guard against the ordering in case that changes.)
				if !terminal {
					capture.MarkDone()
				}
				terminal = true
			case "response.failed":
				// The failed terminal event is already client-visible. Count it
				// as sent so dispatch cannot mistake a failure-only stream for a
				// pre-commit attempt and switch credentials after exposing it.
				capture.RecordChunkSent()
				capture.MarkInterruptedWithReason("native_response_failed")
				terminal = true
			}
		}
		if event.Name == "response.completed" || event.Name == "response.incomplete" || event.Name == "response.failed" {
			terminal = true
		}
		if event.Name == "error" {
			return StreamOutcome{Interrupted: true, Reason: "native_response_error", Kind: errorsx.KindUpstreamDown, Resumable: !attemptHasClientSemanticOutput(gate, chunkCount), ChunkCount: chunkCount}
		}
		if event.Name == "response.failed" {
			return StreamOutcome{Interrupted: true, Reason: "native_response_failed", Kind: errorsx.KindUpstreamDown, Resumable: false, ChunkCount: chunkCount}
		}
	}
}
