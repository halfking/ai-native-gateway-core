package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type EvalItem struct {
	SessionHash string  `json:"session_hash"`
	Expected    string  `json:"expected_label,omitempty"`
	Predicted   string  `json:"predicted_label,omitempty"`
	Title       string  `json:"title,omitempty"`
	Summary     string  `json:"summary,omitempty"`
	LatencyMS   float64 `json:"latency_ms"`
	Error       string  `json:"error,omitempty"`
}

type EvalResult struct {
	Model             string                 `json:"model"`
	Endpoint          string                 `json:"endpoint"`
	Mode              string                 `json:"mode"`
	SampleCount       int                    `json:"sample_count"`
	Completed         int                    `json:"completed"`
	Errors            int                    `json:"errors"`
	Accuracy          float64                `json:"accuracy,omitempty"`
	MacroF1           float64                `json:"macro_f1,omitempty"`
	LabelValidityRate float64                `json:"label_validity_rate,omitempty"`
	TitleFormatRate   float64                `json:"title_format_rate,omitempty"`
	P50LatencyMS      float64                `json:"p50_latency_ms"`
	P95LatencyMS      float64                `json:"p95_latency_ms"`
	P99LatencyMS      float64                `json:"p99_latency_ms"`
	Items             []EvalItem             `json:"items"`
	PerLabel          map[string]LabelMetric `json:"per_label,omitempty"`
	CreatedAt         string                 `json:"created_at"`
}

type LabelMetric struct {
	TP, FP, FN            int
	Precision, Recall, F1 float64
}

func evaluate(ctx context.Context, goldPath, endpoint, model, mode string, concurrency int, timeout time.Duration) (EvalResult, error) {
	gold, err := loadConsensusGold(goldPath)
	if err != nil {
		return EvalResult{}, err
	}
	client := NewOpenAICompatClient(endpoint, os.Getenv("MODEL_API_KEY"), timeout)
	out := EvalResult{Model: model, Endpoint: endpoint, Mode: mode, SampleCount: len(gold), PerLabel: map[string]LabelMetric{}, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	items := make([]EvalItem, len(gold))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	if concurrency < 1 {
		concurrency = 1
	}
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				item := runEvalItem(ctx, client, model, mode, gold[i])
				mu.Lock()
				items[i] = item
				mu.Unlock()
			}
		}()
	}
	for i := range gold {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	out.Items = items
	latencies := make([]float64, 0, len(items))
	valid := 0
	correct := 0
	for _, it := range items {
		if it.Error != "" {
			out.Errors++
			continue
		}
		out.Completed++
		latencies = append(latencies, it.LatencyMS)
		if it.Predicted != "" {
			valid++
		}
		if it.Expected != "" && it.Predicted == it.Expected {
			correct++
		}
	}
	if out.Completed > 0 {
		out.Accuracy = float64(correct) / float64(out.Completed)
		out.LabelValidityRate = float64(valid) / float64(out.Completed)
	}
	out.P50LatencyMS = percentile(latencies, .50)
	out.P95LatencyMS = percentile(latencies, .95)
	out.P99LatencyMS = percentile(latencies, .99)
	if mode == "label" || mode == "all" {
		out.PerLabel = computePerLabel(items)
		out.MacroF1 = macroF1(out.PerLabel)
	}
	if mode == "title" || mode == "all" {
		out.TitleFormatRate = titleFormatRate(items)
	}
	return out, nil
}

func loadConsensusGold(path string) ([]ConsensusRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []ConsensusRecord
	dec := json.NewDecoder(f)
	for dec.More() {
		var r ConsensusRecord
		if err := dec.Decode(&r); err != nil {
			return nil, err
		}
		if r.Gold != nil {
			out = append(out, r)
		}
	}
	return out, nil
}
func runEvalItem(ctx context.Context, c *OpenAICompatClient, model, mode string, r ConsensusRecord) EvalItem {
	item := EvalItem{SessionHash: r.SessionHash}
	if r.Gold != nil {
		item.Expected = r.Gold.Label
	}
	prompt := buildEvalPrompt(r, mode)
	content, lat, err := c.Complete(ctx, model, []ChatMessage{{Role: "system", Content: "Return JSON only."}, {Role: "user", Content: prompt}}, 400, 0)
	item.LatencyMS = lat
	if err != nil {
		item.Error = err.Error()
		return item
	}
	var v struct {
		Label   string `json:"label"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := decodeJSONContent(content, &v); err != nil {
		item.Error = err.Error()
		return item
	}
	item.Predicted = strings.ToLower(strings.TrimSpace(v.Label))
	item.Title = strings.TrimSpace(v.Title)
	item.Summary = strings.TrimSpace(v.Summary)
	return item
}
func buildEvalPrompt(r ConsensusRecord, mode string) string {
	return fmt.Sprintf("Mode=%s. Allowed labels=%s. Return JSON with label, title, summary.\nConversation:\n%s", mode, strings.Join(canonicalLabels, ","), conversationForPrompt(r.SessionSample))
}
func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	idx := int(math.Ceil(p*float64(len(v)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(v) {
		idx = len(v) - 1
	}
	return v[idx]
}
func computePerLabel(items []EvalItem) map[string]LabelMetric {
	labels := map[string]bool{}
	for _, l := range canonicalLabels {
		labels[l] = true
	}
	for _, it := range items {
		labels[it.Expected] = true
		labels[it.Predicted] = true
	}
	out := map[string]LabelMetric{}
	for l := range labels {
		m := LabelMetric{}
		for _, it := range items {
			if it.Error != "" {
				continue
			}
			if it.Expected == l && it.Predicted == l {
				m.TP++
			}
			if it.Expected != l && it.Predicted == l {
				m.FP++
			}
			if it.Expected == l && it.Predicted != l {
				m.FN++
			}
		}
		if m.TP+m.FP > 0 {
			m.Precision = float64(m.TP) / float64(m.TP+m.FP)
		}
		if m.TP+m.FN > 0 {
			m.Recall = float64(m.TP) / float64(m.TP+m.FN)
		}
		if m.Precision+m.Recall > 0 {
			m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
		}
		out[l] = m
	}
	return out
}
func macroF1(m map[string]LabelMetric) float64 {
	if len(m) == 0 {
		return 0
	}
	var s float64
	for _, v := range m {
		s += v.F1
	}
	return s / float64(len(m))
}
func titleFormatRate(items []EvalItem) float64 {
	if len(items) == 0 {
		return 0
	}
	ok := 0
	for _, it := range items {
		r := []rune(it.Title)
		if len(r) > 0 && len(r) <= 80 && !strings.Contains(it.Title, "[TOKEN]") && !strings.Contains(it.Title, "system prompt") {
			ok++
		}
	}
	return float64(ok) / float64(len(items))
}
