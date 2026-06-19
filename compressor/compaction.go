// Package compressor - compaction.go (Round 47 / v7 T12)
//
// Migrated from routing/context_summarize.go (June 2026). The three-tier
// 4xx-recovery path (Memora L1 inject → LLM summary → mechanical trim) is
// the natural home of the compressor package; keeping it in routing forced
// every new compression feature (V7 T-NEW-1/2/3, T13, T14) to touch both
// packages, which is the opposite of separation-of-concerns.
//
// What lives here now:
//   - defaultCompactionMinWindow / compactionSystemPrompt / compactionModelsFromEnv
//   - pickCompactionCandidates (was Executor.pickCompactionCandidates)
//   - tryMemoraCompression     (was Executor.tryMemoraCompression)
//   - tryLLMContextCompaction  (was Executor.tryLLMContextCompaction)
//   - invokeCompactionSummarize + invokeOpenAISummarize + invokeAnthropicSummarize
//   - doCompactionUpstream + applyCompactionRequestHeaders + doCompactionHTTP
//
// What stays in routing:
//   - handleContextLengthRecovery (the 3-tier state machine driver).
//     Now a thin wrapper that calls the compressor functions below and
//     records the chosen tier on contextLengthRecoveryState.
//   - buildCompressionMeta (the JSONB payload helper, used by the
//     state machine driver).
//
// Migration notes:
//   - All public compressor functions take a *Dependencies struct so
//     callers don't need to plumb 6 separate pointers. Pass &Dependencies{
//     Memora: e.Memora, Provider: e.Provider} from the Executor.
//   - Provider and Memora are interfaces in compressor/ (not concrete
//     types) so we can mock them in tests without pulling in the full
//     provider package.
//
// See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §3.4 + §12.

package compressor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/memora"
)

// MemoraClient is the subset of *memora.Client that the compaction flow
// needs. We declare it as an interface here (instead of importing the
// concrete type) so tests can inject a fake without spinning up Memora.
type MemoraClient interface {
	Disabled() bool
	Search(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
	// SmartSearch is the M1 (2026-06-19) high-recall retrieval entry point.
	// Today it safely delegates to user-scoped single-vector search; it is
	// future-ready for the Dashboard RRF+MMR pipeline once that endpoint
	// supports user_id filtering (see memora/client.go).
	SmartSearch(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
}

// ProviderClient is the subset of *provider.Client that pickCompactionCandidates
// needs. Same rationale as MemoraClient — interface for testability.
type ProviderClient interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile string) ([]ProviderCandidate, error)
}

// ProviderCandidate is the minimal candidate shape pickCompactionCandidates
// reads. We don't import the concrete provider.Candidate because the
// full struct has 30+ fields and we only need 3 here.
type ProviderCandidate struct {
	CredentialID  int
	ProviderID    int
	RawModel      string
	BaseURL       string
	APIKey        string
	Protocol      string
	ContextWindow *int
	Available     bool
}

// Dependencies bundles the external clients a Compressor needs for the
// 4xx recovery flow. Build it once at executor init and pass it to
// each recovery call. Nil-tolerant: nil Memora/Provider means the
// corresponding tier is skipped.
type Dependencies struct {
	Memora   MemoraClient
	Provider ProviderClient
}

// upstreamHTTPDoer is the small HTTP shim compaction uses to talk to the
// summarization model. Real builds get httpClientDoer (default); tests
// can inject a fake.
type upstreamHTTPDoer func(req *http.Request) (*http.Response, error)

var defaultHTTPDoer upstreamHTTPDoer = func(req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: 120 * time.Second}
	return client.Do(req)
}

// defaultCompactionMinWindow is the minimum context window an LLM
// summarization model must have to be eligible. Keeps "minimax-text-01"
// and "gemini-2.5-flash" (both 1M+ context) in, drops anything smaller
// that would itself OOM on a 900K-token conversation.
const defaultCompactionMinWindow = 800_000

