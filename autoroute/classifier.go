// Package autoroute implements v2.0's "model=auto" smart routing mode.
//
// When a client sets `"model": "auto"` in their OpenAI-compatible request,
// the gateway instead:
//
//   1. Extracts signals from the request body (system prompt, message
//      count, estimated tokens, tool definitions, image parts, …)
//   2. Classifies the request into one of 8 task types
//      (chat/reasoning/code/agent/creative/long_context/vision/function_call)
//   3. Reads the live 5-min rolled-up index from credential_model_index
//      to find the best credential for the given profile × task type
//   4. Returns the chosen model's response, with a structured
//      X-Gw-Auto-Decision header that documents the decision
//
// Public API entry points (used by relay/handler.go):
//
//   - NewHeuristicClassifier()             : rule-based classifier
//   - NewDecider(classifier, index, ...)    : top-level decision orchestrator
//   - (*Decider).Decide(ctx, signals, ...)  : returns the chosen model
//
// Side effects:
//
//   - request_logs gains 5 columns (see docs/2026-06-15-auto-route-mode.sql)
//   - credential_model_index is refreshed every 5 min by
//     bg/auto_index_refresher.go
//
// Failure mode:
//
//   If classification fails entirely (heuristic + LLM), the gateway
//   falls back to the cheapest available chat model and logs a warning.
package autoroute

import (
	"context"
	"fmt"
	"strings"
)

// TaskType is one of 8 mutually exclusive request categories used by the
// auto route decider. Values must match the strings persisted in
// request_logs.task_type and model_task_index.task_type.
type TaskType string

const (
	// TaskChat is the catch-all for ordinary conversation that doesn't
	// match any other signal. Used when no specialised category applies.
	TaskChat TaskType = "chat"

	// TaskReasoning covers mathematical, logical, or multi-step
	// analytical tasks. Detected via reasoning keywords ("solve", "证明",
	// "推导", …) and step-by-step language patterns.
	TaskReasoning TaskType = "reasoning"

	// TaskCode covers code generation, completion, debugging, and
	// refactoring. Detected via code keywords ("function", "class",
	// "def ", "实现一个") and triple-backtick code blocks.
	TaskCode TaskType = "code"

	// TaskAgent covers multi-step agentic workflows that exercise
	// many tool calls per request. Detected when the request body
	// declares >= 3 tools AND the messages reference tool results.
	TaskAgent TaskType = "agent"

	// TaskCreative covers writing, translation, summarization, and
	// other open-ended generative tasks. Detected via keywords like
	// "write", "翻译", "总结", "story", "blog", …
	TaskCreative TaskType = "creative"

	// TaskLongContext covers prompts whose estimated token count
	// exceeds LongContextTokens (default 50k). Prioritises models
	// with large context windows.
	TaskLongContext TaskType = "long_context"

	// TaskVision covers requests that include image parts (OpenAI
	// `image_url` content blocks). Prioritises models declared as
	// supporting vision modality.
	TaskVision TaskType = "vision"

	// TaskFunctionCall covers simple function/tool invocation where
	// 1-2 tools are declared. Falls back to chat if more tools
	// are present (then it becomes TaskAgent).
	TaskFunctionCall TaskType = "function_call"
)

// AllTaskTypes is the canonical list used for validation and admin UI.
var AllTaskTypes = []TaskType{
	TaskChat, TaskReasoning, TaskCode, TaskAgent,
	TaskCreative, TaskLongContext, TaskVision, TaskFunctionCall,
}

