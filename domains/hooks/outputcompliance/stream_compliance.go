package outputcompliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
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
// memory.
//
// ★★ 2026-10-29（211 生产实测 · 输出合规误报轮）：这条「越界即 fail-closed」
// 的策略在生产里是一台**误报机器**——
//
//	实测：6 个请求/24h 被拦（ZCode / Codex 的多分钟长回合），每个已经流出
//	3806~10529 个 chunk，客户端最终只拿到 801 字节（keepalive + 一个空错误帧）；
//	21 条审核记录（sanitize_block_reviews）的 rule_id / reason / field_path /
//	payload_digest **全部为空** —— 因为越界这条路径返回的 ShouldBlock
//	**不带任何元数据、也不打任何日志**。
//
// ⇒ 「越界 = 没有任何规则命中，只是缓冲装不下」被当成「策略拦截」上屏，
// 用户看到的是「被某条规则拦了」，管理员看到的是一条**空白**审核单。
// ⇒ 本轮把三件事拆开：
//
//	① **上限可配置**（LLM_GATEWAY_OUTPUT_GUARD_PENDING_MAX_BYTES）★
//	   **出厂值仍是 1 MiB**（★ 见 defaultMaxCompliancePendingBytes 的理由）★
//	② **越界不再悄悄杀请求**：默认 **release**（把已逐帧检查过的帧放行、
//	   后续帧改为逐帧放行，不再合并检查），并**大声记录**（WARN + 指标 +
//	   审核单，规则名 builtin.guard.capacity_exceeded）。
//	   需要 fail-closed 的部署可用 LLM_GATEWAY_OUTPUT_GUARD_CAP_MODE=block 恢复，
//	   但那条路径从此**也带元数据**（不再静默）。
//	③ **任何阻断都必须能说出规则**：所有阻断分支都带 policy_rule/policy_reason。
//
// ★★ 2026-10-29 审计轮更正（同日第二轮）：★ 误报的修复**不依赖**把上限抬大
//
//	—— **降级放行**（②）才是修复点 ⇒ ★★ 把出厂上限从 1 MiB 抬到 8 MiB
//	**既无必要、又把存储侧的记忆体预算放大了 8 倍**（★ 每并发流）★★
//	⇒ ★★ 出厂值**恢复 1 MiB**（★ 那是 2026-10-02 存储审计定下的 wire cap
//	契约：★ docs/audit/2026-10-02-conversation-storage-stream-audit.md）★★
//	⇒ ★★ 上限**仍可配**（env），且启动即打生效值（★ 运维要更大缓冲可以自己抬，
//	但那是** knowingly 的记忆体决定**，不是本轮顺手带上的）★★。
const (
	// OutputGuardPendingMaxBytesEnv 是待检缓冲上限的环境变量名。
	OutputGuardPendingMaxBytesEnv = "LLM_GATEWAY_OUTPUT_GUARD_PENDING_MAX_BYTES"
	// OutputGuardCapModeEnv 是越界行为的开关：release（默认）/ block。
	OutputGuardCapModeEnv = "LLM_GATEWAY_OUTPUT_GUARD_CAP_MODE"
	// GuardCapacityRuleID 是「缓冲越界」这条**基础设施性**阻断的合成规则名。
	// 它不是一条内容规则——审核员看到它就该知道「不是规则命中，是守卫装不下」。
	GuardCapacityRuleID = "builtin.guard.capacity_exceeded"
)

// defaultMaxCompliancePendingBytes 是待检缓冲上限的出厂值（1 MiB）。
//
// ★ 出厂值**就是 1 MiB**，而且**必须是 1 MiB**（★ 2026-10-29 审计轮更正）★★
//
// ★ 为什么不能顺手抬到 8 MiB（★ 这条曾经被抬过，同日被审计轮改回来）★★：
//
//	① **它是存储侧的 wire cap 契约**：★ docs/audit/2026-10-02-conversation-
//	   storage-stream-audit.md 定的就是「pending + 当前完整帧 ≤ 1 MiB，超出
//	   阻断」⇒ ★★ 抬到 8 MiB = 每并发流 8 MiB ⇒ ★★ 那是一个**存储审计轮**该做
//	   的记忆体决定（★ 要连带复核分区/并发/峰值）★★ 不是误报轮能顺手带的 ★★
//	② **抬它对误报修复没有必要**：★ 误报（业务被拦）的修复点是**降级放行**
//	   （release 模式 + 留痕）★ ⇒ ★★ 上限是多少都不再杀请求 ★★ ⇒ ⇒抬上限是
//	   **纯风险、无收益** ★★。
//
// ★ 真实的长回合会在1 MiB 处触发降级（可见、可审核），★ 代价是降级段不再做
//
//	lane 合并检查（★ 一个被拆成多帧的凭据可能漏检）★ ⇒ ★★ 需要更严的部署可以
//	自己抬上限（LLM_GATEWAY_OUTPUT_GUARD_PENDING_MAX_BYTES）★★ ⇒ ★★ **代价写
//	在明处**，而不是由出厂值悄悄替运维做决定 ★★。
const defaultMaxCompliancePendingBytes = 1 << 20

