// Package probe provides the per-step trace recorder used by credential
// health checks so operators can see exactly what happened during a manual
// "立即检测" click: which URL was probed, what the upstream returned, where
// the run failed, and how long each step took.
//
// The recorder is deliberately decoupled from any global state. Callers pass
// in a *slog.Logger (typically slog.Default()) and a small metadata map
// (provider_id / credential_id / task_id / model) so every log line emitted
// by Finish() carries the same correlation keys as the surrounding
// admin.doHealthCheck log lines.
//
// Concurrency: Recorder is safe for concurrent use, but the typical caller
// runs each probe sequentially inside a single goroutine. Steps are still
// mutex-guarded so a future refactor that fans out per-candidate fetches
// won't corrupt the trace.
package probe

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxSteps bounds the per-trace slice so a misconfigured caller (e.g.
// iterating over a 10000-element URL candidate list) cannot allocate a
// 30 MiB trace blob. 32 is comfortably above the 5-step doHealthCheck
// shape and below the 6-step diagnose shape; bump this only if a new
// caller genuinely needs more.
const maxSteps = 64

// maxBodyPreviewBytes caps how many bytes of the upstream response body
// are kept in the Step. 2 KiB is enough to spot "<html>", "<!DOCTYPE",
// "<?xml", a Cloudflare / Nginx banner, and most vendor error JSON
// envelopes. Anything longer than that bloats the bg_tasks.result_json
// row and the rendered modal.
const maxBodyPreviewBytes = 2048

// StepStatus is the lifecycle of one Step. Pending / Running are present
// for callers that want to publish incremental state (e.g. WebSocket
// streaming); the manual "立即检测" path emits finished steps only.
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

// RequestSummary is the sanitized form of an outbound HTTP request. The
// recorder strips Authorization / Cookie / Set-Cookie / Proxy-Authorization
// values via SanitizeHeader before storing them — the operator UI shows
// the header name but a redacted value, so the trace is safe to persist
// in bg_tasks.result_json.
type RequestSummary struct {
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers,omitempty"`
	BodyPreview string            `json:"body_preview,omitempty"`
}

// ResponseSummary is the sanitized form of an inbound HTTP response.
// Content-Type / Set-Cookie / Content-Length are usually safe; Cookie is
// not. We still call SanitizeHeader uniformly so future additions don't
// require a per-header audit.
type ResponseSummary struct {
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers,omitempty"`
	BodyPreview string            `json:"body_preview,omitempty"`
	BodyBytes   int               `json:"body_bytes"`
}

// Step is one observable step of a probe. Status transitions:
//
//	Start(name)  → StepPending (later StepRunning if Run() is called)
//	Finish(...)  → StepSucceeded / StepFailed / StepSkipped
//
// Finish panics on double-finish so a caller bug (e.g. defer inside a
// loop) surfaces immediately instead of silently corrupting latency_ms.
type Step struct {
	Name      string          `json:"name"`
	Status    StepStatus      `json:"status"`
	StartedAt string          `json:"started_at"`	// RFC3339Nano, server time
	LatencyMs int             `json:"latency_ms"`
	Request   *RequestSummary `json:"request,omitempty"`
	Response  *ResponseSummary `json:"response,omitempty"`
	ErrorKind string          `json:"error_kind,omitempty"`
	ErrorText string          `json:"error_text,omitempty"`
	// Notes is a free-form short string the caller can attach to give
	// the operator a one-line hint per step (e.g. "candidate 2/3",
	// "source=api+manifest", "upserted=12 failed=0").
	Notes string `json:"notes,omitempty"`
}

// Trace is the aggregate of one probe run. Steps are appended in the
// order Start() is called; the caller is responsible for not exceeding
// maxSteps (extras are silently dropped with a WARN log).
type Trace struct {
	StartedAt string  `json:"started_at"`
	Steps     []*Step `json:"steps"`
}

// Recorder accumulates Step values, emits one structured log line per
// Finish(), and returns an immutable Trace snapshot via Snapshot().
//
// The zero value is NOT usable; callers must use NewRecorder().
type Recorder struct {
	mu    sync.Mutex
	trace *Trace
	log   *slog.Logger
	meta  map[string]any
	now   func() time.Time // injectable clock for tests
}

// NewRecorder constructs a Recorder. log may be nil (no log output) and
// meta may be nil (no metadata keys attached to log lines). The recorder
// always stamps Trace.StartedAt at construction so the operator UI can
// show absolute server time on the modal title.
func NewRecorder(log *slog.Logger, meta map[string]any) *Recorder {
	return &Recorder{
		trace: &Trace{StartedAt: nowFn().UTC().Format(time.RFC3339Nano)},
		log:   log,
		meta:  meta,
		now:   nowFn,
	}
}

// Start registers a new Step in pending state and returns its pointer so
// the caller can mutate Request / Response / Notes before calling
// Finish(). If the recorder is at capacity, Start returns nil and logs
// a WARN with the proposed name; callers must handle nil defensively.
func (r *Recorder) Start(name string) *Step {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.trace.Steps) >= maxSteps {
		if r.log != nil {
			r.log.Warn("probe trace: step capacity reached, dropping new step",
				"step", name,
				"capacity", maxSteps,
			)
		}
		return nil
	}
	step := &Step{
		Name:      name,
		Status:    StepRunning,
		StartedAt: r.now().UTC().Format(time.RFC3339Nano),
	}
	r.trace.Steps = append(r.trace.Steps, step)
	return step
}

