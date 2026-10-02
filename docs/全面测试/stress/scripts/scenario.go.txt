// Package main is the scenario driver for the comprehensive test plan.
//
// It executes each scenario in tests/stress/scripts/scenarios.json against
// the running harness, asserts expected outcomes, and produces a JSON
// report at tests/stress/results/report.json.
//
// All provider / model identifiers are NON-REAL (mock-* / mock-stress-*).
//
// Usage:
//
//	go run ./tests/stress/scripts/scenario.go
//	go run ./tests/stress/scripts/scenario.go -only=s2_5xx_spike
//	go run ./tests/stress/scripts/scenario.go -gateway=http://127.0.0.1:18901 -results=./report.json
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// directTransport bypasses HTTP_PROXY/HTTPS_PROXY for localhost traffic.
// Tests target the local harness; routing through an upstream proxy (when
// set in the dev environment) only adds noise.
var directTransport = &http.Transport{
	Proxy: nil,
}

func directClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: directTransport,
	}
}

// ─────────────────────── types ───────────────────────

type scenario struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	PlanRef     string         `json:"plan_ref"`
	Setup       []providerStep `json:"setup"`
	Request     requestSpec    `json:"request"`
	Wait        string         `json:"wait"`
	Reset       bool           `json:"reset"`
	Recovery    *recoverySpec  `json:"recovery,omitempty"`
	Expect      expectSpec     `json:"expect"`
}

type recoverySpec struct {
	Wait    string         `json:"wait"`
	Setup   []providerStep `json:"setup"`
	Request requestSpec    `json:"request"`
}

type providerStep struct {
	Provider string `json:"provider"` // mock-alpha, etc.
	State    string `json:"state"`    // healthy|slow|server_error|...
	Payload  any    `json:"payload,omitempty"`
}

type requestSpec struct {
	Model       string `json:"model"`
	Stream      bool   `json:"stream"`
	Total       int    `json:"total"`
	Concurrency int    `json:"concurrency"`
	Prompt      string `json:"prompt"`
	MaxTokens   int    `json:"max_tokens"`
}

type expectSpec struct {
	MinSuccessRate        float64  `json:"min_success_rate"`
	MaxSuccessRate        *float64 `json:"max_success_rate"`
	MinTotalRequests      int      `json:"min_total_requests"`
	RequireStatus         []int    `json:"require_status,omitempty"`
	ForbiddenStatus       []int    `json:"forbidden_status,omitempty"`
	RequireDone           bool     `json:"require_done,omitempty"`
	MinDoneRate           float64  `json:"min_done_rate,omitempty"`
	MinDurationMs         int64    `json:"min_duration_ms,omitempty"`
	MaxDurationMs         int64    `json:"max_duration_ms,omitempty"`
	RequireProviders      []string `json:"require_providers,omitempty"`
	ForbiddenProviders    []string `json:"forbidden_providers,omitempty"`
	MinPromptChars        int      `json:"min_prompt_chars,omitempty"`
	AcceptableAny         bool     `json:"acceptable_any,omitempty"`
	// T-series stress acceptance: tokens-per-minute lower bound. When > 0
	// the scenario MUST sustain at least this many TPM (sum of usage.total_tokens
	// over elapsed wall time, scaled to 60s). 150_000 = 150 K TPM per the
	// 2026-09-23 stress-comprehensive acceptance contract.
	MinTPM                float64  `json:"min_tpm,omitempty"`
	// R55-F1b: §11.6 wire-error envelope contract — when all bound providers
	// emit a committed chunk then EOF without [DONE], the gateway keeps
	// HTTP 200 (committed bytes cannot be undone) but appends a structured
	// `data: {"error":{"type":"upstream_incomplete","code":"eof_without_done"}}`
	// envelope + records the request as failed. RequireErrorEnvelope forces
	// every response body to contain the envelope; MinErrorEnvelopeRate
	// sets a fractional floor (default 1.0 when RequireErrorEnvelope is on).
	RequireErrorEnvelope  bool     `json:"require_error_envelope,omitempty"`
	MinErrorEnvelopeRate  float64  `json:"min_error_envelope_rate,omitempty"`
	Note                  string   `json:"note,omitempty"`
}

