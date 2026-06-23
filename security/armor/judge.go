// Package armor implements security checks for LLM calls: prompt-injection
// detection (B1), sensitive-data protection (B2), and (future) hallucination
// guards. All checks share a single LLM-as-judge abstraction so that a cheap
// model (gpt-4o-mini / Claude Haiku / Gemini Flash) can score a prompt and
// return a normalized [0,1] verdict.
//
// Domain boundary (see docs/产品方案/2026-06-23-llmgw-domain-architecture-refactor.md):
//   - armor OWNS: judge scoring, prompt patterns, policy loading, decisions
//   - armor does NOT own: request relaying (gateway/relay), audit persistence
//     (observability/audit), authN/Z (platform/auth)
//
// v1 hard rule: Mode is ALWAYS "observe" in production. Even if a policy
// requests "enforce", the judge returns DecisionWarn (logged) rather than
// DecisionBlock (rejected). Enforce mode requires a separate PR + legal sign-off
// (see design_intent_test.go for the documented invariant).
package armor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Decision is the normalized verdict for a judged prompt.
//
// The ordering matters: callers MUST compare with >= (never ==) so that a
// stronger verdict always wins, and so that "observe" mode can downgrade a
// judge's DecisionBlock into DecisionWarn without losing the signal.
type Decision int

const (
	// DecisionSafe: the prompt passed all checks; no action.
	DecisionSafe Decision = iota
	// DecisionWarn: the prompt is suspicious; log + telemetry, but do NOT
	// reject. This is the only verdict v1 emits in observe mode.
	DecisionWarn
	// DecisionBlock: the prompt should be rejected. v1 only produces this in
	// enforce mode (off by default; requires legal sign-off per PRD §支柱3).
	DecisionBlock
)

// String returns a stable lowercase tag for audit/telemetry. NEVER change
// these strings: audit events and OTel attributes depend on them verbatim.
func (d Decision) String() string {
	switch d {
	case DecisionSafe:
		return "safe"
	case DecisionWarn:
		return "warn"
	case DecisionBlock:
		return "block"
	default:
		return "unknown"
	}
}

// MarshalText / UnmarshalText make Decision JSON-friendly so it can ride
// alongside audit.Event without a custom serializer.
func (d Decision) MarshalText() ([]byte, error) { return []byte(d.String()), nil }
func (d *Decision) UnmarshalText(b []byte) error {
	switch string(b) {
	case "safe":
		*d = DecisionSafe
	case "warn":
		*d = DecisionWarn
	case "block":
		*d = DecisionBlock
	default:
		return fmt.Errorf("armor: unknown decision %q", b)
	}
	return nil
}

// ScoreRequest is the input to Judge.Score. Fields are intentionally minimal:
// every field must be consumable by ANY judge implementation (mock, HTTP, or
// future sidecar) so the interface stays stable as backends change.
type ScoreRequest struct {
	// Prompt is the user-facing text under evaluation. Required.
	Prompt string
	// Context is optional surrounding context (system prompt, prior turns).
	// Judges MAY use it to reduce false positives (e.g. "ignore previous
	// instructions" inside a quoted system message is less alarming).
	Context string
	// Rubric tells the judge WHAT to score for. Different check types pass
	// different rubrics:
	//   - prompt_inject: "Does this prompt attempt to override instructions?"
	//   - pii: "Does this prompt contain personally identifiable information?"
	//   - hallucination: "Is this response making unfounded claims?"
	Rubric string
	// Threshold is the caller-supplied cutoff in [0,1]. A returned score
	// >= Threshold maps to DecisionWarn/Block (see decideByMode). Callers
	// usually load this from Policy.PerCheckThresholds, not hardcode it.
	Threshold float64
}

// ScoreResponse is the normalized output. Score is always clamped to [0,1]
// (defensive against LLMs that return 1.2 or -0.1).
type ScoreResponse struct {
	Score      float64  // clamped to [0,1]
	Decision   Decision // resolved by Threshold + Policy.Mode
	Reason     string   // human-readable explanation from the judge
	JudgeModel string   // model id that actually scored (for audit trail)
	LatencyMs  int      // wall-clock of the judge call
}

