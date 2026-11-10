package contextagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Submitter 是「把一个请求体提交给上游 LLM 并拿回响应体」的能力。
//
// ★ 本包**不**依赖 domains/streaming（避免 import cycle）；
//
//	生产实现由 streaming 层注入（它才有 executor / 路由 / 凭据）。
type Submitter interface {
	// Submit 提交一个**已经装配好**的请求体，返回上游响应体（字节级）。
	Submit(ctx context.Context, body []byte) ([]byte, error)
}

// SubmitterFunc 是 Submitter 的函数式实现。
type SubmitterFunc func(ctx context.Context, body []byte) ([]byte, error)

// Submit 实现 Submitter。
func (f SubmitterFunc) Submit(ctx context.Context, body []byte) ([]byte, error) {
	return f(ctx, body)
}

// RequestSubmitterDeriver 是「按请求派生回灌实例」的能力。
//
// ★ 为什么需要它：回灌必须带**调用方自己的凭据**，否则回灌请求会以
//
//	网关内部身份记账，归属错乱（凭据归属、计费、审计都会挂到错的主体）。
//	而「调用方是谁」是**逐请求**才知道的 ⇒ 提交器必须能按请求派生。
type RequestSubmitterDeriver interface {
	// ForRequest 派生一个带调用方凭据与身份头的回灌实例。
	ForRequest(identity Identity, authHeader string) Submitter
}

// Request 是一次进入网关上下文智能体的请求。
type Request struct {
	RequestID string
	Identity  Identity
	// Body 是客户端**原始**请求体（字节级原样，智能体不改它）。
	Body []byte
	// Protocol 是客户端协议（openai-chat / anthropic-messages …）。
	Protocol string
	// ContextWindow 是目标候选的上下文窗口（<=0 表示未知）。
	ContextWindow int
	// CurrentTokens 是客户端请求体已占用的 token 估计（<=0 由智能体自算）。
	CurrentTokens int
	// SearchQuery 覆盖 L2 语义召回的查询串（空则用请求尾部构造）。
	SearchQuery string
	// DisableAssembly 跳过「主动分层装配」，只保留「记忆工具回环」。
	DisableAssembly bool
	// DisableToolLoop 跳过「记忆工具回环」，只保留「主动分层装配」。
	DisableToolLoop bool
	// AssembleOnly 表示「**只装配，不提交上游**」。
	//
	// ★ 契约依据：客户端驱动形态要的是「装配后的出站体」，
	//
	//	客户端自己拿去提交。旧实现没有这个出口 ⇒ Process 第 [5] 步
	//	**无条件** Submit ⇒ submit=false 也打了一次上游
	//	（非预期调用 + 计费 + 记录里混入一次真实交互），
	//	而 submit=true 时更是**打两次**（先一次无工具声明的裸提交，
	//	再一次正式提交 + 回灌）⇒ 双倍成本与上游副作用。
	//	⇒ 有这个出口后，/resolve 只跑**一次** Process，按 submit 二选一。
	AssembleOnly bool
	// ForceNonStream 强制出站体走**非流式**（把顶层 "stream" 改写为 false）。
	//
	// ★ 为什么需要：记忆工具回环要从上游响应里识别工具调用，
	//
	//	而 SSE（stream:true）响应不是单个 JSON ⇒ 识别恒失败
	//	⇒ 回环静默失效（审计轮实测：SSE 形态识别数恒为 0）。
	//	⇒ 「网关驱动形态」（服务端跑完整循环）必须出站非流式。
	ForceNonStream bool
}

// Outcome 是一次请求的处理结果。
type Outcome struct {
	// ResponseBody 是最终返回客户端的响应体。
	ResponseBody []byte
	// OriginalResponseBody 是第一次上游响应（原样）。
	OriginalResponseBody []byte
	// Record 是可还原记录（可能为 nil —— 未配置 Recorder 时）。
	Record *Record
	// Rounds 是实际发生的上游交互轮数（含首次提交）。
	Rounds int
	// ServedToolCalls 是网关自己执行过的记忆工具调用。
	ServedToolCalls []ServedCall
	// Layers 是本次实际加载到的层内容。
	Layers []LayerContent
	// Assembled 标记出站体是否被装配改写过。
	Assembled bool
	// AssembledBody 是**实际出站**的请求体（装配 + 工具声明注入之后）。
	//
	// ★ AssembleOnly 时它就是「交给客户端去提交的那份」；
	//	非 AssembleOnly 时它等于第 [5] 步提交出去的那份。
	AssembledBody []byte
	// Notes 记录**没有阻止请求但影响读数解释**的事项
	//（例如「无模型面凭据 ⇒ 跳过长期记忆召回」）。
	//
	// ★ 存在的理由：这些情形如果只体现在「结果为空」上，
	//	读数会被误读成「记忆里没有」，而真相是「压根没去查」。
	Notes []string
}