type report struct {
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt time.Time        `json:"finished_at"`
	Gateway    string           `json:"gateway"`
	Scenarios  []scenarioResult `json:"scenarios"`
	Pass       int              `json:"pass"`
	Fail       int              `json:"fail"`
}

type scenarioResult struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	PlanRef           string         `json:"plan_ref"`
	StartedAt         string         `json:"started_at"`
	FinishedAt        string         `json:"finished_at"`
	DurationMs        int64          `json:"duration_ms"`
	TotalRequests     int            `json:"total_requests"`
	Success           int            `json:"success"`
	ClientError       int            `json:"client_error"`
	ServerError       int            `json:"server_error"`
	StatusCodes       map[string]int `json:"status_codes"`
	ByProvider        map[string]int `json:"by_provider"`
	AvgTTFBMs         float64        `json:"avg_ttfb_ms"`
	P50TTFBMs         float64        `json:"p50_ttfb_ms"`
	P95TTFBMs         float64        `json:"p95_ttfb_ms"`
	AvgTotalMs        float64        `json:"avg_total_ms"`
	P50TotalMs        float64        `json:"p50_total_ms"`
	P95TotalMs        float64        `json:"p95_total_ms"`
	SuccessRate       float64        `json:"success_rate"`
	DoneCount         int            `json:"done_count,omitempty"`
	DoneRate          float64        `json:"done_rate,omitempty"`
	ErrorEnvelopeCount int           `json:"error_envelope_count,omitempty"`
	ErrorEnvelopeRate float64        `json:"error_envelope_rate,omitempty"`
	PromptChars       int            `json:"prompt_chars,omitempty"`
	TotalTokens       int64          `json:"total_tokens,omitempty"`
	TPM               float64        `json:"tpm,omitempty"` // tokens per minute, derived
	Passed            bool           `json:"passed"`
	Expectation       string         `json:"expectation"`
	Note              string         `json:"note,omitempty"`
}

type sample struct {
	status            int
	ttfbMs            int64
	totalMs           int64
	bytes             int
	stream            bool
	sawDone           bool
	sawErrorEnvelope  bool // R55-F1b: §11.6 wire-error SSE envelope detected
	errorMsg          string
	bodyModel         string
	totalTokens       int // usage.total_tokens from non-stream mock reply
}

// ─────────────────────── main ───────────────────────

