package sanitize

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// 2026-11-12 211 实测：一次客户端中断风暴 1h 刷 8,661 条
// "sanitize_restore: stream chunk load map failed"（error 全为
// "redis TYPE check failed: context canceled"）。取消是流的常态命运，
// 不是故障——遮蔽兜底照常，日志级必须随事实走。
func TestSanitizeRestoreCanceledContextMasksQuietly(t *testing.T) {
	var buf bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(oldDefault)

	s, _ := NewSanitizer(NewPatternDetector())
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond})
	defer rdb.Close()
	guard, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	meta := &response.StreamMeta{SessionID: "sess-canceled-quiet", State: response.NewStreamState()}
	chunk := []byte("data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"plain text without reserved markers\"}\n\n")
	if _, err := guard.InterceptStreamChunk(ctx, chunk, meta); err != nil {
		t.Fatalf("canceled chunk pass returned error: %v", err)
	}
	if strings.Contains(buf.String(), "\"level\":\"WARN\"") {
		t.Fatalf("canceled context must not WARN, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "load map canceled") {
		t.Fatalf("canceled pass must still be visible at DEBUG level, got: %s", buf.String())
	}
}

// 真故障（Redis 不可达且非取消）仍须 WARN——降噪不吞故障。
func TestSanitizeRestoreRealFailureStillWarns(t *testing.T) {
	var buf bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(oldDefault)

	s, _ := NewSanitizer(NewPatternDetector())
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond})
	defer rdb.Close()
	guard, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}

	meta := &response.StreamMeta{SessionID: "sess-dial-fail", State: response.NewStreamState()}
	chunk := []byte("data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"plain text without reserved markers\"}\n\n")
	if _, err := guard.InterceptStreamChunk(context.Background(), chunk, meta); err != nil {
		t.Fatalf("dial-fail chunk pass returned error: %v", err)
	}
	if !strings.Contains(buf.String(), "\"level\":\"WARN\"") || !strings.Contains(buf.String(), "load map failed") {
		t.Fatalf("real redis failure must still WARN, got: %s", buf.String())
	}
}
