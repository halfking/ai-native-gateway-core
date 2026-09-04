// cmd/autoclass-bench — benchmark an OpenAI-compatible LLM endpoint on the
// auto-route task-classification prompt.
//
// Gate for docs/auto-model-optimization: before any provider (free tier or
// low-cost direct) enters the low-confidence fallback chain, run this tool
// against a reviewed sample set and require the quality thresholds —
// macro-F1, label validity, and latency. Providers that pass accuracy but
// show high 429/5xx rates belong in shadow mode only.
//
// Usage:
//
//	go run ./cmd/autoclass-bench \
//	  -endpoint https://api.groq.com/openai/v1 \
//	  -api-key-env GROQ_API_KEY \
//	  -model llama-3.3-70b-versatile \
//	  -samples data/classification_samples.jsonl \
//	  -labels code,reasoning,chat,creative,long_context,vision,function_call,agent
//
// Samples file: one JSON object per line — {"text": "...", "label": "code"}.
// Privacy: the tool sends sample text to the endpoint you point it at and
// prints an aggregate report to stdout; it never writes samples or raw
// responses to disk.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxSampleChars = 4000 // bound per-request cost; classification needs the head of the prompt

type sample struct {
	Text  string `json:"text"`
	Label string `json:"label"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
}

func main() {
	var (
		endpoint    = flag.String("endpoint", "", "OpenAI-compatible base URL, e.g. https://api.groq.com/openai/v1")
		model       = flag.String("model", "", "model id to benchmark")
		apiKeyEnv   = flag.String("api-key-env", "", "env var holding the API key (omit for keyless endpoints)")
		samplesPath = flag.String("samples", "", "JSONL file with {\"text\":..., \"label\":...} per line")
		labelsFlag  = flag.String("labels", "", "comma-separated task labels (default: derived from samples)")
		conc        = flag.Int("c", 4, "concurrent in-flight requests")
		timeout     = flag.Duration("timeout", 20*time.Second, "per-request timeout")
		limit       = flag.Int("limit", 0, "evaluate at most N samples (0 = all)")
		maxTokens   = flag.Int("max-tokens", 512, "per-request max_tokens; reasoning models spend tokens on hidden reasoning before the label, so keep this well above 1 label")
	)
	flag.Parse()

	if *endpoint == "" || *model == "" || *samplesPath == "" {
		flag.Usage()
		os.Exit(2)
	}
	apiKey := ""
	if *apiKeyEnv != "" {
		apiKey = os.Getenv(*apiKeyEnv)
		if apiKey == "" {
			fmt.Fprintf(os.Stderr, "env %s is empty\n", *apiKeyEnv)
			os.Exit(2)
		}
	}

	samples, err := loadSamples(*samplesPath, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if len(samples) == 0 {
		fmt.Fprintln(os.Stderr, "no samples loaded")
		os.Exit(2)
	}

	labels := parseLabels(*labelsFlag)
	if len(labels) == 0 {
		labels = deriveLabels(samples)
	}
	rep := newReport(labels)

	start := time.Now()
	runWorkers(*conc, samples, func(s sample) {
		predicted, errKind, latencyMS := classify(*endpoint, apiKey, *model, labels, s.Text, *timeout, *maxTokens)
		rep.add(s.Label, predicted, errKind, latencyMS)
		fmt.Fprint(os.Stderr, ".")
	})
	fmt.Fprintf(os.Stderr, "\n")

	printReport(rep, *endpoint, *model, len(samples), time.Since(start))
}

func loadSamples(path string, limit int) ([]sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []sample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var s sample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if s.Text == "" || s.Label == "" {
			return nil, fmt.Errorf("%s:%d: text and label are required", path, lineNo)
		}
		if len(s.Text) > maxSampleChars {
			s.Text = s.Text[:maxSampleChars]
		}
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, sc.Err()
}

func parseLabels(flagVal string) []string {
	if flagVal == "" {
		return nil
	}
	parts := strings.Split(flagVal, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func deriveLabels(samples []sample) []string {
	seen := make(map[string]struct{})
	for _, s := range samples {
		seen[strings.ToLower(s.Label)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// classify sends one classification request and returns
// (predicted label, error kind, latency ms). errKind "" means the endpoint
// answered with a parseable completion (even if the label itself is invalid).
func classify(endpoint, apiKey, model string, labels []string, text string, timeout time.Duration, maxTokens int) (string, string, float64) {
	prompt := buildPrompt(labels, text)
	reqBody, _ := json.Marshal(chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: text},
		},
		Temperature: 0,
		MaxTokens:   maxTokens,
	})

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(endpoint, "/")+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", "request_build", 0
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	latencyMS := float64(time.Since(start).Milliseconds())
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", "timeout", latencyMS
		}
		return "", "network", latencyMS
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", "http_429", latencyMS
	case resp.StatusCode >= 500:
		return "", "http_5xx", latencyMS
	case resp.StatusCode >= 400:
		return "", fmt.Sprintf("http_%d", resp.StatusCode), latencyMS
	}

	var parsed chatResponse
	var content, reasoning string
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:")) {
		// Some gateway protocol-conversion paths answer a non-stream request
		// with an SSE body (HTTP 200). Reassemble the deltas instead of
		// failing the sample as unparseable.
		content, reasoning = parseSSE(body)
	} else if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", "parse", latencyMS
	} else {
		msg := parsed.Choices[0].Message
		content, reasoning = msg.Content, msg.ReasoningContent
	}
	// Primary answer is content; reasoning-style models (GLM-5.x, R1-class)
	// can return the label only after their hidden reasoning, or spend the
	// whole budget there — fall back to reasoning_content, stripping any
	// inline <think> blocks either field may carry.
	label, _ := extractLabel(stripThink(content), func(s string) bool {
		for _, l := range labels {
			if s == l {
				return true
			}
		}
		return false
	})
	if label == "" {
		label, _ = extractLabel(stripThink(reasoning), func(s string) bool {
			for _, l := range labels {
				if s == l {
					return true
				}
			}
			return false
		})
	}
	return label, "", latencyMS
}

// parseSSE reassembles an OpenAI-style "data: {...}" stream into the final
// (content, reasoning_content) pair. Non-JSON keep-alive lines and the
// terminal [DONE] sentinel are skipped.
func parseSSE(body []byte) (string, string) {
	var content, reasoning strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			content.WriteString(c.Delta.Content)
			reasoning.WriteString(c.Delta.ReasoningContent)
		}
	}
	return content.String(), reasoning.String()
}

// stripThink removes inline <think>...</think> reasoning blocks some models
// embed in content before the final answer.
func stripThink(s string) string {
	if i := strings.Index(s, "</think>"); i >= 0 {
		return strings.TrimSpace(s[i+len("</think>"):])
	}
	return s
}

// buildPrompt keeps the system side minimal and enumerates the label set;
// the model must answer with the bare label only.
func buildPrompt(labels []string, text string) string {
	return "Classify the user request into exactly ONE of these task types: " +
		strings.Join(labels, ", ") +
		". Answer with ONLY the label, nothing else. Request:\n\n" + text
}

// extractLabel tolerates the common answer shapes: a bare label, a quoted
// label, or a JSON object with a "label"/"task_type" field, optionally inside
// markdown fences. The bool reports whether the label is in the known set.
func extractLabel(content string, valid func(string) bool) (string, bool) {
	s := strings.TrimSpace(content)
	if s == "" {
		return "", false
	}
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	if strings.HasPrefix(s, "{") {
		var obj struct {
			Label    string `json:"label"`
			TaskType string `json:"task_type"`
		}
		if err := json.Unmarshal([]byte(s), &obj); err == nil {
			for _, cand := range []string{obj.Label, obj.TaskType} {
				cand = strings.Trim(strings.ToLower(strings.TrimSpace(cand)), "\"'")
				if cand != "" && valid(cand) {
					return cand, true
				}
			}
		}
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(strings.ToLower(strings.TrimSpace(s)), "\"'` .")
	if valid(s) {
		return s, true
	}
	// Reasoning-style answers often bury the label in the last few lines
	// ("3. **选择最佳类别：** `code`"). Scan the tail for an exact label
	// match after stripping markdown emphasis; anything that is not an
	// exact label stays invalid so the gate is not softened.
	lines := strings.Split(strings.TrimSpace(content), "\n")
	tail := lines
	if len(tail) > 10 {
		tail = tail[len(tail)-10:]
	}
	for i := len(tail) - 1; i >= 0; i-- {
		cand := stripMarkdownEmphasis(tail[i])
		cand = strings.Trim(strings.ToLower(strings.TrimSpace(cand)), "\"'` .*:：#-")
		if cand != "" && valid(cand) {
			return cand, true
		}
	}
	return s, valid(s)
}