// compliancePendingCap 返回当前生效的待检缓冲上限（字节）。
//
// 读一次 env（进程生命周期内不变）⇒ 用包级变量缓存，避免每个 chunk 都做
// env 查找。非法值退回出厂值（**不**因为一个拼错的 env 就把守卫变成无界的）。
func compliancePendingCap() int {
	if v := os.Getenv(OutputGuardPendingMaxBytesEnv); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
		slog.Warn("output_compliance: invalid pending cap env; using default",
			"env", OutputGuardPendingMaxBytesEnv, "value", v, "default", defaultMaxCompliancePendingBytes)
	}
	return defaultMaxCompliancePendingBytes
}

// complianceCapMode 返回越界行为：release（默认，放行并记录）/ block
// （fail-closed，带元数据）。
func complianceCapMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(OutputGuardCapModeEnv))) {
	case "block":
		return "block"
	default:
		return "release"
	}
}

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
// policyIssueDigest 取拦截结果里第一条带摘要的 issue 的载荷摘要（无则空串）。
//
// 2026-10-28 拦截审核轮：摘要由 sanitize 侧在拦截那一刻计算
// （sha256(ruleID+path+value)，见 setToolIssueRule），审核批准后管理面用同
// 一个键放行这一次载荷。
func policyIssueDigest(issues []outputcompliance.ComplianceIssue) string {
	for _, issue := range issues {
		if issue.Digest != "" {
			return issue.Digest
		}
	}
	return ""
}
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
	// 2026-10-28 拦截审核轮：把载荷摘要与位置一并带给出帧方——审核队列据此
	// 对这一次载荷精确放行（重试即放行），不需要整条规则开/关。
	var pbe *errorsx.PolicyBlockError
	if errors.As(err, &pbe) && pbe != nil {
		if pbe.Path != "" {
			meta["policy_field"] = pbe.Path
		}
		if pbe.Digest != "" {
			meta["policy_digest"] = pbe.Digest
		}
		if pbe.Evidence != "" {
			meta[PolicyEvidenceMetadataKey] = pbe.Evidence
		}
	}
	return &response.ChunkResult{ShouldBlock: true, Metadata: meta}
}

// policyBlockDigestFromError 从策略拦截错误里取被拦载荷的摘要（可为空）。
func policyBlockDigestFromError(err error) string {
	var pbe *errorsx.PolicyBlockError
	if errors.As(err, &pbe) && pbe != nil {
		return pbe.Digest
	}
	return ""
}

// policyBlockPathFromError 从策略拦截错误里取被拦载荷的位置（可为空）。
func policyBlockPathFromError(err error) string {
	var pbe *errorsx.PolicyBlockError
	if errors.As(err, &pbe) && pbe != nil {
		return pbe.Path
	}
	return ""
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
	// degraded 表示待检缓冲曾经越界 ⇒ 本流已经降级为「逐帧检查、逐帧放行」。
	// 降级后不再合并 lane（跨帧拼接形态可能漏检），这是「不杀业务」的代价。
	degraded bool
	// capBytes 是本流的待检缓冲上限（建状态时读一次 env ⇒ 每帧不必查 env）。
	//
	// ★ 零值表示「未设置」⇒ 取当前 env 默认（有些调用方手工构造状态，
	//   见测试与降级重建路径）——**不能**把零值当成「上限 0」，
	//   否则每一帧都会被判越界（这是本轮第一版踩的坑）。
	capBytes int
	// toolNames 是参数 lane → 工具名的声明记录（r1020）。Responses/Anthropic
	// 流式的参数增量帧本身不带工具名，名字在 output_item.added /
	// content_block_start 这类声明帧里；终态合并检查时按 lane 取回，用于
	// sanitize 侧的惰性内容工具豁免。chat 形态首增量自带名字（直接进标签），
	// 不依赖这张表。
	toolNames map[string]string
}

