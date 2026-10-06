// Package streaming — audio_transform.go
//
// POST /v1/audio/refine + POST /v1/audio/analyze —— 转写后处理二件套
// （2026-10-06 ASR 多模型轮新增，openpocket 会议流是首个调用方）：
//
//	refine  精细化转写：把 ASR 原始输出规范化——标点、语流清理（语气词/
//	        口吃/重复）、数字与单位归一（ITN）、热词字形成形。**不改语义
//	        不增删事实**，不确定时保留原文。
//	analyze 及时总结分析：对转写文本（可与上一轮摘要增量合并）给出滚动
//	        摘要、要点、决定、行动项、开放问题与**实时提示**（值得追问的
//	        点/风险/时间信号），供录制中的 UI 周期性刷新。
//
// 两者都是「文本进、JSON 出」的 LLM 调用，经**环回** /v1/chat/completions
// 完成：转发调用方自己的 Authorization，让 chat 面现成的路由/预算/审计/
// 故障转移全量生效（autoroute 的 HTTP LLM caller 同款思路，但这里转发
// 调用方 key 而不是服务 key——成本归属谁调用谁承担）。拒绝直接在音频面
// 重新实现一遍 chat 执行器：那是几百行协议适配的复制品。
//
// 鉴权与 /v1/audio/* 同款（Bearer sk-*）；错误信封与 writeAudioError 同
// 口径（上游 4xx 分流、no_provider/audio_capacity 语义类型）。
package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// maxTransformBodyBytes 限制 refine/analyze 的请求体（纯文本，不需要
// 音频级上限）；512KB 文本约等于 2 小时会议的逐字稿。
const maxTransformBodyBytes = 512 << 10

// maxTransformDuration 约束单次 refine/analyze 总时长。LLM 长文输出
// （滚动摘要 + 全量结构字段）在慢上游上可能超过 chat 面的常规预算，
// 120s 与 maxAudioRequestDuration(10min) 之间取贴近实际的值。
const maxTransformDuration = 120 * time.Second

// AudioTransformService 是 refine/analyze 的共享核。svc 只用它的鉴权；
// LLM 调用走环回 HTTP（不走 s.upstream 的代理解析——环回目标是自己，
// 出代理没有意义且可能被上游 ACL 拒）。
type AudioTransformService struct {
	svc *AudioService
	hc  *http.Client
	// baseURL 覆盖环回基址（如反代形态下网关自己不可直接达时）。空 =
	// 按请求的 Host/X-Forwarded-Proto 推导。
	baseURL string
	// maxTokens 是给 LLM 的输出预算。默认 8192：refine 全文重写 +
	// analyze 滚动摘要都要完整 JSON；reasoning 系模型会先烧 thinking
	// 预算（autoroute O4 同款教训），太小会拿到空 content。
	maxTokens int
}

func NewAudioTransformService(svc *AudioService) *AudioTransformService {
	ts := &AudioTransformService{
		svc:       svc,
		hc:        &http.Client{Timeout: maxTransformDuration},
		maxTokens: 8192,
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_AUDIO_TRANSFORM_LLM_BASE")); v != "" {
		ts.baseURL = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_AUDIO_TRANSFORM_MAX_TOKENS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ts.maxTokens = n
		}
	}
	return ts
}

// AudioTransformHandler 服务 /v1/audio/refine 与 /v1/audio/analyze 两条
// 路径（同一 mux 挂载点，按路径分发）。
type AudioTransformHandler struct {
	ts *AudioTransformService
}

func NewAudioTransformHandler(ts *AudioTransformService) *AudioTransformHandler {
	return &AudioTransformHandler{ts: ts}
}

func (h *AudioTransformHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if h.ts == nil || h.ts.svc == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Audio transform service unavailable", "server_error", "service_unavailable")
		return
	}
	if _, ok := h.ts.svc.authenticate(w, r, requestID); !ok {
		return
	}
	switch r.URL.Path {
	case "/v1/audio/refine":
		h.ts.serveRefine(w, r, requestID)
	case "/v1/audio/analyze":
		h.ts.serveAnalyze(w, r, requestID)
	default:
		writeErrorJSON(w, http.StatusNotFound, requestID, "Unknown transform path", "invalid_request_error", "not_found")
	}
}

