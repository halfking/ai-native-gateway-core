package clienttype

import (
	"net/http"
	"strings"
)

const Unknown = "unknown"

// HasToken reports whether the (already lowercased) haystack contains the
// token at a token boundary: the characters immediately before and after
// the match must NOT be lowercase letters or digits. This is the single
// shared matcher for UA tokens across the two UA tables (streaming
// extractClientType and telemetry ExtractAgentNameFromRequest).
//
// Why it exists (2026-10-29 identification round): plain strings.Contains
// over UA tokens over-matches lookalikes — "MiniMaxCoder/1.0" contains
// "minimaxcode", "ZCodex/2.0" contains "zcode" (and would shadow the codex
// branch), "visualized/2.0" contains "zed". Boundary-checked matching keeps
// every real wire form recognisable ("MiniMaxCode", "claude-cli/2.1.75",
// "ZCode/3.16.3 ai-sdk/…") while refusing alphanumeric continuations on
// either side. Case-insensitivity is the caller's job (both tables lowercase
// the UA first); hyphens and slashes are boundaries by construction.
func HasToken(haystack, token string) bool {
	if haystack == "" || token == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(haystack[i:], token)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(token)
		leadingOK := start == 0 || !isAlnumLower(haystack[start-1])
		trailingOK := end >= len(haystack) || !isAlnumLower(haystack[end])
		if leadingOK && trailingOK {
			return true
		}
		i = start + 1
	}
}

func isAlnumLower(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// Normalize bounds client-type values used in Redis holders and Prometheus labels.
//
// 2026-09-21 audit: added "minimax-code" and "deepseek-code" — the two
// domestic (CN) coding-agent clients that emit a recognisable body shape
// distinct from "zcode". The string is also the canonical value returned
// by telemetry.DetectAgentFromSystemPrompt and the body-level
// ExtractClientTypeFromBody so all three detection paths converge on the
// same Prometheus label cardinality.
func Normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "cursor", "claude-code", "opencode", "zcode", "codex", "roocode",
		"vscode", "copilot", "windsurf", "zed", "jetbrains", "pi",
		// 2026-09-21: domestic coding-agent clients
		"minimax-code", "deepseek-code",
		// 2026-10-29 domestic-vendor round: the remaining CN-vendor
		// coding tools. Names are the canonical kind spellings shared
		// with agent-companion's catalog (codebuddy/trae-agent→trae/
		// comate/qwen-code/kimi-cli/iflow; raccoon, iflycode, lingma are
		// identification-only there — no verified local CLI yet).
		"codebuddy", "trae", "comate", "qwen-code", "kimi-cli", "iflow",
		"raccoon", "iflycode", "lingma",
		Unknown:
		return value
	// 2026-10-29: alias spellings fold onto their canonical name, so a
	// header path (X-Agent-Name / X-Gw-Client-Type) that spells the
	// vendor's product alias keeps ONE label per tool instead of
	// expanding Prometheus cardinality. The UA tables already route the
	// same aliases; this is the header-supplied path.
	case "zulu":
		// Baidu Comate's CLI name.
		return "comate"
	case "code-buddy":
		return "codebuddy"
	case "trae-cli", "trae-agent":
		return "trae"
	case "kimi":
		return "kimi-cli"
	case "iflow-cli":
		return "iflow"
	case "qwen-cli":
		return "qwen-code"
	default:
		return Unknown
	}
}