// compactionSystemPrompt is the enhanced SESSION_MEMORY_PROMPT used by
// the v3 session-level compressor. Based on Anthropic's official template
// (https://platform.claude.com/cookbook/misc-session-memory-compaction)
// with key enhancements for lossless compression:
//
//  1. LOSSLESS_FIRST: all exact values (IDs, paths, keys, numbers, errors)
//     are preserved verbatim — no paraphrasing of factual data.
//  2. KEY_QUOTES: technical statements the user made are quoted directly.
//  3. DENSE_STRUCTURE: 7 sections ordered by decision-relevance so the
//     downstream LLM can pick up exactly where the conversation left off.
//  4. NEVER_DROP: tool results, error messages, corrections, and negative
//     feedback are always kept — these signal past failures the LLM must
//     not repeat.
//
// v3 adds the "## Tool Results & Data" section to capture tool call
// outputs (file contents, API responses, query results) that would
// otherwise be lost by mechanical trim but are critical for correctness.
const compactionSystemPrompt = `你是一个专业的对话历史压缩专家，负责将长对话历史压缩为结构化摘要供下游LLM继续工作。

## 核心原则：内容不丢失

**绝对不能丢失的内容（逐字保留）：**
- 用户说过的所有错误报告和纠错（"不对"、"这里有bug"、"你搞错了"等）
- 精确标识符：文件路径、URL、ID、API Key前缀、端口号、IP地址、变量名、函数名
- 具体数值：版本号、行号、数量、金额、时间戳、配置参数
- 工具调用结果的关键数据（SQL查询结果、API响应中的重要字段、文件内容摘要）
- 用户的负面反馈和修正指令（必须完整保留，LLM不能重复已被纠正的错误）

**必须以直接引用方式保留：**
- 用户的第一条消息（完整原文）
- 用户提出的需求变更和补充说明
- 所有报错信息的完整内容

<summary-format>
## 用户意图（User Intent）
[用户第一条消息原文] + [后续需求演化和补充说明]

## 已完成工作（Completed Work）
[已确认完成的具体任务，包含精确的文件路径、函数名、变更内容]

## 工具结果与数据（Tool Results & Data）
[工具调用返回的关键数据：SQL结果、API响应字段、文件内容摘要、命令输出]
[格式：<tool: 工具名> → <关键数据>]

## 错误与纠正（Errors & Corrections）
[所有报错信息（逐字）+ 用户对LLM的纠正（逐字）+ 已解决方案]

## 进行中工作（Active Work）
[当前正在处理的任务，精确到最后操作的文件/函数/步骤]

## 待办任务（Pending Tasks）
[明确的待办项，按优先级排列]

## 关键引用（Key References）
[所有重要的路径、URL、配置值、代码片段、变量名的精确列表]
</summary-format>

<rules>
1. 每个section不为空则必须填写，宁可冗长也不能遗漏关键事实
2. 数字、路径、标识符必须精确复制，不能改写
3. 工具结果不能只写"调用了工具"，要写出结果的关键内容
4. 用户纠正必须用引号标注原文："用户说：xxx"
5. 禁止输出preamble或markdown代码块包裹
6. 禁止对具体技术值进行模糊化（如不能把"端口8080"写成"某端口"）
</rules>

仅输出压缩后的结构化摘要，不输出任何说明文字。`

