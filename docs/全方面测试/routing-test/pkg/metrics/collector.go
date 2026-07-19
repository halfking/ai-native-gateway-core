package metrics

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Collector collects metrics during test execution
type Collector struct {
	mu             sync.RWMutex
	totalRequests  int
	successCount   int
	errorCount     int
	latencies      []time.Duration
	errorsByType   map[string]int
	startTime      time.Time
	credentialHits map[int]int // credential_id -> count
}

// NewCollector creates a new metrics collector
func NewCollector() *Collector {
	return &Collector{
		errorsByType:   make(map[string]int),
		credentialHits: make(map[int]int),
		startTime:      time.Now(),
	}
}

// RecordLatency records the latency of an operation
func (c *Collector) RecordLatency(operation string, duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latencies = append(c.latencies, duration)
}

// RecordError records an error occurrence
func (c *Collector) RecordError(errorType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errorCount++
	c.errorsByType[errorType]++
}

// RecordSuccess records a successful request
func (c *Collector) RecordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.successCount++
}

// RecordRequest records a total request
func (c *Collector) RecordRequest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalRequests++
}

// RecordCredentialHit records a credential being used
func (c *Collector) RecordCredentialHit(credentialID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.credentialHits[credentialID]++
}

// GetSummary returns a summary of collected metrics
func (c *Collector) GetSummary() Summary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Calculate percentiles
	sorted := make([]time.Duration, len(c.latencies))
	copy(sorted, c.latencies)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	p50, p95, p99, max := time.Duration(0), time.Duration(0), time.Duration(0), time.Duration(0)
	if len(sorted) > 0 {
		p50 = sorted[len(sorted)*50/100]
		p95 = sorted[len(sorted)*95/100]
		p99 = sorted[len(sorted)*99/100]
		max = sorted[len(sorted)-1]
	}

	// Calculate average
	var totalLatency time.Duration
	for _, lat := range c.latencies {
		totalLatency += lat
	}
	avg := time.Duration(0)
	if len(c.latencies) > 0 {
		avg = totalLatency / time.Duration(len(c.latencies))
	}

	// Calculate success rate
	successRate := 0.0
	if c.totalRequests > 0 {
		successRate = float64(c.successCount) / float64(c.totalRequests)
	}

	// Calculate throughput
	elapsed := time.Since(c.startTime)
	throughput := 0.0
	if elapsed.Seconds() > 0 {
		throughput = float64(c.totalRequests) / elapsed.Seconds()
	}

	// Copy error types
	errorsByType := make(map[string]int)
	for k, v := range c.errorsByType {
		errorsByType[k] = v
	}

	// Copy credential hits
	credentialHits := make(map[int]int)
	for k, v := range c.credentialHits {
		credentialHits[k] = v
	}

	return Summary{
		TotalRequests:  c.totalRequests,
		SuccessCount:   c.successCount,
		ErrorCount:     c.errorCount,
		SuccessRate:    successRate,
		P50Latency:     p50,
		P95Latency:     p95,
		P99Latency:     p99,
		MaxLatency:     max,
		AvgLatency:     avg,
		Throughput:     throughput,
		ElapsedTime:    elapsed,
		ErrorsByType:   errorsByType,
		CredentialHits: credentialHits,
	}
}

// Summary contains aggregated metrics
type Summary struct {
	TotalRequests  int
	SuccessCount   int
	ErrorCount     int
	SuccessRate    float64
	P50Latency     time.Duration
	P95Latency     time.Duration
	P99Latency     time.Duration
	MaxLatency     time.Duration
	AvgLatency     time.Duration
	Throughput     float64 // requests per second
	ElapsedTime    time.Duration
	ErrorsByType   map[string]int
	CredentialHits map[int]int // credential_id -> count
}

// Print prints the summary in human-readable format
func (s Summary) Print() {
	fmt.Println("========== Test Summary ==========")
	fmt.Printf("Total Requests:  %d\n", s.TotalRequests)
	fmt.Printf("Success:         %d (%.2f%%)\n", s.SuccessCount, s.SuccessRate*100)
	fmt.Printf("Errors:          %d\n", s.ErrorCount)
	fmt.Printf("Elapsed Time:    %s\n", s.ElapsedTime.Round(time.Millisecond))
	fmt.Printf("Throughput:      %.2f req/s\n", s.Throughput)
	fmt.Println()
	fmt.Println("Latency:")
	fmt.Printf("  P50: %s\n", s.P50Latency.Round(time.Millisecond))
	fmt.Printf("  P95: %s\n", s.P95Latency.Round(time.Millisecond))
	fmt.Printf("  P99: %s\n", s.P99Latency.Round(time.Millisecond))
	fmt.Printf("  Max: %s\n", s.MaxLatency.Round(time.Millisecond))
	fmt.Printf("  Avg: %s\n", s.AvgLatency.Round(time.Millisecond))

	if len(s.ErrorsByType) > 0 {
		fmt.Println()
		fmt.Println("Errors by Type:")
		for errType, count := range s.ErrorsByType {
			fmt.Printf("  %s: %d\n", errType, count)
		}
	}

	if len(s.CredentialHits) > 0 {
		fmt.Println()
		fmt.Println("Credential Distribution:")
		total := 0
		for _, count := range s.CredentialHits {
			total += count
		}
		for credID, count := range s.CredentialHits {
			percentage := float64(count) / float64(total) * 100
			fmt.Printf("  Credential %d: %d requests (%.2f%%)\n", credID, count, percentage)
		}
	}
	fmt.Println("==================================")
}

// Reset resets all collected metrics
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalRequests = 0
	c.successCount = 0
	c.errorCount = 0
	c.latencies = nil
	c.errorsByType = make(map[string]int)
	c.credentialHits = make(map[int]int)
	c.startTime = time.Now()
}
