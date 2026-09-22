// Package main is the stress-test mock client.
//
// IMPORTANT: It only talks to the test gateway on localhost. All model
// names are NON-REAL (`mock-stress-*`). Behaviour modes:
//
//	-burst      single-shot burst of N requests at concurrency C
//	-sustained  RPS-controlled sustained load for D seconds
//	-streams    SSE stream clients, including early-cancel pattern
//	-mixed      interleaved non-stream + stream + cancel for chaos
//
// Each request writes a JSON Lines result to -output:
//
//	{"ts":...,"seq":...,"model":...,"stream":...,"status":...,
//	 "ttfb_ms":...,"total_ms":...,"first_byte":...,"cancelled":...,
//	 "error":...}
//
// Usage:
//
//	go run ./tests/stress/client -gateway=http://127.0.0.1:18901 \
//	    -mode=burst -concurrency=50 -total=2000 -output=/tmp/run.jsonl
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	TS        time.Time `json:"ts"`
	Seq       int64     `json:"seq"`
	Model     string    `json:"model"`
	Stream    bool      `json:"stream"`
	Status    int       `json:"status"`
	TTFBMs    int64     `json:"ttfb_ms"`
	TotalMs   int64     `json:"total_ms"`
	Bytes     int       `json:"bytes"`
	FirstByte bool      `json:"first_byte"`
	Cancelled bool      `json:"cancelled"`
	Error     string    `json:"error,omitempty"`
	Worker    int       `json:"worker"`
}

func main() {
	gateway := flag.String("gateway", "http://127.0.0.1:18901", "test gateway URL")
	mode := flag.String("mode", "burst", "burst|sustained|streams|mixed")
	concurrency := flag.Int("concurrency", 20, "concurrent clients")
	total := flag.Int("total", 100, "total requests (burst/streams) or target count (mixed)")
	duration := flag.Duration("duration", 30*time.Second, "sustained run duration")
	rps := flag.Int("rps", 0, "if >0, sustained target RPS (per-client cap)")
	modelsFlag := flag.String("models", "mock-stress-fast,mock-stress-large,mock-stress-stream", "comma-separated models")
	promptSize := flag.String("prompt", "short", "short|medium|long")
	streamRatio := flag.Float64("stream-ratio", 0.0, "fraction of requests that should be stream [0,1]")
	cancelRatio := flag.Float64("cancel-ratio", 0.0, "fraction of stream requests cancelled after first chunk")
	output := flag.String("output", "", "JSON Lines output path; defaults to stdout")
	timeout := flag.Duration("timeout", 60*time.Second, "per-request timeout")
	flag.Parse()

	models := splitNonEmpty(*modelsFlag)
	if len(models) == 0 {
		log.Fatalf("at least one model required")
	}

	var writer io.Writer = os.Stdout
	if *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			log.Fatalf("create output: %v", err)
		}
		defer f.Close()
		writer = f
	}
	enc := json.NewEncoder(writer)

	switch *mode {
	case "burst":
		runBurst(enc, *gateway, models, *concurrency, *total, *promptSize, *timeout)
	case "sustained":
		runSustained(enc, *gateway, models, *concurrency, *duration, *rps, *promptSize, *timeout)
	case "streams":
		runStreams(enc, *gateway, models, *concurrency, *total, *promptSize, *cancelRatio, *timeout)
	case "mixed":
		runMixed(enc, *gateway, models, *concurrency, *total, *duration, *promptSize, *streamRatio, *cancelRatio, *timeout)
	default:
		log.Fatalf("unknown mode: %s", *mode)
	}
}

// ─────────────────────── burst ───────────────────────

func runBurst(enc *json.Encoder, gateway string, models []string,
	concurrency, total int, promptSize string, timeout time.Duration) {

	if concurrency < 1 {
		concurrency = 1
	}
	if total < concurrency {
		total = concurrency
	}

	seq := atomic.Int64{}
	var wg sync.WaitGroup
	jobs := make(chan int, total)

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			client := &http.Client{Timeout: timeout}
			for range jobs {
				model := models[rand.Intn(len(models))]
				req := buildRequest(model, false, promptSize, 64)
				emit(enc, doOnce(client, gateway, req, worker, &seq))
			}
		}(w)
	}

	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// ─────────────────────── sustained ───────────────────────

