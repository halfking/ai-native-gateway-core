package sessionmeta

import (
	"encoding/json"
	"strings"
)

const (
	SchemaVersion     = "session-analysis/v1"
	AnalysisKind      = "session_metadata"
	StatusProvisional = "provisional"
	StatusFinal       = "final"
	Unknown           = "unknown"
	MaxTitleRunes     = 80
	MaxMessages       = 20
	MaxProjectHints   = 8
	MaxWorkTypes      = 3
)

// Input contains only request/session facts already available at the gateway
// boundary. The extractor does not perform I/O or invoke a model.
type Input struct {
	RequestBody    []byte
	Messages       []Message
	SystemPrompt   string
	UserText       string
	AgentName      string
	AgentType      string
	ClientType     string
	ClientProtocol string
	WorkType       string
	ProjectRef     string
	ProjectLabel   string
	TaskRef        string
	TaskLabel      string
	RepoPaths      []string
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Result struct {
	SchemaVersion string           `json:"schema_version"`
	AnalysisKind  string           `json:"analysis_kind"`
	Status        string           `json:"status"`
	Title         string           `json:"title,omitempty"`
	Agent         AgentIdentity    `json:"agent,omitempty"`
	Client        ClientIdentity   `json:"client,omitempty"`
	WorkTypes     []Classification `json:"work_types,omitempty"`
	Project       ProjectSignal    `json:"project,omitempty"`
	Features      []Feature        `json:"features,omitempty"`
	Evidence      []Evidence       `json:"evidence,omitempty"`
	Provenance    Provenance       `json:"provenance"`
	InputHash     string           `json:"input_hash,omitempty"`
}

type AgentIdentity struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Role       string  `json:"role"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
}

type ClientIdentity struct {
	Type       string  `json:"type"`
	Protocol   string  `json:"protocol,omitempty"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
}

type Classification struct {
	Value      string  `json:"value"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
}

type ProjectSignal struct {
	Ref        string  `json:"ref,omitempty"`
	Label      string  `json:"label,omitempty"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
	Status     string  `json:"status"`
}

type Feature struct {
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
}

type Evidence struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Provenance struct {
	Method          string `json:"method"`
	Extractor       string `json:"extractor"`
	PromptVersion   string `json:"prompt_version,omitempty"`
	GeneratedAtHint string `json:"generated_at_hint,omitempty"`
}

// Extract builds a bounded, deterministic provisional result. Explicit request
// facts always win over text heuristics; unresolved project signals stay
// pending and are never treated as billing/accounting identifiers.
func Extract(in Input) Result {
	messages := append([]Message(nil), in.Messages...)
	if len(messages) == 0 {
		messages = ParseMessages(in.RequestBody)
	}
	corpus, system, user := corpusParts(messages, in.SystemPrompt, in.UserText)
	if system == "" {
		system = firstSystem(messages)
	}
	if user == "" {
		user = lastUser(messages)
	}

	r := Result{
		SchemaVersion: SchemaVersion,
		AnalysisKind:  AnalysisKind,
		Status:        StatusProvisional,
		Provenance: Provenance{
			Method:    "rule",
			Extractor: SchemaVersion + "/rule",
		},
	}
	r.InputHash = hashInput(corpus)
	r.Agent = extractAgent(in, system)
	r.Client = extractClient(in, r.Agent)
	r.WorkTypes = extractWorkTypes(in.WorkType, user+"\n"+system)
	r.Project, r.Evidence = extractProject(in, corpus)
	r.Title = provisionalTitle(user, r.Agent.Name, r.WorkTypes)
	if r.Title != "" {
		// corpusParts/lastUser keep the LAST user message; the feature label
		// must describe what actually produced the title.
		r.Features = append(r.Features, Feature{Key: "title_source", Value: "last_user_message", Source: "rule", Confidence: 1})
	}
	return r
}