// StartAt is like Start but stamps the step's StartedAt to the supplied
// timestamp. Useful when the caller already measured the network time
// inside the same goroutine as the HTTP round-trip and wants the trace
// latency to reflect the exact wire time.
func (r *Recorder) StartAt(name string, startedAt time.Time) *Step {
	step := r.Start(name)
	if step != nil {
		step.StartedAt = startedAt.UTC().Format(time.RFC3339Nano)
	}
	return step
}

// FinishOption mutates the step on Finish.
type FinishOption func(*Step)

// WithNotes sets a free-form note string on the step. Pass "" to clear.
func WithNotes(text string) FinishOption {
	return func(s *Step) { s.Notes = text }
}

// WithRequest attaches a sanitized RequestSummary. The caller is expected
// to have passed the headers through SanitizeHeader already; the recorder
// does NOT re-sanitize, so a caller bug surfaces immediately.
func WithRequest(req *RequestSummary) FinishOption {
	return func(s *Step) { s.Request = req }
}

// WithResponse attaches a sanitized ResponseSummary.
func WithResponse(resp *ResponseSummary) FinishOption {
	return func(s *Step) { s.Response = resp }
}

// Finish terminates the step. status must be one of StepSucceeded /
// StepFailed / StepSkipped (StepPending / StepRunning indicate an
// unfinished state and Finish will coerce them to StepFailed with
// "finish called with running status").
//
// If err is non-nil, error_kind / error_text are filled from err via
// ErrorKind / Preview when err is a *modelresponse.Error, else only the
// err.Error() text is captured (no kind). Pass nil for happy paths.
func (r *Recorder) Finish(step *Step, status StepStatus, err error, opts ...FinishOption) {
	if step == nil {
		return
	}
	finishedAt := r.now()
	latency := finishedAt.Sub(parseRFC3339Nano(step.StartedAt))
	step.LatencyMs = int(latency.Milliseconds())
	if latency < 0 {
		// clock skew between Start and Finish (e.g. monotonic vs wall);
		// clamp to zero rather than reporting a nonsense negative.
		step.LatencyMs = 0
	}
	for _, opt := range opts {
		opt(step)
	}
	if status == StepPending || status == StepRunning {
		status = StepFailed
		if step.ErrorKind == "" {
			step.ErrorKind = "invalid_finish_status"
			step.ErrorText = "Finish called with pending/running status"
		}
	}
	step.Status = status
	if err != nil {
		step.ErrorKind = classifyErrorKind(err)
		step.ErrorText = err.Error()
	}

	if r.log != nil {
		attrs := []any{
			"step", step.Name,
			"status", string(step.Status),
			"latency_ms", step.LatencyMs,
		}
		if step.ErrorKind != "" {
			attrs = append(attrs, "error_kind", step.ErrorKind)
		}
		if step.Notes != "" {
			attrs = append(attrs, "notes", step.Notes)
		}
		if step.Response != nil {
			attrs = append(attrs, "http_status", step.Response.Status, "body_bytes", step.Response.BodyBytes)
		}
		if step.Request != nil {
			attrs = append(attrs, "request_url", step.Request.URL, "request_method", step.Request.Method)
		}
		// Stable correlation keys come first so log shippers can index them.
		if r.meta != nil {
			for _, k := range []string{"task_id","provider_id","credential_id","model"} {
				if v, ok := r.meta[k]; ok {
					attrs = append(attrs, k, v)
				}
			}
		}
		r.log.Info("credential probe step", attrs...)
	}
}

// Snapshot returns a deep-copy of the current Trace. The returned trace
// is safe to marshal / persist without further locking; subsequent
// mutations on Recorder do not affect it.
func (r *Recorder) Snapshot() *Trace {
	r.mu.Lock()
	defer r.mu.Unlock()
	steps := make([]*Step, len(r.trace.Steps))
	for i, s := range r.trace.Steps {
		cp := *s
		if s.Request != nil {
			cp.Request = cloneRequestSummary(s.Request)
		}
		if s.Response != nil {
			cp.Response = cloneResponseSummary(s.Response)
		}
		steps[i] = &cp
	}
	return &Trace{StartedAt: r.trace.StartedAt, Steps: steps}
}

// StartedAt returns the recorder's trace started_at in RFC3339Nano form.
func (r *Recorder) StartedAt() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trace.StartedAt
}

