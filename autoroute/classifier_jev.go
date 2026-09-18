package autoroute

// classifier_jev.go — experimental TypeSafe Jev ("System One") fallback
// classifier. 2026-09-18 Jev 调研轮的落地实验 provider。
//
// Jev is a decision model, not a chat LLM: POST one /v1/systemone call
// with a `choice` question over the task-type allowlist and get back a
// probability distribution + calibrated confidence in 70–500ms for a
// fraction of a cent. It occupies the SAME decider fallback slot as
// LLMFallbackClassifier (heuristic → low confidence → this), so the
// fail-open contract is inherited unchanged: Classify returning an error
// makes the decider keep the heuristic result (decision.go classify).
//
// Feature gate (all required, default OFF — zero behaviour change until
// a deployment opts in):
//
//	LLM_GATEWAY_JEV_CLASSIFIER  "1"/"true"/"on"/"enabled" to activate
//	TYPESAFE_API_KEY            bearer token (console.typesafe.ai)
//	TYPESAFE_BASE_URL           default "https://api.typesafe.ai"
//	LLM_GATEWAY_JEV_MODEL       default "jev-latest"
//	LLM_GATEWAY_JEV_TIMEOUT     seconds (float, default 3, clamped ≤10)
//
// Privacy envelope matches the LLM fallback exactly: bounded system/user
// text (512/1024 bytes) plus structural signals leave the process toward
// a third-party endpoint, tool-result contents are omitted. Deployments
// that must not ship prompt text off-host keep this gate off.
//
// Metrics reuse the llm_gateway_llm_classifier_* family with the same
// outcome vocabulary ("success"/"failure"/"timeout"/"breaker_open") —
// the family measures the fallback-classifier slot, whoever occupies it.
// The breaker gauges likewise report THIS slot's breaker when Jev wins.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// jevTaskTypeRubrics is the choice-criteria map: one rubric per task
// type, mirroring the buildClassificationPrompt allowlist wording so
// both fallback implementations grade the same contract.
func jevTaskTypeRubrics() map[string]string {
	return map[string]string{
		"chat":                  "ordinary conversation or Q&A",
		"reasoning":             "math, logic, multi-step analysis",
		"code":                  "code generation, debugging, refactoring",
		"agent":                 "multi-step workflow with several tools",
		"creative":              "writing, translation, summarisation",
		"long_context":          "very long document (>50k tokens)",
		"vision":                "request contains image input",
		"function_call":         "1-2 tool/function calls",
		"code_audit":            "code review, security, quality checks",
		"intent_classification": "intent, text, or sentiment classification",
		"planning":              "project plan, design, task breakdown, roadmap",
	}
}

const (
	jevDefaultBaseURL = "https://api.typesafe.ai"
	jevDefaultModel   = "jev-latest"
	jevDefaultTimeout = 3 * time.Second
	jevMaxTimeout     = 10 * time.Second

	// jevBreakerFailures / jevBreakerCooldown mirror CircuitBreakerCaller's
	// 5-failure / 30s posture: a flaky third-party endpoint must not turn
	// every low-confidence request into a timeout-shaped tax.
	jevBreakerFailures = 5
	jevBreakerCooldown = 30 * time.Second
)

// JevClassifier implements Classifier against the TypeSafe System One
// API (POST {base}/v1/systemone, model jev-latest by default).
//
// Thread-safe: the breaker state is mutex-guarded; the http.Client is
// safe for concurrent use.
type JevClassifier struct {
	endpoint string
	apiKey   string
	model    string
	timeout  time.Duration
	http     *http.Client

	mu          sync.Mutex
	consecutive int
	openUntil   time.Time
}

