// Package streaming — summarize.go
//
// POST /v1/summarize + POST /v1/summarize/title + POST /v1/classify ——
// 文本加工三件套（2026-10-26 多模态标准接口轮）：
//
//	summarize/title  标题总结：≤20 字、动宾结构、含关键实体、拒绝「关于」
//	                式开头；一次生成 n 个候选，网关侧按规则校验择优。
//	summarize        内容/会话总结：短文本（<3000 字）单次渲染；长文本走
//	                map(分段结构化抽取) → reduce(时间线合并) → render(纪要
//	                渲染) 三层管线，输出会议纪要风格 markdown。
//	classify         任务分类：受控词表 + 严格 JSON 输出。
//
// 三者都是「文本进、JSON 出」的 LLM 编排，与 audio_transform 同款经环回
// /v1/chat/completions 完成（转发调用方自己的 Authorization，路由/预算/
// 审计/故障转移全量复用 chat 面；成本归属调用方）。提示词源自 2026-10-26
// 《嵌入与会话总结优化方案》已验收的三个 SKILL.md（task-classifier /
// title-writer / session-summarist），移植进网关使其成为一等标准接口——
// 调用方不再各自拼 prompt、各自做校验。
//
// 鉴权与 refine/analyze 同口径（Bearer sk-*，不消耗 RPM——环回 chat 步
// 已计一次限流，见 authenticateNoRPM 的注释）。
package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// ── 尺寸与时限 ────────────────────────────────────────────────────────

const (
	// maxSummarizeBodyBytes 限制三件套的请求体。512KB 文本约等于 2 小时
	// 会议逐字稿的 4 倍余量，与 audio_transform 同款上限。
	maxSummarizeBodyBytes = 512 << 10
	// maxSummarizeDuration 约束单次 summarize 请求总时长（含 map-reduce
	// 全部串行 LLM 调用）。10min 与音频面对齐；90 分钟会议（~2 万字 →
	// 3 段 map + reduce + render，30b 58tok/s + 27b 渲染）实测口径在
	// 5min 量级，这里留 2 倍余量。
	maxSummarizeDuration = 10 * time.Minute
	// asmDelegateBudgetDivisor 决定 asm 委托最多能拿走总预算的几分之一。
	//
	// ★ 为什么要切分（2026-10-27 审计轮）：委托与降级**共用同一把预算**时，
	//   委托一旦超时（它存在的意义就是处理长文本 ⇒ 慢），父 ctx 就被吃空，
	//   而**降级管线恰恰只在委托失败时才跑** ⇒ 降级在唯一需要它的场景里
	//   不可用（读数：map 阶段 context deadline exceeded ⇒ 502）。
	//   ⇒ 委托只拿 1/3，降级永远留有 ≥2/3 的预算。
	asmDelegateBudgetDivisor = 3
	// summarizeShortPathRunes 短文本阈值（字）：低于它跳过 map-reduce，
	// 单次调用完成抽取+渲染（技能 §降级与边界同款）。
	summarizeShortPathRunes = 3000
	// defaultSummarizeChunkRunes 每段 map 的目标字数。8000 字/段让 2 万
	// 字会话落在 3 段（5 次 LLM 调用），避开 12 RPM key 的限流带。
	defaultSummarizeChunkRunes = 8000
	// maxSummarizeChunks 硬上限：段数 × 调用数（map N + reduce + render
	// = N+2）必须落在默认 12 RPM 之内，10 段封顶（12 次调用）。
	maxSummarizeChunks = 10
	// summarizeLoopback429RetryDelay 环回 chat 撞 429（key RPM）时的
	// 单次重试间隔。map-reduce 是串行调用，长段间隔天然稀释 RPM，这里
	// 只兜「两次调用恰好落进同一窗口」的边界。
	summarizeLoopback429RetryDelay = 6 * time.Second
)

// ── 服务与处理器 ──────────────────────────────────────────────────────

// SummarizeKeyVerifier 是三件套需要的鉴权面（与 embeddingKeyVerifier
// 同一形状；不引入 RPM limter——见 authenticateNoRPM）。
type SummarizeKeyVerifier interface {
	Enabled() bool
	Verify(ctx context.Context, rawKey string) (*authentication.KeyInfo, error)
	CheckBudget(ctx context.Context, keyID int) error
}

// SummarizeService 是三件套的共享核：只做鉴权与环回 LLM 编排，不持有
// provider/upstream——模型路由完全交给环回 chat 面。
type SummarizeService struct {
	keyVerifier SummarizeKeyVerifier
	hc          *http.Client
	// baseURL 覆盖环回基址（反代形态下网关自己不可直接达时）。空 = 按
	// 请求的 Host/X-Forwarded-Proto 推导。与 AudioTransformService 同款。
	baseURL string
	// asm 是 ai-session-manager 在线优先委托器（nil = 未启用，长文本恒走
	// 网关管线）。见 summarize_asm.go：在线时委托其智能体端点，失败响亮
	// 降级回本服务的 map-reduce——两条路都跑本地模型。
	asm *asmDelegate
}

func NewSummarizeService() *SummarizeService {
	ts := &SummarizeService{
		hc: &http.Client{
			// 客户端不设总超时：各阶段用 ctx 预算（map/reduce/render
			// 时长差异大，一个全局值要么掐死 render 要么放任 map）。
			Timeout: 0,
		},
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SUMMARIZE_LLM_BASE")); v != "" {
		ts.baseURL = strings.TrimRight(v, "/")
	}
	return ts
}

// SetAuth wires the data-plane key verifier (RPM limiter 刻意不接：环回
// chat 步携带调用方 key，已由 chat 面计一次限流)。
func (s *SummarizeService) SetAuth(keyVerifier *authentication.KeyVerifier) {
	s.keyVerifier = keyVerifier
}

// SummarizeHandler serves the three text-refinement paths.
type SummarizeHandler struct {
	svc *SummarizeService
}

func NewSummarizeHandler(svc *SummarizeService) *SummarizeHandler {
	return &SummarizeHandler{svc: svc}
}

func (h *SummarizeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = generateRequestID()
	}
	w.Header().Set("X-Request-Id", requestID)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErrorJSON(w, http.StatusMethodNotAllowed, requestID, "Method not allowed", "invalid_request_error", "method_not_allowed")
		return
	}
	if h.svc == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Summarize service unavailable", "server_error", "service_unavailable")
		return
	}
	if _, ok := authenticateNoRPM(h.svc.keyVerifier, w, r, requestID); !ok {
		return
	}
	switch r.URL.Path {
	case "/v1/summarize":
		h.svc.serveSummarize(w, r, requestID)
	case "/v1/summarize/title":
		h.svc.serveSummarizeTitle(w, r, requestID)
	case "/v1/classify":
		h.svc.serveClassify(w, r, requestID)
	default:
		writeErrorJSON(w, http.StatusNotFound, requestID, "Unknown summarize path", "invalid_request_error", "not_found")
	}
}

