// Package main — synth.go：合成多步 agent 会话 + 分层上下文装配。
//
// 本文件是「总结摘要 + 分层加载」可行性 bench 的数据面。它只依赖仓内已
// 导出的 API（domains/hooks/compression），不引入任何新机制。
//
// ★ 口径声明（读结论前必读）：
//
//	① token 估算用仓内自己的启发式：1 token ≈ 3.5 字节
//	   （domains/hooks/compression/estimator.go 与
//	   domains/streaming/executors/context_summarize.go 的
//	   heuristicCompactBytePerToken 同值）。
//	② 「L0 逐步摘要」的生成契约镜像
//	   domains/analysis/request_summary.go 的 ruleExtract：
//	   request_summary = 请求预览截断 200 字符；
//	   response_summary = 回复预览截断 300 字符。
//	   本包不调用该函数的未导出方法，故按同一契约复刻；两侧口径由
//	   domains/analysis 的既有单测负责各自的钉桩。
//	③ 本包的 L1 明细存储采用【理想形态】：每轮只存「客户端本轮新增的
//	   消息」+「本轮模型回复」。这对应生产上 outbound_body 为数组的
//	   那一种形态（仓内审计：形态 B 真的只存本轮新增）。
//	   ★ 生产另一种形态（outbound_body 为 jsonb 'null'）下 request_delta
//	   存的是【客户端完整历史】⇒ 见 domains/session/v2/session_writer_v2.go
//	   的 FIXME-14372 / FIXME-14373（含 45× 放大与 57.7% 二次方会话的实测）。
//	   本 bench 的 §存储放大 一节会把这个差额量化出来。
package main

import (
	"fmt"
	"sort"
	"strings"
)

// bytesPerToken 与网关自身启发式一致（3.5 字节/token）。
const bytesPerToken = 3.5

// estimateTokens 按仓内启发式估算 token 数。
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return int(float64(len(s)) / bytesPerToken)
}

// wireMsg 是一条 OpenAI chat 形状的消息。
//
// 用 map[string]any 而不是结构体，是为了能直接喂给
// domains/hooks/compression.AnalyzeTasks（它接受 []map[string]any）。
type wireMsg = map[string]any

func sysMsg(text string) wireMsg { return wireMsg{"role": "system", "content": text} }
func userMsg(text string) wireMsg {
	return wireMsg{"role": "user", "content": text}
}
func asstMsg(text string) wireMsg {
	return wireMsg{"role": "assistant", "content": text}
}
func toolMsg(id, text string) wireMsg {
	return wireMsg{"role": "tool", "tool_call_id": id, "content": text}
}
func asstToolCall(id string, sp stepSpec, it int) wireMsg {
	return wireMsg{
		"role":    "assistant",
		"content": fmt.Sprintf("正在按步骤 %d 执行：%s（迭代 %d）", sp.index, sp.goal, it),
		"tool_calls": []any{
			map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      "run_step",
					"arguments": fmt.Sprintf(`{"step":%d,"iter":%d,"target":%q}`, sp.index, it, sp.keys[0]),
				},
			},
		},
	}
}

// msgText 抽出消息的可读文本（用于 token 计量与摘要生成）。
func msgText(m wireMsg) string {
	s, _ := m["content"].(string)
	return s
}

// msgRole 抽出消息角色。
func msgRole(m wireMsg) string {
	s, _ := m["role"].(string)
	return s
}

// stepSpec 是合成会话里一个「步」的真值。
//
// 一「步」= 一个用户目标（task），它跨若干次 agent 迭代；
// 每次迭代 = 一次 HTTP 请求 = 网关的一「轮」（turn）。
type stepSpec struct {
	index  int
	goal   string   // 目标（用户诉求）
	result string   // 结果（该步的结论）
	keys   []string // 关键标识符（文件 / 错误码 / 端点）——L0 索引必须至少留住一个
	iters  int      // 该步包含的 agent 迭代次数
}

// turnRec 是一「轮」的记录，形状对齐生产 session_bodies 的一行。
type turnRec struct {
	no      int
	stepIdx int
	body    []wireMsg // 客户端这一轮发出的完整历史
	delta   []wireMsg // 本轮【客户端新增】的消息（理想形态的 request_delta）
	resp    []wireMsg // 本轮模型回复（response_delta）
}

