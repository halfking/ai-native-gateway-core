package sessionforensics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	outputcompliancehooks "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	securityplugins "github.com/kaixuan/llm-gateway-go/domains/security/plugins"
)

// ScenarioName identifies an operational session analysis capability.
type ScenarioName string

const (
	ScenarioSummary          ScenarioName = "summary"
	ScenarioPanorama         ScenarioName = "panorama"
	ScenarioPromptInjection  ScenarioName = "prompt_injection"
	ScenarioOutputCompliance ScenarioName = "output_compliance"
	ScenarioHealth           ScenarioName = "health"
	ScenarioCluster          ScenarioName = "cluster"
)

var allScenarios = []ScenarioName{
	ScenarioSummary,
	ScenarioPanorama,
	ScenarioPromptInjection,
	ScenarioOutputCompliance,
	ScenarioHealth,
	ScenarioCluster,
}

// ScenarioFinding is a stable, redactable finding from one replay scenario.
// Evidence must contain metadata or hashes, never raw request or response content.
type ScenarioFinding struct {
	Turn     int            `json:"turn,omitempty"`
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Message  string         `json:"message"`
	Evidence map[string]any `json:"evidence,omitempty"`
}

// ScenarioResult is one scenario's outcome. A scenario can complete with
// findings; Error is reserved for a failed or unavailable scenario.
type ScenarioResult struct {
	Scenario ScenarioName      `json:"scenario"`
	Passed   bool              `json:"passed"`
	Findings []ScenarioFinding `json:"findings,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// ScenarioInput supplies a preserved session plus its compression replay.
type ScenarioInput struct {
	Pack   *SessionPack
	Replay *ReplayReport
}

// ScenarioAdapter lets existing domain modules participate without making the
// admin package depend on this package in the reverse direction.
type ScenarioAdapter interface {
	Scenario() ScenarioName
	Analyze(context.Context, ScenarioInput) (ScenarioResult, error)
}

type ScenarioAdapterFunc struct {
	Name ScenarioName
	Run  func(context.Context, ScenarioInput) (ScenarioResult, error)
}

func (f ScenarioAdapterFunc) Scenario() ScenarioName { return f.Name }

func (f ScenarioAdapterFunc) Analyze(ctx context.Context, input ScenarioInput) (ScenarioResult, error) {
	if f.Run == nil {
		return ScenarioResult{}, fmt.Errorf("sessionforensics: %s adapter has no runner", f.Name)
	}
	return f.Run(ctx, input)
}

// ScenarioReport collects all six operational checks for a replayed session.
type ScenarioReport struct {
	SessionID  string           `json:"session_id"`
	StartedAt  string           `json:"started_at"`
	FinishedAt string           `json:"finished_at"`
	Results    []ScenarioResult `json:"results"`
}

// ScenarioRunner runs registered adapters in a deterministic scenario order.
type ScenarioRunner struct {
	adapters map[ScenarioName]ScenarioAdapter
}

func NewScenarioRunner(adapters ...ScenarioAdapter) *ScenarioRunner {
	r := &ScenarioRunner{adapters: make(map[ScenarioName]ScenarioAdapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter != nil {
			r.adapters[adapter.Scenario()] = adapter
		}
	}
	return r
}

// Run records unavailable adapters as failed checks instead of hiding missing
// validation behind a successful partial report.
func (r *ScenarioRunner) Run(ctx context.Context, input ScenarioInput) *ScenarioReport {
	report := &ScenarioReport{StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if input.Pack != nil {
		report.SessionID = input.Pack.SessionMeta.ID
	}
	for _, name := range allScenarios {
		adapter := r.adapters[name]
		if adapter == nil {
			report.Results = append(report.Results, ScenarioResult{
				Scenario: name, Error: "adapter not configured",
			})
			continue
		}
		result, err := adapter.Analyze(ctx, input)
		result.Scenario = name
		if err != nil {
			result.Error = err.Error()
			result.Passed = false
		}
		report.Results = append(report.Results, result)
	}
	report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return report
}

// NewFallbackSummaryAdapter validates summary generation without an online LLM.
func NewFallbackSummaryAdapter(summarizer *Summarizer) ScenarioAdapter {
	return ScenarioAdapterFunc{Name: ScenarioSummary, Run: func(ctx context.Context, input ScenarioInput) (ScenarioResult, error) {
		if input.Pack == nil {
			return ScenarioResult{}, fmt.Errorf("sessionforensics: nil pack")
		}
		first := ""
		if len(input.Pack.Messages) > 0 {
			first = ExtractFirstUserMessage([]byte(input.Pack.Messages[0].Content))
		}
		result, err := summarizer.Summarize(ctx, input.Pack.SessionMeta.ID, SummarizeOptions{
			TenantID: input.Pack.SessionMeta.TenantID, FirstMessageOverride: first, ForceSource: "fallback",
		})
		if err != nil {
			return ScenarioResult{}, err
		}
		return ScenarioResult{Passed: result.Title != "" && result.Summary != "", Findings: []ScenarioFinding{{
			Code: "summary.generated", Severity: "info", Message: "summary generated", Evidence: map[string]any{"source": result.Source},
		}}}, nil
	}}
}

// NewPanoramaAdapter checks that a replay contains a complete turn-level view.
func NewPanoramaAdapter() ScenarioAdapter {
	return ScenarioAdapterFunc{Name: ScenarioPanorama, Run: func(_ context.Context, input ScenarioInput) (ScenarioResult, error) {
		if input.Pack == nil || input.Replay == nil {
			return ScenarioResult{}, fmt.Errorf("sessionforensics: pack and replay are required")
		}
		finding := ScenarioFinding{Code: "panorama.complete", Severity: "info", Message: "turn timeline replayed", Evidence: map[string]any{
			"exported_turns": len(input.Pack.Messages), "replayed_turns": len(input.Replay.Steps),
		}}
		return ScenarioResult{Passed: len(input.Pack.Messages) == len(input.Replay.Steps), Findings: []ScenarioFinding{finding}}, nil
	}}
}

// NewPromptInjectionAdapter applies the production prompt-injection checker to
// every exported request body without sending content to an external service.
func NewPromptInjectionAdapter() ScenarioAdapter {
	checker := securityplugins.NewPromptInjectionChecker()
	return ScenarioAdapterFunc{Name: ScenarioPromptInjection, Run: func(ctx context.Context, input ScenarioInput) (ScenarioResult, error) {
		if input.Pack == nil {
			return ScenarioResult{}, fmt.Errorf("sessionforensics: nil pack")
		}
		result := ScenarioResult{Passed: true}
		for _, message := range input.Pack.Messages {
			content := ExtractFirstUserMessage([]byte(message.Content))
			verdict, err := checker.Inspect(ctx, &domain.PipelineRequest{Metadata: map[string]any{"user_content": content}})
			if err != nil {
				return ScenarioResult{}, err
			}
			if !verdict.Allow {
				result.Passed = false
				result.Findings = append(result.Findings, ScenarioFinding{Turn: message.Turn, Code: verdict.Code, Severity: "critical", Message: verdict.Reason})
			}
		}
		return result, nil
	}}
}

// NewOutputComplianceAdapter evaluates exported non-streaming responses with
// the same checker contract used by the production post-upstream hook. Streaming
// turns without a captured response are recorded as unavailable evidence rather
// than incorrectly treated as clean output.
func NewOutputComplianceAdapter(checker outputcompliancehooks.Checker) ScenarioAdapter {
	return ScenarioAdapterFunc{Name: ScenarioOutputCompliance, Run: func(ctx context.Context, input ScenarioInput) (ScenarioResult, error) {
		if input.Pack == nil {
			return ScenarioResult{}, fmt.Errorf("sessionforensics: nil pack")
		}
		if checker == nil {
			return ScenarioResult{}, fmt.Errorf("sessionforensics: output compliance checker is not configured")
		}
		result := ScenarioResult{Passed: true}
		for _, message := range input.Pack.Messages {
			if message.ResponseContent == "" {
				result.Findings = append(result.Findings, ScenarioFinding{Turn: message.Turn, Code: "output.unavailable", Severity: "info", Message: "response body was not captured"})
				continue
			}
			check, err := checker.Check(ctx, input.Pack.SessionMeta.TenantID, message.ResponseContent)
			if err != nil {
				return ScenarioResult{}, err
			}
			if check == nil {
				return ScenarioResult{}, fmt.Errorf("sessionforensics: output compliance checker returned no result")
			}
			if check.Compliant && !check.Blocked && len(check.Issues) == 0 {
				continue
			}
			result.Passed = false
			severity := "warning"
			if check.Blocked {
				severity = "critical"
			}
			result.Findings = append(result.Findings, ScenarioFinding{Turn: message.Turn, Code: "output.compliance", Severity: severity, Message: "output compliance finding", Evidence: map[string]any{
				"blocked": check.Blocked, "issue_count": len(check.Issues), "redacted": check.RedactedOutput != "",
			}})
		}
		return result, nil
	}}
}

// ScenarioNames returns a copy for external test and reporting code.
func ScenarioNames() []ScenarioName {
	names := append([]ScenarioName(nil), allScenarios...)
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}