func main() {
	gateway := flag.String("gateway", "http://127.0.0.1:18901", "test gateway URL")
	mockBase := flag.String("mock-base", "http://127.0.0.1:18", "mock upstream base URL prefix; port = base + 1..4")
	resultsPath := flag.String("results", "tests/stress/results/report.json", "JSON report output")
	scenarioFilter := flag.String("only", "", "run only matching scenario id (substring)")
	scenarioFile := flag.String("scenarios", "tests/stress/scripts/scenarios.json", "scenarios JSON file")
	flag.Parse()

	scenarios, err := loadScenarios(*scenarioFile)
	if err != nil {
		log.Fatalf("load scenarios: %v", err)
	}

	rep := report{
		StartedAt: time.Now(),
		Gateway:   *gateway,
	}
	for _, sc := range scenarios {
		if *scenarioFilter != "" && !strings.Contains(sc.ID, *scenarioFilter) {
			continue
		}
		rep.Scenarios = append(rep.Scenarios, runOne(*gateway, *mockBase, sc))
	}
	rep.FinishedAt = time.Now()

	for _, r := range rep.Scenarios {
		if r.Passed {
			rep.Pass++
		} else {
			rep.Fail++
		}
	}

	if err := os.MkdirAll(parentDir(*resultsPath), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	if err := writeJSON(*resultsPath, rep); err != nil {
		log.Fatalf("write report: %v", err)
	}

	fmt.Println()
	fmt.Println("================ Stress-Test Report ================")
	fmt.Printf("Scenarios: %d   Pass: %s%d%s   Fail: %s%d%s\n",
		len(rep.Scenarios),
		greenIf(rep.Pass > 0), rep.Pass, ansiOff,
		redIf(rep.Fail > 0), rep.Fail, ansiOff,
	)
	fmt.Printf("Report: %s\n", *resultsPath)
	if rep.Fail > 0 {
		os.Exit(1)
	}
}

// ─────────────────────── runner ───────────────────────

func runOne(gateway, mockBase string, sc scenario) scenarioResult {
	res := scenarioResult{
		ID:          sc.ID,
		Name:        sc.Name,
		PlanRef:     sc.PlanRef,
		StatusCodes: map[string]int{},
		ByProvider:  map[string]int{},
		Expectation: sc.Expect.Note,
		StartedAt:   time.Now().Format(time.RFC3339Nano),
	}

	logBanner(sc)

	// 1. Reset gateway credential state if requested.
	if sc.Reset {
		resetGateway(gateway)
		log.Printf("  reset: gateway credentials → Active")
		// Brief settle so any in-flight requests from a previous
		// scenario finish draining before we change mock state.
		time.Sleep(200 * time.Millisecond)
		// Verify the reset actually took effect by reading /admin/stats.
		if stats := peekGatewayStats(gateway); stats != "" {
			log.Printf("  reset: stats %s", stats)
		}
	}

	// 2. Apply provider state setup steps.
	for _, step := range sc.Setup {
		if err := setProviderState(mockBase, step); err != nil {
			res.Passed = false
			res.Note = fmt.Sprintf("setup failed for %s: %v", step.Provider, err)
			res.FinishedAt = time.Now().Format(time.RFC3339Nano)
			return res
		}
	}

	// 3. Optional pre-wait (e.g., wait for windowed counters to populate).
	if sc.Wait != "" {
		if d, err := time.ParseDuration(sc.Wait); err == nil && d > 0 {
			time.Sleep(d)
		}
	}

	// 4. Send the burst.
	started := time.Now()
	samples := sendBurst(gateway, sc.Request)
	res.PromptChars = len(promptFor(sc.Request.Prompt))

	// 5. Optional recovery phase (s7): flip mocks healthy, wait, then
	// send a second burst that MUST include the previously-failed
	// provider. Without this, "recovery" is just "other providers
	// carried the load".
	if sc.Recovery != nil {
		for _, step := range sc.Recovery.Setup {
			if err := setProviderState(mockBase, step); err != nil {
				res.Passed = false
				res.Note = fmt.Sprintf("recovery setup failed for %s: %v", step.Provider, err)
				res.FinishedAt = time.Now().Format(time.RFC3339Nano)
				restoreHealthy(mockBase, sc.Setup)
				return res
			}
		}
		// Wait AFTER flipping mocks healthy so probeLoop (2s) can
		// promote Unhealthy → Degraded before the second burst.
		if sc.Recovery.Wait != "" {
			if d, err := time.ParseDuration(sc.Recovery.Wait); err == nil && d > 0 {
				time.Sleep(d)
			}
		}
		recReq := sc.Recovery.Request
		if recReq.Total == 0 {
			recReq = sc.Request
		}
		samples = append(samples, sendBurst(gateway, recReq)...)
	}
	res.DurationMs = time.Since(started).Milliseconds()
	aggregateSamples(&res, samples)

	// 6. Assertions. Pointer so failure notes actually stick.
	res.Passed = assertExpectation(sc.Expect, &res)

	// 7. Restore healthy state so subsequent scenarios start clean.
	restoreHealthy(mockBase, sc.Setup)

	res.FinishedAt = time.Now().Format(time.RFC3339Nano)

	prettyScenario(res)
	return res
}

func aggregateSamples(res *scenarioResult, samples []sample) {
	var ttfb, total []int64
	var tokens int64
	for _, s := range samples {
		res.TotalRequests++
		key := fmt.Sprintf("%d", s.status)
		res.StatusCodes[key]++
		if s.sawDone {
			res.DoneCount++
		}
		success := s.status >= 200 && s.status < 300
		if s.stream {
			success = success && s.sawDone
		}
		if success {
			res.Success++
		} else if s.status >= 500 || s.status == 0 {
			res.ServerError++
		} else {
			res.ClientError++
		}
		prov := providerFromBody(s.bodyModel)
		if prov != "" {
			res.ByProvider[prov]++
		}
		if s.sawErrorEnvelope {
			res.ErrorEnvelopeCount++
		}
		tokens += int64(s.totalTokens)
		ttfb = append(ttfb, s.ttfbMs)
		total = append(total, s.totalMs)
	}
	if res.TotalRequests > 0 {
		res.SuccessRate = float64(res.Success) / float64(res.TotalRequests)
		res.DoneRate = float64(res.DoneCount) / float64(res.TotalRequests)
		res.ErrorEnvelopeRate = float64(res.ErrorEnvelopeCount) / float64(res.TotalRequests)
	}
	res.TotalTokens = tokens
	if res.DurationMs > 0 && tokens > 0 {
		// TPM = (tokens / ms) * 60_000
		res.TPM = float64(tokens) * 60000.0 / float64(res.DurationMs)
	}
	res.AvgTTFBMs = avg(ttfb)
	res.P50TTFBMs = percentile(ttfb, 0.50)
	res.P95TTFBMs = percentile(ttfb, 0.95)
	res.AvgTotalMs = avg(total)
	res.P50TotalMs = percentile(total, 0.50)
	res.P95TotalMs = percentile(total, 0.95)
}

func sendBurst(gateway string, req requestSpec) []sample {
	if req.Concurrency < 1 {
		req.Concurrency = 1
	}
	if req.Total < req.Concurrency {
		req.Total = req.Concurrency
	}

	jobs := make(chan int, req.Total)
	var wg sync.WaitGroup
	results := make(chan sample, req.Total)

	client := directClient(60 * time.Second)

	for w := 0; w < req.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				results <- sendOne(client, gateway, req)
			}
		}()
	}

	for i := 0; i < req.Total; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(results)

	var out []sample
	for s := range results {
		out = append(out, s)
	}
	return out
}