// BuildJevClassifierFromEnv assembles a JevClassifier from environment
// variables. Returns (nil, false) when the feature gate is off or the
// API key is missing — the caller then keeps the LLM fallback, which is
// the documented default. envLookup is injectable for tests.
func BuildJevClassifierFromEnv(envLookup func(string) string) (*JevClassifier, bool) {
	if envLookup == nil {
		envLookup = func(string) string { return "" }
	}
	switch strings.ToLower(strings.TrimSpace(envLookup("LLM_GATEWAY_JEV_CLASSIFIER"))) {
	case "1", "true", "yes", "on", "enabled":
	default:
		return nil, false
	}
	apiKey := strings.TrimSpace(envLookup("TYPESAFE_API_KEY"))
	if apiKey == "" {
		slog.Warn("autoroute: LLM_GATEWAY_JEV_CLASSIFIER enabled but TYPESAFE_API_KEY empty; keeping LLM fallback")
		return nil, false
	}

	base := strings.TrimRight(strings.TrimSpace(envLookup("TYPESAFE_BASE_URL")), "/")
	if base == "" {
		base = jevDefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		slog.Warn("autoroute: invalid TYPESAFE_BASE_URL; keeping LLM fallback", "base_url", base, "error", err)
		return nil, false
	}

	model := strings.TrimSpace(envLookup("LLM_GATEWAY_JEV_MODEL"))
	if model == "" {
		model = jevDefaultModel
	}
	timeout := jevDefaultTimeout
	if t := strings.TrimSpace(envLookup("LLM_GATEWAY_JEV_TIMEOUT")); t != "" {
		if secs, err := parseFloatSeconds(t); err == nil && secs > 0 {
			timeout = time.Duration(secs * float64(time.Second))
			if timeout > jevMaxTimeout {
				timeout = jevMaxTimeout
			}
		}
	}

	endpoint := base + "/v1/systemone"
	slog.Info("autoroute: Jev fallback classifier enabled",
		"endpoint", endpoint,
		"model", model,
		"timeout", timeout.String())
	return &JevClassifier{
		endpoint: endpoint,
		apiKey:   apiKey,
		model:    model,
		timeout:  timeout,
		http:     &http.Client{},
	}, true
}

// Name implements Classifier.
func (c *JevClassifier) Name() string { return "jev" }

// ── TypeSafe System One wire types ──────────────────────────────────

type jevQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type jevRequest struct {
	State     any                    `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]jevQuestion `json:"questions"`
}

type jevChoiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type jevResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]jevChoiceAnswer `json:"answers"`
}

// jevState is the `state` payload. Structured (not concatenated text) so
// Jev grades the signals as named fields, mirroring the LLM fallback's
// Signals line. Text stays bounded by the same limits as
// buildClassificationPrompt (llmFallbackSystemPromptLimit /
// llmFallbackUserPromptLimit).
type jevState struct {
	SystemPrompt    string `json:"system_prompt,omitempty"`
	UserPrompt      string `json:"user_prompt,omitempty"`
	MessageCount    int    `json:"message_count"`
	EstimatedTokens int    `json:"estimated_tokens"`
	ToolCount       int    `json:"tool_count"`
	HasToolResults  bool   `json:"has_tool_results"`
	HasImages       bool   `json:"has_images"`
	HasCodeBlock    bool   `json:"has_code_block"`
	ClientType      string `json:"client_type,omitempty"`
	Language        string `json:"language,omitempty"`
}

// Classify implements Classifier. One HTTP call, one choice question.
// Any transport/protocol failure returns an error — the decider's
// fail-open contract then keeps the heuristic result.
func (c *JevClassifier) Classify(ctx context.Context, sigs ClassificationSignals) (*Classification, error) {
	if !c.breakerAllow() {
		RecordLLMMetricCall("breaker_open", 0)
		RecordLLMCircuitBreakerState(jevBreakerFailures, true)
		return nil, ErrLLMCircuitOpen
	}

	start := time.Now()
	cls, err := c.classifyOnce(ctx, sigs)
	latency := time.Since(start)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			RecordLLMMetricCall("timeout", latency)
		} else {
			RecordLLMMetricCall("failure", latency)
		}
		c.breakerFailure()
		return nil, err
	}
	c.breakerSuccess()
	RecordLLMMetricCall("success", latency)
	return cls, nil
}

func (c *JevClassifier) classifyOnce(ctx context.Context, sigs ClassificationSignals) (*Classification, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	rubrics := jevTaskTypeRubrics()
	body, err := json.Marshal(jevRequest{
		State: jevState{
			SystemPrompt:    truncateLLMFallbackPromptText(sigs.SystemPrompt, llmFallbackSystemPromptLimit),
			UserPrompt:      truncateLLMFallbackPromptText(sigs.LastUserPrompt, llmFallbackUserPromptLimit),
			MessageCount:    sigs.MessageCount,
			EstimatedTokens: sigs.EstimatedTokens,
			ToolCount:       sigs.ToolCount,
			HasToolResults:  sigs.HasToolResults,
			HasImages:       sigs.HasImages,
			HasCodeBlock:    sigs.HasCodeBlock,
			ClientType:      sigs.ClientType,
			Language:        sigs.Language,
		},
		Model: c.model,
		Questions: map[string]jevQuestion{
			"task": {
				Type:         "choice",
				Instructions: "Which task type does this request belong to? Choose exactly one from the criteria options, using the structural signals as context.",
				Criteria:     rubrics,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("jev classify: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev classify: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev classify: http: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jev classify: unexpected status %d", resp.StatusCode)
	}

	var parsed jevResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("jev classify: decode: %w", err)
	}
	answer, ok := parsed.Answers["task"]
	if !ok || answer.Choice == "" {
		return nil, fmt.Errorf("jev classify: response missing 'task' answer")
	}
	primary, valid := normaliseLLMTaskType(answer.Choice)
	if !valid {
		return nil, fmt.Errorf("jev classify: unknown task type %q", answer.Choice)
	}

	confidence := answer.Confidence
	if confidence <= 0 || confidence > 1 {
		// Degraded but usable: fall back to the winner's own probability.
		confidence = answer.Probabilities[string(primary)]
	}
	if confidence <= 0 || confidence > 1 {
		confidence = 0.5
	}

	return &Classification{
		Primary:    primary,
		Confidence: confidence,
		Secondary:  jevSecondaryFromProbabilities(answer.Probabilities, primary),
		Signals:    sigs,
		Classifier: "jev",
		Reason: fmt.Sprintf("jev choice p=%.2f conf=%.2f (model %s)",
			answer.Probabilities[string(primary)], answer.Confidence, parsed.Model),
	}, nil
}

// jevSecondaryFromProbabilities converts the choice distribution into the
// Secondary ranking the Classification contract expects (top 3 after the
// winner, descending).
func jevSecondaryFromProbabilities(probs map[string]float64, winner TaskType) []TaskScore {
	scores := make([]TaskScore, 0, len(probs))
	for task, p := range probs {
		t := TaskType(strings.TrimSpace(strings.ToLower(task)))
		if !isValidTaskType(t) || t == winner {
			continue
		}
		scores = append(scores, TaskScore{Task: t, Score: p})
	}
	sort.SliceStable(scores, func(i, j int) bool { return scores[i].Score > scores[j].Score })
	if len(scores) > 3 {
		scores = scores[:3]
	}
	return scores
}

// ── Breaker (mirrors CircuitBreakerCaller semantics) ────────────────

func (c *JevClassifier) breakerAllow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().After(c.openUntil)
}

func (c *JevClassifier) breakerFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutive++
	if c.consecutive >= jevBreakerFailures {
		c.openUntil = time.Now().Add(jevBreakerCooldown)
		slog.Warn("autoroute: jev classifier breaker open",
			"consecutive_failures", c.consecutive,
			"cooldown", jevBreakerCooldown.String())
		RecordLLMCircuitBreakerState(c.consecutive, true)
	} else {
		RecordLLMCircuitBreakerState(c.consecutive, false)
	}
}

func (c *JevClassifier) breakerSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutive = 0
	c.openUntil = time.Time{}
}
