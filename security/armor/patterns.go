// Package armor — attack pattern library (B1-2).
//
// This file provides a curated set of prompt-injection attack patterns used
// by the first-layer fast-path detector. When a pattern matches, the caller
// (relay handler) can emit a DecisionBlock without calling the LLM judge —
// a ~0.01ms regex check vs a ~300ms LLM call.
//
// Design (see implementation-plan.md §B1-3, two-layer verdict):
//
//	Layer 1 (here): regex/string patterns → instant DecisionBlock (v1: log only)
//	Layer 2 (judge.go): LLM-as-judge score ≥ threshold → DecisionBlock (v1: log only)
//
// Coverage philosophy:
//   - English: the classic jailbreak corpus (DAN, "ignore previous", etc.)
//   - Chinese: independent corpus — DO NOT translate English verbatim,
//     because Chinese jailbreak phrasing differs (忽略上面的指令, 越狱模式, etc.)
//
// False-positive discipline: patterns are anchored to be SPECIFIC enough to
// avoid flagging benign requests. E.g. "ignore previous instructions" matches,
// but "previous" alone does not. The MatchSet reports WHICH patterns fired so
// operators can audit precision.
package armor

import (
	"regexp"
	"strings"
	"time"
)

// PatternClass buckets patterns for telemetry and tuning. The strings are
// stable audit tags — do NOT rename without a migration.
type PatternClass string

const (
	ClassOverride   PatternClass = "override"   // "ignore previous instructions"
	ClassPersona    PatternClass = "persona"    // "you are DAN", role-play jailbreak
	ClassEscape     PatternClass = "escape"     // encoding/base64/pig-latin obfuscation
	ClassLeak       PatternClass = "leak"       // "reveal your system prompt"
	ClassCapability PatternClass = "capability" // "enable developer mode"
)

// AttackPattern is one compiled rule.
type AttackPattern struct {
	ID    string // stable identifier, e.g. "override-en-001"
	Class PatternClass
	Re    *regexp.Regexp
	Note  string // human-readable, for audit logs
}

// Match is one pattern hit against an input.
type Match struct {
	PatternID string
	Class     PatternClass
	Snippet   string // ~40-char context around the hit (truncated, never the full prompt)
}

// MatchSet is the result of scanning one prompt.
type MatchSet struct {
	Hits    []Match
	Scanned time.Duration
}

// Any reports whether at least one pattern matched.
func (m MatchSet) Any() bool { return len(m.Hits) > 0 }

// Patterns is the compiled pattern set. It is built once at init from the
// raw corpus below. Use MatchPatterns (the package-level function) to scan.
var Patterns []*AttackPattern

func init() {
	for _, raw := range rawCorpus() {
		// Pre-compile; panic at init if a regex is malformed (caught in tests + CI).
		// All patterns are case-insensitive — attacks appear in mixed case.
		re := regexp.MustCompile("(?i)" + raw.re)
		Patterns = append(Patterns, &AttackPattern{
			ID: raw.id, Class: raw.class, Re: re, Note: raw.note,
		})
	}
}

type rawPattern struct {
	id    string
	class PatternClass
	re    string
	note  string
}

