package sessionsummary

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestSummarizerNilRedisCacheIsSafe(t *testing.T) {
	summarizer := NewSummarizer(nil, nil, nil)
	if _, err := summarizer.getCachedSummary(context.Background(), "tenant-1", "session-1"); !errors.Is(err, redis.Nil) {
		t.Fatalf("getCachedSummary error = %v, want redis.Nil", err)
	}
	if err := summarizer.cacheSummary(context.Background(), "tenant-1", &SessionSummary{SessionKey: "session-1"}, time.Hour); err != nil {
		t.Fatalf("cacheSummary error = %v", err)
	}
}