// Agent 是网关侧上下文智能体。
//
// ★ 默认零值即安全：所有依赖为 nil 时，Process 退化为
//
//	「原样提交一次、原样返回」——不会改变任何既有行为。
type Agent struct {
	// IndexLoader 取 L0 索引层（可 nil）。
	IndexLoader IndexLoader
	// StepLoader 取 L1 步骤明细层（可 nil）。
	StepLoader StepDetailLoader
	// LongTerm 取 L2 长期记忆层（可 nil）。
	LongTerm LongTermLoader
	// Submitter 提交上游（必填；nil 时 Process 直接返回错误）。
	Submitter Submitter
	// Recorder 落可还原记录（可 nil）。
	Recorder Recorder

	// MaxRounds 是记忆工具回环的最大轮数（默认 2）。
	MaxRounds int
	// MaxToolResultBytes 是单次工具结果的字节上限。
	MaxToolResultBytes int
	// InjectTools 是否向出站请求注入记忆工具声明（默认 true —— 无声明则模型
	// 永远不会发起记忆工具调用）。
	InjectTools bool
}

// DefaultMaxRounds 是记忆工具回环的默认最大轮数。
const DefaultMaxRounds = 2

// Process 执行一次完整的「识别 → 计划 → 加载 → 装配 → 提交 → 记忆工具回环 → 记录」。
func (a *Agent) Process(ctx context.Context, req Request) (Outcome, error) {
	var out Outcome
	if a == nil || a.Submitter == nil {
		return out, fmt.Errorf("contextagent: submitter is nil")
	}

	rec := &Record{
		RequestID:           req.RequestID,
		CreatedAt:           time.Now(),
		Identity:            req.Identity,
		OriginalRequestBody: append([]byte(nil), req.Body...),
	}

	// ── [1][2] 识别（身份由调用方给出）→ 计划 ──────────────────────────
	currentTokens := req.CurrentTokens
	if currentTokens <= 0 {
		currentTokens = EstimateTokens(string(req.Body))
	}
	searchQuery := req.SearchQuery
	if searchQuery == "" {
		searchQuery = deriveSearchQuery(req.Body)
	}

	plan := MakePlan(PlanInput{
		Identity:      req.Identity,
		ContextWindow: req.ContextWindow,
		CurrentTokens: currentTokens,
		SearchQuery:   searchQuery,
	})
	rec.Plan = plan

	// ★ 诚实披露：命名空间缺失 ⇒ 长期记忆召回被**跳过**（不是「召回为空」）。
	//
	//	这两种情况在读数上长得一样，但含义完全不同：前者是「没去查」，
	//	后者是「查了没有」。不披露 ⇒ 运维会把「没去查」读成「记忆是空的」。
	if a.LongTerm != nil && plan.SearchQuery != "" && req.Identity.Namespace() == "" {
		note := "长期记忆召回已跳过：本次请求没有可用的记忆命名空间（缺模型面凭据，或缺任务标识）。"
		out.Notes = append(out.Notes, note)
		rec.Notes = append(rec.Notes, note)
	}

	// ── [3] 加载各层 ───────────────────────────────────────────────────
	layers, err := a.loadLayers(ctx, req, plan)
	if err != nil {
		// 加载失败**不阻断请求**：记忆是增强，不是前置条件。
		slog.Warn("contextagent: load layers failed, continue without memory",
			"request_id", req.RequestID, "error", err)
		layers = nil
	}
	out.Layers = layers
	rec.Layers = layers

	// ── [4] 装配出站体 ─────────────────────────────────────────────────
	body := req.Body
	if !req.DisableAssembly && len(layers) > 0 {
		assembled, ok := Assemble(req.Body, layers)
		if ok {
			body = assembled
			out.Assembled = true
		}
	}

	// 注入记忆工具声明（模型必须知道有这条路可走）。
	if a.InjectTools && !req.DisableToolLoop {
		if injected, ok := InjectToolSpecs(body, req.Protocol); ok {
			body = injected
		}
	}

	// 强制非流式（网关驱动形态必须能识别工具调用）。
	if req.ForceNonStream {
		if forced, ok := forceNonStream(body); ok {
			body = forced
			out.Notes = append(out.Notes, "出站体已强制为非流式（stream=false）：记忆工具回环需要结构化响应才能识别工具调用。")
		}
	}

	// ── [5] 首次提交 ───────────────────────────────────────────────────
	//
	// ★ AssembleOnly：**到这里就结束**，不碰上游。
	//
	//	装配结果交还调用方（客户端驱动形态），由客户端自己去提交。
	//	旧实现在这里无条件 Submit ⇒ submit=false 也打了一次上游。
	if req.AssembleOnly {
		out.AssembledBody = body
		out.ResponseBody = nil
		rec.AssembledBody = append([]byte(nil), body...)
		rec.FinalResponseBody = nil
		if a.Recorder != nil {
			if err := a.Recorder.Save(ctx, rec); err != nil {
				slog.Warn("contextagent: save record failed", "request_id", req.RequestID, "error", err)
			}
		}
		out.Record = rec
		return out, nil
	}

	respBody, err := a.Submitter.Submit(ctx, body)
	if err != nil {
		return out, fmt.Errorf("contextagent: submit: %w", err)
	}
	out.AssembledBody = append([]byte(nil), body...)
	out.Rounds = 1
	out.OriginalResponseBody = append([]byte(nil), respBody...)
	rec.OriginalResponseBody = append([]byte(nil), respBody...)
	rec.Rounds = append(rec.Rounds, Round{Index: 1, RequestBody: body, ResponseBody: respBody, Note: "首次提交"})

	// ── [6][7][8] 记忆工具识别 → 网关执行 → 同步重投 ───────────────────
	if !req.DisableToolLoop {
		maxRounds := a.MaxRounds
		if maxRounds <= 0 {
			maxRounds = DefaultMaxRounds
		}
		for round := 1; round < maxRounds; round++ {
			calls := DetectMemoryToolCalls(respBody, req.Protocol)
			if len(calls) == 0 {
				break
			}
			results := a.serveCalls(ctx, req, calls)
			out.ServedToolCalls = append(out.ServedToolCalls, results.served...)
			rec.ServedToolCalls = append(rec.ServedToolCalls, results.served...)

			nextBody, ok := AppendToolResults(body, respBody, results.results, req.Protocol)
			if !ok {
				slog.Warn("contextagent: cannot append tool results, stop loop",
					"request_id", req.RequestID, "protocol", req.Protocol)
				break
			}
			body = nextBody
			respBody, err = a.Submitter.Submit(ctx, body)
			if err != nil {
				return out, fmt.Errorf("contextagent: resubmit after memory tool: %w", err)
			}
			out.AssembledBody = append([]byte(nil), body...)
			out.Rounds++
			rec.Rounds = append(rec.Rounds, Round{
				Index:        out.Rounds,
				RequestBody:  body,
				ResponseBody: respBody,
				Note:         fmt.Sprintf("记忆工具结果已并入（%d 个调用）", len(calls)),
			})
		}
	}

	out.ResponseBody = respBody
	rec.FinalResponseBody = append([]byte(nil), respBody...)

	// ── [9] 记录 ───────────────────────────────────────────────────────
	if a.Recorder != nil {
		if err := a.Recorder.Save(ctx, rec); err != nil {
			// 记录失败**不阻断**响应（响应已经拿到了）。
			slog.Warn("contextagent: save record failed", "request_id", req.RequestID, "error", err)
		}
	}
	out.Record = rec
	return out, nil
}