// decodeSummarizeBody 限长读入并解析 JSON 请求体；失败时已写响应。
func (s *SummarizeService) decodeSummarizeBody(w http.ResponseWriter, r *http.Request, requestID string, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSummarizeBodyBytes))
	if err != nil {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge, requestID, "Request body too large", "invalid_request_error", "request_too_large")
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "invalid JSON body: "+err.Error(), "invalid_request_error", "invalid_summarize_request")
		return false
	}
	return true
}

// chatStageParams 是一次环回 LLM 调用的全部旋钮（按端点/阶段注入）。
type chatStageParams struct {
	model           string
	system          string
	user            string
	temperature     float64
	maxTokens       int
	reasoningEffort string // "" = 不发送
	timeout         time.Duration
}

// callChat 经环回 /v1/chat/completions 取一次补全。两类单次自适应重试：
//   - 429（key RPM）：等一个窗口原样重试；
//   - 400 "does not support thinking"（ollama 2026-10-27 实测）：非 thinking
//     模型（qwen2.5:7b 等）收到 reasoning_effort≠none 直接 400 ⇒ 剥掉
//     effort 重发一次。端点无法预知模型是否支持 thinking，这一步让
//     「thinking 模型保 effort、非 thinking 模型自动裸发」两边都通——
//     这正是整晚 map-on-7b 503 的真根因（title/classify 默认 none 恰好
//     不触发，summarize 默认 low 每发必中）。
func (s *SummarizeService) callChat(ctx context.Context, r *http.Request, requestID string, p chatStageParams) (string, string, error) {
	call := func(effort string) (string, string, error) {
		payload := map[string]any{
			"model":       p.model,
			"messages":    []map[string]string{{"role": "system", "content": p.system}, {"role": "user", "content": p.user}},
			"temperature": p.temperature,
			"stream":      false,
			"max_tokens":  p.maxTokens,
		}
		if effort != "" {
			payload["reasoning_effort"] = effort
		}
		stageCtx, cancel := context.WithTimeout(ctx, p.timeout)
		defer cancel()
		return loopbackChatCompletions(stageCtx, s.hc, s.loopbackEndpoint(r), r.Header.Get("Authorization"), "summarize", requestID, payload)
	}

	content, model, err := call(p.reasoningEffort)
	if isLoopbackRateLimitStatus(err) {
		// 环回撞 key RPM：等一个窗口再试一次。map-reduce 是串行编排，
		// 这里只兜边界，不做更长退避（调用方自己的 key 额度事实）。
		select {
		case <-time.After(summarizeLoopback429RetryDelay):
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
		content, model, err = call(p.reasoningEffort)
	}
	if isThinkingUnsupportedErr(err) && p.reasoningEffort != "" {
		content, model, err = call("")
	}
	if err != nil {
		return "", "", err
	}
	// thinking 模型（qwen3 系）可能把 reasoning 混进 content：剥离
	// <think>...</think> 块（2026-10-26 方案 §风险与对策同款处置）。
	return stripThinkBlock(content), model, nil
}

func (s *SummarizeService) loopbackEndpoint(r *http.Request) string {
	if s.baseURL != "" {
		return s.baseURL
	}
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		scheme = "https"
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// stripThinkBlock removes <think>…</think> reasoning leakage from content.
func stripThinkBlock(s string) string {
	if !strings.Contains(s, "<think>") {
		return s
	}
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "<think>")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		rest = rest[i+len("<think>"):]
		j := strings.Index(rest, "</think>")
		if j < 0 {
			// 未闭合的 think 块：丢弃到结尾（reasoning 泄漏的主体形态）。
			break
		}
		rest = rest[j+len("</think>"):]
	}
	return strings.TrimSpace(b.String())
}

// delegateBudgetFor 计算 asm 委托能拿到的 ctx 预算。
//
// ★ 规则：委托预算 = **min(配置的委托预算, 总预算 / asmDelegateBudgetDivisor)**。
//
//	理由见 asmDelegateBudgetDivisor 的注释：委托与降级共用一把预算时，
//	委托超时会吃空父 ctx，使降级在唯一需要它的场景里不可用。
//	⇒ 配置得再大（LLM_GATEWAY_ASM_TIMEOUT ≥ 总预算）也必须在此夹一次。
//
// ★ 契约：返回值**恒 ≤ total/asmDelegateBudgetDivisor**
//
//	⇒ 降级管线恒有 ≥ (1 - 1/divisor) 的总预算可用。
func delegateBudgetFor(total, asmBudget time.Duration) time.Duration {
	if total <= 0 {
		return asmBudget
	}
	if maxBudget := total / asmDelegateBudgetDivisor; asmBudget > maxBudget {
		return maxBudget
	}
	return asmBudget
}

// ── 标题总结（title-writer） ──────────────────────────────────────────

// titleBannedOpenings 是技能 title-writer 规则 3 的禁用开头集。
var titleBannedOpenings = []string{"关于", "有关", "对于", "浅析", "浅谈"}

type SummarizeTitleResult struct {
	Title      string   `json:"title"`
	Candidates []string `json:"candidates,omitempty"`
	LLMModel   string   `json:"llm_model,omitempty"`
}