// ClassificationSignals is the extracted request fingerprint fed into
// the classifier. Signals are derived from the raw request body in
// relay/handler.go before calling Decider.Decide.
//
// All fields are best-effort — the heuristic classifier is robust to
// missing or zero values; absent fields simply score 0 in that channel.
type ClassificationSignals struct {
	// SystemPrompt is the system message content (concatenated across
	// multiple system messages if present). Used for keyword scanning.
	SystemPrompt string

	// MessageCount is the total number of messages (system + user +
	// assistant + tool). Used as an agent-vs-chat disambiguator.
	MessageCount int

	// EstimatedTokens is the heuristic token estimate for the entire
	// request body. Used to flag long-context requests.
	EstimatedTokens int

	// ToolCount is the number of tools declared in the request's
	// `tools` array. >= 3 → agent candidate; 1-2 → function_call.
	ToolCount int

	// HasImages is true when any message part has type "image_url"
	// or "image". Forces vision task type regardless of other signals.
	HasImages bool

	// LastUserPrompt is the content of the final user-role message.
	// Used for keyword scanning (often the strongest task-type signal).
	LastUserPrompt string

	// Language is one of "zh", "en", "mixed". Detected from message
	// content (CJK density). Influences keyword matching but not the
	// final classification.
	Language string

	// HasCodeBlock is true when the request contains ```-fenced code
	// blocks or `inline` code. Boosts code-task score.
	HasCodeBlock bool

	// HasToolResults is true when any message has role "tool".
	// Combined with high ToolCount → agent task.
	HasToolResults bool
}

// Classification is the structured output of a classifier. The decider
// uses Primary + Confidence to pick a model; Secondary is returned in
// the X-Gw-Auto-Decision header for observability.
type Classification struct {
	// Primary is the highest-scoring task type.
	Primary TaskType

	// Confidence is 0.0-1.0. Heuristic classifiers typically reach 0.6-0.95;
	// anything below LLMConfidenceThreshold triggers an LLM fallback.
	Confidence float64

	// Secondary lists the top-N alternatives sorted by descending score.
	// Useful for debugging misclassifications in admin UI.
	Secondary []TaskScore

	// Signals echoes the input signals used for classification.
	// Stored in auto_decision JSONB for audit.
	Signals ClassificationSignals

	// Classifier names which path produced this result.
	// One of: "heuristic", "llm", "default".
	Classifier string

	// Reason is a human-readable explanation of the dominant signals
	// that drove the classification. Surfaced in admin UI.
	Reason string
}

// TaskScore pairs a task type with its computed score.
type TaskScore struct {
	Task  TaskType   `json:"task"`
	Score float64    `json:"score"` // 0.0-1.0 normalised
}

// HeuristicThresholds groups all tunable thresholds used by
// HeuristicClassifier. Values are loaded from env vars at startup;
// zero values fall back to the defaults in DefaultHeuristicThresholds.
type HeuristicThresholds struct {
	// LongContextTokens is the token-count threshold for TaskLongContext.
	// Default: 50000.
	LongContextTokens int

	// AgentToolThreshold is the minimum tool count to consider TaskAgent.
	// Default: 3.
	AgentToolThreshold int

	// FunctionCallToolMax is the maximum tool count that still counts as
	// TaskFunctionCall (above this, becomes TaskAgent).
	// Default: 2.
	FunctionCallToolMax int

	// LLMConfidenceThreshold is the cutoff below which the heuristic
	// answer is considered uncertain and an LLM fallback is invoked.
	// Default: 0.7.
	LLMConfidenceThreshold float64

	// KeywordWeight controls how much a single keyword hit raises a
	// task type's score (0.0-1.0). Multiple hits accumulate up to 1.0.
	// Default: 0.2 per hit.
	KeywordWeight float64

	// TokenWeight controls how strongly the token count channel scores
	// for TaskLongContext. Default: 0.5.
	TokenWeight float64
}

// DefaultHeuristicThresholds returns sensible defaults for production.
func DefaultHeuristicThresholds() HeuristicThresholds {
	return HeuristicThresholds{
		LongContextTokens:      50_000,
		AgentToolThreshold:     3,
		FunctionCallToolMax:    2,
		LLMConfidenceThreshold: 0.7,
		KeywordWeight:          0.2,
		TokenWeight:            0.5,
	}
}

// KeywordSet groups task-keyword associations. Keywords are case-folded
// at load time and matched as substrings in normalised message text.
//
// The defaults below are loaded from keywords.yaml at startup. Hand-curated
// for zh + en coverage; admins can override via admin/auto_route API.
type KeywordSet struct {
	Reasoning []string `yaml:"reasoning" json:"reasoning"`
	Code      []string `yaml:"code" json:"code"`
	Creative  []string `yaml:"creative" json:"creative"`
}