// loadLayers 按计划加载各层。
func (a *Agent) loadLayers(ctx context.Context, req Request, plan Plan) ([]LayerContent, error) {
	var out []LayerContent

	// L0 索引层。
	if a.IndexLoader != nil && plan.BudgetFor(LayerIndex) > 0 {
		entries, err := a.IndexLoader.LoadIndex(ctx, req.Identity)
		if err != nil {
			return out, err
		}
		text := RenderIndex(entries)
		if text != "" {
			out = append(out, LayerContent{
				Layer:  LayerIndex,
				Text:   text,
				Tokens: EstimateTokens(text),
				Sources: []string{
					fmt.Sprintf("session_request_summaries:%s", req.Identity.Session),
				},
			})
		}
	}

	// L2 长期记忆层（语义召回）。
	if a.LongTerm != nil && plan.BudgetFor(LayerLongTerm) > 0 && plan.SearchQuery != "" {
		ns := req.Identity.Namespace()
		if ns != "" {
			topK := plan.BudgetFor(LayerLongTerm) / 200
			if topK < 1 {
				topK = 1
			}
			if topK > 8 {
				topK = 8
			}
			items, err := a.LongTerm.Recall(ctx, ns, plan.SearchQuery, topK)
			if err != nil {
				slog.Warn("contextagent: long-term recall failed", "request_id", req.RequestID, "error", err)
			} else if len(items) > 0 {
				texts := make([]string, 0, len(items))
				srcs := make([]string, 0, len(items))
				for _, m := range items {
					texts = append(texts, m.Text)
					srcs = append(srcs, "memora:"+m.ID)
				}
				text := RenderLongTerm(texts)
				out = append(out, LayerContent{
					Layer:   LayerLongTerm,
					Text:    text,
					Tokens:  EstimateTokens(text),
					Sources: srcs,
				})
			}
		}
	}

	// L1 步骤明细层（按需加载，精确寻址）。
	//
	// ★ 契约（2026-11-11 审计轮登记）：L1 **不进装配**，走记忆工具按需取。
	//
	//	这不是「实现没来得及」，而是**自动装载会放大一个已知缺陷**：
	//	生产上 request_delta 常常是「客户端完整历史」而不是本轮增量
	//	（仓内 FIXME-14372/14373）⇒ 一步的正文可能是整份历史的重复；
	//	自动装 N 步 ⇒ 同一份历史被注入 N 次 ⇒ 上下文预算直接爆，
	//	而且是在**已经付了 IO 代价之后**才发现装不下。
	//	⇒ 计划里的 Plan.LoadSteps / PlanInput.AvailableSteps 保留为
	//	「按需装载」的预算算式（MakePlan 是纯函数、可完整单测），
	//	但生产路径**不填**它 ⇒ L1 在装配里恒为空。
	//	⇒ 这**不是**静默失效：L0 索引给出**权威**步号（见 PGIndexLoader 的
	//	join 口径），模型据此调 load_step_detail 精确取回那一步。
	if a.StepLoader != nil && len(plan.LoadSteps) > 0 {
		details, err := a.StepLoader.LoadSteps(ctx, req.Identity, plan.LoadSteps)
		if err != nil {
			slog.Warn("contextagent: step detail load failed", "request_id", req.RequestID, "error", err)
		} else if len(details) > 0 {
			text := RenderStepDetails(details)
			srcs := make([]string, 0, len(details))
			for _, d := range details {
				srcs = append(srcs, fmt.Sprintf("session_bodies:%s#%d", req.Identity.Session, d.StepIndex))
			}
			out = append(out, LayerContent{
				Layer:   LayerStepDetail,
				Text:    text,
				Tokens:  EstimateTokens(text),
				Sources: srcs,
			})
		}
	}

	return out, nil
}