// ParseMessages accepts the three wire shapes used by the gateway and
// returns a flat, role-ordered slice suitable for the rule extractor:
//
//   - OpenAI chat:    {"messages":[{"role","content"}, ...]}
//   - Anthropic:      {"system":string|[{type,text}], "messages":[{"role","content"}, ...]}
//   - Responses API:  {"instructions":string, "input":string|[{role,content}, ...]}
//
// For Responses, input items use the {role,content} contract where content may
// be a string or a content-block with {type:"input_text",text:...}. The
// Anthropic top-level "system" is merged as the leading role=system message so
// downstream rules (firstSystem, lastUser) work unchanged across shapes.
// Invalid or oversized JSON returns an empty slice without panicking.
func ParseMessages(raw []byte) []Message {
	if len(raw) == 0 {
		return nil
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return nil
	}
	switch {
	case hasKey(probe, "system") && !hasKey(probe, "instructions") && !hasKey(probe, "input"):
		return parseAnthropicMessages(probe)
	case hasKey(probe, "messages"):
		return parseOpenAIChat(probe["messages"])
	case hasKey(probe, "instructions") || hasKey(probe, "input"):
		return parseResponses(probe)
	default:
		return nil
	}
}

func parseOpenAIChat(raw json.RawMessage) []Message {
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil
	}
	out := make([]Message, 0, min(len(items), MaxMessages))
	for _, m := range items {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if !isKnownRole(role) {
			continue
		}
		content := contentText(m.Content)
		if content == "" {
			continue
		}
		out = append(out, Message{Role: role, Content: content})
		if len(out) == MaxMessages {
			break
		}
	}
	return out
}

// parseAnthropicMessages mirrors the Anthropic /v1/messages wire shape:
// top-level "system" is either a plain string or an array of content blocks
// (type=text). The "messages" array uses the same {role,content} contract as
// OpenAI chat (string or content-block array). The system field, when present,
// is prepended so firstSystem() resolves it before any user-supplied system
// turn.
func parseAnthropicMessages(probe map[string]json.RawMessage) []Message {
	var sys string
	if raw, ok := probe["system"]; ok {
		sys = systemFromAnthropic(raw)
	}
	chat := parseOpenAIChat(probe["messages"])
	if sys == "" {
		return chat
	}
	out := make([]Message, 0, len(chat)+1)
	out = append(out, Message{Role: "system", Content: sys})
	out = append(out, chat...)
	if len(out) > MaxMessages {
		out = out[:MaxMessages]
	}
	return out
}

func systemFromAnthropic(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return cleanText(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "" {
			if v := cleanText(b.Text); v != "" {
				parts = append(parts, v)
			}
		}
	}
	return strings.Join(parts, " ")
}

// parseResponses handles the OpenAI Responses API wire shape:
// top-level "instructions" is a plain string, "input" is either a string or
// an array of items with {role,content} where content is either a string or a
// {type:"input_text",text:...} block. We treat string-form input as a single
// user message so the heuristic extractor can still pick it up.
func parseResponses(probe map[string]json.RawMessage) []Message {
	var instr string
	if raw, ok := probe["instructions"]; ok && len(raw) > 0 {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			instr = cleanText(s)
		}
	}
	out := make([]Message, 0, MaxMessages)
	if instr != "" {
		out = append(out, Message{Role: "system", Content: instr})
	}
	if raw, ok := probe["input"]; ok && len(raw) > 0 {
		var asString string
		if json.Unmarshal(raw, &asString) == nil && asString != "" {
			out = append(out, Message{Role: "user", Content: cleanText(asString)})
		} else {
			out = append(out, parseResponsesInputArray(raw)...)
		}
	}
	if len(out) > MaxMessages {
		out = out[:MaxMessages]
	}
	return out
}

func parseResponsesInputArray(raw json.RawMessage) []Message {
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil
	}
	out := make([]Message, 0, min(len(items), MaxMessages))
	for _, m := range items {
		if len(out) >= MaxMessages {
			break
		}
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if !isKnownRole(role) {
			continue
		}
		content := responsesContentText(m.Content)
		if content == "" {
			continue
		}
		out = append(out, Message{Role: role, Content: content})
	}
	return out
}

// responsesContentText mirrors contentText() but additionally recognizes the
// Responses-specific input_text / output_text content-block variants. Pure
// strings still flow through the same path.
func responsesContentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return cleanText(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text", "input_text", "output_text", "":
			if v := cleanText(b.Text); v != "" {
				parts = append(parts, v)
			}
		}
	}
	return strings.Join(parts, " ")
}

func hasKey(m map[string]json.RawMessage, key string) bool {
	_, ok := m[key]
	return ok
}

func isKnownRole(role string) bool {
	switch role {
	case "system", "user", "assistant", "tool", "function":
		return true
	}
	return false
}
