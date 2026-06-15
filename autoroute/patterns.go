package autoroute

// patterns.go — compiled regex pattern layer for the heuristic classifier.
//
// This layer sits between the tool-based dispatch (channel 2) and the
// keyword-scoring channel (channel 3) in the classification priority chain.
//
// Motivation: keyword substring matching misses requests that express a
// task type via *structural* patterns rather than explicit vocabulary.
// The canonical failure case is the "水池问题" — a Chinese word-problem
// that contains no reasoning keyword ("求解"/"推导") but is unmistakably
// a multi-step math task via the pattern "每(分钟|小时).*(多少|几)".
//
// All patterns are compiled once at package init for zero per-request cost.
// The pattern set is deliberately small and high-precision — false
// positives here are more costly than false negatives because a pattern
// match (weight 0.55-0.65) overrides the keyword layer entirely.

import (
	"fmt"
	"regexp"
)

// PatternMatch is one compiled regex with its task-type attribution.
type PatternMatch struct {
	// TaskType is the classification assigned when this pattern matches.
	TaskType TaskType

	// pattern is the compiled regex (never nil for a valid entry).
	pattern *regexp.Regexp

	// Weight is the confidence assigned on match (0.5-0.7).
	// Kept below the tool-dispatch band (0.80-0.85) and above the
	// single-keyword-hit band (0.40) so the layer composes correctly.
	Weight float64

	// Reason is the human-readable explanation surfaced in admin UI.
	Reason string
}

// MatchString reports whether the pattern matches anywhere in text.
func (p PatternMatch) MatchString(text string) bool {
	if p.pattern == nil || text == "" {
		return false
	}
	return p.pattern.MatchString(text)
}

// compiledPatterns is the package-level singleton, built once at init.
var compiledPatterns []PatternMatch

func init() {
	compiledPatterns = buildDefaultPatterns()
}

// buildDefaultPatterns compiles the curated regex set.
//
// Each pattern was chosen to cover a known misclassification gap
// discovered during multi-round testing (see model-routing-test-report).
// Patterns are case-insensitive; Chinese patterns are naturally
// case-insensitive (no ASCII folding needed).
func buildDefaultPatterns() []PatternMatch {
	type raw struct {
		expr   string
		task   TaskType
		weight float64
		reason string
	}
	specs := []raw{
		// ── Reasoning patterns ──────────────────────────────────────
		// Chinese math word-problem: "每分钟进水10升...需要多少分钟"
		// This is the exact pattern of the "水池问题" test failure.
		{
			expr:   `每(?:分钟|小时|天|秒).{0,40}(?:多少|几|需要多久|耗时)`,
			task:   TaskReasoning,
			weight: 0.65,
			reason: "pattern: chinese math word-problem (rate × time → quantity)",
		},
		// Multi-step conditional logic chain: "如果A那么B，如果B那么C"
		{
			expr:   `如果.{1,60}那么.{1,60}如果`,
			task:   TaskReasoning,
			weight: 0.60,
			reason: "pattern: multi-step conditional logic chain",
		},
		// Arithmetic expression containing operators: "2x + 5 = 13"
		{
			expr:   `\d+\s*[+\-*/]\s*\d+`,
			task:   TaskReasoning,
			weight: 0.55,
			reason: "pattern: arithmetic expression detected",
		},
		// Statistics / probability vocabulary (Chinese)
		{
			expr:   `排列|组合|概率|期望值|方差|标准差|正态分布|贝叶斯`,
			task:   TaskReasoning,
			weight: 0.60,
			reason: "pattern: statistics/probability terminology",
		},
		// Optimization phrasing: "求最值/最大/最小/最优"
		{
			expr:   `求(?:最大|最小|最优|最值)|最大化|最小化|最优解`,
			task:   TaskReasoning,
			weight: 0.60,
			reason: "pattern: optimization problem phrasing",
		},
		// ── Code patterns ───────────────────────────────────────────
		// Function/class/method definition syntax (multi-language)
		{
			expr:   `(?:def|func|fn|function|class|interface|struct|enum)\s+\w+`,
			task:   TaskCode,
			weight: 0.65,
			reason: "pattern: function/class/method definition syntax",
		},
		// Import statements (Python/JS/Go/Java)
		{
			expr:   `(?:import|from|require|include)\s+[\w."'/{ ]+`,
			task:   TaskCode,
			weight: 0.55,
			reason: "pattern: import/include statement",
		},
		// Variable declaration with type annotation
		{
			expr:   `(?:var|let|const|public|private|protected)\s+\w+\s*[:=]`,
			task:   TaskCode,
			weight: 0.55,
			reason: "pattern: typed variable declaration",
		},
		// ── Creative patterns ───────────────────────────────────────
		// "写一个/写一段/写首" without an explicit code/algorithm target
		// (the code keyword "写代码" already covers the code case)
		{
			expr:   `写(?:一个|一段|一首|一篇).{0,20}(?:故事|诗|歌词|散文|读后感|观后感)`,
			task:   TaskCreative,
			weight: 0.60,
			reason: "pattern: creative writing request (story/poem/lyrics)",
		},
	}

	out := make([]PatternMatch, 0, len(specs))
	for _, s := range specs {
		re, err := regexp.Compile(`(?i)` + s.expr)
		if err != nil {
			// A broken regex here is a programmer error, not a runtime
			// error. Log via panic so it surfaces during development
			// but never reaches production (CI catches it).
			panic(fmt.Sprintf("autoroute: invalid pattern %q: %v", s.expr, err))
		}
		out = append(out, PatternMatch{
			TaskType: s.task,
			pattern:  re,
			Weight:   s.weight,
			Reason:   s.reason,
		})
	}
	return out
}

// matchPatterns scans text against all compiled patterns and returns
// the first match for each task type (highest priority = first defined).
//
// Returns a map of task_type → PatternMatch for all types that matched.
// The caller (Classify) picks the winner from the map using the same
// priority tiebreak as keyword scoring.
//
// Performance: O(patterns × text_length). With ~9 patterns and a 32 KiB
// text cap, worst case is ~0.3 ms — well within the <1 ms budget.
func matchPatterns(text string) map[TaskType]PatternMatch {
	if len(text) == 0 {
		return nil
	}
	hits := make(map[TaskType]PatternMatch, 4)
	for _, p := range compiledPatterns {
		if p.MatchString(text) {
			// Keep only the first (highest-weight) match per task type,
			// since patterns are ordered by specificity within each type.
			if _, exists := hits[p.TaskType]; !exists {
				hits[p.TaskType] = p
			}
		}
	}
	return hits
}

// DefaultPatterns returns the compiled default pattern set. Exposed for
// admin introspection and testing.
func DefaultPatterns() []PatternMatch {
	out := make([]PatternMatch, len(compiledPatterns))
	copy(out, compiledPatterns)
	return out
}
