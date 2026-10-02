package outputcompliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	sseparser "github.com/kaixuan/llm-gateway-go/internal/sse"
)

// The checker can load arbitrary tenant regexes. Even its built-in password
// rule has unbounded whitespace and value length. No fixed lookbehind or
// lexical boundary proves that bytes already sent are safe. Hold model text
// through the terminal event and inspect each logical output lane as a whole.
// Prelude/control events before model text and exact gateway heartbeat frames
// pass immediately. This can delay visible model text until the
// request ends; bounded-width rules or a streaming-safe parser are required
// before early text release can be proven safe. A per-request cap bounds
// memory; crossing it fails closed, including when no terminal arrives.
const maxCompliancePendingBytes = 1 << 20

type checkedStreamFrame struct {
	original    []byte
	root        map[string]any
	comment     map[string]any
	commentCRLF bool
	rebuild     func([]byte) []byte
	changed     bool
	parts       []*streamTextPart
}

type streamTextPart struct {
	frame    *checkedStreamFrame
	field    visibleTextField
	original string
}

func (f *checkedStreamFrame) wireBytes() ([]byte, error) {
	if f == nil {
		return nil, nil
	}
	if !f.changed {
		return f.original, nil
	}
	if f.comment != nil {
		value, _ := f.comment["text"].(string)
		return renderSSEComment(value, f.commentCRLF), nil
	}
	data, err := json.Marshal(f.root)
	if err != nil {
		return nil, err
	}
	return f.rebuild(data), nil
}

type complianceStreamState struct {
	mu       sync.Mutex
	checkCtx context.Context
	enabled  bool
	redact   bool
	frames   []*checkedStreamFrame
	lanes    map[string][]*streamTextPart
	bytes    int
}

func (it *OutputComplianceInterceptor) streamState(ctx context.Context, meta *response.StreamMeta) *complianceStreamState {
	if meta == nil || meta.State == nil {
		return nil
	}
	key := it.stateKey
	if key == "" {
		key = "output_compliance.pending"
	}
	state, _ := meta.State.GetOrCreate(key, func() any {
		active := it.isEnabled()
		state := &complianceStreamState{enabled: active, checkCtx: outputcompliance.WithRequestPolicyCache(ctx)}
		if active {
			state.redact = it.shouldRedact(ctx, meta.CallerOwner, meta.SessionID, meta.TenantID)
		}
		return state
	}).(*complianceStreamState)
	return state
}

