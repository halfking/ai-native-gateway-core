package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/routing"
)

const anthropicSSEBufSize = 64 * 1024

// textDelta captures a single upstream content_block_delta for a text
// block, with both the parsed text payload and the raw SSE bytes so we
// can either forward verbatim or rewrite as a [thinking, text] split
// when content_block_stop arrives.
type textDelta struct {
	text string
	raw  []byte
}

// textBlockAccumulator buffers text-delta payloads from a single
// upstream text content block until content_block_stop, so that the
// accumulated text can be inspected for an embedded `<think>...</think>`
// segment and rewritten as [thinking, text] before being flushed to
// the client. Non-text blocks (tool_use, etc.) flush immediately and
// never enter the accumulator.
type textBlockAccumulator struct {
	index        int
	started      bool
	deltas       []textDelta
	eventPayload []byte // raw SSE event bytes for content_block_start (so we can rewrite the type if split occurs)
}

// flushBufferedTextBlock emits the buffered text block either as the
// original (single) text block, or split into [thinking, text] if the
// accumulated text begins with `<think>...</think>`. It writes the
// rewritten SSE events to w and returns whether a thinking block was
// emitted so the caller can update side-channel accounting.
func (acc *textBlockAccumulator) flushBufferedTextBlock(w http.ResponseWriter, flusher http.Flusher) (hasThinking bool, err error) {
	if !acc.started {
		return false, nil
	}
	if len(acc.deltas) == 0 {
		// No content was buffered; emit the original content_block_start
		// and stop events (rewritten from eventPayload if present) so the
		// SDK sees a coherent block.
		if acc.eventPayload != nil {
			if _, werr := w.Write(acc.eventPayload); werr != nil {
				return false, werr
			}
		}
		return false, nil
	}

	var sb strings.Builder
	for _, d := range acc.deltas {
		sb.WriteString(d.text)
	}
	fullText := sb.String()

	think, rest, ok := splitLeadingThinkBlock(fullText)
	if !ok {
		// No <think> prefix: forward each delta verbatim (we never
		// forwarded them earlier because we were buffering).
		for _, d := range acc.deltas {
			if _, werr := w.Write(d.raw); werr != nil {
				return false, werr
			}
		}
		flusher.Flush()
		return false, nil
	}

	// Emit split: thinking block (start + delta + stop), then text block
	// (start + remaining-delta + stop) with re-numbered indices. We
	// never forwarded the original content_block_start or any text_delta,
	// so we don't need to close the original (un-emitted) text block.
	if err := writeSSEEvent(w, "content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         acc.index,
		"content_block": map[string]any{"type": "thinking", "thinking": ""},
	}); err != nil {
		return false, err
	}
	if err := writeSSEEvent(w, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": acc.index,
		"delta": map[string]any{"type": "thinking_delta", "thinking": think},
	}); err != nil {
		return false, err
	}
	if err := writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": acc.index}); err != nil {
		return false, err
	}

	newIndex := acc.index + 1
	if rest != "" {
		if err := writeSSEEvent(w, "content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         newIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		}); err != nil {
			return false, err
		}
		if err := writeSSEEvent(w, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": newIndex,
			"delta": map[string]any{"type": "text_delta", "text": rest},
		}); err != nil {
			return false, err
		}
		if err := writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": newIndex}); err != nil {
			return false, err
		}
	}
	flusher.Flush()
	return true, nil
}

// writeSSEEvent writes one SSE event (event: NAME\ndata: ...\n\n) to w.
func writeSSEEvent(w http.ResponseWriter, eventName string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(eventName)
	buf.WriteByte('\n')
	buf.WriteString("data: ")
	buf.Write(body)
	buf.WriteByte('\n')
	buf.WriteByte('\n')
	_, werr := w.Write(buf.Bytes())
	return werr
}

