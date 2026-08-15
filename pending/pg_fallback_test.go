package pending

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeSource 是 FallbackSource 的测试替身：返回预置条目并记录调用。
type fakeSource struct {
	entries    map[string]*Response // key: sessionID + "\x00" + requestID
	latest     map[string]*Response // key: sessionID；requestID 固定 latest-rid
	calls      int
	latestCalls int
	err        error
}

func (f *fakeSource) Get(ctx context.Context, sessionID, requestID string) (*Response, bool, error) {
	f.calls++
	if f.err != nil {
		return nil, false, f.err
	}
	r, ok := f.entries[sessionID+"\x00"+requestID]
	return r, ok, nil
}

func (f *fakeSource) GetLatest(ctx context.Context, sessionID string) (*Response, string, bool, error) {
	f.latestCalls++
	if f.err != nil {
		return nil, "", false, f.err
	}
	r, ok := f.latest[sessionID]
	return r, "latest-rid", ok, nil
}

// TestStore_PGSourceFallback_RedisUnavailable：doc 18 §12.1 — Redis 不可用
// （nil client）时，配置了回源 source 的 Store 必须能从 PG 投影读到状态。
func TestStore_PGSourceFallback_RedisUnavailable(t *testing.T) {
	src := &fakeSource{entries: map[string]*Response{
		"s1\x00r1": {SessionID: "s1", RequestID: "r1", Status: StatusCompleted, Body: `{"ok":true}`},
	}, latest: map[string]*Response{
		"s1": {SessionID: "s1", RequestID: "latest-rid", Status: StatusFailed, ErrorMessage: "survival_expired"},
	}}
	s := NewStoreWithFallback(nil, 0, src)

	r, found, err := s.Get(context.Background(), "s1", "r1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if r.Status != StatusCompleted || r.Body != `{"ok":true}` {
		t.Fatalf("Get returned %+v", r)
	}

	lr, rid, found, err := s.GetLatest(context.Background(), "s1")
	if err != nil || !found {
		t.Fatalf("GetLatest: found=%v err=%v", found, err)
	}
	if rid != "latest-rid" || lr.Status != StatusFailed {
		t.Fatalf("GetLatest returned rid=%q %+v", rid, lr)
	}
}

// TestStore_PGSourceFallback_UnchangedWithoutSource：未配置回源（既有
// NewStore 路径）时行为必须与 main 完全一致 — nil rdb 仍返回 ErrUnavailable。
func TestStore_PGSourceFallback_UnchangedWithoutSource(t *testing.T) {
	s := NewStore(nil, 0)
	if _, _, err := s.Get(context.Background(), "s1", "r1"); err != ErrUnavailable {
		t.Fatalf("Get err = %v, want ErrUnavailable", err)
	}
	if _, _, _, err := s.GetLatest(context.Background(), "s1"); err != ErrUnavailable {
		t.Fatalf("GetLatest err = %v, want ErrUnavailable", err)
	}
}

// TestStore_PGSourceFallback_RedisHitPreferred：Redis 投影命中时不触发
// 回源；Redis miss 时才回源（doc 18 §12.1：Redis 是加速投影，PG 是 SSoT）。
func TestStore_PGSourceFallback_RedisHitPreferred(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	cached := &Response{SessionID: "s1", RequestID: "r1", Status: StatusCompleted, Body: "from-redis"}
	src := &fakeSource{entries: map[string]*Response{
		"s1\x00r1": {SessionID: "s1", RequestID: "r1", Status: StatusCompleted, Body: "from-pg"},
	}}
	s := NewStoreWithFallback(rdb, time.Minute, src)

	if err := s.Save(context.Background(), cached); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 命中 Redis：不触碰回源
	r, found, err := s.Get(context.Background(), "s1", "r1")
	if err != nil || !found || r.Body != "from-redis" {
		t.Fatalf("Get: body=%+v found=%v err=%v", r, found, err)
	}
	lr, rid, found, err := s.GetLatest(context.Background(), "s1")
	if err != nil || !found || lr.Body != "from-redis" || rid != "r1" {
		t.Fatalf("GetLatest: body=%+v rid=%q found=%v err=%v", lr, rid, found, err)
	}
	if src.calls != 0 || src.latestCalls != 0 {
		t.Fatalf("fallback consulted on redis hit: calls=%d latestCalls=%d", src.calls, src.latestCalls)
	}

	// Redis miss（模拟投影丢失）：回源返回 PG 侧结果
	if err := s.Delete(context.Background(), "s1", "r1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	r, found, err = s.Get(context.Background(), "s1", "r1")
	if err != nil || !found || r.Body != "from-pg" {
		t.Fatalf("Get after miss: resp=%+v found=%v err=%v", r, found, err)
	}
	if src.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", src.calls)
	}

	// 两边都 miss：按未命中返回，不报错
	if _, found, err := s.Get(context.Background(), "s-nope", "r-nope"); found || err != nil {
		t.Fatalf("double miss: found=%v err=%v", found, err)
	}
}

// TestStore_PGSourceFallback_SourceErrorPropagates：回源报错必须向上
// 传播（fail closed），不得静默当作未命中。
func TestStore_PGSourceFallback_SourceErrorPropagates(t *testing.T) {
	src := &fakeSource{err: errors.New("pg down")}
	s := NewStoreWithFallback(nil, 0, src)
	if _, _, err := s.Get(context.Background(), "s1", "r1"); err == nil || err == ErrUnavailable {
		t.Fatalf("Get err = %v, want propagated pg error", err)
	}
	if _, _, _, err := s.GetLatest(context.Background(), "s1"); err == nil || err == ErrUnavailable {
		t.Fatalf("GetLatest err = %v, want propagated pg error", err)
	}
}