// servedResults 是一次回环的工具结果集合。
type servedResults struct {
	results []ToolResult
	served  []ServedCall
}

// serveCalls 执行一批记忆工具调用。
func (a *Agent) serveCalls(ctx context.Context, req Request, calls []ToolCall) servedResults {
	var out servedResults
	deps := ToolDeps{
		Identity:   req.Identity,
		StepLoader: a.StepLoader,
		LongTerm:   a.LongTerm,
		MaxBytes:   a.MaxToolResultBytes,
	}
	for _, c := range calls {
		res := ServeMemoryTool(ctx, deps, c)
		out.results = append(out.results, res)
		out.served = append(out.served, ServedCall{
			ToolCallID: res.ToolCallID,
			Name:       res.Name,
			Arguments:  c.Arguments,
			Loaded:     res.Loaded,
			Error:      res.Error,
		})
	}
	return out
}

// InjectToolSpecs 把记忆工具声明注入出站请求的 tools 数组。
//
// ★ openai-chat：tools[] 追加两条 function 定义。
// ★ anthropic-messages：tools[] 追加两条 input_schema 定义。
//
// ★ 幂等：已经注入过就不重复注入（避免回环里越滚越大）。
func InjectToolSpecs(body []byte, protocol string) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	var probe struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return body, false
	}
	for _, t := range probe.Tools {
		if toolNameOf(t) == ToolLoadStepDetail || toolNameOf(t) == ToolSearchMemory {
			return body, false // 已注入
		}
	}
	specs := ToolSpecs()
	additions := make([]json.RawMessage, 0, len(specs))
	for _, s := range specs {
		var raw json.RawMessage
		var err error
		if protocol == "anthropic-messages" {
			raw, err = json.Marshal(map[string]any{
				"name":         s.Name,
				"description":  s.Description,
				"input_schema": s.Parameters,
			})
		} else {
			raw, err = json.Marshal(map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        s.Name,
					"description": s.Description,
					"parameters":  s.Parameters,
				},
			})
		}
		if err != nil {
			return body, false
		}
		additions = append(additions, raw)
	}
	merged := append(append([]json.RawMessage(nil), probe.Tools...), additions...)
	newTools, err := json.Marshal(merged)
	if err != nil {
		return body, false
	}
	return spliceOrInsertTopLevelArray(body, []byte(`"tools"`), newTools)
}

