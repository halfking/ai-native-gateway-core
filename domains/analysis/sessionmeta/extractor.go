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
		r.Features = append(r.Features, Feature{Key: "title_source", Value: "first_user_message", Source: "rule", Confidence: 1})
	}
	return r
}

// ParseMessages accepts OpenAI-style messages and both string and content-block
// content. Invalid or oversized JSON returns an empty slice without panicking.
func ParseMessages(raw []byte) []Message {
	if len(raw) == 0 {
		return nil
	}
	var wire struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &wire) != nil || len(wire.Messages) == 0 {
		return nil
	}
	out := make([]Message, 0, min(len(wire.Messages), MaxMessages))
	for _, m := range wire.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "system" && role != "user" && role != "assistant" && role != "tool" && role != "function" {
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