// compactionSystemPromptEN is the English variant. Used when task_type
// or upstream model prefers English (e.g. code_debug on non-CN models).
const compactionSystemPromptEN = `You are a professional conversation compressor. Produce a lossless structured summary of the conversation history for downstream LLM continuation.

## Core Rule: LOSSLESS

**Verbatim preserve (NEVER paraphrase):**
- All user corrections and negative feedback ("wrong", "that's a bug", "you misunderstood")
- Exact identifiers: file paths, URLs, IDs, API key prefixes, ports, IPs, variable/function names
- Exact numbers: version numbers, line numbers, counts, amounts, timestamps, config params
- Tool call output data (SQL results, API response fields, file content summaries)
- Error messages in full

**Quote directly:**
- First user message (complete original text)
- Requirement changes and additions
- All error messages verbatim

<summary-format>
## User Intent
[First user message verbatim] + [subsequent requirement evolution]

## Completed Work
[Confirmed-done tasks with exact file paths, function names, changes]

## Tool Results & Data
[Key data from tool calls: SQL results, API response fields, file content, command output]
[Format: <tool: name> → <key data>]

## Errors & Corrections
[All error messages (verbatim) + user corrections to LLM (verbatim) + resolutions]

## Active Work
[Current in-progress task, precise to last-touched file/function/step]

## Pending Tasks
[Explicit pending items, priority-ordered]

## Key References
[All important paths, URLs, config values, code snippets, variable names — exact list]
</summary-format>

<rules>
1. Never leave a non-empty section blank; prefer verbose over omitting key facts
2. Numbers, paths, identifiers must be copied exactly — no rewording
3. Tool results: write the actual key output, not just "tool was called"
4. User corrections must use quotes: "User said: xxx"
5. No preamble or markdown fence wrappers
6. Never vague-ify technical values (e.g., never write "some port" instead of "port 8080")
</rules>

Output only the structured summary, no explanatory text.`

// compactionPromptForTaskType returns the best compaction prompt for the
// given task type and language. Falls back to the default CN prompt for
// unknown task types so callers don't need to guard on nil.
//
// v3 T22: wrap the base prompts with task-specific preservation hints.
// The base prompt (compactionSystemPrompt) is always included verbatim;
// the suffix adds task-specific "also preserve" clauses so the LLM knows
// which domain facts matter most for this task class.
//
// Decision: CN prompt is default because our primary users and upstream
// models (minimax-text-01) work better with Chinese instructions.
// English suffix is appended for code_* tasks since code identifiers and
// error messages are usually in English anyway.
func compactionPromptForTaskType(taskType string) string {
	switch taskType {
	case "code_debug", "code_review", "code_refactor":
		return compactionSystemPromptEN + `

## Additional preservation rules for CODE tasks:
- Stack traces: preserve complete, including all frame lines
- Compiler/linter errors: exact message + file:line
- Diff/patch fragments: keep as-is, never summarise hunks
- Test names and assertion messages: verbatim
- Variable/function renames: record both old and new names`

	case "doc_translate":
		return compactionSystemPrompt + `

## 文档翻译任务附加保留规则：
- 源语言/目标语言对：逐字保留
- 专业术语对照表：完整保留所有条目
- 已翻译片段：保留原文+译文对照
- 格式要求（段落、标题层级、表格）：精确描述`

	case "data_analysis", "sql":
		return compactionSystemPromptEN + `

## Additional preservation rules for DATA tasks:
- SQL queries: preserve complete query text, not just intent
- Schema definitions: table names, column names, data types
- Query results: key rows/counts/aggregates verbatim
- Data quality issues found: exact description + affected rows/columns`

	case "deployment", "devops", "infra":
		return compactionSystemPrompt + `

## 部署/运维任务附加保留规则：
- 所有服务器IP、端口、域名：精确保留
- 命令输出的关键行（error/warning/success状态）：逐字保留
- 环境变量名（不含值）：完整列出
- 部署步骤执行状态（已完成/失败/待执行）：精确记录
- Rollback相关信息：完整保留`

	default:
		return compactionSystemPrompt
	}
}

// compactionModelsFromEnv returns ordered compaction model IDs. Lower
// index = preferred (cost/affinity). Caller walks the slice in order
// and stops on the first summarization success.
func compactionModelsFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_COMPACTION_MODELS"))
	if raw == "" {
		return []string{"minimax-text-01", "gemini-2.5-flash"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"minimax-text-01", "gemini-2.5-flash"}
	}
	return out
}