// serveSummarizeTitle 处理 POST /v1/summarize/title。
//
// 请求：{"model":"<chat 模型,必填>","text":"<内容,必填>","n":3,
//
//	"max_chars":20,"language":"zh","reasoning_effort":"none"}
//
// 响应：{"title":"...","candidates":[...],"llm_model":"..."}
func (s *SummarizeService) serveSummarizeTitle(w http.ResponseWriter, r *http.Request, requestID string) {
	var req struct {
		Model           string `json:"model"`
		Text            string `json:"text"`
		N               int    `json:"n"`
		MaxChars        int    `json:"max_chars"`
		Language        string `json:"language"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if !s.decodeSummarizeBody(w, r, requestID, &req) {
		return
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Text) == "" {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "model and text are required", "invalid_request_error", "invalid_summarize_request")
		return
	}
	if req.N <= 0 || req.N > 5 {
		req.N = 3
	}
	if req.MaxChars <= 0 || req.MaxChars > 60 {
		req.MaxChars = 20
	}
	effort := req.ReasoningEffort
	if effort == "" {
		effort = "none"
	}

	sys := buildTitleSystemPrompt(req.N, req.MaxChars, req.Language)
	user := "## 待起标题的内容\n" + strings.TrimSpace(req.Text) + "\n\n只输出 JSON 数组。"

	p := chatStageParams{model: req.Model, system: sys, user: user, temperature: 0.3, maxTokens: 60 * req.N, reasoningEffort: effort, timeout: 120 * time.Second}
	content, llmModel, err := s.callChat(r.Context(), r, requestID, p)
	if err != nil {
		writeAudioError(w, requestID, err)
		return
	}
	candidates, perr := parseTitleCandidates(content, req.N)
	if perr != nil {
		// 一次结构重试：明确指出上一轮输出不合法（结构反馈比裸重跑命中率高）。
		retryUser := user + "\n\n上一轮输出不是合法的 JSON 字符串数组（"
		retryUser += perr.Error() + "），请严格只输出 JSON 数组，如 [\"标题一\",\"标题二\"]。"
		p2 := p
		p2.user = retryUser
		content, llmModel, err = s.callChat(r.Context(), r, requestID, p2)
		if err != nil {
			writeAudioError(w, requestID, err)
			return
		}
		candidates, perr = parseTitleCandidates(content, req.N)
		if perr != nil {
			writeErrorJSON(w, http.StatusBadGateway, requestID,
				"title LLM returned unparseable output: "+sanitizeAudioErrorMessage(perr.Error()),
				"server_error", "upstream_error")
			return
		}
	}

	valid := filterTitleCandidates(candidates, req.MaxChars)
	if len(valid) == 0 {
		// 规则重试：模型给出的候选全部违规（超长/禁用开头），点名违规项再要一轮。
		retrySys := sys + "\n\n注意：以下候选全部违规（超长或禁用开头），禁止再次输出：\n"
		for _, c := range candidates {
			retrySys += "- " + c + "\n"
		}
		p3 := p
		p3.system = retrySys
		content, llmModel, err = s.callChat(r.Context(), r, requestID, p3)
		if err != nil {
			writeAudioError(w, requestID, err)
			return
		}
		candidates, _ = parseTitleCandidates(content, req.N)
		valid = filterTitleCandidates(candidates, req.MaxChars)
		if len(valid) == 0 {
			writeErrorJSON(w, http.StatusBadGateway, requestID,
				"title LLM candidates all violate constraints (length/banned opening)",
				"server_error", "upstream_error")
			return
		}
	}
	title := pickTitle(valid)
	writeSummarizeJSON(w, requestID, SummarizeTitleResult{Title: title, Candidates: valid, LLMModel: llmModel})
}

// parseTitleCandidates 从 LLM 输出解析字符串数组（容忍围栏/前后文字）。
func parseTitleCandidates(content string, n int) ([]string, error) {
	s := strings.TrimSpace(content)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array in output (%d chars)", len(s))
	}
	var arr []string
	if err := json.Unmarshal([]byte(s[start:end+1]), &arr); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(arr))
	for _, t := range arr {
		if t = normalizeTitleLine(t); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty candidate array")
	}
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// normalizeTitleLine 去引号/首尾标点/首尾空白。
func normalizeTitleLine(t string) string {
	t = strings.TrimSpace(t)
	t = strings.Trim(t, "\"'“”‘’「」『』 ")
	t = strings.TrimRight(t, "。，,..!！?？;；")
	return strings.TrimSpace(t)
}

// filterTitleCandidates 按技能规则硬校验：≤maxChars 字、无禁用开头。
func filterTitleCandidates(candidates []string, maxChars int) []string {
	out := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, c := range candidates {
		if c == "" || utf8.RuneCountInString(c) > maxChars {
			continue
		}
		bad := false
		for _, b := range titleBannedOpenings {
			if strings.HasPrefix(c, b) {
				bad = true
				break
			}
		}
		if bad || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// pickTitle 在合法候选里择优：含实体信号（数字/拉丁字母）优先，同分取
// 更短（技能规则 5「砍修饰词不砍实体」的读法：信息密度高者优先）。
func pickTitle(valid []string) string {
	best := valid[0]
	bestScore := titleScore(best)
	for _, c := range valid[1:] {
		if sc := titleScore(c); sc > bestScore {
			best, bestScore = c, sc
		}
	}
	return best
}

func titleScore(t string) int {
	score := 0
	for _, r := range t {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			score++
		}
	}
	score = score*10 - utf8.RuneCountInString(t)
	return score
}

func buildTitleSystemPrompt(n, maxChars int, language string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "你是标题写手。为任务、会话或工单生成 %d 个互不相同的中文标题候选。规则：\n", n)
	fmt.Fprintf(&b, "1. 每个标题 ≤%d 个汉字；装不下时砍修饰词，不砍实体。\n", maxChars)
	b.WriteString("2. 动宾结构优先：`完成 252 出向流量诊断` 好于 `252 的流量问题`。\n")
	b.WriteString("3. 含关键实体：设备名/主机号/工单号/系统名/日期，至少其一；数字与专名保留原文（`G286`、`qwen3.8:27b-mlx`、`2026-10-26` 不翻译不改写）。\n")
	b.WriteString("4. 禁用开头：关于、有关、对于、浅析、浅谈；不加引号、不以标点结尾。\n")
	b.WriteString("5. 好：「修复 llm 网关 503：URSM 节点键过期」「llm 网关 503 排查与修复记录」；坏：「关于 211 服务器网关问题的一些说明」「一次精彩的排障之旅」。\n")
	if lang := strings.TrimSpace(language); lang != "" && strings.HasPrefix(strings.ToLower(lang), "en") {
		b.WriteString("6. 标题使用英文。\n")
	} else {
		b.WriteString("6. 标题使用简体中文。\n")
	}
	fmt.Fprintf(&b, "只输出 JSON 字符串数组（无其他文字、无代码栏），形如 [\"标题一\",\"标题二\",\"标题三\"]，恰好 %d 个。", n)
	return b.String()
}

// ── 任务分类（task-classifier） ───────────────────────────────────────

// defaultTaskVocab 是受控词表（一级类型 → 二级标签）。调用方可用请求的
// vocab 字段整体覆盖（自己的任务体系）；覆盖时同样只允许词表内输出。
var defaultTaskVocab = []taskVocabGroup{
	{"运维执行", []string{"巡检", "配置变更", "重启恢复", "发布部署"}},
	{"告警处置", []string{"告警确认", "根因定位", "止血操作"}},
	{"规划决策", []string{"方案设计", "技术选型", "容量规划"}},
	{"知识沉淀", []string{"文档编写", "复盘总结", "经验归档"}},
	{"协作沟通", []string{"会议", "汇报", "跨组协调"}},
	{"日常事务", []string{"预约", "提醒", "采购", "报销"}},
	{"开发调试", []string{"功能开发", "缺陷修复", "性能调优"}},
	{"数据处理", []string{"报表", "清洗", "迁移"}},
}

type taskVocabGroup struct {
	Type    string
	SubType []string
}

type ClassifyResult struct {
	TaskType   string   `json:"task_type"`
	SubTypes   []string `json:"sub_types"`
	Confidence float64  `json:"confidence"`
	Keywords   []string `json:"keywords"`
	Reason     string   `json:"reason"`
	LLMModel   string   `json:"llm_model,omitempty"`
}

type classifyRequest struct {
	Model           string          `json:"model"`
	Text            string          `json:"text"`
	RawVocab        json.RawMessage `json:"vocab"`
	ReasoningEffort string          `json:"reasoning_effort"`
}

// serveClassify 处理 POST /v1/classify。
//
// 请求：{"model":"<chat 模型,必填>","text":"<任务文本,必填>",
//
//	"vocab":[["一级类型",["二级标签",...]],...]（可选覆盖）,
//	"reasoning_effort":"none"}
//
// 响应：{"task_type":"...","sub_types":[...],"confidence":0.9,
//
//	"keywords":[...],"reason":"...","llm_model":"..."}
func (s *SummarizeService) serveClassify(w http.ResponseWriter, r *http.Request, requestID string) {
	var req classifyRequest
	if !s.decodeSummarizeBody(w, r, requestID, &req) {
		return
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Text) == "" {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "model and text are required", "invalid_request_error", "invalid_summarize_request")
		return
	}
	vocab := defaultTaskVocab
	if len(req.RawVocab) > 0 && string(req.RawVocab) != "null" {
		parsed, err := parseVocabOverride(req.RawVocab)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, requestID, "invalid vocab: "+err.Error(), "invalid_request_error", "invalid_summarize_request")
			return
		}
		if len(parsed) > 0 {
			vocab = parsed
		}
	}
	effort := req.ReasoningEffort
	if effort == "" {
		effort = "none"
	}

	sys := buildClassifySystemPrompt(vocab)
	user := "## 待分类任务\n" + strings.TrimSpace(req.Text) + "\n\n只输出 JSON。"

	p := chatStageParams{model: req.Model, system: sys, user: user, temperature: 0, maxTokens: 300, reasoningEffort: effort, timeout: 120 * time.Second}
	content, llmModel, err := s.callChat(r.Context(), r, requestID, p)
	if err != nil {
		writeAudioError(w, requestID, err)
		return
	}
	out, perr := parseClassifyOutput(content, vocab)
	if perr != nil {
		retryUser := user + "\n\n上一轮输出不合规（" + perr.Error() + "）。task_type 必须是词表中的一级类型，请严格按 schema 只输出 JSON。"
		p2 := p
		p2.user = retryUser
		content, llmModel, err = s.callChat(r.Context(), r, requestID, p2)
		if err != nil {
			writeAudioError(w, requestID, err)
			return
		}
		out, perr = parseClassifyOutput(content, vocab)
		if perr != nil {
			writeErrorJSON(w, http.StatusBadGateway, requestID,
				"classify LLM returned unparseable output: "+sanitizeAudioErrorMessage(perr.Error()),
				"server_error", "upstream_error")
			return
		}
	}
	out.LLMModel = llmModel
	writeSummarizeJSON(w, requestID, out)
}

// parseVocabOverride 解析调用方自定义词表，接受两种形状：
// [["类型",["标签",...]], ...] 或 [{"type":"...","sub_types":[...]}, ...]。
func parseVocabOverride(raw json.RawMessage) ([]taskVocabGroup, error) {
	var pairs [][]json.RawMessage
	if err := json.Unmarshal(raw, &pairs); err == nil {
		out := make([]taskVocabGroup, 0, len(pairs))
		for _, pair := range pairs {
			if len(pair) != 2 {
				return nil, fmt.Errorf("vocab entry must be [type, [subtypes]]")
			}
			var t string
			var subs []string
			if err := json.Unmarshal(pair[0], &t); err != nil {
				return nil, fmt.Errorf("vocab type must be a string")
			}
			if err := json.Unmarshal(pair[1], &subs); err != nil {
				return nil, fmt.Errorf("vocab subtypes must be a string array")
			}
			if strings.TrimSpace(t) == "" {
				return nil, fmt.Errorf("vocab type must be non-empty")
			}
			out = append(out, taskVocabGroup{Type: t, SubType: subs})
		}
		return out, nil
	}
	var objs []struct {
		Type     string   `json:"type"`
		TaskType string   `json:"task_type"`
		SubTypes []string `json:"sub_types"`
	}
	if err := json.Unmarshal(raw, &objs); err != nil {
		return nil, fmt.Errorf("vocab must be [[type,[subtypes]],...] or [{type,sub_types},...]")
	}
	out := make([]taskVocabGroup, 0, len(objs))
	for _, o := range objs {
		t := o.Type
		if t == "" {
			t = o.TaskType
		}
		if strings.TrimSpace(t) == "" {
			return nil, fmt.Errorf("vocab type must be non-empty")
		}
		out = append(out, taskVocabGroup{Type: t, SubType: o.SubTypes})
	}
	return out, nil
}

// parseClassifyOutput 解析并按受控词表校验分类输出。
func parseClassifyOutput(content string, vocab []taskVocabGroup) (ClassifyResult, error) {
	var out ClassifyResult
	if err := extractLLMJSON(content, &out); err != nil {
		return out, err
	}
	known := map[string]map[string]bool{}
	for _, g := range vocab {
		known[g.Type] = map[string]bool{}
		for _, st := range g.SubType {
			known[g.Type][st] = true
		}
	}
	out.TaskType = strings.TrimSpace(out.TaskType)
	if out.TaskType == "" {
		return out, fmt.Errorf("task_type is empty")
	}
	if _, ok := known[out.TaskType]; !ok {
		return out, fmt.Errorf("task_type %q not in controlled vocabulary", out.TaskType)
	}
	// sub_types 只保留词表内的（模型偶发的自造标签在这里拦下）。
	filtered := make([]string, 0, len(out.SubTypes))
	for _, st := range out.SubTypes {
		st = strings.TrimSpace(st)
		if st != "" && (known[out.TaskType][st] || inAnySubType(vocab, st)) {
			filtered = append(filtered, st)
		}
	}
	out.SubTypes = filtered
	return out, nil
}

func inAnySubType(vocab []taskVocabGroup, sub string) bool {
	for _, g := range vocab {
		for _, st := range g.SubType {
			if st == sub {
				return true
			}
		}
	}
	return false
}

func buildClassifySystemPrompt(vocab []taskVocabGroup) string {
	var b strings.Builder
	b.WriteString("你是任务分类器。把任务文本归入受控词表（只允许以下类型，禁止编造）：\n")
	for _, g := range vocab {
		fmt.Fprintf(&b, "- %s：%s\n", g.Type, strings.Join(g.SubType, " / "))
	}
	b.WriteString("\n判定规则：\n1. 按文本的核心动作归类：有执行对象与操作 → 运维执行/开发调试；有告警词（告警/异常/阈值）→ 告警处置；产出是文档/方案 → 知识沉淀/规划决策。\n2. 多类型并存时取占比最大者为一级，其余进 sub_types。\n3. confidence：词表直接命中 ≥0.85；语义推断 0.6–0.85；模棱两可 <0.6 并在 reason 说明。\n4. 宁可归「日常事务」也不编造词表外类型。\n\n")
	b.WriteString(`输出严格 JSON（无其他文字、无代码栏）：{"task_type":"<一级类型>","sub_types":["<二级标签>"],"confidence":0.0,"keywords":["3-6个关键词"],"reason":"≤30字判定理由"}` + "\n\n")
	b.WriteString("示例：输入「下午三点网络设备巡检会议，请提前准备好上周的流量报告和异常告警清单。」→\n")
	b.WriteString(`{"task_type":"协作沟通","sub_types":["巡检"],"confidence":0.9,"keywords":["网络设备","巡检会议","流量报告","告警清单"],"reason":"核心动作是组织会议，附带巡检准备事项"}`)
	return b.String()
}

// ── 内容/会话总结（session-summarist） ────────────────────────────────

type SummarizeResult struct {
	Summary  string `json:"summary"`
	Title    string `json:"title,omitempty"`
	Pipeline string `json:"pipeline"` // single | map-reduce | asm
	// Engine 记录实际执行方：asm（ai-session-manager 在线优先委托成功）
	// 或 gateway（本服务管线）。调用方据此感知降级。
	Chunks   int    `json:"chunks,omitempty"`
	Engine   string `json:"engine,omitempty"`
	LLMModel string `json:"llm_model,omitempty"`
}

// summarizeRequest 是 /v1/summarize 的请求体。
type summarizeRequest struct {
	Model           string `json:"model"`
	MapModel        string `json:"map_model"` // 长文本 map 层模型；空 = model
	Text            string `json:"text"`
	Style           string `json:"style"`    // minutes(默认) | brief
	Language        string `json:"language"` // 默认 zh
	ChunkChars      int    `json:"chunk_chars"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// serveSummarize 处理 POST /v1/summarize。
//
// 短文本（<3000 字）单次调用完成「抽取+渲染」；长文本走 map→reduce→render：
// 每段 map 产出结构化 JSON（参与者/议题/事实/决议/待办/金句），reduce 合并
// 去重并排时间线，render 渲染为纪要 markdown。分层与降级边界与技能
// session-summarist 一致（2026-10-26 方案 §3.2）。
func (s *SummarizeService) serveSummarize(w http.ResponseWriter, r *http.Request, requestID string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), maxSummarizeDuration)
	defer cancel()

	var req summarizeRequest
	if !s.decodeSummarizeBody(w, r, requestID, &req) {
		return
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Text) == "" {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "model and text are required", "invalid_request_error", "invalid_summarize_request")
		return
	}
	style := strings.ToLower(strings.TrimSpace(req.Style))
	if style != "brief" {
		style = "minutes"
	}
	effort := req.ReasoningEffort
	if effort == "" {
		effort = "low"
	}
	chunkChars := req.ChunkChars
	if chunkChars <= 0 {
		chunkChars = defaultSummarizeChunkRunes
	}
	if chunkChars < 2000 {
		chunkChars = 2000
	}
	if chunkChars > 16000 {
		chunkChars = 16000
	}
	lang := strings.TrimSpace(req.Language)
	if lang == "" {
		lang = "zh"
	}

	text := strings.TrimSpace(req.Text)
	runes := utf8.RuneCountInString(text)

	if runes < summarizeShortPathRunes {
		sys := buildRenderSystemPrompt(style, lang, true)
		user := "## 会话/内容记录\n" + text + "\n\n（短文本路径：先在内部完成分段抽取与合并，直接渲染最终输出。）"
		p := chatStageParams{model: req.Model, system: sys, user: user, temperature: 0.4, maxTokens: 4096, reasoningEffort: effort, timeout: 420 * time.Second}
		content, llmModel, err := s.callChat(ctx, r, requestID, p)
		if err != nil {
			writeAudioError(w, requestID, err)
			return
		}
		if err := validateRenderedSummary(content, style); err != nil {
			// 结构重试一次：render 输出缺必需节是模型漏节，不是内容问题。
			p2 := p
			p2.user = user + "\n\n注意：输出必须包含全部必需小节（" + err.Error() + "），请重新渲染完整输出。"
			content, llmModel, err = s.callChat(ctx, r, requestID, p2)
			if err != nil {
				writeAudioError(w, requestID, err)
				return
			}
			if verr := validateRenderedSummary(content, style); verr != nil {
				writeErrorJSON(w, http.StatusBadGateway, requestID,
					"summarize LLM output missing required sections: "+sanitizeAudioErrorMessage(verr.Error()),
					"server_error", "upstream_error")
				return
			}
		}
		res := SummarizeResult{Summary: content, Pipeline: "single", Engine: "gateway", LLMModel: llmModel}
		writeSummarizeJSON(w, requestID, res)
		logSummarizeOutcome(requestID, res, start, runes)
		return
	}

	// ── 长文本：asm 在线优先，失败响亮降级到网关管线 ──
	// 复杂总结交给 ai-session-manager 的智能体端点（grounded 纪要 + 原文
	// 金句；其 LLM 上游同样指向本地模型）。探测红/委托失败只记 WARN，
	// 绝不把失败透给调用方——engine 字段说明实际执行方。
	//
	// ★★ 预算必须**切分**，委托与降级不能共用同一把预算（2026-10-27 审计轮修）：
	//   委托的 ctx 从总预算 ctx 派生（见 summarize_asm.go），若委托把 8min
	//   全部烧完，父 ctx 只剩 ~2min ⇒ 降级管线的 map(240s)/reduce(300s)/
	//   render(420s) **全部撞上同一个快到期的 ctx** ⇒
	//   **降级恰恰在「委托超时」这一唯一需要它的场景里不可用**
	//   （读数：map 阶段 context deadline exceeded ⇒ 502 而不是摘要）。
	//   ⇒ 委托最多只拿总预算的 1/3，降级管线永远留有 ≥2/3 的预算。
	//   ★ 注意 `s.asm.budget` 可由 LLM_GATEWAY_ASM_TIMEOUT 配置成 ≥ 总预算，
	//     那种配置下委托会吃掉全部预算 ⇒ 降级预算为 0 ⇒ 必须在此**再夹一次**。
	if s.asm != nil && s.asm.available(ctx) {
		delegateBudget := delegateBudgetFor(maxSummarizeDuration, s.asm.budget)
		dctx, dcancel := context.WithTimeout(ctx, delegateBudget)
		asr, derr := s.asm.summarize(dctx, text, style, lang)
		dcancel()
		if derr == nil {
			res := SummarizeResult{
				Summary: asr.Summary, Title: asr.Title, Pipeline: "asm",
				Engine: "asm", LLMModel: asr.Model,
			}
			writeSummarizeJSON(w, requestID, res)
			logSummarizeOutcome(requestID, res, start, runes)
			return
		}
		// ★ 调用方已断开 ⇒ **不**把 asm 判成离线（它可能完全健康），
		//   也**不**再跑降级（没有接收方了）。
		if r.Context().Err() != nil {
			slog.Warn("summarize: caller gone before delegate finished; asm not marked offline",
				"request_id", requestID, "error", derr)
			return
		}
		s.asm.markOffline()
		slog.Warn("summarize: asm delegate failed; falling back to gateway map-reduce",
			"request_id", requestID, "error", derr)
	}

	// ── 长文本：map → reduce → render（网关管线） ──
	chunks := splitSummarizeChunks(text, chunkChars)
	if len(chunks) > maxSummarizeChunks {
		writeErrorJSON(w, http.StatusBadRequest, requestID,
			fmt.Sprintf("text too long: %d chunks (max %d, chunk_chars=%d); raise chunk_chars or split the text",
				len(chunks), maxSummarizeChunks, chunkChars),
			"invalid_request_error", "text_too_long")
		return
	}
	mapModel := req.MapModel
	if strings.TrimSpace(mapModel) == "" {
		mapModel = req.Model
	}

	segments := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		sys := buildMapSystemPrompt()
		user := fmt.Sprintf("## 第 %d/%d 段\n%s", i+1, len(chunks), chunk)
		// maxTokens 3000（而非 2000）：thinking 模型（qwen3 hybrid 不认
		// effort 的硬分离）先烧一段 reasoning 再产出 JSON——2000 会在思考
		// 段耗尽预算 ⇒ content 为空 ⇒ chat 面按 empty_response 故障转移
		// （211 实测：30b 连续空响应把 240s 预算全部烧在 failover 上）。
		p := chatStageParams{model: mapModel, system: sys, user: user, temperature: 0.2, maxTokens: 3000, reasoningEffort: effort, timeout: 240 * time.Second}
		content, _, err := s.callChat(ctx, r, requestID, p)
		if err != nil {
			writeAudioError(w, requestID, fmt.Errorf("map stage chunk %d/%d: %w", i+1, len(chunks), err))
			return
		}
		if !isJSONObjectish(content) {
			// 单段重试一次；仍失败则把该段降级为原文摘录交给 reduce
			//（一段 LLM 格式失败不该让整场总结失败——技能 §校验同款降级）。
			p2 := p
			p2.user = user + "\n\n上一轮输出不是 JSON 对象，请严格只输出 §M 结构的 JSON。"
			content, _, err = s.callChat(ctx, r, requestID, p2)
			if err != nil {
				writeAudioError(w, requestID, fmt.Errorf("map stage chunk %d/%d: %w", i+1, len(chunks), err))
				return
			}
			if !isJSONObjectish(content) {
				slog.Warn("summarize map stage chunk not JSON after retry; passing raw excerpt",
					"request_id", requestID, "chunk", i+1)
				content = `{"segment_range":"` + fmt.Sprintf("第%d段(降级为原文摘录)", i+1) + `","raw_excerpt":` + mustJSONString(truncateRunes(chunk, 1200)) + `}`
			}
		}
		segments = append(segments, content)
	}

	// brief 风格：reduce 层直接产出最终简报（跳过纪要渲染）。
	if style == "brief" {
		sys := buildBriefSystemPrompt(lang)
		user := buildReduceUserPrompt(segments)
		p := chatStageParams{model: mapModel, system: sys, user: user, temperature: 0.3, maxTokens: 2500, reasoningEffort: effort, timeout: 300 * time.Second}
		content, llmModel, err := s.callChat(ctx, r, requestID, p)
		if err != nil {
			writeAudioError(w, requestID, fmt.Errorf("reduce stage: %w", err))
			return
		}
		// ★ brief 风格同样要过校验（校验器对 brief 只要求「非空文本」）。
		//   为何必须：callChat 成功返回时 content 仍可能是**空串** ——
		//   thinking 模型只吐 reasoning 时，chat 面看到非空 content ⇒ 200，
		//   但 stripThinkBlock 把 reasoning 剥掉后就空了 ⇒
		//   **HTTP 200 + 空摘要**（minutes 路径与短文本路径都有校验，
		//   只有 brief 这条分支漏了 ⇒ 同一个请求换个 style 就出现两种语义）。
		if verr := validateRenderedSummary(content, style); verr != nil {
			p2 := p
			p2.user = user + "\n\n注意：上一轮输出为空，请直接输出简报正文（不要输出思考过程或空内容）。"
			content, llmModel, err = s.callChat(ctx, r, requestID, p2)
			if err != nil {
				writeAudioError(w, requestID, fmt.Errorf("reduce stage: %w", err))
				return
			}
			if verr2 := validateRenderedSummary(content, style); verr2 != nil {
				writeErrorJSON(w, http.StatusBadGateway, requestID,
					"summarize LLM output empty after think-stripping: "+sanitizeAudioErrorMessage(verr2.Error()),
					"server_error", "upstream_error")
				return
			}
		}
		res := SummarizeResult{Summary: content, Pipeline: "map-reduce", Chunks: len(chunks), Engine: "gateway", LLMModel: llmModel}
		writeSummarizeJSON(w, requestID, res)
		logSummarizeOutcome(requestID, res, start, runes)
		return
	}

	// reduce：合并全部段 JSON。
	sys := buildReduceSystemPrompt()
	user := buildReduceUserPrompt(segments)
	p := chatStageParams{model: mapModel, system: sys, user: user, temperature: 0.2, maxTokens: 3000, reasoningEffort: effort, timeout: 300 * time.Second}
	merged, _, err := s.callChat(ctx, r, requestID, p)
	if err != nil {
		writeAudioError(w, requestID, fmt.Errorf("reduce stage: %w", err))
		return
	}

	// render：纪要渲染（文风层，交给主模型）。
	sysRender := buildRenderSystemPrompt(style, lang, false)
	userRender := "## 合并后的结构化 JSON\n" + merged + "\n\n按纪要结构渲染最终输出。"
	pRender := chatStageParams{model: req.Model, system: sysRender, user: userRender, temperature: 0.4, maxTokens: 4096, reasoningEffort: effort, timeout: 420 * time.Second}
	content, llmModel, err := s.callChat(ctx, r, requestID, pRender)
	if err != nil {
		writeAudioError(w, requestID, fmt.Errorf("render stage: %w", err))
		return
	}
	if verr := validateRenderedSummary(content, style); verr != nil {
		p2 := pRender
		p2.user = userRender + "\n\n注意：输出必须包含全部必需小节（" + verr.Error() + "），请重新渲染完整输出。"
		content, llmModel, err = s.callChat(ctx, r, requestID, p2)
		if err != nil {
			writeAudioError(w, requestID, fmt.Errorf("render stage: %w", err))
			return
		}
		if verr2 := validateRenderedSummary(content, style); verr2 != nil {
			writeErrorJSON(w, http.StatusBadGateway, requestID,
				"summarize LLM output missing required sections: "+sanitizeAudioErrorMessage(verr2.Error()),
				"server_error", "upstream_error")
			return
		}
	}
	res := SummarizeResult{Summary: content, Pipeline: "map-reduce", Chunks: len(chunks), Engine: "gateway", LLMModel: llmModel}
	writeSummarizeJSON(w, requestID, res)
	logSummarizeOutcome(requestID, res, start, runes)
}