func sendOne(client *http.Client, gateway string, req requestSpec) sample {
	body, _ := json.Marshal(map[string]any{
		"model":      req.Model,
		"stream":     req.Stream,
		"messages":   []any{map[string]any{"role": "user", "content": promptFor(req.Prompt)}},
		"max_tokens": req.MaxTokens,
	})
	httpReq, _ := http.NewRequest(http.MethodPost,
		strings.TrimRight(gateway, "/")+"/v1/chat/completions",
		bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	started := time.Now()
	var s sample
	if req.Stream {
		// Stream: parse SSE, measure TTFB.
		resp, err := client.Do(httpReq)
		s.totalMs = time.Since(started).Milliseconds()
		if err != nil {
			s.errorMsg = err.Error()
			s.status = 0
			return s
		}
		defer resp.Body.Close()
		s.status = resp.StatusCode
		s.stream = true
		buf := make([]byte, 4096)
		first := true
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if first {
					s.ttfbMs = time.Since(started).Milliseconds()
					first = false
				}
				s.bytes += n
				if bytes.Contains(buf[:n], []byte("data: [DONE]")) {
					s.sawDone = true
				}
				// R55-F1b: §11.6 wire-error envelope. The gateway appends
				// `data: {"error":{"type":"upstream_incomplete",...}}`
				// after a committed chunk + EOF without [DONE]. Match a
				// stable substring of the envelope shape; the gateway
				// emitter (domains/streaming/stream.go:1157) is the SSOT.
				if bytes.Contains(buf[:n], []byte(`"type":"upstream_incomplete"`)) ||
					bytes.Contains(buf[:n], []byte(`"code":"eof_without_done"`)) {
					s.sawErrorEnvelope = true
				}
				if s.bodyModel == "" {
					if idx := bytesIndex(buf[:n], []byte(`"id":"chatcmpl-mock-`)); idx >= 0 {
						rest := buf[idx+len(`"id":"chatcmpl-mock-`):]
						end := bytesIndex(rest, []byte(`"`))
						if end > 0 {
							prov := string(rest[:end])
							if dash := strings.Index(prov, "-"); dash > 0 {
								prov = prov[:dash]
							}
							s.bodyModel = "mock-" + prov
						}
					}
				}
			}
			if err != nil {
				if err == io.EOF {
					break
				}
				if s.errorMsg == "" {
					s.errorMsg = err.Error()
				}
				break
			}
		}
		return s
	}

	// Non-stream.
	resp, err := client.Do(httpReq)
	s.totalMs = time.Since(started).Milliseconds()
	if err != nil {
		s.errorMsg = err.Error()
		s.status = 0
		return s
	}
	defer resp.Body.Close()
	s.status = resp.StatusCode
	s.ttfbMs = s.totalMs
	bodyBytes, _ := io.ReadAll(resp.Body)
	s.bytes = len(bodyBytes)
	// Parse out "id" to attribute provider.
	s.bodyModel = providerFromID(bodyBytes)
	// Parse usage.total_tokens so TPM aggregates can validate ≥150K TPM.
	s.totalTokens = totalTokensFromUsage(bodyBytes)
	return s
}