// toolNameOf 从一条 tool 定义里取工具名（兼容 openai / anthropic 两种形状）。
func toolNameOf(raw json.RawMessage) string {
	var probe struct {
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	if probe.Function.Name != "" {
		return probe.Function.Name
	}
	return probe.Name
}

// AppendToolResults 把工具结果并回请求体，供下一次提交使用。
//
// ★ openai-chat：
//
//	[..., assistant{tool_calls:[...]}, {role:"tool",tool_call_id,content}, ...]
//
// ★ anthropic-messages：
//
//	[..., assistant{content:[tool_use...]}, {role:"user",content:[{type:"tool_result",tool_use_id,content}]}, ...]
func AppendToolResults(requestBody, responseBody []byte, results []ToolResult, protocol string) ([]byte, bool) {
	if len(requestBody) == 0 || len(results) == 0 {
		return requestBody, false
	}
	if protocol == "anthropic-messages" {
		return appendAnthropicToolResults(requestBody, responseBody, results)
	}
	return appendOpenAIToolResults(requestBody, responseBody, results)
}

// appendOpenAIToolResults 实现 openai-chat 的工具结果回填。
func appendOpenAIToolResults(requestBody, responseBody []byte, results []ToolResult) ([]byte, bool) {
	var reqProbe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(requestBody, &reqProbe); err != nil || len(reqProbe.Messages) == 0 {
		return requestBody, false
	}
	// 从响应里取出 assistant 消息（含 tool_calls）。
	var respProbe struct {
		Choices []struct {
			Message json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &respProbe); err != nil || len(respProbe.Choices) == 0 {
		return requestBody, false
	}
	assistant := respProbe.Choices[0].Message
	if len(assistant) == 0 {
		return requestBody, false
	}

	merged := append([]json.RawMessage(nil), reqProbe.Messages...)
	merged = append(merged, assistant)
	for _, r := range results {
		raw, err := json.Marshal(map[string]any{
			"role":         "tool",
			"tool_call_id": r.ToolCallID,
			"content":      r.Content,
		})
		if err != nil {
			return requestBody, false
		}
		merged = append(merged, raw)
	}
	newMsgs, err := json.Marshal(merged)
	if err != nil {
		return requestBody, false
	}
	return spliceMessages(requestBody, newMsgs)
}

// appendAnthropicToolResults 实现 anthropic-messages 的工具结果回填。
func appendAnthropicToolResults(requestBody, responseBody []byte, results []ToolResult) ([]byte, bool) {
	var reqProbe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(requestBody, &reqProbe); err != nil || len(reqProbe.Messages) == 0 {
		return requestBody, false
	}
	var respProbe struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(responseBody, &respProbe); err != nil {
		return requestBody, false
	}
	if len(respProbe.Content) == 0 {
		return requestBody, false
	}
	merged := append([]json.RawMessage(nil), reqProbe.Messages...)
	assistant, err := json.Marshal(map[string]any{
		"role":    "assistant",
		"content": respProbe.Content,
	})
	if err != nil {
		return requestBody, false
	}
	merged = append(merged, assistant)

	blocks := make([]any, 0, len(results))
	for _, r := range results {
		blocks = append(blocks, map[string]any{
			"type":        "tool_result",
			"tool_use_id": r.ToolCallID,
			"content":     r.Content,
		})
	}
	userMsg, err := json.Marshal(map[string]any{
		"role":    "user",
		"content": blocks,
	})
	if err != nil {
		return requestBody, false
	}
	merged = append(merged, userMsg)
	newMsgs, err := json.Marshal(merged)
	if err != nil {
		return requestBody, false
	}
	return spliceMessages(requestBody, newMsgs)
}

// deriveSearchQuery 从请求体尾部构造 L2 召回查询串。
//
// ★ 只取最后 ~1500 字符（与仓内压缩链的 buildMemoraQuery 同口径），
//
//	太长只会拖慢检索。
func deriveSearchQuery(body []byte) string {
	text := extractTailText(body, 1500)
	return strings.TrimSpace(text)
}

// extractTailText 抽出请求体末尾的纯文本片段。
func extractTailText(body []byte, max int) string {
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	var b strings.Builder
	for i := len(probe.Messages) - 1; i >= 0; i-- {
		m := probe.Messages[i]
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		b.WriteString(m.Content)
		b.WriteString("\n")
		if b.Len() >= max {
			break
		}
	}
	out := b.String()
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// spliceOrInsertTopLevelArray 把顶层对象里的某个数组整体替换掉；
// 若该键**不存在**，则在对象开头插入 `"key":[...]`。
//
// ★ 插入分支是必需路径：客户端常常**不带** tools 字段，
//
//	而记忆工具声明必须注入 ⇒ 没有 tools 就要新建。
func spliceOrInsertTopLevelArray(original, key, newArray []byte) ([]byte, bool) {
	if idx := indexOfKey(original, key); idx >= 0 {
		return spliceTopLevelArray(original, key, newArray)
	}
	// 找顶层对象的 '{'。
	//
	// ★ 必须确认**首个非空白字节就是 '{'**（审计轮修复）：
	//
	//	原实现取「第一个出现的 '{'」⇒ 对数组体 `[{"a":1}]` 会插到
	//	**嵌套对象**里去 ⇒ 顶层并没有这个键，注入等于无效。
	brace := -1
	for i := 0; i < len(original); i++ {
		if isSpaceByte(original[i]) {
			continue
		}
		if original[i] == '{' {
			brace = i
		}
		break
	}
	if brace < 0 {
		return original, false
	}
	entry := make([]byte, 0, len(key)+len(newArray)+4)
	entry = append(entry, key...)
	entry = append(entry, ':')
	entry = append(entry, newArray...)
	out := make([]byte, 0, len(original)+len(entry)+1)
	out = append(out, original[:brace+1]...)
	out = append(out, entry...)
	// ★ 空对象 `{}` 不加逗号：加了就是 `{"tools":[...],}` ⇒ 非法 JSON
	//	（审计轮发现的潜伏缺陷：注入工具声明到空体上会产出坏体）。
	rest := original[brace+1:]
	j := 0
	for j < len(rest) && isSpaceByte(rest[j]) {
		j++
	}
	if !(j < len(rest) && rest[j] == '}') {
		out = append(out, ',')
	}
	out = append(out, rest...)
	return out, true
}

// spliceTopLevelArray 把顶层对象里的某个数组整体替换掉。
func spliceTopLevelArray(original, key, newArray []byte) ([]byte, bool) {
	idx := indexOfKey(original, key)
	if idx < 0 {
		return original, false
	}
	i := idx + len(key)
	for i < len(original) && isSpaceByte(original[i]) {
		i++
	}
	if i >= len(original) || original[i] != ':' {
		return original, false
	}
	i++
	for i < len(original) && isSpaceByte(original[i]) {
		i++
	}
	if i >= len(original) || original[i] != '[' {
		return original, false
	}
	end := matchArrayEnd(original, i)
	if end < 0 {
		return original, false
	}
	out := make([]byte, 0, len(original)+len(newArray))
	out = append(out, original[:i]...)
	out = append(out, newArray...)
	out = append(out, original[end+1:]...)
	return out, true
}

// matchArrayEnd 返回与 original[start]=='[' 配对的 ']' 下标；找不到返回 -1。
func matchArrayEnd(original []byte, start int) int {
	depth := 1
	inStr := false
	escape := false
	for end := start + 1; end < len(original); end++ {
		c := original[end]
		if inStr {
			switch {
			case escape:
				escape = false
			case c == '\\':
				escape = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return end
			}
		}
	}
	return -1
}