// splitSummarizeChunks 按目标字数切段，优先在换行处断开（不撕碎一行消息）。
func splitSummarizeChunks(text string, chunkChars int) []string {
	runes := []rune(text)
	if len(runes) <= chunkChars {
		return []string{text}
	}
	var chunks []string
	for start := 0; start < len(runes); {
		end := start + chunkChars
		if end >= len(runes) {
			end = len(runes)
		} else {
			// 在 [end-400, end] 窗口里找最后一个换行，避免半行断段。
			for i := end; i > end-400 && i > start; i-- {
				if runes[i-1] == '\n' {
					end = i
					break
				}
			}
		}
		chunk := strings.TrimSpace(string(runes[start:end]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		start = end
	}
	return chunks
}

func isJSONObjectish(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.HasPrefix(strings.TrimSpace(s), "{") && strings.Contains(s, "}")
}

// validateRenderedSummary 校验纪要输出含必需节（minutes 风格）。
// brief 风格只要求非空文本。
func validateRenderedSummary(content string, style string) error {
	c := strings.TrimSpace(content)
	if c == "" {
		return fmt.Errorf("empty output")
	}
	if style != "minutes" {
		return nil
	}
	required := []string{"回顾", "关键决议", "待办"}
	missing := make([]string, 0, len(required))
	for _, kw := range required {
		if !strings.Contains(c, kw) {
			missing = append(missing, kw)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing sections: %s", strings.Join(missing, ","))
	}
	return nil
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ── 提示词（移植自 2026-10-26 方案三技能，版本化进网关） ─────────────

// buildMapSystemPrompt §M：分段结构化抽取。
func buildMapSystemPrompt() string {
	return "你是会话/会议的结构化抽取器。把本段对话抽取为 JSON（禁止编造未出现的内容；转写错字按最接近原意修正后标注）：\n" +
		`{"segment_range":"[起始消息号, 结束消息号]","participants":["出现过的角色"],"topics":[{"topic":"议题","facts":["关键事实(带数字/实体)"],"open_questions":["未决问题"]}],"decisions":[{"decision":"决议","rationale":"理由","owner":"负责人|未指派"}],"todos":[{"item":"待办","due":"期限|未定","depends_on":"依赖|无"}],"conflicts":[{"between":"A与B","about":"分歧点","status":"解决方式|悬置"}],"quotes":["≤2条原话金句(保留原文)"],"tone":"整体氛围一瞥(≤10字)"}` + "\n" +
		"规则：只记录说出来的事实；数字、版本号、主机名逐字保留；推断要写成「（推断）」。只输出 JSON 对象，无其他文字、无代码栏。"
}

// buildReduceSystemPrompt §R：合并去重 + 时间线。
func buildReduceSystemPrompt() string {
	return "你会收到多段结构化 JSON（每段来自会话的一个分段）。合并它们：\n" +
		"1. 同议题跨段合并，facts 去重（同一事实不同说法取信息量最大者）；\n" +
		"2. 生成时间线：每议题一行 `[阶段] 议题 → 走向`；\n" +
		"3. decisions/todos 全量保留并按时间排序；open_questions 里已在后文解决的移除；quotes 只保留全篇最有代表性的 ≤3 条。\n" +
		"输出合并后的单个 JSON 对象（同输入结构，topics 换成 timeline 数组），无其他文字、无代码栏。"
}

func buildReduceUserPrompt(segments []string) string {
	var b strings.Builder
	for i, seg := range segments {
		fmt.Fprintf(&b, "## 段 %d\n%s\n\n", i+1, seg)
	}
	b.WriteString("请合并以上全部段。")
	return b.String()
}

// buildRenderSystemPrompt §O：生动化渲染（短文本路径 inline=true 时合并
// 抽取指令）。
func buildRenderSystemPrompt(style, language string, inline bool) string {
	var b strings.Builder
	if inline {
		b.WriteString("你是会话/内容总结器。对输入先做分段结构化抽取（参与者/议题/关键事实/决议/待办/金句，在内部完成，不要输出抽取过程），然后渲染最终输出。\n")
	} else {
		b.WriteString("你是纪要渲染器，把合并后的结构化 JSON 渲染为最终输出。\n")
	}
	if style == "brief" {
		b.WriteString("输出格式：先一段 ≤150 字的摘要（把发生了什么、结论是什么讲清楚），然后 ≤6 条要点（每条一行，动词开头）。不编造、不堆形容词。\n")
	} else {
		b.WriteString("输出结构（markdown）：\n" +
			"# 标题（≤20字，动宾结构）\n\n" +
			"## 一句话回顾\n≤30 字，有画面感：把这场会话最关键的一个瞬间/决定说成像场景一样。\n\n" +
			"## 时间线\n- [开场] 围绕什么聚到一起\n- [中段] 关键转折/发现（写清“谁发现了什么”）\n- [收尾] 如何散场：决定了什么/留下了什么\n\n" +
			"## 关键决议\n| 决议 | 为什么 | 谁负责 |\n|---|---|---|\n\n" +
			"## 待办\n| 事项 | 截止 | 依赖 |\n|---|---|---|\n\n" +
			"## 花絮（可选）\n≤2 条会话原文金句，注明说话角色；没有值得引的就整节省略。\n")
		b.WriteString("文风约束：白描、动词优先、每句 ≤30 字；禁止“深入探讨”“取得了丰硕成果”式套话；禁止感叹号超过一个；生动的抓手是结构、具体和原文引用，不是形容词。宁可短而具体，不可长而空泛。\n")
		b.WriteString("表格无内容时写“无”并保留表头，不许整节省略（一句话回顾/时间线/关键决议/待办四节必需）。\n")
	}
	if strings.HasPrefix(strings.ToLower(language), "en") {
		b.WriteString("输出使用英文。\n")
	} else {
		b.WriteString("输出使用简体中文。\n")
	}
	return b.String()
}

// buildBriefSystemPrompt 长文本 brief 风格：reduce 层直接产出简报。
func buildBriefSystemPrompt(language string) string {
	return "你会收到多段结构化 JSON（每段来自会话/内容的一个分段）。合并去重后直接输出最终简报：\n" +
		"1. 先一段 ≤200 字摘要（发生了什么、结论是什么）；\n" +
		"2. 然后 ≤8 条要点（每条一行，动词开头，保留数字/实体）；\n" +
		"3. 最后列决议与待办（如有，各一行一条）。\n" +
		"不编造、不堆形容词；只输出简报正文（markdown），无 JSON、无代码栏。\n" +
		outputLanguageDirective(language)
}

func outputLanguageDirective(language string) string {
	if strings.HasPrefix(strings.ToLower(language), "en") {
		return "输出使用英文。\n"
	}
	return "输出使用简体中文。\n"
}

// logSummarizeOutcome 是 /v1/summarize 的唯一完成记账点。
//
// 2026-10-27 审计轮（多模态端点调用方流量审计）：外层 /v1/summarize 调用
// 不进 request_logs（该路径不走记账中间件），asm 委托成功路径此前完全静默、
// 只有降级才打 WARN ⇒ engine=asm 与 engine=gateway 的占比在事后**无法回溯**
// （24h 审计实测：只能靠时长/字节推断，asm 成功样本只能从存留容器的
// 访问日志里考古）。这条 INFO 让每次完成的实际执行方可查：
// grep 'summarize: completed' 即得占比与降级率。
func logSummarizeOutcome(requestID string, res SummarizeResult, start time.Time, runes int) {
	slog.Info("summarize: completed",
		"request_id", requestID,
		"pipeline", res.Pipeline,
		"engine", res.Engine,
		"llm_model", res.LLMModel,
		"chunks", res.Chunks,
		"input_runes", runes,
		"duration_ms", time.Since(start).Milliseconds())
}

func writeSummarizeJSON(w http.ResponseWriter, requestID string, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Gw-Summarize", "1")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// ── 共享件（audio_transform 与本文件同一份实现） ─────────────────────

// authenticateNoRPM 是 refine/analyze/summarize 三面的共用鉴权：校验
// Bearer key、键状态与预算，但**不消耗 RPM**——这些端点的 LLM 步骤经环回
// chat 面携带同一把调用方 key，那一跳已计一次限流；这里再计一次会把一次
// 逻辑调用算成两次 RPM。直连打爆本面最终仍被环回 chat 步的同一把 key
// 限流拦住，不产生绕过。
func authenticateNoRPM(kv SummarizeKeyVerifier, w http.ResponseWriter, r *http.Request, requestID string) (*authentication.KeyInfo, bool) {
	if kv == nil || !kv.Enabled() {
		return nil, true
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "Missing API key", "authentication_error", "missing_key")
		return nil, false
	}
	keyInfo, err := kv.Verify(r.Context(), rawKey)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "Invalid or expired API key", "authentication_error", "invalid_key")
		return nil, false
	}
	if keyInfo.Status == "throttled" {
		ratelimit.MarkGatewaySharedKeyRateLimit(w)
		writeErrorJSON(w, http.StatusTooManyRequests, requestID, "API key throttled", "rate_limit_error", "key_throttled")
		return nil, false
	}
	if err := kv.CheckBudget(r.Context(), keyInfo.ID); err != nil {
		if _, exceeded := err.(*authentication.BudgetExceededError); exceeded {
			writeErrorJSON(w, http.StatusPaymentRequired, requestID, "Budget exhausted", "insufficient_quota", "budget_exhausted")
			return nil, false
		}
		slog.Error("summarize/transform budget check failed, failing closed",
			"request_id", requestID,
			"key_id", keyInfo.ID,
			"error", err)
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Budget check temporarily unavailable", "server_error", "budget_unavailable")
		return nil, false
	}
	return keyInfo, true
}

// loopbackChatCompletions 经环回 /v1/chat/completions 取一次补全（audio
// _transform 与 summarize 共用；转发调用方 Authorization，成本归属调用
// 方）。503 no_candidate 带 alternatives 时转成 noProvider 错误，方便调用
// 方换可路由模型。
func loopbackChatCompletions(ctx context.Context, hc *http.Client, endpoint, authHeader, purpose, requestID string, payload map[string]any) (string, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("marshal chat payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", requestID)
	req.Header.Set("X-Gateway-Internal-Purpose", purpose)

	resp, err := hc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("loopback chat call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := readLimitedResponse(resp.Body, 8<<10)
		if resp.StatusCode == http.StatusServiceUnavailable {
			if alts := extractAlternativeModels(raw); len(alts) > 0 {
				return "", "", newAudioNoProviderError(
					"loopback chat model %q has no routable candidate; gateway alternatives: %s",
					payload["model"], strings.Join(alts, ", "))
			}
			return "", "", newAudioNoProviderError(
				"loopback chat model %q has no routable candidate", payload["model"])
		}
		return "", "", newAudioUpstreamError(resp.StatusCode, raw)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Model string `json:"model"`
	}
	raw, err := readLimitedResponse(resp.Body, 32<<20)
	if err != nil {
		return "", "", err
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", fmt.Errorf("loopback chat response is not JSON: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", "", fmt.Errorf("loopback chat response has no choices")
	}
	return parsed.Choices[0].Message.Content, parsed.Model, nil
}

// isLoopbackRateLimitStatus 报告环回错误是否为上游 429（key RPM）。
func isLoopbackRateLimitStatus(err error) bool {
	if up, ok := err.(interface{ StatusCode() int }); ok {
		return up.StatusCode() == http.StatusTooManyRequests
	}
	return false
}

// isThinkingUnsupportedErr 报告环回错误是否为上游 400 且错误体是 ollama 的
// 「模型不支持 thinking」（reasoning_effort 发给了非 thinking 模型）。
// 按状态码+错误体判据识别，不猜模型名——模型能力是上游的事实。
func isThinkingUnsupportedErr(err error) bool {
	up, ok := err.(interface {
		StatusCode() int
		Body() string
	})
	if !ok || up.StatusCode() != http.StatusBadRequest {
		return false
	}
	return strings.Contains(up.Body(), "does not support thinking")
}
