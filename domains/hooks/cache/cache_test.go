package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// ---------- InMemoryStore 测试 ----------

func TestInMemoryStore_Set_Get_Hit(t *testing.T) {
	s := NewInMemoryStore()
	key := CacheKey{TenantID: "t1", Model: "gpt-4", Hash: "abc"}
	entry := &CacheEntry{
		Key:       key,
		Value:     []byte(`{"ok":true}`),
		CreatedAt: time.Now(),
		TTL:       time.Minute,
	}
	if err := s.Set(entry); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, hit, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !hit {
		t.Fatal("expected hit=true")
	}
	if string(got.Value) != `{"ok":true}` {
		t.Fatalf("value mismatch: %q", got.Value)
	}
	if s.Size() != 1 {
		t.Fatalf("expected size=1, got %d", s.Size())
	}
}

func TestInMemoryStore_Get_Miss(t *testing.T) {
	s := NewInMemoryStore()
	_, hit, err := s.Get(CacheKey{TenantID: "t1", Model: "gpt-4", Hash: "nope"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hit {
		t.Fatal("expected hit=false for missing key")
	}
}

func TestInMemoryStore_TTL_Expires(t *testing.T) {
	s := NewInMemoryStore()
	key := CacheKey{TenantID: "t1", Model: "gpt-4", Hash: "abc"}
	// 已过期（创建时间在过去）
	_ = s.Set(&CacheEntry{
		Key:       key,
		Value:     []byte("old"),
		CreatedAt: time.Now().Add(-1 * time.Hour),
		TTL:       time.Minute,
	})
	_, hit, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hit {
		t.Fatal("expected hit=false for expired entry")
	}
	if s.Size() != 0 {
		t.Fatalf("expected expired entry to be deleted, size=%d", s.Size())
	}
}

func TestInMemoryStore_Delete(t *testing.T) {
	s := NewInMemoryStore()
	key := CacheKey{TenantID: "t1", Model: "gpt-4", Hash: "abc"}
	_ = s.Set(&CacheEntry{Key: key, Value: []byte("v"), TTL: time.Minute})
	if err := s.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, hit, _ := s.Get(key)
	if hit {
		t.Fatal("expected hit=false after Delete")
	}
	// 幂等：删除不存在的 key 不报错
	if err := s.Delete(key); err != nil {
		t.Fatalf("Delete non-existent: %v", err)
	}
}

func TestInMemoryStore_NilEntry_NoOp(t *testing.T) {
	s := NewInMemoryStore()
	if err := s.Set(nil); err != nil {
		t.Fatalf("Set(nil) should be no-op, got %v", err)
	}
	if s.Size() != 0 {
		t.Fatalf("expected size=0 after Set(nil), got %d", s.Size())
	}
}

// ---------- CacheLookupHook 测试 ----------

func TestCacheLookupHook_Hit(t *testing.T) {
	store := NewInMemoryStore()
	body := []byte(`{"prompt":"hi"}`)
	hash := hashBytes(body)
	_ = store.Set(&CacheEntry{
		Key:       CacheKey{TenantID: "t1", Model: "gpt-4", Hash: hash},
		Value:     []byte(`{"cached":true}`),
		CreatedAt: time.Now(),
		TTL:       time.Minute,
	})
	h := NewCacheLookupHook(store)
	env := &domain.PipelineRequest{
		TenantID:           "t1",
		TransformedRequest: body,
		Metadata:           map[string]any{MetaKeyModel: "gpt-4"},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	hit, _ := env.Metadata[MetaKeyCacheHit].(bool)
	if !hit {
		t.Fatal("expected cache_hit=true")
	}
	if string(env.UpstreamResponse) != `{"cached":true}` {
		t.Fatalf("UpstreamResponse mismatch: %q", env.UpstreamResponse)
	}
	if env.StatusCode != 200 {
		t.Fatalf("expected StatusCode=200, got %d", env.StatusCode)
	}
}

func TestCacheLookupHook_Miss(t *testing.T) {
	store := NewInMemoryStore()
	h := NewCacheLookupHook(store)
	env := &domain.PipelineRequest{
		TenantID:           "t1",
		TransformedRequest: []byte("body"),
		Metadata:           map[string]any{MetaKeyModel: "gpt-4"},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	hit, _ := env.Metadata[MetaKeyCacheHit].(bool)
	if hit {
		t.Fatal("expected cache_hit=false")
	}
	if env.UpstreamResponse != nil {
		t.Fatalf("UpstreamResponse should be nil, got %q", env.UpstreamResponse)
	}
}

func TestCacheLookupHook_Enabled_NilBody(t *testing.T) {
	h := NewCacheLookupHook(NewInMemoryStore())
	env := &domain.PipelineRequest{TenantID: "t1"}
	if h.Enabled(context.Background(), env) {
		t.Fatal("expected disabled when TransformedRequest is nil")
	}
}

func TestCacheLookupHook_Enabled_AlreadyChecked(t *testing.T) {
	h := NewCacheLookupHook(NewInMemoryStore())
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("b"),
		Metadata:           map[string]any{MetaKeyCacheHit: false},
	}
	if h.Enabled(context.Background(), env) {
		t.Fatal("expected disabled when cache_hit already set")
	}
}

// 错误注入 Store：模拟底层故障
type errStore struct{ err error }

func (e *errStore) Get(CacheKey) (*CacheEntry, bool, error) { return nil, false, e.err }
func (e *errStore) Set(*CacheEntry) error                   { return e.err }
func (e *errStore) Delete(CacheKey) error                   { return e.err }

func TestCacheLookupHook_StoreError_Propagates(t *testing.T) {
	h := NewCacheLookupHook(&errStore{err: errors.New("boom")})
	env := &domain.PipelineRequest{
		TenantID:           "t1",
		TransformedRequest: []byte("b"),
		Metadata:           map[string]any{MetaKeyModel: "gpt-4"},
	}
	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected error from store")
	}
	// OnError 应吞掉（降级），cache_hit=false 已记录
	if onErr := h.OnError(context.Background(), env, err); onErr != nil {
		t.Fatalf("OnError should swallow (return nil) for graceful degradation, got %v", onErr)
	}
	if v, _ := env.Metadata[MetaKeyCacheHit].(bool); v {
		t.Fatal("expected cache_hit=false after error")
	}
	if _, ok := env.Metadata["cache_lookup_error"]; !ok {
		t.Fatal("expected cache_lookup_error metadata set")
	}
}

// ---------- CacheSaveHook 测试 ----------

func TestCacheSaveHook_Saves_WhenMissed(t *testing.T) {
	store := NewInMemoryStore()
	h := NewCacheSaveHook(store, time.Minute)
	body := []byte("body")
	env := &domain.PipelineRequest{
		TenantID:           "t1",
		TransformedRequest: body,
		UpstreamResponse:   []byte(`{"reply":"hello"}`),
		Metadata:           map[string]any{MetaKeyModel: "gpt-4", MetaKeyCacheHit: false},
	}
	if !h.Enabled(context.Background(), env) {
		t.Fatal("expected enabled")
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	key := CacheKey{TenantID: "t1", Model: "gpt-4", Hash: hashBytes(body)}
	got, hit, _ := store.Get(key)
	if !hit {
		t.Fatal("expected saved entry to be hit")
	}
	if string(got.Value) != `{"reply":"hello"}` {
		t.Fatalf("saved value mismatch: %q", got.Value)
	}
}

func TestCacheSaveHook_Skips_WhenHit(t *testing.T) {
	store := NewInMemoryStore()
	h := NewCacheSaveHook(store, time.Minute)
	env := &domain.PipelineRequest{
		TenantID:           "t1",
		TransformedRequest: []byte("b"),
		UpstreamResponse:   []byte(`{"r":1}`),
		Metadata:           map[string]any{MetaKeyModel: "gpt-4", MetaKeyCacheHit: true},
	}
	if h.Enabled(context.Background(), env) {
		t.Fatal("expected disabled when cache_hit=true")
	}
}

func TestCacheSaveHook_NilResponse(t *testing.T) {
	store := NewInMemoryStore()
	h := NewCacheSaveHook(store, time.Minute)
	env := &domain.PipelineRequest{
		TenantID: "t1",
		Metadata: map[string]any{MetaKeyModel: "gpt-4"},
	}
	if h.Enabled(context.Background(), env) {
		t.Fatal("expected disabled when UpstreamResponse is nil")
	}
}

func TestCacheSaveHook_StoreError_OnErrorSwallows(t *testing.T) {
	store := &errStore{err: errors.New("save boom")}
	h := NewCacheSaveHook(store, time.Minute)
	env := &domain.PipelineRequest{
		TenantID:           "t1",
		TransformedRequest: []byte("b"),
		UpstreamResponse:   []byte(`{}`),
		Metadata:           map[string]any{MetaKeyModel: "gpt-4", MetaKeyCacheHit: false},
	}
	if err := h.Execute(context.Background(), env); err == nil {
		t.Fatal("expected Execute to return store error")
	}
	if onErr := h.OnError(context.Background(), env, errors.New("x")); onErr != nil {
		t.Fatalf("OnError should swallow, got %v", onErr)
	}
	if _, ok := env.Metadata["cache_save_error"]; !ok {
		t.Fatal("expected cache_save_error metadata set")
	}
}

// ---------- CacheKey & Entry 单元测试 ----------

func TestCacheKey_IsValid(t *testing.T) {
	if (CacheKey{}).IsValid() {
		t.Fatal("empty key should be invalid")
	}
	if !(CacheKey{TenantID: "t", Model: "m", Hash: "h"}).IsValid() {
		t.Fatal("full key should be valid")
	}
}

func TestCacheKey_String(t *testing.T) {
	k := CacheKey{TenantID: "t1", Model: "gpt-4", Hash: "abc"}
	if k.String() != "t1|gpt-4|abc" {
		t.Fatalf("String mismatch: %q", k.String())
	}
}

func TestCacheEntry_IsExpired(t *testing.T) {
	e := &CacheEntry{CreatedAt: time.Now().Add(-time.Hour), TTL: time.Minute}
	if !e.IsExpired() {
		t.Fatal("expected expired")
	}
	e2 := &CacheEntry{CreatedAt: time.Now(), TTL: 0}
	if e2.IsExpired() {
		t.Fatal("TTL<=0 should mean never-expired")
	}
	if (*CacheEntry)(nil).IsExpired() {
		t.Fatal("nil entry should not be expired")
	}
}

func TestHashBytes(t *testing.T) {
	h1 := hashBytes([]byte("hello"))
	h2 := hashBytes([]byte("hello"))
	if h1 != h2 {
		t.Fatal("same input should produce same hash")
	}
	if hashBytes([]byte("")) != "" {
		t.Fatal("empty input should produce empty hash")
	}
	if h1 == hashBytes([]byte("hello!")) {
		t.Fatal("different input should produce different hash")
	}
}

// ---------- 接口断言编译期保障 ----------

func TestHookInterfaceCompliance(t *testing.T) {
	// 编译期已通过 var _ 断言；这里再加运行期 sanity check
	h1 := NewCacheLookupHook(NewInMemoryStore())
	h2 := NewCacheSaveHook(NewInMemoryStore(), time.Minute)
	if h1.Name() != "cache.lookup" {
		t.Fatalf("lookup name mismatch: %q", h1.Name())
	}
	if h2.Name() != "cache.save" {
		t.Fatalf("save name mismatch: %q", h2.Name())
	}
	if h1.Priority() != 50 || h2.Priority() != 50 {
		t.Fatal("expected priority=50")
	}
}
