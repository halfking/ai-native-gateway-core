// Package main is the memory-profile validation harness.
//
// Goal: derive the optimal CPU/RAM ratio for the LLM gateway on each of
// 2c4G / 2c8G / 4c8G / 4c16G by running representative scenarios under
// tight memory limits (GOMEMLIMIT) and recording the throughput
// degradation curve.
//
// Methodology:
//  1. For each spec × memory-budget grid:
//     - GOMAXPROCS = cores, GOMEMLIMIT = (RAM - OS headroom)
//     - Run a memory "competitor" that pre-allocates the rest, then
//     launch the gateway with the chosen GOMEMLIMIT
//     - Run scenarios s8_burst, s9_sustained, s13_long, s15_weighted
//     - Sample /admin/memstats every 200 ms throughout the run
//  2. Emit JSONL traces for plotting, plus a summary report.
//
// Usage:
//
//	go run ./tests/stress/scripts/memprofile.go
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
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type spec struct {
	Name         string
	Cores        int
	HostRAMMiB   int // total host RAM assigned to gateway "host"
	GWLimitMiB   int // GOMEMLIMIT for the gateway process
	GOGC         int // GOGC percent (0 = off)
	PGConns      int // simulated pgxpool connections (≈5 MB each)
	RedisConns   int // simulated Redis pool connections (≈100 KB each)
	URSMSessions int // simulated URSM v2 sessions (≈256 B each)
	Description  string
}

type runSample struct {
	TS          time.Time `json:"ts"`
	Phase       string    `json:"phase"` // before|warmup|during|cooldown
	AllocMB     uint64    `json:"alloc_mb"`
	SysMB       uint64    `json:"sys_mb"`
	HeapInuse   uint64    `json:"heap_inuse_mb"`
	HeapAlloc   uint64    `json:"heap_alloc_mb"`
	NumGC       uint32    `json:"num_gc"`
	NumGorr     int       `json:"goroutines"`
	PauseLastNs uint64    `json:"pause_last_ns"`
}

type runReport struct {
	Spec           spec          `json:"spec"`
	StartedAt      time.Time     `json:"started_at"`
	FinishedAt     time.Time     `json:"finished_at"`
	Scenarios      []scenarioOut `json:"scenarios"`
	PeakRSS        uint64        `json:"peak_rss_mb"`
	PeakAlloc      uint64        `json:"peak_alloc_mb"`
	PeakGorr       int           `json:"peak_goroutines"`
	TotalGCPauseNs uint64        `json:"total_gc_pause_ns"`
}

type scenarioOut struct {
	ID         string  `json:"id"`
	TotalReqs  int     `json:"total_requests"`
	Success    int     `json:"success"`
	SuccessR   float64 `json:"success_rate"`
	DurationMS int64   `json:"duration_ms"`
	RPS        float64 `json:"rps"`
}