// systemPrompt 是合成会话的系统提示（占位，保持短）。
const systemPrompt = "你是一个编码助手。"

// taskTemplates 是步模板池（英文）。
//
// ★ 每条 goal 都以 "next task:" 起头 —— 这正是
// domains/hooks/compression/task_analyzer.go 的 completionPatterns
// 里的英文触发词之一（"next task"），用来测量英文会话下的边界识别率。
var enTemplates = []stepSpec{
	{goal: "next task: add retry with exponential backoff to the auth client",
		result: "retry added, max 5 attempts, see internal/auth/client.go",
		keys:   []string{"internal/auth/client.go"}, iters: 3},
	{goal: "next task: partition the session bodies table by month",
		result: "migration 913 applied, session_bodies partitioned by partition_date",
		keys:   []string{"session_bodies", "913"}, iters: 2},
	{goal: "next task: chase the 502 mixed responses behind the old nginx worker",
		result: "old worker still bound 8781, graceful QUIT applied, 200 restored",
		keys:   []string{"8781", "502"}, iters: 3},
	{goal: "next task: reconcile the request statistics against the upstream",
		result: "late arriving rows reswept, gap narrowed to 1.8%",
		keys:   []string{"stats_minute_rollup"}, iters: 2},
	{goal: "next task: unblock the credential probe that kept hitting rate limits",
		result: "probe rate window aligned to 60s, 429 storm gone",
		keys:   []string{"credential_probe_v2", "429"}, iters: 2},
	{goal: "next task: publish the pricing history page",
		result: "pricing page renders the effective dated price versions",
		keys:   []string{"model_price_versions", "effective_from"}, iters: 2},
	{goal: "next task: drain the request journal backlog",
		result: "outbox reaper reclaimed 1.2M rows in 6 minutes",
		keys:   []string{"session_aggregate_outbox"}, iters: 2},
	{goal: "next task: add the mcp gateway control plane endpoints",
		result: "control plane exposed on /mcp/control with 8 governance gates",
		keys:   []string{"/mcp/control", "-32003"}, iters: 3},
}

// taskTemplatesCN 是步模板池（中文）。
//
// ★ 中文模板刻意【不含】任何英文触发词（"next task" / "done" / "next:"）。
//
//	这不是为了造一个偏袒性的样本，而是为了测量一个真实缺口：
//	domains/hooks/compression/task_analyzer.go 的 completionPatterns 与
//	newTaskPatterns **全是英文字面量**，中文会话因此得不到任何边界。
var cnTemplates = []stepSpec{
	{goal: "接下来做：给鉴权客户端加指数退避重试",
		result: "重试最多 5 次，退避指数增长，改动在 internal/auth/client.go",
		keys:   []string{"internal/auth/client.go"}, iters: 3},
	{goal: "接下来做：把会话正文表按月分区",
		result: "迁移 913 已应用，session_bodies 按 partition_date 分区",
		keys:   []string{"session_bodies", "913"}, iters: 2},
	{goal: "接下来做：排查 502 混合响应",
		result: "旧 worker 仍占用 8781，优雅退出后恢复 200",
		keys:   []string{"8781", "502"}, iters: 3},
	{goal: "接下来做：把请求统计和上游对账",
		result: "迟到行重扫后缺口收敛到 1.8%",
		keys:   []string{"stats_minute_rollup"}, iters: 2},
	{goal: "接下来做：解决凭据探针被限流",
		result: "探针限流窗口对齐 60s，429 风暴消失",
		keys:   []string{"429"}, iters: 2},
	{goal: "接下来做：发布定价历史页面",
		result: "定价页已渲染生效期价版本",
		keys:   []string{"model_price_versions"}, iters: 2},
	{goal: "接下来做：清理请求日志积压",
		result: "outbox reaper 六分钟回收 120 万行",
		keys:   []string{"session_aggregate_outbox"}, iters: 2},
	{goal: "接下来做：补 MCP 网关控制面端点",
		result: "控制面挂在 /mcp/control，含 8 道治理闸",
		keys:   []string{"/mcp/control"}, iters: 3},
}

// buildSpecs 造出 n 个步（模板循环使用，必要时加序号消歧）。
func buildSpecs(templates []stepSpec, n int) []stepSpec {
	out := make([]stepSpec, 0, n)
	for i := 0; i < n; i++ {
		base := templates[i%len(templates)]
		sp := base
		sp.index = i + 1
		sp.goal = fmt.Sprintf("%s (#%d)", base.goal, i+1)
		sp.result = fmt.Sprintf("%s (#%d)", base.result, i+1)
		out = append(out, sp)
	}
	return out
}