// Judge scores a single prompt. Implementations MUST be safe for concurrent
// use (relay calls into them from many goroutines).
//
// The contract for Score:
//   - Never panic; return an error instead.
//   - Never block indefinitely; respect ctx.
//   - Clamp Score to [0,1] before returning (callers should not need to
//     re-validate).
//   - If the backing LLM is unavailable, return a non-nil error; do NOT
//     silently return DecisionSafe (fail-open would let attacks through).
//     Fail-CLOSED for enforce mode is a policy decision at the caller layer,
//     not here.
type Judge interface {
	Score(ctx context.Context, req ScoreRequest) (ScoreResponse, error)
}

// noopJudge is the zero-value Judge: it returns DecisionSafe without doing
// any work. It exists so that callers can always hold a non-nil Judge (avoids
// nil-check noise at every call site) when armor is disabled per-tenant.
type noopJudge struct{}

func (noopJudge) Score(ctx context.Context, req ScoreRequest) (ScoreResponse, error) {
	return ScoreResponse{Decision: DecisionSafe, JudgeModel: "noop", Reason: "armor disabled"}, nil
}

// Noop returns a Judge that always returns DecisionSafe. Use it as the default
// for tenants whose Policy has no enabled checks.
func Noop() Judge { return noopJudge{} }

// mockJudge returns a fixed score so unit tests can drive decision logic
// without an LLM. The decision is still resolved from the score + mode by the
// caller (see resolveDecision), so tests cover the full path.
type mockJudge struct {
	score      float64
	judgeModel string
	err        error // if non-nil, Score returns this error
	latency    time.Duration
}

// NewMockJudge returns a Judge that always scores `score`. Use in tests only;
// production code should construct via NewHTTPJudge. The judgeModel is
// recorded in ScoreResponse.JudgeModel for audit fidelity.
func NewMockJudge(score float64, judgeModel string) Judge {
	return &mockJudge{score: clampScore(score), judgeModel: judgeModel}
}

// NewMockJudgeWithError returns a Judge whose Score always fails. Use to test
// error-handling paths (e.g. judge timeout must not crash the relay).
func NewMockJudgeWithError(err error) Judge { return &mockJudge{err: err} }

// NewMockJudgeWithLatency returns a Judge that sleeps for d before returning,
// for latency-sensitive tests.
func NewMockJudgeWithLatency(score float64, judgeModel string, d time.Duration) Judge {
	return &mockJudge{score: clampScore(score), judgeModel: judgeModel, latency: d}
}

func (m *mockJudge) Score(ctx context.Context, req ScoreRequest) (ScoreResponse, error) {
	if m.err != nil {
		return ScoreResponse{}, m.err
	}
	if m.latency > 0 {
		select {
		case <-time.After(m.latency):
		case <-ctx.Done():
			return ScoreResponse{}, ctx.Err()
		}
	}
	return ScoreResponse{
		Score:      m.score,
		Decision:   resolveDecision(m.score, req.Threshold, ModeObserve),
		Reason:     "mock judge",
		JudgeModel: firstNonEmpty(m.judgeModel, "mock"),
		LatencyMs:  int(m.latency / time.Millisecond),
	}, nil
}

// httpJudge calls an OpenAI-compatible /v1/chat/completions endpoint and
// parses a JSON {"score": <n>, "reason": "..."} reply into a ScoreResponse.
// It uses ONLY the standard library (net/http + encoding/json) — no SDK
// dependency — because every vendor already speaks OpenAI-compatible and we
// do not want a per-vendor SDK in this repo.
//
// The prompt template is versioned (PromptTemplateV1) so we can A/B test
// changes without losing the ability to reproduce old judgments from audit.
const PromptTemplateV1 = `You are a security judge. Evaluate the following prompt strictly.

Rubric: %s

Context (may be empty):
%s

Prompt:
%s

Reply with ONLY a JSON object: {"score": <0.0 to 1.0>, "reason": "<short>"}
A score of 1.0 means the prompt definitely violates the rubric.
Do not output anything else.`

const judgeJSONSchemaHint = `{"score": number, "reason": string}`

