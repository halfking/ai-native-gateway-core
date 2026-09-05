package sessionmeta

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/clienttype"
	"github.com/kaixuan/llm-gateway-go/telemetry"
)

func extractAgent(in Input, system string) AgentIdentity {
	name := normalizeAgent(in.AgentName)
	source := "header"
	confidence := 1.0
	if name == "" || name == Unknown || isGenericAgent(name) {
		if detected := normalizeAgent(telemetry.DetectAgentFromSystemPrompt(system)); detected != "" {
			name, source, confidence = detected, "system_prompt", 0.9
		}
	}
	if name == "" {
		name = Unknown
		source, confidence = "unknown", 0
	}
	role := agentRole(name)
	typ := strings.ToLower(strings.TrimSpace(in.AgentType))
	if typ == "" {
		typ = Unknown
	}
	return AgentIdentity{Name: name, Type: typ, Role: role, Source: source, Confidence: confidence}
}

func extractClient(in Input, agent AgentIdentity) ClientIdentity {
	value := clienttype.Normalize(in.ClientType)
	source := "header"
	confidence := 1.0
	if strings.TrimSpace(in.ClientType) == "" || value == clienttype.Unknown {
		if mapped := clienttype.Normalize(agent.Name); mapped != clienttype.Unknown {
			value, source, confidence = mapped, "agent", 0.85
		} else {
			source, confidence = "unknown", 0
		}
	}
	return ClientIdentity{Type: value, Protocol: strings.TrimSpace(in.ClientProtocol), Source: source, Confidence: confidence}
}

func extractWorkTypes(explicit, text string) []Classification {
	if value := normalizeLabel(explicit); value != "" {
		return []Classification{{Value: value, Source: "header", Confidence: 1}}
	}
	lower := strings.ToLower(text)
	candidates := []struct {
		value string
		words []string
	}{
		{"debugging", []string{"debug", "bug", "error", "panic", "failure", "修复", "报错", "故障"}},
		{"refactoring", []string{"refactor", "refactoring", "重构", "整理代码"}},
		{"feature", []string{"implement", "add feature", "new feature", "新增", "实现功能"}},
		{"testing", []string{"test", "测试", "回归", "coverage", "覆盖率"}},
		{"deployment", []string{"deploy", "deployment", "发布", "上线", "部署"}},
		{"migration", []string{"migration", "migrate", "迁移", "schema"}},
		{"audit", []string{"audit", "审计", "security review", "安全审查"}},
		{"documentation", []string{"document", "docs", "文档", "方案"}},
		{"research", []string{"research", "调研", "开源项目", "对比"}},
	}
	var out []Classification
	for _, c := range candidates {
		if containsAny(lower, c.words) {
			out = append(out, Classification{Value: c.value, Source: "rule", Confidence: 0.75})
		}
		if len(out) == MaxWorkTypes {
			break
		}
	}
	if len(out) == 0 {
		out = []Classification{{Value: "unknown", Source: "unknown", Confidence: 0}}
	}
	return out
}

func extractProject(in Input, corpus string) (ProjectSignal, []Evidence) {
	if ref := strings.TrimSpace(in.ProjectRef); ref != "" {
		return ProjectSignal{Ref: ref, Label: cleanLabel(in.ProjectLabel), Source: "authoritative", Confidence: 1, Status: "confirmed"}, []Evidence{{Kind: "project_ref", Value: bounded(ref)}}
	}
	haystack := strings.ToLower(corpus)
	var hints []string
	for _, p := range in.RepoPaths {
		if v := normalizePathHint(p); v != "" {
			hints = append(hints, v)
		}
	}
	hints = append(hints, pathHints(haystack)...)
	hints = append(hints, remoteHints(haystack)...)
	hints = uniqueStrings(hints)
	if len(hints) == 0 {
		return ProjectSignal{Source: "unknown", Confidence: 0, Status: "pending"}, nil
	}
	label := projectLabel(hints[0])
	evidence := make([]Evidence, 0, min(len(hints), MaxProjectHints))
	for _, hint := range hints[:min(len(hints), MaxProjectHints)] {
		evidence = append(evidence, Evidence{Kind: "project_hint", Value: bounded(hint)})
	}
	return ProjectSignal{Label: label, Source: "rule", Confidence: 0.75, Status: "pending"}, evidence
}