// buildSession 造出整个合成会话的轮序列。
//
// 轮的语义与生产一致：
//
//	body  = 客户端这一轮发出的完整历史
//	delta = 客户端相对上一轮【新增】的消息（理想形态 request_delta）
//	resp  = 本轮模型回复（response_delta）
//
// ⇒ 拼接所有 (delta ++ resp) 恰好还原完整历史，与
// domains/session/v2/turn_reader.go 的 LoadChain 口径一致。
func buildSession(specs []stepSpec, fillerBytes int) []turnRec {
	var pending []wireMsg // 客户端已知的全部消息（含模型上一轮回复的回声）
	var turns []turnRec

	emit := func(stepIdx int, clientNew []wireMsg, resp wireMsg) {
		body := make([]wireMsg, 0, len(pending)+len(clientNew))
		body = append(body, pending...)
		body = append(body, clientNew...)
		turns = append(turns, turnRec{
			no:      len(turns) + 1,
			stepIdx: stepIdx,
			body:    body,
			delta:   clientNew,
			resp:    []wireMsg{resp},
		})
		pending = make([]wireMsg, 0, len(body)+1)
		pending = append(pending, body...)
		pending = append(pending, resp)
	}

	for si, sp := range specs {
		// 本步第 1 轮：客户端发出目标（第 1 轮顺带带上 system）。
		first := []wireMsg{userMsg(sp.goal)}
		if len(turns) == 0 {
			first = append([]wireMsg{sysMsg(systemPrompt)}, first...)
		}
		iterResp := asstToolCall(fmt.Sprintf("call_%d_1", sp.index), sp, 1)
		if sp.iters == 1 {
			iterResp = asstMsg(sp.result)
		}
		emit(si, first, iterResp)

		// 中间迭代：客户端回传工具结果，模型继续。
		for it := 2; it <= sp.iters; it++ {
			callID := fmt.Sprintf("call_%d_%d", sp.index, it)
			toolOut := toolMsg(callID, fillerFor(sp, it, fillerBytes))
			resp := asstToolCall(callID, sp, it)
			if it == sp.iters {
				resp = asstMsg(sp.result)
			}
			emit(si, []wireMsg{toolOut}, resp)
		}
	}
	return turns
}

// fillerFor 造一段工具输出（模拟文件内容 / 命令输出）。
func fillerFor(sp stepSpec, it int, fillerBytes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool output for step %d iter %d (target %s)\n", sp.index, it, sp.keys[0])
	for b.Len() < fillerBytes {
		fmt.Fprintf(&b, "line %d: reading %s ... ok\n", b.Len(), sp.keys[0])
	}
	return b.String()
}

// ---- L0 索引（逐步摘要）----------------------------------------------------

// stepSummary 是一步的 L0 摘要（目标 + 结果）。
type stepSummary struct {
	StepIndex int
	Goal      string
	Result    string
}

// truncate 按仓内 ruleExtract 的字符预算截断（请求 200 / 回复 300）。
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// buildStepSummaries 从轮序列生成每步的 L0 摘要。
//
// 口径 = 每轮一条 (request_summary, response_summary)（镜像
// domains/analysis/request_summary.go 的 ruleExtract 契约），
// 再按步聚合：目标是该步第一轮的请求摘要，结果是该步最后一轮的回复摘要。
func buildStepSummaries(turns []turnRec) []stepSummary {
	first := map[int]int{}
	last := map[int]int{}
	for _, t := range turns {
		if _, ok := first[t.stepIdx]; !ok {
			first[t.stepIdx] = t.no
		}
		last[t.stepIdx] = t.no
	}
	byNo := map[int]turnRec{}
	for _, t := range turns {
		byNo[t.no] = t
	}
	// ★ 按步号升序输出（map 迭代顺序随机 ⇒ 索引行顺序不可复现）。
	order := make([]int, 0, len(first))
	for si := range first {
		order = append(order, si)
	}
	sort.Ints(order)
	out := make([]stepSummary, 0, len(order))
	for _, si := range order {
		f := byNo[first[si]]
		l := byNo[last[si]]
		out = append(out, stepSummary{
			StepIndex: si + 1,
			Goal:      truncate(joinText(f.delta), 200),
			Result:    truncate(joinText(l.resp), 300),
		})
	}
	return out
}