// compactionDisabled returns true when LLM-summary compaction is turned
// off via env. Kept as a separate helper so the operator's kill-switch
// doesn't require touching code.
func compactionDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_COMPACTION_DISABLE")))
	return v == "1" || v == "true" || v == "yes"
}

// pickCompactionCandidates returns every viable summarization candidate
// in env-preference order. Used by tryLLMContextCompaction to build
// the fallback chain: model1+credA → model1+credB → model2+credA → ...
//
// Lower index = preferred. The caller walks the slice in order and
// stops on the first summarization success.
func pickCompactionCandidates(ctx context.Context, deps *Dependencies, profile string) []ProviderCandidate {
	if deps == nil || deps.Provider == nil || !deps.Provider.Enabled() {
		return nil
	}
	minWindow := defaultCompactionMinWindow
	var out []ProviderCandidate
	for _, model := range compactionModelsFromEnv() {
		cands, err := deps.Provider.GetCandidates(ctx, model, profile)
		if err != nil {
			slog.Debug("compaction: resolve candidates failed", "model", model, "error", err)
			continue
		}
		added := 0
		for i := range cands {
			c := cands[i]
			if !c.Available {
				continue
			}
			if c.ContextWindow != nil && *c.ContextWindow < minWindow {
				continue
			}
			out = append(out, c)
			added++
		}
		if added == 0 {
			slog.Debug("compaction: no available candidates for model", "model", model)
		}
	}
	return out
}

// tryMemoraCompression searches Memora for the task's most relevant L1
// facts and rebuilds the body around them as a "dynamic_context" user
// message. Best-effort: any failure (Memora down, 0 results, parse
// error) returns (nil, false) and the caller falls through to LLM
// summary.
//
// Moved from routing/context_summarize.go on 2026-06-18 as part of v7 T12.
// Now accepts *Dependencies + tenantID/apiKeyID explicitly (instead of
// reaching into the Executor struct) so it can be tested in isolation.
func tryMemoraCompression(ctx context.Context, deps *Dependencies, tenantID string, apiKeyID int, body []byte, taskID string, clientProtocol string) ([]byte, bool) {
	if deps == nil || deps.Memora == nil || deps.Memora.Disabled() || len(body) == 0 {
		return nil, false
	}
	if taskID == "" {
		return nil, false
	}
	userID := memora.UserID(tenantID, apiKeyID, taskID)
	if userID == "" {
		return nil, false
	}

	// Warm-up guard: skip L1 injection when Memora has too few facts
	// accumulated for this user. v7 §2: LLM_GATEWAY_COMPRESSION_WARMUP_MIN_FACTS.
	warmup := warmupMinFacts()
	if warmup > 0 {
		probe, err := deps.Memora.Search(ctx, userID, "*", 1)
		if err == nil && len(probe) < warmup {
			return nil, false
		}
	}

	// Build a "what is the user trying to do right now?" query from the
	// last few messages.
	query := buildMemoraQuery(body, clientProtocol)
	if query == "" {
		return nil, false
	}

	// Retrieve the most relevant L1 facts via the M1 (2026-06-19) high-recall
	// entry point. SmartSearch is future-ready for the Dashboard RRF+MMR
	// pipeline; today it safely delegates to user-scoped single-vector search
	// (the smart endpoint lacks user_id filtering — see memora/client.go).
	snippets, err := deps.Memora.SmartSearch(ctx, userID, query, 8)
	if err != nil || len(snippets) == 0 {
		return nil, false
	}

	// Use the rebuilder (T6) to splice the snippets into a fresh
	// dynamic_context user message. RebuildBodyWithMemoraSnippets is in
	// the memora package (existing implementation).
	newBody, ok := memora.RebuildBodyWithMemoraSnippets(body, snippets, 2)
	if !ok {
		return nil, false
	}
	return newBody, true
}

// warmupMinFacts reads LLM_GATEWAY_COMPRESSION_WARMUP_MIN_FACTS. v7 §2
// default = 3.
func warmupMinFacts() int {
	raw := os.Getenv("LLM_GATEWAY_COMPRESSION_WARMUP_MIN_FACTS")
	if raw == "" {
		return 3
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 3
	}
	return v
}