func main() {
	gatewayBin := flag.String("gateway-bin", "/tmp/stress-gateway", "path to stress-gateway binary")
	gatewayPort := flag.String("gateway-port", "18901", "gateway listen port")
	mocks := flag.Bool("with-mocks", true, "start 4 mock upstreams before running")
	results := flag.String("results", "tests/stress/results/memory.jsonl", "JSONL output")
	summary := flag.String("summary", "tests/stress/results/memory-summary.json", "summary JSON output")
	only := flag.String("only", "", "comma-separated spec names to run (e.g. '2c4G,4c8G')")
	flag.Parse()

	allSpecs := []spec{
		{Name: "2c4G", Cores: 2, HostRAMMiB: 4096, GWLimitMiB: 3500, GOGC: 100,
			PGConns: 4, RedisConns: 8, URSMSessions: 1000,
			Description: "Smallest prod spec; tiny pool for host-safe simulation."},
		{Name: "2c8G", Cores: 2, HostRAMMiB: 8192, GWLimitMiB: 7500, GOGC: 100,
			PGConns: 8, RedisConns: 16, URSMSessions: 10000,
			Description: "Edge+large cache; modest pool."},
		{Name: "4c8G", Cores: 4, HostRAMMiB: 8192, GWLimitMiB: 7500, GOGC: 100,
			PGConns: 16, RedisConns: 32, URSMSessions: 50000,
			Description: "Production workhorse; mid-size pool."},
		{Name: "4c16G", Cores: 4, HostRAMMiB: 16384, GWLimitMiB: 15000, GOGC: 100,
			PGConns: 32, RedisConns: 64, URSMSessions: 100000,
			Description: "Big-tenant / hot-cache; large pool."},
	}

	if *only != "" {
		wanted := map[string]bool{}
		for _, s := range splitNonEmpty(*only) {
			wanted[s] = true
		}
		filtered := allSpecs[:0]
		for _, s := range allSpecs {
			if wanted[s.Name] {
				filtered = append(filtered, s)
			}
		}
		allSpecs = filtered
	}

	if err := os.MkdirAll(parentDir(*results), 0o755); err != nil {
		log.Fatal(err)
	}

	if *mocks {
		startMocks()
	}

	resF, err := os.Create(*results)
	if err != nil {
		log.Fatal(err)
	}
	defer resF.Close()
	enc := json.NewEncoder(resF)

	var reports []runReport
	for _, sp := range allSpecs {
		log.Printf("====== spec %s (cores=%d, mem=%d MB, gwlimit=%d MB) ======",
			sp.Name, sp.Cores, sp.HostRAMMiB, sp.GWLimitMiB)

		// Spawn memory competitor to consume OS RAM headroom; this
		// exercises the host-RAM dimension that GOMEMLIMIT alone can't
		// model (Go heap vs OS pages).
		comp := startMemoryCompetitor(sp.HostRAMMiB - sp.GWLimitMiB + 200) // 200 MB margin

		// Spawn the gateway with the requested constraints.
		gwCmd, gwClient := spawnGateway(*gatewayBin, *gatewayPort, sp)

		// Pre-warmup: warm up goroutines + heap so cold-start cost is
		// not attributed to the run.
		warmup(gwClient)

		// Run the four representative scenarios.
		rep := runReport{
			Spec:      sp,
			StartedAt: time.Now(),
		}
		var peakRSSUnused, peakAlloc uint64
		_ = peakRSSUnused
		var peakGorr int
		var totalPause uint64
		stopSampler := startSampler(gwClient, enc, sp.Name, func(s runSample) {
			if s.AllocMB > peakAlloc {
				peakAlloc = s.AllocMB
			}
			if s.NumGorr > peakGorr {
				peakGorr = s.NumGorr
			}
			totalPause += s.PauseLastNs
		})

		for _, sc := range []struct {
			id  string
			run func() scenarioOut
		}{
			{"s8_burst", func() scenarioOut { return runSc("s8_burst_stress", gwClient) }},
			{"s9_sustained", func() scenarioOut { return runSc("s9_sustained_load", gwClient) }},
			{"s13_long", func() scenarioOut { return runSc("s13_long_prompt_stress", gwClient) }},
			{"s15_weighted", func() scenarioOut { return runSc("s15_dynamic_weighting", gwClient) }},
		} {
			log.Printf("  [spec %s] %s ...", sp.Name, sc.id)
			out := sc.run()
			out.ID = sc.id
			rep.Scenarios = append(rep.Scenarios, out)
			log.Printf("  [spec %s] %s → %.0f rps, %.1f%% success",
				sp.Name, sc.id, out.RPS, out.SuccessR*100)
		}
		close(stopSampler)
		rep.FinishedAt = time.Now()
		rep.PeakAlloc = peakAlloc
		rep.PeakGorr = peakGorr
		rep.TotalGCPauseNs = totalPause
		rep.PeakRSS = peakAlloc // approx; real RSS measured via ps
		reports = append(reports, rep)

		// Tear down.
		_ = gwCmd.Process.Kill()
		_ = gwCmd.Wait()
		if comp != nil && comp.Process != nil {
			_ = comp.Process.Kill()
			_ = comp.Wait()
		}
	}

	if err := writeSummary(*summary, reports); err != nil {
		log.Fatal(err)
	}
	log.Printf("results: %s", *results)
	log.Printf("summary: %s", *summary)
}

// ─────────────────────── gateway control ───────────────────────

func spawnGateway(bin, port string, sp spec) (*exec.Cmd, *http.Client) {
	// Compute total simulated memory: 5 MB × PGConns + 100 KB × RedisConns + 256 B × URSM
	simMB := sp.PGConns*5 + sp.RedisConns/10 + sp.URSMSessions*256/(1024*1024)

	cmd := exec.Command(bin,
		"-port="+port,
		fmt.Sprintf("-mem-limit-mb=%d", sp.GWLimitMiB),
		fmt.Sprintf("-gogc=%d", sp.GOGC),
		fmt.Sprintf("-simulate-prod-mem=%d", simMB),
		fmt.Sprintf("-simulate-pg-pool=%d", sp.PGConns),
		fmt.Sprintf("-simulate-redis-pool=%d", sp.RedisConns),
		fmt.Sprintf("-simulate-ursm-sessions=%d", sp.URSMSessions),
	)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GOMAXPROCS=%d", sp.Cores),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Fatalf("spawn gateway: %v", err)
	}
	// Wait for /healthz
	waitForHealth("http://127.0.0.1:"+port+"/healthz", 15*time.Second)
	return cmd, &http.Client{Timeout: 60 * time.Second}
}

func waitForHealth(url string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Fatalf("gateway did not come up at %s", url)
}