// joinText 把一组消息拼成一段文本。
func joinText(msgs []wireMsg) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(msgText(m))
		b.WriteString("\n")
	}
	return b.String()
}

// renderIndex 把 L0 摘要渲染成索引正文（放在上下文最上层的那一段）。
//
// 形状与生产一致：一行一步，"目标 → 结果"，并保留关键标识符。
func renderIndex(sums []stepSummary) string {
	var b strings.Builder
	b.WriteString("【执行摘要索引（每一步的目标与结果）】\n")
	for _, s := range sums {
		fmt.Fprintf(&b, "- 步%d | 目标: %s | 结果: %s\n", s.StepIndex, s.Goal, s.Result)
	}
	return b.String()
}

// indexKeyRecall 测量：L0 索引行里保留住了多少关键标识符。
//
// ★ 这是「模型能不能据此判断『要不要去加载这一步的细节』」的代理指标：
//
//	如果索引行里连文件路径都没有，模型就没有理由去重载那一段。
func indexKeyRecall(sums []stepSummary, specs []stepSpec) float64 {
	if len(sums) == 0 {
		return 0
	}
	hit := 0
	for i, s := range sums {
		if i >= len(specs) {
			break
		}
		line := s.Goal + " " + s.Result
		for _, k := range specs[i].keys {
			if strings.Contains(line, k) {
				hit++
				break
			}
		}
	}
	return float64(hit) / float64(len(sums))
}

// ---- L1 明细（按需加载）---------------------------------------------------

// stepDetail 是一步的明细（可按需加载的那一块）。
type stepDetail struct {
	StepIndex int
	Messages  []wireMsg
	Tokens    int
}

// buildStepDetails 从 L1 存储（delta/resp 轮序列）重建每一步的明细。
//
// 这段重建逻辑与 domains/session/v2/turn_reader.go 的 LoadChain 同口径：
// 按轮序拼接 (request_delta ++ response_delta)。
func buildStepDetails(turns []turnRec) []stepDetail {
	order := []int{}
	byStep := map[int][]turnRec{}
	for _, t := range turns {
		if _, ok := byStep[t.stepIdx]; !ok {
			order = append(order, t.stepIdx)
		}
		byStep[t.stepIdx] = append(byStep[t.stepIdx], t)
	}
	out := make([]stepDetail, 0, len(order))
	for _, si := range order {
		var msgs []wireMsg
		for _, t := range byStep[si] {
			msgs = append(msgs, t.delta...)
			msgs = append(msgs, t.resp...)
		}
		out = append(out, stepDetail{
			StepIndex: si + 1,
			Messages:  msgs,
			Tokens:    estimateTokens(joinText(msgs)),
		})
	}
	return out
}

// reloadStep 按需加载某一步的明细，返回逐字节可比的文本。
//
// ★ 这模拟「客户端/模型按 step id 点名要某一步的细节」这一动作。
func reloadStep(details []stepDetail, stepIdx int) (string, bool) {
	for _, d := range details {
		if d.StepIndex == stepIdx {
			return joinText(d.Messages), true
		}
	}
	return "", false
}

// ---- 分层上下文装配 --------------------------------------------------------

// layerPlan 是一次分层装配的结果与计量。
type layerPlan struct {
	IndexTokens   int
	TailTokens    int
	ReloadTokens  int
	TotalTokens   int
	FullTokens    int
	TrimTokens    int
	SummaryTokens int
	KeptSteps     int
	TotalSteps    int
	Reloadable    []int // 在窗口预算内还能再加载几步
}