// rawCorpus is the source-of-truth pattern list. ADD HERE to extend coverage.
// Conventions:
//   - ID format: <class>-<lang>-<seq>, e.g. "override-en-001"
//   - Regex must be case-insensitive (we prepend (?i))
//   - Anchor on SPECIFIC multi-word phrases to limit false positives
//   - One pattern per line for reviewability
func rawCorpus() []rawPattern {
	return []rawPattern{
		// ── ClassOverride: instruction-override attempts ──────────────
		{id: "override-en-001", class: ClassOverride, note: "ignore previous instructions",
			re: `ignore (all )?(previous|prior|above) (instructions?|prompts?|rules?|directives?)`},
		{id: "override-en-002", class: ClassOverride, note: "disregard your instructions",
			re: `disregard (all|your|previous) (instructions?|rules?|guidelines?)`},
		{id: "override-en-003", class: ClassOverride, note: "forget your instructions",
			re: `forget (everything|all|your) (previous )?(instructions?|rules?|programming)`},
		{id: "override-en-004", class: ClassOverride, note: "new instructions override",
			re: `from now on[,;.]? you (must|will|are to|should)`},
		{id: "override-en-005", class: ClassOverride, note: "override system prompt",
			re: `override (your|the) (system )?prompt`},
		{id: "override-zh-001", class: ClassOverride, note: "忽略上面的指令",
			re: `忽略(上面|之前|先前|前面|所有)(的)?(指令|提示|规则|约束|限制)`},
		{id: "override-zh-002", class: ClassOverride, note: "无视你的设定",
			re: `无视(你的|所有|上面|前面)(设定|指令|规则|约束|限制)`},
		{id: "override-zh-003", class: ClassOverride, note: "忘记你的指令",
			re: `忘记(你的|所有|之前)(指令|设定|规则|约束|编程)`},
		{id: "override-zh-004", class: ClassOverride, note: "从现在开始你是",
			re: `从现在开始[,，。]?你(是|必须|要|将会)`},

		// ── ClassPersona: role-play / persona jailbreak (DAN-style) ───
		{id: "persona-en-001", class: ClassPersona, note: "DAN jailbreak",
			re: `(do anything now|you are dan|dan mode)`},
		{id: "persona-en-002", class: ClassPersona, note: "you are a different AI",
			re: `you are (now )?(a |an )?(developer|root|admin|hacker|unrestricted|free) (mode|ai|assistant|version)`},
		{id: "persona-en-003", class: ClassPersona, note: "act as / pretend",
			re: `(act as|pretend to be|role-?play as|simulate) (a |an )?(root|admin|unrestricted|jailbroken|hacker)`},
		{id: "persona-en-004", class: ClassPersona, note: "evil / immoral mode",
			re: `(evil|immoral|unethical|chaos) (mode|ai|assistant|version)`},
		{id: "persona-en-005", class: ClassPersona, note: "jailbreak literal",
			re: `jailbreak( mode)?`},
		{id: "persona-zh-001", class: ClassPersona, note: "越狱模式",
			re: `越狱(模式)?`},
		{id: "persona-zh-002", class: ClassPersona, note: "你现在是开发者模式",
			re: `你现在(是|进入|开启)?(开发者|开发者)?模式`},
		{id: "persona-zh-003", class: ClassPersona, note: "扮演一个不受限制的AI",
			re: `扮演(一个)?(不受限制|无限制|没有限制|不受约束)的?(AI|助手|智能体)`},
		{id: "persona-zh-004", class: ClassPersona, note: "假装你是",
			re: `假装你(是|拥有)(root|管理员|超级用户|不受限制)`},

		// ── ClassEscape: obfuscation / encoding attempts ──────────────
		{id: "escape-en-001", class: ClassEscape, note: "base64 obfuscation hint",
			re: `(decode|translate) (this|the following) base64`},
		{id: "escape-en-002", class: ClassEscape, note: "pig latin / rot13 request",
			re: `(respond|answer|decode) (in )?(pig latin|rot13|leetspeak|l33t)`},
		{id: "escape-en-003", class: ClassEscape, note: "split prompt across tokens",
			re: `(combine|concatenate|join) (these|the following) (parts|words|chunks)`},
		{id: "escape-en-004", class: ClassEscape, note: "reverse text decode",
			re: `read (this )?(backwards|in reverse|reversed)`},
		{id: "escape-zh-001", class: ClassEscape, note: "解码这段",
			re: `(解码|翻译|还原)(这|下面)(段)?(base64|编码|加密|密文)`},

		// ── ClassLeak: system-prompt exfiltration ─────────────────────
		{id: "leak-en-001", class: ClassLeak, note: "reveal system prompt",
			re: `(reveal|show|print|output|repeat|display) (your )?(system )?(prompt|instructions?|rules?|initial message)`},
		{id: "leak-en-002", class: ClassLeak, note: "what are your instructions",
			re: `what (are|were) (your|the) (original )?(instructions?|rules?|guidelines?)`},
		{id: "leak-en-003", class: ClassLeak, note: "repeat everything above",
			re: `repeat (everything|all) (above|that|you were given)`},
		{id: "leak-zh-001", class: ClassLeak, note: "显示你的系统提示",
			re: `(显示|输出|告诉我|重复)(你的)?(系统提示|初始指令|系统指令|提示词|设定)`},
		{id: "leak-zh-002", class: ClassLeak, note: "你的规则是什么",
			re: `你(的)?(规则|指令|设定|约束)(是什么|有哪些|具体是什么)`},

		// ── ClassCapability: capability-escalation requests ───────────
		{id: "capability-en-001", class: ClassCapability, note: "enable developer mode",
			re: `(enable|activate|turn on) (developer|god|admin|debug|root) mode`},
		{id: "capability-en-002", class: ClassCapability, note: "remove restrictions",
			re: `(remove|lift|disable) (your |the |all )?(restrictions?|limitations?|safety|filters?|guardrails?)`},
		{id: "capability-en-003", class: ClassCapability, note: "no restrictions",
			re: `without (any )?(restrictions?|limitations?|rules?|filters?)`},
		{id: "capability-en-004", class: ClassCapability, note: "unfiltered response",
			re: `(respond|answer|reply) (without|free from|un) filtered`},
		{id: "capability-zh-001", class: ClassCapability, note: "解除限制",
			re: `(解除|取消|移除|关闭|去掉)(你的|所有)?(限制|约束|安全设置|过滤|防护)`},
		{id: "capability-zh-002", class: ClassCapability, note: "无限制地回答",
			re: `(无限制|不受限制|没有限制)(地)?(回答|输出|生成)`},
		{id: "capability-zh-003", class: ClassCapability, note: "开启开发者模式",
			re: `(开启|启动|进入|打开)(开发者|上帝|管理员|调试)?模式`},
	}
}

// MatchPatterns scans prompt against the compiled corpus and returns all hits.
// The scan is O(n_patterns × prompt_length); for the current ~30-pattern set
// and a 4KB prompt this is well under 1ms (see TestMatchPatterns_Performance).
//
// The returned MatchSet.Snippet fields are truncated to ~40 chars to avoid
// logging the full prompt in audit (privacy — the prompt may contain PII).
func MatchPatterns(prompt string) MatchSet {
	start := time.Now()
	var hits []Match
	if strings.TrimSpace(prompt) == "" {
		return MatchSet{Scanned: time.Since(start)}
	}
	for _, p := range Patterns {
		loc := p.Re.FindStringIndex(prompt)
		if loc == nil {
			continue
		}
		hits = append(hits, Match{
			PatternID: p.ID,
			Class:     p.Class,
			Snippet:   snippetAround(prompt, loc[0], loc[1]),
		})
	}
	return MatchSet{Hits: hits, Scanned: time.Since(start)}
}

// snippetAround returns a ~40-char window centered on the match, for audit.
// We never return the full prompt here to keep audit logs PII-safe.
func snippetAround(s string, start, end int) string {
	const window = 20 // chars of context each side
	lo := start - window
	if lo < 0 {
		lo = 0
	}
	hi := end + window
	if hi > len(s) {
		hi = len(s)
	}
	snip := strings.ReplaceAll(s[lo:hi], "\n", " ")
	if len(snip) > 80 {
		snip = snip[:80]
	}
	return snip
}