// DetectFromRequest is the single implementation of the header/UA client
// identification chain (2026-10-29 identification round).
//
// Why it lives here: the chain was previously duplicated — streaming
// client_fingerprint.go (full table) and executors/executor.go (a
// "behaviour-consistent mirror" copy). The copies had already drifted once
// before this round (the wire-real "MiniMaxCode" form landed in streaming
// only), and the mirror contract ("两份实现必须保持行为一致") has no
// enforcement — drift is structural. Both call sites are now thin
// delegates to this function; adding a new vendor client touches exactly
// one table.
//
// Priority (unchanged from the streaming original):
//  1. X-Gw-Client-Type header (explicit, normalized)
//  2. User-Agent tokens (HasToken dual-boundary matching)
//  3. Anthropic-Beta "claude-code-" vendor flag (Claude Code's own
//     namespace; plain SDK clients never set it)
//
// Returns "" when nothing matches (caller decides its own fallback — the
// streaming chain falls through to body markers, the FpSlot holder path
// falls through to "unknown").
func DetectFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	// 1. 明确的客户端类型头（优先级最高）
	if ct := r.Header.Get("X-Gw-Client-Type"); ct != "" {
		return Normalize(ct)
	}
	// 2. User-Agent tokens。Match order matters (zcode before codex);
	//    HasToken makes "/", "-", space and end-of-string boundaries, so
	//    bare tokens suffice and lookalikes ("ZCodex", "MiniMaxCoder",
	//    "visualized") cannot match.
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	switch {
	case HasToken(ua, "cursor"):
		return "cursor"
	case HasToken(ua, "claude-code"):
		return "claude-code"
	// 2026-10-29 identification round (vendor-spec reconciliation): Claude
	// Code's real wire UA is "claude-cli/{ver}" (e.g. "claude-cli/2.1.75
	// (external, cli)") — the archive only documented the anthropic-beta
	// flag, and no table matched claude-cli, so the claude-code↔claude
	// native pair could never fire on the header/UA preferred path for a
	// real Claude Code client (RISK-7).
	case HasToken(ua, "claude-cli"):
		return "claude-code"
	case HasToken(ua, "opencode"):
		return "opencode"
	case HasToken(ua, "zcode"):
		return "zcode"
	case HasToken(ua, "codex"):
		return "codex"
	case HasToken(ua, "roocode"), HasToken(ua, "roo-code"):
		return "roocode"
	case HasToken(ua, "vscode"), HasToken(ua, "visual-studio-code"):
		return "vscode"
	case HasToken(ua, "github-copilot"), HasToken(ua, "copilot"):
		return "copilot"
	case HasToken(ua, "windsurf"):
		return "windsurf"
	case HasToken(ua, "zed"):
		return "zed"
	case HasToken(ua, "jetbrains"), HasToken(ua, "intellij"),
		HasToken(ua, "pycharm"), HasToken(ua, "webstorm"):
		return "jetbrains"
	// 2026-09-21 audit: domestic coding-agent clients.
	//   MiniMax Code (archive form, hyphenated) and the 2026-10-29 wire
	//   reality (bare "MiniMaxCode", 211 llm.itestu.cn) — both recognised,
	//   HasToken keeps "MiniMaxCoder/1.0" from false-positive.
	case HasToken(ua, "minimax-code"), HasToken(ua, "minimaxcode"):
		return "minimax-code"
		//   DeepSeek Code ships "DeepSeek-Code/{ver}" / "DeepSeek-IDE/{ver}" /
		//   "deepseek-cli/{ver}". Client side only — the gateway's outbound
		//   UA toward DeepSeek upstreams is governed by the upstream-dialect
		//   path, not by this detection.
	case HasToken(ua, "deepseek-code"), HasToken(ua, "deepseek-ide"),
		HasToken(ua, "deepseek-cli"):
		return "deepseek-code"
	// 2026-10-29 identification round: pi coding agent (pi.dev,
	// badlogic/pi-mono, npm @earendil-works/pi-coding-agent). Official
	// formatUserAgent() emits "pi/{ver} ({platform}; node/{v}; {arch})"
	// — 2,385 requests/day on 211 were falling through to "unknown"
	// because no table matched the bare "pi" product token. HasToken's
	// dual boundary keeps ordinary prose containing "pi" (e.g.
	// "raspberry-pi/1.0", "pip/24.0") from matching: "i" and "p" are
	// alnum, so only a slash/space/string-end delimiter fires.
	case HasToken(ua, "pi"):
		return "pi"
	// 2026-10-29 domestic-vendor round: the remaining CN-vendor coding
	// tools, wire forms first where they are known. HasToken's dual
	// boundary keeps the lookalikes out ("Trajectory/1.0" contains
	// "trae"-only as a substring of a longer word — alnum continuation
	// refuses it; "KimiK2/1.0" contains "kimi" followed by a digit and
	// is refused the same way).
	case HasToken(ua, "codebuddy"), HasToken(ua, "code-buddy"):
		return "codebuddy"
	case HasToken(ua, "trae-cli"), HasToken(ua, "trae-agent"), HasToken(ua, "trae"):
		return "trae"
	case HasToken(ua, "zulu"), HasToken(ua, "comate"):
		return "comate"
	case HasToken(ua, "qwen-code"), HasToken(ua, "qwen-cli"):
		return "qwen-code"
	case HasToken(ua, "kimi-cli"), HasToken(ua, "kimi"):
		return "kimi-cli"
	case HasToken(ua, "iflow-cli"), HasToken(ua, "iflow"):
		return "iflow"
	case HasToken(ua, "raccoon"):
		return "raccoon"
	case HasToken(ua, "iflycode"):
		return "iflycode"
	case HasToken(ua, "lingma"):
		return "lingma"
	}
	// 3. Vendor beta-flag header. Claude Code always carries
	//    "anthropic-beta: claude-code-20250219,…" — a match is deliberate
	//    self-identification rather than an SDK fingerprint. Checked AFTER
	//    the UA; both converge on "claude-code".
	if strings.Contains(strings.ToLower(r.Header.Get("Anthropic-Beta")), "claude-code-") {
		return "claude-code"
	}
	return ""
}