// totalTokensFromUsage extracts usage.total_tokens from a non-stream chat
// completion response body. Returns 0 if the field is missing or malformed;
// the caller (aggregateSamples) treats 0 as "could not measure" rather than
// as an error.
func totalTokensFromUsage(body []byte) int {
	const key = `"total_tokens":`
	idx := bytesIndex(body, []byte(key))
	if idx < 0 {
		return 0
	}
	rest := body[idx+len(key):]
	// skip whitespace
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
		i++
	}
	// parse integer
	n := 0
	haveDigit := false
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		n = n*10 + int(rest[i]-'0')
		i++
		haveDigit = true
	}
	if !haveDigit {
		return 0
	}
	return n
}

func providerFromID(body []byte) string {
	idx := bytesIndex(body, []byte(`"id":"chatcmpl-mock-`))
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(`"id":"chatcmpl-mock-`):]
	end := bytesIndex(rest, []byte(`"`))
	if end <= 0 {
		return ""
	}
	prov := string(rest[:end])
	if dash := strings.Index(prov, "-"); dash > 0 {
		prov = prov[:dash]
	}
	return "mock-" + prov
}

func providerFromBody(s string) string {
	// Already extracted upstream; otherwise empty.
	if strings.HasPrefix(s, "mock-") {
		return s
	}
	return ""
}

// ─────────────────────── expectations ───────────────────────