// buildMemoraQuery turns the last few messages of an OpenAI/Anthropic
// conversation into a plain-text query for Memora's /product/search.
// Deliberately simple — the LLM-summary pass (next tier) does the
// heavy semantic lifting.
func buildMemoraQuery(body []byte, protocol string) string {
	text, err := extractConversationText(body, protocol)
	if err != nil || strings.TrimSpace(text) == "" {
		return ""
	}
	if len(text) > 1500 {
		text = text[len(text)-1500:]
	}
	return text
}

// tryLLMContextCompaction summarizes oversized history using a large-
// context model, then rebuilds the client wire body with [summary +
// recent tail]. Returns the new source body and true when compaction
// succeeded.
//
// Falls back across the env-configured compaction model list (and
// across credentials within a model) so a single down/saturated
// credential does not abort recovery.
func tryLLMContextCompaction(ctx context.Context, deps *Dependencies, clientProfile string, clientProtocol string, body []byte) ([]byte, bool) {
	if compactionDisabled() {
		return body, false
	}

	conversation, err := extractConversationText(body, clientProtocol)
	if err != nil || strings.TrimSpace(conversation) == "" {
		slog.Warn("compaction: extract conversation failed", "error", err)
		return body, false
	}
	// Keep summarize input under ~900k tokens (heuristic) so 1M models fit.
	conversation = trimTextToTokenBudget(conversation, 900_000)

	candidates := pickCompactionCandidates(ctx, deps, clientProfile)
	if len(candidates) == 0 {
		slog.Warn("compaction: no candidate",
			"configured_models", compactionModelsFromEnv(),
		)
		return body, false
	}

	for i := range candidates {
		cand := candidates[i]
		summary, sErr := invokeCompactionSummarize(ctx, deps, &cand, conversation)
		if sErr != nil {
			slog.Warn("compaction: summarize call failed, trying next",
				"attempt", i+1, "of", len(candidates),
				"compact_model", cand.RawModel,
				"error", sErr,
			)
			continue
		}
		summary = strings.TrimSpace(summary)
		if summary == "" {
			slog.Warn("compaction: empty summary returned, trying next",
				"attempt", i+1, "of", len(candidates),
				"compact_model", cand.RawModel,
			)
			continue
		}

		// Splice the summary back into the body. We delegate to the
		// existing rebuilder helpers in compressor/ (T6/T7) which
		// handle A-track / B-track retention (system + first user).
		var newBody []byte
		if clientProtocol == "anthropic-messages" {
			ret, retErr := extractAnthropic(body)
			if retErr != nil {
				slog.Warn("compaction: rebuilder extractAnthropic failed", "error", retErr)
				continue
			}
			out, ok := RebuildAnthropicAfterSummary(body, summary, ret, 2)
			if !ok {
				continue
			}
			newBody = out
		} else {
			ret, retErr := extractOpenAI(body)
			if retErr != nil {
				slog.Warn("compaction: rebuilder extractOpenAI failed", "error", retErr)
				continue
			}
			out, ok := RebuildOpenAIAfterSummary(body, summary, ret, 2)
			if !ok {
				continue
			}
			newBody = out
		}
		return newBody, true
	}
	return body, false
}

// invokeCompactionSummarize dispatches to the per-protocol summarizer
// (OpenAI or Anthropic) based on the candidate's Protocol.
func invokeCompactionSummarize(ctx context.Context, deps *Dependencies, cand *ProviderCandidate, conversation string) (string, error) {
	timeout := 120 * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if cand.Protocol == "anthropic-messages" {
		return invokeAnthropicSummarize(ctx, deps, cand, conversation)
	}
	return invokeOpenAISummarize(ctx, deps, cand, conversation)
}