func (it *OutputComplianceInterceptor) processStreamChunk(ctx context.Context, frame []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	if it == nil || it.checker == nil || meta == nil || len(frame) == 0 {
		return nil, nil
	}
	state := it.streamState(ctx, meta)
	if state == nil {
		if !it.isEnabled() {
			return nil, nil
		}
		// No request-local state means adjacent deltas cannot be inspected.
		// Production response writers always populate StreamMeta.State.
		return &response.ChunkResult{ShouldBlock: true}, nil
	}
	if !state.enabled {
		return nil, nil
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if isTrustedSSEHeartbeat(frame) {
		// Gateway-owned fixed transport bytes contain no provider output.
		return nil, nil
	}
	checked, err := it.inspectSSEFrame(state.checkCtx, frame, meta, state.redact)
	if err != nil {
		state.clear()
		return &response.ChunkResult{ShouldBlock: true}, nil
	}
	if isStreamTerminalFrame(frame) {
		prior, err := it.releaseCheckedFrames(state.checkCtx, state, meta)
		if err != nil {
			return &response.ChunkResult{ShouldBlock: true}, nil
		}
		terminal, err := checked.wireBytes()
		if err != nil {
			return &response.ChunkResult{ShouldBlock: true}, nil
		}
		if len(prior) == 0 && bytes.Equal(terminal, frame) {
			return nil, nil
		}
		return &response.ChunkResult{SuppressChunk: true, ModifiedChunk: append(prior, terminal...)}, nil
	}
	if len(state.frames) == 0 && len(checked.parts) == 0 {
		modified, err := checked.wireBytes()
		if err != nil {
			return &response.ChunkResult{ShouldBlock: true}, nil
		}
		if !bytes.Equal(modified, frame) {
			return &response.ChunkResult{ModifiedChunk: modified}, nil
		}
		return nil, nil
	}

	state.frames = append(state.frames, checked)
	state.bytes += len(frame)
	if state.lanes == nil {
		state.lanes = make(map[string][]*streamTextPart)
	}
	for _, part := range checked.parts {
		state.lanes[part.field.lane] = append(state.lanes[part.field.lane], part)
	}
	if state.bytes > maxCompliancePendingBytes {
		state.clear()
		return &response.ChunkResult{ShouldBlock: true}, nil
	}
	return &response.ChunkResult{SuppressChunk: true}, nil
}

// FlushStreamPending handles upstream streams without a recognized terminal.
// It emits no unchecked text. Text release is delayed until this point when
// arbitrary-length regex rules are active, which trades streaming latency for
// a guarantee that no previously sent byte must later be retracted.
func (it *OutputComplianceInterceptor) FlushStreamPending(ctx context.Context, meta *response.StreamMeta) ([]byte, error) {
	if it == nil || it.checker == nil {
		return nil, nil
	}
	state := it.streamState(ctx, meta)
	if state == nil {
		return nil, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return it.releaseCheckedFrames(state.checkCtx, state, meta)
}

func (it *OutputComplianceInterceptor) releaseCheckedFrames(ctx context.Context, state *complianceStreamState, meta *response.StreamMeta) ([]byte, error) {
	if len(state.frames) == 0 {
		return nil, nil
	}
	defer state.clear()
	for lane, parts := range state.lanes {
		if len(parts) < 2 && (len(parts) == 0 || parts[0].field.label != "__tool_json") {
			continue
		}
		var joined strings.Builder
		for _, part := range parts {
			joined.WriteString(part.original)
		}
		original := joined.String()
		result, err := it.checkText(ctx, meta.TenantID, parts[0].field.label, original)
		if err != nil || result == nil {
			return nil, fmt.Errorf("check stream lane %q: %w", lane, errOrMissingResult(err))
		}
		if result.Blocked {
			return nil, errors.New("output policy blocked stream lane")
		}
		if !state.redact || result.RedactedOutput == "" || result.RedactedOutput == original {
			continue
		}
		for _, part := range parts {
			if part.field.immutable || (it.mandatory && (isToolArgumentLane(lane) || part.field.label != "")) {
				return nil, errors.New("cannot safely rewrite signed or sensitive tool stream lane")
			}
		}
		// Tool-call argument deltas form one JSON document. Moving a rewrite
		// to the first delta is valid only when the reconstructed arguments
		// remain JSON; otherwise fail closed rather than break the client tool.
		if parts[0].field.label == "__tool_json" && json.Valid([]byte(original)) && !json.Valid([]byte(result.RedactedOutput)) {
			return nil, errors.New("redacted tool arguments are invalid JSON")
		}
		parts[0].field.set(result.RedactedOutput)
		parts[0].frame.changed = true
		for _, part := range parts[1:] {
			part.field.set("")
			part.frame.changed = true
		}
	}
	var output []byte
	for _, frame := range state.frames {
		rendered, err := frame.wireBytes()
		if err != nil {
			return nil, err
		}
		output = append(output, rendered...)
	}
	return output, nil
}

func isJSONToolArgumentLane(lane string) bool {
	return (strings.Contains(lane, ".tool.") && strings.HasSuffix(lane, ".arguments")) ||
		strings.HasSuffix(lane, ".function_call.arguments") || strings.HasSuffix(lane, ".partial_json") ||
		strings.Contains(lane, "response.function_call_arguments.") ||
		(strings.HasPrefix(lane, "responses.") && (strings.HasSuffix(lane, ".arguments") || strings.HasSuffix(lane, ".arguments.snapshot"))) ||
		(strings.HasPrefix(lane, "anthropic.block.") && strings.HasSuffix(lane, ".input"))
}

func isToolArgumentLane(lane string) bool {
	return isJSONToolArgumentLane(lane) || strings.Contains(lane, ".tool_use.input") ||
		(strings.HasPrefix(lane, "responses.") && (strings.HasSuffix(lane, ".input") ||
			strings.HasSuffix(lane, ".custom_input") || strings.HasSuffix(lane, ".custom_input.snapshot")))
}

func isTrustedSSEHeartbeat(frame []byte) bool {
	return bytes.Equal(frame, []byte(": keep-alive\n\n")) ||
		bytes.Equal(frame, []byte(": keep-alive\r\n\r\n")) ||
		bytes.Equal(frame, []byte(": gw-survival-keepalive\n\n")) ||
		bytes.Equal(frame, []byte(": gw-survival-keepalive\r\n\r\n")) ||
		bytes.Equal(frame, []byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
}

func isSSECommentOnlyFrame(frame []byte) bool {
	comments := false
	for _, line := range bytes.Split(frame, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 {
			continue
		}
		if line[0] != ':' {
			return false
		}
		comments = true
	}
	return comments
}

func stripUntrustedSSEComments(frame []byte) []byte {
	lines := bytes.Split(frame, []byte{'\n'})
	kept := make([][]byte, 0, len(lines))
	for _, line := range lines {
		trimmed := bytes.TrimSuffix(line, []byte{'\r'})
		if len(trimmed) > 0 && trimmed[0] == ':' {
			continue
		}
		kept = append(kept, line)
	}
	return bytes.Join(kept, []byte{'\n'})
}

func sseCommentText(frame []byte) string {
	var parts []string
	for _, line := range bytes.Split(frame, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 || line[0] != ':' {
			continue
		}
		parts = append(parts, strings.TrimPrefix(string(line[1:]), " "))
	}
	return strings.Join(parts, "\n")
}

func renderSSEComment(value string, crlf bool) []byte {
	ending := "\n"
	if crlf {
		ending = "\r\n"
	}
	var out strings.Builder
	for _, line := range strings.Split(value, "\n") {
		out.WriteString(": ")
		out.WriteString(line)
		out.WriteString(ending)
	}
	out.WriteString(ending)
	return []byte(out.String())
}

func (state *complianceStreamState) clear() {
	state.frames = nil
	state.lanes = nil
	state.bytes = 0
}

func errOrMissingResult(err error) error {
	if err != nil {
		return err
	}
	return errors.New("checker returned nil result")
}

func (it *OutputComplianceInterceptor) shouldRedact(ctx context.Context, callerOwner, sessionID, tenantID string) bool {
	if it.mandatory {
		return true
	}
	dataOwner := ""
	if it.ownerFn != nil {
		dataOwner = it.ownerFn(ctx, sessionID, tenantID)
	}
	return outputcompliance.ShouldRedact(redactionMode(), callerOwner, dataOwner)
}

func isStreamTerminalFrame(frame []byte) bool {
	data, _, err := sseFrameData(frame)
	if err != nil {
		return false
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return true
	}
	var payload struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &payload) == nil {
		switch payload.Type {
		case "message_stop", "response.completed", "response.failed", "response.incomplete":
			return true
		}
	}
	for _, line := range bytes.Split(frame, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if bytes.HasPrefix(line, []byte("event:")) {
			switch strings.TrimSpace(string(line[len("event:"):])) {
			case "message_stop", "response.completed", "response.failed", "response.incomplete":
				return true
			}
		}
	}
	return false
}

func (it *OutputComplianceInterceptor) inspectSSEFrame(ctx context.Context, frame []byte, meta *response.StreamMeta, shouldRedact bool) (*checkedStreamFrame, error) {
	if isSSECommentOnlyFrame(frame) {
		text := sseCommentText(frame)
		checked := &checkedStreamFrame{
			original:    append([]byte(nil), frame...),
			comment:     map[string]any{"text": text},
			commentCRLF: bytes.Contains(frame, []byte("\r\n")),
		}
		result, err := it.checker.Check(ctx, meta.TenantID, text)
		if err != nil || result == nil {
			return nil, errOrMissingResult(err)
		}
		if result.Blocked {
			return nil, errors.New("output policy blocked stream comment")
		}
		checked.parts = append(checked.parts, &streamTextPart{
			frame:    checked,
			field:    visibleTextField{parent: checked.comment, key: "text", lane: "sse.comment"},
			original: text,
		})
		if shouldRedact && result.RedactedOutput != "" && result.RedactedOutput != text {
			checked.comment["text"] = result.RedactedOutput
			checked.changed = true
		}
		return checked, nil
	}
	saved := stripUntrustedSSEComments(frame)
	data, rebuild, err := sseFrameData(saved)
	if err != nil {
		return nil, err
	}
	checked := &checkedStreamFrame{original: saved, rebuild: rebuild}
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return checked, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, errors.New("uninspectable stream JSON event")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple values in stream JSON event")
	}
	checked.root = root
	incremental := isIncrementalStreamText(root)
	for _, field := range collectVisibleText(root, true) {
		if field.checkOnly && !it.mandatory {
			continue
		}
		original, ok := field.text()
		if !ok || original == "" {
			continue
		}
		label := field.label
		if incremental && label == "__tool_json" && !field.checkOnly {
			label = "__tool_fragment"
		}
		result, err := it.checkText(ctx, meta.TenantID, label, original)
		if err != nil || result == nil {
			return nil, errOrMissingResult(err)
		}
		if result.Blocked {
			return nil, errors.New("output policy blocked stream event")
		}
		if incremental {
			checked.parts = append(checked.parts, &streamTextPart{
				frame: checked, field: field, original: original,
			})
		}
		if shouldRedact && result.RedactedOutput != "" && result.RedactedOutput != original {
			if field.immutable || (it.mandatory && (isToolArgumentLane(field.lane) || field.label != "")) {
				return nil, errors.New("cannot safely rewrite signed or sensitive tool stream event")
			}
			if field.label == "__tool_json" && json.Valid([]byte(original)) && !json.Valid([]byte(result.RedactedOutput)) {
				return nil, errors.New("redacted tool arguments are invalid JSON")
			}
			field.set(result.RedactedOutput)
			checked.changed = true
		}
	}
	return checked, nil
}

func isIncrementalStreamText(root map[string]any) bool {
	if _, ok := root["choices"].([]any); ok {
		return true
	}
	typ, _ := root["type"].(string)
	return typ == "content_block_delta" || typ == "content_block_start" ||
		typ == "response.content_part.added" || typ == "response.reasoning_summary_part.added" ||
		typ == "response.output_item.added" || strings.HasSuffix(typ, ".delta")
}

// sseFrameData follows SSE's data-line joining rule and keeps non-data lines
// and original line endings when a redaction changes the JSON value.
func sseFrameData(frame []byte) ([]byte, func([]byte) []byte, error) {
	data, hasData, rebuild := sseparser.ParseDataFrame(frame)
	if !hasData {
		return nil, nil, nil
	}
	return data, rebuild, nil
}