func runSustained(enc *json.Encoder, gateway string, models []string,
	concurrency int, duration time.Duration, rps int, promptSize string, timeout time.Duration) {

	if concurrency < 1 {
		concurrency = 1
	}

	client := &http.Client{Timeout: timeout}
	deadline := time.Now().Add(duration)
	var wg sync.WaitGroup
	seq := atomic.Int64{}

	// Pace: if rps>0, total = rps*duration(s); else unlimited per worker.
	var interval time.Duration
	var cap int64
	if rps > 0 {
		interval = time.Duration(int64(time.Second)/int64(rps)) / time.Duration(concurrency)
		cap = int64(rps) * int64(duration/time.Second)
	}

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for time.Now().Before(deadline) {
				if cap > 0 && seq.Load() >= cap {
					return
				}
				model := models[rand.Intn(len(models))]
				req := buildRequest(model, false, promptSize, 64)
				emit(enc, doOnce(client, gateway, req, worker, &seq))
				if interval > 0 {
					<-ticker.C
				}
			}
		}(w)
	}
	wg.Wait()
}

// ─────────────────────── streams ───────────────────────

func runStreams(enc *json.Encoder, gateway string, models []string,
	concurrency, total int, promptSize string, cancelRatio float64, timeout time.Duration) {

	if concurrency < 1 {
		concurrency = 1
	}
	seq := atomic.Int64{}
	jobs := make(chan int, total)
	var wg sync.WaitGroup

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			client := &http.Client{Timeout: timeout}
			for range jobs {
				model := models[rand.Intn(len(models))]
				req := buildRequest(model, true, promptSize, 64)
				// Pick whether to cancel early.
				cancel := rand.Float64() < cancelRatio
				emit(enc, doStream(client, gateway, req, worker, &seq, cancel))
			}
		}(w)
	}

	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// ─────────────────────── mixed ───────────────────────

func runMixed(enc *json.Encoder, gateway string, models []string,
	concurrency, total int, duration time.Duration, promptSize string,
	streamRatio, cancelRatio float64, timeout time.Duration) {

	if duration <= 0 {
		duration = 30 * time.Second
	}
	deadline := time.Now().Add(duration)
	seq := atomic.Int64{}
	var wg sync.WaitGroup
	jobs := make(chan int, total)

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			client := &http.Client{Timeout: timeout}
			for range jobs {
				if !time.Now().Before(deadline) {
					return
				}
				model := models[rand.Intn(len(models))]
				stream := rand.Float64() < streamRatio
				req := buildRequest(model, stream, promptSize, 64)
				var res result
				if stream {
					cancel := rand.Float64() < cancelRatio
					res = doStream(client, gateway, req, worker, &seq, cancel)
				} else {
					res = doOnce(client, gateway, req, worker, &seq)
				}
				emit(enc, res)
			}
		}(w)
	}

	go func() {
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			select {
			case <-t.C:
				if !time.Now().Before(deadline) {
					close(jobs)
					return
				}
				if i >= total {
					close(jobs)
					return
				}
				jobs <- i
			}
		}
	}()
	wg.Wait()
}

// ─────────────────────── request building ───────────────────────