// DefaultKeywords returns the built-in default keyword set. Conservative
// choices to keep precision high; admins can extend via admin API.
//
// Coverage rationale:
//   - Reasoning: explicit math/logic verbs in zh + en
//   - Code: programming-related verbs and Chinese "写代码/实现" idioms
//   - Creative: writing/translation/summarisation idioms
func DefaultKeywords() KeywordSet {
	return KeywordSet{
		Reasoning: []string{
			// English
			"solve", "prove", "derive", "calculate", "compute",
			"reason", "reasoning", "logic", "theorem", "proof",
			"step by step", "explain why", "analyze",
			// 中文
			"证明", "推导", "求解", "计算", "推理", "逻辑",
			"分析", "证明题", "推导过程", "步骤",
		},
		Code: []string{
			// English
			"function", "class", "method", "algorithm", "implement",
			"code", "program", "script", "debug", "refactor",
			"compile", "syntax", "variable", "function",
			// 中文
			"代码", "函数", "方法", "实现", "编写", "写代码",
			"算法", "重构", "调试", "bug", "编程",
			// Code-fence marker (boosted separately by HasCodeBlock)
			"```",
		},
		Creative: []string{
			// English
			"write a", "write an", "compose", "draft", "story",
			"blog post", "essay", "poem", "creative",
			"translate", "summarize", "summary",
			// 中文
			"写一篇", "撰写", "创作", "故事", "小说", "诗歌",
			"翻译", "总结", "摘要", "文案",
		},
	}
}

// HeuristicClassifier implements Classifier using only signal extraction
// (no LLM call). Deterministic, zero-latency, zero-cost. Confidence is
// 0.7-0.95 for clear cases; 0.5-0.7 for ambiguous cases (triggers LLM
// fallback in Decider).
type HeuristicClassifier struct {
	thresholds HeuristicThresholds
	keywords   KeywordSet
}

// NewHeuristicClassifier constructs a classifier with the given thresholds
// and keyword set. Pass DefaultHeuristicThresholds() and DefaultKeywords()
// for built-in defaults; admin API can override at runtime.
func NewHeuristicClassifier(t HeuristicThresholds, k KeywordSet) *HeuristicClassifier {
	if t.LongContextTokens == 0 {
		t = DefaultHeuristicThresholds()
	}
	if len(k.Reasoning) == 0 && len(k.Code) == 0 && len(k.Creative) == 0 {
		k = DefaultKeywords()
	}
	return &HeuristicClassifier{thresholds: t, keywords: k}
}