// pendingCap 返回本流的待检缓冲上限（零值 ⇒ 取当前 env 默认）。
//
// ★ 零值必须回落，不能当成「上限 0」——手工构造状态的调用方（测试、降级
//
//	重建路径）会把每一帧都判成越界（本轮第一版就踩了这个坑）。
func (s *complianceStreamState) pendingCap() int {
	if s != nil && s.capBytes > 0 {
		return s.capBytes
	}
	return compliancePendingCap()
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
		state := &complianceStreamState{enabled: active, checkCtx: checkCtx, capBytes: compliancePendingCap()}
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
		//
		// ★ 2026-10-29：这条路径此前**静默阻断**（无元数据、无日志）⇒ 审核单
		// 是空白的、客户端只看到「被拦了但说不出哪条规则」。现在它带元数据、
		// 记日志、计指标 ⇒ 「阻断必须能说出规则」这条纪律对**所有**阻断分支
		// 成立，不再只有「有规则命中」的那几条。
		slog.Warn("output_compliance: blocked stream chunk (no request state)",
			"request_id", meta.RequestID,
			"session_id", meta.SessionID,
			"rule_id", GuardCapacityRuleID,
		)
		metrics.OutputGateFailClosedErrorsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateSourceTransformError).Inc()
		return &response.ChunkResult{
			ShouldBlock: true,
			Metadata: map[string]interface{}{
				"policy_rule":   GuardCapacityRuleID,
				"policy_reason": policyBlockErrorReason("output policy blocked stream chunk", GuardCapacityRuleID, ""),
			},
		}, nil
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
	// ★ 2026-10-29：已降级的流（缓冲曾越界）不再合并 lane —— 逐帧检查后
	// 直接放行。降级发生在 overCapacityResult 里，那一次已把扣下的帧放行。
	if state.degraded {
		checked, err := it.inspectSSEFrame(state.checkCtx, frame, meta, state.redact, &state.toolNames)
		if err != nil {
			return blockedChunkResult(err), nil
		}
		modified, err := checked.wireBytes()
		if err != nil {
			return &response.ChunkResult{
				ShouldBlock: true,
				Metadata: map[string]interface{}{
					"policy_rule":   GuardCapacityRuleID,
					"policy_reason": policyBlockErrorReason("output policy blocked stream chunk", GuardCapacityRuleID, ""),
				},
			}, nil
		}
		if !bytes.Equal(modified, frame) {
			return &response.ChunkResult{ModifiedChunk: modified}, nil
		}
		return nil, nil
	}
	// Terminal events can contain a complete model output snapshot. Bound
	// them together with held frames, before JSON parsing allocates copies or
	// the terminal path releases the pending output.
	//
	// ★ 2026-10-29：越界不再**静默杀请求**——见 overCapacityResult 的注释。
	if len(frame) > state.pendingCap()-state.bytes {
		return it.overCapacityResult(state, frame, meta)
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
			return &response.ChunkResult{
				ShouldBlock: true,
				Metadata: map[string]interface{}{
					"policy_rule":   GuardCapacityRuleID,
					"policy_reason": policyBlockErrorReason("output policy blocked stream terminal", GuardCapacityRuleID, ""),
				},
			}, nil
		}
		if len(prior) == 0 && bytes.Equal(terminal, frame) {
			return nil, nil
		}
		return &response.ChunkResult{SuppressChunk: true, ModifiedChunk: append(prior, terminal...)}, nil
	}
	if len(state.frames) == 0 && len(checked.parts) == 0 {
		modified, err := checked.wireBytes()
		if err != nil {
			return &response.ChunkResult{
				ShouldBlock: true,
				Metadata: map[string]interface{}{
					"policy_rule":   GuardCapacityRuleID,
					"policy_reason": policyBlockErrorReason("output policy blocked stream chunk", GuardCapacityRuleID, ""),
				},
			}, nil
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
	if state.bytes > state.pendingCap() {
		return it.overCapacityResult(state, nil, meta)
	}
	return &response.ChunkResult{SuppressChunk: true}, nil
}

// overCapacityResult 处理「待检缓冲越界」。
//
// ## 为什么不再直接杀请求
//
// 211 生产实测（2026-10-29，24h 内 6 例）：越界不是「内容违规」，而是
// **这条响应太长，缓冲装不下**。此前它返回 ShouldBlock（无元数据、无日志）
// ⇒ ① 客户端整条响应丢失（已流出的 3806~10529 个 chunk 全部作废）；
// ② 审核单是**空白**的（rule_id/reason 全空）⇒ 管理员既看不出是哪条规则，
// 也无法批准（放行键按规则，空规则放行不了）⇒ 同一个误报**无限复发**。
//
// ⇒ 现在：
//
//	默认（release）：把已逐帧检查过的帧**放行**，后续帧改为逐帧放行
//	（不再合并检查）⇒ 请求**成功**返回；同时 WARN + 指标 + 审核单
//	（规则名 builtin.guard.capacity_exceeded）⇒ 这条降级**可见、可审核**。
//	block 模式（LLM_GATEWAY_OUTPUT_GUARD_CAP_MODE=block）：保持 fail-closed，
//	但**带元数据**（不再静默）⇒ 审核单能说出「是缓冲越界，不是规则命中」。
//
// ## 降级的代价（必须写明）
//
// 越界之后**不再做 lane 合并检查** ⇒ 跨帧拼接才看得出的形态（一个被拆成
// 多帧的凭据）在降级段可能漏检。这是「不杀业务」的代价，不是「更安全」。
// 需要严格保证的部署：调大上限（LLM_GATEWAY_OUTPUT_GUARD_PENDING_MAX_BYTES）
// 或用 block 模式 + 上面的全局开关做取舍。
func (it *OutputComplianceInterceptor) overCapacityResult(state *complianceStreamState, frame []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	cap := state.pendingCap()
	held := state.bytes
	reason := policyBlockErrorReason(
		fmt.Sprintf("output policy guard pending capacity exceeded (%d bytes held, cap %d)", held, cap),
		GuardCapacityRuleID, "")
	slog.Warn("output_compliance: guard pending capacity exceeded",
		"request_id", meta.RequestID,
		"session_id", meta.SessionID,
		"tenant_id", meta.TenantID,
		"held_bytes", held,
		"cap_bytes", cap,
		"mode", complianceCapMode(),
		"rule_id", GuardCapacityRuleID,
	)
	metrics.OutputGateFailClosedErrorsTotal.WithLabelValues(metrics.OutputGateNameCompliance, metrics.OutputGateSourceCapacity).Inc()

	// 已扣下的帧的线路字节（release 时原样上屏；block 模式下作为摘录）。
	var released []byte
	for _, heldFrame := range state.frames {
		wire, err := heldFrame.wireBytes()
		if err != nil {
			state.clear()
			return &response.ChunkResult{
				ShouldBlock: true,
				Metadata: map[string]interface{}{
					"policy_rule":   GuardCapacityRuleID,
					"policy_reason": reason,
				},
			}, nil
		}
		released = append(released, wire...)
	}

	if complianceCapMode() == "block" {
		state.clear()
		return &response.ChunkResult{
			ShouldBlock: true,
			Metadata: map[string]interface{}{
				"policy_rule":   GuardCapacityRuleID,
				"policy_reason": reason,
				// 摘录：当时扣下不放的那批帧的头部（让审核员看到「模型在写
				// 什么才把缓冲撑爆」）。
				PolicyEvidenceMetadataKey: boundedPolicyEvidence(string(released)),
			},
		}, nil
	}

	// release：把已逐帧检查过的帧放行。当前帧（frame 非空）尚未检查——
	// 逐帧检查它之后再一起放行。
	state.clear()
	// 降级：后续帧不再合并检查（逐帧检查后直接放行）。
	state.degraded = true
	if len(frame) > 0 {
		checked, err := it.inspectSSEFrame(state.checkCtx, frame, meta, state.redact, &state.toolNames)
		if err != nil {
			return blockedChunkResult(err), nil
		}
		wire, err := checked.wireBytes()
		if err != nil {
			return &response.ChunkResult{
				ShouldBlock: true,
				Metadata: map[string]interface{}{
					"policy_rule":   GuardCapacityRuleID,
					"policy_reason": reason,
				},
			}, nil
		}
		released = append(released, wire...)
	}
	if len(released) == 0 {
		return nil, nil
	}
	// SuppressChunk + ModifiedChunk 是「holder 释放此前扣下的帧」的既有形状
	//（chain.go 的 holder 分支会把这批帧再过一遍后续拦截器）。
	//
	// ★ 元数据仍然带规则名/原因/摘录：**降级也要留痕**（管理面据此登记一条
	// 审核单，运维看得到「守卫在什么规模上让过步」）。policy_degraded 标记
	// 让写者知道这是「降级放行」而不是「拦截」——不渲染失败终帧。
	return &response.ChunkResult{
		SuppressChunk: true,
		ModifiedChunk: released,
		Metadata: map[string]interface{}{
			"policy_rule":             GuardCapacityRuleID,
			"policy_reason":           reason,
			"policy_degraded":         true,
			PolicyEvidenceMetadataKey: boundedPolicyEvidence(string(released)),
		},
	}, nil
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
				Path:   policyIssueLocation(result.Issues),
				Digest: policyIssueDigest(result.Issues),
				// ★ 2026-10-29 输出合规误报轮：被拦的那段 lane 内容（有界
				// 摘录）随错误一起上送 ⇒ 审核面能看到「当时拦的是什么」。
				Evidence: boundedPolicyEvidence(original),
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
				Path:   policyIssueLocation(result.Issues),
				Digest: policyIssueDigest(result.Issues),
				// ★ 2026-10-29：同上（事件级被拦内容的摘录）。
				Evidence: boundedPolicyEvidence(original),
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