// StreamAnthropicPassthrough is the Q4 Anthropic SSE forwarder. It reads
// Anthropic-format SSE events from upstream and writes them to the
// client mostly unchanged (byte-for-byte), while scanning for
// has_thinking / usage accounting in the side-channel audit capture.
//
// Upstream quirks handled here:
//   - Some Anthropic-compatible providers (minimax) emit the reasoning
//     trace inside a single text block as `<think>...</think>` rather
//     than as an independent thinking block. We buffer the text-block
//     deltas until content_block_stop, then either forward verbatim
//     (no think prefix) or rewrite the SSE stream as [thinking, text]
//     with re-numbered indices so SDKs render the trace separately.
//
// This is the "Q4" path: client speaks Anthropic, upstream speaks
// Anthropic (e.g. anthropic provider, or minimax's /anthropic
// compatible endpoint), no protocol conversion required.
func StreamAnthropicPassthrough(
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
) routing.StreamOutcome {
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic passthrough panic", "panic", r, "stack", string(debug.Stack()))
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
		}
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return routing.StreamOutcome{Interrupted: true, Reason: "no_flusher"}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	reader := bufio.NewReaderSize(resp.Body, anthropicSSEBufSize)
	outcome := routing.StreamOutcome{}

	var pendingEvent string
	var pendingPayload string
	var textAcc *textBlockAccumulator

	flushPending := func() error {
		if pendingEvent == "" {
			return nil
		}
		var buf bytes.Buffer
		buf.WriteString("event: ")
		buf.WriteString(pendingEvent)
		buf.WriteByte('\n')
		buf.WriteString("data: ")
		buf.WriteString(pendingPayload)
		buf.WriteByte('\n')
		buf.WriteByte('\n')
		_, werr := w.Write(buf.Bytes())
		pendingEvent = ""
		pendingPayload = ""
		return werr
	}

	flushBlank := func() error {
		if err := flushPending(); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			outcome.Interrupted = true
			outcome.Reason = "read_error"
			if capture != nil {
				capture.MarkInterruptedWithReason("read_error")
			}
			return outcome
		}

		if strings.HasPrefix(line, "event: ") {
			pendingEvent = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		} else if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimRight(strings.TrimPrefix(line, "data: "), "\n")
			pendingPayload = payload

			if capture != nil {
				observeAnthropicPayload(capture, payload, clientModel, outboundModel)
			}

			// While buffering a text block, capture text deltas instead
			// of forwarding.
			if textAcc != nil && pendingEvent == "content_block_delta" {
				var d struct {
					Delta struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(payload), &d); err == nil && d.Delta.Type == "text_delta" {
					textAcc.deltas = append(textAcc.deltas, textDelta{
						text: d.Delta.Text,
						raw:  []byte(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", payload)),
					})
					pendingEvent = ""
					pendingPayload = ""
					continue
				}
			}

			// Begin buffering on text content_block_start.
			if pendingEvent == "content_block_start" && textAcc == nil {
				var s struct {
					Index        *int `json:"index"`
					ContentBlock *struct {
						Type string `json:"type"`
					} `json:"content_block"`
				}
				if err := json.Unmarshal([]byte(payload), &s); err == nil &&
					s.ContentBlock != nil && s.ContentBlock.Type == "text" {
					idx := 0
					if s.Index != nil {
						idx = *s.Index
					}
					textAcc = &textBlockAccumulator{
						index:        idx,
						started:      true,
						eventPayload: []byte(fmt.Sprintf("event: content_block_start\ndata: %s\n\n", payload)),
					}
					pendingEvent = ""
					pendingPayload = ""
					continue
				}
			}

			// content_block_stop for buffered text block → flush.
			if pendingEvent == "content_block_stop" && textAcc != nil {
				// Drop the original content_block_stop: flushBufferedTextBlock
				// will emit either one (no-split) or two (split) stop events
				// with the right indices.
				hasThink, err := textAcc.flushBufferedTextBlock(w, flusher)
				if err != nil {
					outcome.Interrupted = true
					outcome.Reason = "client_disconnected"
					if capture != nil {
						capture.MarkInterruptedWithReason("client_disconnected")
					}
					return outcome
				}
				if capture != nil && hasThink {
					capture.HasThinking = true
					capture.ThinkingBlocksN++
				}
				textAcc = nil
				pendingEvent = ""
				pendingPayload = ""
				continue
			}

			// Fall through: queue for the blank-line flush below.
		} else if line == "\n" {
			if err := flushBlank(); err != nil {
				outcome.Interrupted = true
				outcome.Reason = "client_disconnected"
				if capture != nil {
					capture.MarkInterruptedWithReason("client_disconnected")
				}
				return outcome
			}
			pendingEvent = ""
			pendingPayload = ""
		}
	}

	// Trailing flush.
	if err := flushPending(); err != nil {
		outcome.Interrupted = true
		outcome.Reason = "client_disconnected"
	}
	if textAcc != nil {
		_, _ = textAcc.flushBufferedTextBlock(w, flusher)
	}
	flusher.Flush()
	return outcome
}

// observeAnthropicPayload inspects a single Anthropic SSE data payload
// and updates the side-channel audit capture accordingly. It handles
// the three event types that carry state worth recording:
//
//   - message_start: contains usage.input_tokens (initial value)
//   - message_delta:  contains usage.output_tokens (cumulative final)
//   - content_block_start with type=thinking: marks the response as
//     containing reasoning, increments the thinking-block counter
//   - any event with a "message.model" field: triggers model-mismatch
//     check against the requested model
func observeAnthropicPayload(c *audit.StreamCapture, payload, clientModel, outboundModel string) {
	if payload == "" || payload == "[DONE]" {
		return
	}
	var v struct {
		Type    string `json:"type"`
		Message *struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens  *int `json:"input_tokens"`
				OutputTokens *int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage *struct {
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
		Index        *int `json:"index"`
		ContentBlock *struct {
			Type string `json:"type"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		return
	}
	switch v.Type {
	case "message_start":
		if v.Message != nil {
			if v.Message.Usage.InputTokens != nil {
				pt := *v.Message.Usage.InputTokens
				c.InputTokens = &pt
			}
			if v.Message.Model != "" {
				checkAnthropicModelMismatch(c, clientModel, outboundModel, v.Message.Model)
			}
		}
	case "message_delta":
		if v.Usage != nil && v.Usage.OutputTokens != nil {
			ot := *v.Usage.OutputTokens
			c.OutputTokens = &ot
		}
	case "message_stop":
		c.MarkDone()
	case "content_block_start":
		if v.ContentBlock != nil && v.ContentBlock.Type == "thinking" {
			c.HasThinking = true
			c.ThinkingBlocksN++
		}
	case "error":
		c.MarkStreamError()
	}
}

// checkAnthropicModelMismatch sets the capture's ModelMismatch flag
// when the upstream response model name does not case-insensitively
// match the clientModel (or outboundModel fallback). This is a
// side-channel detection — most upstreams will report a different
// model name in the response message_start than what the client
// asked for (e.g. "claude-3-5-sonnet-20241022" vs "claude-3-5-sonnet"),
// so we only flag a mismatch when the names diverge in a way the
// caller likely cares about.
func checkAnthropicModelMismatch(c *audit.StreamCapture, clientModel, outboundModel, respModel string) {
	want := clientModel
	if want == "" {
		want = outboundModel
	}
	if want == "" || respModel == "" {
		return
	}
	if !strings.EqualFold(want, respModel) {
		c.ModelMismatch = true
	}
}