var pathPattern = regexp.MustCompile("(?:^|[\\s\\\"'`()])((?:/|~/)[A-Za-z0-9._~+@%/-]{2,160})")
var remotePattern = regexp.MustCompile(`(?i)(?:https?://|git@)([^\s/:]+)[/:]([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)(?:\.git)?`)

func pathHints(text string) []string {
	matches := pathPattern.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			if v := normalizePathHint(m[1]); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func remoteHints(text string) []string {
	matches := remotePattern.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) == 3 {
			out = append(out, strings.ToLower(m[1]+"/"+m[2]))
		}
	}
	return out
}

func normalizePathHint(raw string) string {
	raw = strings.TrimSpace(strings.Trim(raw, "\"'`()[]{}.,;:"))
	if raw == "" {
		return ""
	}
	if u, err := url.PathUnescape(raw); err == nil {
		raw = u
	}
	if !strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "~/") {
		return ""
	}
	clean := path.Clean(raw)
	if clean == "." || clean == "/" || len(clean) < 3 {
		return ""
	}
	return bounded(clean)
}

func projectLabel(hint string) string {
	if strings.Contains(hint, "/") {
		if strings.HasPrefix(hint, "github.com/") || strings.HasPrefix(hint, "gitlab.com/") {
			parts := strings.Split(hint, "/")
			return strings.TrimSuffix(parts[len(parts)-1], ".git")
		}
		base := path.Base(hint)
		if base != "." && base != "/" {
			return base
		}
	}
	return hint
}

func provisionalTitle(user, agent string, types []Classification) string {
	text := cleanText(user)
	if text == "" {
		return ""
	}
	text = stripInstructionPrefix(text)
	if text == "" {
		return ""
	}
	if len([]rune(text)) > MaxTitleRunes {
		text = truncateRunes(text, MaxTitleRunes)
	}
	if agent != "" && agent != Unknown && startsWithAgent(text) == false && len([]rune(text)) < MaxTitleRunes-10 {
		return truncateRunes("["+agent+"] "+text, MaxTitleRunes)
	}
	_ = types
	return text
}

func stripInstructionPrefix(s string) string {
	for _, prefix := range []string{"user:", "用户：", "用户:", "最新请求：", "latest user message:"} {
		if strings.HasPrefix(strings.ToLower(s), strings.ToLower(prefix)) {
			return strings.TrimSpace(s[len(prefix):])
		}
	}
	return s
}

func startsWithAgent(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "[") }

func agentRole(name string) string {
	switch name {
	case "zcode", "opencode", "codex", "claude-code", "roocode", "cline", "aider", "continue", "cursor", "windsurf", "zed", "copilot", "kiro":
		return "coding_agent"
	case "vscode", "jetbrains":
		return "ide_client"
	case Unknown, "":
		return Unknown
	default:
		return "other_agent"
	}
}

func normalizeAgent(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	v = strings.Trim(v, "[]\"'")
	if v == "claude" {
		return "claude"
	}
	return clienttype.Normalize(v)
}

func isGenericAgent(v string) bool {
	return v == "go-client" || v == "python-client" || v == "curl" || v == "postman" || v == "insomnia"
}

func normalizeLabel(v string) string { return strings.ToLower(strings.Join(strings.Fields(v), "_")) }
func cleanLabel(v string) string     { return strings.TrimSpace(strings.Join(strings.Fields(v), " ")) }
func cleanText(v string) string      { return strings.Join(strings.Fields(strings.TrimSpace(v)), " ") }

func containsAny(text string, words []string) bool {
	for _, w := range words {
		if strings.Contains(text, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func bounded(v string) string { return truncateRunes(cleanText(v), 160) }

func truncateRunes(v string, n int) string {
	r := []rune(v)
	if len(r) <= n {
		return v
	}
	return string(r[:n-1]) + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func hashInput(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
