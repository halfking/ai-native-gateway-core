package reqprobe

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRedisStore(rdb, "llmgw:reqanom:test"), mr
}

func TestRedisStoreRoundTrip(t *testing.T) {
	s, _ := newTestRedisStore(t)
	ctx := context.Background()

	mk := func(recovered int) Record {
		return Record{
			ProviderCode: "xai", OutboundModel: "grok-4.6", ClientModel: "grok-4.6",
			Protocol: "openai-chat", Trigger: TriggerParamRejected, Param: "reasoning_effort",
			HTTPStatus: 400, ErrorKind: "client_bug", ErrorSample: "Invalid value for 'reasoning_effort'",
			RecoveredCount: recovered, LastRequestID: "req-1",
		}
	}
	// 第一次插入。
	if _, err := s.Upsert(ctx, mk(0)); err != nil {
		t.Fatal(err)
	}
	// 第二次：恢复成功。
	if _, err := s.Upsert(ctx, mk(1)); err != nil {
		t.Fatal(err)
	}
	recs, total, err := s.List(ctx, Filter{})
	if err != nil || total != 1 {
		t.Fatalf("list err=%v total=%d", err, total)
	}
	if recs[0].Occurrences != 2 || recs[0].RecoveredCount != 1 {
		t.Fatalf("merged = %+v", recs[0])
	}
	if recs[0].Day == "" || recs[0].Fingerprint == "" || recs[0].ID == 0 {
		t.Fatalf("record not stamped: %+v", recs[0])
	}

	// 学习规则。
	learned := s.LearnedParams(ctx, "xai", "grok-4.6")
	if len(learned) != 1 || learned[0] != "reasoning_effort" {
		t.Fatalf("learned = %v", learned)
	}
	// 大小写不敏感 + 其他模型不命中。
	if got := s.LearnedParams(ctx, "XAI", "GROK-4.6"); len(got) != 1 {
		t.Fatalf("case-insensitive learned = %v", got)
	}
	if got := s.LearnedParams(ctx, "xai", "other-model"); len(got) != 0 {
		t.Fatalf("other model learned = %v", got)
	}

	// 计数 + 过滤。
	counts, err := s.Counts(ctx)
	if err != nil || counts.Unresolved != 1 || counts.NewToday != 1 {
		t.Fatalf("counts = %+v err=%v", counts, err)
	}
	recID := recs[0].ID
	recs, _, _ = s.List(ctx, Filter{Trigger: "mode_mismatch"})
	if len(recs) != 0 {
		t.Fatalf("trigger filter = %d", len(recs))
	}

	// 解决后：计数清零、学习失效。
	n, err := s.Resolve(ctx, []int64{recID}, "fixed")
	if err != nil || n != 1 {
		t.Fatalf("resolve n=%d err=%v", n, err)
	}
	counts, _ = s.Counts(ctx)
	if counts.Unresolved != 0 {
		t.Fatalf("after resolve counts = %+v", counts)
	}
	if got := s.LearnedParams(ctx, "xai", "grok-4.6"); len(got) != 0 {
		t.Fatalf("learned after resolve = %v", got)
	}
}

func TestRedisStoreBatchResolveFilter(t *testing.T) {
	s, _ := newTestRedisStore(t)
	ctx := context.Background()
	for _, p := range []string{"xai", "openrouter"} {
		if _, err := s.Upsert(ctx, Record{ProviderCode: p, OutboundModel: "m",
			Trigger: TriggerUpstreamError, HTTPStatus: 400, ErrorKind: "client_bug"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.ResolveFilter(ctx, Filter{ProviderCode: "openrouter"}, "batch")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	counts, _ := s.Counts(ctx)
	if counts.Unresolved != 1 {
		t.Fatalf("unresolved = %d", counts.Unresolved)
	}
}

func TestRedisStoreResolveAllUnresolved(t *testing.T) {
	s, _ := newTestRedisStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Upsert(ctx, Record{ProviderCode: "p", OutboundModel: string(rune('a' + i)),
			Trigger: TriggerUpstreamError, HTTPStatus: 422, ErrorKind: "unsupported_feature"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.ResolveFilter(ctx, Filter{UnresolvedOnly: true}, "all")
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestRedisStoreLearningTTLWindow(t *testing.T) {
	s, _ := newTestRedisStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 21, 8, 0, 0, 0, time.Local)
	s.nowFn = func() time.Time { return base }
	if _, err := s.Upsert(ctx, Record{ProviderCode: "xai", OutboundModel: "grok-4.6",
		Trigger: TriggerParamRejected, Param: "thinking", HTTPStatus: 400,
		ErrorKind: "client_bug", RecoveredCount: 1}); err != nil {
		t.Fatal(err)
	}
	if got := s.LearnedParams(ctx, "xai", "grok-4.6"); len(got) != 1 {
		t.Fatalf("fresh rule should apply: %v", got)
	}
	// 超过 learnedTTL 后规则过期。
	s.nowFn = func() time.Time { return base.Add(25 * time.Hour) }
	if got := s.LearnedParams(ctx, "xai", "grok-4.6"); len(got) != 0 {
		t.Fatalf("expired rule still applies: %v", got)
	}
}
