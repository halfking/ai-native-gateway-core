package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

func TestNewCacheInjector(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	if ci == nil || ci.sessionMgr != mgr {
		t.Fatal("NewCacheInjector did not set sessionMgr")
	}
}

func TestCacheInjector_InjectCacheParams_EmptySessionID(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	body := []byte(`{"x":1}`)
	got, err := ci.InjectCacheParams(context.Background(), "", body, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("body should be unchanged")
	}
}

func TestCacheInjector_InjectCacheParams_NilCandidate(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	body := []byte(`{"x":1}`)
	got, err := ci.InjectCacheParams(context.Background(), "s", body, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("body should be unchanged")
	}
}

func TestCacheInjector_InjectCacheParams_NotSupportCache(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: false}
	got, err := ci.InjectCacheParams(context.Background(), "s", body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("body should be unchanged")
	}
}

func TestCacheInjector_InjectCacheParams_SessionNotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: true}
	got, err := ci.InjectCacheParams(context.Background(), "nonexistent", body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("body should be unchanged")
	}
}

func TestCacheInjector_InjectCacheParams_BadJSON(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`not json`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "checkpoint"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("body should be unchanged on bad JSON")
	}
}

func TestCacheInjector_InjectCacheParams_CheckpointMode(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "checkpoint"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) == string(body) {
		t.Fatal("body should be modified for checkpoint mode")
	}
}

func TestCacheInjector_InjectCacheParams_TokensModeNewMeta(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "tokens"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) == string(body) {
		t.Fatal("body should be modified for tokens mode (new meta)")
	}
}

func TestCacheInjector_InjectCacheParams_TokensModeExistingMeta(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"metadata":{"cache_control":{"type":"old"}}}`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "tokens"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) == string(body) {
		t.Fatal("body should be modified for tokens mode (existing meta)")
	}
}

func TestCacheInjector_InjectCacheParams_HeaderMode(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "header"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// header mode is a no-op
	if string(got) != string(body) {
		t.Fatal("body should be unchanged for header mode")
	}
}

// 2026-10-27 缓存标准对齐轮：anthropic-messages 候选的 "tokens" 模式改写
// 厂商真实形态——请求体顶层 cache_control（自动缓存）；此前写 metadata
// 的形态不是厂方字段（Anthropic metadata 只认 user_id ⇒ 真上游会 400）。
func TestCacheInjector_InjectCacheParams_TokensModeAnthropicUsesVendorForm(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"model":"claude-sonnet-5","messages":[]}`)
	cand := &provider.Candidate{
		SupportsPromptCache: true,
		CacheMode:           "tokens",
		Protocol:            providercatalog.ProtocolAnthropicMessages,
	}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cc, ok := obj["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		t.Fatalf("top-level cache_control = %v, want {type:ephemeral}", obj["cache_control"])
	}
	if _, exists := obj["metadata"]; exists {
		t.Fatalf("metadata must not be created for anthropic candidates, got %v", obj["metadata"])
	}
}

// 非 Anthropic 候选维持历史约定（metadata.cache_control），行为不变。
func TestCacheInjector_InjectCacheParams_TokensModeNonAnthropicKeepsLegacyMeta(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"model":"gpt-5","messages":[]}`)
	cand := &provider.Candidate{
		SupportsPromptCache: true,
		CacheMode:           "tokens",
		Protocol:            providercatalog.ProtocolOpenAICompletions,
	}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("legacy metadata path must be kept, got %v", obj["metadata"])
	}
	if cc, ok := meta["cache_control"].(map[string]any); !ok || cc["type"] != "ephemeral" {
		t.Fatalf("metadata.cache_control = %v", meta["cache_control"])
	}
	if _, exists := obj["cache_control"]; exists {
		t.Fatalf("top-level cache_control must not be written for non-anthropic candidates")
	}
}
