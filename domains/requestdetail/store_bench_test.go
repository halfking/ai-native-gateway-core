package requestdetail

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// BenchmarkGetFileConcurrent measures concurrent GetFile performance with 1MB bodies.
// This benchmark validates the lock granularity optimization (task 2).
func BenchmarkGetFileConcurrent(b *testing.B) {
	dir := b.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		b.Fatal(err)
	}

	// Setup: 100 requests with 1MB bodies
	const numRequests = 100
	const bodySize = 1024 * 1024 // 1MB

	for i := 0; i < numRequests; i++ {
		reqID := fmt.Sprintf("req-bench-%04d", i)
		// Create valid JSON array of 1MB
		largeArray := make([]string, 0, 15000)
		for j := 0; j < 15000; j++ {
			largeArray = append(largeArray, strings.Repeat("x", 64))
		}
		largeBodyJSON, _ := json.Marshal(largeArray)

		meta := Meta{RequestID: reqID, TenantID: "default"}
		bodies := Bodies{RequestBody: json.RawMessage(largeBodyJSON)}

		if err := store.Put(meta, &bodies); err != nil {
			b.Fatalf("failed to put %s: %v", reqID, err)
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			reqID := fmt.Sprintf("req-bench-%04d", i%numRequests)
			_, _, err := store.GetFile(reqID)
			if err != nil {
				b.Fatalf("GetFile failed: %v", err)
			}
			i++
		}
	})
}

// BenchmarkGetFileSequential measures single-threaded GetFile performance.
func BenchmarkGetFileSequential(b *testing.B) {
	dir := b.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		b.Fatal(err)
	}

	// Setup: 10 requests with 100KB bodies
	const numRequests = 10
	for i := 0; i < numRequests; i++ {
		reqID := fmt.Sprintf("req-seq-%04d", i)
		smallArray := make([]string, 0, 1500)
		for j := 0; j < 1500; j++ {
			smallArray = append(smallArray, strings.Repeat("y", 64))
		}
		bodyJSON, _ := json.Marshal(smallArray)

		meta := Meta{RequestID: reqID, TenantID: "default"}
		bodies := Bodies{RequestBody: json.RawMessage(bodyJSON)}

		if err := store.Put(meta, &bodies); err != nil {
			b.Fatalf("failed to put %s: %v", reqID, err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reqID := fmt.Sprintf("req-seq-%04d", i%numRequests)
		_, _, err := store.GetFile(reqID)
		if err != nil {
			b.Fatalf("GetFile failed: %v", err)
		}
	}
}
