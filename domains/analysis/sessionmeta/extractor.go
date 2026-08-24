package sessionmeta

import (
	"bytes"
	"encoding/json"
	"io"
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
	MaxSystemRunes    = 8192
	MaxInputBytes     = 1 << 20
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
	messages := normalizeMessages(in.Messages)
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

// ParseMessages accepts OpenAI Chat, Anthropic Messages, and OpenAI Responses
// request bodies. Invalid, oversized, or concatenated JSON returns no messages.
func ParseMessages(raw []byte) []Message {
	if len(raw) == 0 || len(raw) > MaxInputBytes {
		return nil
	}
	var probe map[string]json.RawMessage
	if err := decodeSingleJSON(raw, &probe); err != nil || probe == nil {
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

func decodeSingleJSON(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

func normalizeMessages(messages []Message) []Message {
	out := make([]Message, 0, min(len(messages), MaxMessages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "developer" {
			role = "system"
		}
		if !isKnownRole(role) {
			continue
		}
		content := cleanText(message.Content)
		if content == "" {
			continue
		}
		if role == "system" {
			content = truncateRunes(content, MaxSystemRunes)
		}
		out = append(out, Message{Role: role, Content: content})
	}
	return retainMessageWindow(out, "")
}

func retainMessageWindow(messages []Message, instruction string) []Message {
	instruction = truncateRunes(cleanText(instruction), MaxSystemRunes)
	if instruction != "" {
		messages = append([]Message{{Role: "system", Content: instruction}}, messages...)
	}
	if len(messages) <= MaxMessages {
		return messages
	}
	if len(messages) > 0 && messages[0].Role == "system" {
		return append([]Message{messages[0]}, messages[len(messages)-MaxMessages+1:]...)
	}
	return messages[len(messages)-MaxMessages:]
}

func parseOpenAIChat(raw json.RawMessage) []Message {
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := decodeSingleJSON(raw, &items); err != nil || len(items) == 0 {
		return nil
	}
	messages := make([]Message, 0, len(items))
	for _, item := range items {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role == "developer" {
			role = "system"
		}
		if !isKnownRole(role) {
			continue
		}
		content := contentText(item.Content)
		if content == "" {
			continue
		}
		messages = append(messages, Message{Role: role, Content: content})
	}
	return normalizeMessages(messages)
}

func parseAnthropicMessages(probe map[string]json.RawMessage) []Message {
	var system string
	if raw, ok := probe["system"]; ok {
		system = systemFromAnthropic(raw)
	}
	return retainMessageWindow(parseOpenAIChat(probe["messages"]), system)
}

func systemFromAnthropic(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := decodeSingleJSON(raw, &text); err == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := decodeSingleJSON(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" || block.Type == "" {
			if text := cleanText(block.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, " ")
}

func parseResponses(probe map[string]json.RawMessage) []Message {
	var instruction string
	if raw, ok := probe["instructions"]; ok && len(raw) > 0 && string(raw) != "null" {
		if err := decodeSingleJSON(raw, &instruction); err != nil {
			return nil
		}
		instruction = cleanText(instruction)
	}

	messages := make([]Message, 0, MaxMessages)
	if raw, ok := probe["input"]; ok && len(raw) > 0 && string(raw) != "null" {
		var text string
		if err := decodeSingleJSON(raw, &text); err == nil {
			if text = cleanText(text); text != "" {
				messages = append(messages, Message{Role: "user", Content: text})
			}
		} else {
			items, valid := parseResponsesInputArray(raw)
			if !valid {
				return nil
			}
			messages = append(messages, items...)
		}
	}
	return retainMessageWindow(normalizeMessages(messages), instruction)
}

func parseResponsesInputArray(raw json.RawMessage) ([]Message, bool) {
	var items []json.RawMessage
	if err := decodeSingleJSON(raw, &items); err != nil {
		return nil, false
	}
	messages := make([]Message, 0, len(items))
	for _, itemRaw := range items {
		var item struct {
			Role    string          `json:"role"`
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Content json.RawMessage `json:"content"`
		}
		if err := decodeSingleJSON(itemRaw, &item); err != nil {
			return nil, false
		}
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role == "developer" || role == "system" {
			role = "system"
		}
		if role == "" && (item.Type == "input_text" || item.Type == "text" || item.Type == "message") {
			role = "user"
		}
		if !isKnownRole(role) {
			continue
		}
		content := responsesContentText(item.Content)
		if content == "" {
			content = cleanText(item.Text)
		}
		if content == "" {
			continue
		}
		messages = append(messages, Message{Role: role, Content: content})
	}
	return messages, true
}

func responsesContentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := decodeSingleJSON(raw, &text); err == nil {
		return cleanText(text)
	}
	var block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := decodeSingleJSON(raw, &block); err == nil && (block.Type == "text" || block.Type == "input_text" || block.Type == "output_text" || block.Type == "") {
		return cleanText(block.Text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := decodeSingleJSON(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" || block.Type == "input_text" || block.Type == "output_text" || block.Type == "" {
			if text := cleanText(block.Text); text != "" {
				parts = append(parts, text)
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
