package outputcompliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	sseparser "github.com/kaixuan/llm-gateway-go/internal/sse"
	"github.com/kaixuan/llm-gateway-go/metrics"
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

// policyRuleFromIssues 取第一条带规则 ID 的问题。
//
// owner 口径（2026-10-20）：拦截必须说清「因为什么规则」。规则 ID 由检查器
// 给出（托管路径是快照里的规则 ID，非托管路径是 builtin.<type> 形态）。
func policyRuleFromIssues(issues []outputcompliance.ComplianceIssue) string {
	for _, issue := range issues {
		if issue.RuleID != "" {
			return issue.RuleID
		}
	}
	return ""
}

// policyIssueLocation 取第一条带位置的问题（tool 参数是 JSON 路径，纯文本是
// 字符区间）。
func policyIssueLocation(issues []outputcompliance.ComplianceIssue) string {
	for _, issue := range issues {
		if issue.Location != "" {
			return issue.Location
		}
	}
	return ""
}

// blockedChunkResult 把策略拦截错误转成**带规则名**的阻断结果。
//
// 写者据此渲染协议终帧，客户端因此能区分「策略拦截」与「上游故障」，
// 而不是只看到一个空响应（211 事故：zcode 只拿到 empty_model_response）。
//
// r1039（D4）：一并带上**人类可读**原因短语——客户端要拿它提示用户
// 「这次调用被拦了，要不要继续执行」。
func blockedChunkResult(err error) *response.ChunkResult {
	ruleID := errorsx.PolicyBlockRuleID(err)
	if ruleID == "" {
		return &response.ChunkResult{ShouldBlock: true}
	}
	meta := map[string]interface{}{"policy_rule": ruleID}
	if reason := policyBlockReasonFromError(err); reason != "" {
		meta["policy_reason"] = reason
	}
	return &response.ChunkResult{ShouldBlock: true, Metadata: meta}
}

// policyBlockReasonFromError 从策略拦截错误里取人类可读原因短语。
func policyBlockReasonFromError(err error) string {
	var pbe *errorsx.PolicyBlockError
	if errors.As(err, &pbe) && pbe != nil {
		return pbe.Reason
	}
	return ""
}

// policyBlockErrorReason 组装策略拦截错误的原因短语。
//
// 形如「output policy blocked stream lane（rule builtin.egress_sink）at $.command」
// ——规则名给机器看，位置给人看。
func policyBlockErrorReason(situation, ruleID, location string) string {
	reason := situation
	if ruleID != "" {
		reason += " (rule " + ruleID + ")"
	}
	if location != "" {
		reason += " at " + location
	}
	return reason
}