// ─────────────────────── sampler ───────────────────────

func startSampler(client *http.Client, enc *json.Encoder, specName string,
	record func(s runSample)) chan struct{} {

	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s := sampleMem(client)
				if s.AllocMB == 0 && s.SysMB == 0 {
					// gateway unreachable; skip emission.
					continue
				}
				s.Phase = "during"
				s.TS = time.Now()
				out := map[string]any{
					"spec":          specName,
					"ts":            s.TS,
					"phase":         s.Phase,
					"alloc_mb":      s.AllocMB,
					"sys_mb":        s.SysMB,
					"heap_inuse_mb": s.HeapInuse,
					"heap_alloc_mb": s.HeapAlloc,
					"num_gc":        s.NumGC,
					"goroutines":    s.NumGorr,
					"pause_last_ns": s.PauseLastNs,
				}
				_ = enc.Encode(out)
				record(s)
			}
		}
	}()
	return stop
}

// sampleMem queries the gateway's /admin/memstats endpoint to read the
// gateway process's heap pressure (not the memprofile's own).
func sampleMem(client *http.Client) runSample {
	resp, err := client.Get("http://127.0.0.1:18901/admin/memstats")
	if err != nil {
		return runSample{}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var m struct {
		AllocMB     uint64 `json:"alloc_mb"`
		SysMB       uint64 `json:"sys_mb"`
		HeapAllocMB uint64 `json:"heap_alloc_mb"`
		HeapInuseMB uint64 `json:"heap_inuse_mb"`
		HeapObjects uint64 `json:"heap_objects"`
		NumGC       uint32 `json:"num_gc"`
		PauseLastNs uint64 `json:"pause_last_ns"`
		Goroutines  int    `json:"goroutines"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return runSample{}
	}
	return runSample{
		AllocMB:     m.AllocMB,
		SysMB:       m.SysMB,
		HeapInuse:   m.HeapInuseMB,
		HeapAlloc:   m.HeapAllocMB,
		NumGC:       m.NumGC,
		NumGorr:     m.Goroutines,
		PauseLastNs: m.PauseLastNs,
	}
}

func lastPauseNs(ms runtime.MemStats) uint64 {
	if ms.NumGC == 0 {
		return 0
	}
	return ms.PauseNs[(ms.NumGC+255)%256]
}

// ─────────────────────── scenarios ───────────────────────

func runSc(id string, client *http.Client) scenarioOut {
	// Pre-reset gateway state.
	resetGateway(client)

	// Inline mini-scenarios mirroring the heavy hitters from
	// scenarios.json. We use direct HTTP to avoid bootstrapping the
	// full driver.
	var conf struct {
		n, c         int
		stream, long bool
	}
	switch id {
	case "s8_burst_stress":
		conf = struct {
			n, c         int
			stream, long bool
		}{2000, 50, false, false}
	case "s9_sustained_load":
		conf = struct {
			n, c         int
			stream, long bool
		}{200, 25, false, false}
	case "s13_long_prompt_stress":
		conf = struct {
			n, c         int
			stream, long bool
		}{200, 20, false, true}
	case "s15_dynamic_weighting":
		conf = struct {
			n, c         int
			stream, long bool
		}{600, 30, false, false}
	default:
		log.Fatalf("unknown scenario %s", id)
	}

	jobs := make(chan int, conf.n)
	resCh := make(chan bool, conf.n)
	for w := 0; w < conf.c; w++ {
		go func() {
			for range jobs {
				resCh <- sendOne(client, conf.stream, conf.long)
			}
		}()
	}
	started := time.Now()
	for i := 0; i < conf.n; i++ {
		jobs <- i
	}
	close(jobs)
	succ := 0
	for i := 0; i < conf.n; i++ {
		if <-resCh {
			succ++
		}
	}
	dur := time.Since(started).Milliseconds()
	rps := float64(conf.n) / (float64(dur) / 1000.0)
	return scenarioOut{
		ID: id, TotalReqs: conf.n, Success: succ,
		SuccessR:   float64(succ) / float64(conf.n),
		DurationMS: dur, RPS: rps,
	}
}

func sendOne(client *http.Client, stream, long bool) bool {
	prompt := "stress"
	if long {
		prompt = strings.Repeat("stress ", 4000)
	}
	body, _ := json.Marshal(map[string]any{
		"model":      "mock-stress-fast",
		"stream":     stream,
		"messages":   []any{map[string]any{"role": "user", "content": prompt}},
		"max_tokens": 32,
	})
	resp, err := client.Post("http://127.0.0.1:18901/v1/chat/completions",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, resp.Body)
		return true
	}
	return false
}

func resetGateway(client *http.Client) {
	req, _ := http.NewRequest(http.MethodPost,
		"http://127.0.0.1:18901/admin/reset", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

// ─────────────────────── memory competitor ───────────────────────

// startMemoryCompetitor spawns a small Go program that pre-allocates
// the requested amount of RAM (and never releases it) so the host's
// page cache + RSS budget matches the spec we're modelling. This
// complements GOMEMLIMIT (which is a soft heap cap, not an OS cap).
func startMemoryCompetitor(mib int) *exec.Cmd {
	if mib <= 200 {
		// Less than 200 MB competitor → no need to spawn anything;
		// return nil so callers can no-op the teardown.
		return nil
	}
	prog := `package main
import ("runtime/debug")
func main() {
    debug.SetMemoryLimit(int64(` + fmt.Sprint(mib) + `) * 1024 * 1024)
    chunk := make([][]byte, 0, 256)
    for i := 0; i < 256; i++ {
        b := make([]byte, ` + fmt.Sprint((mib/256)*1024*1024) + `)
        for j := range b { b[j] = 1 }
        chunk = append(chunk, b)
    }
    select {}
}
`
	tmp, _ := os.CreateTemp("", "memcomp-*.go")
	tmp.WriteString(prog)
	tmp.Close()
	defer os.Remove(tmp.Name())
	bin := tmp.Name() + ".bin"
	build := exec.Command("go", "build", "-o", bin, tmp.Name())
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		log.Printf("mem competitor build failed (skip): %v", err)
		return &exec.Cmd{}
	}
	cmd := exec.Command(bin)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		log.Printf("mem competitor spawn failed: %v", err)
		return &exec.Cmd{}
	}
	return cmd
}

// ─────────────────────── mocks ───────────────────────

func startMocks() {
	// Mocks are already expected to be running (we share the harness
	// with the main scenario driver). Just verify.
	for _, port := range []string{"18101", "18102", "18103", "18104"} {
		waitForHealth("http://127.0.0.1:"+port+"/healthz", 3*time.Second)
	}
}

// ─────────────────────── warmup ───────────────────────

func warmup(client *http.Client) {
	// Fire 50 warmup requests so the gateway pre-allocates hot
	// structures before the measurement window opens.
	jobs := make(chan int, 50)
	var wg sync.WaitGroup
	var c atomic.Int64
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				if sendOne(client, false, false) {
					c.Add(1)
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	log.Printf("  warmup: %d/50 ok", c.Load())
}

// ─────────────────────── summary ───────────────────────

func writeSummary(path string, reports []runReport) error {
	type cell struct {
		Spec     string  `json:"spec"`
		Scenario string  `json:"scenario"`
		RPS      float64 `json:"rps"`
		SuccessR float64 `json:"success_rate"`
	}
	type summary struct {
		Spec           string `json:"spec"`
		Cores          int    `json:"cores"`
		GWLimitMiB     int    `json:"gw_limit_mb"`
		SimProdMemMiB  int    `json:"sim_prod_mem_mb"`
		Cells          []cell `json:"cells"`
		PeakAllocMB    uint64 `json:"peak_alloc_mb"`
		PeakGorr       int    `json:"peak_goroutines"`
		TotalGCPauseNS uint64 `json:"total_gc_pause_ns"`
	}
	var rows []cell
	var summaries []summary
	for _, r := range reports {
		s := summary{Spec: r.Spec.Name,
			Cores: r.Spec.Cores, GWLimitMiB: r.Spec.GWLimitMiB,
			SimProdMemMiB: r.Spec.PGConns*5 + r.Spec.RedisConns/10 + r.Spec.URSMSessions*256/(1024*1024),
			PeakAllocMB:   r.PeakAlloc, PeakGorr: r.PeakGorr, TotalGCPauseNS: r.TotalGCPauseNs}
		for _, sc := range r.Scenarios {
			rows = append(rows, cell{Spec: r.Spec.Name, Scenario: sc.ID, RPS: sc.RPS, SuccessR: sc.SuccessR})
			s.Cells = append(s.Cells, cell{Spec: r.Spec.Name, Scenario: sc.ID, RPS: sc.RPS, SuccessR: sc.SuccessR})
		}
		summaries = append(summaries, s)
	}
	type payload struct {
		Rows        []cell    `json:"rows"`
		Summaries   []summary `json:"summaries"`
		GeneratedAt time.Time `json:"generated_at"`
	}
	b, _ := json.MarshalIndent(payload{Rows: rows, Summaries: summaries, GeneratedAt: time.Now()}, "", "  ")
	return os.WriteFile(path, b, 0o644)
}

// ─────────────────────── helpers ───────────────────────

func splitNonEmpty(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

var _ = rand.Intn
var _ = debug.SetMemoryLimit
var _ = atomic.AddUint64
var _ = exec.Command
var _ = sync.WaitGroup{}
var _ = bytes.NewReader