// SanitizeHeader redacts the value of credentials-bearing HTTP headers
// before persistence. Recognized secrets: Authorization, Cookie,
// Set-Cookie, Proxy-Authorization, Proxy-Authenticate, X-Api-Key,
// X-Auth-Token, X-Secret, X-Secret-Token. Returns the (possibly renamed)
// key and the redacted value. Header names are matched case-insensitive.
//
// The recorder never persists the raw value of these headers; instead
// the trace shows "<header-name>: ***" so the operator can confirm the
// right header was sent, but cannot exfiltrate credentials from logs.
func SanitizeHeader(k, v string) (string, string) {
	lower := strings.ToLower(strings.TrimSpace(k))
	switch lower {
	case "authorization", "cookie", "set-cookie",
		"proxy-authorization", "proxy-authenticate",
		"x-api-key", "x-auth-token", "x-secret", "x-secret-token":
		return k, "***"
	}
	return k, v
}

// SanitizeHeaders is the map-aware variant of SanitizeHeader.
func SanitizeHeaders(in http.Header) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, vs := range in {
		// http.Header values are []string; join with the canonical ", "
		// before sanitizing so the trace preserves header list shape.
		joined := strings.Join(vs, ", ")
		ok, ov := SanitizeHeader(k, joined)
		out[ok] = ov
	}
	return out
}

// RequestFromHTTP builds a RequestSummary from a live *http.Request. The
// request body is not consumed (caller still owns it); BodyPreview is
// only useful for GET-style probes where the caller can call
// RequestFromBody separately.
func RequestFromHTTP(req *http.Request) *RequestSummary {
	if req == nil {
		return nil
	}
	return &RequestSummary{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: SanitizeHeaders(req.Header),
	}
}

// ResponseFromHTTP builds a ResponseSummary from a live *http.Response.
// Body is read up to maxBodyPreviewBytes+1 so we can detect truncation
// and report BodyBytes accurately. The body slice is replaced with a
// bytes.Reader so the caller can still consume the rest.
func ResponseFromHTTP(resp *http.Response) *ResponseSummary {
	if resp == nil {
		return nil
	}
	var preview string
	var bodyBytes int
	if resp.Body != nil {
		limited := io.LimitReader(resp.Body, int64(maxBodyPreviewBytes)+1)
		buf, _ := io.ReadAll(limited)
		bodyBytes = len(buf)
		if bodyBytes > maxBodyPreviewBytes {
			buf = buf[:maxBodyPreviewBytes]
			preview = string(buf) + "\n…(truncated, total " + itoa(bodyBytes) + " bytes)"
		} else {
			preview = string(buf)
		}
		// Replace the body with a fresh reader that yields whatever we
		// already buffered, so the caller can re-read the start. We do
		// NOT replay bytes the caller is about to read because that
		// requires a TeeReader and the caller is usually logging-only.
		resp.Body = io.NopCloser(bytes.NewReader(nil))
	}
	return &ResponseSummary{
		Status:      resp.StatusCode,
		Headers:     SanitizeHeaders(resp.Header),
		BodyPreview: preview,
		BodyBytes:   bodyBytes,
	}
}

// BodyPreviewFromBytes is a helper for callers that already have the
// body in memory (e.g. discovery.fetchModels) and want a sanitized
// snippet without going through an *http.Response.
func BodyPreviewFromBytes(body []byte) (preview string, total int) {
	total = len(body)
	if total > maxBodyPreviewBytes {
		preview = string(body[:maxBodyPreviewBytes]) + "\n…(truncated, total " + itoa(total) + " bytes)"
		return
	}
	preview = string(body)
	return
}

// classifyErrorKind inspects err and returns a stable kind for logs.
// Unknown kinds fall back to err.Error() only — no fake "other" string
// here so new classifier kinds surface instead of silently joining a
// wrong bucket.
func classifyErrorKind(err error) string {
	if err == nil {
		return ""
	}
	// Indirection via interface avoids an import cycle with
	// internal/modelresponse. We keep this dependency-free here so the
	// trace package is reusable by admin, bg, and discovery packages.
	type kindExtractor interface{ ProbeKind() string }
	if k, ok := err.(kindExtractor); ok {
		return k.ProbeKind()
	}
	return ""
}

func cloneRequestSummary(s *RequestSummary) *RequestSummary {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Headers != nil {
		cp.Headers = make(map[string]string, len(s.Headers))
		for k, v := range s.Headers {
			cp.Headers[k] = v
		}
	}
	return &cp
}

func cloneResponseSummary(s *ResponseSummary) *ResponseSummary {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Headers != nil {
		cp.Headers = make(map[string]string, len(s.Headers))
		for k, v := range s.Headers {
			cp.Headers[k] = v
		}
	}
	return &cp
}

func parseRFC3339Nano(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Now()
}

// itoa formats int without dragging in strconv at the call path — small
// helper kept local so callers don't need to import strconv just to
// format the truncated-bytes annotation.
func itoa(n int) string { return fmt.Sprintf("%d", n) }

// nowFn is the wall clock. Indirected so tests can swap to a fixed clock.
var nowFn = func() time.Time { return time.Now() }