// ── refine ────────────────────────────────────────────────────────────

// RefineOps 是精细化的开关集。nil 字段 = 按默认（全开）。
type RefineOps struct {
	Punctuation *bool `json:"punctuation,omitempty"`
	Disfluency  *bool `json:"disfluency,omitempty"`
	ITN         *bool `json:"itn,omitempty"`
}

func (o RefineOps) punctuation() bool { return o.Punctuation == nil || *o.Punctuation }
func (o RefineOps) disfluency() bool  { return o.Disfluency == nil || *o.Disfluency }
func (o RefineOps) itn() bool         { return o.ITN == nil || *o.ITN }

type RefineCorrection struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason,omitempty"`
}

type RefineResult struct {
	Refined      string             `json:"refined"`
	Corrections  []RefineCorrection `json:"corrections,omitempty"`
	IgnoredWords []string           `json:"ignored_hotwords,omitempty"`
	LLMModel     string             `json:"llm_model,omitempty"`
}

// serveRefine 处理 POST /v1/audio/refine。
//
// 请求：{"model":"<chat 模型,必填>","text":"<ASR 原文,必填>","language":"zh",
//
//	"hotwords":["达摩院"],"context":"产品评审会","ops":{...},
//	"include_corrections":true}
//
// 响应：{"refined":"...","corrections":[...],"llm_model":"..."}
func (ts *AudioTransformService) serveRefine(w http.ResponseWriter, r *http.Request, requestID string) {
	var req struct {
		Model              string    `json:"model"`
		Text               string    `json:"text"`
		Language           string    `json:"language"`
		Hotwords           []string  `json:"hotwords"`
		Context            string    `json:"context"`
		Ops                RefineOps `json:"ops"`
		IncludeCorrections bool      `json:"include_corrections"`
	}
	if !ts.decodeTransformBody(w, r, requestID, &req) {
		return
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Text) == "" {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "model and text are required", "invalid_request_error", "invalid_audio_request")
		return
	}

	sys := buildRefineSystemPrompt(req.Ops, req.IncludeCorrections)
	user := buildRefineUserPrompt(req.Text, req.Language, req.Hotwords, req.Context)

	content, llmModel, err := ts.callLoopbackChat(r.Context(), r, requestID, req.Model, sys, user)
	if err != nil {
		writeAudioError(w, requestID, err)
		return
	}
	var out struct {
		Refined     string             `json:"refined"`
		Corrections []RefineCorrection `json:"corrections"`
	}
	if err := extractLLMJSON(content, &out); err != nil {
		writeErrorJSON(w, http.StatusBadGateway, requestID,
			"refine LLM returned unparseable output: "+sanitizeAudioErrorMessage(err.Error()),
			"server_error", "upstream_error")
		return
	}
	if strings.TrimSpace(out.Refined) == "" {
		// LLM 拒答/空输出时如实报错，不拿空串覆盖调用方文本——那等于
		// 帮客户端把转写结果清空。
		writeErrorJSON(w, http.StatusBadGateway, requestID,
			"refine LLM returned empty refined text", "server_error", "upstream_error")
		return
	}
	res := RefineResult{Refined: out.Refined, LLMModel: llmModel}
	if req.IncludeCorrections {
		res.Corrections = out.Corrections
	}
	// 热词回执：精修结果里没出现的词单独列出（可能是同音字形没纠过来，
	// 也可能确实没说）。静默吞掉热词会让调用方误以为提示词已生效——
	// 与 chat-audio 的 IgnoredParams 回执同一原则。
	res.IgnoredWords = hotwordsMissingFrom(out.Refined, req.Hotwords)
	writeTransformJSON(w, requestID, res)
}

// ── analyze ───────────────────────────────────────────────────────────