// Classify implements Classifier.
//
// Algorithm (deterministic, pure function of inputs):
//
//  1. Hard overrides (highest priority):
//     - HasImages                → TaskVision
//     - EstimatedTokens > thresh → TaskLongContext
//
//  2. Tool-based dispatch:
//     - ToolCount >= AgentToolThreshold && HasToolResults → TaskAgent
//     - 1 <= ToolCount <= FunctionCallToolMax              → TaskFunctionCall
//
//  3. Keyword-based scoring (channels sum to 1.0):
//     - reasoning channel: keywords in Reasoning
//     - code channel: keywords in Code + HasCodeBlock boost
//     - creative channel: keywords in Creative
//
//  4. Pick highest-scoring channel. Ties broken by priority
//     (reasoning > code > creative > chat).
//
//  5. Confidence = top score (capped at 0.95).
func (c *HeuristicClassifier) Classify(_ context.Context, sigs ClassificationSignals) (*Classification, error) {
	// v2.0.3 audit fix #17: nil-keyword-slice protection. If the
	// caller passes nil for LastUserPrompt/SystemPrompt we still
	// produce a valid (chat) classification without panicking.
	scores := make(map[TaskType]float64, len(AllTaskTypes))

	// Channel 1: hard overrides — confidence 0.95 (very high)
	if sigs.HasImages {
		return &Classification{
			Primary:    TaskVision,
			Confidence: 0.95,
			Secondary:  []TaskScore{{Task: TaskVision, Score: 0.95}},
			Signals:    sigs,
			Classifier: "heuristic",
			Reason:     "request contains image parts (hard override)",
		}, nil
	}
	if sigs.EstimatedTokens > c.thresholds.LongContextTokens {
		conf := 0.90
		return &Classification{
			Primary:    TaskLongContext,
			Confidence: conf,
			Secondary:  []TaskScore{{Task: TaskLongContext, Score: conf}},
			Signals:    sigs,
			Classifier: "heuristic",
			Reason:     fmtTokens(sigs.EstimatedTokens, c.thresholds.LongContextTokens),
		}, nil
	}

	// Channel 2: tool-based dispatch
	if sigs.ToolCount >= c.thresholds.AgentToolThreshold && sigs.HasToolResults {
		conf := 0.85
		return &Classification{
			Primary:    TaskAgent,
			Confidence: conf,
			Secondary:  []TaskScore{{Task: TaskAgent, Score: conf}},
			Signals:    sigs,
			Classifier: "heuristic",
			Reason:     fmt.Sprintf("tool_count=%d (>= %d) + has_tool_results=true",
				sigs.ToolCount, c.thresholds.AgentToolThreshold),
		}, nil
	}
	if sigs.ToolCount >= 1 && sigs.ToolCount <= c.thresholds.FunctionCallToolMax {
		conf := 0.80
		scores[TaskFunctionCall] = conf
	}

	// Channel 3: keyword scoring
	text := normaliseForKeyword(sigs.LastUserPrompt, sigs.SystemPrompt)
	reasoningHits := countKeywordHits(text, c.keywords.Reasoning)
	codeHits := countKeywordHits(text, c.keywords.Code)
	creativeHits := countKeywordHits(text, c.keywords.Creative)

	// Per-keyword-hit weight bumped to 0.4 so a single strong hit
	// (e.g. "prove", "算法") decisively beats the chat baseline.
	const perHitWeight = 0.4
	if reasoningHits > 0 {
		scores[TaskReasoning] = min(1.0, float64(reasoningHits)*perHitWeight)
	}
	if codeHits > 0 || sigs.HasCodeBlock {
		boost := float64(codeHits) * perHitWeight
		if sigs.HasCodeBlock {
			boost += 0.3
		}
		scores[TaskCode] = min(1.0, boost)
	}
	if creativeHits > 0 {
		scores[TaskCreative] = min(1.0, float64(creativeHits)*perHitWeight)
	}

	// Default baseline for chat (so we always have a non-zero answer)
	// Lowered to 0.1 so a single keyword hit can beat it.
	scores[TaskChat] = 0.1

	// Pick winner (with priority tiebreak)
	winner, winnerScore := pickWinner(scores)
	// Build sorted secondary list
	secondary := rankSecondary(scores, winner)

	// Apply small bonus when a single channel strongly dominates —
	// bumps confidence above LLM threshold to avoid an unnecessary LLM call.
	if winnerScore >= 0.8 {
		winnerScore = min(0.95, winnerScore+0.05)
	}

	reason := buildReason(winner, reasoningHits, codeHits, creativeHits, sigs.HasCodeBlock)

	return &Classification{
		Primary:    winner,
		Confidence: winnerScore,
		Secondary:  secondary,
		Signals:    sigs,
		Classifier: "heuristic",
		Reason:     reason,
	}, nil
}

// Classifier is the interface implemented by both HeuristicClassifier
// and LLMFallbackClassifier. Decider calls Classify and decides whether
// to trust the result or escalate to the next classifier.
type Classifier interface {
	Classify(ctx context.Context, sigs ClassificationSignals) (*Classification, error)
	// Name returns a short identifier used in Classification.Classifier
	// and admin UI.
	Name() string
}

// Name implements Classifier.
func (c *HeuristicClassifier) Name() string { return "heuristic" }

