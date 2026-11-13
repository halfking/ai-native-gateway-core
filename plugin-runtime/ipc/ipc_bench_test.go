package ipc

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// Benchmark single request/response
func BenchmarkSingleRequest(b *testing.B) {
	socketPath := filepath.Join(b.TempDir(), "bench.sock")

	// Create server
	server := NewServer(socketPath)
	defer server.Stop()

	// Register echo handler
	server.RegisterHandler(MessageTypeExtensionPoint, func(ctx context.Context, request []byte) ([]byte, error) {
		var req ExtensionPointRequest
		json.Unmarshal(request, &req)

		resp := ExtensionPointResponse{
			Type:    MessageTypeExtensionPoint,
			ID:      req.ID,
			Success: true,
			Result:  req.Context,
		}

		return json.Marshal(resp)
	})

	ctx := context.Background()
	server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// Create client
	client := NewClient(socketPath)
	defer client.Close()

	// Prepare request
	req := ExtensionPointRequest{
		Type:    MessageTypeExtensionPoint,
		ID:      "bench-1",
		Phase:   "governance",
		Plugin:  "test-plugin",
		Context: json.RawMessage(`{"key":"value"}`),
	}
	reqData, _ := json.Marshal(req)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.Send(ctx, reqData)
	}
}

// Benchmark concurrent requests
func BenchmarkConcurrentRequests(b *testing.B) {
	socketPath := filepath.Join(b.TempDir(), "bench.sock")

	// Create server
	server := NewServer(socketPath)
	defer server.Stop()

	// Register echo handler
	server.RegisterHandler(MessageTypeExtensionPoint, func(ctx context.Context, request []byte) ([]byte, error) {
		var req ExtensionPointRequest
		json.Unmarshal(request, &req)

		resp := ExtensionPointResponse{
			Type:    MessageTypeExtensionPoint,
			ID:      req.ID,
			Success: true,
		}

		return json.Marshal(resp)
	})

	ctx := context.Background()
	server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// Create client
	client := NewClient(socketPath)
	defer client.Close()

	// Prepare request
	req := ExtensionPointRequest{
		Type:   MessageTypeExtensionPoint,
		ID:     "bench-1",
		Phase:  "governance",
		Plugin: "test-plugin",
	}
	reqData, _ := json.Marshal(req)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			client.Send(ctx, reqData)
		}
	})
}

// Benchmark small message (1KB)
func BenchmarkSmallMessage(b *testing.B) {
	socketPath := filepath.Join(b.TempDir(), "bench.sock")

	server := NewServer(socketPath)
	defer server.Stop()

	server.RegisterHandler(MessageTypeExtensionPoint, func(ctx context.Context, request []byte) ([]byte, error) {
		return request, nil // Echo
	})

	ctx := context.Background()
	server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	client := NewClient(socketPath)
	defer client.Close()

	// 1KB payload
	payload := make([]byte, 1024)
	req := ExtensionPointRequest{
		Type:    MessageTypeExtensionPoint,
		ID:      "bench-1",
		Context: payload,
	}
	reqData, _ := json.Marshal(req)

	b.SetBytes(int64(len(reqData)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		client.Send(ctx, reqData)
	}
}

// Benchmark large message (100KB)
func BenchmarkLargeMessage(b *testing.B) {
	socketPath := filepath.Join(b.TempDir(), "bench.sock")

	server := NewServer(socketPath)
	defer server.Stop()

	server.RegisterHandler(MessageTypeExtensionPoint, func(ctx context.Context, request []byte) ([]byte, error) {
		return request, nil // Echo
	})

	ctx := context.Background()
	server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	client := NewClient(socketPath)
	defer client.Close()

	// 100KB payload
	payload := make([]byte, 100*1024)
	req := ExtensionPointRequest{
		Type:    MessageTypeExtensionPoint,
		ID:      "bench-1",
		Context: payload,
	}
	reqData, _ := json.Marshal(req)

	b.SetBytes(int64(len(reqData)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		client.Send(ctx, reqData)
	}
}

// Benchmark connection pool
func BenchmarkConnectionPool(b *testing.B) {
	socketPath := filepath.Join(b.TempDir(), "bench.sock")

	server := NewServer(socketPath)
	defer server.Stop()

	server.RegisterHandler(MessageTypeExtensionPoint, func(ctx context.Context, request []byte) ([]byte, error) {
		return []byte(`{"type":"extension_point","success":true}`), nil
	})

	ctx := context.Background()
	server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	client := NewClient(socketPath)
	defer client.Close()

	req := ExtensionPointRequest{
		Type:   MessageTypeExtensionPoint,
		ID:     "bench-1",
		Phase:  "governance",
		Plugin: "test-plugin",
	}
	reqData, _ := json.Marshal(req)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			client.Send(ctx, reqData)
		}
	})
}

// Benchmark JSON marshal/unmarshal
func BenchmarkJSONEncoding(b *testing.B) {
	req := ExtensionPointRequest{
		Type:    MessageTypeExtensionPoint,
		ID:      "bench-1",
		Phase:   "governance",
		Plugin:  "test-plugin",
		Context: json.RawMessage(`{"key":"value","data":"test"}`),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(req)
		var decoded ExtensionPointRequest
		json.Unmarshal(data, &decoded)
	}
}