type AnalyzeActionItem struct {
	Text  string `json:"text"`
	Owner string `json:"owner,omitempty"`
}

type AnalyzeResult struct {
	Summary       string              `json:"summary"`
	KeyPoints     []string            `json:"key_points"`
	Decisions     []string            `json:"decisions"`
	ActionItems   []AnalyzeActionItem `json:"action_items"`
	OpenQuestions []string            `json:"open_questions"`
	Hints         []string            `json:"hints"`
	Topics        []string            `json:"topics"`
	LLMModel      string              `json:"llm_model,omitempty"`
}

// analyzeStyles 是 style 参数的合法集（其他值按 auto 处理并回执语义）。
var analyzeStyles = map[string]bool{
	"auto": true, "meeting": true, "interview": true,
	"customer_service": true, "lecture": true,
}

// serveAnalyze 处理 POST /v1/audio/analyze。
//
// 请求：{"model":"<chat 模型,必填>","transcript":"<转写文本,必填>",
//
//	"prior_summary":"<上一轮滚动摘要,可空>","style":"meeting",
//	"language":"zh","max_points":8}
//
// 响应：{"summary":"<滚动更新后的全文摘要>","key_points":[...],
//
//	"decisions":[...],"action_items":[...],"open_questions":[...],
//	"hints":[...],"topics":[...],"llm_model":"..."}
//
// 增量模式：调用方周期性把「上一轮 summary + 新增片段」发来，返回的
// summary 就是合并后的全文摘要——网关无状态，滚动状态由调用方持有。
func (ts *AudioTransformService) serveAnalyze(w http.ResponseWriter, r *http.Request, requestID string) {
	var req struct {
		Model        string `json:"model"`
		Transcript   string `json:"transcript"`
		PriorSummary string `json:"prior_summary"`
		Style        string `json:"style"`
		Language     string `json:"language"`
		MaxPoints    int    `json:"max_points"`
	}
	if !ts.decodeTransformBody(w, r, requestID, &req) {
		return
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Transcript) == "" {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "model and transcript are required", "invalid_request_error", "invalid_audio_request")
		return
	}
	style := strings.ToLower(strings.TrimSpace(req.Style))
	if !analyzeStyles[style] {
		style = "auto"
	}
	if req.MaxPoints <= 0 || req.MaxPoints > 30 {
		req.MaxPoints = 8
	}

	sys := buildAnalyzeSystemPrompt(style, req.Language, req.MaxPoints)
	user := buildAnalyzeUserPrompt(req.Transcript, req.PriorSummary)

	content, llmModel, err := ts.callLoopbackChat(r.Context(), r, requestID, req.Model, sys, user)
	if err != nil {
		writeAudioError(w, requestID, err)
		return
	}
	var out AnalyzeResult
	if err := extractLLMJSON(content, &out); err != nil {
		writeErrorJSON(w, http.StatusBadGateway, requestID,
			"analyze LLM returned unparseable output: "+sanitizeAudioErrorMessage(err.Error()),
			"server_error", "upstream_error")
		return
	}
	// summary 是本端点的核心承诺（滚动摘要语义依赖它非空）——空则如实
	// 报错；其余列表字段为空是合法状态（文本太短/无行动项）。
	if strings.TrimSpace(out.Summary) == "" {
		writeErrorJSON(w, http.StatusBadGateway, requestID,
			"analyze LLM returned empty summary", "server_error", "upstream_error")
		return
	}
	out.LLMModel = llmModel
	writeTransformJSON(w, requestID, out)
}

// ── 共用件 ────────────────────────────────────────────────────────────

// decodeTransformBody 限长读入并解析 JSON 请求体；失败时已写响应。
func (ts *AudioTransformService) decodeTransformBody(w http.ResponseWriter, r *http.Request, requestID string, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTransformBodyBytes))
	if err != nil {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge, requestID, "Request body too large", "invalid_request_error", "request_too_large")
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "invalid JSON body: "+err.Error(), "invalid_request_error", "invalid_audio_request")
		return false
	}
	return true
}

