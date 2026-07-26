package logging

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestLockFreeQueue_CloseWhileEnqueuing(t *testing.T) {
	queue := NewLockFreeQueue[int](1024)
	stop := make(chan struct{})
	var producers sync.WaitGroup

	for producer := 0; producer < 16; producer++ {
		producers.Add(1)
		go func(seed int) {
			defer producers.Done()
			for value := seed; ; value += 16 {
				select {
				case <-stop:
					return
				default:
					queue.Enqueue(value)
				}
			}
		}(producer)
	}

	time.Sleep(10 * time.Millisecond)
	queue.Close()
	close(stop)
	producers.Wait()

	if queue.Enqueue(1) {
		t.Fatal("enqueue succeeded after queue close")
	}
}

func TestAsyncRawDataLogger_CloseWhileLogging(t *testing.T) {
	logger, err := NewAsyncRawDataLogger(t.TempDir(), 1024*1024, true, 1024)
	if err != nil {
		t.Fatalf("NewAsyncRawDataLogger() error = %v", err)
	}

	stop := make(chan struct{})
	var producers sync.WaitGroup
	for producer := 0; producer < 16; producer++ {
		producers.Add(1)
		go func(id int) {
			defer producers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					logger.LogUpstreamResponse("request", "openai-completions", []byte("payload"), "pre_conversion")
				}
			}
		}(producer)
	}

	time.Sleep(10 * time.Millisecond)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	close(stop)
	producers.Wait()
}

func TestLockFreeAnomalyReporter_CloseRejectsReports(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	reporter := NewLockFreeAnomalyReporter(server.URL, true, 4)
	reporter.ReportConversionError(nil, "request", "openai-completions", "anthropic-messages", "parse", []byte("payload"), errors.New("parse failed"))
	if err := reporter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	statsAfterClose := reporter.Stats()
	reporter.ReportConversionError(nil, "request", "openai-completions", "anthropic-messages", "parse", []byte("payload"), errors.New("parse failed"))
	if stats := reporter.Stats(); stats.Enqueued != statsAfterClose.Enqueued {
		t.Fatalf("reports enqueued after close = %d, want %d", stats.Enqueued, statsAfterClose.Enqueued)
	}
}