type httpJudge struct {
	baseURL    string        // e.g. "https://api.openai.com/v1"
	model      string        // e.g. "gpt-4o-mini"
	apiKey     string        // injected per-call by the caller via ctx? no — stored here, see NOTE
	httpClient *http.Client  // injectable for testing (httptest.Server)
	logger     *slog.Logger  // never logs apiKey (MUST NOT be logged)
	timeout    time.Duration // per-call timeout; defaults to 5s
	mu         sync.Mutex    // guards httpClient reuse, not state
}

// NOTE on apiKey: stored on the struct (not in context) because (a) it is
// per-judge, not per-request, and (b) the same judge instance is shared
// across many relay requests. The key MUST NOT be logged; see parseJudgeJSON.

// HTTPOptions configures an HTTP-backed Judge. Zero values are safe and use
// sane defaults; only BaseURL / Model / APIKey are required.
type HTTPOptions struct {
	BaseURL    string        // required, e.g. "https://api.openai.com/v1"
	Model      string        // required, e.g. "gpt-4o-mini"
	APIKey     string        // required; never logged
	Timeout    time.Duration // default 5s if zero
	HTTPClient *http.Client  // default new client with Timeout; inject httptest in tests
	Logger     *slog.Logger  // default slog.Default()
}

// NewHTTPJudge builds an OpenAI-compatible HTTP judge. Returns an error (not
// a panic) if required fields are missing — callers MUST handle it.
func NewHTTPJudge(opts HTTPOptions) (Judge, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, errors.New("armor: HTTPOptions.BaseURL is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, errors.New("armor: HTTPOptions.Model is required")
	}
	if opts.APIKey == "" {
		return nil, errors.New("armor: HTTPOptions.APIKey is required")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &httpJudge{
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		model:      opts.Model,
		apiKey:     opts.APIKey,
		httpClient: hc,
		logger:     logger,
		timeout:    timeout,
	}, nil
}

func (h *httpJudge) Score(ctx context.Context, req ScoreRequest) (ScoreResponse, error) {
	start := time.Now()
	if err := ctx.Err(); err != nil {
		return ScoreResponse{}, err
	}

	body, err := h.buildBody(req)
	if err != nil {
		return ScoreResponse{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, h.baseURL+"/chat/completions", body)
	if err != nil {
		return ScoreResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+h.apiKey)

	resp, err := h.httpClient.Do(httpReq)
	if err != nil {
		return ScoreResponse{}, fmt.Errorf("armor: judge HTTP call failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // 64KB cap; judge replies are tiny
	if err != nil {
		return ScoreResponse{}, fmt.Errorf("armor: judge read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		// Never log the body verbatim — it may echo the prompt back. Truncate.
		return ScoreResponse{}, fmt.Errorf("armor: judge HTTP %d (body %dB)", resp.StatusCode, len(raw))
	}

	score, reason, err := parseJudgeJSON(raw)
	if err != nil {
		return ScoreResponse{}, fmt.Errorf("armor: judge parse: %w", err)
	}

	latencyMs := int(time.Since(start) / time.Millisecond)
	return ScoreResponse{
		Score:      score,
		Decision:   resolveDecision(score, req.Threshold, ModeObserve),
		Reason:     reason,
		JudgeModel: h.model,
		LatencyMs:  latencyMs,
	}, nil
}

// resolveDecision maps a numeric score + threshold + mode to a Decision.
//
// The mode parameter exists so the SAME judge instance can be used in both
// observe and (future) enforce modes without re-constructing it. In observe
// mode (the only mode v1 ships), DecisionBlock is downgraded to DecisionWarn
// so callers can never accidentally block traffic. This is the v1 hard rule.
func resolveDecision(score, threshold float64, mode Mode) Decision {
	score = clampScore(score)
	if score < threshold {
		return DecisionSafe
	}
	if mode == ModeEnforce {
		return DecisionBlock
	}
	return DecisionWarn // observe (v1) always downgrades to warn
}

// clampScore forces any numeric value into [0,1]. Defends against LLMs that
// hallucinate "score": 1.5 or "-0.2".
func clampScore(s float64) float64 {
	switch {
	case s < 0:
		return 0
	case s > 1:
		return 1
	default:
		return s
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