// callLoopbackChat 经环回 /v1/chat/completions 取一次 LLM 补全。转发调用
// 方 Authorization（成本/预算/租户归属调用方）；环回目标按请求推导或用
// 服务级覆盖。返回 content 与实际上游模型名。
func (ts *AudioTransformService) callLoopbackChat(ctx context.Context, r *http.Request, requestID, model, system, user string) (string, string, error) {
	endpoint := ts.baseURL
	if endpoint == "" {
		scheme := "http"
		if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
			scheme = "https"
		} else if r.TLS != nil {
			scheme = "https"
		}
		host := r.Host
		if host == "" {
			return "", "", newAudioClientInputError("cannot derive loopback endpoint: request has no Host")
		}
		endpoint = scheme + "://" + host
	}
	payload, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.1,
		"stream":      false,
		"max_tokens":  ts.maxTokens,
	})
	if err != nil {
		return "", "", fmt.Errorf("marshal chat payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", requestID)
	req.Header.Set("X-Gateway-Internal-Purpose", "audio_transform")

	resp, err := ts.hc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("loopback chat call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readLimitedResponse(resp.Body, 8<<10)
		return "", "", newAudioUpstreamError(resp.StatusCode, body)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Model string `json:"model"`
	}
	body, err := readLimitedResponse(resp.Body, 32<<20)
	if err != nil {
		return "", "", err
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("loopback chat response is not JSON: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", "", fmt.Errorf("loopback chat response has no choices")
	}
	return parsed.Choices[0].Message.Content, parsed.Model, nil
}

// extractLLMJSON 从 LLM 输出里抠出最外层 JSON 对象并解析。LLM 可能包
// markdown 代码栏或前后解释文字；取首个 '{' 到最后一个 '}' 的窗口。
func extractLLMJSON(content string, v any) error {
	s := strings.TrimSpace(content)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return fmt.Errorf("no JSON object in LLM output (%d chars)", len(s))
	}
	return json.Unmarshal([]byte(s[start:end+1]), v)
}

// hotwordsMissingFrom 返回没在精修结果字面出现的热词（大小写不敏感）。
func hotwordsMissingFrom(refined string, hotwords []string) []string {
	if len(hotwords) == 0 {
		return nil
	}
	low := strings.ToLower(refined)
	var missing []string
	for _, hw := range hotwords {
		hw = strings.TrimSpace(hw)
		if hw != "" && !strings.Contains(low, strings.ToLower(hw)) {
			missing = append(missing, hw)
		}
	}
	return missing
}