// assembleLayered 装配分层上下文：
//
//	[system] + [L0 索引] + [最近 tailMsgs 条原始消息] + [按需加载的步骤明细]
//
// tailMsgs 与 keepRecentPairs 的取法对齐
// domains/hooks/compression/rebuilder_openai.go 的 splitSystemAndTail。
//
// budget = 上下文窗口（token）⇒ 用于计算「预算内还能再按需加载几步」。
func assembleLayered(
	full []wireMsg,
	sums []stepSummary,
	details []stepDetail,
	tailMsgs int,
	reloadSteps []int,
	budget int,
) layerPlan {
	idxText := renderIndex(sums)
	plan := layerPlan{
		IndexTokens: estimateTokens(idxText),
		TotalSteps:  len(sums),
		KeptSteps:   len(sums),
	}
	plan.FullTokens = estimateTokens(joinText(full))

	// 最近尾部：system 之后的消息末尾 tailMsgs 条。
	tail := tailMessages(full, tailMsgs)
	plan.TailTokens = estimateTokens(joinText(tail))

	// 按需加载的步骤明细。
	for _, si := range reloadSteps {
		if d, ok := findDetail(details, si); ok {
			plan.ReloadTokens += d.Tokens
		}
	}
	plan.TotalTokens = plan.IndexTokens + plan.TailTokens + plan.ReloadTokens

	// 现状基线①：LLM 摘要替换头部（一段结构化摘要）。
	plan.SummaryTokens = estimateTokens(simulateLLMSummary(full))

	// 现状基线②：机械裁剪（drop-oldest），裁到与分层方案同预算。
	plan.TrimTokens = estimateTokens(joinText(trimToBudget(full, plan.TotalTokens)))

	// 窗口预算内还能再加载几步（分层基座 = 索引 + 尾部）。
	base := plan.IndexTokens + plan.TailTokens
	used := base
	for _, d := range details {
		if used+d.Tokens > budget {
			continue
		}
		used += d.Tokens
		plan.Reloadable = append(plan.Reloadable, d.StepIndex)
	}
	return plan
}

// tailMessages 取末尾 n 条消息（不含 system 头）。
func tailMessages(msgs []wireMsg, n int) []wireMsg {
	if n <= 0 || len(msgs) == 0 {
		return nil
	}
	start := 0
	for i, m := range msgs {
		if msgRole(m) != "system" {
			start = i
			break
		}
	}
	body := msgs[start:]
	if len(body) <= n {
		return body
	}
	return body[len(body)-n:]
}

// simulateLLMSummary 模拟现状的 LLM 压缩产物（一段结构化摘要）。
//
// 形状对齐 domains/hooks/compression/compaction.go 的 compactionSystemPrompt
// 的七段式摘要，量级按「原文的 12%」估计（摘要模型对长会话的常见压缩比）。
func simulateLLMSummary(full []wireMsg) string {
	fullText := joinText(full)
	target := int(float64(len(fullText)) * 0.12)
	if target < 400 {
		target = 400
	}
	var b strings.Builder
	b.WriteString("## 用户意图\n")
	b.WriteString(truncate(fullText, target/4))
	b.WriteString("\n## 已完成工作\n")
	b.WriteString(truncate(fullText, target/2))
	b.WriteString("\n## 工具结果与数据\n")
	b.WriteString(truncate(fullText, target/4))
	b.WriteString("\n## 错误与纠正\n")
	b.WriteString(truncate(fullText, target/8))
	b.WriteString("\n## 进行中工作\n")
	b.WriteString(truncate(fullText, target/8))
	return b.String()
}

// trimToBudget 做 drop-oldest 机械裁剪（现状兜底档），裁到不超过 budget token。
func trimToBudget(msgs []wireMsg, budget int) []wireMsg {
	out := append([]wireMsg(nil), msgs...)
	for estimateTokens(joinText(out)) > budget && len(out) > 1 {
		// 从最旧的非 system 消息开始成对丢弃（与
		// domains/transformation/ctx_compress.go 的 drop-oldest 同策）。
		idx := -1
		for i, m := range out {
			if msgRole(m) != "system" {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		out = append(out[:idx], out[idx+1:]...)
	}
	return out
}

// findDetail 按步号找明细。
func findDetail(details []stepDetail, stepIdx int) (stepDetail, bool) {
	for _, d := range details {
		if d.StepIndex == stepIdx {
			return d, true
		}
	}
	return stepDetail{}, false
}

// storageAmplification 量化「理想 delta 形态」与「生产实际形态」的存储差额。
//
// 生产实际形态 = 每轮都把客户端完整历史整份落成 request_delta
// （domains/session/v2/session_writer_v2.go 的 FIXME-14372/14373 描述的
// 主形态）；理想形态 = 只落本轮新增。
func storageAmplification(turns []turnRec) (idealBytes, prodBytes int) {
	for _, t := range turns {
		idealBytes += len(joinText(t.delta))
		prodBytes += len(joinText(t.body))
	}
	return idealBytes, prodBytes
}
