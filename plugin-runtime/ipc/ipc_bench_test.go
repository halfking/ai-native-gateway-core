package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func benchmarkRequest(b *testing.B, size int) []byte {
	b.Helper()
	payload, err := json.Marshal(strings.Repeat("a", size-2))
	if err != nil {
		b.Fatal(err)
	}
	if len(payload) != size || !json.Valid(payload) {
		b.Fatalf("invalid benchmark payload: want %d bytes, got %d", size, len(payload))
	}
	request, err := json.Marshal(ExtensionPointRequest{
		Type: MessageTypeExtensionPoint, ID: "bench-1", Context: payload,
	})
	if err != nil {
		b.Fatalf("marshal %d-byte JSON payload: %v", size, err)
	}
	return request
}

func benchmarkEcho(b *testing.B, size, concurrency int) {
	b.Helper()
	request := benchmarkRequest(b, size)
	server := NewServer(testSocketPath(b))
	server.RegisterHandler(MessageTypeExtensionPoint, func(_ context.Context, request []byte) ([]byte, error) {
		return request, nil
	})
	if err := server.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := server.Stop(); err != nil {
			b.Error(err)
		}
	})
	client := NewClient(server.socketPath)
	b.Cleanup(func() {
		if err := client.Close(); err != nil {
			b.Error(err)
		}
	})
	b.SetBytes(int64(len(request) * concurrency))
	b.ResetTimer()
	// Each benchmark operation is one request per worker. With concurrency=100,
	// even -benchtime=1x executes 100 checked requests through 100 workers.
	start := make(chan struct{})
	done := make(chan error, concurrency)
	for range concurrency {
		go func() {
			<-start
			for range b.N {
				response, err := client.Send(context.Background(), request)
				if err != nil {
					done <- fmt.Errorf("send: %w", err)
					return
				}
				if !bytes.Equal(response, request) {
					done <- fmt.Errorf("incomplete echo: sent %d bytes, received %d", len(request), len(response))
					return
				}
			}
			done <- nil
		}()
	}
	close(start)
	for range concurrency {
		if err := <-done; err != nil {
			b.Error(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(concurrency), "requests/op")
}

func BenchmarkSingleRequest(b *testing.B) { benchmarkEcho(b, 1024, 1) }

func BenchmarkConcurrentRequests(b *testing.B) { benchmarkEcho(b, 1024, 100) }

func BenchmarkSmallMessage(b *testing.B) { benchmarkEcho(b, 1024, 1) }

func BenchmarkLargeMessage(b *testing.B) { benchmarkEcho(b, 100*1024, 1) }

func Benchmark10MiBMessage(b *testing.B) { benchmarkEcho(b, 10*1024*1024, 1) }

// This measures concurrent Client.Send calls; the one-exchange-per-connection
// protocol does not reuse idle connections.
func BenchmarkConnectionPool(b *testing.B) { benchmarkEcho(b, 1024, 100) }

func BenchmarkJSONEncoding(b *testing.B) {
	req := ExtensionPointRequest{
		Type: MessageTypeExtensionPoint, ID: "bench-1", Phase: "governance",
		Plugin: "test-plugin", Context: json.RawMessage(`{"key":"value","data":"test"}`),
	}
	for range b.N {
		data, err := json.Marshal(req)
		if err != nil {
			b.Fatal(err)
		}
		var decoded ExtensionPointRequest
		if err := json.Unmarshal(data, &decoded); err != nil {
			b.Fatal(err)
		}
		if decoded.Type != req.Type || decoded.ID != req.ID || decoded.Phase != req.Phase ||
			decoded.Plugin != req.Plugin || !bytes.Equal(decoded.Context, req.Context) {
			b.Fatal("JSON round trip changed the request")
		}
	}
}