// normaliseForKeyword lower-cases and concatenates the two text sources
// we scan for keywords. Concatenation is fine because keywords are
// single-token or short multi-word strings — no boundary issues.
//
// Performance guard: caps the concatenated length at maxScanTextBytes
// (default 32 KiB). Keyword scanning is O(text × keywords); a 1 MB
// system prompt would take ~100 ms. Capping at 32 KiB keeps the scan
// under 1 ms while still covering the entire user message and the head
// of the system prompt (where instructions typically live).
const maxScanTextBytes = 32 * 1024

func normaliseForKeyword(lastUser, system string) string {
	var b strings.Builder
	if system != "" {
		if len(system) > maxScanTextBytes/2 {
			system = system[:maxScanTextBytes/2] // truncate head — system instructions typically lead
		}
		b.WriteString(lowerASCII(system))
		b.WriteByte('\n')
	}
	if lastUser != "" {
		if len(lastUser) > maxScanTextBytes {
			lastUser = lastUser[:maxScanTextBytes]
		}
		b.WriteString(lowerASCII(lastUser))
	}
	return b.String()
}

// countKeywordHits returns the count of distinct keywords found in text.
// Performs case-insensitive substring match (both text and keyword are
// ASCII-lowercased per call). The text is assumed to already be
// lowercased by the caller (normaliseForKeyword does this for the
// request pipeline); the double-fold here makes the function safe
// when called directly in tests or admin tools.
func countKeywordHits(text string, kws []string) int {
	if len(kws) == 0 {
		return 0 // v2.0.3 audit fix #17: no keywords = no hits
	}
	count := 0
	lt := lowerASCII(text)
	for _, kw := range kws {
		if kw == "" {
			continue
		}
		if strings.Contains(lt, lowerASCII(kw)) {
			count++
		}
	}
	return count
}

// pickWinner returns the task type with the highest score, with a
// deterministic priority tiebreak (reasoning > code > agent > creative
// > function_call > vision > long_context > chat).
func pickWinner(scores map[TaskType]float64) (TaskType, float64) {
	priority := []TaskType{
		TaskReasoning, TaskCode, TaskAgent, TaskCreative,
		TaskFunctionCall, TaskVision, TaskLongContext, TaskChat,
	}
	var best TaskType
	bestScore := -1.0
	for _, t := range priority {
		if s, ok := scores[t]; ok && s > bestScore {
			bestScore = s
			best = t
		}
	}
	if best == "" {
		best = TaskChat
		bestScore = scores[TaskChat]
	}
	return best, bestScore
}

// rankSecondary returns the top-N alternative scores (excluding winner)
// sorted by descending score. Used for observability.
func rankSecondary(scores map[TaskType]float64, winner TaskType) []TaskScore {
	out := make([]TaskScore, 0, len(scores))
	for t, s := range scores {
		if t == winner {
			continue
		}
		out = append(out, TaskScore{Task: t, Score: s})
	}
	// Insertion sort — list is small (≤8).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Score > out[j-1].Score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func buildReason(winner TaskType, reasoningHits, codeHits, creativeHits int, hasCodeBlock bool) string {
	switch winner {
	case TaskReasoning:
		return fmtIntHits("reasoning", reasoningHits)
	case TaskCode:
		if hasCodeBlock && codeHits == 0 {
			return "code: fenced code block detected"
		}
		return fmtIntHits("code", codeHits)
	case TaskCreative:
		return fmtIntHits("creative", creativeHits)
	case TaskFunctionCall:
		return "1-2 tools declared (function_call)"
	default:
		return "default: no strong signal, falling back to chat"
	}
}

func fmtIntHits(channel string, hits int) string {
	return "keyword hits in " + channel + ": " + itoa(hits)
}

func fmtTokens(est, threshold int) string {
	return "estimated tokens (" + itoa(est) + ") > long_context threshold (" + itoa(threshold) + ")"
}

// itoa avoids importing strconv in this hot-path file (called per-request).
// Negative or zero values render as "0".
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// min returns the smaller of two float64 values. Inlined here to avoid
// pulling math.Min for a single use site.
func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}