func assertExpectation(e expectSpec, r *scenarioResult) bool {
	ok := true
	if e.MinTotalRequests > 0 && r.TotalRequests < e.MinTotalRequests {
		r.Note += " | expected >= " + itoa(e.MinTotalRequests) + " requests"
		ok = false
	}
	if e.MinSuccessRate > 0 && r.SuccessRate < e.MinSuccessRate {
		r.Note += " | success_rate " + ftoa(r.SuccessRate) + " < " + ftoa(e.MinSuccessRate)
		ok = false
	}
	if e.MaxSuccessRate != nil && r.SuccessRate > *e.MaxSuccessRate {
		r.Note += " | success_rate " + ftoa(r.SuccessRate) + " > " + ftoa(*e.MaxSuccessRate)
		ok = false
	}
	for _, want := range e.RequireStatus {
		if _, seen := r.StatusCodes[itoa(want)]; !seen && r.TotalRequests > 0 {
			r.Note += " | required status " + itoa(want) + " never seen"
			ok = false
		}
	}
	for _, forbidden := range e.ForbiddenStatus {
		if c, seen := r.StatusCodes[itoa(forbidden)]; seen && c > 0 {
			r.Note += " | forbidden status " + itoa(forbidden) + " seen " + itoa(c) + " times"
			ok = false
		}
	}
	if e.RequireDone && r.DoneRate < 0.99 {
		r.Note += " | require [DONE] but done_rate=" + ftoa(r.DoneRate)
		ok = false
	}
	if e.MinDoneRate > 0 && r.DoneRate < e.MinDoneRate {
		r.Note += " | done_rate " + ftoa(r.DoneRate) + " < " + ftoa(e.MinDoneRate)
		ok = false
	}
	// R55-F1b: §11.6 wire-error envelope contract — when all bound providers
	// commit-then-EOF, every response body must contain the structured
	// `data: {"error":{"type":"upstream_incomplete",...}}` envelope. A
	// missing envelope means the §11.6 fallback regressed and clients see a
	// silent HTTP 200 with truncated body.
	if e.RequireErrorEnvelope && r.ErrorEnvelopeRate < 1.0 {
		r.Note += " | require error_envelope but rate=" + ftoa(r.ErrorEnvelopeRate)
		ok = false
	}
	if e.MinErrorEnvelopeRate > 0 && r.ErrorEnvelopeRate < e.MinErrorEnvelopeRate {
		r.Note += " | error_envelope_rate " + ftoa(r.ErrorEnvelopeRate) + " < " + ftoa(e.MinErrorEnvelopeRate)
		ok = false
	}
	// T-series stress acceptance: ≥150K TPM contract. Computed only when
	// TotalTokens > 0 (i.e. the mock returned usage.total_tokens); zero
	// tokens means we cannot measure and we do not fail.
	if e.MinTPM > 0 && r.TotalTokens > 0 && r.TPM < e.MinTPM {
		r.Note += " | tpm " + ftoa(r.TPM) + " < " + ftoa(e.MinTPM)
		ok = false
	}
	if e.MinDurationMs > 0 && r.DurationMs < e.MinDurationMs {
		r.Note += " | duration_ms " + itoa(int(r.DurationMs)) + " < " + itoa(int(e.MinDurationMs)) + " (timeout path not exercised)"
		ok = false
	}
	if e.MaxDurationMs > 0 && r.DurationMs > e.MaxDurationMs {
		r.Note += " | duration_ms " + itoa(int(r.DurationMs)) + " > " + itoa(int(e.MaxDurationMs))
		ok = false
	}
	for _, want := range e.RequireProviders {
		if r.ByProvider[want] == 0 {
			r.Note += " | required provider " + want + " never seen (recovery/failover not proven)"
			ok = false
		}
	}
	for _, forbidden := range e.ForbiddenProviders {
		if r.ByProvider[forbidden] > 0 {
			r.Note += " | forbidden provider " + forbidden + " seen " + itoa(r.ByProvider[forbidden]) + " times"
			ok = false
		}
	}
	if e.MinPromptChars > 0 && r.PromptChars < e.MinPromptChars {
		r.Note += " | prompt_chars " + itoa(r.PromptChars) + " < " + itoa(e.MinPromptChars)
		ok = false
	}
	if !e.AcceptableAny && r.TotalRequests == 0 {
		r.Note += " | no requests recorded"
		ok = false
	}
	return ok
}

// ─────────────────────── helpers ───────────────────────

func setProviderState(mockBase string, step providerStep) error {
	port, ok := portForProvider(step.Provider)
	if !ok {
		return fmt.Errorf("unknown provider %s", step.Provider)
	}
	// mockBase may already have scheme prefix (e.g. "http://127.0.0.1:18"); strip it.
	hostOnly := strings.TrimPrefix(mockBase, "http://")
	hostOnly = strings.TrimPrefix(hostOnly, "https://")
	url := fmt.Sprintf("http://%s%d/admin/state", hostOnly, port)
	payload := map[string]any{"mode": step.State}
	if step.Payload != nil {
		// Merge additional payload fields (latency_min_ms, recover_in_seconds, ...).
		if m, ok := step.Payload.(map[string]any); ok {
			for k, v := range m {
				payload[k] = v
			}
		}
	}
	body, _ := json.Marshal(payload)
	client := directClient(10 * time.Second)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("state api %d: %s", resp.StatusCode, string(b))
	}
	log.Printf("  setup: %s → %s", step.Provider, step.State)
	return nil
}