// stripMarkdownEmphasis removes common markdown wrappers (**bold**, *em*,
// `code`, leading list markers) around an answer line.
func stripMarkdownEmphasis(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimPrefix(s, "*")
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "`", "")
	return s
}

func runWorkers(conc int, samples []sample, fn func(sample)) {
	if conc < 1 {
		conc = 1
	}
	jobs := make(chan sample)
	var wg sync.WaitGroup
	for i := 0; i < conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				fn(s)
			}
		}()
	}
	for _, s := range samples {
		jobs <- s
	}
	close(jobs)
	wg.Wait()
}

func printReport(rep *report, endpoint, model string, attempted int, elapsed time.Duration) {
	sortErrKinds := make([]string, 0, len(rep.errors))
	for k := range rep.errors {
		sortErrKinds = append(sortErrKinds, k)
	}
	sort.Strings(sortErrKinds)

	fmt.Printf("auto-classification benchmark\n")
	fmt.Printf("  endpoint : %s\n", endpoint)
	fmt.Printf("  model    : %s\n", model)
	fmt.Printf("  samples  : %d attempted, %d evaluated, %d failed\n", attempted, rep.total, attempted-rep.total)
	fmt.Printf("  wall time: %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("\n")
	fmt.Printf("  accuracy      : %.2f%%\n", rep.accuracy()*100)
	fmt.Printf("  macro-F1      : %.4f\n", rep.macroF1())
	fmt.Printf("  invalid label : %.2f%%\n", pct(rep.invalid, rep.total))
	fmt.Printf("  latency p50   : %.0f ms\n", rep.latencyPercentile(0.50))
	fmt.Printf("  latency p95   : %.0f ms\n", rep.latencyPercentile(0.95))
	fmt.Printf("\n  per-label F1:\n")
	for _, label := range rep.labels {
		if st, ok := rep.stats[label]; ok {
			fmt.Printf("    %-24s F1=%.4f  P=%.3f R=%.3f\n", label, st.f1(), st.precision(), st.recall())
		}
	}
	if len(sortErrKinds) > 0 {
		fmt.Printf("\n  errors:\n")
		for _, k := range sortErrKinds {
			fmt.Printf("    %-16s %d\n", k, rep.errors[k])
		}
	}
	if mis := rep.topMisclassifications(10); len(mis) > 0 {
		fmt.Printf("\n  top misclassifications (expected -> predicted xN):\n")
		for _, m := range mis {
			fmt.Printf("    %-24s -> %-24s x%d\n", m.Expected, m.Predicted, m.Count)
		}
	}
}

func pct(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}