func writeTransformJSON(w http.ResponseWriter, requestID string, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// ── 提示词 ────────────────────────────────────────────────────────────

// buildRefineSystemPrompt 生成精修系统提示。四条规则各自可关；核心约束
// 是「不改语义」——ASR 精修器的第一罪过是把没说的内容补出来，宁缺毋滥。
func buildRefineSystemPrompt(ops RefineOps, includeCorrections bool) string {
	var b strings.Builder
	b.WriteString("你是严格的转写精修器。输入是语音识别(ASR)的原始输出，可能缺标点、带语气词、数字为汉字、专名字形成音。你的任务只做规范化，绝不增删或改写事实内容：\n")
	if ops.punctuation() {
		b.WriteString("1. 标点与分段：按语义补全/修正标点，长句适当断句。\n")
	}
	if ops.disfluency() {
		b.WriteString("2. 语流清理：删除无意义的语气词（嗯、啊、呃、那个……）、口吃重复与明显的自纠错（说出半句又重说的保留完整版）。\n")
	}
	if ops.itn() {
		b.WriteString("3. 数字与单位归一（ITN）：「百分之五十」→「50%」、「三点五公里」→「3.5公里」、「二零二六年」→「2026年」；日期、时间、金额、百分比优先用阿拉伯数字书写。\n")
	}
	b.WriteString("4. 专名与同音字：热词表给出的写法优先；同音字纠错仅在上下文有充分把握时进行，没有把握时保留原字。\n")
	b.WriteString("5. 不翻译、不概括、不补写没出现的内容；不确定时保留原文。输出语言与原文一致。\n")
	if includeCorrections {
		b.WriteString(`输出严格 JSON（不要 markdown 代码栏）：{"refined":"精修后的全文","corrections":[{"from":"原文片段","to":"修正后","reason":"简要原因"}]}。corrections 只列真实发生的实质修改（标点变化不必列）。`)
	} else {
		b.WriteString(`输出严格 JSON（不要 markdown 代码栏）：{"refined":"精修后的全文"}`)
	}
	return b.String()
}

func buildRefineUserPrompt(text, language string, hotwords []string, context string) string {
	var b strings.Builder
	if context = strings.TrimSpace(context); context != "" {
		fmt.Fprintf(&b, "【场景背景】%s\n", context)
	}
	if language = strings.TrimSpace(language); language != "" {
		fmt.Fprintf(&b, "【语言】%s\n", language)
	}
	if len(hotwords) > 0 {
		fmt.Fprintf(&b, "【热词表】（优先使用这些写法）%s\n", strings.Join(nonEmpty(hotwords), "、"))
	}
	fmt.Fprintf(&b, "【ASR 原文】\n%s", text)
	return b.String()
}

// buildAnalyzeSystemPrompt 生成分析系统提示。hints 是本端点的差异化价值：
// 不只总结过去，还给「接下来值得做什么」。
func buildAnalyzeSystemPrompt(style, language string, maxPoints int) string {
	var b strings.Builder
	b.WriteString("你是会议/通话的实时分析器。你会收到转写文本（可能还有上一轮的滚动摘要）。只依据文本本身，绝不编造。输出严格 JSON（不要 markdown 代码栏）：\n")
	b.WriteString(`{"summary":"全文摘要（若给了上一轮摘要，则输出合并新增内容后的**最新全文**摘要，不超过200字）",` +
		`"key_points":["要点，最多 ` + strconv.Itoa(maxPoints) + ` 条"],` +
		`"decisions":["已明确做出的决定"],` +
		`"action_items":[{"text":"待办","owner":"负责人,未提到则留空"}],` +
		`"open_questions":["尚未有答案的问题/分歧"],` +
		`"hints":["给主持人的实时提示：值得追问的点、风险信号、时间/预算数字待确认、情绪或节奏问题、建议的下一步"],` +
		`"topics":["讨论主题词"]}`)
	b.WriteString("\n要求：\n")
	b.WriteString("- 文本过短或还没有实质内容时，summary 如实说明（如「开始阶段，暂无实质内容」），其余数组给空。\n")
	b.WriteString("- hints 每条独立、可执行、指向具体的文本位置或内容，不超过 5 条；没有值得提示的就给空数组。\n")
	switch style {
	case "meeting":
		b.WriteString("- 场景是多人会议：关注决定、分工、时间点与分歧。\n")
	case "interview":
		b.WriteString("- 场景是访谈：关注受访者的关键观点、值得深挖的回答。\n")
	case "customer_service":
		b.WriteString("- 场景是客服通话：关注客户诉求、承诺的解决方案、升级风险与情绪信号。\n")
	case "lecture":
		b.WriteString("- 场景是讲座/课程：关注知识要点、概念定义与例子。\n")
	default:
		b.WriteString("- 场景未指定：按通用对话分析。\n")
	}
	if lang := strings.TrimSpace(language); lang != "" && strings.HasPrefix(strings.ToLower(lang), "zh") {
		b.WriteString("- 输出使用简体中文。\n")
	}
	return b.String()
}

func buildAnalyzeUserPrompt(transcript, priorSummary string) string {
	var b strings.Builder
	if priorSummary = strings.TrimSpace(priorSummary); priorSummary != "" {
		fmt.Fprintf(&b, "【上一轮滚动摘要】\n%s\n\n", priorSummary)
	}
	fmt.Fprintf(&b, "【转写文本】\n%s", transcript)
	return b.String()
}

func nonEmpty(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