func restoreHealthy(mockBase string, steps []providerStep) {
	for _, step := range steps {
		port, ok := portForProvider(step.Provider)
		if !ok {
			continue
		}
		hostOnly := strings.TrimPrefix(mockBase, "http://")
		hostOnly = strings.TrimPrefix(hostOnly, "https://")
		url := fmt.Sprintf("http://%s%d/admin/state", hostOnly, port)
		body, _ := json.Marshal(map[string]any{"mode": "healthy"})
		client := directClient(5 * time.Second)
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}
}

func resetGateway(gateway string) {
	client := directClient(5 * time.Second)
	req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(gateway, "/")+"/admin/reset",
		strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func peekGatewayStats(gateway string) string {
	client := directClient(3 * time.Second)
	resp, err := client.Get(strings.TrimRight(gateway, "/") + "/admin/stats")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var s struct {
		Credentials map[string]struct {
			Status      string `json:"status"`
			ConsecFails int64  `json:"consec_fails"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return ""
	}
	out := []string{}
	for id, cs := range s.Credentials {
		out = append(out, fmt.Sprintf("%s=%s(cf=%d)", id, cs.Status, cs.ConsecFails))
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func portForProvider(name string) (int, bool) {
	switch name {
	case "mock-alpha":
		return 101, true
	case "mock-beta":
		return 102, true
	case "mock-gamma":
		return 103, true
	case "mock-delta":
		return 104, true
	}
	return 0, false
}

func promptFor(size string) string {
	switch size {
	case "long":
		// ~24k chars. The previous Repeat("stress ", 200) was ~1.4k
		// and the report still claimed 24k — that was the lie.
		return strings.Repeat("stress-long-context-token ", 1000)
	case "medium":
		return strings.Repeat("stress ", 50)
	default:
		return "hi"
	}
}

func loadScenarios(path string) ([]scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []scenario
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeJSON(path string, v any) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	return os.WriteFile(path, b, 0o644)
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

// bytesIndex is a stdlib-free byte search (avoids importing bytes for one call).
func bytesIndex(b, sep []byte) int {
	if len(sep) == 0 {
		return 0
	}
	for i := 0; i+len(sep) <= len(b); i++ {
		match := true
		for j := 0; j < len(sep); j++ {
			if b[i+j] != sep[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func avg(xs []int64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := int64(0)
	for _, v := range xs {
		sum += v
	}
	return float64(sum) / float64(len(xs))
}

func percentile(xs []int64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]int64(nil), xs...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(p * float64(len(cp)-1))
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return float64(cp[idx])
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func ftoa(f float64) string {
	return fmt.Sprintf("%.3f", f)
}

func logBanner(sc scenario) {
	fmt.Println()
	fmt.Println("───────────────────────────────────────────────────────────")
	fmt.Printf("[scenario] %s — %s\n", sc.ID, sc.Name)
	fmt.Printf("           %s\n", sc.Description)
	if sc.PlanRef != "" {
		fmt.Printf("           plan ref: %s\n", sc.PlanRef)
	}
}

func prettyScenario(r scenarioResult) {
	status := ansiGreen + "PASS" + ansiOff
	if !r.Passed {
		status = ansiRed + "FAIL" + ansiOff
	}
	fmt.Printf("  → %s success=%d/%d (%.2f%%) ttfb_p50=%.0fms total_p95=%.0fms duration=%dms\n",
		status, r.Success, r.TotalRequests, r.SuccessRate*100,
		r.P50TTFBMs, r.P95TotalMs, r.DurationMs)
	fmt.Printf("    status_codes: %v\n", r.StatusCodes)
	fmt.Printf("    by_provider:  %v\n", r.ByProvider)
	if !r.Passed {
		fmt.Printf("    expectation:  %s\n", r.Expectation)
		if r.Note != "" {
			fmt.Printf("    note:         %s\n", r.Note)
		}
	}
}

const (
	ansiRed   = "\033[0;31m"
	ansiGreen = "\033[0;32m"
	ansiOff   = "\033[0m"
)

func greenIf(b bool) string {
	if b {
		return ansiGreen
	}
	return ""
}
func redIf(b bool) string {
	if b {
		return ansiRed
	}
	return ""
}

var _ = rand.Intn // keep math/rand available if future randomness added