// invokeOpenAISummarize calls the candidate as an OpenAI chat endpoint
// with the compaction system prompt + conversation as the user message.
// Returns the assistant's first-message content (the summary).
func invokeOpenAISummarize(ctx context.Context, deps *Dependencies, cand *ProviderCandidate, conversation string) (string, error) {
	userContent := "Summarize the following conversation history:\n\n" + conversation
	payload, _ := json.Marshal(map[string]any{
		"model":       cand.RawModel,
		"max_tokens":  4096,
		"temperature": 0.2,
		"stream":      false,
		"messages": []map[string]string{
			{"role": "system", "content": compactionSystemPrompt},
			{"role": "user", "content": userContent},
		},
	})

	req, err := buildCompactionRequest(ctx, cand, payload, false)
	if err != nil {
		return "", err
	}
	resp, err := defaultHTTPDoer(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxCompactionBody)))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("openai summarize upstream %d: %s", resp.StatusCode, truncateForLog(body, 200))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("openai summarize: empty choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

// invokeAnthropicSummarize calls the candidate as an Anthropic Messages
// endpoint with the compaction system prompt in the system field.
func invokeAnthropicSummarize(ctx context.Context, deps *Dependencies, cand *ProviderCandidate, conversation string) (string, error) {
	userContent := "Summarize the following conversation history:\n\n" + conversation
	payload, _ := json.Marshal(map[string]any{
		"model":      cand.RawModel,
		"max_tokens": 4096,
		"system":     compactionSystemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userContent},
		},
	})

	req, err := buildCompactionRequest(ctx, cand, payload, true)
	if err != nil {
		return "", err
	}
	resp, err := defaultHTTPDoer(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxCompactionBody)))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("anthropic summarize upstream %d: %s", resp.StatusCode, truncateForLog(body, 200))
	}

	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" && block.Text != "" {
			b.WriteString(block.Text)
		}
	}
	if b.Len() == 0 {
		return "", errors.New("anthropic summarize: empty content")
	}
	return b.String(), nil
}

// buildCompactionRequest constructs the http.Request to the summarization
// model. Supports both OpenAI chat completions and Anthropic messages.
func buildCompactionRequest(ctx context.Context, cand *ProviderCandidate, payload []byte, anthropic bool) (*http.Request, error) {
	var url string
	if anthropic {
		url = strings.TrimRight(cand.BaseURL, "/") + "/v1/messages"
	} else {
		url = strings.TrimRight(cand.BaseURL, "/") + "/v1/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if anthropic {
		req.Header.Set("x-api-key", cand.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+cand.APIKey)
	}
	return req, nil
}

const maxCompactionBody = 4 << 20 // 4 MiB; the summary is bounded by max_tokens

// trimTextToTokenBudget keeps the summarize input under tokenBudget
// tokens by hard-truncating chars at the head. 3.5 chars/token heuristic
// matches compressor/estimator.go.
func trimTextToTokenBudget(text string, tokenBudget int) string {
	maxChars := tokenBudget * 7 / 2 // ×3.5 chars/token
	if len(text) <= maxChars {
		return text
	}
	return text[len(text)-maxChars:]
}

// truncateForLog is a small helper for log output that bounds the
// preview length so a giant upstream error body doesn't blow out slog.
func truncateForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}

// buildUserSummaryInstruction composes the user-role prompt for the
// summariser LLM. Language matches the system prompt (which is determined
// by taskType). When taskType is code_* / data_*, English; otherwise Chinese.
//
// Language consistency matters: a Chinese system prompt "你是一个专业的对话历史压缩专家"
// paired with an English user message "Summarize the following..." degrades
// summary quality — the LLM has to switch language mid-call. This function
// keeps both halves of the prompt in the same language.
func buildUserSummaryInstruction(taskType, conversation string) string {
	if strings.HasPrefix(taskType, "code_") || strings.HasPrefix(taskType, "data_") {
		return "Summarize the following conversation history:\n\n" + conversation
	}
	return "请压缩以下对话历史为结构化摘要：\n\n" + conversation
}