func buildRequest(model string, stream bool, promptSize string, maxTokens int) *http.Request {
	body := buildBody(model, stream, promptSize, maxTokens)
	r, _ := http.NewRequest(http.MethodPost,
		"http://gateway-placeholder/v1/chat/completions",
		bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func buildBody(model string, stream bool, promptSize string, maxTokens int) []byte {
	prompt := promptContent(promptSize)
	msg := map[string]any{
		"role":    "user",
		"content": prompt,
	}
	body := map[string]any{
		"model":      model,
		"messages":   []any{msg},
		"max_tokens": maxTokens,
		"stream":     stream,
	}
	b, _ := json.Marshal(body)
	return b
}

func promptContent(size string) string {
	const block = "stress-prompt-word "
	switch size {
	case "long":
		return "[long prompt] " + strings.Repeat(block, 1500) // ~24k chars
	case "medium":
		return "[medium prompt] " + strings.Repeat(block, 250) // ~4k chars
	default: // short
		return "[short prompt] please answer"
	}
}

// ─────────────────────── non-stream runner ───────────────────────

func doOnce(client *http.Client, gateway string, req *http.Request,
	worker int, seq *atomic.Int64) result {
	s := seq.Add(1)
	res := result{
		TS:     time.Now(),
		Seq:    s,
		Model:  req.URL.Query().Get("model"), // empty, fallback below
		Stream: false,
		Worker: worker,
	}
	// Body holds the model name; parse quickly without re-decoding the
	// whole request (we already have model in the body, but it's easier
	// to round-trip).
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	// Rebuild model name from the body's first field is overkill; we
	// instead peek the body for the "model" key directly.
	res.Model = extractModel(req)
	if req.GetBody != nil {
		// not needed here; just leave it.
	}
	_ = body

	// Build absolute URL with gateway.
	upReq := req.Clone(req.Context())
	upReq.URL.Scheme = "http"
	upReq.URL.Host = strings.TrimPrefix(gateway, "http://")
	upReq.URL.Path = "/v1/chat/completions"

	started := time.Now()
	resp, err := client.Do(upReq)
	res.TotalMs = time.Since(started).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		res.Bytes = len(bodyBytes)
		res.FirstByte = res.TotalMs >= 0
	}
	return res
}

// ─────────────────────── stream runner ───────────────────────

func doStream(client *http.Client, gateway string, req *http.Request,
	worker int, seq *atomic.Int64, cancelEarly bool) result {
	s := seq.Add(1)
	res := result{
		TS:     time.Now(),
		Seq:    s,
		Model:  extractModel(req),
		Stream: true,
		Worker: worker,
	}

	upReq := req.Clone(req.Context())
	upReq.URL.Scheme = "http"
	upReq.URL.Host = strings.TrimPrefix(gateway, "http://")
	upReq.URL.Path = "/v1/chat/completions"

	// Build a context we can cancel manually for cancelEarly.
	ctx, cancel := contextFunc(upReq.Context())
	defer cancel()

	started := time.Now()
	resp, err := client.Do(upReq.Clone(ctx))
	if err != nil {
		res.TotalMs = time.Since(started).Milliseconds()
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	chunks := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			if res.TTFBMs == 0 {
				res.TTFBMs = time.Since(started).Milliseconds()
				res.FirstByte = true
			}
			chunks++
			if cancelEarly && chunks >= 3 {
				// Simulate client disconnect.
				res.Cancelled = true
				cancel()
				res.TotalMs = time.Since(started).Milliseconds()
				return res
			}
		}
	}
	res.TotalMs = time.Since(started).Milliseconds()
	if err := scanner.Err(); err != nil {
		if res.Error == "" {
			res.Error = err.Error()
		}
	}
	return res
}

// contextFunc returns a cancelable context derived from parent.
func contextFunc(parent context.Context) (context.Context, func()) {
	return context.WithCancel(parent)
}

// ─────────────────────── helpers ───────────────────────

func emit(enc *json.Encoder, r result) {
	if err := enc.Encode(r); err != nil {
		log.Printf("encode: %v", err)
	}
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func extractModel(req *http.Request) string {
	if req.Body == nil {
		return ""
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return ""
	}
	// Reset body for downstream reads.
	req.Body = io.NopCloser(bytes.NewReader(body))
	var peek struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &peek)
	return peek.Model
}

// ─────────────────────── placeholder numeric ───────────────────────

var _ = strconv.Itoa // keep strconv alive if unused