type complianceStreamState struct {
	mu       sync.Mutex
	checkCtx context.Context
	enabled  bool
	redact   bool
	frames   []*checkedStreamFrame
	lanes    map[string][]*streamTextPart
	bytes    int
	// toolNames 是参数 lane → 工具名的声明记录（r1020）。Responses/Anthropic
	// 流式的参数增量帧本身不带工具名，名字在 output_item.added /
	// content_block_start 这类声明帧里；终态合并检查时按 lane 取回，用于
	// sanitize 侧的惰性内容工具豁免。chat 形态首增量自带名字（直接进标签），
	// 不依赖这张表。
	toolNames map[string]string
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
		// 2026-10-10: 挂一次已还原值集合，跨全部 chunk 复用。集合解析是
		// 每响应一次（一次 Redis 读），绝不能按 chunk 解析。
		checkCtx := it.withRestoredValues(
			outputcompliance.WithRequestPolicyCache(ctx), meta.SessionID, meta.TenantID)
		state := &complianceStreamState{enabled: active, checkCtx: checkCtx}
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
	// Terminal events can contain a complete model output snapshot. Bound
	// them together with held frames, before JSON parsing allocates copies or
	// the terminal path releases the pending output.
	if len(frame) > maxCompliancePendingBytes-state.bytes {
		state.clear()
		return &response.ChunkResult{ShouldBlock: true}, nil
	}
	checked, err := it.inspectSSEFrame(state.checkCtx, frame, meta, state.redact, &state.toolNames)
	if err != nil {
		state.clear()
		return blockedChunkResult(err), nil
	}
	if isStreamTerminalFrame(frame) {
		prior, err := it.releaseCheckedFrames(state.checkCtx, state, meta)
		if err != nil {
			return blockedChunkResult(err), nil
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
		if len(parts) < 2 && (len(parts) == 0 || !isToolJSONLabel(parts[0].field.label)) {
			continue
		}
		var joined strings.Builder
		for _, part := range parts {
			joined.WriteString(part.original)
		}
		original := joined.String()
		// 工具名优先取字段自带标签（chat 首增量）； Responses/Anthropic 的
		// 增量帧无名，回查声明帧留下的 lane → name 记录。
		label := parts[0].field.label
		if name, ok := splitToolJSONLabel(label); ok && name == "" {
			label = toolJSONLabelFor(state.toolNames[lane])
		}
		result, err := it.checkText(ctx, meta.TenantID, label, original)
		if err != nil || result == nil {
			return nil, fmt.Errorf("check stream lane %q: %w", lane, errOrMissingResult(err))
		}
		if result.Blocked || state.redact && result.ToolShapeRedactionRequired {
			metrics.OutputGateDecisionsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateActionBlock, metrics.OutputGatePathStreamLane).Inc()
			// r1028 (2026-10-14, 154 incident): the lane block used to be
			// completely silent — no log named the request, so every hit
			// looked like an unexplained client disconnect. Log the decision
			// with the joined-lane shape; the payload preview stays off the
			// log on purpose (blocked text may carry the very bytes the
			// policy refuses to emit).
			reasons := make([]string, 0, len(result.Issues))
			for _, issue := range result.Issues {
				reasons = append(reasons, issue.Subtype)
			}
			ruleID := policyRuleFromIssues(result.Issues)
			slog.Warn("output_compliance: blocked stream lane",
				"request_id", meta.RequestID,
				"session_id", meta.SessionID,
				"tenant_id", meta.TenantID,
				"lane", lane,
				"label", label,
				"lane_bytes", len(original),
				"issues", len(result.Issues),
				"reason_codes", reasons,
				"rule_id", ruleID,
			)
			return nil, &errorsx.PolicyBlockError{
				RuleID: ruleID,
				Reason: policyBlockErrorReason("output policy blocked stream lane", ruleID, policyIssueLocation(result.Issues)),
			}
		}
		if !state.redact || result.RedactedOutput == "" || result.RedactedOutput == original {
			continue
		}
		for _, part := range parts {
			if part.field.immutable || (it.mandatory && (isToolArgumentLane(lane) || part.field.label != "")) {
				slog.Warn("output_compliance: refused stream lane rewrite",
					"request_id", meta.RequestID,
					"session_id", meta.SessionID,
					"tenant_id", meta.TenantID,
					"lane", lane,
					"mandatory", it.mandatory,
					"immutable_field", part.field.immutable,
					"lane_bytes", len(original),
				)
				return nil, errors.New("cannot safely rewrite signed or sensitive tool stream lane")
			}
		}
		// Tool-call argument deltas form one JSON document. Moving a rewrite
		// to the first delta is valid only when the reconstructed arguments
		// remain JSON; otherwise fail closed rather than break the client tool.
		if isToolJSONLabel(parts[0].field.label) && json.Valid([]byte(original)) && !json.Valid([]byte(result.RedactedOutput)) {
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

func (it *OutputComplianceInterceptor) inspectSSEFrame(ctx context.Context, frame []byte, meta *response.StreamMeta, shouldRedact bool, toolNames *map[string]string) (*checkedStreamFrame, error) {
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
			metrics.OutputGateDecisionsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateActionBlock, metrics.OutputGatePathStreamComment).Inc()
			slog.Warn("output_compliance: blocked stream comment",
				"request_id", meta.RequestID,
				"session_id", meta.SessionID,
				"tenant_id", meta.TenantID,
				"comment_bytes", len(text),
			)
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
	if toolNames != nil {
		recordDeclaredToolNames(root, toolNames)
	}
	for _, field := range collectVisibleText(root, true) {
		if field.checkOnly && !it.mandatory {
			continue
		}
		original, ok := field.text()
		if !ok || original == "" {
			continue
		}
		label := field.label
		if incremental && isToolJSONLabel(label) && !field.checkOnly {
			// 增量片段（含命名形态）一律按片段处理：完整的 JSON 检查在终态
			// 合并后进行，命名工具名随 parts[0].field.label / 声明记录带回。
			label = "__tool_fragment"
		}
		result, err := it.checkText(ctx, meta.TenantID, label, original)
		if err != nil || result == nil {
			return nil, errOrMissingResult(err)
		}
		if result.Blocked || shouldRedact && result.ToolShapeRedactionRequired {
			metrics.OutputGateDecisionsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateActionBlock, metrics.OutputGatePathStreamEvent).Inc()
			// r1037（2026-10-20，211）：这条分支原来**不带任何原因**——lane 那条
			// 分支有 reason_codes，event 这条没有，于是 211 现场只看到
			// "blocked stream event"，既说不出哪条规则，也没法据此开/关。
			reasons := make([]string, 0, len(result.Issues))
			for _, issue := range result.Issues {
				reasons = append(reasons, issue.Subtype)
			}
			ruleID := policyRuleFromIssues(result.Issues)
			slog.Warn("output_compliance: blocked stream event",
				"request_id", meta.RequestID,
				"session_id", meta.SessionID,
				"tenant_id", meta.TenantID,
				"label", label,
				"field_bytes", len(original),
				"issues", len(result.Issues),
				"reason_codes", reasons,
				"rule_id", ruleID,
			)
			return nil, &errorsx.PolicyBlockError{
				RuleID: ruleID,
				Reason: policyBlockErrorReason("output policy blocked stream event", ruleID, policyIssueLocation(result.Issues)),
			}
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
			if isToolJSONLabel(field.label) && json.Valid([]byte(original)) && !json.Valid([]byte(result.RedactedOutput)) {
				return nil, errors.New("redacted tool arguments are invalid JSON")
			}
			field.set(result.RedactedOutput)
			checked.changed = true
		}
	}
	return checked, nil
}

// recordDeclaredToolNames 登记声明帧里的工具名（r1020）。
//
// Responses/Anthropic 的参数增量帧不带工具名（名字在 output_item.added /
// content_block_start 里，且那些帧的 arguments 多为空串、进不了 parts），
// 终态合并检查拿不到名字就会把惰性内容工具当命令类处理。这里按**参数 lane
// 原文**记录，lane 构造与 collectVisibleText 逐字对齐——两边不同步时名字
// 落空，行为退回裸 __tool_json（fail-closed），不会误放行。
func recordDeclaredToolNames(root map[string]any, names *map[string]string) {
	if root == nil || names == nil {
		return
	}
	record := func(lane, name string) {
		if name == "" || lane == "" {
			return
		}
		if *names == nil {
			*names = map[string]string{}
		}
		(*names)[lane] = name
	}
	// chat：choices[].{message,delta}.tool_calls[].function.name / function_call.name
	if choices, ok := root["choices"].([]any); ok {
		for position, raw := range choices {
			choice, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			choiceLane := fmt.Sprintf("chat.choice.%s", streamLaneComponent(choice, "index", position))
			for _, key := range []string{"message", "delta"} {
				msg, ok := choice[key].(map[string]any)
				if !ok {
					continue
				}
				msgLane := choiceLane + "." + key
				if calls, ok := msg["tool_calls"].([]any); ok {
					for callPosition, rawCall := range calls {
						call, ok := rawCall.(map[string]any)
						if !ok {
							continue
						}
						function, ok := call["function"].(map[string]any)
						if !ok {
							continue
						}
						name, _ := function["name"].(string)
						record(fmt.Sprintf("%s.tool.%s.arguments", msgLane, streamLaneComponent(call, "index", callPosition)), name)
					}
				}
				if function, ok := msg["function_call"].(map[string]any); ok {
					name, _ := function["name"].(string)
					record(msgLane+".function_call.arguments", name)
				}
			}
		}
	}
	typ, _ := root["type"].(string)
	// anthropic：content_block_start 的 tool_use 名字。合并 lane 有 .input 与
	// .partial_json 两种形态（结构化 input 走前者，增量 JSON 走后者），都记。
	if typ == "content_block_start" {
		if block, ok := root["content_block"].(map[string]any); ok {
			if blockType, _ := block["type"].(string); blockType == "tool_use" {
				if name, _ := block["name"].(string); name != "" {
					blockLane := "anthropic.block." + streamLaneComponent(root, "index", 0)
					record(blockLane+".input", name)
					record(blockLane+".partial_json", name)
				}
			}
		}
	}
	// responses：output_item.added / output_item.done / function_call_arguments.done
	// 的 function_call 名字。增量帧的 lane 是 responses.<item_id>.<output_index>.
	// .arguments；声明帧的 item.id 与增量帧的 item_id 在标准协议里同值，两个都记
	//（不同也无害，查不到就退回裸标签 fail-closed）。
	if typ == "response.output_item.added" || typ == "response.output_item.done" {
		item, ok := root["item"].(map[string]any)
		if !ok {
			return
		}
		switch itemType, _ := item["type"].(string); itemType {
		case "function_call", "custom_tool_call":
			name, _ := item["name"].(string)
			if name == "" {
				return
			}
			itemID := streamLaneComponent(root, "item_id", 0)
			outputIndex := streamLaneComponent(root, "output_index", 0)
			declaredID := streamLaneComponent(item, "id", 0)
			if declaredID == "0" {
				declaredID = itemID
			}
			record(fmt.Sprintf("responses.%s.%s.arguments", declaredID, outputIndex), name)
			record(fmt.Sprintf("responses.%s.%s.arguments", itemID, outputIndex), name)
		}
	}
	// r1020：done 快照自带 item_id + name，是最可靠的名字声明点——即便
	// added 帧因故没经过守卫（被上游 holder 批量释放等），这里也能补记。
	if typ == "response.function_call_arguments.done" {
		if name, _ := root["name"].(string); name != "" {
			record(fmt.Sprintf("responses.%s.%s.arguments",
				streamLaneComponent(root, "item_id", 0), streamLaneComponent(root, "output_index", 0)), name)
		}
	}
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
